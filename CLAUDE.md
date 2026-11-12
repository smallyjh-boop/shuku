# 书库 Shuku —— 项目约定

## 部署到用户的 NAS（威联通 TS-453D，Intel x86，Container Station）

- **容器一律用 `network_mode: host`，服务直接监听自己的端口，不写 `ports:` 映射。** 威联通上 Docker 的端口转发不通：桥接网络加端口映射的容器访问不到，也上不了网。用户已经因为这个吃过好几次亏；他的其他项目（流水账房、极速 GTD、泰好说、memory-trainer）全都是宿主机网络。
- 书库的端口是 **27200**（`SHUKU_PORT`，镜像默认值也是 27200），书库自带的本地语音是 **27210**（只听本机）。新增端口前先避开 NAS 上已占用的：8080（QTS 管理页）、443、8720 / 8721 / 8730（泰好说）、8741（泰好说的更新服务 taihaoshuo-updater，只在本机回环）、8721（memory-trainer）、8780 / 8781（极速 GTD）、8790（3D 运营管理系统）、8791（运营分析系统）、18765（3D 选品工具）、27180 / 27190（流水账房个人版 / 公司版）。Tailscale serve 的 https 端口（2026-10-21 在 NAS 上 `serve status` 看到的）：443 → 27180（流水账房个人版）、10443 → 27190（流水账房公司版）、8443 → 8720（泰好说）、9443 → 27200（书库）。**新增 serve 之前先让用户跑 `serve status`，只加没用过的端口，不要覆盖别的项目的。**
- 书放在共享文件夹 `books`（小写，路径区分大小写）：`/share/books/投递`、`/share/books/图书`、`/share/books/漫画`；数据在 `/share/Container/shuku`。
- NAS 内网地址 `192.168.31.200`，Tailscale 地址 `100.81.34.58`（MagicDNS `nasfinecycle.tailbb4459.ts.net`）。时区 `Asia/Bangkok`。
- NAS 上的 Tailscale 命令行在 `/share/CACHEDEV1_DATA/.qpkg/Tailscale/tailscale`，版本较旧，`serve` 用老写法：`tailscale serve https:9443 / http://127.0.0.1:27200`（443 是流水账房的）。书库的 https 地址是 `https://nasfinecycle.tailbb4459.ts.net:9443`；App 和手表用 `http://100.81.34.58:27200` 也行。
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
- 听书的声音按语言：`/api/tts/voices` 每个音色带 `code`（zh、en、th…），`defaults` 是每种语言的默认音色；网页 `tts.js` 的 `voiceFor` 按书的语言挑、`prefs.voices[语言]` 记住选过的。微软语音合成前 `adaptVoice` 看文字种类（`scriptLang`，汉字假名按 4 个字母算），音色读不了就换成这种语言的默认音色；整句拉丁字母遇到中文、泰文这类音色时换成书的语言（不是拉丁字母的语言就用英语）的音色。网页的书的语言看正文（`bookCode`，元数据只用来区分拉丁字母的语言），每句按 `segLang` 挑声音。
- 手机后台听书（`NarrationService`）：句子平时由网页给（`need` / `append`）；网页 15 秒没回应（熄屏后被冻结）或不在了，改由服务自己从 `/api/books/{id}/chapters?loc=&pct=` 找到读到哪一章、`/chapters/{i}` 取正文接着读（`startFeed` / `feedMore`），用的是 `common` 里和手表共用的 `BookText`、`BookPos`。预先合成 `PREFETCH`（30）句；合成失败 `synthFailed` 按 `RETRIES` 隔几秒重试（重新探测线路），一分半钟后才改用手机语音，手机没有语音引擎（`deviceBroken`）时一直等网络。第一次听书时 `askIgnoreBatteryOnce` 请求不受电池优化限制。听书进度一直由服务自己记（`notePosition`，每 30 秒、暂停停止时；关屏后网页被冻结，存不了），网页位置没变、只报阅读时长时带 `time_only`，书库和 `ProgressSync` 都不拿它改位置。
- 听书的读法（在线合成 / 本机语音）每台设备自己选，阅读设置的“听书”里就能换：网页 `Narrator` 记在 `shuku.tts` 的 `engine`（`device` 用浏览器的 speechSynthesis，本机音色记在 `deviceVoices[语言]`）；App 是原生的 `tts_engine`（`NativeNarrator.setEngine` → `setPref` + 听书服务的 `engine` 命令马上换，手机音色走 `deviceVoice` 命令，和书库音色分开）；手表是 `ListenService.VOICE_KEY`（`listen_voice`），听书页、阅读页的“读法”按钮（`ListenActivity.pickVoice`），选了手表语音就不放书库合成好的语音、也不再试书库。书库管理员设成“设备自带语音”时，网页只能用本机语音。听书条上的“在线 / 本机”按钮（`openVoiceMenu`）一点就能换读法和音色。本机语音按文字种类分段读（App、手表用 `common` 的 `MixedText.runs` + `DeviceSpeech.use`，网页用 `tts.js` 的 `mixedRuns`，两边规则一样：主要是中日韩文字时短英文不切开），设备没有那种语言的声音就跳过那段并提示，不拿英文声音硬读。App 的 `engine` 命令 1.0.0.24 起才有：网页看 `appInfo().versionCode`，旧版 App 改成停下再从当前位置重新开始。
- 自动翻页（`web/js/reader/autoturn.js`）：平时自己往后翻一页就学一次（这一页真正在看的秒数：工具栏、面板、查字典、听书、后台都不计时；按字数算“每 100 字几秒”，PDF、漫画按“每页几秒”；太快太慢的样本不要），按书的种类分开、按人分开记在本机（`shuku.autoturn.{uid}`）。开着时嫌慢自己翻会多学一点、刚翻过去就翻回来会放慢、“－ / ＋”直接调（`adj`）。看书时只在底部状态栏显示一小行（`r-auto-mini`），带按钮的那条只在工具栏叫出来时显示（不挡字）。App 里自动翻页开着时 `keepAwake(true)` 让屏幕一直亮；浏览器用 Wake Lock。引擎要给 `pageChars()`（flow 有，PDF、漫画没有时按页算）。
- App 阅读时屏幕常亮只在最近 10 分钟碰过屏幕时（`KEEP_AWAKE_MS`，自动翻页除外）；App 退到后台且没在听书时 `pauseTimers()`（网页 JS 的定时器全停，回到前台 `resumeTimers`）；内存小或只有 32 位的设备（`Diag.lowEnd`）网页进程在后台时不再保持“重要”。`Diag` 把回到前台、网页打不开、网页进程没了、主线程卡住 5 秒以上（带调用栈）记在 `files/diag.log`，“设置 → 手机 App → 诊断记录”里看：起因是一台 armv7 老平板开着书过夜、再打开显示“连接书库”后卡死，现场拿不到。连接页（`setup.html`）用 `connectAsync` 在后台连，不再让网页线程等网络；显示“连不上书库”时网络一恢复自动重新打开。
- 听书提前合成（1.0.0.28）：设备把后面的句子（和正式合成一样的文字、音色、语速）交给 `POST /api/tts/prefetch`，书库每人一条队列（新的换掉旧的），2 个工作线程在后台合成进缓存，`GET /api/tts/prefetch` 报进度；`synthesizeWith` 里同一句只合成一次（`ttsInflight`）。网页阅读器读了 20 秒、换到新的一节时从当前页往后交（`prefetchFromView`），听书时 `prefetchAhead` / `prefetchPacked`，App 熄屏自己取章节时 `serverPrefetch`。
- AI 助手（`server/airead.go`，阅读器侧栏“AI 助手”，`web/js/reader/aipanel.js`）：前情提要 `/ai/recap`、问这本书 `/ai/ask`、整理划线 `/ai/notes`，都用 `aiChat`。默认只用读到的地方以前的内容（阅读器带 `loc`/`pct`，没带用存的进度）。结果缓存在 `/data/cache/ai`（每章概要大家共用）。每日目标是 `shuku.app` 的 `goal`，`/api/stats/today` 多了 `streak`、`avg14`（读完预测用）。
- NAS 本地语音（1.0.0.30 起带在书库镜像里）：`Dockerfile` 的 `tts` 段（ocr 层上加 Python 环境 + sherpa-onnx + MeloTTS 中英模型，放在 `/opt/tts`），workflow 单独做成 `shuku-runtime:tts-<哈希>`（ocr 层和这一段没变就不重做，NAS 只下载一次）；程序是 `tts/server.py`（OpenAI 兼容的 `POST /v1/audio/speech`，`GET /health` 报速度，正在听的句子优先于 `X-Priority: prefetch`）。书库 `server/tts_embed.go` 启动后自己开它（只听 127.0.0.1:27210，3 线程、低一档优先级，退出了隔一会儿重开；设置成“不用”或引擎不是微软就关；端口上已经有别的本地语音就不开；`SHUKU_TTS_EMBED=0` 不开）。`server/tts_local.go` 自动发现（`SHUKU_LOCAL_TTS`，默认 `http://127.0.0.1:27210`），`tts.local` 不是 off 时中文、英文（`scriptLang`）能用本地：本地速度（`/health` 的 rtf，按真实合成随时算、闲时每 20 分钟再测）≤ 1 时先用本地、不行用微软，> 1 时先用微软、不行用本地兜底；本地出错一分钟内不用。设备可以指定来源：音色名前加 `nas:` / `ms:`（`tts.js` 的 `srcVoice`，`prefs.src`，阅读页“自动 / 微软 / NAS / 本机”），旧版 App 原样转过来也认。打包试运行检查本地语音能合成。Kokoro 太慢（RTF 1.4）、Piper 中文出不了声音、MMS 泰语效果差，都不用；单独的 shuku-tts 容器（1.0.0.29）因为 ghcr 私有镜像在 NAS 命令行拉不下来，改成带在书库里。
- 安卓签名：`android/signing/shuku.jks`（手机和手表必须同一个签名、同一个包名 `app.shuku`）。
- 手表 App 是原生界面（不是 WebView），自己能听书：`ListenService` 按句调 `POST /api/tts` 合成、手表自己播放（蓝牙耳机，或用户同意后用扬声器；书库语音不行时用手表自带语音），进度和阅读页一样换算成 `sec:` / `p:` 位置传回书库。首页另外还能遥控手机上的听书（Wearable 消息 `/shuku/tts/…`）。没有 Android SDK 时用 `android/tools/check.sh` 做类型检查（它按 res/drawable 补一个 R，引用不存在的图标会报错）。手表按钮统一用 `Ui.action`（圆形图标加一行小字，宽度按屏幕算，一排三个；卡片里 `inCard`），图标是 `res/drawable/ic_a_*.xml`。

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

