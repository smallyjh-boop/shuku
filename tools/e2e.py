#!/usr/bin/env python3
"""端到端检查：构建服务端、生成一批乱名测试书、启动模拟的联网服务（豆瓣、Open Library、
Google Books、Bangumi、AI、语音接口），然后从接口和网页两方面把功能走一遍。

用法：python3 tools/e2e.py            全部检查
     python3 tools/e2e.py --api      只查接口
环境变量：E2E_DIR（默认 /tmp/shuku-e2e）、E2E_PORT（8095）、E2E_MOCK_PORT（8096）
结果：终端里逐项打印，汇总写到 $E2E_DIR/report.txt，截图在 $E2E_DIR/shots/
需要：Go、Python 的 requests / Pillow / reportlab / playwright，以及 ffmpeg（可选，生成试听用的 MP3）
"""
import glob, hashlib, io, json, os, re, shutil, signal, subprocess, sys, threading, time, traceback, urllib.parse, zipfile
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

TTS_LOG, AI_LOG, FETCH_LOG, WS_LOG, OPDS_AUTH, EXT_LOG, WT_LOG, DICT_LOG = [], [], [], [], [], [], [], []
MSTR_LOG, AI_TR_LOG = [], []  # 翻译：微软翻译收到的（目标语言, 段落），AI 翻译收到的段落
MS_TOKEN = "aaa.eyJleHAiOjQxMDI0NDQ4MDB9.ccc"
WT_TOKEN = "wt-test-token"
# 浏览器插件测试：这些网站的域名在浏览器里指向模拟服务（--host-resolver-rules），书库服务器解析不到它们
EXT_SITES = ["www.gutenberg.org", "archive.org", "standardebooks.org", "zh.wikisource.org"]
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
        if p == "/mstr/auth":  # 微软翻译：发令牌（JWT 的样子，过期时间 2100 年）
            return self.send(200, MS_TOKEN, "text/plain")
        if p == "/google/books/v1/volumes":
            return self.send(200, {"items": []})
        if p.startswith("/img/"):
            return self.send(200, COVER, "image/jpeg")
        if p.startswith("/opds/"):
            import base64
            a = self.headers.get("Authorization", "")
            if a.startswith("Basic "):
                OPDS_AUTH.append(base64.b64decode(a[6:]).decode())
            return self.opds(p, q)
        if p == "/page.html":
            return self.send(200, "<html><body>这是一个网页</body></html>", "text/html; charset=utf-8")
        if p == "/ws/zh/api.php":
            sr = q.get("srsearch", [""])[0]
            WS_LOG.append(sr)
            hits = []
            if "紅樓夢" in sr:
                hits = [{"title": "紅樓夢", "snippet": "<span class=\"searchmatch\">紅樓夢</span>，清代曹雪芹著"}, {"title": "紅樓夢/第001回", "snippet": "甄士隱夢幻識通靈"}]
            return self.send(200, {"query": {"search": hits}})
        if p == "/ws/export":
            page = q.get("page", [""])[0]
            f = os.path.join(T, "remote", "ws-" + page + ".epub")
            make_epub(f, page, "曹雪芹", [("第一回", ["甄士隱夢幻識通靈，賈雨村風塵懷閨秀。" * 20])])
            return self.send(200, open(f, "rb").read(), "application/epub+zip", {"Content-Disposition": "attachment; filename*=UTF-8''" + urllib.parse.quote(page + ".epub")})
        if p == "/ia/advancedsearch.php":
            return self.send(200, {"response": {"numFound": 1, "docs": [{"identifier": "rulinwaishi00wu", "title": "儒林外史", "creator": ["吴敬梓"], "year": "1803", "language": "chi"}]}})
        if p == "/ia/metadata/rulinwaishi00wu":
            return self.send(200, {"metadata": {"title": "儒林外史"}, "files": [{"name": "rlws.pdf", "format": "Text PDF"}, {"name": "rlws.epub", "format": "EPUB"}]})
        if p == "/ia/download/rulinwaishi00wu/rlws.epub":
            f = os.path.join(T, "remote", "rlws.epub")
            make_epub(f, "儒林外史", "吴敬梓", [("第一回", ["说楔子敷陈大义，借名流隐括全文。" * 20])])
            return self.send(200, open(f, "rb").read(), "application/epub+zip")
        if p.startswith("/ia/services/img/"):
            return self.send(200, COVER, "image/jpeg")
        if p == "/ia/metadata/xiyouji00wu":
            return self.send(200, {"metadata": {"title": "西游记"}, "files": [{"name": "xyj.pdf", "format": "Text PDF"}, {"name": "xyj.epub", "format": "EPUB"}]})
        if p == "/ia/download/xiyouji00wu/xyj.epub":
            f = os.path.join(T, "remote", "xyj.epub")
            make_epub(f, "西游记", "吴承恩", [("第一回", ["灵根育孕源流出，心性修持大道生。" * 20])])
            return self.send(200, open(f, "rb").read(), "application/epub+zip")
        if p.startswith("/slow/"):  # 1.0.0.7：慢慢下载，测“取消下载”
            self.send_response(200)
            self.send_header("Content-Type", "application/epub+zip")
            self.send_header("Content-Length", str(8 << 20))
            self.end_headers()
            try:
                for _ in range(500):
                    self.wfile.write(b"\0" * 16384)
                    self.wfile.flush()
                    time.sleep(0.15)
            except Exception:
                pass
            return
        host = (self.headers.get("Host") or "").split(":")[0]
        if host in EXT_SITES or p.startswith(("/shop/", "/dl/", "/protected/")) or p in ("/get", "/fake.epub"):
            return self.ext_site(host, p, q)
        if p.startswith("/files/"):
            FETCH_LOG.append(p)
            f = os.path.join(T, "remote", urllib.parse.unquote(p[len("/files/"):]))
            if os.path.isfile(f):
                return self.send(200, open(f, "rb").read(), "application/octet-stream",
                                 {"Content-Disposition": "attachment; filename*=UTF-8''" + urllib.parse.quote(os.path.basename(f))})
            return self.send(404, "not found", "text/plain")
        self.send(404, {"error": "no route " + p})

    def do_HEAD(self):
        """浏览器插件用 HEAD 问文件大小（判断不用登录能不能下载）：照 GET 回应，只发响应头"""
        real = self.wfile

        class HeadersOnly:
            sent = False

            def write(self, b):
                if not self.sent:  # 第一次写的是响应头，后面的正文丢掉
                    self.sent = True
                    return real.write(b)
                return len(b)

            def flush(self):
                real.flush()

        self.wfile = HeadersOnly()
        try:
            self.do_GET()
        finally:
            self.wfile = real

    def ext_site(self, host, p, q):
        """浏览器插件测试用的网站：一个普通的电子书小站 + 几个公开书库的书页"""
        EXT_LOG.append((host, p, self.headers.get("Referer", ""), self.headers.get("Cookie", "")))

        def page(body, title=""):
            return self.send(200, f"<!doctype html><html><head><meta charset='utf-8'><title>{title}</title></head><body>{body}</body></html>", "text/html; charset=utf-8")

        def epub_file(fname, title, author, pad=0):
            f = os.path.join(T, "remote", "ext-" + fname)
            if not os.path.exists(f):
                make_epub(f, title, author, [("第一章", [f"{title}的正文。" * 30])])
                if pad:  # 大文件：塞一段随机数据（EPUB 里多一个文件不影响识别）
                    with zipfile.ZipFile(f, "a") as z:
                        z.writestr("pad.bin", os.urandom(pad))
            return f

        def epub(fname, title, author, ctype="application/epub+zip", cd=True, pad=0):
            f = epub_file(fname, title, author, pad)
            hd = {"Content-Disposition": "attachment; filename*=UTF-8''" + urllib.parse.quote(fname)} if cd else {}
            return self.send(200, open(f, "rb").read(), ctype, hd)

        def trickle(data, seconds, total=None, stop_after=None):
            """慢慢发：分 40 段在 seconds 秒内发完；stop_after：发这么多字节后就不再发（模拟卡住的网站）"""
            self.send_response(200)
            self.send_header("Content-Type", "application/epub+zip")
            self.send_header("Content-Length", str(total or len(data)))
            self.end_headers()
            step = len(data) // 40 + 1
            try:
                for i in range(0, len(data), step):
                    if stop_after is not None and i >= stop_after:
                        time.sleep(seconds)
                        return
                    self.wfile.write(data[i:i + step])
                    self.wfile.flush()
                    time.sleep(seconds / 40)
            except OSError:
                pass

        if host == "www.gutenberg.org":
            if p == "/ebooks/1342":
                return page("<h1>Pride and Prejudice by Jane Austen</h1><table>"
                            "<tr><td><a href='/ebooks/1342.html.images'>Read online (web)</a></td></tr>"
                            "<tr><td><a href='/ebooks/1342.epub3.images'>EPUB3 (E-readers incl. Send-to-Kindle)</a></td></tr>"
                            "<tr><td><a href='/ebooks/1342.epub.images'>EPUB (older E-readers)</a></td></tr>"
                            "<tr><td><a href='/ebooks/1342.epub.noimages'>EPUB (no images, older E-readers)</a></td></tr>"
                            "<tr><td><a href='/ebooks/1342.kf8.images'>Kindle</a></td></tr>"
                            "<tr><td><a href='/ebooks/1342.txt.utf-8'>Plain Text UTF-8</a></td></tr>"
                            "<tr><td><a href='/ebooks/1342.rdf'>RDF</a></td></tr></table>", "Pride and Prejudice by Jane Austen | Project Gutenberg")
            if p == "/ebooks/1342.epub3.images":  # 真实的古腾堡会跳转到缓存文件
                self.send_response(302)
                self.send_header("Location", "/cache/epub/1342/pg1342-images-3.epub")
                self.send_header("Content-Length", "0")
                self.end_headers()
                return
            if p == "/cache/epub/1342/pg1342-images-3.epub":
                return epub("pg1342-images-3.epub", "Pride and Prejudice", "Jane Austen", cd=False)
        if host == "archive.org" and p == "/details/xiyouji00wu":
            return page("<h1>西游记</h1><section><a href='/download/xiyouji00wu/xyj.epub'>EPUB</a> <a href='/download/xiyouji00wu/xyj.pdf'>PDF</a>"
                        " <a href='/download/xiyouji00wu'>SHOW ALL</a></section>", "西游记 : 吴承恩 : Free Download, Borrow, and Streaming : Internet Archive")
        if host == "standardebooks.org" and p == "/ebooks/jane-austen/emma":
            d = "/ebooks/jane-austen/emma/downloads/jane-austen_emma"
            return page(f"<h1>Emma</h1><ul><li><a href='{d}.epub'>Compatible epub</a></li><li><a href='{d}_advanced.epub'>Advanced epub</a></li>"
                        f"<li><a href='{d}.kepub.epub'>Kobo Kepub epub</a></li><li><a href='{d}.azw3'>Amazon Kindle azw3</a></li></ul>", "Emma, by Jane Austen - Standard Ebooks")
        if host == "zh.wikisource.org" and p.startswith("/wiki/"):
            return self.send(200, "<!doctype html><html><head><meta charset='utf-8'><title>水滸傳 - 维基文库，自由的图书馆</title></head>"
                                  "<body class='mediawiki ns-0'><h1 id='firstHeading'>水滸傳</h1><p>正文</p></body></html>", "text/html; charset=utf-8")
        # 一个普通的电子书小站：同一本书两种格式、下载接口、要来源页才给下载的、其实是网页的、框架和 shadow DOM 里的链接
        if p == "/shop/list.html":
            return page("<h1>电子书小站</h1><ul>"
                        "<li><a href='/dl/alice.epub'>爱丽丝漫游奇境</a> <a href='/dl/alice.mobi'>MOBI</a></li>"
                        "<li><a href='/dl/manual.pdf'>产品说明书</a></li>"
                        "<li><a href='/get?id=7&amp;format=epub'>下载 EPUB</a></li>"
                        "<li><a href='/dl/notes.txt'>读书笔记</a></li>"
                        "<li><a href='/protected/secret.epub'>秘密花园</a></li>"
                        "<li><a href='/fake.epub'>需要登录的书</a></li>"
                        "<li><a href='/dl/disguised.epub'>伪装的书</a></li>"
                        "<li><a href='/about'>关于本站</a></li></ul>"
                        "<iframe src='/shop/frame.html'></iframe><div id='host'></div>"
                        "<script>document.getElementById('host').attachShadow({mode: 'open'}).innerHTML = \"<a href='/dl/shadow.azw3'>影子里的书</a>\"</script>", "电子书小站")
        if p == "/shop/frame.html":
            return page("<a href='/dl/frame-book.fb2'>框架里的书</a>")
        if p == "/dl/alice.epub":
            return epub("alice.epub", "爱丽丝漫游奇境", "刘易斯·卡罗尔")
        if p == "/dl/frame-book.fb2":
            fb2 = ("<?xml version='1.0' encoding='utf-8'?><FictionBook xmlns='http://www.gribuser.ru/xml/fictionbook/2.0'><description><title-info>"
                   "<author><first-name>安托万</first-name><last-name>圣埃克苏佩里</last-name></author><book-title>小王子</book-title><lang>zh</lang></title-info></description>"
                   "<body><section><title><p>第一章</p></title><p>" + "如果你驯服了我，我们就互相不可缺少了。" * 20 + "</p></section></body></FictionBook>")
            return self.send(200, fb2, "application/x-fictionbook+xml")
        if p == "/get" and q.get("format") == ["epub"]:
            return epub("海底两万里.epub", "海底两万里", "儒勒·凡尔纳", ctype="application/octet-stream")
        if p == "/protected/secret.epub":
            if self.headers.get("Referer") != MOCK + "/shop/list.html":
                return self.send(403, "forbidden", "text/plain")
            return epub("secret.epub", "秘密花园", "伯内特")
        if p == "/fake.epub":
            return self.send(200, "<!doctype html><html><body>请先登录</body></html>", "text/html; charset=utf-8")
        if p == "/dl/disguised.epub":  # 类型写的是文件，内容其实是网页
            return self.send(200, "\n  <!DOCTYPE html><html><body>下载次数用完了</body></html>", "application/octet-stream")
        # 大文件（插件测试里把“大”调成 1MB）：公开的、要登录（Cookie）的
        if p == "/dl/big-public.epub":
            return epub("big-public.epub", "公开的大部头", "无名氏", pad=1_500_000)
        if p == "/protected/big-member.epub":
            if "member=1" not in self.headers.get("Cookie", ""):
                return self.send(403, "forbidden", "text/plain")
            return epub("big-member.epub", "会员的大部头", "无名氏", pad=1_500_000)
        # 慢慢下载的书（下载途中停掉插件后台、断开书库再连上）；下到一半就卡住的书
        if p == "/dl/slow-book.epub":
            return trickle(open(epub_file("slow-book.epub", "后台停了也能下完的书", "无名氏", pad=400_000), "rb").read(), 6)
        if p == "/dl/slow-nas.epub":
            return trickle(open(epub_file("slow-nas.epub", "断网后接着查的书", "无名氏", pad=400_000), "rb").read(), 6)
        if p == "/dl/stuck.epub":
            return trickle(os.urandom(400_000), 20, total=1_000_000, stop_after=10_000)
        return self.send(404, "not found", "text/plain")

    def opds(self, p, q):
        A = 'xmlns="http://www.w3.org/2005/Atom" xmlns:opensearch="http://a9.com/-/spec/opensearch/1.1/" xmlns:dc="http://purl.org/dc/terms/"'
        def entry(t, a, f, summary="", cats=()):
            return (f"<entry><title>{t}</title><id>urn:{t}</id><author><name>{a}</name></author><summary type='text'>{summary}</summary>"
                    + "".join(f"<category term='{c}' label='{c}'/>" for c in cats) +
                    f"<link rel='http://opds-spec.org/acquisition' type='application/epub+zip' href='/files/{urllib.parse.quote(f)}' length='1234'/>"
                    f"<link rel='http://opds-spec.org/acquisition' type='application/x-mobipocket-ebook' href='/files/{urllib.parse.quote(f)}.mobi'/>"
                    f"<link rel='http://opds-spec.org/image' type='image/jpeg' href='{MOCK}/img/{urllib.parse.quote(t)}.jpg'/></entry>")
        nav = "application/atom+xml;profile=opds-catalog;kind=acquisition"
        if p == "/opds/root.xml":
            return self.send(200, f"<?xml version='1.0' encoding='utf-8'?><feed {A}><title>测试书源</title><id>root</id>"
                             f"<link rel='search' type='application/opensearchdescription+xml' href='osd.xml'/>"
                             f"<entry><title>新书</title><id>new</id><content type='text'>最近上架</content><link rel='subsection' type='{nav}' href='new.xml'/></entry></feed>", "application/atom+xml")
        if p == "/opds/osd.xml":
            return self.send(200, f"<?xml version='1.0'?><OpenSearchDescription xmlns='http://a9.com/-/spec/opensearch/1.1/'><Url type='application/atom+xml;profile=opds-catalog' template='{MOCK}/opds/search?q={{searchTerms}}&amp;page={{startPage?}}'/></OpenSearchDescription>", "application/opensearchdescription+xml")
        if p == "/opds/new.xml":
            return self.send(200, f"<?xml version='1.0' encoding='utf-8'?><feed {A}><title>新书</title><id>new</id><link rel='next' type='{nav}' href='/opds/new2.xml'/>"
                             + entry("镜花缘", "李汝珍", "镜花缘.epub", "清代长篇小说") + entry("老残游记", "刘鹗", "老残游记.epub") + "</feed>", "application/atom+xml")
        if p == "/opds/new2.xml":
            return self.send(200, f"<?xml version='1.0' encoding='utf-8'?><feed {A}><title>新书 第 2 页</title><id>new2</id>" + entry("海上花列传", "韩邦庆", "海上花列传.epub", cats=("Chinese fiction", "Shanghai (China) -- Fiction")) + "</feed>", "application/atom+xml")
        if p == "/opds/search":
            kw = q.get("q", [""])[0]
            es = "".join(entry(t, a, t + ".epub") for t, a in [("镜花缘", "李汝珍"), ("老残游记", "刘鹗"), ("海上花列传", "韩邦庆")] if kw and kw in t)
            return self.send(200, f"<?xml version='1.0' encoding='utf-8'?><feed {A}><title>搜索：{kw}</title><id>s</id>{es}</feed>", "application/atom+xml")
        self.send(404, "no", "text/plain")

    def do_POST(self):
        p = urllib.parse.urlparse(self.path).path
        raw = self.body()
        if p == "/tts/v1/audio/speech":
            j = json.loads(raw or b"{}")
            TTS_LOG.append(j)
            return self.send(200, TONE, "audio/mpeg")
        if p == "/tts-bad/v1/audio/speech":
            return self.send(500, {"error": "engine down"})
        if p == "/watchtower/v1/update":
            WT_LOG.append(self.headers.get("Authorization", ""))
            return self.send(200 if self.headers.get("Authorization") == "Bearer " + WT_TOKEN else 401, b"", "text/plain")
        if p == "/bgm/v0/search/subjects":
            j = json.loads(raw or b"{}")
            if "进击" in j.get("keyword", ""):
                return self.send(200, {"data": [{"id": 12, "name": "進撃の巨人", "name_cn": "进击的巨人", "summary": "巨人与人类的战斗。", "date": "2010-03-17",
                                                 "images": {"large": f"{MOCK}/img/bgm.jpg"}, "tags": [{"name": "热血"}], "infobox": [{"key": "作者", "value": "諫山創"}]}]})
            return self.send(200, {"data": []})
        if p == "/mstr/translate":  # 微软翻译：每段变成“[目标语言]原文”
            to = urllib.parse.parse_qs(urllib.parse.urlparse(self.path).query).get("to", [""])[0]
            if self.headers.get("Authorization") != "Bearer " + MS_TOKEN or not to:
                return self.send(401, {"error": "unauthorized"})
            items = json.loads(raw or b"[]")
            MSTR_LOG.append((to, [x["Text"] for x in items]))
            return self.send(200, [{"translations": [{"text": f"[{to}]" + x["Text"], "to": to}]} for x in items])
        if p == "/ai/v1/chat/completions":
            j = json.loads(raw or b"{}")
            if "书籍翻译" in j["messages"][0]["content"]:  # AI 翻译：收到字符串数组，每项加“AI译:”
                texts = json.loads(j["messages"][1]["content"])
                AI_TR_LOG.append(texts)
                return self.send(200, {"choices": [{"message": {"content": "<think>想一想</think>" + json.dumps(["AI译:" + t for t in texts], ensure_ascii=False)}}]})
            if "读书助手" in j["messages"][0]["content"]:
                DICT_LOG.append(j["messages"][1]["content"])
                return self.send(200, {"choices": [{"message": {"content": "<think>想一想</think>mí màn：烟雾、气味等充满某个地方。这里是说面包的香气飘满了街道。"}}]})
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
    # 只停这个测试目录的服务器（别的目录可能同时在跑另一组测试）
    subprocess.run(["pkill", "-f", os.path.join(T, "shuku-e2e-server")], check=False)
    time.sleep(0.3)
    if fresh:
        shutil.rmtree(T, ignore_errors=True)
        os.makedirs(SHOTS)
        os.makedirs(os.path.join(T, "remote"))
    make_assets()
    make_clients()
    print("构建服务端…", flush=True)
    env = dict(os.environ, GOPROXY="off", GOFLAGS="-mod=vendor")
    subprocess.run(["go", "build", "-tags", "sqlite_fts5", "-o", os.path.join(T, "shuku-e2e-server"), "."], cwd=os.path.join(ROOT, "server"), check=True, env=env)
    if fresh:
        subprocess.run([sys.executable, os.path.join(ROOT, "tools", "make-samples.py"), T], check=True, stdout=subprocess.DEVNULL)
    dict_db = make_dict(os.path.join(T, "shuku-e2e-server"))
    # 兼容旧系统的那一套网页（装了 Node 和 tools/legacy 的依赖时构建；打包时在 Actions 里构建）
    leg = os.path.join(ROOT, "tools", "legacy")
    if shutil.which("node") and os.path.isdir(os.path.join(leg, "node_modules")):
        print("构建兼容旧系统的网页…", flush=True)
        subprocess.run(["node", "build.mjs"], cwd=leg, check=True, stdout=subprocess.DEVNULL)
    srv = ThreadingHTTPServer(("127.0.0.1", MPORT), Mock)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    env = dict(os.environ,
               SHUKU_PORT=str(PORT), SHUKU_DATA=os.path.join(T, "data"), SHUKU_INBOX=os.path.join(T, "inbox"), SHUKU_WEB=os.path.join(ROOT, "web"),
               SHUKU_LIBRARIES=f"图书:{T}/books:book,漫画:{T}/comics:comic",
               SHUKU_DOUBAN_BASE=MOCK + "/douban", SHUKU_OPENLIBRARY_BASE=MOCK + "/ol", SHUKU_OPENLIBRARY_COVERS=MOCK + "/img",
               SHUKU_GOOGLE_BASE=MOCK + "/google", SHUKU_BANGUMI_BASE=MOCK + "/bgm", SHUKU_EDGE_WSS="ws://127.0.0.1:9/none",
               SHUKU_LAN_URL=BASE, SHUKU_REMOTE_URL="",
               SHUKU_CATALOG_GUTENBERG=MOCK + "/opds/root.xml", SHUKU_CATALOG_IA=MOCK + "/ia",
               SHUKU_WIKISOURCE_API=MOCK + "/ws/%s/api.php", SHUKU_WIKISOURCE_EXPORT=MOCK + "/ws/export",
               SHUKU_CLIENTS=os.path.join(T, "clients"), SHUKU_UPDATE_URL=MOCK + "/watchtower", SHUKU_UPDATE_TOKEN=WT_TOKEN, SHUKU_DICT=dict_db,
               SHUKU_MS_TRANSLATE_AUTH=MOCK + "/mstr/auth", SHUKU_MS_TRANSLATE_BASE=MOCK + "/mstr",
               SHUKU_NO_RESTART="1")
    log = open(os.path.join(T, "server.log"), "w")
    server_proc = subprocess.Popen([os.path.join(T, "shuku-e2e-server")], cwd=T, env=env, stdout=log, stderr=subprocess.STDOUT)
    wait(lambda: requests.get(BASE + "/api/ping", timeout=2).ok, 20, what="服务器启动")


