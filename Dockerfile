# 书库 Shuku —— NAS 电子书管理与阅读
# 普通版：docker build -t shuku .
# 完整版（带 Calibre，可转换 DOCX/CHM/RTF 等，镜像大约大 600MB）：docker build --build-arg FULL=1 -t shuku:full .

FROM golang:1.24-bookworm AS build
WORKDIR /src
COPY server/ ./
# 依赖已经放在 vendor 目录，构建时不需要访问 Go 模块代理
# sqlite_fts5：书内全文搜索
RUN CGO_ENABLED=1 go build -mod=vendor -tags sqlite_fts5 -trimpath -ldflags "-s -w" -o /out/shuku .

FROM debian:bookworm-slim
ARG FULL=0
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates tzdata mupdf-tools p7zip-full djvulibre-bin \
 && if [ "$FULL" = "1" ]; then apt-get install -y --no-install-recommends calibre; fi \
 && rm -rf /var/lib/apt/lists/*
ENV TZ=Asia/Shanghai \
    SHUKU_PORT=8080 \
    SHUKU_DATA=/data \
    SHUKU_INBOX=/inbox \
    SHUKU_WEB=/app/web \
    SHUKU_LIBRARIES="图书:/books:book,漫画:/comics:comic"
COPY --from=build /out/shuku /app/shuku
COPY web/ /app/web/
VOLUME ["/data"]
EXPOSE 8080
HEALTHCHECK --interval=60s --timeout=5s CMD ["/app/shuku", "-health"]
ENTRYPOINT ["/app/shuku"]
