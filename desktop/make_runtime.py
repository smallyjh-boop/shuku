#!/usr/bin/env python3
"""做书库 Windows 版的运行环境：Windows 版嵌入式 Python + 要用的库（sherpa-onnx、pywebview、pystray…）。

在 Linux 上也能做（Dockerfile 的 desk 段），在 Windows 上也能做（.github/workflows/desktop-check.yml 试装）：
只下载 win_amd64 的现成安装包解开，不编译。

    python3 make_runtime.py 输出目录 [--pip pip的路径]

输出目录里是 python/（解压即用）和 runtime.txt（这一套运行环境的编号：库的版本变了编号就变，
桌面版据此判断升级时只换程序还是要重新安装）。
"""
import glob
import hashlib
import io
import os
import shutil
import subprocess
import sys
import tarfile
import tempfile
import urllib.request
import zipfile

PY = '3.12.10'
PY_URL = f'https://www.python.org/ftp/python/{PY}/python-{PY}-embed-amd64.zip'
# 带依赖一起下载的
WHEELS = ['sherpa-onnx==2.0.1', 'lameenc==1.9.0', 'numpy==2.3.5', 'pystray==0.19.5', 'pillow==11.3.0', 'pythonnet==3.0.5']
# pywebview 的依赖 proxy_tools 只有源码包：pywebview 不带依赖下载，依赖单独列
NODEPS = ['pywebview==5.4', 'bottle==0.13.4', 'typing_extensions==4.16.0']
SDIST = ['proxy_tools==0.1.0']
SKIP = ('python_xlib', 'xlib')  # pystray 在 Linux 上才用


def main():
    try:
        sys.stdout.reconfigure(encoding='utf-8')  # Windows 控制台默认不是 UTF-8
    except Exception:
        pass
    out = os.path.abspath(sys.argv[1])
    pip = [sys.executable, '-m', 'pip']
    if '--pip' in sys.argv:
        pip = [sys.argv[sys.argv.index('--pip') + 1]]
    py = os.path.join(out, 'python')
    site = os.path.join(py, 'Lib', 'site-packages')
    if os.path.exists(py):
        shutil.rmtree(py)
    os.makedirs(site)
    with urllib.request.urlopen(PY_URL, timeout=300) as r:
        zipfile.ZipFile(io.BytesIO(r.read())).extractall(py)
    tmp = tempfile.mkdtemp()
    common = ['download', '--no-cache-dir', '--disable-pip-version-check', '-d', tmp]
    win = ['--platform', 'win_amd64', '--python-version', '3.12', '--implementation', 'cp', '--only-binary=:all:']
    subprocess.check_call(pip + common + win + WHEELS)
    subprocess.check_call(pip + common + win + ['--no-deps'] + NODEPS)
    subprocess.check_call(pip + common + ['--no-deps', '--no-binary=:all:'] + SDIST)
    names = []
    for w in sorted(glob.glob(os.path.join(tmp, '*.whl'))):
        base = os.path.basename(w)
        if base.lower().startswith(SKIP):
            continue
        names.append(base)
        with zipfile.ZipFile(w) as z:
            for m in z.namelist():
                # .data/ 里是装到 Scripts 的重复 DLL，include/、.lib 是编译用的
                if '.data/' in m or m.endswith('.lib') or '/include/' in m:
                    continue
                z.extract(m, site)
    for s in sorted(glob.glob(os.path.join(tmp, '*.tar.gz'))):
        names.append(os.path.basename(s))
        with tarfile.open(s) as t:
            for m in t.getmembers():
                parts = m.name.split('/')
                # 源码包里的 包名/包名/*.py
                if m.isfile() and len(parts) >= 3 and parts[1] == parts[0].rsplit('-', 1)[0] and m.name.endswith('.py'):
                    dest = os.path.join(site, *parts[1:])
                    os.makedirs(os.path.dirname(dest), exist_ok=True)
                    with t.extractfile(m) as src, open(dest, 'wb') as f:
                        f.write(src.read())
    shutil.rmtree(tmp, ignore_errors=True)
    # 嵌入式 Python 只看 ._pth 里列的路径：加上库和程序（..\app）
    with open(os.path.join(py, 'python312._pth'), 'w', newline='\r\n') as f:
        f.write('python312.zip\n.\nLib\\site-packages\n..\\app\nimport site\n')
    rid = hashlib.sha256(('\n'.join([PY] + names)).encode()).hexdigest()[:12]
    with open(os.path.join(out, 'runtime.txt'), 'w') as f:
        f.write(rid)
    print('运行环境', rid, '：', ', '.join(names))


if __name__ == '__main__':
    main()