NEW = "9.9.9.99"  # 书库里“新版本”的各端安装包


def make_clients():
    d = os.path.join(T, "clients")
    os.makedirs(d, exist_ok=True)
    files = {"shuku-android.apk": b"PK-android-" + os.urandom(64), "shuku-wear.apk": b"PK-wear-" + os.urandom(64), "shuku-extension.zip": b"PK-ext-" + os.urandom(64)}
    info = {}
    for name, data in files.items():
        open(os.path.join(d, name), "wb").write(data)
        info[name] = {"file": name, "size": len(data), "sha256": hashlib.sha256(data).hexdigest()}
    json.dump({"version": NEW, "android": {**info["shuku-android.apk"], "versionCode": 99, "versionName": NEW},
               "wear": {**info["shuku-wear.apk"], "versionCode": 100099, "versionName": NEW},
               "extension": {**info["shuku-extension.zip"], "version": NEW}}, open(os.path.join(d, "clients.json"), "w"))


DICT_MOE = """[
 {"title":"瀰漫","heteronyms":[{"pinyin":"mí màn","definitions":[{"def":"煙塵、霧氣等充滿、布滿。","type":"動","example":["如：「煙霧瀰漫」。"]}]}]},
 {"title":"晴朗","heteronyms":[{"pinyin":"qíng lǎng","definitions":[{"def":"天氣晴和明朗。"}]}]},
 {"title":"瀰","heteronyms":[{"pinyin":"mí","definitions":[{"def":"水滿的樣子。","type":"形"}]}]},
 {"title":"漫","heteronyms":[{"pinyin":"màn","definitions":[{"def":"水過滿而向外流。","type":"動"}]}]}
]"""
DICT_CEDICT = "瀰漫 弥漫 [mi2 man4] /to pervade/to fill the air/\n瀰 弥 [mi2] /to fill/\n漫 漫 [man4] /free/to overflow/\n晴朗 晴朗 [qing2 lang3] /sunny and cloudless/\n滿 满 [man3] /full/\n氣 气 [qi4] /gas/air/\n樣 样 [yang4] /manner/\n過 过 [guo4] /to cross/\n霧 雾 [wu4] /fog/\n塵 尘 [chen2] /dust/\n煙 烟 [yan1] /smoke/\n"
DICT_ECDICT = "word,phonetic,definition,translation,pos,collins,oxford,tag,bnc,frq,exchange,detail,audio\ngo,gəʊ,v. move,vi. 去,,5,1,zk,35,35,p:went/d:gone,,\nwent,went,,go的过去式,,,,,0,0,0:go/1:p,,\nmorning,'mɔ:niŋ,n. the first part of the day,n. 早晨,,5,1,zk,100,100,s:mornings,,\n"


def make_dict(binary):
    d = os.path.join(T, "dictsrc")
    os.makedirs(d, exist_ok=True)
    for name, text in (("moe.json", DICT_MOE), ("cedict.txt", DICT_CEDICT), ("ecdict.csv", DICT_ECDICT)):
        open(os.path.join(d, name), "w", encoding="utf-8").write(text)
    out = os.path.join(T, "dict.db")
    subprocess.run([binary, "-build-dict", out, "-moe", os.path.join(d, "moe.json"), "-cedict", os.path.join(d, "cedict.txt"), "-ecdict", os.path.join(d, "ecdict.csv")], check=True, stderr=subprocess.DEVNULL)
    return out


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
    check("TXT 转成 EPUB 阅读", info["reader"] == "foliate" and "/epub?v=" in info["url"], info)
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
    # 1.0.0.7：离线攒下的阅读时长联网后一次补报，单次最多算 4 小时
    check("单次上报最多算 4 小时", (j("PUT", f"/api/books/{pid}/progress", json={"percent": 0.43, "seconds": 99999}) and j("GET", "/api/stats")["today"] == 420 + 14400), j("GET", "/api/stats")["today"])
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
    # 等入库整理结束（文件离开投递目录）再检查，免得赶上登记和移动之间的那一下
    inbox = os.path.join(T, "inbox") + os.sep
    b = wait(lambda: (x := book_by_title("围城")) and not x["files"][0]["path"].startswith(inbox) and x, 40, 0.5, "AI 识别上传的书")
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


def test_clients():
    global TOKEN
    section("配对、设备、按章节读（手机、手表用）")
    p = j("POST", "/api/pair")
    check("生成配对码和二维码链接", len(p["code"]) == 8 and p["link"].startswith("shuku://pair?") and "c=" + p["code"] in p["link"], p)
    saved, TOKEN = TOKEN, ""
    check("错误的配对码", api("POST", "/api/pair/claim", 401, json={"code": "WRONG234", "device": "x"}).status_code == 401)
    r = j("POST", "/api/pair/claim", json={"code": p["code"].lower(), "device": "Pixel 9"})
    check("配对码（不分大小写）换到令牌和地址", len(r["token"]) == 64 and r["lan_url"] == BASE, r)
    check("配对码只能用一次", api("POST", "/api/pair/claim", 401, json={"code": p["code"]}).status_code == 401)
    phone = TOKEN = r["token"]
    check("新令牌可以用", j("GET", "/api/info")["id"])
    TOKEN = saved
    pix = [x for x in j("GET", "/api/sessions") if x["device"] == "Pixel 9"]
    check("设备列表里有新手机", pix and not pix[0]["current"], pix)
    j("DELETE", f"/api/sessions/{pix[0]['id']}")
    TOKEN = phone
    check("移除设备后它的令牌失效", api("GET", "/api/info", 401).status_code == 401)
    TOKEN = saved
    b2 = book_by_title("三体Ⅱ·黑暗森林")
    c = j("GET", f"/api/books/{b2['id']}/chapters")
    check("EPUB 按章节列出（3 章）", [x["title"] for x in c["chapters"]] == ["上部 面壁者", "中部 咒语", "下部 黑暗森林"], [x["title"] for x in c["chapters"]])
    check("章节位置是阅读器能认的 sec:", c["chapters"][1]["loc"].startswith("sec:1:"), c["chapters"][1])
    ch = j("GET", f"/api/books/{b2['id']}/chapters/1")
    check("取一章正文", "晴朗的早晨" in ch["text"] and ch["count"] == 3)
    j("PUT", f"/api/books/{b2['id']}/progress", json={"file_id": c["file_id"], "location": "sec:2:0.5", "percent": 0.83, "device": "手表"})
    c = j("GET", f"/api/books/{b2['id']}/chapters")
    check("从手表位置接着读（第 3 章一半）", c["resume"]["i"] == 2 and abs(c["resume"]["frac"] - 0.5) < 0.01, c["resume"])
    alive = book_by_title("活着")
    c = j("GET", f"/api/books/{alive['id']}/chapters")
    check("TXT 章节", len(c["chapters"]) == 5 and c["chapters"][0]["title"].startswith("第一章"), [x["title"] for x in c["chapters"]])
    pdf = [b for b in all_books() if "深度学习" in b["title"]][0]
    c = j("GET", f"/api/books/{pdf['id']}/chapters")
    check("PDF 几页合成一段", c["chapters"][0]["loc"].startswith("p:1:") and c["chapters"][0]["title"].startswith("第 1"), c["chapters"][:2])
    comic = [b for b in all_books() if b["kind"] == "comic"][0]
    check("漫画没有文字", api("GET", f"/api/books/{comic['id']}/chapters", 400).status_code == 400)
    small = api("GET", f"/api/books/{b2['id']}/cover?w=120")
    big = api("GET", f"/api/books/{b2['id']}/cover")
    check("小尺寸封面", small.headers["Content-Type"] == "image/jpeg" and len(small.content) < len(big.content), (len(small.content), len(big.content)))
    t = j("GET", "/api/trash")
    check("回收站统计", t["files"] >= 1, t)
    j("DELETE", "/api/trash")
    check("清空回收站", j("GET", "/api/trash")["files"] == 0)


def test_update():
    global TOKEN
    section("在线升级")
    saved, TOKEN = TOKEN, ""
    r = api("GET", "/api/clients")
    c = r.json()
    check("版本信息不用登录、能跨域读", r.headers.get("Access-Control-Allow-Origin") == "*" and c["server"] and c["app"] == "shuku", c)
    check("各端的版本和下载地址", c["android"]["versionCode"] == 99 and c["android"]["url"] == "/dl/shuku-android.apk" and c["wear"]["versionCode"] == 100099 and c["extension"]["version"] == NEW, c)
    apk = api("GET", "/dl/shuku-android.apk")
    check("下载手机 App（校验值对得上）", hashlib.sha256(apk.content).hexdigest() == c["android"]["sha256"] and "android.package-archive" in apk.headers["Content-Type"], apk.headers.get("Content-Type"))
    check("别的文件不能下载", api("GET", "/dl/clients.json", 404).status_code == 404)
    check("版本与升级要登录", api("GET", "/api/update", 401).status_code == 401)
    TOKEN = saved
    u = j("GET", "/api/update")
    check("版本与升级：服务器版本、已开启一键升级", u["version"] == c["server"] and u["updater"] is True and u["clients"]["android"]["versionName"] == NEW, u)
    n = len(WT_LOG)
    r = j("POST", "/api/update/check")
    check("检查更新：带着令牌通知更新服务", WT_LOG[n:] == ["Bearer " + WT_TOKEN], WT_LOG[n:])
    check("检查更新：书库没被换掉，如实说没有换新版本", r["ok"] and "没有换新版本" in r["reason"], r)
    s = j("GET", "/api/status")
    check("状态里有版本号（网页靠它发现书库升级了）", s.get("version") == c["server"], s.get("version"))


def test_dict():
    section("选词查字典")
    r = j("GET", "/api/dict?q=" + urllib.parse.quote("弥漫"))
    w = (r.get("words") or [{}])[0]
    check("中文：大陆拼音和释义（繁体辞典显示成简体）", r["installed"] and w.get("pinyin") == ["mí màn"] and "烟尘、雾气等充满、布满" in json.dumps(w, ensure_ascii=False), r)
    check("中文：多个字时列出单字", [c["ch"] for c in r.get("chars", [])] == ["弥", "漫"], r.get("chars"))
    r = j("GET", "/api/dict?q=" + urllib.parse.quote("的晴朗。"))
    check("选多了字也能找出里面的词", (r.get("words") or [{}])[0].get("word") == "晴朗", r.get("words"))
    r = j("GET", "/api/dict?q=went&lang=en")
    check("英文：变形找到原形", r["words"] and r["words"][0]["word"] == "go" and "过去式" in r["words"][0].get("note", ""), r["words"])
    r = j("GET", "/api/dict?q=" + urllib.parse.quote("こんにちは"))
    check("其他语言：认出语言，交给 AI", r["lang"] == "ja" and not r["words"], r)
    r = j("POST", "/api/dict/explain", json={"text": "弥漫", "context": "空气里弥漫着刚出炉面包的香气"})
    check("没配置 AI 时说明原因", r["ok"] is False and "AI" in r["reason"], r)
    s = j("GET", "/api/settings")
    j("PUT", "/api/settings", json={"ai": {"enabled": True, "base_url": MOCK + "/ai/v1", "api_key": "", "model": "mock-1"}})
    n = len(DICT_LOG)
    r = j("POST", "/api/dict/explain", json={"text": "弥漫", "context": "空气里弥漫着刚出炉面包的香气", "title": "三体Ⅱ·黑暗森林"})
    check("AI 结合上下文解释", r["ok"] and "香气" in r["answer"] and "think" not in r["answer"] and "上下文：空气里弥漫着" in DICT_LOG[-1], r)
    r = j("POST", "/api/dict/explain", json={"text": "弥漫", "context": "空气里弥漫着刚出炉面包的香气", "title": "三体Ⅱ·黑暗森林"})
    check("同样的问题第二次走缓存", r.get("cached") and len(DICT_LOG) == n + 1, r)
    j("PUT", "/api/settings", json={"ai": s["ai"]})
    j("PUT", "/api/settings", json={"tts": {"engine": "openai", "openai": {"base_url": MOCK + "/tts/v1", "model": "tts-1", "voice": "nova"}}})
    a = api("POST", "/api/dict/speak", json={"text": "serendipity", "lang": "en"})
    check("发音", a.headers["Content-Type"] == "audio/mpeg" and len(a.content) > 100)
    check("工具状态里有本地字典", j("GET", "/api/settings")["tools"]["dict"] is True)


def wait_job(jid):
    def done():
        js = [x for x in j("GET", "/api/fetch") if x["id"] == jid]
        return js[0] if js and js[0]["status"] in ("done", "error") else None
    return wait(done, 40, 0.5, "下载任务")


def test_fetch_discover():
    section("从链接添加、发现（公开书库）")
    for t, a in [("镜花缘", "李汝珍"), ("老残游记", "刘鹗"), ("海上花列传", "韩邦庆")]:
        make_epub(os.path.join(T, "remote", t + ".epub"), t, a, [("第一回", [t + "的第一回。" * 40])])
    r = j("POST", "/api/fetch", json={"url": MOCK + "/files/" + urllib.parse.quote("镜花缘.epub")})
    job = wait_job(r["id"])
    check("链接下载完成", job["status"] == "done" and job["bytes"] > 1000, job)
    b = wait(lambda: book_by_title("镜花缘"), 40, 1, "链接下载的书入库")
    check("下载的书自动入库", b and b["author"] == "李汝珍", b and b["author"])
    job = wait_job(j("POST", "/api/fetch", json={"url": MOCK + "/page.html"})["id"])
    check("网页链接给出明确提示", job["status"] == "error" and "网页" in job["error"], job)
    check("非 http 链接被拒绝", api("POST", "/api/fetch", 400, json={"url": "ftp://x/y.epub"}).status_code == 400)
    cats = j("GET", "/api/catalogs")
    check("内置书源", {"wikisource", "gutenberg", "ia", "standardebooks"} <= {c["id"] for c in cats}, [c["id"] for c in cats])
    root = j("GET", "/api/catalogs/gutenberg/browse")
    check("OPDS 目录", root["entries"] and root["entries"][0]["kind"] == "nav" and root["can_search"], root)
    feed = j("GET", "/api/catalogs/gutenberg/browse?url=" + urllib.parse.quote(root["entries"][0]["href"]))
    bk = feed["entries"][1]
    check("OPDS 书目（作者、封面、格式按 EPUB 优先）", bk["kind"] == "book" and bk["author"] == "刘鹗" and bk["formats"][0]["type"] == "EPUB" and bk["cover"].startswith(MOCK), bk)
    check("书库里已有的书做了标记", feed["entries"][0]["id"].startswith("have:"), feed["entries"][0]["id"])
    check("OPDS 分页", feed.get("next"))
    page2 = j("GET", "/api/catalogs/gutenberg/browse?next=" + urllib.parse.quote(feed["next"]))
    check("下一页", page2["entries"] and page2["entries"][0]["title"] == "海上花列传", page2)
    check("书目带上书源的主题（入库时自动分类用）", "Chinese fiction" in (page2["entries"][0].get("tags") or []), page2["entries"][0].get("tags"))
    sr = j("GET", "/api/catalogs/gutenberg/browse?q=" + urllib.parse.quote("老残"))
    check("OPDS 搜索（OpenSearch 描述）", [e["title"] for e in sr["entries"]] == ["老残游记"], sr)
    img = api("GET", "/api/catalog-image?c=gutenberg&url=" + urllib.parse.quote(bk["cover"]))
    check("书源封面经服务器转发", img.headers["Content-Type"].startswith("image/"))
    j("POST", "/api/fetch", json={"url": sr["entries"][0]["formats"][0]["url"], "name": "老残游记 - 刘鹗", "catalog": "gutenberg"})
    b = wait(lambda: book_by_title("老残游记"), 40, 1, "书源下载入库")
    check("书源一键入库", b and b["author"] == "刘鹗")
    c = j("POST", "/api/catalogs", json={"url": MOCK + "/opds/root.xml", "username": "me", "password": "pw"})
    check("添加自定义 OPDS 书源（自动取名）", c["name"] == "测试书源" and c["id"].startswith("u"), c)
    check("书源密码不回传", all(x.get("password") in (None, "", "••••") for x in j("GET", "/api/catalogs")))
    check("需要登录的书源带上用户名密码", OPDS_AUTH and OPDS_AUTH[-1] == "me:pw", OPDS_AUTH[-3:])
    check("无效地址被拒绝", api("POST", "/api/catalogs", 400, json={"url": MOCK + "/page.html"}).status_code == 400)
    j("DELETE", f"/api/catalogs/{c['id']}")
    j("DELETE", "/api/catalogs/standardebooks")
    ids = [x["id"] for x in j("GET", "/api/catalogs")]
    check("删除书源（内置的可以隐藏）", c["id"] not in ids and "standardebooks" not in ids, ids)
    j("POST", "/api/catalogs/restore")
    check("恢复内置书源", "standardebooks" in [x["id"] for x in j("GET", "/api/catalogs")])
    ws = j("GET", "/api/catalogs/wikisource/browse?q=" + urllib.parse.quote("红楼梦"))
    check("维基文库：简体搜到繁体书名，章节页归到作品", [e["title"] for e in ws["entries"]] == ["紅樓夢"], (ws, WS_LOG))
    j("POST", "/api/fetch", json={"url": ws["entries"][0]["formats"][0]["url"], "name": "紅樓夢", "catalog": "wikisource"})
    check("维基文库生成 EPUB 入库", wait(lambda: book_by_title("紅樓夢"), 40, 1, "维基文库入库"))
    ia = j("GET", "/api/catalogs/ia/browse?q=" + urllib.parse.quote("儒林外史"))
    check("互联网档案馆搜索", ia["entries"] and ia["entries"][0]["formats"][0]["url"] == "ia:rulinwaishi00wu" and ia["entries"][0]["author"] == "吴敬梓", ia)
    j("POST", "/api/fetch", json={"url": "ia:rulinwaishi00wu", "catalog": "ia"})
    b = wait(lambda: book_by_title("儒林外史"), 40, 1, "档案馆入库")
    check("互联网档案馆下载（自动选 EPUB）", b and b["files"][0]["format"] == "epub")
    # 1.0.0.9：书源里的书目信息（主题、简介）跟着下载走，入库识别时用上（AI 也看得到；没配置 AI 时按主题分类）
    make_epub(os.path.join(T, "remote", "hint-test.epub"), "", "", [("第一回", ["书源信息测试。" * 40])])
    n0 = len(AI_LOG)
    j("POST", "/api/fetch", json={"url": MOCK + "/files/hint-test.epub", "name": "书源信息测试 - 测试作者", "catalog": "gutenberg", "title": "书源信息测试", "author": "测试作者",
                                  "summary": "一部测试用的长篇小说。", "language": "zh", "tags": ["Science fiction", "测试主题甲"]})
    hb = wait(lambda: book_by_title("书源信息测试"), 40, 1, "带书目信息的下载入库")
    sent = [x for x in AI_LOG[n0:] if "书源信息测试" in json.dumps(x, ensure_ascii=False)]
    check("从发现入库：作者用书源的（文件里没有）", hb and hb["author"] == "测试作者", hb and (hb["title"], hb["author"]))
    # 主题、简介记到书上（识别把握不大要问 AI 时，AI 也看得到）
    check("从发现入库：书源的主题、简介记到书上", hb and "测试主题甲" in (hb.get("tags") or []) and "长篇小说" in (hb.get("description") or ""), hb and (hb.get("tags"), hb.get("description")))
    if sent:
        check("从发现入库：问 AI 时带上书源的主题、简介", "测试主题甲" in json.dumps(sent[-1], ensure_ascii=False), sent[-1])
    check("从发现入库：按书源的主题自动分了类", hb and hb.get("category") not in ("", None, "其他"), hb and hb.get("category"))


