#!/usr/bin/env python3
"""书库 Windows 版：打开就是书库的窗口（看书、听书），听书加速器在同一个程序里一起运行。

- 窗口用系统自带的 Edge 内核（WebView2，pywebview）显示书库网页，不开浏览器；配对一次，看书和加速都用这个登录。
- 加速：书库的微软语音不行（网络不好）时，要合成的听书句子交给电脑，用电脑的 CPU 跑 MeloTTS
  （和 NAS 本地语音同一个模型，sherpa-onnx），合成好传回书库存进听书包。电脑只往外连书库（拉活、交活），不用开端口。
  打开程序就在加速；窗口里（书库“设置 → 手机、手表、浏览器插件”）、托盘菜单都能开关。
- 电脑空闲时全速；有人在用（最近一分钟碰过键盘鼠标）时降速；用电池时不干活（可以改）。
- 关窗口缩到托盘接着加速，托盘“退出”才真正关掉；开机自动启动（可以关）；有新版自己升级。
- 本机设置页 http://127.0.0.1:27230 （只有这台电脑能打开）：配对、状态；安装程序用它的 /quit 关掉正在运行的程序。

书库那边的接口见 server/listen.go（/api/listen/worker/next、/done）和 server/desktop.go。安装程序是 installer.nsi，
运行环境是 make_runtime.py 做的。
"""
import ctypes
import io
import json
import os
import platform
import socket
import sys
import tarfile
import threading
import time
import traceback
import urllib.error
import urllib.parse
import urllib.request
import webbrowser
import zipfile
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

APP_DIR = os.path.dirname(os.path.abspath(__file__))
WINDOWS = sys.platform == 'win32'
UI_PORT = int(os.environ.get('SHUKU_DESKTOP_PORT', '27230'))
MODEL_NAME = 'vits-melo-tts-zh_en'
MODEL_URL = 'https://github.com/k2-fsa/sherpa-onnx/releases/download/tts-models/vits-melo-tts-zh_en.tar.bz2'


def _read(p, default=''):
    try:
        with open(p, encoding='utf-8') as f:
            return f.read().strip()
    except OSError:
        return default


VERSION = _read(os.path.join(APP_DIR, 'version.txt'), 'dev')
RUNTIME = _read(os.path.join(APP_DIR, 'runtime.txt'), '')
ICON = os.path.join(APP_DIR, 'shuku.ico')
SELFTEST = os.environ.get('SHUKU_DESKTOP_SELFTEST') == '1'

if WINDOWS:
    HOME = os.path.join(os.environ.get('LOCALAPPDATA') or os.path.expanduser('~'), 'Shuku')
else:
    HOME = os.environ.get('SHUKU_DESKTOP_HOME') or os.path.expanduser('~/.shuku-desktop')
os.makedirs(HOME, exist_ok=True)
CFG_PATH = os.path.join(HOME, 'desktop.json')
LOG_PATH = os.path.join(HOME, 'desktop.log')
MODEL_DIR = os.path.join(HOME, 'models', MODEL_NAME)


def log(*a):
    line = time.strftime('%m-%d %H:%M:%S ') + ' '.join(str(x) for x in a)
    try:
        if os.path.exists(LOG_PATH) and os.path.getsize(LOG_PATH) > 1 << 20:
            os.replace(LOG_PATH, LOG_PATH + '.1')
        with open(LOG_PATH, 'a', encoding='utf-8') as f:
            f.write(line + '\n')
    except OSError:
        pass
    if sys.stdout:
        try:
            print(line, flush=True)
        except Exception:
            pass


# ---------- 设置 ----------

cfg_lock = threading.Lock()
cfg = {'token': '', 'urls': [], 'server_name': '', 'accel': True, 'battery': False, 'autostart': True, 'threads': 0}


def load_cfg():
    try:
        with open(CFG_PATH, encoding='utf-8') as f:
            cfg.update(json.load(f))
    except (OSError, ValueError):
        pass


def save_cfg():
    with cfg_lock:
        tmp = CFG_PATH + '.tmp'
        with open(tmp, 'w', encoding='utf-8') as f:
            json.dump(cfg, f, ensure_ascii=False, indent=1)
        os.replace(tmp, CFG_PATH)


def bundled_server():
    """下载桌面版时书库写进来的地址（app/server.json）"""
    try:
        return json.loads(_read(os.path.join(APP_DIR, 'server.json'), '{}'))
    except ValueError:
        return {}


def candidate_urls():
    out = []
    s = bundled_server()
    for u in list(cfg.get('urls') or []) + [s.get('lan_url'), s.get('remote_url'), s.get('origin')]:
        u = (u or '').strip().rstrip('/')
        if u and u not in out:
            out.append(u)
    return out


