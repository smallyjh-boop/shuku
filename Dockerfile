# 书库 Shuku —— NAS 电子书管理与阅读
# 一般不用自己构建：GitHub Actions 打包好的镜像发在 ghcr.io/smallyjh-boop/shuku（NAS 上的更新服务会自动换新版本），
# Releases 里也有一份 shuku-server-amd64.tar.gz，可以在 Container Station 导入。
# 自己构建普通版：docker build -t localhost/shuku:latest .
# 完整版（带 Calibre，可转换 DOCX/CHM/RTF 等，镜像大约大 600MB）：docker build --build-arg FULL=1 -t localhost/shuku:latest .
# 自己构建的镜像：compose 里的 image 跟着改，并删掉 labels（否则更新服务会换回发布的版本）。
# 发布时 Actions 先在同样的 golang:1.24-bookworm 里编译好程序、打好手机/手表 App 和插件，
# 再用 --build-context build=... 替换下面的编译阶段。自己构建的镜像里没有各端安装包。

FROM golang:1.24-bookworm AS build
WORKDIR /src
COPY server/ ./
# 依赖已经放在 vendor 目录，构建时不需要访问 Go 模块代理
# sqlite_fts5：书内全文搜索
RUN CGO_ENABLED=1 go build -mod=vendor -tags sqlite_fts5 -trimpath -ldflags "-s -w" -o /out/shuku . \
 && mkdir -p /out/clients

FROM debian:bookworm-slim
ARG FULL=0
LABEL org.opencontainers.image.source=https://github.com/smallyjh-boop/shuku
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates tzdata mupdf-tools p7zip-full djvulibre-bin \
 && if [ "$FULL" = "1" ]; then apt-get install -y --no-install-recommends calibre; fi \
 && rm -rf /var/lib/apt/lists/*
# 威联通上要用宿主机网络（network_mode: host）运行，书库直接监听 SHUKU_PORT；默认 27200，避开 QTS 自己的 8080
ENV TZ=Asia/Bangkok \
    SHUKU_PORT=27200 \
    SHUKU_DATA=/data \
    SHUKU_INBOX=/inbox \
    SHUKU_WEB=/app/web \
    SHUKU_CLIENTS=/app/clients \
    SHUKU_LIBRARIES="图书:/books:book,漫画:/comics:comic"
COPY --from=build /out/shuku /app/shuku
# 手机 App、手表 App、浏览器插件的安装包和版本信息（clients.json），各端从书库检查更新、下载
COPY --from=build /out/clients/ /app/clients/
COPY web/ /app/web/
VOLUME ["/data"]
EXPOSE 27200
HEALTHCHECK --interval=60s --timeout=5s CMD ["/app/shuku", "-health"]
ENTRYPOINT ["/app/shuku"]