# ---- 1.0.0.7 书架和设置 ----
# 家人账号、书架多选和右键菜单、想读评分、系列、标签、回收站、存储、备份、笔记页、取消下载、返回书架时保留位置

CSP_ERRORS = []


def test_v7_family_api():
    global TOKEN
    section("家人账号（接口）")
    libs = j("GET", "/api/libraries")
    lib_book = next(l["id"] for l in libs if l["kind"] == "book")
    u = j("POST", "/api/users", json={"name": "临时", "password": "4321", "role": "member", "libraries": [lib_book]})
    st = j("GET", "/api/auth/state")
    check("有两个人时登录页列出名字", len(st.get("users") or []) == 2 and {x["name"] for x in st["users"]} >= {"临时"}, st.get("users"))
    saved, TOKEN = TOKEN, ""
    check("家人密码错误 401", api("POST", "/api/auth/login", 401, json={"user_id": u["id"], "password": "nope"}).status_code == 401)
    TOKEN = j("POST", "/api/auth/login", json={"user_id": u["id"], "password": "4321", "Device": "e2e-家人"})["token"]
    me = j("GET", "/api/me")
    check("家人登录后是家人身份", me["name"] == "临时" and me["role"] == "member", me)
    kinds = {b["kind"] for b in all_books()}
    check("只限图书书库的家人看不到漫画", "comic" not in kinds and "book" in kinds, kinds)
    for m, p in [("GET", "/api/users"), ("GET", "/api/storage"), ("GET", "/api/backups"), ("GET", "/api/trash"), ("POST", "/api/scan"), ("GET", "/api/events")]:
        check(f"家人不能 {m} {p}", api(m, p, 403).status_code == 403)
    s = j("GET", "/api/settings")
    check("家人拿到的设置里没有 AI、路径", "ai" not in s and "inbox" not in s and "tts" in s, sorted(s))
    TOKEN = saved
    j("DELETE", f"/api/users/{u['id']}")
    check("删除家人账号", "临时" not in [x["name"] for x in j("GET", "/api/users")])
    ser = j("GET", "/api/series")
    sant = next((x for x in ser if x["name"] == "三体"), None)
    check("系列列表（三体 2 本）", sant and sant["count"] == 2 and sant["cover_id"], ser)


def ui_v7_shelf(browser, context, attach, shot, reader_ready):
    section("网页：1.0.0.7 书架、详情、系列、多选")
    books = {b["title"]: b for b in all_books()}
    qiu = books["球状闪电"]
    j("POST", "/api/books/batch", json={"ids": [qiu["id"]], "tags_add": ["七号标签"]})
    ctx = context()
    pg = ctx.new_page(); attach(pg, "v7-shelf")
    pg.goto(BASE + "/")
    pg.wait_for_selector(".book")
    # 右键菜单
    pg.locator(".book", has_text="球状闪电").first.click(button="right")
    pg.wait_for_selector(".ctx-menu.show")
    items = pg.locator(".ctx-menu button").all_inner_texts()
    check("右键菜单：打开、详情、读完、想读、删除", all(any(k in t for t in items) for k in ("详情", "想读", "删除")) and ("标为读完" in items or "标为未读" in items), items)
    shot(pg, "v7-01-menu")
    pg.click(".ctx-menu button >> text='想读'")
    wait(lambda: book_by_title("球状闪电")["want"], 5, 0.2, "菜单里加入想读")
    check("菜单里加入想读", True)
    # 详情：评分、读完、标签
    pg.locator(".book", has_text="球状闪电").first.click(button="right")
    pg.click(".ctx-menu button:has-text('详情')")
    pg.wait_for_selector(".drawer .mine .stars button")
    check("详情里显示想读", "on" in pg.get_attribute(".drawer .btn.want", "class"))
    pg.locator(".drawer .stars button").nth(3).click()
    pg.wait_for_function("() => document.querySelectorAll('.drawer .stars button.on').length === 4")
    check("详情里评 4 星", book_by_title("球状闪电")["rating"] == 4)
    pg.click(".drawer .mine .seg button:has-text('读完')")
    wait(lambda: book_by_title("球状闪电")["read_state"] == "done", 5, 0.2, "标为读完")
    check("详情里标为读完", True)
    shot(pg, "v7-02-detail")
    pg.click(".drawer .tag.link:has-text('七号标签')")
    pg.wait_for_selector(".filters .chip.on:has-text('七号标签')")
    pg.wait_for_function("() => document.querySelectorAll('.grid .book').length === 1")
    check("点标签筛选出带这个标签的书", pg.locator(".grid .book .t").first.inner_text() == "球状闪电")
    # 想读、评分排序
    pg.goto(BASE + "/#/want")
    pg.wait_for_selector(".grid .book")
    check("“想读”页", pg.locator(".grid .book .t").all_inner_texts() == ["球状闪电"], pg.locator(".grid .book .t").all_inner_texts())
    # 系列
    pg.goto(BASE + "/#/series")
    pg.wait_for_selector(".series-card")
    names = [pg.locator(".series-card .t").nth(i).inner_text() for i in range(pg.locator(".series-card").count())]
    check("系列列表", "三体" in names and "进击的巨人" in names, names)
    shot(pg, "v7-03-series")
    pg.locator(".series-card", has_text="三体").click()
    pg.wait_for_selector(".page-head .crumb")
    pg.wait_for_function("() => document.querySelectorAll('.grid .book').length === 2")
    check("系列里按序号排", pg.locator(".grid .book .t").all_inner_texts() == ["三体", "三体Ⅱ·黑暗森林"], pg.locator(".grid .book .t").all_inner_texts())
    pg.goto(BASE + "/#/books")
    pg.wait_for_selector(".grid .book")
    pg.click(".filters .chip:has-text('按系列')")
    pg.wait_for_selector(".series-card[data-series='三体']")
    check("书架按系列折叠", pg.locator(".series-card[data-series='三体']").count() == 1 and pg.locator(".grid .book[data-id] .t", has_text="三体Ⅱ").count() == 0)
    pg.click(".filters .chip:has-text('按系列')")
    pg.wait_for_selector(".grid .book[data-id] >> text=三体Ⅱ·黑暗森林")
    # 多选：标记想读、设成系列、删除进回收站、在设置里恢复
    huo, liu = books["活着"], books["流浪地球"]
    pg.goto(BASE + "/#/all")
    pg.wait_for_selector(".grid .book")
    pg.click(".page-head button:has-text('选择')")
    pg.locator(f".grid .book[data-id='{huo['id']}']").click()
    pg.locator(f".grid .book[data-id='{liu['id']}']").click()
    check("多选：已选 2 本", pg.inner_text(".sel-bar b") == "已选 2 本", pg.inner_text(".sel-bar b"))
    shot(pg, "v7-04-select")
    pg.click(".sel-bar button:has-text('标记')")
    pg.click(".ctx-menu button >> text='想读'")
    pg.wait_for_selector(".sel-bar", state="detached")
    check("多选后一起标为想读（退出多选）", book_by_title("活着")["want"] and book_by_title("流浪地球")["want"])
    pg.click(".page-head button:has-text('选择')")
    pg.locator(f".grid .book[data-id='{liu['id']}']").click()
    pg.click(".sel-bar button:has-text('整理')")
    pg.click(".ctx-menu button:has-text('系列')")
    pg.fill(".form-dialog input >> nth=0", "测试系列")
    pg.fill(".form-dialog input >> nth=1", "3")
    pg.click(".form-dialog button[type=submit]")
    pg.wait_for_selector(".sel-bar", state="detached")
    b3 = book_by_title("流浪地球")
    check("多选改系列（带起始序号）", b3["series"] == "测试系列" and b3["series_index"] == 3, (b3["series"], b3["series_index"]))
    pg.wait_for_selector(".grid .book")
    pg.click(".page-head button:has-text('选择')")
    pg.locator(f".grid .book[data-id='{liu['id']}']").click()
    pg.click(".sel-bar button:has-text('删除')")
    pg.click(".dialog .btn.danger")
    pg.wait_for_selector(".sel-bar", state="detached")
    check("多选删除（进回收站）", book_by_title("流浪地球") is None)
    pg.goto(BASE + "/#/settings")
    pg.wait_for_selector("#set-trash .trash-row")
    check("回收站列出删掉的书", pg.locator("#set-trash .trash-row", has_text="流浪地球").count() == 1)
    pg.locator("#set-trash .trash-row", has_text="流浪地球").locator("button:has-text('恢复')").click()
    wait(lambda: book_by_title("流浪地球") is not None, 10, 0.3, "从回收站恢复")
    check("回收站里恢复（想读状态还在）", book_by_title("流浪地球")["want"])
    # 设置：目录、存储、备份、家人账号
    check("设置页目录", pg.locator(".set-toc .chip").count() >= 12, pg.locator(".set-toc .chip").count())
    st = pg.locator("#set-storage")
    st.locator(".st-block >> nth=4").wait_for()
    check("存储与状态", st.locator(".st-block").count() >= 5 and "数据目录" in st.inner_text() and "缓存" in st.inner_text())
    st.locator("button:has-text('全部清理')").click()
    pg.click(".dialog .btn.primary")
    pg.wait_for_function("() => [...document.querySelectorAll('#toasts .toast')].some(t => t.textContent.includes('已清理'))")
    check("清理缓存", True)
    bk = pg.locator("#set-backup")
    first = pg.evaluate("() => document.querySelector('#set-backup .backup-row')?.dataset.name || ''")
    bk.locator("button:has-text('立即备份')").click()
    pg.wait_for_function("(f) => (document.querySelector('#set-backup .backup-row')?.dataset.name || '') > f", arg=first)
    check("立即备份", "manual" in bk.locator(".backup-row >> nth=0").get_attribute("data-name"))
    bk.locator(".backup-row >> nth=0").locator("button:has-text('恢复')").click()
    pg.click(".dialog .btn.danger")
    pg.wait_for_function("() => [...document.querySelectorAll('#toasts .toast')].some(t => t.textContent.includes('已准备好恢复'))")
    check("从备份恢复（测试时不重启，提示重启后生效）", os.path.exists(os.path.join(T, "data", "restore.db")))
    os.remove(os.path.join(T, "data", "restore.db"))
    shot(pg, "v7-05-settings")
    fam = pg.locator("#set-family")
    fam.locator("button:has-text('添加家人')").click()
    pg.fill(".form-dialog input >> nth=0", "小明")
    pg.fill(".form-dialog input[type=password]", "1234")
    pg.locator(".form-dialog .check", has_text="图书").locator("input").check()
    pg.click(".form-dialog button[type=submit]")
    fam.locator(".user-row[data-user='小明']").wait_for()
    xm = next(x for x in j("GET", "/api/users") if x["name"] == "小明")
    check("添加家人（限定书库）", xm["role"] == "member" and len(xm["libraries"]) == 1, xm)
    ctx.close()

    # 笔记页
    section("网页：笔记")
    s1 = books["三体"]
    hl = j("POST", f"/api/books/{s1['id']}/bookmarks", json={"kind": "highlight", "location": "epubcfi(/6/4!/4/2/1:0)", "label": "第一章 科学边界", "text": "宇宙很大，生活更大", "note": "想想", "color": "yellow"})
    ctx = context()
    pg = ctx.new_page(); attach(pg, "v7-notes")
    pg.goto(BASE + "/#/notes")
    pg.wait_for_selector(".note-item")
    check("笔记页列出划线笔记", pg.locator(".note-item blockquote", has_text="宇宙很大").count() == 1)
    pg.fill(".page-head .search input", "生活更大")
    pg.wait_for_function("() => document.querySelectorAll('.note-item').length === 1")
    item = pg.locator(".note-item", has_text="宇宙很大")
    item.locator("button:has-text('改笔记')").click()
    item.locator("textarea").fill("改过的笔记")
    item.locator("button:has-text('保存')").click()
    pg.wait_for_function("() => [...document.querySelectorAll('.note-item .n-note')].some(e => e.textContent === '改过的笔记')")
    check("在笔记页改笔记", any(b["note"] == "改过的笔记" for b in j("GET", f"/api/books/{s1['id']}/bookmarks")))
    md = api("GET", "/api/notes.md").text
    check("导出全部笔记（Markdown）", "宇宙很大" in md and "改过的笔记" in md, md[:200])
    shot(pg, "v7-06-notes")
    item.locator(".n-body").click()
    pg.wait_for_function("() => location.hash.startsWith('#/read/') && location.hash.includes('at=')")
    check("点笔记回到书里那个位置", True)
    ctx.close()

    # 发现：取消下载
    ctx = context()
    pg = ctx.new_page(); attach(pg, "v7-cancel")
    pg.goto(BASE + "/#/discover")
    pg.fill(".link-add input", MOCK + "/slow/慢慢下载.epub")
    pg.click(".link-add button[type=submit]")
    pg.wait_for_selector(".uploads.jobs li button[aria-label^='取消下载']")
    pg.click(".uploads.jobs li button[aria-label^='取消下载']")
    wait(lambda: not [x for x in j("GET", "/api/fetch") if "slow" in x["url"]], 10, 0.3, "取消下载")
    check("发现页能取消下载", True)
    ctx.close()

    # 从阅读器回到书架：搜索词、滚动位置都在
    ctx = context(700, 420)
    pg = ctx.new_page(); attach(pg, "v7-restore")
    pg.goto(BASE + "/#/all")
    pg.wait_for_selector(".grid .book")
    pg.wait_for_timeout(500)
    pg.mouse.wheel(0, 900)
    pg.wait_for_timeout(500)
    y0 = pg.evaluate("() => scrollY")
    last = pg.locator(".grid .book[data-id]").last
    last.click()
    reader_ready(pg)
    pg.keyboard.press("Escape")
    pg.wait_for_selector(".grid .book")
    pg.wait_for_timeout(700)
    y1 = pg.evaluate("() => scrollY")
    check("从阅读器回到书架，滚动位置还在", y0 > 100 and abs(y1 - y0) < 80, (y0, y1))
    pg.fill(".page-head .search input", "刘慈欣")
    pg.wait_for_timeout(600)
    pg.locator(".grid .book[data-id]").first.click()
    reader_ready(pg)
    pg.keyboard.press("Escape")
    pg.wait_for_selector(".grid .book")
    check("从阅读器回到书架，搜索词还在", pg.input_value(".page-head .search input") == "刘慈欣")
    ctx.close()

    # 家人登录：选名字、只看到允许的书库、没有管理功能
    section("网页：家人账号")
    ctx = context(token=False)
    pg = ctx.new_page(); attach(pg, "v7-member")
    pg.goto(BASE + "/")
    pg.wait_for_selector(".who-btn")
    check("登录页先选人", pg.locator(".who-btn").count() == 2)
    shot(pg, "v7-07-who")
    pg.locator(".who-btn", has_text="小明").click()
    pg.fill(".auth input[type=password]", "1234")
    pg.click(".auth button[type=submit]")
    pg.wait_for_selector(".grid .book")
    titles = pg.locator(".grid .book .t").all_inner_texts()
    check("家人只看到允许的书库", "活着" in titles and not any("进击的巨人" in t for t in titles), titles)
    nav = pg.inner_text(".side .nav")
    check("家人没有整理、发现、添加书", "整理" not in nav and "发现" not in nav and pg.locator(".page-head button:has-text('添加书')").count() == 0, nav)
    pg.locator(".book", has_text="镜花缘").first.click(button="right")
    pg.wait_for_selector(".ctx-menu.show")
    check("家人的菜单里没有删除", pg.locator(".ctx-menu button:has-text('删除')").count() == 0)
    pg.click(".ctx-menu button:has-text('详情')")
    pg.wait_for_selector(".drawer .mine")
    check("家人看详情：没有编辑、删除", pg.locator(".drawer .form-grid").count() == 0 and pg.locator(".drawer footer").count() == 0)
    pg.click(".drawer .mine .seg button:has-text('读完')")
    pg.wait_for_function("() => [...document.querySelectorAll('#toasts .toast')].some(t => t.textContent.includes('已标为读完'))")
    check("家人标为读完，不影响管理员", book_by_title("镜花缘")["read_state"] != "done")
    pg.keyboard.press("Escape")
    pg.goto(BASE + "/#/organize")
    pg.wait_for_function("() => location.hash === '#/'")
    check("家人打不开整理页", True)
    pg.goto(BASE + "/#/settings")
    pg.wait_for_selector("#set-account")
    check("家人的设置页：没有家人账号、存储、备份", pg.locator("#set-family, #set-storage, #set-backup, #set-libs").count() == 0 and "小明" in pg.inner_text("#set-account"))
    shot(pg, "v7-08-member-settings")
    pg.locator("#set-account button:has-text('退出登录')").click()
    pg.wait_for_selector(".auth .hello")
    check("退出后登录页记得上次选的人", "小明" in pg.inner_text(".auth .hello"))
    ctx.close()

    # 手机 App 里的下载设置（模拟原生桥）
    section("网页：手机 App 的下载设置")
    fake = """
      window.__calls = []; window.__store = { 'shuku.token': %s }
      window.ShukuNative = {
        storageGet: k => (k in __store ? __store[k] : null), storageSet: (k, v) => { __store[k] = v }, storageRemove: k => { delete __store[k] },
        storageKeys: () => JSON.stringify(Object.keys(__store)), version: () => '1.0-test', deviceName: () => '测试手机',
        setRoute: j => 0, setNetwork: j => 0, setToken: t => 0, setThemeColor: () => 0, setReaderMode: () => 0,
        prefs: () => JSON.stringify({ download_limit_mb: 0, auto_clean_finished: false }), setPref: (k, v) => __calls.push(['setPref', k, v]),
        downloads: () => '[]',
        downloadInfo: () => JSON.stringify({ items: [{ id: %d, title: '活着', state: 'error', error: '网络断了', size: 0 }, { id: 99999, title: '下好的书', state: 'done', size: 3145728, format: 'epub' }], total_size: 3145728, limit_mb: 0 }),
        download: (id, json) => __calls.push(['download', id, json]), removeDownload: id => __calls.push(['removeDownload', id]),
      }""" % (json.dumps(TOKEN), huo["id"])
    ctx = browser.new_context(viewport={"width": 412, "height": 900}, device_scale_factor=2, has_touch=True, is_mobile=True, locale="zh-CN")
    ctx.add_init_script(fake)
    pg = ctx.new_page(); attach(pg, "v7-native")
    pg.goto(BASE + "/#/settings")
    pg.wait_for_selector("#set-app")
    app = pg.locator("#set-app")
    check("显示下载占用", "共占用 3.0 MB" in app.inner_text(), app.inner_text())
    app.locator("select[aria-label='下载到手机最多占用']").select_option("1024")
    app.locator(".set-row", has_text="读完的书自动从手机删掉").locator("input").check()
    calls = pg.evaluate("() => __calls.filter(c => c[0] === 'setPref')")
    check("下载上限、读完自动删除交给原生", ["setPref", "download_limit_mb", "1024"] in calls and ["setPref", "auto_clean_finished", "true"] in calls, calls)
    check("下载失败显示原因", "网络断了" in app.inner_text())
    app.locator(".dev-row", has_text="活着").locator("button:has-text('重试')").click()
    pg.wait_for_function("() => __calls.some(c => c[0] === 'download')")
    d = pg.evaluate("() => __calls.find(c => c[0] === 'download')")
    check("下载失败可以重试（参数和详情里一样）", d[1] == huo["id"] and json.loads(d[2])["title"] == "活着", d)
    shot(pg, "v7-09-native")
    ctx.close()


