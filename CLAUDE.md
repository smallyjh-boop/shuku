# 书库 Shuku —— 项目约定

## 部署到用户的 NAS（威联通 TS-453D，Intel x86，Container Station）

- **容器一律用 `network_mode: host`，服务直接监听自己的端口，不写 `ports:` 映射。** 威联通上 Docker 的端口转发不通：桥接网络加端口映射的容器访问不到，也上不了网。用户已经因为这个吃过好几次亏；他的其他项目（流水账房、极速 GTD、泰好说、memory-trainer）全都是宿主机网络。
- 书库的端口是 **27200**（`SHUKU_PORT`，镜像默认值也是 27200）。新增端口前先避开 NAS 上已占用的：8080（QTS 管理页）、443、8720 / 8721 / 8730 / 8741（泰好说）、8721（memory-trainer）、8780 / 8781（极速 GTD）、8790（3D 运营管理系统）、8791（运营分析系统）、18765（3D 选品工具）、27180 / 27190（流水账房个人版 / 公司版），Tailscale serve 的 https 8443（泰好说）。
- NAS 内网地址 `192.168.31.200`，Tailscale 地址 `100.81.34.58`（MagicDNS `nasfinecycle.tailbb4459.ts.net`）。时区 `Asia/Bangkok`。
- NAS 上的 Tailscale 命令行在 `/share/CACHEDEV1_DATA/.qpkg/Tailscale/tailscale`，版本较旧，`serve` 用老写法：`tailscale serve https:443 / http://127.0.0.1:27200`。
- 用户在 Container Station 网页上操作（“映像 → 导入”“应用程序 → 创建 → 粘贴 YAML”），能在网页上做的不要让他 SSH。
- 用户改过的本地文件（比如电脑上 `Documents\Codex\shuku` 里的 `docker-compose.yml`）不要覆盖。
- 部署相关的改动，发布前先对照上面几条检查一遍，再对照用户其他项目的 compose（`smallyjh-boop/liushui`、`smallyjh-boop/GTD` 的 `deploy/nas`、`smallyjh-boop/taihaoshuo` 的 `deploy`）。

## 打包（GitHub Actions）

- `.github/workflows/android.yml` 只能手动运行（workflow_dispatch），推送代码不触发；用户在意 Actions 分钟数，能一次打完就一次打完，不跑多余的任务。
- 服务器镜像：在 `golang:1.24-bookworm` 里交叉编译 x86 和 ARM，再用根目录 `Dockerfile` 的运行阶段（`--build-context build=…`）打包，试运行用宿主机网络。
- 安卓签名：`android/signing/shuku.jks`（手机和手表必须同一个签名、同一个包名 `app.shuku`）。

## 待办

- 各端自动升级（见 README 的“待办”）：等用户检查完 bug 一起做。NAS 上已经有泰好说的 Watchtower（`taihaoshuo-updater`，按 `com.centurylinklabs.watchtower.enable=true` 标签更新）和 ghcr.io 的只读登录，服务器可以照泰好说 / memory-trainer 的做法发到 ghcr.io 自动更新。
