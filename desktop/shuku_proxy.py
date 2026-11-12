"""书库 Windows 版的本机代理：窗口打开 http://localhost:27231 ，这里转给书库（内网、Tailscale 哪个通用哪个）。

为什么要经过它：
- 离线看书：浏览器只在 https 或 localhost 下才能把书存进本机（Service Worker + Cache Storage，web/js/offline.js、
  web/sw.js），NAS 的内网地址是 http，直接打开存不了。经 localhost 打开，书的详情里就有“离线可读”，断网也能看。
- 听书语音存在电脑上：听书播放器要的每一句语音（/api/books/{id}/listen/{n}/{i}）经过这里时存到
  %LOCALAPPDATA%\\Shuku\\listen（最多 1GB，最久没用的先删），断网、NAS 关机也能接着听存好的；
  听书稿（/listen、/listen/{n}）也存一份。播放器在 Windows 版里会往后取 1 小时（web/js/listen.js 的 deskFill）。
- 换网络：内网不通时自动换 Tailscale，窗口里的地址不变，登录、设置、离线存的书都在。

连不上书库时，别的请求直接断开连接（不回答），网页当成断网：Service Worker 用存好的书回答。
只听本机，只认 Host 是 localhost:27231 / 127.0.0.1:27231 的请求（别的网站借 DNS 改指向也进不来）。
"""
import hashlib
import http.client
import os
import re
import socket
import threading
import time
import urllib.parse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PORT = int(os.environ.get('SHUKU_PROXY_PORT', '27231'))
ORIGIN = f'http://localhost:{PORT}'
LIMIT = 1 << 30  # 电脑上的听书语音最多 1GB

HOP = {'connection', 'keep-alive', 'proxy-authenticate', 'proxy-authorization', 'te', 'trailers',
       'transfer-encoding', 'upgrade', 'host', 'content-length'}
AUDIO = re.compile(r'^/api/books/(\d+)/listen/(\d+)/(\d+)$')
SCRIPT = re.compile(r'^/api/books/(\d+)/listen(/\d+)?$')

_base_fn = None   # 现在能用的书库地址（调用方给：先用上次通的，不通再探测）
_probe_fn = None  # 重新探测
_home = ''
_log = print
_last_probe = [0.0]
_down_until = [0.0]  # 刚试过都连不上：这之前直接当断网，不再一个个等超时


class Offline(Exception):
    pass


def _cache_dir():
    return os.path.join(_home, 'listen')


def _audio_path(book, n, i, query):
    v = urllib.parse.parse_qs(query).get('voices', [''])[0]
    key = hashlib.sha1(v.encode('utf-8')).hexdigest()[:10]
    return os.path.join(_cache_dir(), f'{book}-{key}', n, i + '.mp3')


def _script_path(path, query):
    q = urllib.parse.parse_qs(query)
    q.pop('t', None)  # 登录令牌不算
    key = path + '?' + urllib.parse.urlencode(sorted((k, v[0]) for k, v in q.items()))
    return os.path.join(_cache_dir(), 'scripts', hashlib.sha1(key.encode('utf-8')).hexdigest() + '.json')


def _upstream(method, path, body, headers, timeout=60):
    """发给书库；连不上换地址再试一次。返回 (地址, 连接, 回应)"""
    if time.time() < _down_until[0]:
        raise Offline()
    tried = set()
    for attempt in range(2):
        base = _base_fn() or ''
        if not base or base in tried:
            if time.time() - _last_probe[0] > 10:
                _last_probe[0] = time.time()
                base = _probe_fn() or ''
            if not base or base in tried:
                raise Offline()
        tried.add(base)
        u = urllib.parse.urlsplit(base)
        conn_cls = http.client.HTTPSConnection if u.scheme == 'https' else http.client.HTTPConnection
        c = conn_cls(u.hostname, u.port or (443 if u.scheme == 'https' else 80), timeout=5)
        h = dict(headers)
        h['Host'] = u.netloc
        # 书库按 Origin 防跨站写请求：换成书库自己的地址
        for k in ('Origin', 'Referer'):
            if k in h and h[k].startswith(ORIGIN):
                h[k] = base + h[k][len(ORIGIN):]
        try:
            c.connect()  # 连接最多等 5 秒，连上以后慢慢传
            c.sock.settimeout(timeout)
            c.request(method, (u.path.rstrip('/') or '') + path, body=body, headers=h)
            return base, c, c.getresponse()
        except (OSError, http.client.HTTPException):
            c.close()
            # 这个地址不通：下一次探测
            if time.time() - _last_probe[0] > 10:
                _last_probe[0] = time.time()
                _probe_fn()
    _down_until[0] = time.time() + 15
    raise Offline()