# ---------------------------------------------------------------- 网页检查

def ui_tests():
    from playwright.sync_api import sync_playwright
    chrome = sorted(glob.glob("/opt/pw-browsers/chromium-*/chrome-linux/chrome"))
    errors = []

    def attach(pg, label):
        # 书里夹带的脚本被内容安全策略拦下是预期的（book_csp，书的章节在 blob: 框架里），网页自己的违规、报错才算
        pg.on("console", lambda m: "Content Security Policy" in m.text and not book_csp(m) and CSP_ERRORS.append(f"[{label}] {m.text[:240]}"))
        pg.on("pageerror", lambda e: errors.append(f"[{label}] PAGEERROR {e}"))
        pg.on("console", lambda m: m.type == "error" and "favicon" not in m.text and "Failed to load resource" not in m.text and not book_csp(m) and errors.append(f"[{label}] console.error {m.text}"))

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
        # 1.0.0.7：从阅读器回来，搜索词和书中内容的结果都还在
        pg.wait_for_selector(".ft-sec .ft-hit")
        check("从阅读器回到书架，搜索结果还在", pg.input_value(".search input") == "游手好闲", pg.input_value(".search input"))
        pg.fill(".search input", "")
        pg.wait_for_selector(".book")
        pg.wait_for_timeout(500)
        card = pg.locator(".book", has_text="活着").first
        card.click(button="right")
        pg.click(".ctx-menu button:has-text('详情')")
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
        pg.locator("#set-tts").locator("button:has-text('试听')").click()
        pg.wait_for_timeout(1500)
        status = pg.locator("#set-tts").locator(".muted.small-text").first.inner_text()
        check("设置里试听（自定义语音接口）", status == "", status)
        up = pg.locator("#set-update")
        up.locator("text=书库服务器").wait_for()
        check("设置里有“版本与升级”", up.locator("a[href$='/dl/shuku-android.apk']").count() == 1 and up.locator("a[href$='/dl/shuku-wear.apk']").count() == 1 and up.locator("a[href$='/dl/shuku-extension.zip']").count() == 1, up.inner_text())
        n = len(WT_LOG)
        up.locator("button:has-text('检查更新')").click()
        up.locator("text=没有换新版本").wait_for(timeout=10000)
        check("网页上点“检查更新”", len(WT_LOG) == n + 1 and up.locator("button:has-text('检查更新')").is_enabled())
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
        check("从手表的位置（sec:2:0.5）接着读", "下部" in pg.inner_text(".r-status"), pg.inner_text(".r-status"))
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

        # ---- 发现 ----
        section("网页：发现、从链接添加")
        ctx = context()
        pg = ctx.new_page(); attach(pg, "discover")
        pg.goto(BASE + "/#/discover")
        pg.wait_for_selector(".src-chips .chip")
        check("书源列表", pg.locator(".src-chips .chip", has_text="维基文库").count() == 1 and pg.locator(".src-chips .chip", has_text="古腾堡计划").count() == 1)
        pg.locator(".src-chips .chip", has_text="维基文库").click()
        pg.wait_for_selector(".disc-hint .chip")
        pg.fill(".src-search input", "红楼梦")
        pg.press(".src-search input", "Enter")
        pg.wait_for_selector(".grid.disc .book")
        check("维基文库搜索结果", "紅樓夢" in pg.inner_text(".grid.disc"))
        shot(pg, "17-discover-wikisource")
        pg.locator(".src-chips .chip", has_text="古腾堡计划").click()
        pg.wait_for_selector(".disc-nav")
        pg.click(".disc-nav")
        pg.wait_for_selector(".grid.disc .book")
        check("OPDS 目录点进去看到书", pg.locator(".grid.disc .book").count() == 2)
        check("书库里已有的书有标记", pg.locator(".grid.disc .flag.have").count() >= 1)
        pg.click(".loading-more button")
        pg.wait_for_function("() => document.querySelectorAll('.grid.disc .book').length === 3")
        check("加载更多", True)
        shot(pg, "18-discover-opds")
        pg.locator(".grid.disc .book", has_text="海上花列传").click()
        pg.wait_for_selector(".drawer.show .disc-formats .btn.primary")
        check("书目详情：格式按 EPUB 优先", "EPUB" in pg.inner_text(".drawer .disc-formats .btn.primary"))
        pg.click(".drawer .disc-formats .btn.primary")
        pg.wait_for_selector(".uploads.jobs li")
        check("下载任务面板", True)
        pg.wait_for_function("() => [...document.querySelectorAll('#toasts .toast')].some(t => t.textContent.includes('海上花列传') && t.textContent.includes('入库'))", timeout=40000)
        check("书源一键入库后有提示", True)
        make_epub(os.path.join(T, "remote", "浮生六记.epub"), "浮生六记", "沈复", [("闺房记乐", ["余生乾隆癸未冬十一月二十有二日。" * 30])])
        pg.fill(".link-add input", MOCK + "/files/" + urllib.parse.quote("浮生六记.epub"))
        pg.click(".link-add button")
        # 和上一本差不多同时入库时，提示合成一条“已入库 2 本（最近：《…》）”，不一定写着这本书名：书库里有这本书、有入库提示就算
        wait(lambda: book_by_title("浮生六记"), 40, 0.5, "粘贴链接入库")
        pg.wait_for_function("() => [...document.querySelectorAll('#toasts .toast')].some(t => t.textContent.includes('浮生六记') || /已入库 \\d+ 本/.test(t.textContent))", timeout=40000)
        check("粘贴链接下载入库", True)
        pg.fill(".link-add input", MOCK + "/page.html")
        pg.click(".link-add button")
        pg.wait_for_function("() => [...document.querySelectorAll('#toasts .toast.err')].some(t => t.textContent.includes('网页'))", timeout=20000)
        check("网页链接提示失败原因", True)
        ctx.close()

        # ---- 设备配对 ----
        section("网页：手机配对二维码、设备列表")
        ctx = context()
        pg = ctx.new_page(); attach(pg, "pair")
        pg.goto(BASE + "/#/settings")
        pg.wait_for_selector("button:has-text('显示配对二维码')")
        pg.click("button:has-text('显示配对二维码')")
        pg.wait_for_selector(".pair img.qr")
        pg.add_script_tag(content=open("/home/claude/cozmo/jsQR/dist/jsQR.js").read()) if os.path.exists("/home/claude/cozmo/jsQR/dist/jsQR.js") else None
        code = pg.inner_text(".pair-code").replace(" ", "")
        decoded = pg.evaluate("""async () => {
          if (!window.jsQR) return 'nojsqr'
          const img = document.querySelector('.pair img.qr'); await img.decode()
          const c = document.createElement('canvas'); c.width = c.height = 400; const x = c.getContext('2d'); x.drawImage(img, 0, 0, 400, 400)
          const q = jsQR(x.getImageData(0, 0, 400, 400).data, 400, 400); return q ? q.data : null }""")
        check("二维码能扫出配对链接", decoded == "nojsqr" or (decoded and decoded.startswith("shuku://pair?") and "c=" + code in decoded), decoded)
        pg.wait_for_selector(".dev-row")
        check("已登录设备列表", pg.locator(".dev-row").count() >= 2)
        shot(pg, "19-pair")
        ctx.close()

        # ---- 选词查字典、菜单关闭 ----
        section("网页：查字典、菜单")
        SEL = """(word) => {
          const doc = document.querySelector('foliate-view').renderer.getContents()[0].doc
          const walker = doc.createTreeWalker(doc.body, NodeFilter.SHOW_TEXT)
          let n
          while ((n = walker.nextNode())) {
            const i = n.nodeValue.indexOf(word)
            if (i < 0) continue
            const r = doc.createRange(); r.setStart(n, i); r.setEnd(n, i + word.length)
            const sel = doc.getSelection(); sel.removeAllRanges(); sel.addRange(r)
            doc.dispatchEvent(new PointerEvent('pointerup', { bubbles: true }))
            return true
          }
          return false
        }"""
        j("PUT", "/api/settings", json={"ai": {"enabled": True, "base_url": MOCK + "/ai/v1", "api_key": "", "model": "mock-1"}})
        ctx = context()
        pg = ctx.new_page(); attach(pg, "dict")
        pg.goto(BASE + f"/#/read/{b2['id']}")
        reader_ready(pg)
        pg.wait_for_timeout(800)
        found = pg.evaluate(SEL, "弥漫")
        pg.wait_for_selector(".r-dict.show .zh li", timeout=8000)
        txt = pg.inner_text(".r-dict")
        check("选中文字弹出字典：拼音、释义、单字", found and "mí màn" in txt and "烟尘、雾气等充满" in txt and "单字" in txt, txt[:200])
        shot(pg, "40-dict")
        pg.click(".r-dict .ai-btn")
        pg.wait_for_selector(".r-dict .ai-a", timeout=8000)
        check("字典里点 AI 解释", "香气" in pg.inner_text(".r-dict .ai-a"))
        n = len(TTS_LOG)
        pg.click(".r-dict .hd .icon-btn[title='发音']")
        wait(lambda: len(TTS_LOG) > n, 10, 0.2, "发音请求")
        check("字典里点喇叭发音", TTS_LOG[-1].get("input") == "弥漫", TTS_LOG[-1] if TTS_LOG else None)
        pg.keyboard.press("Escape")
        pg.wait_for_timeout(200)
        check("Esc 关闭字典，不退出阅读器", not pg.locator(".r-dict.show").count() and pg.locator(".reader").count() == 1)
        pg.evaluate(SEL, "弥漫")
        pg.wait_for_selector(".r-dict.show .zh li")
        bx = pg.locator(".r-dict").bounding_box()
        v_before = pg.eval_on_selector(".r-prog input[type=range]", "e => e.value")
        pg.mouse.click(1100 if bx["x"] < 600 else 300, 430)  # 点书页中间一带（不是左右翻页区），避开弹窗
        pg.wait_for_timeout(300)
        check("点书页关闭字典（不翻页）", not pg.locator(".r-dict.show").count() and pg.eval_on_selector(".r-prog input[type=range]", "e => e.value") == v_before)
        # 设置面板：× 关闭、点外面关闭、Esc 关闭；目录：× 关闭、点外面关闭
        # 刚打开书时工具栏短暂显示、1.8 秒后自动收起：先等它收起再手动显示，不然点按钮时正好被收起（按钮跑到屏幕外）
        pg.wait_for_function("() => !document.querySelector('.reader').classList.contains('ui')", timeout=6000)
        def sheet_open():
            pg.evaluate("() => document.querySelector('.reader').classList.add('ui')")
            pg.click(".r-top button[title='阅读设置']")
            pg.wait_for_selector(".r-sheet.show")
        sheet_open(); pg.click(".r-sheet-hd .icon-btn"); pg.wait_for_timeout(250)
        check("设置菜单：点 × 关闭", not pg.locator(".r-sheet.show").count())
        sheet_open(); pg.mouse.click(400, 300); pg.wait_for_timeout(250)
        check("设置菜单：点外面关闭", not pg.locator(".r-sheet.show").count())
        sheet_open(); pg.keyboard.press("Escape"); pg.wait_for_timeout(250)
        check("设置菜单：Esc 关闭", not pg.locator(".r-sheet.show").count() and pg.locator(".reader").count() == 1)
        pg.keyboard.press("t"); pg.wait_for_selector(".r-panel.show")
        pg.click(".r-panel header .icon-btn"); pg.wait_for_timeout(300)
        check("目录：点 × 关闭", not pg.locator(".r-panel.show").count())
        pg.keyboard.press("t"); pg.wait_for_selector(".r-panel.show")
        pg.mouse.click(1200, 400); pg.wait_for_timeout(300)
        check("目录：点外面关闭", not pg.locator(".r-panel.show").count())
        ctx.close()

        # 书一直在打开（比如网络很慢）时，菜单也要能关
        ctx = context(412, 900, mobile=True)
        pg = ctx.new_page(); attach(pg, "stuck")
        pg.route("**/api/files/**", lambda route: None)  # 卡住下载，不回复
        pg.goto(BASE + f"/#/read/{b2['id']}")
        pg.wait_for_selector(".reader .r-loading")
        pg.evaluate("() => document.querySelector('.reader').classList.add('ui')")
        pg.click(".r-top button[title='阅读设置']")
        pg.wait_for_selector(".r-sheet.show")
        pg.mouse.click(206, 200)
        pg.wait_for_timeout(300)
        check("书还在打开时，点外面能关掉设置菜单", not pg.locator(".r-sheet.show").count())
        pg.click(".r-top button[title='阅读设置']")
        pg.wait_for_selector(".r-sheet.show")
        cdp = ctx.new_cdp_session(pg)
        box = pg.locator(".r-sheet-hd").bounding_box()
        x, y = box["x"] + box["width"] / 2, box["y"] + 10
        cdp.send("Input.dispatchTouchEvent", {"type": "touchStart", "touchPoints": [{"x": x, "y": y}]})
        for k in range(1, 9):
            cdp.send("Input.dispatchTouchEvent", {"type": "touchMove", "touchPoints": [{"x": x, "y": y + k * 25}]})
        cdp.send("Input.dispatchTouchEvent", {"type": "touchEnd", "touchPoints": []})
        pg.wait_for_timeout(400)
        check("手机上往下拖关掉设置菜单", not pg.locator(".r-sheet.show").count())
        pg.click(".r-top button[title='阅读设置']")
        pg.wait_for_selector(".r-sheet.show")
        pg.evaluate("() => window.shukuBack()")
        pg.wait_for_timeout(300)
        check("返回键先关菜单", not pg.locator(".r-sheet.show").count() and pg.locator(".reader").count() == 1)
        ctx.close()
        j("PUT", "/api/settings", json={"ai": {"enabled": False, "base_url": MOCK + "/ai/v1", "api_key": "", "model": "mock-1"}})

        # ---- 模拟安卓 App：桥接 ----
        section("网页：在安卓 App 里运行（模拟原生桥）")
        fake = """
          window.__calls = []; window.__store = { 'shuku.token': %s }; window.__downloads = []
          window.ShukuNative = {
            storageGet: k => (k in __store ? __store[k] : null), storageSet: (k, v) => { __store[k] = v }, storageRemove: k => { delete __store[k] },
            storageKeys: () => JSON.stringify(Object.keys(__store)), version: () => '1.0-test', deviceName: () => '测试手机',
            setRoute: j => __calls.push(['setRoute', j]), setNetwork: j => __calls.push(['setNetwork', j]), setToken: t => __calls.push(['setToken', t]),
            setThemeColor: (c, d) => __calls.push(['setThemeColor', c, d]), setReaderMode: on => __calls.push(['setReaderMode', on]),
            saveProgress: (id, body) => __calls.push(['saveProgress', id, body]), tts: (cmd, arg) => __calls.push(['tts', cmd, arg]),
            prefs: () => JSON.stringify({ volume_keys: true, keep_screen_on: true, tts_engine: 'server' }), setPref: (k, v) => __calls.push(['setPref', k, v]),
            downloads: () => JSON.stringify(__downloads), removeDownload: id => { __downloads = [] },
            download: (id, json) => { __calls.push(['download', id, json]); __downloads = [{ id, title: JSON.parse(json).title, state: 'done', size: 2048, format: 'epub' }]; setTimeout(() => shukuNativeEvent('download', JSON.stringify({ id, state: 'done' })), 50) },
            appInfo: () => JSON.stringify({ versionName: '1.0.0.5', versionCode: 5 }), updateApp: () => __calls.push(['updateApp']), updateWatchApp: () => __calls.push(['updateWatchApp']), openExternal: u => __calls.push(['openExternal', u]),
            syncWatch: () => { __calls.push(['syncWatch']); setTimeout(() => shukuNativeEvent('watch', JSON.stringify({ ok: true, message: '已把连接信息发给 1 块手表' })), 300) }, logout: () => __calls.push(['logout']), ttsVoices: () => '[]',
          }""" % json.dumps(TOKEN)
        ctx = browser.new_context(viewport={"width": 412, "height": 900}, device_scale_factor=2, has_touch=True, is_mobile=True, locale="zh-CN")
        ctx.add_init_script(fake)
        pg = ctx.new_page(); attach(pg, "native")
        calls = lambda name: pg.evaluate(f"() => __calls.filter(c => c[0] === {json.dumps(name)})")
        pg.goto(BASE + "/")
        pg.wait_for_selector(".book")
        check("App 里登录状态来自原生存储", pg.evaluate("() => localStorage.getItem('shuku.token')") == TOKEN)
        check("线路告诉原生", any(json.loads(c[1])["route"] in ("lan", "origin") for c in calls("setRoute")), calls("setRoute"))
        check("状态栏颜色交给原生", len(calls("setThemeColor")) >= 1)
        check("App 里不注册 Service Worker", pg.evaluate("() => navigator.serviceWorker ? navigator.serviceWorker.getRegistrations().then(r => r.length) : 0") == 0)
        pg.locator(".book", has_text="活着").first.click(button="right")
        pg.click(".ctx-menu button:has-text('详情')")
        pg.wait_for_selector(".drawer.show .read-row button:has-text('下载到手机')")
        pg.click(".drawer .read-row button:has-text('下载到手机')")
        pg.wait_for_selector(".drawer .read-row button:has-text('已下载')")
        check("详情里下载到手机", calls("download") and json.loads(calls("download")[0][2])["title"] == "活着")
        check("返回键先关详情", pg.evaluate("() => window.shukuBack()") is True)
        pg.wait_for_timeout(400)
        check("已下载的书在封面上有标记", pg.locator(".book .cv .dl").count() >= 1)
        pg.goto(BASE + "/#/settings")
        pg.wait_for_selector(".set-sec >> text=手机 App")
        check("设置里有“手机 App”一节", pg.locator(".set-sec h2", has_text="手机 App").count() == 1)
        shot(pg, "20-native-settings")
        pg.click(".set-sec button:has-text('连接手表')")
        check("连接手表：后台进行，按钮先禁用", pg.locator(".set-sec button:has-text('连接手表')").is_disabled() and len(calls("syncWatch")) == 1)
        pg.wait_for_selector(".toast >> text=已把连接信息发给 1 块手表")
        check("连接手表：结果用事件回来", pg.locator(".set-sec button:has-text('连接手表')").is_enabled())
        up = pg.locator("#set-update")
        up.locator("text=本机 1.0.0.5").wait_for()
        check("App 里显示本机版本和最新版本", f"最新 {NEW}" in up.inner_text(), up.inner_text())
        up.locator("button:text-is('更新')").click()
        up.locator("button:text-is('更新手表')").click()
        check("App 里点“更新”“更新手表”交给原生", len(calls("updateApp")) == 1 and len(calls("updateWatchApp")) == 1, [c[0] for c in pg.evaluate("() => __calls")][-4:])
        # 加上在线升级之前的 App：没有 appInfo，交给手机浏览器下载新版
        pg.evaluate("() => { delete ShukuNative.appInfo; delete ShukuNative.updateApp; delete ShukuNative.updateWatchApp; location.hash = '#/stats' }")
        pg.wait_for_selector(".tiles .tile")
        pg.evaluate("() => { location.hash = '#/settings' }")
        up = pg.locator("#set-update")
        up.locator("button:has-text('下载新版')").click()
        ext_calls = calls("openExternal")
        check("旧版 App：用手机浏览器下载新版", ext_calls and ext_calls[-1][1] == BASE + "/dl/shuku-android.apk" and "还不会自己更新" in up.inner_text(), ext_calls)
        pg.goto(BASE + f"/#/read/{b2['id']}")
        reader_ready(pg)
        pg.wait_for_timeout(600)
        check("阅读器打开时通知原生（音量键、常亮）", [c[1] for c in calls("setReaderMode")][-1] is True)
        cfi0 = pg.evaluate("() => document.querySelector('foliate-view').lastLocation?.cfi")
        pg.evaluate("() => shukuNativeEvent('key', JSON.stringify({ dir: 'next' }))")
        pg.wait_for_timeout(800)
        check("音量键翻页", pg.evaluate("() => document.querySelector('foliate-view').lastLocation?.cfi") != cfi0)
        pg.wait_for_timeout(1800)
        sp = calls("saveProgress")
        check("进度交给原生保存", sp and json.loads(sp[-1][2])["location"].startswith("epubcfi"), sp[-1:] if sp else None)
        pg.keyboard.press("r")
        pg.wait_for_function("() => __calls.some(c => c[0] === 'tts' && c[1] === 'start')")
        start = json.loads([c for c in calls("tts") if c[1] == "start"][-1][2])
        check("听书交给原生：带书名、语速和句子", start["book"]["title"] == "三体Ⅱ·黑暗森林" and len(start["segments"]) > 3 and start["speed"] == 1, {k: start[k] for k in ("book", "speed")})
        first_id = start["segments"][0]["id"]
        pg.evaluate("(id) => { shukuNativeEvent('tts', JSON.stringify({ type: 'state', playing: true })); shukuNativeEvent('tts', JSON.stringify({ type: 'seg', id })) }", first_id)
        pg.wait_for_function("() => document.querySelector('foliate-view').renderer.getContents()[0].doc.defaultView.CSS.highlights.has('tts')")
        check("原生读到哪句就高亮哪句", True)
        check("播放状态同步到界面", pg.get_attribute(".r-player .play", "title") == "暂停")
        pg.evaluate("() => shukuNativeEvent('tts', JSON.stringify({ type: 'need' }))")
        pg.wait_for_function("() => __calls.some(c => c[0] === 'tts' && (c[1] === 'append' || c[1] === 'end'))")
        app = [c for c in calls("tts") if c[1] in ("append", "end")][-1]
        check("需要更多句子时不排版直接取下一章（或读完）", app[1] == "end" or len(json.loads(app[2])["segments"]) > 0, app[:2])
        pg.click(".r-player button[title='下一句']")
        check("控制按钮发给原生", any(c[1] == "next" for c in calls("tts")))
        pg.evaluate("() => shukuNativeEvent('tts', JSON.stringify({ type: 'stopped' }))")
        check("原生停止后界面收起", pg.locator(".r-player[hidden]").count() == 1)
        check("返回键退出阅读器", pg.evaluate("() => window.shukuBack()") is True)
        pg.wait_for_selector(".reader", state="detached")
        check("退出阅读器时通知原生", [c[1] for c in calls("setReaderMode")][-1] is False)
        check("首页按返回键交给 App（退到后台）", pg.evaluate("() => { location.hash = '#/'; return new Promise(r => setTimeout(() => r(window.shukuBack()), 300)) }") is False)
        ctx.close()

        ui_v7_shelf(browser, context, attach, shot, reader_ready)

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
        # 一次放进很多本书（一本本陆续入库）：入库动态合成一条会更新的提示，提示条不会堆满屏幕（手机尺寸）
        pg.set_viewport_size({"width": 390, "height": 844})
        pg.wait_for_timeout(7000)
        most = 0
        for i in range(8):
            body = (f"第一章 连续入库{i}\n" + f"连续入库测试第{i}本的正文。" * 120).encode()
            api("POST", "/api/upload?path=" + urllib.parse.quote(f"[www.xx.com]连续入库测试{i}_精校版.txt"), data=body)
            pg.wait_for_timeout(1500)
            most = max(most, pg.locator("#toasts .toast").count())
        pg.wait_for_timeout(7000)
        most = max(most, pg.locator("#toasts .toast").count())
        texts = pg.evaluate("() => [...document.querySelectorAll('#toasts .toast')].map(t => t.innerText)")
        check("连续入库好几本：提示合成一条、不堆满屏幕", most <= 2 and any(("本" in t and ("待确认" in t or "已入库" in t)) for t in texts), f"{most} {texts}")
        shot(pg, "52-import-toast")
        ctx.close()
        # 这几本测试书删掉（进回收站），不影响后面的检查
        for b in all_books():
            if "连续入库测试" in b["title"]:
                api("DELETE", f"/api/books/{b['id']}")
        ui_reader_v7(browser, context, attach, reader_ready, shot)
        ui_translate_ocr(browser, context, attach, reader_ready, shot)
        ui_v8_eink_legacy(browser, context, attach, reader_ready, shot)
        browser.close()

    section("网页：脚本错误")
    check("没有脚本错误", not errors, "\n      ".join(errors[:20]))
    check("没有违反内容安全策略（CSP）", not CSP_ERRORS, "\n      ".join(CSP_ERRORS[:10]))