def device_name():
    return '电脑 · ' + (platform.node() or socket.gethostname() or 'Windows')


# ---------- 和书库通信 ----------

class Unauthorized(Exception):
    pass


class Conn:
    def __init__(self):
        self.base = ''

    def _try(self, base, method, path, body, ctype, timeout, auth):
        req = urllib.request.Request(base + path, data=body, method=method)
        if body is not None and ctype:
            req.add_header('Content-Type', ctype)
        if auth and cfg.get('token'):
            req.add_header('Authorization', 'Bearer ' + cfg['token'])
        req.add_header('User-Agent', 'ShukuDesktop/' + VERSION)
        try:
            with urllib.request.urlopen(req, timeout=timeout) as r:
                return r.status, r.read()
        except urllib.error.HTTPError as e:
            return e.code, e.read()

    def request(self, method, path, data=None, raw=None, ctype=None, timeout=30, auth=True):
        body = raw
        if data is not None:
            body = json.dumps(data, ensure_ascii=False).encode('utf-8')
            ctype = 'application/json'
        urls = candidate_urls()
        if self.base in urls:
            urls.remove(self.base)
            urls.insert(0, self.base)
        err = None
        for u in urls:
            try:
                code, b = self._try(u, method, path, body, ctype, timeout, auth)
            except (OSError, urllib.error.URLError) as e:
                err = e
                continue
            if self.base != u:
                log('书库地址：', u)
                self.base = u
            if code == 401 and auth:
                raise Unauthorized(_msg(b) or '登录已失效，请重新配对')
            return code, b
        raise OSError('连不上书库' + (f'（{err}）' if err else '，还没有书库地址'))

    def json(self, method, path, data=None, timeout=30, auth=True):
        code, b = self.request(method, path, data=data, timeout=timeout, auth=auth)
        if code >= 400:
            raise OSError(_msg(b) or f'书库返回 {code}')
        return json.loads(b.decode('utf-8') or '{}')


def _msg(b):
    try:
        return json.loads(b.decode('utf-8')).get('error', '')
    except Exception:
        return ''


conn = Conn()

# ---------- 电脑的状态（Windows） ----------


def on_battery():
    if not WINDOWS:
        return False

    class SPS(ctypes.Structure):
        _fields_ = [('ACLineStatus', ctypes.c_byte), ('BatteryFlag', ctypes.c_byte), ('BatteryLifePercent', ctypes.c_byte),
                    ('SystemStatusFlag', ctypes.c_byte), ('BatteryLifeTime', ctypes.c_ulong), ('BatteryFullLifeTime', ctypes.c_ulong)]
    s = SPS()
    if not ctypes.windll.kernel32.GetSystemPowerStatus(ctypes.byref(s)):
        return False
    return s.ACLineStatus == 0


def idle_seconds():
    """多久没碰键盘鼠标了"""
    if not WINDOWS:
        return 3600

    class LII(ctypes.Structure):
        _fields_ = [('cbSize', ctypes.c_uint), ('dwTime', ctypes.c_uint)]
    li = LII()
    li.cbSize = ctypes.sizeof(LII)
    if not ctypes.windll.user32.GetLastInputInfo(ctypes.byref(li)):
        return 3600
    return ((ctypes.windll.kernel32.GetTickCount() - li.dwTime) & 0xFFFFFFFF) / 1000.0


def lower_priority():
    """比别的程序低一档：电脑上的事优先"""
    try:
        if WINDOWS:
            ctypes.windll.kernel32.SetPriorityClass(ctypes.windll.kernel32.GetCurrentProcess(), 0x4000)  # BELOW_NORMAL
        else:
            os.nice(5)
    except Exception:
        pass


def set_autostart(on):
    if not WINDOWS:
        return
    import winreg
    key = r'Software\Microsoft\Windows\CurrentVersion\Run'
    exe = sys.executable
    if exe.lower().endswith('python.exe'):
        exe = exe[:-10] + 'pythonw.exe'
    with winreg.OpenKey(winreg.HKEY_CURRENT_USER, key, 0, winreg.KEY_SET_VALUE) as k:
        if on:
            winreg.SetValueEx(k, 'ShukuDesktop', 0, winreg.REG_SZ, f'"{exe}" "{os.path.abspath(__file__)}" --hidden')
        else:
            try:
                winreg.DeleteValue(k, 'ShukuDesktop')
            except OSError:
                pass


# ---------- 状态（托盘、设置页显示） ----------

state = {'text': '启动中…', 'done': 0, 'today': 0, 'day': '', 'rtf': None, 'busy': False, 'model': '',
         'last_error': '', 'update': '', 'window': '', 'tray': False, 'page_title': ''}
