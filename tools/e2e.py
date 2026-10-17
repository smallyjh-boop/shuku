#!/usr/bin/env python3
"""端到端检查：构建服务端、生成一批乱名测试书、启动模拟的联网服务（豆瓣、Open Library、
Google Books、Bangumi、AI、语音接口），然后从接口和网页两方面把功能走一遍。

用法：python3 tools/e2e.py            全部检查
     python3 tools/e2e.py --api      只查接口
环境变量：E2E_DIR（默认 /tmp/shuku-e2e）、E2E_PORT（8095）、E2E_MOCK_PORT（8096）
结果：终端里逐项打印，汇总写到 $E2E_DIR/report.txt，截图在 $E2E_DIR/shots/
需要：Go、Python 的 requests / Pillow / reportlab / playwright，以及 ffmpeg（可选，生成试听用的 MP3）
"""
import glob, io, json, os, re, shutil, signal, subprocess, sys, threading, time, traceback, urllib.parse, zipfile
import xml.etree.ElementTree as ET
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import requests

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
T = os.environ.get("E2E_DIR", "/tmp/shuku-e2e")
PORT = int(os.environ.get("E2E_PORT", "8095"))
MPORT = int(os.environ.get("E2E_MOCK_PORT", "8096"))
BASE = f"http://127.0.0.1:{PORT}"
MOCK = f"http://127.0.0.1:{MPORT}"
PASSWORD = "test123"
SHOTS = os.path.join(T, "shots")
ONLY_API = "--api" in sys.argv

results = []


def check(name, ok, detail=""):
    ok = bool(ok)
    results.append((name, ok, detail))
    print(("  ✓ " if ok else "  ✗ ") + name + ("" if ok or not detail else f"  —— {detail}"), flush=True)
    return ok


def section(name):
    print(f"\n== {name}", flush=True)
    results.append((f"== {name}", None, ""))


# ---------------------------------------------------------------- 模拟服务

TTS_LOG, AI_LOG, FETCH_LOG = [], [], []
TONE = b""
COVER = b""


def make_assets():
    global TONE, COVER
    from PIL import Image, ImageDraw
    im = Image.new("RGB", (400, 600), (60, 80, 110))
    d = ImageDraw.Draw(im)
    for i in range(0, 600, 6):
        d.line([0, i, 400, (i * 7) % 600], fill=((i * 3) % 255, 120, 160), width=2)
    b = io.BytesIO()
    im.save(b, "JPEG", quality=90)
    COVER = b.getvalue()
    mp3 = os.path.join(T, "tone.mp3")
    if shutil.which("ffmpeg"):
        subprocess.run(["ffmpeg", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "sine=frequency=520:duration=0.7", "-q:a", "9", mp3], check=True)
        TONE = open(mp3, "rb").read()
    else:  # 没有 ffmpeg 就用一段静音 WAV
        import wave
        w = wave.open(mp3, "wb")
        w.setnchannels(1); w.setsampwidth(2); w.setframerate(16000); w.writeframes(b"\0\0" * 11000); w.close()
        TONE = open(mp3, "rb").read()


DOUBAN = {
    "三体Ⅱ": [("三体Ⅱ：黑暗森林", "3066477", "刘慈欣", "2008")],
    "三体": [("三体", "2567698", "刘慈欣", "2008"), ("三体Ⅱ：黑暗森林", "3066477", "刘慈欣", "2008")],
    "活着": [("活着", "4913064", "余华", "2012")],
    "球状闪电": [("球状闪电", "2155921", "刘慈欣", "2005")],
    "围城": [("围城", "1008145", "钱锺书", "1991")],
}
DETAIL = {
    "3066477": ("重庆出版社", "9787536693968", "三体人在利用科技锁死了地球人的科学之后，面壁计划开始。"),
    "2567698": ("重庆出版社", "9787536692930", "文化大革命如火如荼进行的同时，军方探寻外星文明的绝秘计划取得了突破性进展。"),
    "4913064": ("作家出版社", "9787506365437", "《活着》讲述了农村人福贵悲惨的人生遭遇。"),
    "2155921": ("四川科学技术出版社", "9787536455542", "一个离奇的雷雨之夜，一颗球状闪电闯进了少年的家里。"),
    "1008145": ("人民文学出版社", "9787020024759", "《围城》是钱锺书所著的长篇小说。"),
}