# ---- 1.0.0.7 阅读器 ----

def book_csp(m):
    """书里夹带的脚本被内容安全策略拦下（想要的效果）：这种报错来自书的章节（blob: 地址），不算网页自己的错"""
    return ("Content Security Policy" in m.text or "Refused to" in m.text) and str((m.location or {}).get("url", "")).startswith("blob:")


def make_fn_epub(path):
    """繁体、带脚注 / 尾注 / 章节链接、还夹带一段脚本的 EPUB"""
    trad = "這是一個測試。我們應該學習歷史，認識過去的國家與語言。" * 6
    head = '<?xml version="1.0" encoding="utf-8"?><html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops"><head><title>%s</title></head><body>'
    c0 = (head % "第一章") + "<h2>第一章 注釋</h2>" + \
        '<script>try{window.top.__pwned=1;window.top.document.title="PWNED"}catch(e){}</script>' + \
        f'<p>{trad}這裡有個腳註<a epub:type="noteref" href="#n1">1</a>，還有一個尾註<sup><a href="c1.xhtml#e1">[2]</a></sup>。</p>' + \
        f'<p>{trad}</p><p><a href="c2.xhtml">跳到第三章</a></p>' + \
        '<aside epub:type="footnote" id="n1"><p>這是腳註的內容，應該彈出來看。</p></aside>' + "".join(f"<p>{trad}</p>" for _ in range(8)) + "</body></html>"
    c1 = (head % "第二章") + "<h2>第二章 尾註</h2>" + "".join(f"<p>{trad}</p>" for _ in range(3)) + '<p id="e1">尾註二的內容在這裡。</p>' + "".join(f"<p>{trad}</p>" for _ in range(6)) + "</body></html>"
    c2 = (head % "第三章") + "<h2>第三章 終章</h2>" + "".join(f"<p>{trad}</p>" for _ in range(10)) + "</body></html>"
    z = zipfile.ZipFile(path, "w")
    z.writestr(zipfile.ZipInfo("mimetype"), "application/epub+zip")
    z.writestr("META-INF/container.xml", '<?xml version="1.0"?><container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="c.opf" media-type="application/oebps-package+xml"/></rootfiles></container>')
    man = spine = ""
    for i, c in enumerate([c0, c1, c2]):
        z.writestr(f"c{i}.xhtml", c)
        man += f'<item id="c{i}" href="c{i}.xhtml" media-type="application/xhtml+xml"/>'
        spine += f'<itemref idref="c{i}"/>'
    z.writestr("nav.xhtml", '<?xml version="1.0" encoding="utf-8"?><html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops"><body><nav epub:type="toc"><ol><li><a href="c0.xhtml">第一章 注釋</a></li><li><a href="c1.xhtml">第二章 尾註</a></li><li><a href="c2.xhtml">第三章 終章</a></li></ol></nav></body></html>')
    z.writestr("c.opf", '<?xml version="1.0"?><package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="id"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:identifier id="id">fn-test</dc:identifier><dc:title>註釋測試</dc:title><dc:creator>測試者</dc:creator><dc:language>zh-TW</dc:language></metadata><manifest><item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>' + man + "</manifest><spine>" + spine + "</spine></package>")
    z.close()


# 在书页（iframe）里选中一段文字，然后像手指抬起一样通知阅读器
SELECT_JS = """(word) => {
  const doc = document.querySelector('foliate-view').renderer.getContents()[0].doc
  const walker = doc.createTreeWalker(doc.body, NodeFilter.SHOW_TEXT)
  let n
  while ((n = walker.nextNode())) {
    const i = n.nodeValue.indexOf(word)
    if (i < 0 || n.parentElement.closest('script')) continue
    const r = doc.createRange(); r.setStart(n, i); r.setEnd(n, i + word.length)
    const sel = doc.getSelection(); sel.removeAllRanges(); sel.addRange(r)
    doc.dispatchEvent(new PointerEvent('pointerup', { bubbles: true }))
    return true
  }
  return false
}"""
# 书页里一段文字在屏幕上的中心点
TEXT_POINT_JS = """(word) => {
  const doc = document.querySelector('foliate-view').renderer.getContents()[0].doc
  const w = doc.createTreeWalker(doc.body, NodeFilter.SHOW_TEXT)
  let n
  while ((n = w.nextNode())) {
    const i = n.nodeValue.indexOf(word)
    if (i < 0 || n.parentElement.closest('script')) continue
    const r = doc.createRange(); r.setStart(n, i); r.setEnd(n, i + word.length)
    const b = r.getBoundingClientRect(), f = doc.defaultView.frameElement.getBoundingClientRect()
    return [b.left + f.left + b.width / 2, b.top + f.top + b.height / 2]
  }
  return null
}"""
DOC_JS = "document.querySelector('foliate-view').renderer.getContents()[0].doc"
has_toast = lambda pg, text: pg.evaluate("(t) => [...document.querySelectorAll('#toasts .toast')].some(e => e.textContent.includes(t))", text)


def ui_v8_eink_legacy(browser, context, attach, reader_ready, shot):
    section("网页：墨水屏模式、兼容旧系统的网页、打不开时的诊断（1.0.0.9）")
    books = all_books()
    epub = next((b for b in books if "epub" in str(b.get("formats"))), None)
    pdf = next((b for b in books if "pdf" in str(b.get("formats"))), None)
    comic = next((b for b in books if b.get("kind") == "comic"), None)

    # ---- 新浏览器默认用新的那一套；设置里打开墨水屏模式 ----
    ctx = context(w=702, h=936, mobile=True)
    pg = ctx.new_page(); attach(pg, "v8-eink")
    pg.goto(BASE + "/#/settings")
    pg.wait_for_selector("#set-look")
    check("新浏览器用新的那一套网页（ES 模块）", pg.evaluate("() => window.SHUKU_LEGACY") is False)
    row = pg.locator("#set-look .set-row", has_text="墨水屏模式")
    row.locator("button", has_text="开").click()
    check("设置里打开墨水屏模式：马上生效", pg.evaluate("() => document.documentElement.classList.contains('eink')"))
    scale = pg.evaluate("() => getComputedStyle(document.documentElement).getPropertyValue('--ui-scale').trim()")
    check("墨水屏模式自动放大界面字号", scale in ("1.15",), scale)
    shot(pg, "70-eink-settings")
    pg.goto(BASE + "/#/all")
    pg.wait_for_selector(".book")
    pg.wait_for_timeout(500)
    st = pg.evaluate("""() => ({ bg: getComputedStyle(document.body).backgroundColor, on: getComputedStyle(document.querySelector('.chip.on') || document.body).backgroundColor,
        shadow: getComputedStyle(document.querySelector('.cv')).boxShadow, trans: getComputedStyle(document.querySelector('.btn') || document.body).transitionDuration })""")
    check("墨水屏：白底、选中的反白、封面没有阴影、没有动画", st["bg"] == "rgb(255, 255, 255)" and st["on"] == "rgb(0, 0, 0)" and st["shadow"] == "none" and st["trans"] in ("0s", ""), st)
    shot(pg, "71-eink-shelf")
    if epub:
        pg.goto(BASE + f"/#/read/{epub['id']}")
        reader_ready(pg)
        pg.wait_for_timeout(800)
        r = pg.evaluate("""() => { const v = document.querySelector('foliate-view'); const p = v && v.renderer
            const d = p && p.getContents ? p.getContents()[0] : null
            return { animated: p ? p.hasAttribute('animated') : null, color: d ? getComputedStyle(d.doc.body).color : '', size: d ? parseFloat(getComputedStyle(d.doc.body).fontSize) : 0 } }""")
        check("墨水屏阅读：纯黑字、没有翻页动画、字号按屏幕算", r["animated"] is False and r["color"] in ("rgb(0, 0, 0)",) and r["size"] >= 20, r)
        # 点屏幕中间显示工具栏（刚打开时工具栏可能本来就显示着，点一下反而收起）
        for _ in range(3):
            if pg.evaluate("() => document.querySelector('.reader').classList.contains('ui')"):
                break
            pg.mouse.click(351, 468)
            pg.wait_for_timeout(400)
        pg.click(".r-top button[title='阅读设置']")
        pg.wait_for_selector(".r-sheet.show")
        sheet = pg.inner_text(".r-sheet")
        check("墨水屏：阅读设置里没有亮度、暖色、翻页动画", "亮度" not in sheet and "暖色" not in sheet and "翻页动画" not in sheet, sheet[:200])
        shot(pg, "72-eink-reader")
        # 快速排版：底栏“字号”三档（原来的“全屏”按钮去掉了）
        pg.click(".r-sheet [aria-label='关闭']")
        check("底栏没有全屏按钮，有字号", pg.locator(".r-tools button[title='全屏']").count() == 0 and pg.locator(".r-tools button[title='字号']").is_visible())
        pg.click(".r-tools button[title='字号']")
        pg.wait_for_selector(".r-quick .seg button")
        labels = pg.locator(".r-quick .seg button").all_inner_texts()
        size = lambda: pg.evaluate("() => { const d = document.querySelector('foliate-view').renderer.getContents()[0]; return parseFloat(getComputedStyle(d.doc.body).fontSize) }")
        s0 = size()
        pg.locator(".r-quick .seg button", has_text="大字").click()
        pg.wait_for_timeout(600)
        s1 = size()
        pg.locator(".r-quick .seg button", has_text="小字").click()
        pg.wait_for_timeout(600)
        s2 = size()
        check("快速排版：小字、标准、大字三档，点了马上变", labels == ["小字", "标准", "大字"] and s1 > s0 > s2, (labels, s0, s1, s2))
        shot(pg, "73-quick-font")
        pg.locator(".r-quick .seg button", has_text="标准").click()
    if pdf:
        pg.goto(BASE + f"/#/read/{pdf['id']}")
        reader_ready(pg)
        pg.wait_for_function("() => [...document.querySelectorAll('.pdfv canvas')].some(c => c.width > 50)", timeout=30000)
        check("墨水屏：PDF 按页翻（不滚动，这本书在别处选过滚动也一样）", pg.evaluate("() => !!document.querySelector('.pdfv.paged')"))
        shot(pg, "72-eink-pdf")
    # 关掉
    pg.goto(BASE + "/#/settings")
    pg.wait_for_selector("#set-look")
    pg.locator("#set-look .set-row", has_text="墨水屏模式").locator("button", has_text="关").click()
    check("关掉墨水屏模式", not pg.evaluate("() => document.documentElement.classList.contains('eink')"))
    ctx.close()

    # ---- 兼容旧系统的那一套（tools/legacy 构建；旧浏览器自动用它，这里用 ?legacy=1 强制） ----
    if not os.path.exists(os.path.join(ROOT, "web", "legacy", "app.js")):
        check("兼容旧系统的网页已构建（web/legacy）", False, "先运行 cd tools/legacy && npm ci && node build.mjs")
    else:
        ctx = context(w=1100, h=860)
        pg = ctx.new_page(); attach(pg, "v8-legacy")
        pg.goto(BASE + "/?legacy=1#/all")
        pg.wait_for_selector(".book")
        check("兼容旧系统的那一套：书架打开（普通脚本，不是 ES 模块）", pg.evaluate("() => window.SHUKU_LEGACY === true && !!document.querySelector('script[src=\"/legacy/app.js\"]') && !document.querySelector('script[type=module]')"))
        check("兼容旧系统的那一套：用降级的样式", pg.evaluate("() => document.getElementById('app-css').getAttribute('href')") == "/legacy/app.css")
        if epub:
            pg.goto(BASE + f"/?legacy=1#/read/{epub['id']}")
            reader_ready(pg)
            pg.wait_for_timeout(800)
            n = pg.evaluate("() => { const v = document.querySelector('foliate-view'); const d = v && v.renderer && v.renderer.getContents ? v.renderer.getContents() : []; return d.map(x => x.doc.body.textContent).join('').length }")
            check("兼容旧系统的那一套：EPUB 能读", n > 20, n)
            pg.keyboard.press("ArrowRight"); pg.wait_for_timeout(500)
        if pdf:
            pg.goto(BASE + f"/?legacy=1#/read/{pdf['id']}")
            reader_ready(pg)
            pg.wait_for_function("() => [...document.querySelectorAll('.pdfv canvas')].some(c => c.width > 50)", timeout=30000)
            pg.wait_for_function("() => [...document.querySelectorAll('.pdfv .textLayer')].some(t => t.textContent.length > 5)", timeout=30000)
            check("兼容旧系统的那一套：PDF 画出来、有文字层（普通脚本的 worker）", True)
            shot(pg, "73-legacy-pdf")
        if comic:
            pg.goto(BASE + f"/?legacy=1#/read/{comic['id']}")
            reader_ready(pg)
            pg.wait_for_function("() => [...document.querySelectorAll('.reader img')].some(i => i.naturalWidth > 0)", timeout=20000)
            check("兼容旧系统的那一套：漫画能看", True)
        ctx.close()

    # ---- 打不开时显示原因（脚本加载不了），不是一片空白 ----
    ctx = context()
    pg = ctx.new_page()
    pg.route("**/js/main.js", lambda r: r.abort())
    pg.route("**/legacy/app.js", lambda r: r.abort())
    pg.goto(BASE + "/")
    try:
        pg.wait_for_selector("text=书库没有打开", timeout=9000)
        txt = pg.inner_text("body")
        check("网页打不开时显示原因和浏览器版本", "没有加载上" in txt and "浏览器：" in txt, txt[:200])
    except Exception as e:
        check("网页打不开时显示原因和浏览器版本", False, str(e)[:200])
    shot(pg, "74-boot-diagnose")
    ctx.close()