state_lock = threading.Lock()
listeners = []


def set_state(**kw):
    with state_lock:
        changed = any(state.get(k) != v for k, v in kw.items())
        state.update(kw)
    if changed:
        for f in listeners:
            try:
                f()
            except Exception:
                pass


# ---------- 语音模型 ----------

tts = None


def ensure_model():
    """模型不在电脑上就下载：先从书库（NAS 上就有一份），书库没有时从 GitHub"""
    if os.path.exists(os.path.join(MODEL_DIR, 'model.onnx')) and os.path.exists(os.path.join(MODEL_DIR, '.complete')):
        return
    os.makedirs(MODEL_DIR, exist_ok=True)
    try:
        lst = conn.json('GET', '/api/listen/worker/model')
        files = lst.get('files') or []
        total = sum(f['size'] for f in files) or 1
        got = 0
        for f in files:
            dest = os.path.join(MODEL_DIR, *f['name'].split('/'))
            if os.path.exists(dest) and os.path.getsize(dest) == f['size']:
                got += f['size']
                continue
            os.makedirs(os.path.dirname(dest), exist_ok=True)
            q = urllib.parse.urlencode({'name': f['name']})
            code, b = conn.request('GET', '/api/listen/worker/model/file?' + q, timeout=600)
            if code != 200 or len(b) != f['size']:
                raise OSError(f'模型文件 {f["name"]} 下载不完整')
            with open(dest + '.part', 'wb') as out:
                out.write(b)
            os.replace(dest + '.part', dest)
            got += f['size']
            set_state(text=f'下载语音模型 {got * 100 // total}%')
    except Unauthorized:
        raise
    except Exception as e:
        log('从书库下载模型失败，改从 GitHub 下载：', e)
        set_state(text='下载语音模型（GitHub）…')
        with urllib.request.urlopen(MODEL_URL, timeout=600) as r:
            data = r.read()
        with tarfile.open(fileobj=io.BytesIO(data), mode='r:bz2') as t:
            for m in t.getmembers():
                if not m.isfile() or m.name.endswith('model.int8.onnx'):
                    continue
                parts = m.name.split('/')[1:]
                if not parts or '..' in parts:
                    continue
                dest = os.path.join(MODEL_DIR, *parts)
                os.makedirs(os.path.dirname(dest), exist_ok=True)
                with t.extractfile(m) as src, open(dest, 'wb') as out:
                    out.write(src.read())
    with open(os.path.join(MODEL_DIR, '.complete'), 'w') as f:
        f.write('ok')


def threads():
    n = int(cfg.get('threads') or 0)
    if n <= 0:
        n = max(1, min(8, (os.cpu_count() or 2) - 1))
    return n


def load_tts():
    global tts
    if tts is not None:
        return tts
    ensure_model()
    set_state(text='加载语音模型…')
    import sherpa_onnx as so
    d = MODEL_DIR + os.sep
    fsts = ','.join(d + f for f in ('date.fst', 'phone.fst', 'number.fst') if os.path.exists(d + f))
    c = so.OfflineTtsConfig(
        model=so.OfflineTtsModelConfig(
            vits=so.OfflineTtsVitsModelConfig(model=d + 'model.onnx', lexicon=d + 'lexicon.txt', tokens=d + 'tokens.txt', dict_dir=d + 'dict'),
            num_threads=threads()),
        rule_fsts=fsts, max_num_sentences=2)
    tts = so.OfflineTts(c)
    set_state(model=f'MeloTTS 中英（{threads()} 线程）')
    return tts


def to_mp3(samples, rate):
    import lameenc
    import numpy as np
    s = np.asarray(samples, dtype=np.float32)
    if s.size == 0:
        s = np.zeros(int(rate * 0.2), dtype=np.float32)
    pcm = (np.clip(s, -1, 1) * 32767).astype(np.int16)
    enc = lameenc.Encoder()
    enc.set_bit_rate(64)
    enc.set_in_sample_rate(rate)
    enc.set_channels(1)
    enc.set_quality(5)
    return enc.encode(pcm.tobytes()) + enc.flush()


def synth(text):
    t = load_tts()
    t0 = time.time()
    a = t.generate(text, sid=0, speed=1.0)  # 听书包一律 1 倍速，语速由播放的设备调
    dt = time.time() - t0
    secs = len(a.samples) / max(1, a.sample_rate)
    if secs >= 1:
        r = dt / secs
        old = state.get('rtf')
        set_state(rtf=r if old is None else old * 0.8 + r * 0.2)
    return to_mp3(a.samples, a.sample_rate), dt


# ---------- 干活 ----------

stop_event = threading.Event()
wake = threading.Event()


def wait(sec):
    wake.wait(sec)
    wake.clear()


