# 书库 Shuku —— 项目约定

## 部署到用户的 NAS（威联通 TS-453D，Intel x86，Container Station）

- **容器一律用 `network_mode: host`，服务直接监听自己的端口，不写 `ports:` 映射。** 威联通上 Docker 的端口转发不通：桥接网络加端口映射的容器访问不到，也上不了网。用户已经因为这个吃过好几次亏；他的其他项目（流水账房、极速 GTD、泰好说、memory-trainer）全都是宿主机网络。
- 书库的端口是 **27200**（`SHUKU_PORT`，镜像默认值也是 27200）。新增端口前先避开 NAS 上已占用的：8080（QTS 管理页）、443、8720 / 8721 / 8730（泰好说）、8741（泰好说的更新服务 taihaoshuo-updater，只在本机回环）、8721（memory-trainer）、8780 / 8781（极速 GTD）、8790（3D 运营管理系统）、8791（运营分析系统）、18765（3D 选品工具）、27180 / 27190（流水账房个人版 / 公司版），Tailscale serve 的 https 8443（泰好说）。
- 书放在共享文件夹 `books`（小写，路径区分大小写）：`/share/books/投递`、`/share/books/图书`、`/share/books/漫画`；数据在 `/share/Container/shuku`。
- NAS 内网地址 `192.168.31.200`，Tailscale 地址 `100.81.34.58`（MagicDNS `nasfinecycle.tailbb4459.ts.net`）。时区 `Asia/Bangkok`。
- NAS 上的 Tailscale 命令行在 `/share/CACHEDEV1_DATA/.qpkg/Tailscale/tailscale`，版本较旧，`serve` 用老写法：`tailscale serve https:443 / http://127.0.0.1:27200`。
- 用户在 Container Station 网页上操作（“映像 → 导入”“应用程序 → 创建 → 粘贴 YAML”），能在网页上做的不要让他 SSH。
- 用户改过的本地文件（比如电脑上 `Documents\Codex\shuku` 里的 `docker-compose.yml`）不要覆盖。
- 部署相关的改动，发布前先对照上面几条检查一遍，再对照用户其他项目的 compose（`smallyjh-boop/liushui`、`smallyjh-boop/GTD` 的 `deploy/nas`、`smallyjh-boop/taihaoshuo` 的 `deploy`）。

## 打包（GitHub Actions）

- `.github/workflows/android.yml` 只能手动运行（workflow_dispatch），推送代码不触发；用户在意 Actions 分钟数，能一次打完就一次打完，不跑多余的任务。
- 每次都全部打包（各端安装包要放进服务器镜像），只打 x86（NAS 是 Intel）。服务器在 `golang:1.24-bookworm` 里编译，再用根目录 `Dockerfile` 打包：`--build-context build=…` 换掉编译阶段，`--build-context runtime=docker-image://ghcr.io/smallyjh-boop/shuku-runtime:<哈希>` 换掉运行环境。试运行用宿主机网络，检查接口、网页、`/api/clients`、`/dl/…` 下载、Calibre 转换和本地字典。
- 安卓 App 是 WebView：阅读器每一章在 iframe 里（blob: 地址），`shouldOverrideUrlLoading` 对框架里的跳转必须放行（`!request.isForMainFrame` 直接返回 false），否则 EPUB、TXT 打不开。e2e 里的“模拟安卓 App”是桌面 Chromium 加假的原生桥，测不到这类 WebView 行为。
- 运行环境（Debian + mupdf/7z/djvulibre + Calibre）是 `Dockerfile` 里的 `base` 段，单独做成基础镜像，标签是这一段文字的哈希（awk 取 `FROM … AS base` 到下一个 `FROM` 之间，**注释、空行也算**）：这一段没改就不重做，NAS 升级只下载几十 MB。改 `base` 段（或者勾“重新制作基础镜像”）会让 NAS 多下载几百 MB，没必要别动，说明性注释写到别的段里。
- 本地字典在 `runtime` 段（base + `/app/dict/dict.db`），做成 `ghcr.io/smallyjh-boop/shuku-runtime`，标签按 base 标签、runtime 段文字、字典数据地址（固定版本，在 workflow 的 env 里）、`server/dict_build.go` 算。字典由 `shuku -build-dict` 在 Actions 里生成：国语辞典（g0v/moedict-data，CC BY-ND 3.0 TW，原文照存、显示时转简体）、CC-CEDICT（大陆拼音、繁简对照）、ECDICT（英文）。
- Releases 不再放服务器镜像的 tar.gz（带 Calibre 太大，NAS 直接从 ghcr.io 拉）。
- 安卓签名：`android/signing/shuku.jks`（手机和手表必须同一个签名、同一个包名 `app.shuku`）。
- 手表 App 是原生界面（不是 WebView），自己能听书：`ListenService` 按句调 `POST /api/tts` 合成、手表自己播放（蓝牙耳机，或用户同意后用扬声器；书库语音不行时用手表自带语音），进度和阅读页一样换算成 `sec:` / `p:` 位置传回书库。首页另外还能遥控手机上的听书（Wearable 消息 `/shuku/tts/…`）。没有 Android SDK 时用 `android/tools/check.sh` 做类型检查。

## 在线升级（已做好）

- 版本号 = 填的版本号（或 `android/gradle.properties`）+ `.` + run_number，比如 `1.0.0.12`；服务器 `-X main.version=`，手机 versionCode = run_number，手表 versionCode = 100000 + run_number，插件 manifest 的 version 也是它。
- 服务器：镜像推到 `ghcr.io/smallyjh-boop/shuku:latest` 和 `:v版本号`（ghcr.io 上只留 3 个版本），compose 带 `com.centurylinklabs.watchtower.enable=true`，NAS 上泰好说的 `taihaoshuo-updater`（Watchtower，`--interval 60`）一分钟内自动换上。网页“检查更新”调它的 HTTP 接口：`SHUKU_UPDATE_URL=http://127.0.0.1:8741`、`SHUKU_UPDATE_TOKEN`（和泰好说 `deploy/docker-compose.yml` 里的 `WATCHTOWER_HTTP_API_TOKEN` 一样）。这个更新服务是泰好说装的，书库的 compose 里不要再建一个。
- 手机、手表 App 和插件：安装包和 `clients.json` 打进镜像的 `/app/clients`，各端读 `/api/clients`（不用登录）、从 `/dl/{文件名}` 下载。旧版各端都依赖这两个接口，改的时候要兼容。
- NAS 上已经登录过 ghcr.io（和泰好说、memory-trainer 同一个只读令牌），镜像是私有的也能拉。
