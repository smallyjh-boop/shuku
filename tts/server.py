#!/usr/bin/env python3
"""书库本地语音服务：在 NAS 上用 MeloTTS（中英混读，sherpa-onnx，只用 CPU）合成听书的语音。
书库镜像里带着它（Dockerfile 的 tts 段），书库启动后自己在后台开着（server/tts_embed.go），不用另外装。

接口和 OpenAI 的语音接口一样：POST /v1/audio/speech {"input": 文字, "speed": 语速}，返回 MP3。
书库（server/tts_local.go）自动发现本机 27210 端口上的这个服务：中文、英文先用它，出错或者没开时用微软。

- 同一时间只合成一句（CPU 就那么多），正在听的那句（请求头 X-Priority: live）排在提前合成的前面
- GET /health：模型、线程数、启动时测的速度（合成 1 秒语音要几秒）
- 宿主机网络，端口 TTS_PORT（默认 27210）；线程数 TTS_THREADS（默认 4，NAS 的 CPU 是 4 核）
"""
import json
import os
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import lameenc
import numpy as np
import sherpa_onnx as so

PORT = int(os.environ.get('TTS_PORT', '27210'))
HOST = os.environ.get('TTS_HOST', '0.0.0.0')  # 书库自己开的只听本机（127.0.0.1）
THREADS = int(os.environ.get('TTS_THREADS', '4'))
MODEL = os.environ.get('TTS_MODEL_DIR', '/models/vits-melo-tts-zh_en').rstrip('/') + '/'
VERSION = os.environ.get('TTS_VERSION', 'dev')


def load():
    d = MODEL
    fsts = ','.join(d + f for f in ('date.fst', 'phone.fst', 'number.fst') if os.path.exists(d + f))
    cfg = so.OfflineTtsConfig(
        model=so.OfflineTtsModelConfig(
            vits=so.OfflineTtsVitsModelConfig(model=d + 'model.onnx', lexicon=d + 'lexicon.txt', tokens=d + 'tokens.txt', dict_dir=d + 'dict'),
            num_threads=THREADS),
        rule_fsts=fsts, max_num_sentences=2)
    return so.OfflineTts(cfg)


tts = load()

# ---------- 排队：一次合成一句，正在听的优先 ----------
cv = threading.Condition()
busy = False
live_waiting = 0


def synth(text, speed, live):
    global busy, live_waiting
    with cv:
        if live:
            live_waiting += 1
        while busy or (not live and live_waiting > 0):
            cv.wait()
        if live:
            live_waiting -= 1
        busy = True
    try:
        t0 = time.time()
        a = tts.generate(text, sid=0, speed=speed)
        dt = time.time() - t0
        note_speed(dt, len(a.samples) / max(1, a.sample_rate))
        return a, dt
    finally:
        with cv:
            busy = False
            cv.notify_all()


def to_mp3(a):
    s = np.asarray(a.samples, dtype=np.float32)
    if s.size == 0:
        s = np.zeros(int(a.sample_rate * 0.2), dtype=np.float32)  # 只有标点：给一小段静音
    pcm = (np.clip(s, -1, 1) * 32767).astype(np.int16)
    enc = lameenc.Encoder()
    enc.set_bit_rate(64)
    enc.set_in_sample_rate(a.sample_rate)
    enc.set_channels(1)
    enc.set_quality(5)
    return enc.encode(pcm.tobytes()) + enc.flush()


# 速度（合成 1 秒语音要几秒）：按真实合成的句子随时算（最近的分量大），书库按它决定先用本地还是先用微软。
# 刚启动时 NAS 可能正忙（书库升级后扫描、建索引），只测一次不准
RTF = None
_speed_lock = threading.Lock()


def note_speed(dt, audio_secs):
    global RTF
    if audio_secs < 1.0:  # 太短的句子不准
        return
    r = dt / audio_secs
    with _speed_lock:
        RTF = r if RTF is None else RTF * 0.8 + r * 0.2


# 先热身（第一次推理慢），再测一次
synth('你好。', 1.0, True)
RTF = None
synth('那是一个晴朗的早晨，城市还没有完全醒来。It was a bright cold day in April.', 1.0, True)
# 闲着的时候每 20 分钟再测一次（书库觉得慢、平时不用它时，速度也能跟着 NAS 的忙闲更新）
_last_req = [time.time()]


def _idle_probe():
    while True:
        time.sleep(1200)
        if busy or time.time() - _last_req[0] < 120:
            continue
        try:
            synth('那是一个晴朗的早晨，城市还没有完全醒来。It was a bright cold day in April.', 1.0, False)
        except Exception:  # noqa: BLE001
            pass


threading.Thread(target=_idle_probe, daemon=True).start()
print(f'shuku-tts {VERSION}: MeloTTS 中英，{THREADS} 线程，端口 {PORT}，合成 1 秒语音约 {round(RTF, 2)} 秒', flush=True)


class H(BaseHTTPRequestHandler):
    protocol_version = 'HTTP/1.1'

    def log_message(self, *a):
        pass

    def send(self, code, body, ctype='application/json; charset=utf-8'):
        self.send_response(code)
        self.send_header('Content-Type', ctype)
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path.startswith('/health'):
            self.send(200, json.dumps({'ok': True, 'app': 'shuku-tts', 'version': VERSION, 'model': 'melo-zh_en', 'langs': ['zh', 'en'],
                                       'threads': THREADS, 'rtf': round(RTF or 0, 2)}).encode())
        else:
            self.send(404, b'{"error":"not found"}')

    def do_POST(self):
        if not self.path.startswith('/v1/audio/speech'):
            self.send(404, b'{"error":"not found"}')
            return
        try:
            req = json.loads(self.rfile.read(int(self.headers.get('Content-Length') or 0)) or b'{}')
        except Exception:
            self.send(400, b'{"error":"bad json"}')
            return
        text = str(req.get('input') or '').strip()[:3000]
        if not text:
            self.send(400, b'{"error":"empty input"}')
            return
        try:
            speed = min(3.0, max(0.5, float(req.get('speed') or 1)))
        except (TypeError, ValueError):
            speed = 1.0
        live = self.headers.get('X-Priority', 'live') != 'prefetch'
        _last_req[0] = time.time()
        try:
            a, _ = synth(text, speed, live)
            self.send(200, to_mp3(a), 'audio/mpeg')
        except Exception as e:  # noqa: BLE001
            self.send(500, json.dumps({'error': str(e)}).encode())


if __name__ == '__main__':
    srv = ThreadingHTTPServer((HOST, PORT), H)
    srv.daemon_threads = True
    srv.serve_forever()