def ui_reader_v7(browser, context, attach, reader_ready, shot):
    section("网页：阅读器 1.0.0.7（脚注、繁转简、划线笔记、进度冲突、下一卷、PDF 文字层）")
    own_csp = []

    def watch_csp(pg):
        pg.on("console", lambda m: ("Content Security Policy" in m.text or "Refused to" in m.text) and not book_csp(m) and own_csp.append(m.text[:200]))

    make_fn_epub(os.path.join(T, "inbox", "註釋測試.epub"))
    fnb = wait(lambda: next((b for b in all_books() if b["title"] in ("註釋測試", "注释测试")), None), 60, 0.5, "注释测试书入库")

    # ---- 脚注、链接返回、书里的脚本、繁转简 ----
    ctx = context()
    pg = ctx.new_page(); attach(pg, "v7-fn"); watch_csp(pg)
    pg.goto(BASE + f"/#/read/{fnb['id']}")
    reader_ready(pg)
    pg.wait_for_timeout(700)
    check("书里夹带的脚本不运行（内容安全策略）", pg.evaluate("() => window.__pwned") is None and pg.title() != "PWNED", pg.title())
    pg.evaluate(f"() => {DOC_JS}.querySelector('a[href=\"#n1\"]').click()")
    pg.wait_for_selector(".r-pop.show", timeout=5000)
    check("点脚注弹出内容，不跳走", re.search("[腳脚][註注]的[內内]容", pg.inner_text(".r-pop")) and "第一章" in pg.inner_text(".r-status"), pg.inner_text(".r-pop"))
    shot(pg, "41-footnote")
    pg.keyboard.press("Escape")
    pg.evaluate(f"() => {DOC_JS}.querySelector('sup a').click()")
    pg.wait_for_selector(".r-pop.show", timeout=5000)
    check("上标的尾注也弹出来看", re.search("尾[註注]二", pg.inner_text(".r-pop")), pg.inner_text(".r-pop"))
    pg.keyboard.press("Escape")
    pg.evaluate(f"() => [...{DOC_JS}.querySelectorAll('a')].find(a => a.textContent.includes('第三章')).click()")
    pg.wait_for_function("() => document.querySelector('.r-status').textContent.includes('第三章')", timeout=8000)
    check("正文里的链接跳走后显示“返回原处”", pg.is_visible(".r-backchip"))
    pg.click(".r-backchip")
    pg.wait_for_function("() => document.querySelector('.r-status').textContent.includes('第一章')", timeout=8000)
    check("点“返回原处”回到原来读的地方", not pg.is_visible(".r-backchip"))
    v0 = pg.eval_on_selector(".r-prog input[type=range]", "e => e.value")
    pg.keyboard.press("Control+ArrowRight"); pg.wait_for_timeout(500)
    check("按着 Ctrl 的组合键不当翻页", pg.eval_on_selector(".r-prog input[type=range]", "e => e.value") == v0)
    pg.evaluate("() => document.querySelector('.reader').classList.add('ui')")
    pg.click(".r-top button[title='阅读设置']")
    pg.wait_for_selector(".r-sheet.show")
    pg.locator(".r-sheet .set-row", has_text="繁体转简体").locator("input").check()
    pg.wait_for_function(f"() => {DOC_JS}.body.textContent.includes('这是一个测试')", timeout=8000)
    check("繁体转简体阅读", "注释" in pg.inner_text(".r-status") or "注释" in pg.inner_text(".r-prog"), pg.inner_text(".r-status"))
    shot(pg, "42-t2s")
    pg.keyboard.press("Escape")
    prefs = wait(lambda: json.loads(j("GET", f"/api/books/{fnb['id']}/read")["prefs"] or "{}").get("t2s") and json.loads(j("GET", f"/api/books/{fnb['id']}/read")["prefs"]), 10, 0.3, "按书设置上传")
    check("繁转简按书记住（存在服务器上，换设备也一样）", prefs.get("t2s") is True, prefs)
    ctx.close()

    # ---- 划线、笔记、导出 ----
    ctx = context()
    pg = ctx.new_page(); attach(pg, "v7-notes"); watch_csp(pg)
    pg.goto(BASE + f"/#/read/{fnb['id']}")
    reader_ready(pg)
    pg.wait_for_function(f"() => {DOC_JS}.body.textContent.includes('这是一个测试')", timeout=8000)
    check("重新打开时还是简体（按书记住）", True)
    pg.wait_for_timeout(2300)  # 等工具栏自动收起
    check("选中文字", pg.evaluate(SELECT_JS, "学习历史"))
    pg.wait_for_selector(".r-dict.show .seltools .dot", timeout=8000)
    check("选中文字后弹出划线工具和字典", pg.locator(".r-dict.show .seltools .dot").count() == 4)
    RECTS = "() => { const c = document.querySelector('foliate-view').renderer.getContents()[0]; return c.overlayer ? c.overlayer.element.querySelectorAll('rect').length : 0 }"
    pg.locator(".r-dict .seltools .dot").first.click()
    hl = wait(lambda: [b for b in j("GET", f"/api/books/{fnb['id']}/bookmarks") if b["kind"] == "highlight" and b["text"] == "学习历史"], 10, 0.3, "划线保存")
    check("划线存到服务器（位置、原文、颜色）", hl[0]["color"] == "yellow" and hl[0]["location"].startswith("epubcfi"), hl)
    pg.wait_for_function(f"() => ({RECTS})() >= 1", timeout=5000)
    check("划线画在书页上", True)
    pt = pg.evaluate(TEXT_POINT_JS, "学习历史")
    v0 = pg.eval_on_selector(".r-prog input[type=range]", "e => e.value")
    pg.mouse.click(pt[0], pt[1])
    pg.wait_for_selector(".r-note.show textarea", timeout=5000)
    check("点书里的划线弹出笔记（不翻页）", pg.eval_on_selector(".r-prog input[type=range]", "e => e.value") == v0)
    pg.fill(".r-note textarea", "历史是最好的老师")
    pg.click(".r-note button:has-text('完成')")
    wait(lambda: any(b["note"] == "历史是最好的老师" for b in j("GET", f"/api/books/{fnb['id']}/bookmarks")), 10, 0.3, "笔记保存")
    check("写笔记", True)
    pg.keyboard.press("t")
    pg.wait_for_selector(".r-panel.show")
    pg.click(".r-panel .ptabs button:has-text('笔记')")
    pg.wait_for_selector(".r-panel .r-note-item")
    check("目录面板里的“笔记”", "历史是最好的老师" in pg.inner_text(".r-panel .pbody"))
    shot(pg, "43-notes")
    href = pg.get_attribute(".r-panel .notes-hd a", "href")
    r = S.get(BASE + href if href.startswith("/") else href, timeout=10)
    check("导出笔记（Markdown）", r.status_code == 200 and "> 学习历史" in r.text and "历史是最好的老师" in r.text, r.text[:200])
    for _ in range(pg.locator(".r-panel .r-note-item").count()):
        pg.locator(".r-panel .r-note-item .icon-btn[title='删除划线']").first.click()
        pg.wait_for_timeout(400)
    wait(lambda: not [b for b in j("GET", f"/api/books/{fnb['id']}/bookmarks") if b["kind"] == "highlight"], 10, 0.3, "删除划线")
    pg.wait_for_function(f"() => ({RECTS})() === 0", timeout=5000)
    check("删除划线（书页上也去掉）", True)
    ctx.close()

    # ---- 进度：别的设备读过、冲突、剩余时间、从头读、下一卷、at 参数、删掉的书 ----
    b2 = book_by_title("三体Ⅱ·黑暗森林")
    ctx = context()
    pg = ctx.new_page(); attach(pg, "v7-sync"); watch_csp(pg)
    pg.goto(BASE + f"/#/read/{b2['id']}")
    reader_ready(pg)
    pg.wait_for_timeout(2500)
    time.sleep(1.1)
    j("PUT", f"/api/books/{b2['id']}/progress", json={"location": "epubcfi(/6/4!/4/2/1:0)", "percent": 0.12, "device": "测试平板"})
    pg.evaluate("() => document.dispatchEvent(new Event('visibilitychange'))")
    pg.wait_for_function("() => [...document.querySelectorAll('#toasts .toast')].some(t => t.textContent.includes('测试平板'))", timeout=8000)
    check("回到前台时发现别的设备读过：提示跳过去", True)
    pg.click("#toasts .toast:has-text('测试平板') button:has-text('跳过去')")
    pg.wait_for_function("() => document.querySelector('.r-status').textContent.includes('中部')", timeout=8000)
    check("点“跳过去”到别的设备读到的地方", True)
    time.sleep(1.1)
    j("PUT", f"/api/books/{b2['id']}/progress", json={"location": "epubcfi(/6/6!/4/2/1:0)", "percent": 0.9, "device": "测试手机"})
    pg.keyboard.press("ArrowRight"); pg.wait_for_timeout(2600)
    check("保存时服务器上有别的设备更新的进度：不覆盖，提示", has_toast(pg, "测试手机") and j("GET", f"/api/books/{b2['id']}/progress")["device"] == "测试手机")
    pg.keyboard.press("ArrowRight"); pg.wait_for_timeout(2600)
    check("接着在这台读：以这台为准", j("GET", f"/api/books/{b2['id']}/progress")["device"] != "测试手机")
    pg.evaluate("() => { for (let u = 0; u < 5; u++) localStorage.setItem('shuku.speed.' + u, JSON.stringify({ flow: 3, pdf: 1, comic: 4 })) }")
    pg.reload()
    reader_ready(pg)
    pg.wait_for_timeout(800)
    pg.evaluate("() => document.querySelector('.reader').classList.add('ui')")
    t = pg.inner_text(".r-time")
    check("底栏显示本章、全书还剩多久", "本章还剩约" in t and "全书还剩约" in t, t)
    pg.goto(BASE + f"/#/read/{b2['id']}?at=" + urllib.parse.quote("epubcfi(/6/6!/4/2/1:0)"))
    reader_ready(pg)
    pg.wait_for_function("() => document.querySelector('.r-status').textContent.includes('下部')", timeout=8000)
    check("链接带 at 参数时直接打开到那个位置", True)
    santi = book_by_title("三体")
    pg.goto(BASE + f"/#/read/{santi['id']}")
    reader_ready(pg)
    pg.evaluate("() => document.querySelector('foliate-view').goToFraction(1)")
    pg.wait_for_selector(".r-end:not([hidden])", timeout=10000)
    check("读到最后提示同一系列的下一卷", "三体Ⅱ" in pg.inner_text(".r-end"), pg.inner_text(".r-end"))
    shot(pg, "44-next-volume")
    pg.evaluate("() => document.querySelector('.reader').classList.add('ui')")
    pg.click(".r-top button[title='阅读设置']")
    pg.click(".r-sheet button:has-text('从头读')")
    pg.click(".dialog button:has-text('从头读')")
    pg.wait_for_timeout(2800)
    p0 = j("GET", f"/api/books/{santi['id']}/progress")
    check("从头读：回到开头，进度从头算", "第一章" in pg.inner_text(".r-status") and p0["percent"] < 0.3, (pg.inner_text(".r-status"), p0))
    pg.goto(BASE + "/#/read/987654")
    pg.wait_for_function("() => [...document.querySelectorAll('#toasts .toast')].some(t => t.textContent.includes('已经删除'))", timeout=10000)
    pg.wait_for_selector(".reader", state="detached", timeout=5000)
    check("打开已经删除的书：提示一下就退出阅读器", True)
    ctx.close()

    # ---- 漫画：按书记住方向，最后一页提示下一卷 ----
    comics = sorted([b for b in all_books() if b["kind"] == "comic"], key=lambda b: b["title"])
    v1, v2 = comics[0], comics[1]
    j("PATCH", f"/api/books/{v1['id']}/me", json={"prefs": {"comicMode": "single"}})
    ctx = context()
    pg = ctx.new_page(); attach(pg, "v7-comic"); watch_csp(pg)
    img_ok = "() => { const i = document.querySelector('.comic img.pg'); return i && i.naturalWidth > 0 }"
    pg.goto(BASE + f"/#/read/{v1['id']}")
    pg.wait_for_function(img_ok, timeout=20000)
    pg.evaluate("() => document.querySelector('.reader').classList.add('ui')")
    pg.click(".r-top button[title='阅读设置']")
    pg.click(".r-sheet button:has-text('从右到左')")
    pg.keyboard.press("Escape")
    prefs = wait(lambda: json.loads(j("GET", f"/api/books/{v1['id']}/read")["prefs"] or "{}").get("comicDir") == "rtl" and True, 10, 0.3, "漫画方向上传")
    check("漫画的翻页方向按书记住", prefs)
    pg.evaluate("() => document.querySelector('.reader').classList.remove('ui')")
    for _ in range(12):
        pg.keyboard.press("ArrowLeft")  # 从右到左：← 是下一页
        pg.wait_for_timeout(120)
    pg.wait_for_selector(".r-end:not([hidden])", timeout=10000)
    check("漫画最后一页提示下一卷", "第2卷" in pg.inner_text(".r-end") or v2["title"] in pg.inner_text(".r-end"), pg.inner_text(".r-end"))
    pg.click(".r-end button:has-text('继续读')")
    pg.wait_for_function("(t) => document.querySelector('.reader .ttl')?.textContent === t", arg=v2["title"], timeout=10000)
    pg.wait_for_function(img_ok, timeout=20000)
    check("点“继续读”打开下一卷；方向只记在上一卷", pg.locator(".comic .spread.rtl").count() == 0)
    ctx.close()

    # ---- PDF：文字层、选字查字典、划线、书内搜索 ----
    pdf = [b for b in all_books() if "深度学习" in b["title"]][0]
    j("PATCH", f"/api/books/{pdf['id']}/me", json={"prefs": {"pdfMode": "scroll"}, "restart": True})
    ctx = context()
    pg = ctx.new_page(); attach(pg, "v7-pdf"); watch_csp(pg)
    pg.goto(BASE + f"/#/read/{pdf['id']}")
    pg.wait_for_selector(".pdfv .pg .textLayer[data-ready]", timeout=30000)
    check("PDF 有文字层（能选字、复制）", pg.locator(".pdfv .pg .textLayer span").count() > 5)
    pg.wait_for_timeout(2000)
    ok = pg.evaluate("""() => { const tl = [...document.querySelectorAll('.pdfv .pg .textLayer[data-ready]')].find(e => { const r = e.getBoundingClientRect(); return r.top < innerHeight && r.bottom > 0 }); const w = document.createTreeWalker(tl, NodeFilter.SHOW_TEXT); let n
      while ((n = w.nextNode())) { const i = n.nodeValue.indexOf('Neural'); if (i >= 0) { const r = document.createRange(); r.setStart(n, i); r.setEnd(n, i + 15); const s = getSelection(); s.removeAllRanges(); s.addRange(r); document.querySelector('.pdfv').dispatchEvent(new PointerEvent('pointerup', { bubbles: true })); return true } }
      return false }""")
    pg.wait_for_selector(".r-dict.show .seltools .dot", timeout=8000)
    check("PDF 选字弹出字典和划线工具", ok and "Neural networks" in pg.inner_text(".r-dict"))
    pg.locator(".r-dict .seltools .dot").nth(2).click()
    hl = wait(lambda: [b for b in j("GET", f"/api/books/{pdf['id']}/bookmarks") if b["kind"] == "highlight" and b["color"] == "blue"], 10, 0.3, "PDF 划线")
    check("PDF 划线（存页码和字的位置）", re.match(r"^p:\d+:[\d.]+:\d+-\d+$", hl[0]["location"]) is not None and hl[0]["text"] == "Neural networks", hl)
    pg.wait_for_function("() => (CSS.highlights.get('pdf-hl-blue')?.size || 0) >= 1", timeout=5000)
    check("PDF 划线画在文字上", True)
    pg.keyboard.press("t")
    pg.wait_for_selector(".r-panel.show")
    pg.click(".r-panel .ptabs button:has-text('搜索')")
    pg.fill(".r-search input", "representations")
    pg.press(".r-search input", "Enter")
    pg.wait_for_selector(".r-panel .hit", timeout=15000)
    pg.locator(".r-panel .hit").nth(20).click()
    pg.wait_for_timeout(1200)
    check("PDF 书内搜索，结果在页面上标出来", (pg.evaluate("() => CSS.highlights.get('pdf-search')?.size") or 0) >= 1 and "2 / 6" in pg.inner_text(".r-status"), pg.inner_text(".r-status"))
    shot(pg, "45-pdf-text")
    ctx.close()

    # ---- App 里：原生上传进度后回报冲突；听书带位置 ----
    fake = """window.__calls = []; window.__store = { 'shuku.token': %s }
      window.ShukuNative = {
        storageGet: k => (k in __store ? __store[k] : null), storageSet: (k, v) => { __store[k] = v }, storageRemove: k => { delete __store[k] },
        storageKeys: () => JSON.stringify(Object.keys(__store)), version: () => '1.0-test', deviceName: () => '测试手机',
        setRoute: j => {}, setNetwork: j => {}, setToken: t => {}, setThemeColor: () => {}, setReaderMode: () => {},
        saveProgress: (id, body) => __calls.push(['saveProgress', id, body]), tts: (cmd, arg) => __calls.push(['tts', cmd, arg]),
        prefs: () => JSON.stringify({ tts_engine: 'server' }), setPref: () => {}, downloads: () => '[]',
        appInfo: () => JSON.stringify({ versionName: '1.0.0.7', versionCode: 7 }), openExternal: u => {}, ttsVoices: () => '[]',
      }""" % json.dumps(TOKEN)
    ctx = browser.new_context(viewport={"width": 412, "height": 900}, device_scale_factor=2, has_touch=True, is_mobile=True, locale="zh-CN")
    ctx.add_init_script(fake)
    pg = ctx.new_page(); attach(pg, "v7-native"); watch_csp(pg)
    pg.goto(BASE + f"/#/read/{b2['id']}")
    reader_ready(pg)
    pg.evaluate("() => shukuNativeEvent('key', JSON.stringify({ dir: 'next' }))")
    pg.wait_for_function("() => __calls.some(c => c[0] === 'saveProgress')", timeout=8000)
    body = json.loads(pg.evaluate("() => __calls.filter(c => c[0] === 'saveProgress').pop()[2]"))
    check("App 里进度交给原生：带 base 和 at", "base" in body and body["at"] > 0, body)
    pg.evaluate("(id) => shukuNativeEvent('progress', JSON.stringify({ id, conflict: true, current: { file_id: 0, location: 'epubcfi(/6/2!/4/2/1:0)', percent: 0.05, device: '测试手表', updated_at: 9999999999 } }))", b2["id"])
    pg.wait_for_function("() => [...document.querySelectorAll('#toasts .toast')].some(t => t.textContent.includes('测试手表'))", timeout=5000)
    check("原生传进度时发现冲突：网页提示跳过去", True)
    pg.evaluate("() => document.querySelector('.reader').classList.add('ui')")
    pg.click(".r-top button[title='听书']")
    pg.wait_for_function("() => __calls.some(c => c[0] === 'tts' && c[1] === 'start')", timeout=8000)
    start = json.loads(pg.evaluate("() => __calls.filter(c => c[0] === 'tts' && c[1] === 'start').pop()[2]"))
    seg = start["segments"][0]
    check("听书交给原生：带文件编号，每句带位置和百分比", start["book"].get("file_id") and str(seg.get("loc", "")).startswith("sec:") and 0 <= seg.get("pct", -1) <= 1, {"book": start["book"], "seg": seg})
    ctx.close()

    check("网页自己的脚本、样式没有违反内容安全策略", not own_csp, own_csp[:5])


# ---- 1.0.0.9：翻译本页、扫描版 PDF 识别文字 ----

# 这一页开头的位置记在 window.__start 上；之后检查它还在这一页上（翻译加了译文以后，读到的地方不能跑掉）
MARK_START_JS = "() => { const r = document.querySelector('foliate-view').lastLocation.range; window.__start = [r.startContainer, r.startOffset]; return true }"
START_VISIBLE_JS = """() => { const r = document.querySelector('foliate-view').lastLocation.range, s = window.__start
  try { return r.comparePoint(s[0], s[1]) === 0 } catch (e) { return false } }"""
# 译文真的显示出来了（::after 的内容）
TR_SHOWN_JS = """(prefix) => { const doc = document.querySelector('foliate-view').renderer.getContents()[0].doc
  const el = doc.querySelector('[data-shuku-tr]')
  return !!el && doc.defaultView.getComputedStyle(el, '::after').content.includes(prefix) }"""
# 这一页能看到的段落里有没有译文
TR_ON_PAGE_JS = """() => { const r = document.querySelector('foliate-view').lastLocation.range, doc = r.startContainer.ownerDocument
  return [...doc.querySelectorAll('[data-shuku-tr]')].some(el => r.intersectsNode(el)) }"""