## 旧浏览器、墨水屏（1.0.0.9 起）

- `index.html` 不直接写 `<script type="module">`：`web/js/boot.js`（只用 ES5）按功能检测选网页——新浏览器加载 `/js/main.js`；旧浏览器（墨水屏阅读器、旧安卓自带的网页组件，Chrome 117 以前）加载 `tools/legacy` 构建的 `web/legacy/`（不进 git；打包时 Node 22 `npm ci && node build.mjs`，镜像里跟着网页一起）。打不开时 boot.js 显示浏览器版本和出错原因。
- 新写的网页代码要能被兼容构建处理（`build.mjs` 会检查）：不用正则后行断言、命名分组、`\p{…}`，不用顶层 await、`import.meta`；新的浏览器功能要么有退路，要么在 `tools/legacy/src/dom.js`（或 `shims.js`）里补上。改完跑 `node tools/legacy/build.mjs` 和 `tools/legacy/test-old-chrome.mjs`（Chrome 64：npm 包 `chrome-aws-lambda@64.0.3264` 里的 `bin/chromium.br`，brotli 解开；`--samples` 自己放测试书，`--strip` 再去掉 Chrome 58～63 没有的功能）。打包试运行时也跑这个检查。
- 页面不能比屏幕宽（墨案 inkPad X 是 936x1248 网页像素，用的是电脑那种带侧栏的排版）：网格里的 `fr` 一律写成 `minmax(0, 1fr)`（光写 `1fr` 会被宽的内容撑开，旧的网页组件里整页变宽、能左右拖，固定定位的阅读器跟着排到屏幕外面一片空白）；`html` 上有 `overflow-x: hidden`，viewport 有 `minimum-scale=1`。`test-old-chrome.mjs` 默认按 936x1248 检查，再看 600x800、702x936、1248x936（`--app` 模拟 App 的原生桥，打包时用这个）。
- foliate 的 `paginator.js` 样式里有嵌套和容器查询（竖屏单栏靠它），旧浏览器不认识，`build.mjs` 的 PATCHES 展开成普通写法、竖屏由脚本标 `data-portrait`；更新 foliate-js 时 PATCHES 对不上会报错，照着改。
- `/api/assets` 默认不列 `/legacy/`，`?legacy=1` 才列（App 在网页组件旧时才缓存这一套）。
- 墨水屏、旧浏览器在线时一律按需取书（`server/zipentry.go`：`/zip` 文件列表、`/zip/{name}` 单个文件；`?split=` 大章节由 `zipsplit.go` 拆段、`?part=j` 取一段；`?max=宽x高` 大图由 `zipimage.go` 缩小；`?inline=1` 列表里带上 container.xml、OPF、目录），断网时下载整本、在设备上拆（`web/js/reader/split.js`）。两边拆法一样：前面的子元素换成占位的空元素，每个元素的位置不变，CFI 和不拆的设备通用；改拆法时两边一起改，跑 `go test -run TestPlanSplit` 和 test-old-chrome（`--samples` 里有整本一个文件的书）。按需取书时书内搜索走 `/api/files/{fid}/search`（全文索引）。改了阅读器、按需取书以后用 `tools/legacy/perf-open.mjs`（Chrome 64，CPU 慢 20 倍模拟墨水屏）拿大书量打开速度，和改之前比。
- 墨水屏、旧浏览器在线时 PDF 由书库画（`server/pdfpages.go`：`/pdf/info` 页数、每页大小、目录（`mutool run` 脚本），`/pdf/page/{n}?w=&g=1` 画成 16 级灰度 PNG，`/pdf/text/{n}` 每行文字和位置（`mutool draw -F stext`）；本机没有 mutool 时用 poppler）。网页 `pdf.js` 的 `this.remote` 分支用 `img.pgimg` 代替 canvas、文字层和扫描版识别的一样铺（`layLines`）。按页翻时 `.pages` 里只放当前页和前后一页（`attachPages`），几千个页框都放进去墨水屏上光算样式就要好几秒。本机测 mutool：`git clone --branch 1.21.1` mupdf（和 Debian bookworm 一样的版本）加子模块，`make HAVE_X11=no HAVE_GLUT=no HAVE_CURL=no tools`，放进 PATH 再起 e2e 服务器。test-old-chrome 的 PDF 样书有一行没嵌入字体的中文（STSong-Light），打包试运行时检查 NAS 上的 mutool 画得出、取得到字。
- 翻页手势（`paginator.js` 的书库改动）：滑过屏幕宽的 6%（至少 30 像素）就翻一页；没有翻页动画（墨水屏）时页面不跟手指走。点击区域在 `reader.js` 的 `tap()`：正中间一块叫出工具栏，左边三分之一往前，其余往后。
- foliate-js 有书库自己的改动（`epub.js`、`paginator.js` 里注释写着“书库”）：更新 foliate-js 时保留，build.mjs 的 PATCHES 对不上会报错。
- 界面颜色（1.0.0.27 起）：`shuku.app` 的 `appearance` 是 system / light / eyecare / dark / night，规则在 `ui.js` 的 `uiTone`（boot.js 绘制前用 ES5 照抄一遍，改的时候两边一起改）：手动切换的夜间（`shuku.nightOverride`，10 小时）> 自动夜读（和阅读器共用 `shuku.reader` 的 `nightMode` / `nightStart` / `nightEnd`）> 选的颜色；墨水屏只有 light / dark。`html[data-theme]` 还是 light / dark（深色样式照旧），`data-tone` 是 eyecare / night，颜色变量在 app.css 的 `:root[data-tone=…]`。阅读器夜间主题默认 `oled`（夜读）。蓝光过滤是 body 上的 `#bluefilter`（`blueFilter` 0～3，夜读时 `nightFilter` 默认用 2），休息提醒是 `rest`（分钟，阅读器 `restTick`）。
- 墨水屏模式是 `html.eink`（App 认出墨水屏：`EInk.kt`；设置 → 外观）。新写样式时想着墨水屏：别用灰字、阴影、半透明、动画，`html.eink` 下要清楚；墨水屏阅读一律按页翻。字号一律写成 `calc(Npx * var(--ui-scale, 1))`（界面字号设置靠它）。
