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
- 编译前在同一个 golang 容器里跑 `go vet` 和 `go test`（-tags sqlite_fts5 -trimpath，和编译共用缓存），没通过不打包。试运行另外检查 `/api/settings` 的 tools 全为 true，并做升级测试：先 `docker pull` 上一版 `:latest`（必须在新镜像占用 latest 标签之前），用它建库、入库、记进度笔记，再用新镜像在同一个数据目录启动，检查书、进度、笔记、读完状态、旧登录都在。
- CC-CEDICT 下载失败时运行环境用 `-nocedict` 标签推送，正式标签留给下次重做。
- 安卓签名：`android/signing/shuku.jks`（手机和手表必须同一个签名、同一个包名 `app.shuku`）。
- 手表 App 是原生界面（不是 WebView），自己能听书：`ListenService` 按句调 `POST /api/tts` 合成、手表自己播放（蓝牙耳机，或用户同意后用扬声器；书库语音不行时用手表自带语音），进度和阅读页一样换算成 `sec:` / `p:` 位置传回书库。首页另外还能遥控手机上的听书（Wearable 消息 `/shuku/tts/…`）。没有 Android SDK 时用 `android/tools/check.sh` 做类型检查。

## 在线升级（已做好）

- 版本号 = 填的版本号（或 `android/gradle.properties`）+ `.` + run_number，比如 `1.0.0.12`；服务器 `-X main.version=`，手机 versionCode = run_number，手表 versionCode = 100000 + run_number，插件 manifest 的 version 也是它。
- 服务器：镜像推到 `ghcr.io/smallyjh-boop/shuku:latest` 和 `:v版本号`（ghcr.io 上留最近 10 个版本，出问题时 compose 里改成 `:v旧版本号` 退回），compose 带 `com.centurylinklabs.watchtower.enable=true`，NAS 上泰好说的 `taihaoshuo-updater`（Watchtower，`--interval 60`）一分钟内自动换上。网页“检查更新”调它的 HTTP 接口：`SHUKU_UPDATE_URL=http://127.0.0.1:8741`、`SHUKU_UPDATE_TOKEN`（和泰好说 `deploy/docker-compose.yml` 里的 `WATCHTOWER_HTTP_API_TOKEN` 一样）。这个更新服务是泰好说装的，书库的 compose 里不要再建一个。
- 手机、手表 App 和插件：安装包和 `clients.json` 打进镜像的 `/app/clients`，各端读 `/api/clients`（不用登录）、从 `/dl/{文件名}` 下载。旧版各端都依赖这两个接口，改的时候要兼容。
- NAS 上已经登录过 ghcr.io（和泰好说、memory-trainer 同一个只读令牌），镜像是私有的也能拉。

## 数据库、账号（1.0.0.7 起）

- 数据库升级只写在 `server/migrate.go` 的 `migrations`（版本号存在 settings 的 `schema_version`，一个版本一个事务，只忽略 duplicate column / already exists，其他错误停止启动）。升级前自动备份到 `/data/backup/shuku-日期-upgrade-vN.db`。不要在 openDB 里零散地 ALTER。改了表结构要在 `server/testdata/` 的旧库结构上测升级（`TestMigrateFrom1006`）。
- 全文索引在单独的 `/data/fulltext.db`（每个连接 ATTACH 成 `fts`，表名不带前缀时自动找到），能重建、不进备份。
- 家人账号：`users` 表，`sessions.user_id`；progress、bookmarks（书签和划线笔记，kind 区分）、reading_log、user_books（读完状态、评分、想读、按书设置）都按 user_id 分开。以前的数据归 id=1 的管理员。新接口用 `me(r)` 取当前用户，查书带 `visibleSQL(u)`（同时排除回收站里的书、限定书库），管理功能的路由用 `requireAdmin`。
- 删书是软删除（`books.deleted_at`，文件进 `_回收站`），30 天后 purgeLoop 彻底删除；扫描时找不到的文件标 `files.missing_since`，不马上删。写新查询时记得排除 `deleted_at>0` 的书。
- 恢复备份：放到 `/data/restore.db`，进程正常退出（退出码 3），靠 compose 的 `restart: unless-stopped` 拉起来，启动时换上。
- 网页有内容安全策略（`server/api.go` 的 pageCSP，只允许本站脚本文件）：`index.html` 不能写内联脚本（启动前要做的事在 `web/js/boot.js`），不用 eval / new Function / 内联事件属性。书的章节在 blob: iframe 里会继承这个策略，书里夹带的脚本不运行。

## 旧浏览器、墨水屏（1.0.0.8 起）

- `index.html` 不直接写 `<script type="module">`：`web/js/boot.js`（只用 ES5）按功能检测选网页——新浏览器加载 `/js/main.js`；旧浏览器（墨水屏阅读器、旧安卓自带的网页组件，Chrome 117 以前）加载 `tools/legacy` 构建的 `web/legacy/`（不进 git；打包时 Node 22 `npm ci && node build.mjs`，镜像里跟着网页一起）。打不开时 boot.js 显示浏览器版本和出错原因。
- 新写的网页代码要能被兼容构建处理（`build.mjs` 会检查）：不用正则后行断言、命名分组、`\p{…}`，不用顶层 await、`import.meta`；新的浏览器功能要么有退路，要么在 `tools/legacy/src/dom.js`（或 `shims.js`）里补上。改完跑 `node tools/legacy/build.mjs` 和 `tools/legacy/test-old-chrome.mjs`（Chrome 64：npm 包 `chrome-aws-lambda@64.0.3264` 里的 `bin/chromium.br`，brotli 解开；`--samples` 自己放测试书，`--strip` 再去掉 Chrome 58～63 没有的功能）。打包试运行时也跑这个检查。
- `/api/assets` 默认不列 `/legacy/`，`?legacy=1` 才列（App 在网页组件旧时才缓存这一套）。
- 墨水屏模式是 `html.eink`（App 认出墨水屏：`EInk.kt`；设置 → 外观）。新写样式时想着墨水屏：别用灰字、阴影、半透明、动画，`html.eink` 下要清楚；墨水屏阅读一律按页翻。字号一律写成 `calc(Npx * var(--ui-scale, 1))`（界面字号设置靠它）。