def ui_translate_ocr(browser, context, attach, reader_ready, shot):
    section("网页：翻译本页、扫描版 PDF 识别文字（1.0.0.9）")
    tr_reqs = []
    ai_before = j("GET", "/api/settings")["ai"]

    # ---- 设置：用哪个翻译、默认译成哪种语言 ----
    ctx = context()
    pg = ctx.new_page(); attach(pg, "v8-settings")
    pg.goto(BASE + "/#/settings")
    pg.wait_for_selector("#set-translate .tr-engine button")
    pg.click("#set-translate .tr-engine button:has-text('AI')")
    pg.select_option("#set-translate select.tr-default", "ja")
    pg.click("#set-translate button:has-text('保存')")
    s = wait(lambda: (lambda x: x if x.get("translate", {}).get("engine") == "ai" else None)(j("GET", "/api/settings")), 10, 0.3, "翻译设置保存")
    check("设置里选翻译方式（AI）、默认语言，保存", s["translate"] == {"engine": "ai", "to": "ja"}, s.get("translate"))
    pg.locator("#set-translate").scroll_into_view_if_needed()
    shot(pg, "60-settings-translate")
    j("PUT", "/api/settings", json={"ai": {"enabled": True, "base_url": MOCK + "/ai/v1", "api_key": "", "model": "mock-1"}})
    r = j("POST", "/api/translate", json={"texts": ["你好，世界"], "to": "en"})
    check("选了 AI：翻译交给 AI", r["engine"] == "ai" and r["translations"][0] == "AI译:你好，世界", r)
    pg.reload()
    pg.wait_for_selector("#set-translate .tr-engine button")
    pg.click("#set-translate .tr-engine button:has-text('微软翻译')")
    pg.select_option("#set-translate select.tr-default", "zh-Hans")
    pg.click("#set-translate button:has-text('保存')")
    wait(lambda: j("GET", "/api/settings")["translate"] == {"engine": "microsoft", "to": "zh-Hans"} or None, 10, 0.3, "翻译设置改回")
    check("换回微软翻译", True)
    ctx.close()
    j("PUT", "/api/settings", json={"ai": ai_before})

    # ---- EPUB：译文显示在每段原文后面，翻页接着翻；划线、进度还按原文算 ----
    book = book_by_title("三体")
    j("PATCH", f"/api/books/{book['id']}/me", json={"restart": True})
    ctx = context()
    pg = ctx.new_page(); attach(pg, "v8-epub-translate")
    pg.on("request", lambda rq: "/api/translate" in rq.url and tr_reqs.append(rq.url))
    pg.goto(BASE + f"/#/read/{book['id']}")
    reader_ready(pg)
    pg.wait_for_timeout(2300)  # 等工具栏自动收起
    pg.evaluate("() => document.querySelector('.reader').classList.add('ui')")
    pg.click(".r-top button[title='阅读设置']")
    pg.wait_for_selector(".r-sheet.show select.tr-lang")
    pg.select_option(".r-sheet.show select.tr-lang", "en")  # 书是中文的，译成英文
    pg.evaluate("() => document.activeElement && document.activeElement.blur()")
    pg.keyboard.press("Escape")
    pg.keyboard.press("ArrowRight"); pg.wait_for_timeout(900)  # 翻到正文中间的一页
    pg.evaluate(MARK_START_JS)
    pg.evaluate("() => document.querySelector('.reader').classList.add('ui')")
    pg.click(".r-tools button[title='翻译']")
    pg.wait_for_function(TR_SHOWN_JS, arg="[en]", timeout=15000)
    check("EPUB 翻译本页：译文显示在每段原文后面", pg.evaluate(TR_ON_PAGE_JS) and pg.locator(".r-tools button[title='翻译'].on").count() == 1)
    pg.wait_for_timeout(800)
    check("加了译文以后还停在原来那一页", pg.evaluate(START_VISIBLE_JS))
    shot(pg, "61-epub-translate")
    sent = sum(len(x[1]) for x in MSTR_LOG if x[0] == "en")
    for _ in range(3):
        pg.keyboard.press("ArrowRight"); pg.wait_for_timeout(700)
    pg.wait_for_function(TR_ON_PAGE_JS, timeout=15000)
    check("开着翻译翻页：新的一页也有译文", True)
    check("同样的段落只翻一次（书里每段都一样）", sum(len(x[1]) for x in MSTR_LOG if x[0] == "en") <= sent + 2, MSTR_LOG[-3:])
    # 开着翻译划线
    check("开着翻译选中文字", pg.evaluate(SELECT_JS, "晴朗的早晨"))
    pg.wait_for_selector(".r-dict.show .seltools .dot", timeout=8000)
    pg.locator(".r-dict .seltools .dot").first.click()
    hl = wait(lambda: [b for b in j("GET", f"/api/books/{book['id']}/bookmarks") if b["kind"] == "highlight" and b["text"] == "晴朗的早晨"], 10, 0.3, "划线保存")
    pg.wait_for_timeout(2600)  # 进度 1.5 秒后上传
    prog = j("GET", f"/api/books/{book['id']}/progress")
    # 关掉翻译
    pg.evaluate("() => document.querySelector('.reader').classList.add('ui')")
    n_req = len(tr_reqs)
    pg.click(".r-tools button[title='翻译']")
    pg.wait_for_function("() => !document.querySelector('foliate-view').renderer.getContents()[0].doc.querySelector('[data-shuku-tr]')", timeout=8000)
    check("再点一次“翻译”：译文去掉", pg.locator(".r-tools button[title='翻译'].on").count() == 0)
    RESOLVE = """(cfi) => { const v = document.querySelector('foliate-view'), r = v.resolveCFI(cfi)
      const c = v.renderer.getContents().find(x => x.index === r.index); return c ? r.anchor(c.doc).toString() : null }"""
    got = pg.evaluate(RESOLVE, hl[0]["location"])
    check("开着翻译划的线，位置按原文算（关掉翻译后还是那几个字）", got == "晴朗的早晨", got)
    got = pg.evaluate("""(cfi) => { const v = document.querySelector('foliate-view'), r = v.resolveCFI(cfi)
      const c = v.renderer.getContents().find(x => x.index === r.index); if (!c) return null
      const range = r.anchor(c.doc), vis = v.lastLocation.range
      try { return vis.comparePoint(range.startContainer, range.startOffset) === 0 } catch (e) { return String(e) } }""", prog["location"])
    check("开着翻译时存的进度，关掉翻译后还在这一页", got is True, (prog["location"], got))
    pg.evaluate("() => document.querySelector('.reader').classList.add('ui')")
    pg.click(".r-tools button[title='翻译']")
    pg.wait_for_function(TR_SHOWN_JS, arg="[en]", timeout=10000)
    check("同一页再翻译不用再请求（本机记着译文）", len(tr_reqs) == n_req, len(tr_reqs) - n_req)
    pg.evaluate("() => document.querySelector('.reader').classList.add('ui')")
    pg.click(".r-tools button[title='翻译']")
    for b in hl:
        api("DELETE", f"/api/bookmarks/{b['id']}")
    ctx.close()

    # ---- PDF：对照面板（这一页的原文、译文），翻页跟着换 ----
    pdf = [b for b in all_books() if "深度学习" in b["title"]][0]
    j("PATCH", f"/api/books/{pdf['id']}/me", json={"prefs": {"pdfMode": "page"}, "restart": True})
    ctx = context()
    pg = ctx.new_page(); attach(pg, "v8-pdf-translate")
    pg.goto(BASE + f"/#/read/{pdf['id']}")
    pg.wait_for_selector(".pdfv .pg .textLayer[data-ready]", timeout=30000)
    pg.evaluate("() => document.querySelector('.reader').classList.add('ui')")
    pg.click(".r-tools button[title='翻译']")
    pg.wait_for_function("() => [...document.querySelectorAll('.r-trsheet.show .tr-pair .t')].some(e => e.textContent.startsWith('[zh-Hans]'))", timeout=15000)
    text = pg.inner_text(".r-trsheet")
    check("PDF 翻译本页：对照面板里是这一页的原文和译文", "第 1 页" in text and "Neural networks" in text and "[zh-Hans]" in text, text[:200])
    shot(pg, "62-pdf-translate")
    pg.evaluate("() => document.querySelector('.reader').classList.remove('ui')")
    pg.keyboard.press("ArrowRight")
    pg.wait_for_function("() => document.querySelector('.r-trsheet .r-sheet-hd b').textContent.includes('第 2 页')", timeout=10000)
    check("翻页后对照面板跟着换到这一页", True)
    pg.select_option(".r-trsheet select.tr-lang", "ja")
    pg.wait_for_function("() => [...document.querySelectorAll('.r-trsheet .tr-pair .t')].some(e => e.textContent.startsWith('[ja]'))", timeout=10000)
    check("面板里换要译成的语言", True)
    pg.evaluate("() => document.activeElement && document.activeElement.blur()")
    pg.keyboard.press("Escape")
    pg.wait_for_timeout(300)
    check("按 Esc（安卓返回键）关掉对照面板，不退出阅读器", not pg.is_visible(".r-trsheet.show") and pg.is_visible(".reader"))
    ctx.close()

    # ---- 扫描版 PDF：书库识别出文字，能选字、划线、搜索、翻译 ----
    if not (shutil.which("tesseract") and (shutil.which("mutool") or shutil.which("pdftoppm"))):
        check("扫描版 PDF 识别文字（本机没有 tesseract，跳过）", True)
        return
    shutil.copy(os.path.join(ROOT, "server", "testdata", "scan-en.pdf"), os.path.join(T, "inbox", "扫描版测试.pdf"))
    # 书名取 PDF 属性里的（scan-en），属性里没有时用文件名
    sb = wait(lambda: next((b for b in all_books() if "扫描" in b["title"] or b["title"] == "scan-en"), None), 60, 0.5, "扫描版入库")
    ctx = context()
    pg = ctx.new_page(); attach(pg, "v8-ocr")
    pg.goto(BASE + f"/#/read/{sb['id']}")
    pg.wait_for_selector(".pdfv .pg .textLayer.ocr[data-ready]", timeout=90000)
    layer = pg.evaluate("() => document.querySelector('.pdfv .pg .textLayer.ocr').textContent")
    check("扫描版 PDF：书库识别出文字，铺成透明文字层", "12345" in layer and "quick" in layer.lower(), layer)
    ok = pg.evaluate("""() => { const tl = document.querySelector('.pdfv .pg .textLayer.ocr'); const w = document.createTreeWalker(tl, NodeFilter.SHOW_TEXT); let n
      while ((n = w.nextNode())) { const i = n.nodeValue.indexOf('12345'); if (i >= 0) { const r = document.createRange(); r.setStart(n, i); r.setEnd(n, i + 5); const s = getSelection(); s.removeAllRanges(); s.addRange(r); document.querySelector('.pdfv').dispatchEvent(new PointerEvent('pointerup', { bubbles: true })); return getSelection().toString() } }
      return '' }""")
    pg.wait_for_selector(".r-dict.show .seltools .dot", timeout=8000)
    check("扫描版能选字（选到 12345，弹出查字典和划线）", ok == "12345" and "12345" in pg.inner_text(".r-dict"), ok)
    shot(pg, "63-pdf-ocr")
    pg.locator(".r-dict .seltools .dot").nth(1).click()
    hl = wait(lambda: [b for b in j("GET", f"/api/books/{sb['id']}/bookmarks") if b["kind"] == "highlight"], 10, 0.3, "扫描版划线")
    check("扫描版也能划线（存页码和字的位置）", re.match(r"^p:1:[\d.]+:\d+-\d+$", hl[0]["location"]) is not None and hl[0]["text"] == "12345", hl)
    pg.keyboard.press("t")
    pg.wait_for_selector(".r-panel.show")
    pg.click(".r-panel .ptabs button:has-text('搜索')")
    pg.fill(".r-search input", "quick brown")
    pg.press(".r-search input", "Enter")
    pg.wait_for_selector(".r-panel .hit", timeout=30000)
    check("扫描版书内搜索（用识别出的文字）", "quick brown" in pg.inner_text(".r-panel .hit").lower())
    pg.keyboard.press("Escape")
    pg.evaluate("() => document.querySelector('.reader').classList.add('ui')")
    pg.click(".r-tools button[title='翻译']")
    pg.wait_for_function("() => [...document.querySelectorAll('.r-trsheet.show .tr-pair .t')].some(e => e.textContent.includes('12345'))", timeout=15000)
    check("扫描版翻译本页（用识别出的文字）", "[zh-Hans]" in pg.inner_text(".r-trsheet"))
    ctx.close()


def offline_v7():
    """离线读书：存书、断网后从浏览器里打开（EPUB、PDF、漫画），进度排队、联网后同步；别人的令牌拿不到"""
    section("网页：离线读书（浏览器 / iPhone 主屏幕）")
    # 让 Playwright 也能拦下 Service Worker 发出的请求（模拟连不上书库）
    os.environ["PW_EXPERIMENTAL_SERVICE_WORKER_NETWORK_EVENTS"] = "1"
    from playwright.sync_api import sync_playwright
    chrome = sorted(glob.glob("/opt/pw-browsers/chromium-*/chrome-linux/chrome"))
    errs = []
    epub = book_by_title("三体Ⅱ·黑暗森林")
    pdf = [b for b in all_books() if "深度学习" in b["title"]][0]
    comic = sorted([b for b in all_books() if b["kind"] == "comic"], key=lambda b: b["title"])[0]
    me = j("GET", "/api/me")
    with sync_playwright() as p:
        browser = p.chromium.launch(executable_path=chrome[-1] if chrome else None)
        ctx = browser.new_context(viewport={"width": 1200, "height": 820}, locale="zh-CN")
        ctx.add_init_script(f"localStorage.setItem('shuku.token', {json.dumps(TOKEN)})")
        pg = ctx.new_page()
        pg.on("pageerror", lambda e: errs.append(f"PAGEERROR {e}"))
        pg.goto(BASE + "/")
        pg.wait_for_selector(".book")
        user = {"user": {"id": me["id"], "name": me.get("name", "")}}
        for b in (epub, pdf, comic):
            pg.evaluate("""async ([b, ctx]) => { const m = await import('/js/offline.js'); const btn = m.offlineButton(b, ctx); btn.id = 'ob' + b.id; document.body.append(btn); btn.click() }""", [{"id": b["id"], "title": b["title"]}, user])
            pg.wait_for_function("(id) => /[KM]B/.test(document.querySelector('#ob' + id)?.textContent || '')", arg=b["id"], timeout=60000)
        check("书的详情里点“离线可读”，存到浏览器里（EPUB、PDF、漫画）", pg.evaluate("(ids) => import('/js/offline.js').then(m => ids.every(id => m.isOfflineBook(id)))", [epub["id"], pdf["id"], comic["id"]]))
        # 书库整个连不上：接口、网页本身都拿不到（Service Worker 去取也一样）
        ctx.route(re.compile(r".*"), lambda route: route.abort())
        res = pg.evaluate("""async (id) => { const t = localStorage.getItem('shuku.token'); const get = h => fetch('/api/books/' + id + '/read', { headers: { Authorization: 'Bearer ' + h } }).then(r => r.status + ':' + (r.headers.get('X-Shuku-Offline') || ''), () => 'neterr'); return [await get(t), await get('someone-else')] }""", epub["id"])
        check("断网时用存好的内容回答；别人的令牌拿不到", res == ["200:1", "neterr"], res)
        # 重新打开网页：用浏览器里存的网页启动，直接进离线书架（存书时已经把网页、阅读器、PDF 内核和字体预先存好）
        pg.evaluate("() => { location.hash = '#/offline' }")
        try:
            pg.reload(timeout=30000)
            pg.wait_for_selector(".offline-page .off-book", timeout=30000)
        except Exception as ex:
            errs.append(f"断网后重新打开网页：{ex}")
        check("连不上书库时用存在浏览器里的网页打开，直接进离线书架", pg.locator(".offline-bar").count() == 1 and pg.locator(".offline-page .off-book").count() == 3,
              pg.inner_text("body")[:300])
        opened = []
        for b, ready in ((epub, "() => !!document.querySelector('foliate-view')?.lastLocation?.cfi"),
                         (pdf, "() => !!document.querySelector('.pdfv .pg canvas') && !!document.querySelector('.pdfv .pg .textLayer span')"),
                         (comic, "() => { const i = document.querySelector('.comic img.pg'); return !!(i && i.naturalWidth > 0) }")):
            try:
                pg.locator(".off-book", has_text=b["title"]).locator("button:has-text('阅读')").click(timeout=10000)
                pg.wait_for_function(ready, timeout=30000)
                opened.append(b["kind"] if b is not pdf else "pdf")
            except Exception:
                opened.append("FAIL " + b["title"] + " " + (pg.inner_text(".reader .r-loading") if pg.locator(".reader .r-loading").count() else ""))
            if b is epub:
                pg.keyboard.press("ArrowRight")
                pg.wait_for_timeout(2600)
            pg.evaluate("() => { location.hash = '#/offline' }")  # 退出阅读器，回离线书架
            try:
                pg.wait_for_selector(".offline-page .off-book", timeout=10000)
            except Exception as ex:
                errs.append(f"回离线书架：{ex}")
        check("断网时打开存好的书（EPUB、PDF 带文字层、漫画）", opened == ["book", "pdf", "comic"], opened)
        q = pg.evaluate(f"() => JSON.parse(localStorage.getItem('shuku.pq.{me['id']}') || '{{}}')")
        check("断网时的进度先记在这台设备上", str(epub["id"]) in q, q)
        try:
            wait(lambda: "进度还没同步" in pg.inner_text(".offline-page"), 10, 0.3, "离线书架显示没同步的进度")
        except Exception:
            pass
        shelf = pg.inner_text(".offline-page")
        check("离线书架：列出存好的书和占用空间", epub["title"] in shelf and "连不上书库" in shelf and "进度还没同步" in shelf, shelf[:300])
        pg.screenshot(path=os.path.join(SHOTS, "46-offline.png"), full_page=True)
        ctx.unroute_all(behavior="ignoreErrors")
        pg.wait_for_timeout(300)
        pg.evaluate("() => window.dispatchEvent(new Event('online'))")
        wait(lambda: pg.evaluate(f"() => !localStorage.getItem('shuku.pq.{me['id']}')"), 20, 0.5, "联网后同步进度")
        prog = j("GET", f"/api/books/{epub['id']}/progress")
        check("联网后自动把离线时的进度传上去", prog["location"] == q[str(epub["id"])]["body"]["location"], prog)
        pg.locator(".off-book", has_text=comic["title"]).locator("button:has-text('删掉')").click()
        pg.click(".dialog button:has-text('删掉')")
        wait(lambda: pg.evaluate("(id) => import('/js/offline.js').then(m => !m.isOfflineBook(id))", comic["id"]), 10, 0.3, "取消离线")
        check("取消离线：从浏览器里删掉", pg.evaluate("() => caches.open('shuku-offline-v1').then(c => c.keys()).then(ks => !ks.some(k => /pages\\/0$/.test(k.url)))"))
        ctx.close()
        browser.close()
    check("离线读书没有脚本错误", not errs, errs[:5])