def worker():
    lower_priority()
    name = device_name()
    while not stop_event.is_set():
        try:
            if not cfg.get('token'):
                set_state(text='还没连接书库：打开设置填配对码')
                wait(5)
                continue
            if not cfg.get('accel', True):
                set_state(text='加速已暂停')
                wait(10)
                continue
            if on_battery() and not cfg.get('battery'):
                set_state(text='在用电池，不加速（插上电源后继续）')
                wait(30)
                continue
            load_tts()
            busy = idle_seconds() < 60
            r = conn.json('POST', '/api/listen/worker/next', {
                'name': name, 'rtf': state.get('rtf') or 0, 'version': VERSION, 'busy': busy, 'max': 3 if busy else 10})
            tasks = r.get('tasks') or []
            set_state(busy=busy, last_error='')
            if not tasks:
                set_state(text='已连接，等书库派活' + ('（电脑在用，降速）' if busy else ''))
                wait(float(r.get('wait') or 5))
                continue
            for t in tasks:
                if stop_event.is_set() or not cfg.get('accel', True):
                    break
                set_state(text='正在合成听书包' + ('（电脑在用，降速）' if busy else ''))
                mp3, dt = synth(t['text'])
                q = urllib.parse.urlencode({'id': t['id'], 'name': name})
                code, b = conn.request('POST', '/api/listen/worker/done?' + q, raw=mp3, ctype='audio/mpeg', timeout=60)
                if code < 400:
                    day = time.strftime('%Y-%m-%d')
                    set_state(done=state['done'] + 1, today=(state['today'] if state['day'] == day else 0) + 1, day=day)
                # 电脑在用：干一句歇一句（差不多一半的速度）
                if busy or idle_seconds() < 60:
                    busy = True
                    time.sleep(min(dt, 15))
        except Unauthorized as e:
            log('需要重新配对：', e)
            cfg['token'] = ''
            save_cfg()
            set_state(text='书库登录失效，请重新配对', last_error=str(e))
        except Exception as e:
            log('出错：', e, traceback.format_exc(limit=3))
            set_state(text='连不上书库，稍后重试', last_error=str(e))
            wait(30)


# ---------- 配对、更新 ----------


def pair(address, code):
    urls = []
    address = (address or '').strip().rstrip('/')
    if address:
        if '://' not in address:
            address = 'http://' + address
        urls.append(address)
    for u in candidate_urls():
        if u not in urls:
            urls.append(u)
    err = None
    for u in urls:
        try:
            req = urllib.request.Request(u + '/api/pair/claim', method='POST',
                                         data=json.dumps({'code': code, 'device': device_name()}).encode('utf-8'),
                                         headers={'Content-Type': 'application/json'})
            with urllib.request.urlopen(req, timeout=15) as r:
                info = json.loads(r.read().decode('utf-8'))
        except urllib.error.HTTPError as e:
            raise OSError(_msg(e.read()) or f'配对失败（{e.code}）')
        except OSError as e:
            err = e
            continue
        cfg['token'] = info['token']
        cfg['server_name'] = info.get('name', '')
        cfg['urls'] = [x for x in [u, info.get('lan_url'), info.get('remote_url'), info.get('origin')] if x]
        save_cfg()
        conn.base = u
        wake.set()
        log('已连接书库：', u)
        return info
    raise OSError('连不上书库' + (f'：{err}' if err else ''))


def check_update(auto=True):
    """有新版就换上：运行环境没变只换程序（几十 KB），变了就下载安装程序静默重装；然后重新打开"""
    try:
        r = conn.json('GET', '/api/clients', auth=False)
    except Exception as e:
        return f'检查更新失败：{e}'
    d = r.get('desktop') or {}
    v = d.get('version')
    if not v or v == VERSION or VERSION == 'dev':
        set_state(update='')
        return '已经是最新版' + ('' if VERSION != 'dev' else '（开发版）')
    set_state(update=v)
    # 运行环境变了（或者是 1.0.0.32 的 zip 版，没有窗口要用的库）：下载安装程序重新安装
    import importlib.util
    old_zip = WINDOWS and importlib.util.find_spec('webview') is None
    if d.get('url') and d.get('runtime') and (old_zip or (RUNTIME and d['runtime'] != RUNTIME)):
        code, b = conn.request('GET', d['url'], auth=False, timeout=900)
        if code != 200 or b[:2] != b'MZ':
            return '下载新版安装程序失败'
        import subprocess
        import tempfile
        exe = os.path.join(tempfile.gettempdir(), 'shuku-setup.exe')
        with open(exe, 'wb') as f:
            f.write(b)
        log('运行环境有更新，重新安装', v)
        # 安装程序会先让这个程序退出（/quit），装完再打开
        subprocess.Popen([exe, '/S'], close_fds=True, creationflags=0x00000008 if WINDOWS else 0)
        return '正在安装新版 ' + v
    code, b = conn.request('GET', d.get('app_url') or '/dl/shuku-desktop.zip?part=app', auth=False, timeout=120)
    if code != 200:
        return '下载新版失败'
    with zipfile.ZipFile(io.BytesIO(b)) as z:
        for n in z.namelist():
            if not n.startswith('app/') or n.endswith('/') or '..' in n:
                continue
            dest = os.path.join(APP_DIR, n[4:])
            with open(dest + '.new', 'wb') as f:
                f.write(z.read(n))
            os.replace(dest + '.new', dest)
    log('已更新到', v, '，重新启动')
    threading.Timer(1.5, restart).start()  # 先把结果回给设置页
    return '已更新到 ' + v + '，马上重新启动'


