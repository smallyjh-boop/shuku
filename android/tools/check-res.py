#!/usr/bin/env python3
"""检查安卓资源：XML 格式是否正确、清单和资源里引用的 @string/@color/@drawable/... 是否都存在、
Kotlin 代码里用到的 R.xxx.yyy 是否存在。没有 Android SDK 时代替 aapt 做一遍最常见的检查。
用法：python3 tools/check-res.py app wear
"""
import os, re, sys
import xml.etree.ElementTree as ET

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
ANDROID_NS = "{http://schemas.android.com/apk/res/android}"


def check_module(mod):
    errors = []
    main = os.path.join(HERE, mod, "src", "main")
    res = os.path.join(main, "res")
    defined = set()
    xml_files = [os.path.join(main, "AndroidManifest.xml")]
    for d in sorted(os.listdir(res)) if os.path.isdir(res) else []:
        kind = d.split("-")[0]
        for f in sorted(os.listdir(os.path.join(res, d))):
            p = os.path.join(res, d, f)
            if f.endswith(".xml"):
                xml_files.append(p)
            if kind == "values":
                try:
                    for el in ET.parse(p).getroot():
                        t = {"string": "string", "color": "color", "style": "style", "dimen": "dimen", "bool": "bool", "integer": "integer", "array": "array", "string-array": "array", "plurals": "plurals"}.get(el.tag)
                        if t and el.get("name"):
                            defined.add(f"{t}/{el.get('name')}")
                except ET.ParseError as e:
                    errors.append(f"{p}: XML 格式错误 {e}")
            else:
                defined.add(f"{kind}/{os.path.splitext(f)[0]}")
    for p in xml_files:
        try:
            text = open(p, encoding="utf-8").read()
            ET.fromstring(text.encode("utf-8"))
        except ET.ParseError as e:
            errors.append(f"{p}: XML 格式错误 {e}")
            continue
        except FileNotFoundError:
            errors.append(f"{p}: 缺少文件")
            continue
        for m in re.finditer(r'"@(?!android:)(?:\+)?([a-z]+)/([A-Za-z0-9_.]+)"', text):
            kind, name = m.group(1), m.group(2)
            if kind == "id":
                continue
            ref = f"{kind}/{name.replace('.', '_') if kind != 'style' else name}"
            if ref not in defined:
                errors.append(f"{os.path.relpath(p, HERE)}: 引用了不存在的资源 @{kind}/{name}")
        # 样式的 parent 也要存在（系统样式除外）
        for m in re.finditer(r'parent="(?!@?android:)@?(?:style/)?([A-Za-z0-9_.]+)"', text):
            if f"style/{m.group(1)}" not in defined:
                errors.append(f"{os.path.relpath(p, HERE)}: 样式的 parent 不存在 {m.group(1)}")
    # 清单：Activity、Service 等类是否存在
    man = os.path.join(main, "AndroidManifest.xml")
    if os.path.exists(man):
        root = ET.parse(man).getroot()
        src = [os.path.join(main, "java")]
        kotlin = ""
        for s in src + [os.path.join(HERE, "common", "src")]:
            for dp, _, fs in os.walk(s):
                for f in fs:
                    if f.endswith(".kt"):
                        kotlin += open(os.path.join(dp, f), encoding="utf-8").read()
        for tag in ("application", "activity", "service", "receiver", "provider"):
            for el in root.iter(tag):
                name = el.get(ANDROID_NS + "name")
                if not name:
                    continue
                cls = name.split(".")[-1]
                if not re.search(r"\bclass\s+" + re.escape(cls) + r"\b", kotlin):
                    errors.append(f"{mod}/AndroidManifest.xml: 找不到类 {name}")
        # 代码里的 R.xxx.yyy
        for m in re.finditer(r"(?<![\w.])R\.([a-z]+)\.([A-Za-z0-9_]+)", kotlin):
            if f"{m.group(1)}/{m.group(2)}" not in defined:
                errors.append(f"{mod}: 代码引用了不存在的资源 R.{m.group(1)}.{m.group(2)}")
    return errors


status = 0
for mod in sys.argv[1:] or ["app", "wear"]:
    errs = check_module(mod)
    for e in errs:
        print("  " + e)
    print(f"{mod} 资源：" + ("通过" if not errs else f"{len(errs)} 个问题"))
    status |= bool(errs)
sys.exit(status)