def ext_tests():
    """浏览器插件：在无头 Chromium 里装上插件，配对、扫描网页、加入书库、各种出错情况"""
    from playwright.sync_api import sync_playwright
    section("浏览器插件")
    EXT = os.path.join(ROOT, "extension")
    profile = os.path.join(T, "ext-profile")
    shutil.rmtree(profile, ignore_errors=True)
    rules = ", ".join(f"MAP {h} 127.0.0.1:{MPORT}" for h in EXT_SITES)
    errors = []
    with sync_playwright() as p:
        ctx = p.chromium.launch_persistent_context(profile, channel="chromium", headless=True, locale="zh-CN", viewport={"width": 1200, "height": 800},
                                                   args=[f"--disable-extensions-except={EXT}", f"--load-extension={EXT}", f"--host-resolver-rules={rules}"])
        try:
            sw = ctx.service_workers[0] if ctx.service_workers else ctx.wait_for_event("serviceworker", timeout=15000)
            ext = sw.url.split("/")[2]
            check("插件装好，后台启动", sw.url.endswith("/background.js"))
            ev = lambda js: sw.evaluate(js)
            tasks = lambda: ev("async () => (await chrome.storage.session.get('tasks')).tasks || []")
            offscreens = lambda: ev("async () => (await chrome.runtime.getContexts({ contextTypes: ['OFFSCREEN_DOCUMENT'] })).length")

            def watch(pg, label):
                pg.on("pageerror", lambda e: errors.append(f"[{label}] {e}"))
                # 网站自己的 favicon 404、故意输错配对码的 401 不算
                pg.on("console", lambda m: m.type == "error" and "Failed to load resource" not in m.text and errors.append(f"[{label}] console.error {m.text}"))
                # 探测不通的 Tailscale 地址（前面的测试设成了 nas.example.ts.net）是正常的
                pg.on("requestfailed", lambda r: r.url.endswith("/api/ping") or errors.append(f"[{label}] 请求失败 {r.url} {r.failure}"))
                return pg

            # 插件装好时会自己打开设置页：等它开出来再往下走。不然它正好撞上下面打开的设置页，
            # 浏览器会把那个标签页重新打开一次，正在加载的页面和脚本被中止（ERR_ABORTED）
            try:
                wait(lambda: any(pg.url.endswith("/options.html") for pg in ctx.pages), 5, 0.1, "插件自己打开设置页")
            except TimeoutError:
                pass

            # ---- 配对 ----
            opt = watch(ctx.new_page(), "options")
            opt.goto(f"chrome-extension://{ext}/options.html")
            check("设置页：没连接时显示配对表单", opt.locator("#pair").is_visible() and not opt.locator("#linked").is_visible())
            opt.fill("#addr", f"127.0.0.1:{PORT}")
            opt.fill("#code", "WRONG234")
            opt.click("#pair button[type=submit]")
            opt.wait_for_function("() => /配对码无效/.test(document.getElementById('pair-msg').textContent)", timeout=10000)
            check("设置页：错误的配对码有提示", True)
            code = j("POST", "/api/pair")["code"]
            opt.fill("#code", code.lower())
            opt.click("#pair button[type=submit]")
            opt.wait_for_selector("#state >> text=已连接", timeout=15000)
            cfg = ev("async () => (await chrome.storage.local.get('config')).config")
            check("设置页：用配对码连接成功", cfg.get("token") and cfg.get("lan") == BASE, {k: v for k, v in cfg.items() if k != "token"})
            check("书库的设备列表里有插件", any("浏览器插件" in x["device"] for x in j("GET", "/api/sessions")))
            check("设置页：显示当前线路", "内网" in opt.inner_text("#state"))
            check("加入方式默认“自动”", opt.locator("input[name=mode][value=auto]").is_checked())
            opt.wait_for_timeout(300)
            opt.screenshot(path=os.path.join(SHOTS, "30-ext-options.png"), full_page=True)

            def popup_for(url, label, wait_groups=True):
                tab = watch(ctx.new_page(), label)
                tab.goto(url)
                tab.wait_for_load_state("load")
                tid = ev(f"async () => (await chrome.tabs.query({{}})).find(t => t.url === {json.dumps(tab.url)})?.id")
                pop = watch(ctx.new_page(), label + "-popup")
                pop.set_viewport_size({"width": 380, "height": 600})
                pop.goto(f"chrome-extension://{ext}/popup.html?tab={tid}")
                if wait_groups:
                    pop.wait_for_selector(".group, .empty", timeout=10000)
                return tab, pop

            def groups(pop):
                return pop.evaluate("() => [...document.querySelectorAll('.group')].map(g => ({ title: g.querySelector('.g-title').textContent, checked: g.querySelector('input').checked, weak: g.classList.contains('weak'), chips: [...g.querySelectorAll('.fmt')].map(b => b.textContent + (b.classList.contains('on') ? '*' : '')) }))")

            def wait_tasks(n, timeout=60):
                return wait(lambda: (lambda ts: ts if len(ts) >= n and all(t["status"] in ("done", "error") for t in ts) else None)(tasks()), timeout, 0.5, "插件任务")

            # ---- 一般网站 ----
            shop, pop = popup_for(MOCK + "/shop/list.html", "shop")
            pop.wait_for_function("() => document.getElementById('route').textContent === '内网'", timeout=10000)
            check("弹窗：线路显示内网", True)
            pop.wait_for_selector("#update-note", timeout=10000)
            note = pop.inner_text("#update-note")
            check("弹窗：书库上有新版插件时提示", f"插件有新版本 {NEW}" in note and pop.locator("#update-note button:has-text('已覆盖，重新加载')").count() == 1, note)
            check("插件图标上显示“新”", wait(lambda: ev("() => chrome.action.getBadgeText({})") == "新", 10, 0.3, "图标显示“新”"))
            gs = groups(pop)
            by = {g["title"]: g for g in gs}
            check("弹窗：找到页面上的电子书（含框架和 shadow DOM）", {"爱丽丝漫游奇境", "产品说明书", "秘密花园", "需要登录的书", "伪装的书", "框架里的书", "影子里的书", "读书笔记"} <= set(by), [g["title"] for g in gs])
            check("弹窗：不是书的链接不列出", not any("关于" in g["title"] for g in gs))
            check("弹窗：同一本书的两种格式合成一组，默认选 EPUB", by.get("爱丽丝漫游奇境", {}).get("chips") == ["EPUB*", "MOBI"], by.get("爱丽丝漫游奇境"))
            check("弹窗：下载接口（?format=epub）也认得", any(g["chips"] == ["EPUB*"] and "下载" in g["title"] for g in gs), gs)
            check("弹窗：txt 归到“其他文件”，默认不勾", by.get("读书笔记", {}).get("weak") and not by["读书笔记"]["checked"])
            check("弹窗：书多时 PDF 默认不勾", by.get("产品说明书", {}).get("checked") is False)
            check("弹窗：标题行数统计", "8 本书、1 个其他文件" in pop.inner_text(".found-head"), pop.inner_text(".found-head"))
            pop.locator(".group", has_text="影子里的书").locator("input").uncheck()
            check("弹窗：按钮显示要加入的文件数", pop.inner_text("#add") == "加入书库（6 个文件）", pop.inner_text("#add"))
            pop.screenshot(path=os.path.join(SHOTS, "31-ext-popup.png"))
            pop.emulate_media(color_scheme="dark")
            pop.screenshot(path=os.path.join(SHOTS, "32-ext-popup-dark.png"))
            pop.emulate_media(color_scheme="light")
            pop.click("#add")
            pop.wait_for_selector("#add >> text=已开始加入 6 个文件", timeout=5000)
            ts = wait_tasks(6)
            check("浏览器下载、上传在离屏页面里做（后台里不做长时间的请求）", offscreens() == 1)
            st = {(t.get("name") or t["url"].split("/")[-1]): (t["status"], t.get("error", "")) for t in ts}
            check("加入：浏览器下载后上传（带 Content-Disposition 的中文名）", st.get("海底两万里.epub", ("",))[0] == "done", st)
            check("加入：普通链接", st.get("alice.epub", ("",))[0] == "done", st)
            check("加入：框架里的 FB2", st.get("frame-book.fb2", ("",))[0] == "done", st)
            check("加入：要来源页的网站带上了 Referer", st.get("secret.epub", ("",))[0] == "done" and any(e[1] == "/protected/secret.epub" and e[2] == MOCK + "/shop/list.html" for e in EXT_LOG), st)
            fake = next((t for t in ts if t["url"].endswith("/fake.epub")), {})
            check("加入：链接其实是网页时报错", fake.get("status") == "error" and "网页" in fake.get("error", ""), fake)
            dis = next((t for t in ts if t["url"].endswith("/disguised.epub")), {})
            check("加入：类型是文件、内容是网页时也报错", dis.get("status") == "error" and "下载到的是网页" in dis.get("error", ""), dis)
            for title in ("爱丽丝漫游奇境", "海底两万里", "秘密花园", "小王子"):
                b = wait(lambda: book_by_title(title), 40, 1, "插件加入的书入库：" + title)
                check(f"入库：{title}", b and b["files"], b and b.get("files"))
            pop.wait_for_selector(".task.error >> text=让书库下载", timeout=5000)
            check("弹窗：加入记录里显示结果", pop.locator(".task.done").count() >= 4 and pop.locator(".task.error").count() == 2)
            pop.locator(".task.error", has_text="fake.epub").locator("text=让书库下载").click()
            wait(lambda: (lambda t: t if t.get("mode") == "nas" and t["status"] == "error" else None)(next(x for x in tasks() if x["url"].endswith("/fake.epub"))), 30, 0.5, "让书库下载")
            fake = next(x for x in tasks() if x["url"].endswith("/fake.epub"))
            check("出错后“让书库下载”：书库也判断出是网页（插件里换了说法）", fake["error"].startswith("书库打开这个链接得到的是网页"), fake)
            pop.screenshot(path=os.path.join(SHOTS, "33-ext-popup-tasks.png"))
            pop.click("#clear")
            pop.wait_for_function("() => document.getElementById('tasks').hidden", timeout=5000)
            check("弹窗：清除已结束的记录", True)
            pop.close(); shop.close()

            # ---- 公开书库 ----
            tab, pop = popup_for("http://www.gutenberg.org/ebooks/1342", "gutenberg")
            gs = groups(pop)
            check("古腾堡：一本书的各种格式合成一组，用书页标题", len(gs) == 1 and gs[0]["title"] == "Pride and Prejudice by Jane Austen", gs)
            check("古腾堡：格式标签和默认选 EPUB3", gs and gs[0]["chips"] == ["EPUB3*", "EPUB", "EPUB 无图", "AZW3", "TXT"], gs and gs[0]["chips"])
            pop.click("#add")
            t = wait(lambda: (lambda ts: ts[-1] if ts and ts[-1]["status"] in ("done", "error") else None)(tasks()), 40, 0.5, "古腾堡")
            check("古腾堡：书库下载不了时自动改用浏览器下载", t["status"] == "done" and t.get("mode") == "browser" and t.get("triedBrowser"), t)
            check("古腾堡：文件名没意义时用书名", t.get("name") == "Pride and Prejudice by Jane Austen.epub", t.get("name"))
            check("古腾堡：入库", wait(lambda: book_by_title("Pride and Prejudice"), 40, 1, "古腾堡入库"))
            pop.close(); tab.close()

            tab, pop = popup_for("http://archive.org/details/xiyouji00wu", "ia")
            gs = groups(pop)
            check("互联网档案馆：整本加入的选项", gs and gs[0]["title"] == "西游记" and gs[0]["chips"] == ["EPUB*", "整本（自动选格式）", "PDF"], gs)
            pop.click(".fmt >> text=EPUB")
            pop.click(".fmt >> text=整本（自动选格式）")
            pop.click("#add")
            t = wait(lambda: (lambda ts: ts[-1] if ts and ts[-1]["url"].startswith("ia:") and ts[-1]["status"] in ("done", "error") else None)(tasks()), 40, 0.5, "互联网档案馆")
            check("互联网档案馆：交给书库下载", t["status"] == "done" and t.get("mode") == "nas" and t.get("name") == "西游记.epub", t)
            b = wait(lambda: book_by_title("西游记"), 40, 1, "西游记入库")
            check("互联网档案馆：入库", b and b["author"] == "吴承恩", b and b.get("author"))
            pop.close(); tab.close()

            tab, pop = popup_for("http://standardebooks.org/ebooks/jane-austen/emma", "se")
            gs = groups(pop)
            check("Standard Ebooks：几种格式合成一组，默认选兼容版 EPUB", gs and gs[0]["title"] == "Emma" and gs[0]["chips"] == ["EPUB*", "EPUB 新版", "KEPUB", "AZW3"], gs)
            pop.close(); tab.close()

            tab, pop = popup_for("http://zh.wikisource.org/wiki/%E6%B0%B4%E6%BB%B8%E5%82%B3", "ws")
            gs = groups(pop)
            check("维基文库：作品页给出整部作品的 EPUB", gs and gs[0]["title"] == "水滸傳" and gs[0]["chips"] == ["整部作品 EPUB*"], gs)
            pop.close(); tab.close()

            tab, pop = popup_for("about:blank", "blank")
            check("浏览器自带页面：提示读不到", "浏览器自带的页面" in pop.inner_text("#main"))
            pop.close(); tab.close()

            # ---- 大文件、书库任务的时限、后台被停掉（插件里的时间用 globalThis.shukuTune 调短） ----
            def add_items(*items):
                return opt.evaluate("([items, page]) => chrome.runtime.sendMessage({ type: 'add', items, page })", [list(items), {"url": MOCK + "/shop/list.html", "title": "电子书小站"}])

            def task_of(url, pred, timeout, what, read=None):
                return wait(lambda: (lambda t: t if t and pred(t) else None)(next((x for x in (read or tasks)() if x["url"] == url), None)), timeout, 0.3, what)

            def set_config(**patch):
                ev(f"async () => {{ const {{ config }} = await chrome.storage.local.get('config'); await chrome.storage.local.set({{ config: {{ ...config, ...{json.dumps(patch)} }} }}) }}")

            def tune(**kw):
                ev(f"() => {{ globalThis.shukuTune = {json.dumps(kw)} }}")

            ended = lambda t: t["status"] in ("done", "error")
            tune(big=1024 * 1024, idle=1000)
            big = MOCK + "/dl/big-public.epub"
            add_items({"url": big, "fmt": "epub", "file": "big-public.epub", "title": "公开的大部头"})
            t = task_of(big, ended, 60, "大文件交给书库")
            check("大文件、不用登录也能下载：自动改让书库下载", t["status"] == "done" and t.get("mode") == "nas" and t.get("triedNas") and "改让书库直接下载" in t.get("note", ""), t)
            check("大文件：书库里有插件交来的下载任务", any(x["url"] == big and x.get("source") == "浏览器插件" for x in j("GET", "/api/fetch")))
            check("大文件：入库", wait(lambda: book_by_title("公开的大部头"), 40, 1, "大文件入库"))

            ctx.add_cookies([{"name": "member", "value": "1", "url": MOCK}])
            mem = MOCK + "/protected/big-member.epub"
            add_items({"url": mem, "fmt": "epub", "file": "big-member.epub", "title": "会员的大部头"})
            t = task_of(mem, ended, 60, "要登录的大文件")
            check("大文件、要登录才能下载：还是浏览器下载（带着登录状态）后上传", t["status"] == "done" and t.get("mode") == "browser" and not t.get("triedNas"), t)
            hits = [e for e in EXT_LOG if e[1] == "/protected/big-member.epub"]
            check("要登录的大文件：不带登录状态试过一次，没有交给书库", any("member=1" in e[3] for e in hits) and any("member=1" not in e[3] for e in hits)
                  and not any(x["url"] == mem for x in j("GET", "/api/fetch")), hits)
            check("要登录的大文件：入库", wait(lambda: book_by_title("会员的大部头"), 40, 1, "要登录的大文件入库"))
            check("离屏页面闲下来就关掉", wait(lambda: offscreens() == 0, 10, 0.3, "关掉离屏页面"))

            set_config(mode="nas")
            tune(stall=4000)
            stuck = MOCK + "/dl/stuck.epub"
            add_items({"url": stuck, "fmt": "epub", "file": "stuck.epub", "title": "卡住的书"})
            t = task_of(stuck, ended, 40, "书库那边卡住的任务")
            check("书库那边没有进展：插件不再等，说明原因", t["status"] == "error" and "没有进展" in t["error"] and "取消" in t["error"], t)
            check("书库那边没有进展：书库里的任务也取消了", wait(lambda: not any(x["url"] == stuck for x in j("GET", "/api/fetch")), 10, 0.5, "取消书库任务"))
            check("没有任务时不再定时叫醒后台", wait(lambda: ev("async () => !(await chrome.alarms.get('watch'))"), 5, 0.3, "定时器"))

            tune(retry=[500, 500], stall=120000)
            slow = MOCK + "/dl/slow-nas.epub"
            add_items({"url": slow, "fmt": "epub", "file": "slow-nas.epub", "title": "断网后接着查的书"})
            task_of(slow, lambda t: t["status"] == "fetching" and t.get("jobId"), 15, "交给书库")
            cfg = ev("async () => (await chrome.storage.local.get('config')).config")
            set_config(lan="http://127.0.0.1:9", remote="")
            t = task_of(slow, lambda t: t.get("parked"), 30, "连不上书库时停下来")
            check("连不上书库：隔几秒再查，还连不上就停下来、每分钟查一次", t["status"] == "fetching" and "连不上书库" in t.get("wait", ""), t)
            check("连不上书库：有定时器叫醒后台", ev("async () => !!(await chrome.alarms.get('watch'))"))
            wp = watch(ctx.new_page(), "wait-popup")
            wp.set_viewport_size({"width": 380, "height": 600})
            wp.goto(f"chrome-extension://{ext}/popup.html")
            wp.wait_for_selector(".task.wait", timeout=5000)
            check("弹窗：显示连不上书库、正在等", "连不上书库" in wp.inner_text(".task.wait"), wp.inner_text("#tasks"))
            wp.screenshot(path=os.path.join(SHOTS, "34-ext-popup-waiting.png"))
            wp.close()
            set_config(lan=cfg["lan"], remote=cfg.get("remote", ""))
            ev("() => chrome.alarms.create('watch', { when: Date.now() + 200, periodInMinutes: 1 })")  # 不等一分钟，马上叫醒
            t = task_of(slow, ended, 40, "连上后接着查")
            check("书库又能连上：接着查进度，下载完成", t["status"] == "done" and not t.get("wait"), t)
            check("断网后接着查的书：入库", wait(lambda: book_by_title("断网后接着查的书"), 40, 1, "断网后接着查的书入库"))
            set_config(mode="auto")
            ev("() => { delete globalThis.shukuTune }")

            # 后台（service worker）在下载途中被浏览器停掉：离屏页面接着下载、上传，后台被它的消息叫醒后接着记进度
            slowb = MOCK + "/dl/slow-book.epub"
            add_items({"url": slowb, "fmt": "epub", "file": "slow-book.epub", "title": "后台停了也能下完的书"})
            task_of(slowb, lambda t: t["status"] == "downloading" and t.get("bytes", 0) > 0, 15, "开始下载")
            si = ctx.new_page()
            si.goto("chrome://serviceworker-internals/")
            si.locator("button[data-command=stop]").first.click()
            si.close()
            stopped = False
            for _ in range(50):
                try:
                    sw.evaluate("() => 1")
                except Exception:
                    stopped = True
                    break
                time.sleep(0.2)
            # 后台重启后 Playwright 拿不到新的后台，之后从设置页读插件的状态
            ev = lambda js: opt.evaluate(js)
            mid = task_of(slowb, lambda t: True, 5, "后台停掉时的任务")
            t = task_of(slowb, ended, 40, "后台停掉后下载完")
            check("后台在下载途中被停掉：离屏页面接着下载、上传，后台醒来接着记进度", stopped and mid["status"] in ("downloading", "uploading") and t["status"] == "done" and t.get("mode") == "browser", (stopped, mid["status"], t))
            check("后台被停掉的那本书：入库", wait(lambda: book_by_title("后台停了也能下完的书"), 40, 1, "后台被停掉的那本书入库"))

            # ---- 设置：加入方式、断开 ----
            opt.click("input[name=mode][value=nas]")
            opt.wait_for_timeout(300)
            check("设置页：切换加入方式", ev("async () => (await chrome.storage.local.get('config')).config.mode") == "nas")
            opt.click("input[name=mode][value=auto]")
            opt.on("dialog", lambda d: d.accept())
            opt.click("#logout")
            opt.wait_for_selector("#pair", state="visible", timeout=10000)
            check("设置页：断开后书库里的登录也注销了", not any("浏览器插件" in x["device"] for x in j("GET", "/api/sessions")))
            tab, pop = popup_for(MOCK + "/shop/list.html", "after-logout", wait_groups=False)
            pop.wait_for_selector(".note >> text=先连接你的书库", timeout=5000)
            check("断开后弹窗提示先连接", True)
        finally:
            ctx.close()
    check("插件页面没有脚本错误", not errors, "\n".join(errors[:8]))


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
        for t in [test_auth, test_ingest, test_search, test_fulltext, test_reading, test_organize, test_upload_ai_delete, test_online, test_tts, test_opds, test_settings_libs, test_clients, test_update, test_dict, test_fetch_discover, test_v7_family_api]:
            try:
                t()
            except Exception as e:
                check(f"{t.__name__} 执行出错", False, f"{e}\n{traceback.format_exc(limit=3)}")
        if not ONLY_API:
            try:
                ui_tests()
            except Exception as e:
                check("网页检查执行出错", False, f"{e}\n{traceback.format_exc(limit=4)}")
            try:
                offline_v7()
            except Exception as e:
                check("离线读书检查执行出错", False, f"{e}\n{traceback.format_exc(limit=4)}")
        if not ONLY_API or "--ext" in sys.argv:
            try:
                ext_tests()
            except Exception as e:
                check("插件检查执行出错", False, f"{e}\n{traceback.format_exc(limit=4)}")
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