def restart():
    import subprocess
    stop_event.set()
    try:
        if tray:
            tray.stop()
    except Exception:
        pass
    flags = 0x00000008 if WINDOWS else 0  # DETACHED_PROCESS
    env = dict(os.environ, SHUKU_DESKTOP_RESTART='1')  # 新的等这个退出、端口空出来再开
    subprocess.Popen([sys.executable, os.path.abspath(__file__)], cwd=APP_DIR, creationflags=flags, close_fds=True, env=env)
    os._exit(0)


def updater():
    wait_s = 120
    while not stop_event.is_set():
        if stop_event.wait(wait_s):
            return
        wait_s = 6 * 3600
        if cfg.get('token') or candidate_urls():
            log(check_update())


# ---------- 设置页（本机网页） ----------

PAGE = """<!doctype html><html lang="zh"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>书库</title><style>
:root{--bg:#f7f7f5;--fg:#222;--card:#fff;--muted:#777;--acc:#2f6f8f;--line:#e3e3e0}
@media (prefers-color-scheme:dark){:root{--bg:#161616;--fg:#e8e8e8;--card:#202020;--muted:#999;--acc:#6fb2d2;--line:#333}}
body{margin:0;background:var(--bg);color:var(--fg);font:15px/1.6 system-ui,"Microsoft YaHei",sans-serif}
main{max-width:620px;margin:0 auto;padding:24px 16px}h1{font-size:22px;margin:0 0 4px}
.card{background:var(--card);border:1px solid var(--line);border-radius:12px;padding:16px;margin:14px 0}
.muted{color:var(--muted);font-size:13px}.big{font-size:17px;font-weight:600}
input[type=text]{width:100%;box-sizing:border-box;padding:9px 10px;border:1px solid var(--line);border-radius:8px;background:var(--bg);color:var(--fg);font-size:15px;margin:4px 0 10px}
button{background:var(--acc);color:#fff;border:0;border-radius:8px;padding:9px 16px;font-size:15px;cursor:pointer;margin:4px 6px 4px 0}
button.q{background:transparent;color:var(--acc);border:1px solid var(--acc)}label{display:block;margin:8px 0}
.row{display:flex;gap:8px;flex-wrap:wrap;align-items:center}#msg{min-height:1.4em;color:var(--acc)}
</style></head><body><main>
<h1>书库</h1><div class="muted">Windows 版 <span id="ver"></span> · 看书、听书加速</div>
<div class="card"><div class="big" id="text">…</div><div class="muted" id="sub"></div>
<div class="row" style="margin-top:8px"><button id="open">进入书库</button><button class="q" id="upd">检查更新</button></div></div>
<div class="card" id="paircard"><div class="big">连接书库</div>
<div class="muted">在书库网页“设置 → 手机、手表、浏览器插件”里点“显示配对二维码”，把配对码填到下面。</div>
<label>书库地址<input type="text" id="addr" placeholder="http://192.168.x.x:27200"></label>
<label>配对码<input type="text" id="code" placeholder="8 位配对码" autocomplete="off"></label>
<button id="pair">连接</button></div>
<div class="card"><div class="big">听书加速</div>
<label><input type="checkbox" id="accel"> 帮书库合成听书包（加速）</label>
<label><input type="checkbox" id="battery"> 用电池时也加速（笔记本）</label>
<label><input type="checkbox" id="autostart"> 开机自动启动</label>
<div class="muted">电脑空闲时全速；最近一分钟用过键盘鼠标时降到一半左右；程序优先级比别的程序低。关掉窗口会缩到右下角托盘里接着加速。</div></div>
<div id="msg"></div>
<script>
const $=id=>document.getElementById(id)
async function post(p,b){const r=await fetch(p,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(b||{})});const j=await r.json();if(!r.ok)throw new Error(j.error||r.status);return j}
let first=true
async function refresh(){try{const s=await (await fetch('/status')).json();$('ver').textContent=s.version;$('text').textContent=s.text
let sub=[];if(s.server)sub.push('书库：'+s.server+(s.url?'（'+s.url+'）':''));if(s.model)sub.push(s.model);if(s.rtf)sub.push('速度：合成 1 秒语音要 '+s.rtf.toFixed(2)+' 秒');sub.push('今天合成 '+s.today+' 句');if(s.update)sub.push('有新版 '+s.update);if(s.last_error)sub.push('上次出错：'+s.last_error)
$('sub').textContent=sub.join(' · ');$('open').style.display=s.paired?'':'none'
if(first){first=false;$('paircard').style.display=s.paired?'none':'';$('addr').value=s.default_url||'';for(const k of ['accel','battery','autostart'])$(k).checked=!!s[k]}}catch(e){$('text').textContent='书库程序没有在运行'}}
for(const k of ['accel','battery','autostart'])$(k).onchange=async e=>{try{await post('/settings',{[k]:e.target.checked});$('msg').textContent='已保存'}catch(err){$('msg').textContent=err.message}}
$('pair').onclick=async()=>{$('msg').textContent='连接中…';try{const r=await post('/pair',{address:$('addr').value,code:$('code').value});$('msg').textContent='已连接：'+(r.name||'书库')+'，正在打开…';$('paircard').style.display='none';refresh();post('/open')}catch(e){$('msg').textContent=e.message}}
$('open').onclick=()=>post('/open')
$('upd').onclick=async()=>{$('msg').textContent='检查中…';try{const r=await post('/update');$('msg').textContent=r.message}catch(e){$('msg').textContent=e.message}}
refresh();setInterval(refresh,3000)
</script></main></body></html>"""


