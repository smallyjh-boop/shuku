# 书库本地语音服务（shuku-tts）：MeloTTS 中英混读，sherpa-onnx，只用 CPU。
# GitHub Actions 打包（android.yml 的“本地语音服务”一步）：这个文件夹没改就不重做，NAS 不用重新下载。
FROM python:3.13-slim
RUN pip install --no-cache-dir sherpa-onnx==2.0.1 lameenc==1.9.0 numpy==2.5.4 \
 && apt-get update && apt-get install -y --no-install-recommends curl bzip2 ca-certificates \
 && mkdir -p /models && curl -fsSL https://github.com/k2-fsa/sherpa-onnx/releases/download/tts-models/vits-melo-tts-zh_en.tar.bz2 | tar xj -C /models \
 && rm -f /models/vits-melo-tts-zh_en/model.int8.onnx \
 && apt-get purge -y bzip2 && apt-get autoremove -y && rm -rf /var/lib/apt/lists/*
COPY server.py /app/server.py
ARG TTS_VERSION=dev
ENV TTS_VERSION=$TTS_VERSION TTS_PORT=27210 TTS_THREADS=4 PYTHONUNBUFFERED=1
HEALTHCHECK --interval=60s --timeout=5s --start-period=120s CMD curl -fs http://127.0.0.1:${TTS_PORT}/health || exit 1
CMD ["python", "/app/server.py"]