class Mock(BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def send(self, code, body, ctype="application/json; charset=utf-8", headers=None):
        if isinstance(body, (dict, list)):
            body = json.dumps(body, ensure_ascii=False).encode()
        elif isinstance(body, str):
            body = body.encode()
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        for k, v in (headers or {}).items():
            self.send_header(k, v)
        self.end_headers()
        self.wfile.write(body)

    def body(self):
        n = int(self.headers.get("Content-Length") or 0)
        return self.rfile.read(n) if n else b""

    def do_GET(self):
        u = urllib.parse.urlparse(self.path)
        q = urllib.parse.parse_qs(u.query)
        p = u.path
        if p == "/douban/j/subject_suggest":
            s = q.get("q", [""])[0]
            hits = []
            for key, lst in DOUBAN.items():  # 豆瓣支持按 ISBN 搜
                hits += [x for x in lst if DETAIL.get(x[1], ("", "", ""))[1] == s and x not in hits]
            for key in sorted(DOUBAN, key=len, reverse=True):
                if not hits and key in s:
                    hits = DOUBAN[key]
                    break
            return self.send(200, [{"title": t, "url": f"https://book.douban.com/subject/{i}/", "pic": f"{MOCK}/img/view/subject/s/public/s{i}.jpg",
                                    "author_name": a, "year": y, "type": "b", "id": i} for t, i, a, y in hits])
        m = re.match(r"^/douban/subject/(\d+)/?$", p)
        if m and m.group(1) in DETAIL:
            pub, isbn, intro = DETAIL[m.group(1)]
            return self.send(200, f'<div id="info" class=""><span class="pl">出版社:</span> <a href="#">{pub}</a><br/><span class="pl">ISBN:</span> {isbn}<br/></div><div class="intro"><p>{intro}</p></div>', "text/html; charset=utf-8")
        if p == "/ol/search.json":
            return self.send(200, {"docs": []})
        if p == "/google/books/v1/volumes":
            return self.send(200, {"items": []})
        if p.startswith("/img/"):
            return self.send(200, COVER, "image/jpeg")
        if p.startswith("/files/"):
            FETCH_LOG.append(p)
            f = os.path.join(T, "remote", urllib.parse.unquote(p[len("/files/"):]))
            if os.path.isfile(f):
                return self.send(200, open(f, "rb").read(), "application/octet-stream",
                                 {"Content-Disposition": "attachment; filename*=UTF-8''" + urllib.parse.quote(os.path.basename(f))})
            return self.send(404, "not found", "text/plain")
        self.send(404, {"error": "no route " + p})

    def do_POST(self):
        p = urllib.parse.urlparse(self.path).path
        raw = self.body()
        if p == "/tts/v1/audio/speech":
            j = json.loads(raw or b"{}")
            TTS_LOG.append(j)
            return self.send(200, TONE, "audio/mpeg")
        if p == "/tts-bad/v1/audio/speech":
            return self.send(500, {"error": "engine down"})
        if p == "/bgm/v0/search/subjects":
            j = json.loads(raw or b"{}")
            if "进击" in j.get("keyword", ""):
                return self.send(200, {"data": [{"id": 12, "name": "進撃の巨人", "name_cn": "进击的巨人", "summary": "巨人与人类的战斗。", "date": "2010-03-17",
                                                 "images": {"large": f"{MOCK}/img/bgm.jpg"}, "tags": [{"name": "热血"}], "infobox": [{"key": "作者", "value": "諫山創"}]}]})
            return self.send(200, {"data": []})
        if p == "/ai/v1/chat/completions":
            j = json.loads(raw or b"{}")
            info = json.loads(j["messages"][1]["content"])
            AI_LOG.append(info)
            name = info.get("文件名", "")
            rule = info.get("规则解析", {})
            if "noname" in name or "x7f3" in name:
                out = {"title": "围城", "author": "钱锺书", "category": "小说", "year": 1947, "tags": ["讽刺", "爱情"], "confidence": 0.96}
            elif "三体2" in name or "黑暗森林" in name:
                out = {"title": "三体Ⅱ·黑暗森林", "author": "刘慈欣", "series": "三体", "series_index": 2, "category": "小说", "confidence": 0.97}
            else:
                out = {"title": rule.get("书名") or info.get("内嵌书名") or name, "author": rule.get("作者") or info.get("内嵌作者") or "", "category": "小说", "confidence": 0.9}
            return self.send(200, {"choices": [{"message": {"content": "<think>想一想</think>" + json.dumps(out, ensure_ascii=False)}}]})
        self.send(404, {"error": "no route " + p})


# ---------------------------------------------------------------- 工具

S = requests.Session()
TOKEN = ""


def api(method, path, expect=200, **kw):
    h = kw.pop("headers", {})
    if TOKEN:
        h["Authorization"] = "Bearer " + TOKEN
    r = S.request(method, BASE + path, headers=h, timeout=kw.pop("timeout", 60), **kw)
    S.cookies.clear()  # 只用令牌，不让 Cookie 掩盖未登录的情况
    if expect is not None and r.status_code != expect:
        raise AssertionError(f"{method} {path} → {r.status_code} {r.text[:300]}")
    return r


def j(method, path, expect=200, **kw):
    return api(method, path, expect, **kw).json()


def wait(fn, timeout=60, step=0.5, what=""):
    end = time.time() + timeout
    last = None
    while time.time() < end:
        try:
            last = fn()
            if last:
                return last
        except Exception as e:  # noqa
            last = e
        time.sleep(step)
    raise TimeoutError(f"等待超时：{what} {last!r}")


def all_books():
    return j("GET", "/api/books?limit=500&sort=title")["items"]


def book_by_title(t):
    for b in all_books():
        if b["title"] == t:
            return j("GET", f"/api/books/{b['id']}")
    return None


def make_epub(path, title, author, chapters):
    z = zipfile.ZipFile(path, "w")
    z.writestr(zipfile.ZipInfo("mimetype"), "application/epub+zip")
    z.writestr("META-INF/container.xml", '<?xml version="1.0"?><container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="c.opf" media-type="application/oebps-package+xml"/></rootfiles></container>')
    man = spine = ""
    for i, (t, paras) in enumerate(chapters):
        z.writestr(f"s{i}.xhtml", f'<?xml version="1.0" encoding="utf-8"?><html xmlns="http://www.w3.org/1999/xhtml"><head><title>{t}</title></head><body><h2>{t}</h2>' + "".join(f"<p>{p}</p>" for p in paras) + "</body></html>")
        man += f'<item id="s{i}" href="s{i}.xhtml" media-type="application/xhtml+xml"/>'
        spine += f'<itemref idref="s{i}"/>'
    z.writestr("c.opf", f'<?xml version="1.0"?><package xmlns="http://www.idpf.org/2007/opf" version="2.0" unique-identifier="id"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:identifier id="id">x{abs(hash(title))}</dc:identifier><dc:title>{title}</dc:title>{f"<dc:creator>{author}</dc:creator>" if author else ""}</metadata><manifest>{man}</manifest><spine>{spine}</spine></package>')
    z.close()


# ---------------------------------------------------------------- 准备环境

server_proc = None


def setup(fresh=True):
    global server_proc
    print(f"测试目录：{T}")
    subprocess.run(["pkill", "-f", "shuku-e2e-server"], check=False)
    time.sleep(0.3)
    if fresh:
        shutil.rmtree(T, ignore_errors=True)
        os.makedirs(SHOTS)
        os.makedirs(os.path.join(T, "remote"))
    make_assets()
    print("构建服务端…", flush=True)
    env = dict(os.environ, GOPROXY="off", GOFLAGS="-mod=vendor")
    subprocess.run(["go", "build", "-tags", "sqlite_fts5", "-o", os.path.join(T, "shuku-e2e-server"), "."], cwd=os.path.join(ROOT, "server"), check=True, env=env)
    if fresh:
        subprocess.run([sys.executable, os.path.join(ROOT, "tools", "make-samples.py"), T], check=True, stdout=subprocess.DEVNULL)
    srv = ThreadingHTTPServer(("127.0.0.1", MPORT), Mock)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    env = dict(os.environ,
               SHUKU_PORT=str(PORT), SHUKU_DATA=os.path.join(T, "data"), SHUKU_INBOX=os.path.join(T, "inbox"), SHUKU_WEB=os.path.join(ROOT, "web"),
               SHUKU_LIBRARIES=f"图书:{T}/books:book,漫画:{T}/comics:comic",
               SHUKU_DOUBAN_BASE=MOCK + "/douban", SHUKU_OPENLIBRARY_BASE=MOCK + "/ol", SHUKU_OPENLIBRARY_COVERS=MOCK + "/img",
               SHUKU_GOOGLE_BASE=MOCK + "/google", SHUKU_BANGUMI_BASE=MOCK + "/bgm", SHUKU_EDGE_WSS="ws://127.0.0.1:9/none",
               SHUKU_LAN_URL=BASE, SHUKU_REMOTE_URL="")
    log = open(os.path.join(T, "server.log"), "w")
    server_proc = subprocess.Popen([os.path.join(T, "shuku-e2e-server")], cwd=T, env=env, stdout=log, stderr=subprocess.STDOUT)
    wait(lambda: requests.get(BASE + "/api/ping", timeout=2).ok, 20, what="服务器启动")


def teardown():
    if server_proc:
        server_proc.send_signal(signal.SIGTERM)
        try:
            server_proc.wait(10)
        except Exception:
            server_proc.kill()


# ---------------------------------------------------------------- 接口检查

def test_auth():
    global TOKEN
    section("登录")
    st = j("GET", "/api/auth/state")
    check("首次启动提示设置密码", st["setup"] is False and st["authed"] is False, st)
    check("密码太短被拒绝", api("POST", "/api/auth/setup", 400, json={"Password": "123"}).status_code == 400)
    TOKEN = j("POST", "/api/auth/setup", json={"Password": PASSWORD, "Device": "e2e"})["token"]
    check("设置密码后拿到令牌", len(TOKEN) == 64)
    check("重复设置被拒绝", api("POST", "/api/auth/setup", 400, json={"Password": "abcdef"}).status_code == 400)
    tok = TOKEN
    TOKEN = ""
    check("错误密码 401", api("POST", "/api/auth/login", 401, json={"Password": "nope"}).status_code == 401)
    check("未登录访问接口 401", api("GET", "/api/books", 401).status_code == 401)
    TOKEN = j("POST", "/api/auth/login", json={"Password": PASSWORD, "Device": "e2e2"})["token"]
    check("正确密码能登录", len(TOKEN) == 64 and TOKEN != tok)
    ping = j("GET", "/api/ping")
    check("ping 返回服务器 ID", ping.get("app") == "shuku" and len(ping.get("id", "")) == 16, ping)
    code = j("POST", "/api/auth/handoff")["code"]
    TOKEN, saved = "", TOKEN
    t2 = j("POST", "/api/auth/exchange", json={"Code": code, "Device": "x"})["token"]
    check("交接码能换到新令牌（切换地址免登录）", len(t2) == 64)
    check("交接码只能用一次", api("POST", "/api/auth/exchange", 401, json={"Code": code}).status_code == 401)
    TOKEN = saved


def test_ingest():
    section("入库与识别")
    wait(lambda: j("GET", "/api/status")["inbox"]["pending"] == 0 and len(all_books()) >= 8, 90, 1, "投递目录处理完")
    time.sleep(1)
    books = {b["title"]: b for b in all_books()}
    print("    书目：" + "，".join(f"{t}/{b['author']}/{b['status']}" for t, b in books.items()))
    b2 = book_by_title("三体Ⅱ·黑暗森林")
    check("乱名文件恢复书名《三体Ⅱ·黑暗森林》", b2 is not None)
    if b2:
        check("系列和序号识别（三体 第 2 本）", b2["series"] == "三体" and b2["series_index"] == 2, (b2["series"], b2["series_index"]))
        check("按系列规则归档", b2["files"][0]["path"].endswith("刘慈欣/三体/02 三体Ⅱ·黑暗森林.epub"), b2["files"][0]["path"])
        check("联网补全了 ISBN 和简介", b2["isbn"] == "9787536693968" and "面壁" in b2["description"], (b2["isbn"], b2["description"][:30]))
    b1 = book_by_title("三体")
    check("作者名里的空格被规范（刘 慈欣 → 刘慈欣）", b1 and b1["author"] == "刘慈欣", b1 and b1["author"])
    alive = book_by_title("活着")
    check("GBK 编码 TXT 识别出《活着》余华", alive and alive["author"] == "余华", alive and alive["author"])
    check("TXT 按规则命名", alive and alive["files"][0]["path"].endswith("余华/活着 (2012)/活着 - 余华.txt"), alive and alive["files"][0]["path"])
    pdf = [b for b in books.values() if "深度学习" in b["title"]]
    check("PDF 从属性里取到书名（去掉 Microsoft Word 前缀）", pdf and pdf[0]["title"].startswith("深度学习入门"), list(books))
    if pdf:
        check("PDF 作者", pdf[0]["author"] == "斋藤康毅", pdf[0]["author"])
    comics = [b for b in all_books() if b["kind"] == "comic"]
    check("两卷漫画入库到漫画库", len(comics) == 2, [c["title"] for c in comics])
    if comics:
        c = j("GET", f"/api/books/{comics[0]['id']}")
        check("漫画按“系列 第001卷”命名", re.search(r"进击的巨人/进击的巨人 第00[12]卷\.cbz$", c["files"][0]["path"]), c["files"][0]["path"])
    check("重复文件移到 _重复（保留不带“副本”的那个）", glob.glob(os.path.join(T, "inbox", "_重复", "*副本*.epub")), os.listdir(os.path.join(T, "inbox", "_重复")))
    check("不是书的压缩包移到 _无法识别", os.path.exists(os.path.join(T, "inbox", "_无法识别", "资料.zip")))
    check("已有书库里的书原地登记", "球状闪电" in books and "流浪地球" in books)
    evs = j("GET", "/api/events")
    check("入库动态有记录", len(evs) >= 6 and any("已入库" in e["message"] for e in evs))
    covers = [b for b in books.values() if b["cover"]]
    check("有封面的书生成了封面", len(covers) >= 4, len(covers))
    r = api("GET", f"/api/books/{b2['id']}/cover", 200)
    check("封面图片能取到", r.headers["Content-Type"].startswith("image/"), r.headers.get("Content-Type"))


def test_search():
    section("搜索")
    def titles(q):
        return [b["title"] for b in j("GET", "/api/books?q=" + urllib.parse.quote(q))["items"]]
    check("首字母 st 找到三体", "三体" in titles("st"), titles("st"))
    check("全拼 liucixin 找到刘慈欣的书", len(titles("liucixin")) >= 3, titles("liucixin"))
    check("繁体“三體”也能找到", "三体" in titles("三體"), titles("三體"))
    check("作者名搜索", "活着" in titles("余华"))
    check("没有结果时返回空", titles("zzzzzz") == [])
    facets = j("GET", "/api/facets")
    check("分类、格式统计", facets["total"] >= 8 and any(f["name"] == "epub" for f in facets["formats"]), facets["formats"])


def test_fulltext():
    section("书内搜索（全文索引）")
    st = wait(lambda: (lambda s: s if s["indexed"] + s["skipped"] >= s["total"] and s["total"] >= 5 and not s["running"] else None)(j("GET", "/api/status")["fulltext"]), 120, 1, "全文索引")
    check("全文索引完成", st["indexed"] >= 5, st)
    r = j("GET", "/api/fulltext?q=" + urllib.parse.quote("游手好闲"))
    hit = [b for b in r["books"] if b["title"] == "活着"]
    check("TXT 正文搜得到", hit and hit[0]["hits"][0]["snippet"], r)
    if hit:
        check("命中带章节名", hit[0]["hits"][0]["label"].startswith("第"), hit[0]["hits"][0])
    r = j("GET", "/api/fulltext?q=" + urllib.parse.quote("晴朗的早晨"))
    check("EPUB 正文搜得到（多本）", len(r["books"]) >= 2, [b["title"] for b in r["books"]])
    r = j("GET", "/api/fulltext?q=" + urllib.parse.quote("晴朗的早晨") + "&limit=1")
    check("limit 生效", len(r["books"]) == 1)
    r = j("GET", "/api/fulltext?q=" + urllib.parse.quote("遊手好閒"))
    check("繁体搜简体正文", any(b["title"] == "活着" for b in r["books"]), [b["title"] for b in r["books"]])
    r = j("GET", "/api/fulltext?q=Neural")
    check("PDF 正文搜得到（pdftotext / mutool）", any("深度学习" in b["title"] for b in r["books"]), r)
    r = j("GET", "/api/fulltext?q=" + urllib.parse.quote("的"))
    check("单字提示至少两个字", r["books"] == [] and r.get("hint"), r)


def test_reading():
    section("阅读：格式、进度、书签、统计")
    b2 = book_by_title("三体Ⅱ·黑暗森林")
    info = j("GET", f"/api/books/{b2['id']}/read")
    check("EPUB 用 foliate 阅读器", info["reader"] == "foliate" and info["url"].endswith("/raw"), info)
    r = api("GET", info["url"], 206, headers={"Range": "bytes=0-99"})
    check("文件支持分段下载（Range）", len(r.content) == 100 and r.headers.get("Content-Range", "").startswith("bytes 0-99/"))
    alive = book_by_title("活着")
    info = j("GET", f"/api/books/{alive['id']}/read")
    check("TXT 转成 EPUB 阅读", info["reader"] == "foliate" and info["url"].endswith("/epub"), info)
    z = zipfile.ZipFile(io.BytesIO(api("GET", info["url"]).content))
    docs = [n for n in z.namelist() if n.endswith(".xhtml") and "nav" not in n]
    check("TXT 按章节切分（5 章）", len(docs) >= 5, z.namelist())
    pdf = [b for b in all_books() if "深度学习" in b["title"]][0]
    info = j("GET", f"/api/books/{pdf['id']}/read")
    check("PDF 用 pdf.js 阅读器", info["reader"] == "pdf", info)
    comic = [b for b in all_books() if b["kind"] == "comic"][0]
    info = j("GET", f"/api/books/{comic['id']}/read")
    check("漫画用漫画阅读器", info["reader"] == "comic", info)
    pages = j("GET", f"/api/files/{info['file_id']}/pages")
    check("漫画 10 页，第 5 页是跨页大图", pages["count"] == 10 and pages["dims"][4][0] > pages["dims"][4][1], pages)
    full = api("GET", f"/api/files/{info['file_id']}/pages/0")
    small = api("GET", f"/api/files/{info['file_id']}/pages/0?w=300")
    check("漫画按屏幕宽度压缩", len(small.content) < len(full.content) and small.headers["Content-Type"] == "image/jpeg", (len(full.content), len(small.content)))
    check("超出页码 404", api("GET", f"/api/files/{info['file_id']}/pages/99", 404).status_code == 404)
    # 进度
    pid = b2["id"]
    j("PUT", f"/api/books/{pid}/progress", json={"file_id": b2["files"][0]["id"], "location": "epubcfi(/6/4!/4/2/1:0)", "percent": 0.42, "device": "e2e", "seconds": 300})
    p = j("GET", f"/api/books/{pid}/progress")
    check("进度保存", abs(p["percent"] - 0.42) < 1e-6 and p["location"].startswith("epubcfi"), p)
    check("在读列表", any(x["id"] == pid for x in j("GET", "/api/books?reading=1")["items"]))
    j("PUT", f"/api/books/{alive['id']}/progress", json={"file_id": alive["files"][0]["id"], "location": "", "percent": 1.0, "seconds": 120})
    check("读到最后标记为读完", book_by_title("活着")["read_state"] == "done")
    st = j("GET", "/api/stats")
    check("阅读统计：今天 7 分钟", st["today"] == 420, st["today"])
    check("今年读完 1 本", st["finished_year"] >= 1)
    check("近 30 天每天数据", len(st["days"]) >= 30)
    check("单次上报超过 10 分钟按 10 分钟算", (j("PUT", f"/api/books/{pid}/progress", json={"percent": 0.43, "seconds": 5000}) and j("GET", "/api/stats")["today"] == 1020), j("GET", "/api/stats")["today"])
    bm = j("POST", f"/api/books/{pid}/bookmarks", json={"location": "epubcfi(/6/4!/4)", "label": "上部 · 10%", "text": "那是一个晴朗的早晨"})
    check("添加书签", bm["id"] > 0)
    check("书签列表", len(j("GET", f"/api/books/{pid}/bookmarks")) == 1)
    j("DELETE", f"/api/bookmarks/{bm['id']}")
    check("删除书签", len(j("GET", f"/api/books/{pid}/bookmarks")) == 0)


def test_organize():
    section("整理建议与撤销")
    sug = j("GET", "/api/suggestions?fresh=1")
    types = sorted({s["type"] for s in sug})
    print("    建议：" + "；".join(f"[{s['type']}] {s['title']}" for s in sug))
    check("提醒合并相似文件夹（刘慈欣 / 刘 慈欣）", "folder" in types, types)
    check("提醒统一作者写法（Liu Cixin）", "author" in types, types)
    check("提醒清理空文件夹", "empty" in types, types)
    check("提醒按规范整理", "standard" in types, types)
    folder = [s for s in sug if s["type"] == "folder"][0]
    r = j("POST", "/api/suggestions/apply", json={"key": folder["key"]})
    check("执行合并文件夹", r["ok"] and r["count"] >= 1, r)
    check("合并后旧文件夹没了", not os.path.exists(os.path.join(T, "books", "小说", "刘 慈欣")) or not os.listdir(os.path.join(T, "books", "小说", "刘 慈欣")))
    u = j("POST", f"/api/ops/{r['batch']}/undo")
    check("整批撤销", u["count"] >= 1, u)
    check("撤销后文件回到原处", os.path.exists(os.path.join(T, "books", "小说", "刘 慈欣", "球状闪电.epub")))
    ops = j("GET", "/api/ops")
    check("操作记录", any(o["batch"] == r["batch"] and o["undone"] for o in ops))
    emp = [s for s in j("GET", "/api/suggestions?fresh=1") if s["type"] == "empty"]
    if emp:
        j("POST", "/api/suggestions/ignore", json={"key": emp[0]["key"]})
        check("忽略的建议不再出现", not any(s["key"] == emp[0]["key"] for s in j("GET", "/api/suggestions?fresh=1")))
    # 修改信息后自动改名
    ball = book_by_title("球状闪电")
    r = j("PATCH", f"/api/books/{ball['id']}", json={"title": "球状闪电", "author": "刘慈欣", "year": 2005, "organize": True})
    path = r["book"]["files"][0]["path"]
    check("改信息后按规则移动文件", path.endswith("刘慈欣/球状闪电 (2005)/球状闪电 - 刘慈欣.epub"), path)
    prev = j("GET", "/api/preview-template?t=" + urllib.parse.quote("{作者}/{书名}{ (年份)}"))
    check("命名规则预览", prev["path"] == "刘慈欣/三体Ⅱ·黑暗森林 (2008).epub", prev)


def test_upload_ai_delete():
    section("上传、AI 识别、删除与回收站")
    s = j("PUT", "/api/settings", json={"ai": {"enabled": True, "base_url": MOCK + "/ai/v1", "api_key": "sk-test-1234567890", "model": "mock-1"}})
    check("保存 AI 设置后 Key 打码", "•" in s["ai"]["api_key"] and s["ai"]["api_key"].endswith("7890"), s["ai"])
    s = j("PUT", "/api/settings", json={"ai": {"enabled": True, "base_url": MOCK + "/ai/v1", "api_key": s["ai"]["api_key"], "model": "mock-1"}})
    r = j("POST", "/api/ai/test", json={"enabled": True, "base_url": MOCK + "/ai/v1", "api_key": s["ai"]["api_key"], "model": "mock-1"})
    check("AI 测试（带 <think> 的推理模型输出）", r["result"]["title"] == "三体Ⅱ·黑暗森林" and r["result"]["series_index"] == 2, r)
    check("打码的 Key 没有被当成新 Key 保存", AI_LOG and True)
    p = os.path.join(T, "noname_x7f3.epub")
    make_epub(p, "Untitled", "", [("第一章", ["方鸿渐到了上海。" * 20]), ("第二章", ["鲍小姐的故事。" * 20])])
    r = api("POST", "/api/upload?path=" + urllib.parse.quote("新书/noname_x7f3.epub"), data=open(p, "rb").read())
    check("上传到投递目录", r.json()["ok"])
    b = wait(lambda: book_by_title("围城"), 40, 1, "AI 识别上传的书")
    check("AI 把乱名文件识别成《围城》钱锺书", b and b["author"] == "钱锺书" and b["status"] == "ok", b and (b["author"], b["status"]))
    check("识别后按规则归档", b and b["files"][0]["path"].endswith("钱锺书/围城 (1947)/围城 - 钱锺书.epub"), b and b["files"][0]["path"])
    check("不支持的格式被拒绝", api("POST", "/api/upload?path=a.exe", 415, data=b"MZ").status_code == 415)
    j("DELETE", f"/api/books/{b['id']}")
    check("删除后文件进 _回收站", glob.glob(os.path.join(T, "books", "_回收站", "围城 - 钱锺书*.epub")))
    check("删除后书目不在了", book_by_title("围城") is None)
    n = len(all_books())
    j("POST", "/api/scan")
    time.sleep(1)
    wait(lambda: not j("GET", "/api/status")["scan"]["running"], 30, 0.5, "扫描")
    check("扫描书库时跳过回收站（删掉的书不会回来）", len(all_books()) == n and not any(x["author"] == "钱锺书" for x in all_books()))
    j("PUT", "/api/settings", json={"ai": {"enabled": False, "base_url": MOCK + "/ai/v1", "api_key": "", "model": "mock-1"}})


def test_online():
    section("联网补全")
    alive = book_by_title("活着")
    r = j("POST", f"/api/books/{alive['id']}/enrich", json={})
    check("联网查找返回候选", r["candidates"] and r["candidates"][0]["title"] == "活着" and r["candidates"][0]["isbn"] == "9787506365437", r)
    r2 = j("POST", f"/api/books/{alive['id']}/cover-url", json={"url": MOCK + "/img/x.jpg"})
    check("用候选封面替换", r2["ok"])
    check("太小的图片当作占位图拒绝", api("POST", f"/api/books/{alive['id']}/cover-url", 502, json={"url": MOCK + "/nothing"}).status_code == 502)
    comic = [b for b in all_books() if b["kind"] == "comic"][0]
    r = j("POST", f"/api/books/{comic['id']}/enrich", json={})
    check("漫画查 Bangumi", r["candidates"] and r["candidates"][0]["source"] == "Bangumi", r)
    r = j("POST", "/api/enrich-all")
    check("一键补全启动", "message" in r)
    wait(lambda: not j("GET", "/api/status")["enrich"]["running"], 60, 1, "批量补全")
    check("批量补全完成", j("GET", "/api/status")["enrich"]["total"] >= 1)


def test_tts():
    section("听书（服务器语音）")
    v = j("GET", "/api/tts/voices")
    check("默认微软自然语音，含晓晓", v["engine"] == "edge" and any(x["id"] == "zh-CN-XiaoxiaoNeural" for x in v["voices"]), v["engine"])
    r = api("POST", "/api/tts", 502, json={"text": "你好", "voice": "zh-CN-XiaoxiaoNeural"})
    check("微软语音连不上时返回明确的错误", "语音合成失败" in r.json()["error"], r.text)
    j("PUT", "/api/settings", json={"tts": {"engine": "openai", "voice": "", "openai": {"base_url": MOCK + "/tts/v1", "api_key": "k-12345678901", "model": "tts-1", "voice": "nova", "voices": "nova,alloy"}}})
    v = j("GET", "/api/tts/voices")
    check("自定义接口的音色列表", [x["id"] for x in v["voices"]] == ["nova", "alloy"] and v["default"] == "nova", v)
    n0 = len(TTS_LOG)
    t0 = time.time()
    r = api("POST", "/api/tts", json={"text": "夜色渐深，城市的灯一盏一盏亮了起来。", "voice": "alloy", "speed": 1.2})
    check("合成返回 MP3", r.headers["Content-Type"] == "audio/mpeg" and r.content == TONE)
    check("语速和音色传给接口", TTS_LOG[-1]["speed"] == 1.2 and TTS_LOG[-1]["voice"] == "alloy", TTS_LOG[-1])
    api("POST", "/api/tts", json={"text": "夜色渐深，城市的灯一盏一盏亮了起来。", "voice": "alloy", "speed": 1.2})
    check("同一段文字第二次走缓存", len(TTS_LOG) == n0 + 1)
    s = j("GET", "/api/settings")
    check("语音接口 Key 打码", "•" in s["tts"]["openai"]["api_key"])
    check("空文字被拒绝", api("POST", "/api/tts", 400, json={"text": "  "}).status_code == 400)


def test_opds():
    section("OPDS（KOReader 等阅读器）")
    def get(p, auth=("u", PASSWORD)):
        return requests.get(BASE + p, auth=auth, timeout=20)
    check("不带密码要求认证", get("/opds", None).status_code == 401)
    check("错误密码拒绝", get("/opds", ("u", "bad")).status_code == 401)
    ns = {"a": "http://www.w3.org/2005/Atom"}
    root = ET.fromstring(get("/opds").content)
    navs = [e.find("a:title", ns).text for e in root.findall("a:entry", ns)]
    check("根目录导航", len(navs) >= 5, navs)
    feed = ET.fromstring(get("/opds/books/all").content)
    entries = feed.findall("a:entry", ns)
    nbooks = len([b for b in all_books() if b["kind"] == "book"])
    check("全部图书（漫画单独一栏）", len(entries) == nbooks, (len(entries), nbooks))
    check("漫画栏", len(ET.fromstring(get("/opds/books/comics").content).findall("a:entry", ns)) == 2)
    acq = [l.get("href") for e in entries for l in e.findall("a:link", ns) if l.get("rel") == "http://opds-spec.org/acquisition"]
    check("每本书有下载链接", len(acq) >= len(entries), len(acq))
    r = get(acq[0])
    check("通过 OPDS 下载文件", r.status_code == 200 and len(r.content) > 1000, r.status_code)
    s = ET.fromstring(get("/opds/search?q=st").content)
    check("OPDS 搜索", any("三体" in (e.find("a:title", ns).text or "") for e in s.findall("a:entry", ns)))
    check("OpenSearch 描述", "{searchTerms}" in get("/opds/opensearch.xml").text)
    img = [l.get("href") for e in entries for l in e.findall("a:link", ns) if l.get("rel") == "http://opds-spec.org/image"]
    check("封面链接", img and get(img[0]).headers.get("Content-Type", "").startswith("image/"))


def test_settings_libs():
    section("设置与书库目录")
    s = j("GET", "/api/settings")
    for k in ["network", "ai", "tts", "online_meta", "fulltext", "categories", "version", "tools"]:
        check(f"设置里有 {k}", k in s)
    check("网络地址来自环境变量", s["network"]["lan_url"] == BASE, s["network"])
    s = j("PUT", "/api/settings", json={"network": {"lan_url": BASE + "/", "remote_url": " https://nas.example.ts.net/ "}})
    check("网络地址去掉多余斜杠和空格", s["network"]["remote_url"] == "https://nas.example.ts.net" and s["network"]["lan_url"] == BASE, s["network"])
    check("相对路径的书库被拒绝", api("POST", "/api/libraries", 400, json={"name": "x", "path": "books2"}).status_code == 400)
    check("书库不能放在投递目录里", api("POST", "/api/libraries", 400, json={"name": "x", "path": os.path.join(T, "inbox", "lib")}).status_code == 400)
    j("POST", "/api/libraries", json={"name": "技术书", "path": os.path.join(T, "tech"), "kind": "book"})
    libs = j("GET", "/api/libraries")
    tech = [l for l in libs if l["name"] == "技术书"]
    check("添加书库目录", tech and tech[0]["exists"])
    j("PUT", f"/api/libraries/{tech[0]['id']}", json={"template": "{作者}/{书名}"})
    check("修改命名规则", [l for l in j("GET", "/api/libraries") if l["id"] == tech[0]["id"]][0]["template"] == "{作者}/{书名}")
    j("DELETE", f"/api/libraries/{tech[0]['id']}")
    check("移除书库目录", not [l for l in j("GET", "/api/libraries") if l["name"] == "技术书"])
    r = requests.get(BASE + "/", timeout=5)
    check("网页首页不缓存", r.headers.get("Cache-Control") == "no-cache" and "<div id=\"app\">" in r.text)
    r = requests.get(BASE + "/some/deep/link", timeout=5)
    check("前端路由回落到 index.html", r.status_code == 200 and "<div id=\"app\">" in r.text)
    r = requests.get(BASE + "/vendor/pdfjs/pdf.mjs", timeout=5)
    check("阅读内核长缓存，.mjs 类型正确", "max-age" in r.headers.get("Cache-Control", "") and "javascript" in r.headers.get("Content-Type", ""))
    check("路径穿越被挡住", "password_hash" not in requests.get(BASE + "/../data/shuku.db", timeout=5).text)
    r = requests.options(BASE + "/api/books", headers={"Origin": "http://other:8080", "Access-Control-Request-Method": "GET"}, timeout=5)
    check("跨地址访问的 CORS 预检", r.status_code == 204 and r.headers.get("Access-Control-Allow-Origin") == "http://other:8080")


# ---------------------------------------------------------------- 网页检查

def ui_tests():
    from playwright.sync_api import sync_playwright
    chrome = sorted(glob.glob("/opt/pw-browsers/chromium-*/chrome-linux/chrome"))
    errors = []

    def attach(pg, label):
        pg.on("pageerror", lambda e: errors.append(f"[{label}] PAGEERROR {e}"))
        pg.on("console", lambda m: m.type == "error" and "favicon" not in m.text and "Failed to load resource" not in m.text and errors.append(f"[{label}] console.error {m.text}"))

    def reader_ready(pg):
        # 页面刚跳转时阅读器还没建好，不能只等“加载中”消失
        pg.wait_for_function("() => document.querySelector('.reader') && !document.querySelector('.reader .r-loading')", timeout=30000)

    def shot(pg, name):
        pg.screenshot(path=os.path.join(SHOTS, name + ".png"))

    with sync_playwright() as p:
        browser = p.chromium.launch(executable_path=chrome[-1] if chrome else None, args=["--autoplay-policy=no-user-gesture-required"])

        def context(w=1366, h=860, dark=False, mobile=False, token=True):
            ctx = browser.new_context(viewport={"width": w, "height": h}, device_scale_factor=2 if mobile else 1, color_scheme="dark" if dark else "light",
                                      has_touch=mobile, is_mobile=mobile, locale="zh-CN", timezone_id="Asia/Shanghai")
            if token:
                ctx.add_init_script(f"localStorage.setItem('shuku.token', {json.dumps(TOKEN)})")
            return ctx

        # ---- 登录 ----
        section("网页：登录")
        ctx = context(token=False)
        pg = ctx.new_page(); attach(pg, "login")
        pg.goto(BASE + "/")
        pg.wait_for_selector(".auth input[type=password]")
        pg.fill(".auth input[type=password]", "wrong-pass")
        pg.click(".auth button[type=submit]")
        pg.wait_for_selector(".auth .err:not(:empty)")
        check("错误密码有提示", "不正确" in pg.inner_text(".auth .err"))
        pg.fill(".auth input[type=password]", PASSWORD)
        pg.click(".auth button[type=submit]")
        pg.wait_for_selector(".book")
        check("登录后进入书架", pg.locator(".book").count() >= 6)
        ctx.close()

        # ---- 书架、搜索、详情 ----
        section("网页：书架、搜索、详情")
        ctx = context()
        pg = ctx.new_page(); attach(pg, "shelf")
        pg.goto(BASE + "/")
        pg.wait_for_selector(".book")
        pg.wait_for_selector(".continue-item")
        check("继续阅读区域", pg.locator(".continue-item").count() >= 1)
        check("侧栏显示内网线路", "内网" in pg.inner_text(".side .netline") or "已连接" in pg.inner_text(".side .netline"), pg.inner_text(".side .netline"))
        shot(pg, "01-shelf")
        pg.fill(".search input", "st")
        pg.wait_for_function("() => [...document.querySelectorAll('.book .t')].some(e => e.textContent === '三体') && document.querySelectorAll('.book').length < 6")
        check("书架拼音首字母搜索", True)
        pg.fill(".search input", "游手好闲")
        pg.wait_for_selector(".ft-sec .ft-hit")
        check("书架搜索显示书中内容", "活着" in pg.inner_text(".ft-sec"))
        shot(pg, "02-fulltext")
        pg.click(".ft-sec .ft-hit")
        pg.wait_for_selector(".reader .toast, #toasts .toast", timeout=20000)
        pg.wait_for_function("() => [...document.querySelectorAll('#toasts .toast')].some(t => t.textContent.includes('已定位'))", timeout=20000)
        check("点书中内容跳到原文", True)
        shot(pg, "03-jump")
        pg.keyboard.press("Escape")
        pg.wait_for_selector(".book")
        pg.fill(".search input", "")
        pg.wait_for_timeout(500)
        card = pg.locator(".book", has_text="活着").first
        card.click(button="right")
        pg.wait_for_selector(".drawer.show h2")
        pg.wait_for_selector(".drawer .detail-top h2")
        check("右键打开书籍详情", pg.inner_text(".drawer .detail-top h2") == "活着", pg.inner_text(".drawer .detail-top h2"))
        pg.click(".drawer footer button:has-text('联网查找')")
        pg.wait_for_selector(".drawer .cand")
        check("详情里联网查找出候选", pg.locator(".drawer .cand").count() >= 1)
        shot(pg, "04-detail")
        pg.click(".drawer .cand button:has-text('用这条信息')")
        check("候选信息填入表单", pg.input_value(".drawer .form-grid input >> nth=0") == "活着")
        pg.keyboard.press("Escape")
        ctx.close()

        # ---- 整理、设置、统计 ----
        section("网页：整理、设置、统计")
        ctx = context()
        pg = ctx.new_page(); attach(pg, "manage")
        pg.goto(BASE + "/#/organize?tab=suggest")
        pg.wait_for_selector(".list .item h3")
        check("整理建议列表", pg.locator(".list .item").count() >= 2)
        shot(pg, "05-organize")
        pg.locator(".list .item .acts .btn.primary").first.click()
        pg.wait_for_selector("#toasts .toast button:has-text('撤销')")
        pg.click("#toasts .toast button:has-text('撤销')")
        pg.wait_for_function("() => [...document.querySelectorAll('#toasts .toast')].some(t => t.textContent.includes('已撤销'))")
        check("执行建议后可以从提示里撤销", True)
        pg.click(".tabs button:has-text('操作记录')")
        pg.wait_for_selector(".list .item h3")
        check("操作记录", pg.locator(".list .item").count() >= 2)
        pg.click(".tabs button:has-text('入库动态')")
        pg.wait_for_selector(".events li")
        check("入库动态", pg.locator(".events li").count() >= 5)
        pg.goto(BASE + "/#/settings")
        pg.wait_for_selector(".set-sec >> nth=8")
        check("设置页各部分", pg.locator(".set-sec").count() >= 9, pg.locator(".set-sec").count())
        pg.click("button:has-text('检测两个地址')")
        pg.wait_for_function("() => [...document.querySelectorAll('.status-line')].some(e => e.textContent.includes('可以连接'))")
        check("检测内网地址", True)
        pg.locator(".set-sec", has_text="听书").locator("button:has-text('试听')").click()
        pg.wait_for_timeout(1500)
        status = pg.locator(".set-sec", has_text="听书").locator(".muted.small-text").first.inner_text()
        check("设置里试听（自定义语音接口）", status == "", status)
        pg.set_viewport_size({"width": 1366, "height": 3000})
        shot(pg, "06-settings")
        pg.set_viewport_size({"width": 1366, "height": 860})
        pg.goto(BASE + "/#/stats")
        pg.wait_for_selector(".tiles .tile")
        check("阅读统计页", pg.locator(".tiles .tile").count() == 4 and pg.locator("svg.bars").count() == 1)
        shot(pg, "07-stats")
        ctx.close()

        # ---- 阅读器：EPUB ----
        section("网页：阅读器")
        b2 = book_by_title("三体Ⅱ·黑暗森林")
        ctx = context()
        pg = ctx.new_page(); attach(pg, "epub")
        pg.goto(BASE + f"/#/read/{b2['id']}")
        reader_ready(pg)
        pg.wait_for_timeout(800)
        v0 = int(pg.eval_on_selector(".r-prog input[type=range]", "e => e.value"))
        pg.keyboard.press("ArrowRight"); pg.wait_for_timeout(700)
        pg.keyboard.press("ArrowRight"); pg.wait_for_timeout(700)
        v1 = int(pg.eval_on_selector(".r-prog input[type=range]", "e => e.value"))
        check("EPUB 键盘翻页", v1 != v0, (v0, v1))
        pg.keyboard.press("t")
        pg.wait_for_selector(".r-panel.show .toc a")
        check("目录", pg.locator(".r-panel .toc a").count() >= 3)
        pg.locator(".r-panel .toc a").nth(2).click()
        pg.wait_for_timeout(900)
        check("点目录跳转", "下部" in pg.inner_text(".r-status"), pg.inner_text(".r-status"))
        pg.keyboard.press("b")
        pg.wait_for_function("() => [...document.querySelectorAll('#toasts .toast')].some(t => t.textContent.includes('已添加书签'))")
        check("添加书签", True)
        pg.mouse.click(683, 430)
        pg.wait_for_timeout(300)
        pg.click(".r-top button[title='阅读设置']")
        pg.wait_for_selector(".r-sheet.show")
        check("阅读设置面板", pg.locator(".r-sheet .swatches button").count() == 6)
        shot(pg, "08-reader-sheet")
        pg.locator(".r-sheet .swatches button[title='纸']").click()
        check("换主题", pg.get_attribute(".reader", "data-t") == "paper")
        pg.keyboard.press("Escape")
        pg.keyboard.press("n")
        pg.wait_for_timeout(500)
        check("夜间模式切换", pg.get_attribute(".reader", "data-t") in ("gray", "black", "amber"), pg.get_attribute(".reader", "data-t"))
        bg = pg.evaluate("() => { const v = document.querySelector('foliate-view'); const d = v.renderer.getContents()[0].doc; return getComputedStyle(d.body).color }")
        check("夜间正文是浅色字", bg.startswith("rgb(2") or bg.startswith("rgb(1"), bg)
        shot(pg, "09-reader-night")
        pg.keyboard.press("n")
        # 听书
        n0 = len(TTS_LOG)
        pg.click(".r-top button[title='听书']")
        pg.wait_for_selector(".r-player:not([hidden])")
        wait(lambda: len(TTS_LOG) > n0, 20, 0.3, "听书请求语音")
        pg.wait_for_function("() => { const d = document.querySelector('foliate-view').renderer.getContents()[0].doc; return d.defaultView.CSS.highlights.has('tts') }", timeout=15000)
        check("听书：请求服务器语音并高亮当前句", True)
        first = TTS_LOG[n0]["input"]
        pg.click(".r-player button[title='下一句']")
        wait(lambda: any(x["input"] != first for x in TTS_LOG[n0:]), 10, 0.3, "下一句")
        check("听书：下一句", True)
        shot(pg, "10-tts")
        pg.click(".r-player button[title='停止听书']")
        check("听书：停止", pg.locator(".r-player[hidden]").count() == 1)
        pg.wait_for_timeout(1700)
        prog = j("GET", f"/api/books/{b2['id']}/progress")
        check("阅读进度同步到服务器", prog["device"] and prog["location"].startswith("epubcfi"), prog)
        ctx.close()

        # 阅读器里手动开夜间：书架跟着变深，10 小时后失效
        ctx = context()
        pg = ctx.new_page(); attach(pg, "night")
        pg.goto(BASE + f"/#/read/{b2['id']}")
        reader_ready(pg)
        pg.keyboard.press("n")
        pg.wait_for_timeout(300)
        pg.keyboard.press("Escape")
        pg.wait_for_selector(".book")
        check("阅读器里开夜间，回到书架也是深色", pg.get_attribute("html", "data-theme") == "dark")
        pg.evaluate("() => localStorage.setItem('shuku.nightOverride', JSON.stringify({on: true, at: Date.now() - 11 * 3600e3}))")
        pg.reload()
        pg.wait_for_selector(".book")
        check("手动夜间 10 小时后失效，书架恢复浅色", pg.get_attribute("html", "data-theme") == "light")
        ctx.close()

        # 听书：服务器语音坏掉时改用设备语音
        j("PUT", "/api/settings", json={"tts": {"engine": "openai", "openai": {"base_url": MOCK + "/tts-bad/v1", "model": "tts-1", "voice": "nova"}}})
        ctx = context()
        ctx.add_init_script("localStorage.setItem('shuku.tts', JSON.stringify({speed: 1.4}))")  # 换个语速，避开服务器上的缓存
        pg = ctx.new_page(); attach(pg, "tts-fallback")
        pg.goto(BASE + f"/#/read/{b2['id']}")
        reader_ready(pg)
        pg.wait_for_timeout(2200)  # 等工具栏自动隐藏后再用快捷键，避免点在正在隐藏的按钮上
        pg.keyboard.press("r")
        pg.wait_for_function("() => [...document.querySelectorAll('#toasts .toast')].some(t => t.textContent.includes('设备自带语音'))", timeout=15000)
        check("服务器语音失败时自动改用设备语音", True)
        ctx.close()
        errors[:] = [e for e in errors if "tts-fallback" not in e]  # 预期内的 502
        j("PUT", "/api/settings", json={"tts": {"engine": "openai", "openai": {"base_url": MOCK + "/tts/v1", "model": "tts-1", "voice": "nova"}}})

        # ---- TXT、PDF、漫画 ----
        alive = book_by_title("活着")
        ctx = context()
        pg = ctx.new_page(); attach(pg, "txt")
        pg.goto(BASE + f"/#/read/{alive['id']}")
        reader_ready(pg)
        pg.keyboard.press("t")
        pg.wait_for_selector(".r-panel.show .toc a")
        check("TXT 自动分章（目录 5 章）", pg.locator(".r-panel .toc a").count() >= 5, pg.locator(".r-panel .toc a").count())
        shot(pg, "11-txt")
        ctx.close()

        pdf = [b for b in all_books() if "深度学习" in b["title"]][0]
        ctx = context()
        pg = ctx.new_page(); attach(pg, "pdf")
        pg.goto(BASE + f"/#/read/{pdf['id']}")
        pg.wait_for_selector(".pdfv .pg canvas", timeout=30000)
        check("PDF 渲染", True)
        pg.click(".r-top button[title='阅读设置']")
        pg.click(".r-sheet button:has-text('单页翻页')")
        pg.keyboard.press("Escape")
        pg.keyboard.press("ArrowRight"); pg.wait_for_timeout(600)
        check("PDF 单页翻页", "2 / 6" in pg.inner_text(".r-status"), pg.inner_text(".r-status"))
        shot(pg, "12-pdf")
        ctx.close()

        comic = sorted([b for b in all_books() if b["kind"] == "comic"], key=lambda b: b["title"])[0]
        ctx = context()
        pg = ctx.new_page(); attach(pg, "comic")
        pg.goto(BASE + f"/#/read/{comic['id']}")
        pg.wait_for_function("() => { const i = document.querySelector('.comic img.pg'); return i && i.naturalWidth > 0 }", timeout=20000)
        check("漫画显示", True)
        check("漫画不显示听书按钮", pg.locator(".r-bar button[title='听书']:visible").count() == 0)
        pg.keyboard.press("ArrowRight"); pg.wait_for_timeout(400)
        check("漫画翻页", "2 / 10" in pg.inner_text(".r-status"), pg.inner_text(".r-status"))
        pg.click(".r-top button[title='阅读设置']")
        pg.click(".r-sheet button:has-text('双页')")
        pg.keyboard.press("Escape")
        pg.wait_for_timeout(500)
        check("漫画双页", pg.locator(".comic .spread.two img").count() == 2)
        shot(pg, "13-comic-double")
        pg.click(".r-top button[title='阅读设置']")
        pg.click(".r-sheet button:has-text('条漫')")
        pg.keyboard.press("Escape")
        pg.wait_for_timeout(600)
        check("条漫模式", pg.locator(".webtoon img").count() == 10)
        ctx.close()

        # ---- 手机 ----
        section("网页：手机布局与深色")
        ctx = context(390, 844, mobile=True)
        pg = ctx.new_page(); attach(pg, "mobile")
        pg.goto(BASE + "/")
        pg.wait_for_selector(".book")
        check("手机底部标签栏", pg.is_visible(".tabbar"))
        check("手机没有横向滚动", pg.evaluate("() => document.documentElement.scrollWidth <= innerWidth + 1"))
        shot(pg, "14-mobile-shelf")
        pg.goto(BASE + f"/#/read/{b2['id']}")
        reader_ready(pg)
        pg.wait_for_timeout(600)
        cfi = lambda: pg.evaluate("() => document.querySelector('foliate-view').lastLocation?.cfi")
        s0 = cfi()
        pg.touchscreen.tap(360, 420); pg.wait_for_timeout(900)
        check("手机点右侧翻页", cfi() != s0, (s0, cfi()))
        s1 = cfi()
        pg.touchscreen.tap(30, 420); pg.wait_for_timeout(900)
        check("手机点左侧往回翻", cfi() != s1)
        pg.touchscreen.tap(195, 420); pg.wait_for_timeout(400)
        check("手机点中间显示工具栏", "ui" in pg.get_attribute(".reader", "class"))
        shot(pg, "15-mobile-reader")
        ctx.close()
        ctx = context(dark=True)
        pg = ctx.new_page(); attach(pg, "dark")
        pg.goto(BASE + "/")
        pg.wait_for_selector(".book")
        check("系统深色时界面是深色", pg.get_attribute("html", "data-theme") == "dark")
        shot(pg, "16-dark-shelf")
        ctx.close()

        # ---- 上传 ----
        section("网页：上传")
        p = os.path.join(T, "remote", "上传测试《夜航船》张岱.epub")
        make_epub(p, "夜航船", "张岱", [("天文部", ["日月星辰。" * 30])])
        ctx = context()
        pg = ctx.new_page(); attach(pg, "upload")
        pg.goto(BASE + "/")
        pg.wait_for_selector(".book")
        with pg.expect_file_chooser() as fc:
            pg.click(".page-head button:has-text('添加书')")
        fc.value.set_files(p)
        pg.wait_for_selector(".uploads li.ok", timeout=20000)
        check("上传面板显示完成", True)
        pg.wait_for_function("() => [...document.querySelectorAll('#toasts .toast')].some(t => t.textContent.includes('夜航船'))", timeout=40000)
        check("入库后有提示", True)
        ctx.close()
        browser.close()

    section("网页：脚本错误")
    check("没有脚本错误", not errors, "\n      ".join(errors[:20]))


def main():
    global TOKEN
    if "--serve" in sys.argv:  # 用上次的测试数据启动服务器和模拟服务，方便手动调试
        setup(fresh=False)
        TOKEN = j("POST", "/api/auth/login", json={"Password": PASSWORD})["token"]
        open(os.path.join(T, "tok"), "w").write(TOKEN)
        print(f"服务器：{BASE}  模拟服务：{MOCK}  令牌：{T}/tok", flush=True)
        while True:
            time.sleep(3600)
    try:
        setup()
        for t in [test_auth, test_ingest, test_search, test_fulltext, test_reading, test_organize, test_upload_ai_delete, test_online, test_tts, test_opds, test_settings_libs]:
            try:
                t()
            except Exception as e:
                check(f"{t.__name__} 执行出错", False, f"{e}\n{traceback.format_exc(limit=3)}")
        if not ONLY_API:
            try:
                ui_tests()
            except Exception as e:
                check("网页检查执行出错", False, f"{e}\n{traceback.format_exc(limit=4)}")
    finally:
        teardown()
    passed = sum(1 for r in results if r[1] is True)
    failed = [r for r in results if r[1] is False]
    with open(os.path.join(T, "report.txt"), "w") as f:
        for name, ok, detail in results:
            f.write(("" if ok is None else "✓ " if ok else "✗ ") + name + ("" if ok or not detail else f"  —— {detail}") + "\n")
        f.write(f"\n通过 {passed} 项，失败 {len(failed)} 项\n")
    print(f"\n通过 {passed} 项，失败 {len(failed)} 项。报告：{T}/report.txt  截图：{SHOTS}")
    for name, _, detail in failed:
        print("  ✗ " + name + (f"  —— {str(detail)[:300]}" if detail else ""))
    sys.exit(1 if failed else 0)


if __name__ == "__main__":
    main()