def status():
    with state_lock:
        s = dict(state)
    urls = candidate_urls()
    s.update(version=VERSION, runtime=RUNTIME, paired=bool(cfg.get('token')),
             server=cfg.get('server_name') or bundled_server().get('name', ''),
             url=conn.base, default_url=urls[0] if urls else '', accel=cfg.get('accel', True),
             battery=cfg.get('battery', False), autostart=cfg.get('autostart', True))
    return s


def set_option(k, v):
    if k not in ('accel', 'battery', 'autostart'):
        raise ValueError('没有这个设置')
    cfg[k] = bool(v)
    if k == 'autostart':
        set_autostart(cfg[k])
    save_cfg()
    wake.set()
    for f in listeners:
        try:
            f()
        except Exception:
            pass


class UI(BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def send(self, code, body, ctype='application/json; charset=utf-8'):
        if isinstance(body, (dict, list)):
            body = json.dumps(body, ensure_ascii=False)
        b = body.encode('utf-8') if isinstance(body, str) else body
        self.send_response(code)
        self.send_header('Content-Type', ctype)
        self.send_header('Content-Length', str(len(b)))
        self.send_header('Cache-Control', 'no-store')
        self.end_headers()
        self.wfile.write(b)

    def local(self):
        # 只认本机打开的设置页：别的网站不能偷偷让这台电脑去连别的服务器
        ok = (f'127.0.0.1:{UI_PORT}', f'localhost:{UI_PORT}')
        if self.headers.get('Host') not in ok:
            return False
        o = self.headers.get('Origin')
        return o is None or o in ('http://' + h for h in ok)

    def do_GET(self):
        if not self.local():
            return self.send(403, {'error': 'forbidden'})
        if self.path == '/':
            return self.send(200, PAGE, 'text/html; charset=utf-8')
        if self.path == '/status':
            return self.send(200, status())
        self.send(404, {'error': 'not found'})

    def do_POST(self):
        if not self.local():
            return self.send(403, {'error': 'forbidden'})
        try:
            n = int(self.headers.get('Content-Length') or 0)
            body = json.loads(self.rfile.read(n).decode('utf-8') or '{}') if n else {}
        except ValueError:
            body = {}
        try:
            if self.path == '/pair':
                code = ''.join(str(body.get('code', '')).split()).upper()
                if not code:
                    return self.send(400, {'error': '请填配对码'})
                info = pair(body.get('address', ''), code)
                return self.send(200, {'ok': True, 'name': info.get('name', '')})
            if self.path == '/settings':
                for k in ('accel', 'battery', 'autostart'):
                    if k in body:
                        set_option(k, body[k])
                return self.send(200, {'ok': True})
            if self.path == '/open':
                threading.Thread(target=open_library, daemon=True).start()
                return self.send(200, {'ok': True})
            if self.path == '/show':
                show_window()
                return self.send(200, {'ok': True})
            if self.path == '/update':
                return self.send(200, {'message': check_update(auto=False)})
            if self.path == '/quit':
                self.send(200, {'ok': True})
                threading.Timer(0.3, quit_app).start()
                return
            if self.path == '/selftest' and SELFTEST:
                mp3, dt = synth('那是一个晴朗的早晨。It was a bright day.')
                return self.send(200, {'ok': len(mp3) > 1000, 'bytes': len(mp3), 'secs': round(dt, 2), 'rtf': state.get('rtf')})
        except Exception as e:
            return self.send(400, {'error': str(e)})
        self.send(404, {'error': 'not found'})


ui_server = None
tray = None
window = None
quitting = False


def local_page():
    return f'http://127.0.0.1:{UI_PORT}/'


def library_url():
    """能连上的书库地址（不用登录的 /api/ping 试一下）"""
    urls = candidate_urls()
    if conn.base in urls:
        urls.remove(conn.base)
        urls.insert(0, conn.base)
    for u in urls:
        try:
            with urllib.request.urlopen(u + '/api/ping', timeout=4) as r:
                if b'shuku' in r.read(200):
                    conn.base = u
                    return u
        except Exception:
            continue
    return ''


def open_library():
    """窗口里打开书库（还没配对、连不上时打开本机设置页）"""
    u = library_url() if cfg.get('token') else ''
    target = (u + '/') if u else local_page()
    if window is None:
        webbrowser.open(target)
        return
    show_window()
    try:
        window.load_url(target)
    except Exception as e:
        log('打开书库失败：', e)


def open_settings():
    if window is None:
        webbrowser.open(local_page())
        return
    show_window()
    window.load_url(local_page())


def show_window():
    if window is None:
        open_settings() if not cfg.get('token') else open_library()
        return
    try:
        window.show()
        window.restore()
    except Exception:
        pass


def quit_app():
    global quitting
    quitting = True
    stop_event.set()
    wake.set()
    try:
        if tray:
            tray.stop()
    except Exception:
        pass
    try:
        if window is not None:
            window.destroy()
    except Exception:
        pass
    if window is None:
        os._exit(0)
    threading.Timer(5, lambda: os._exit(0)).start()  # 窗口没关干净也要退出


# ---------- 窗口里的书库网页能调用的（window.pywebview.api，书库“设置”里显示这台电脑的加速开关） ----------

class Api:
    def state(self):
        return status()

    def set_option(self, key, value):
        set_option(key, value)
        return status()

    def open_settings(self):
        threading.Thread(target=open_settings, daemon=True).start()

    def check_update(self):
        return check_update(auto=False)


def on_loaded():
    """页面打开了：书库的网页还没登录就用配对时拿到的登录"""
    try:
        url = window.get_current_url() or ''
        set_state(window=url)
        tok = cfg.get('token')
        if tok and not url.startswith(local_page()) and any(url.startswith(u) for u in candidate_urls()):
            t = json.dumps(tok)
            window.evaluate_js(f"if(!localStorage.getItem('shuku.token')){{localStorage.setItem('shuku.token',{t});location.reload()}}")
        set_state(page_title=str(window.evaluate_js('document.title') or ''))
        time.sleep(2)
        set_state(bridge=bool(window.evaluate_js('!!(window.pywebview && window.pywebview.api && window.pywebview.api.state)')))
    except Exception as e:
        log('页面打开后出错：', e)


def on_closing():
    """关窗口：缩到托盘接着加速（托盘“退出”才真正关掉）"""
    if quitting or tray is None:
        stop_event.set()
        return True
    try:
        window.hide()
        if not cfg.get('hint_tray'):
            cfg['hint_tray'] = True
            save_cfg()
            tray.notify('书库还在右下角托盘里帮你加速。要完全退出，右键托盘图标选“退出”。', '书库')
    except Exception:
        pass
    return False


def on_shown():
    set_state(shown=True)
    # 窗口和任务栏用书库的图标（不然是 Python 的）
    try:
        import clr  # noqa: F401
        from System import Action
        from System.Drawing import Icon as DIcon
        f = window.native
        f.Invoke(Action(lambda: setattr(f, 'Icon', DIcon(ICON))))
        set_state(icon=True)
    except Exception as e:
        log('设窗口图标失败：', e)


def title_loop():
    """窗口标题上显示加速的状态"""
    last = ''
    while not stop_event.wait(3):
        if window is None:
            return
        t = '书库'
        if cfg.get('token'):
            t += ' · ' + ('加速已暂停' if not cfg.get('accel', True) else
                          f'加速中（今天 {state["today"]} 句）' if state['text'].startswith('正在合成') else '听书加速开着')
        if t != last:
            last = t
            try:
                window.set_title(t)
            except Exception:
                pass


# ---------- 托盘 ----------


def tray_icon_image(active):
    from PIL import Image, ImageDraw
    try:
        img = Image.open(ICON).convert('RGBA').resize((64, 64))
    except Exception:
        img = Image.new('RGBA', (64, 64), (0, 0, 0, 0))
        ImageDraw.Draw(img).rounded_rectangle((6, 8, 58, 56), radius=8, fill=(47, 111, 143, 255))
    if not active:
        img = img.convert('LA').convert('RGBA')  # 加速关着：灰色
    return img


def make_tray():
    import pystray
    from pystray import Menu, MenuItem as Item

    def toggle_accel(icon, item):
        set_option('accel', not cfg.get('accel', True))

    def toggle_autostart(icon, item):
        set_option('autostart', not cfg.get('autostart', True))

    icon = pystray.Icon('shuku', tray_icon_image(True), '书库', Menu(
        Item('打开书库', lambda i, it: threading.Thread(target=show_window, daemon=True).start(), default=True),
        Item('连接和加速设置', lambda i, it: threading.Thread(target=open_settings, daemon=True).start()),
        Menu.SEPARATOR,
        Item('帮书库加速', toggle_accel, checked=lambda it: cfg.get('accel', True)),
        Item('开机自动启动', toggle_autostart, checked=lambda it: cfg.get('autostart', True)),
        Item('检查更新', lambda i, it: threading.Thread(target=lambda: icon.notify(check_update(auto=False), '书库'), daemon=True).start()),
        Menu.SEPARATOR,
        Item('退出', lambda i, it: threading.Thread(target=quit_app, daemon=True).start())))

    def refresh():
        try:
            icon.icon = tray_icon_image(bool(cfg.get('token')) and cfg.get('accel', True))
            icon.title = ('书库 · ' + state['text'])[:120]
        except Exception:
            pass
    listeners.append(refresh)
    return icon


def main():
    global ui_server, tray, window
    load_cfg()
    restarting = os.environ.pop('SHUKU_DESKTOP_RESTART', '') == '1'
    for k in range(30 if restarting else 1):
        try:
            ui_server = ThreadingHTTPServer(('127.0.0.1', UI_PORT), UI)
            break
        except OSError:
            if k + 1 < (30 if restarting else 1):
                time.sleep(0.5)
    if ui_server is None:
        # 已经开着一个：让它把窗口显示出来
        if not restarting:
            try:
                urllib.request.urlopen(urllib.request.Request(local_page() + 'show', data=b'{}', method='POST'), timeout=3)
            except Exception:
                webbrowser.open(local_page())
        return
    log('书库 Windows 版', VERSION, '启动')
    if WINDOWS:
        try:
            ctypes.windll.shell32.SetCurrentProcessExplicitAppUserModelID('Shuku.Desktop')
        except Exception:
            pass
    threading.Thread(target=ui_server.serve_forever, daemon=True).start()
    if cfg.get('autostart', True):
        try:
            set_autostart(True)
        except Exception as e:
            log('设开机启动失败：', e)
    threading.Thread(target=worker, daemon=True).start()
    threading.Thread(target=updater, daemon=True).start()
    if os.environ.get('SHUKU_DESKTOP_NO_TRAY') != '1':
        try:
            tray = make_tray()
            tray.run_detached()
            set_state(tray=True)
        except Exception as e:
            log('托盘图标出不来：', e)
            tray = None
    # 开机自动启动时（带 --hidden）只在托盘里加速，不弹窗口
    hidden = '--hidden' in sys.argv and tray is not None
    try:
        if os.environ.get('SHUKU_DESKTOP_NO_WINDOW') == '1':
            raise RuntimeError('不开窗口')
        import webview
        u = library_url() if cfg.get('token') else ''
        window = webview.create_window('书库', (u + '/') if u else local_page(), js_api=Api(),
                                       width=1280, height=860, min_size=(420, 500), hidden=hidden, text_select=True)
        window.events.loaded += on_loaded
        window.events.closing += on_closing
        window.events.shown += on_shown
        threading.Thread(target=title_loop, daemon=True).start()
        os.makedirs(os.path.join(HOME, 'webview'), exist_ok=True)
        webview.start(private_mode=False, storage_path=os.path.join(HOME, 'webview'))
        # 窗口关了（退出）
        quit_app()
    except Exception as e:
        window = None
        log('窗口打不开，改用浏览器：', e)
        if not hidden and os.environ.get('SHUKU_DESKTOP_NO_WINDOW') != '1':
            open_library() if cfg.get('token') else open_settings()
        stop_event.wait()
        os._exit(0)


if __name__ == '__main__':
    main()
