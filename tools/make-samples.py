#!/usr/bin/env python3
"""生成一批文件名很乱的测试书，用来检验入库、改名、整理和合并提醒。需要 Pillow 和 reportlab。
用法：python3 make-samples.py <目录>   会生成 <目录>/inbox、books、comics"""
import io, os, shutil, sys, zipfile

from PIL import Image, ImageDraw

root = sys.argv[1] if len(sys.argv) > 1 else "."
os.chdir(root)
for d in ["inbox/漫画", "books", "comics"]:
    os.makedirs(d, exist_ok=True)


def cover(color):
    im = Image.new("RGB", (600, 900), color)
    d = ImageDraw.Draw(im)
    d.rectangle([60, 320, 540, 520], fill=(235, 215, 170))
    b = io.BytesIO()
    im.save(b, "JPEG")
    return b.getvalue()


def epub(path, title, author, chapters, series=None, idx=None, color=(40, 70, 120)):
    z = zipfile.ZipFile(path, "w")
    z.writestr(zipfile.ZipInfo("mimetype"), "application/epub+zip")
    z.writestr("META-INF/container.xml", '<?xml version="1.0"?><container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles></container>')
    z.writestr("OEBPS/cover.jpg", cover(color))
    man = spine = ""
    for i, (t, body) in enumerate(chapters):
        z.writestr(f"OEBPS/c{i}.xhtml", f'<?xml version="1.0" encoding="utf-8"?><html xmlns="http://www.w3.org/1999/xhtml"><head><title>{t}</title></head><body><h2>{t}</h2>' + "".join(f"<p>{p}</p>" for p in body) + "</body></html>")
        man += f'<item id="c{i}" href="c{i}.xhtml" media-type="application/xhtml+xml"/>'
        spine += f'<itemref idref="c{i}"/>'
    nav = '<?xml version="1.0" encoding="utf-8"?><html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops"><body><nav epub:type="toc"><ol>' + "".join(f'<li><a href="c{i}.xhtml">{t}</a></li>' for i, (t, _) in enumerate(chapters)) + "</ol></nav></body></html>"
    z.writestr("OEBPS/nav.xhtml", nav)
    ser = f'<meta name="calibre:series" content="{series}"/><meta name="calibre:series_index" content="{idx}"/>' if series else ""
    z.writestr("OEBPS/content.opf", f'<?xml version="1.0"?><package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="id"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:identifier id="id">{title}</dc:identifier><dc:title>{title}</dc:title><dc:creator>{author}</dc:creator><dc:language>zh</dc:language><dc:date>2008-05-01</dc:date><dc:subject>科幻</dc:subject>{ser}<meta name="cover" content="cover"/></metadata><manifest><item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/><item id="cover" href="cover.jpg" media-type="image/jpeg" properties="cover-image"/>{man}</manifest><spine>{spine}</spine></package>')
    z.close()


para = ["那是一个晴朗的早晨，城市还没有完全醒来。街道上只有零星的行人，空气里弥漫着刚出炉面包的香气。" * 3] * 14
epub("inbox/[www.zxcs.me]三体2：黑暗森林(精校版).epub", "三体Ⅱ·黑暗森林", "刘慈欣", [("上部 面壁者", para), ("中部 咒语", para), ("下部 黑暗森林", para)], "三体", 2)
epub("inbox/sanTi1.epub", "三体", "刘 慈欣", [("第一章 科学边界", para), ("第二章 台球", para)], "三体", 1, (90, 30, 30))

# GBK 编码的 TXT，带章节
lines = ["《活着》作者：余华", ""]
for n, t in zip("一二三四五", ["开始", "福贵", "家珍", "有庆", "凤霞"]):
    lines += [f"第{n}章 {t}", "　　" + "我比现在年轻十岁的时候，获得了一个游手好闲的职业，去乡间收集民间歌谣。" * 8, "　　" + "那一年的整个夏天，我如同一只乱飞的麻雀，游荡在知了和阳光充斥的村舍田野。" * 6]
open("inbox/《活着》作者：余华【完结】.txt", "wb").write("\r\n".join(lines).encode("gbk"))

# 漫画：两卷 CBZ，第 5 页是跨页大图
for vol in (1, 2):
    z = zipfile.ZipFile(f"inbox/漫画/进击的巨人 Vol.{vol:02d}.cbz", "w")
    for p in range(10):
        w, h = (1600, 1200) if p == 4 else (800, 1200)
        im = Image.new("RGB", (w, h), (248, 248, 246))
        d = ImageDraw.Draw(im)
        d.rectangle([40, 40, w - 40, h - 40], outline=(20, 20, 20), width=6)
        d.line([40, h // 2, w - 40, h // 2], fill=(20, 20, 20), width=4)
        d.text((w // 2 - 40, h // 2 - 30), f"Vol{vol} P{p + 1}", fill=(0, 0, 0))
        b = io.BytesIO()
        im.save(b, "JPEG")
        z.writestr(f"{p + 1:03d}.jpg", b.getvalue())
    z.close()

# PDF：标题是 Word 自动生成的，作者在属性里
try:
    from reportlab.pdfgen import canvas

    c = canvas.Canvas("inbox/Microsoft Word - 深度学习入门_final(1).pdf")
    c.setTitle("深度学习入门：基于Python的理论与实现")
    c.setAuthor("斋藤康毅")
    for i in range(6):
        c.setFont("Helvetica", 22)
        c.drawString(90, 720, f"Deep Learning - page {i + 1}")
        for j in range(18):
            c.setFont("Helvetica", 11)
            c.drawString(90, 680 - j * 22, "Neural networks learn representations from data, layer by layer." * 1)
        c.showPage()
    c.save()
except ImportError:
    pass

# 不是书的压缩包、重复文件
zz = zipfile.ZipFile("inbox/资料.zip", "w")
zz.writestr("readme.md", "hello")
zz.writestr("a.py", "x")
zz.close()
shutil.copy("inbox/sanTi1.epub", "inbox/sanTi1 - 副本.epub")

# 已有书库：作者文件夹有两种写法、一个空文件夹
for d in ["books/小说/刘慈欣", "books/小说/刘 慈欣", "books/空文件夹/子"]:
    os.makedirs(d, exist_ok=True)
epub("books/小说/刘 慈欣/球状闪电.epub", "球状闪电", "刘慈欣", [("第一章 雷电", para)], color=(30, 90, 70))
epub("books/小说/刘慈欣/流浪地球.epub", "流浪地球", "Liu Cixin", [("刹车时代", para)], color=(20, 40, 60))
print("samples ready")
