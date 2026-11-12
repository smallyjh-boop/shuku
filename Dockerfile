# 书库 Shuku —— NAS 电子书管理与阅读
# 一般不用自己构建：GitHub Actions 打包好的镜像发在 ghcr.io/smallyjh-boop/shuku（NAS 上的更新服务会自动换新版本）。
# 自己构建：docker build -t localhost/shuku:latest .
# 默认带 Calibre（转换 DOCX、CHM、RTF 等，镜像大约大 600MB）；不要的话加 --build-arg FULL=0。
# 自己构建的镜像：compose 里的 image 跟着改，并删掉 labels（否则更新服务会换回发布的版本）。
# 发布时 Actions 先在同样的 golang:1.24-bookworm 里编译好程序、打好手机/手表 App 和插件，
# 再用 --build-context build=... 替换下面的编译阶段，用 --build-context runtime=... 换成做好的运行环境（带本地字典）。
# 自己构建的镜像里没有各端安装包和本地字典（查字典时只能用 AI 解释）。

FROM golang:1.24-bookworm AS build
WORKDIR /src
COPY server/ ./
# 依赖已经放在 vendor 目录，构建时不需要访问 Go 模块代理
# sqlite_fts5：书内全文搜索
RUN CGO_ENABLED=1 go build -mod=vendor -tags sqlite_fts5 -trimpath -ldflags "-s -w" -o /out/shuku . \
 && mkdir -p /out/clients /out/dict