class Handler(BaseHTTPRequestHandler):
    protocol_version = 'HTTP/1.1'

    def log_message(self, *a):
        pass

    def do_GET(self): self.proxy()
    def do_HEAD(self): self.proxy()
    def do_POST(self): self.proxy()
    def do_PUT(self): self.proxy()
    def do_PATCH(self): self.proxy()
    def do_DELETE(self): self.proxy()
    def do_OPTIONS(self): self.proxy()

    def proxy(self):
        self.close_connection = True
        if self.headers.get('Host') not in (f'localhost:{PORT}', f'127.0.0.1:{PORT}'):
            return self.simple(403, b'forbidden')
        u = urllib.parse.urlsplit(self.path)
        n = int(self.headers.get('Content-Length') or 0)
        body = self.rfile.read(n) if n else None
        headers = {k: v for k, v in self.headers.items() if k.lower() not in HOP}
        m = AUDIO.match(u.path) if self.command == 'GET' else None
        if m:
            return self.audio(m, u, headers)
        try:
            base, c, r = _upstream(self.command, self.path, body, headers, timeout=300)
        except Offline:
            # 听书稿：断网时用存好的
            if self.command == 'GET' and SCRIPT.match(u.path):
                p = _script_path(u.path, u.query)
                if os.path.exists(p):
                    with open(p, 'rb') as f:
                        return self.simple(200, f.read(), 'application/json; charset=utf-8')
            return  # 不回答：网页当成断网
        try:
            if self.command == 'GET' and SCRIPT.match(u.path) and r.status == 200:
                data = r.read()
                p = _script_path(u.path, u.query)
                os.makedirs(os.path.dirname(p), exist_ok=True)
                with open(p + '.part', 'wb') as f:
                    f.write(data)
                os.replace(p + '.part', p)
                return self.simple(200, data, r.getheader('Content-Type') or 'application/json')
            self.relay(base, r)
        finally:
            c.close()

    def relay(self, base, r):
        self.send_response_only(r.status, r.reason)
        length = r.getheader('Content-Length')
        for k, v in r.getheaders():
            kl = k.lower()
            if kl in HOP:
                continue
            if kl == 'location' and v.startswith(base):
                v = ORIGIN + v[len(base):]
            self.send_header(k, v)
        chunked = length is None and self.command != 'HEAD' and r.status not in (204, 304)
        if length is not None:
            self.send_header('Content-Length', length)
        elif chunked:
            self.send_header('Transfer-Encoding', 'chunked')
        self.send_header('Connection', 'close')
        self.end_headers()
        if self.command == 'HEAD' or r.status in (204, 304):
            return
        while True:
            b = r.read(64 * 1024)
            if not b:
                break
            if chunked:
                self.wfile.write(b'%x\r\n' % len(b) + b + b'\r\n')
            else:
                self.wfile.write(b)
        if chunked:
            self.wfile.write(b'0\r\n\r\n')

    def simple(self, code, data, ctype='text/plain; charset=utf-8', extra=None):
        self.send_response_only(code)
        self.send_header('Content-Type', ctype)
        self.send_header('Content-Length', str(len(data)))
        self.send_header('Cache-Control', 'no-store')
        for k, v in (extra or {}).items():
            self.send_header(k, v)
        self.send_header('Connection', 'close')
        self.end_headers()
        if self.command != 'HEAD':
            self.wfile.write(data)

    def audio(self, m, u, headers):
        """一句听书语音：电脑上有就直接给，没有从书库取了存下来"""
        p = _audio_path(m.group(1), m.group(2), m.group(3), u.query)
        if os.path.exists(p):
            try:
                with open(p, 'rb') as f:
                    data = f.read()
                dur = ''
                try:
                    with open(p + '.dur') as f:
                        dur = f.read().strip()
                except OSError:
                    pass
                os.utime(p)  # 最近用过：清理时后删
                return self.simple(200, data, 'audio/mpeg', {'X-Duration': dur} if dur else None)
            except OSError:
                pass
        headers.pop('Range', None)
        headers.pop('range', None)
        try:
            base, c, r = _upstream('GET', self.path, None, headers, timeout=120)
        except Offline:
            return
        try:
            data = r.read()
            if r.status != 200 or len(data) < 200:
                return self.simple(r.status, data, r.getheader('Content-Type') or 'application/json')
            dur = r.getheader('X-Duration') or ''
            os.makedirs(os.path.dirname(p), exist_ok=True)
            with open(p + '.part', 'wb') as f:
                f.write(data)
            os.replace(p + '.part', p)
            if dur:
                with open(p + '.dur', 'w') as f:
                    f.write(dur)
            return self.simple(200, data, 'audio/mpeg', {'X-Duration': dur} if dur else None)
        finally:
            c.close()


class _Server(ThreadingHTTPServer):
    daemon_threads = True
    allow_reuse_address = os.name != 'nt'

    def server_bind(self):
        if os.name == 'nt' and hasattr(socket, 'SO_EXCLUSIVEADDRUSE'):
            self.socket.setsockopt(socket.SOL_SOCKET, socket.SO_EXCLUSIVEADDRUSE, 1)
        super().server_bind()


class _Server6(_Server):
    address_family = socket.AF_INET6


def _cleanup_loop():
    """电脑上的听书语音超过 1GB：最久没用的先删"""
    while True:
        time.sleep(600)
        try:
            files = []
            total = 0
            for root, _, names in os.walk(_cache_dir()):
                for n in names:
                    if n.endswith('.mp3'):
                        fp = os.path.join(root, n)
                        st = os.stat(fp)
                        files.append((st.st_mtime, st.st_size, fp))
                        total += st.st_size
            if total <= LIMIT:
                continue
            for _, size, fp in sorted(files):
                for x in (fp, fp + '.dur'):
                    try:
                        os.remove(x)
                    except OSError:
                        pass
                total -= size
                if total <= LIMIT * 0.9:
                    break
        except Exception as e:
            _log('清理听书语音出错：', e)


def start(base_fn, probe_fn, home, log=print):
    """开代理（后台线程）。localhost 可能先解析成 ::1：两个都听"""
    global _base_fn, _probe_fn, _home, _log
    _base_fn, _probe_fn, _home, _log = base_fn, probe_fn, home, log
    os.makedirs(_cache_dir(), exist_ok=True)
    servers = []
    srv = _Server(('127.0.0.1', PORT), Handler)
    servers.append(srv)
    try:
        servers.append(_Server6(('::1', PORT), Handler))
    except OSError:
        pass
    for s in servers:
        threading.Thread(target=s.serve_forever, daemon=True).start()
    threading.Thread(target=_cleanup_loop, daemon=True).start()
    return servers