# 运行环境：格式转换工具（mupdf、7z、djvulibre，以及转换 DOCX、CHM、RTF 等用的 Calibre）。
# 发布时这一段单独做成基础镜像 ghcr.io/smallyjh-boop/shuku-base（这一段没改就一直用同一个），
# 每次升级只换下面的程序、网页和安装包，NAS 只要下载几十 MB
FROM debian:bookworm-slim AS base
LABEL org.opencontainers.image.source=https://github.com/smallyjh-boop/shuku
ARG FULL=1
ARG DEBIAN_FRONTEND=noninteractive
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates tzdata mupdf-tools p7zip-full djvulibre-bin \
 && if [ "$FULL" = "1" ]; then apt-get install -y --no-install-recommends calibre; fi \
 && rm -rf /var/lib/apt/lists/*

FROM base AS runtime
# 运行环境：基础镜像加上本地字典（中文、英文，几十 MB）。发布时单独做成镜像 ghcr.io/smallyjh-boop/shuku-runtime，
# 字典的数据和转换程序没变就一直用同一个，NAS 升级时不用重新下载字典
# （注释写在这里：base 那一段从 FROM 到下一个 FROM 之间，连注释、空行改了都会让基础镜像重做）
COPY --from=build /out/dict/ /app/dict/

FROM runtime AS ocr
# 扫描版 PDF 识别文字：tesseract 和各语言的识别模型（Debian 打包的是“快速”模型，每种一两 MB）：
# 简体、繁体、英文、日文、韩文、泰文、越南文、老挝文、高棉文、缅甸文、印尼文、马来文，法、德、西、意、葡、俄文。
# 发布时这一段单独做成 ghcr.io/smallyjh-boop/shuku-runtime:ocr-…（运行环境和这一段没变就一直用同一个），
# 放在字典后面：加它不用重新下载字典
RUN apt-get update \
 && apt-get install -y --no-install-recommends tesseract-ocr tesseract-ocr-chi-sim tesseract-ocr-chi-tra tesseract-ocr-jpn \
    tesseract-ocr-kor tesseract-ocr-tha tesseract-ocr-vie tesseract-ocr-lao tesseract-ocr-khm tesseract-ocr-mya \
    tesseract-ocr-ind tesseract-ocr-msa tesseract-ocr-fra tesseract-ocr-deu tesseract-ocr-spa tesseract-ocr-ita \
    tesseract-ocr-por tesseract-ocr-rus \
 && rm -f /usr/share/tesseract-ocr/*/tessdata/osd.traineddata \
 && rm -rf /var/lib/apt/lists/*

FROM ocr AS tts
# NAS 本地语音（听书时中文、英文不经过外网）：MeloTTS 中英混读模型和运行它的 Python 环境（sherpa-onnx，只用 CPU）。
# 书库启动后自己在后台开着它（server/tts_embed.go，只听本机 127.0.0.1:27210），程序在 tts/server.py。
# 发布时这一段单独做成 ghcr.io/smallyjh-boop/shuku-runtime:tts-…（文字识别层和这一段没变就一直用同一个），
# NAS 只在第一次多下载这一层（三百多 MB）
RUN apt-get update \
 && apt-get install -y --no-install-recommends python3 python3-venv curl bzip2 ca-certificates \
 && python3 -m venv /opt/tts/venv \
 && /opt/tts/venv/bin/pip install --no-cache-dir sherpa-onnx==2.0.1 lameenc==1.9.0 "numpy<2.4" \
 && mkdir -p /opt/tts/models && curl -fsSL https://github.com/k2-fsa/sherpa-onnx/releases/download/tts-models/vits-melo-tts-zh_en.tar.bz2 | tar xj -C /opt/tts/models \
 && rm -f /opt/tts/models/vits-melo-tts-zh_en/model.int8.onnx \
 && apt-get purge -y bzip2 && apt-get autoremove -y && rm -rf /var/lib/apt/lists/* /root/.cache

FROM tts AS desk
# Windows 桌面版（听书加速器）的运行环境：Windows 版 Python（嵌入式）和 sherpa-onnx 等。书库下载桌面版时现场加上程序
# （/app/desktop）拼成 zip（server/desktop.go），不用每一版都带一个安装包。发布时单独做成 shuku-runtime:desk-…，NAS 只下载一次
RUN mkdir -p /opt/desktop/runtime/python /tmp/w \
 && curl -fsSLo /tmp/py.zip https://www.python.org/ftp/python/3.12.10/python-3.12.10-embed-amd64.zip \
 && /opt/tts/venv/bin/pip download --no-cache-dir --platform win_amd64 --python-version 3.12 --implementation cp --only-binary=:all: \
    -d /tmp/w sherpa-onnx==2.0.1 lameenc==1.9.0 "numpy<2.4" pystray==0.19.5 "pillow<12" \
 && python3 -c "import zipfile,glob; r='/opt/desktop/runtime/python/'; zipfile.ZipFile('/tmp/py.zip').extractall(r); \
[ [z.extract(m, r+'Lib/site-packages') for m in z.namelist() if '.data/' not in m and not m.endswith('.lib') and '/include/' not in m] \
  for z in (zipfile.ZipFile(w) for w in glob.glob('/tmp/w/*.whl') if 'xlib' not in w.lower()) ]; \
open(r+'python312._pth','w').write('python312.zip\\n.\\nLib\\\\site-packages\\n..\\\\app\\nimport site\\n')" \
 && rm -rf /tmp/w /tmp/py.zip

FROM desk
# 威联通上要用宿主机网络（network_mode: host）运行，书库直接监听 SHUKU_PORT；默认 27200，避开 QTS 自己的 8080
ENV TZ=Asia/Bangkok \
    SHUKU_PORT=27200 \
    SHUKU_DATA=/data \
    SHUKU_INBOX=/inbox \
    SHUKU_WEB=/app/web \
    SHUKU_CLIENTS=/app/clients \
    SHUKU_DICT=/app/dict/dict.db \
    SHUKU_LIBRARIES="图书:/books:book,漫画:/comics:comic"
COPY --from=build /out/shuku /app/shuku
# 手机 App、手表 App、浏览器插件的安装包和版本信息（clients.json），各端从书库检查更新、下载
COPY --from=build /out/clients/ /app/clients/
COPY web/ /app/web/
COPY tts/server.py /app/tts/server.py
COPY desktop/ /app/desktop/
VOLUME ["/data"]
EXPOSE 27200
HEALTHCHECK --interval=60s --timeout=5s CMD ["/app/shuku", "-health"]
ENTRYPOINT ["/app/shuku"]
