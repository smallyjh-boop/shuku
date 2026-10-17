# 书库 Shuku

装在 NAS 上的电子书和漫画书库：把书拖进去，自动识别书名作者、按规范改名归类；电脑和 iPhone 用网页读，安卓手机和 Wear OS 手表用客户端，进度在各设备之间同步。在家走内网，出门自动切到 Tailscale。浏览器插件能把任何网站上的电子书一键加入书库。

当前进度：

- 第一阶段（NAS 服务端 + 网页端）、第二阶段（联网补全、书内搜索、听书、阅读统计、OPDS）：已完成。
- 第三阶段（安卓 App、Wear OS 手表 App、浏览器插件、“发现”页）：已完成。所有安装文件（NAS 服务器镜像、安卓 App、手表 App、浏览器插件）都在仓库的 Releases 里，安装见[部署到威联通](#部署到威联通)。

## 现在能做什么

- **入库**：网页里拖拽文件或整个文件夹，或者直接丢进 NAS 的投递文件夹。自动去掉网站水印、“精校版”等后缀，读取书里的内嵌信息，可选用 AI 恢复正式书名。识别把握大的直接按命名规则归档，把握不大的进“待确认”。
- **整理建议**：相似文件夹（“刘慈欣”和“刘 慈欣”）、同一作者的不同写法（含拼音/英文名）、散落的系列、同一本书登记多次、重复文件、没按规则命名的书、空文件夹。每条都能预览、一键执行、整批撤销。所有移动都有记录，**不会删除任何文件**（删除书会移到书库的 `_回收站`）。
- **阅读**：
  - EPUB、MOBI、AZW3、FB2：字体、字号、行距、段距、页边距、首行缩进、两端对齐；左右翻页或上下滚动，宽屏自动双栏；目录、书签、书内搜索。
  - TXT：自动识别 GBK/GB18030/Big5/UTF-8 编码，按“第X章”智能分章。
  - PDF：按需分段下载（大文件不用等），连续滚动或单页翻页，缩放，目录。
  - 漫画（CBZ/CBR/CB7/CBT/ZIP/RAR）：单页、双页（自动识别跨页大图）、条漫；日漫从右到左；内网看原图，Tailscale 下按屏幕压缩。
  - DOCX、CHM、RTF 等冷门格式：完整版镜像里用 Calibre 转成 EPUB 再读。DJVU 转 PDF 再读。
- **夜间模式**：六套阅读主题（白、纸、绿、灰、纯黑、暖夜），夜间模式可以跟随系统或按时间自动切换，单独选择夜间主题；软件亮度（可以低于系统最低亮度）、暖色滤镜；PDF 夜间反色。界面在页面绘制前就确定深浅色，夜里打开不会闪白。
- **搜索**：书名、作者、系列、标签，支持全拼、首字母（输入 `st` 找到《三体》）、繁简互搜。
- **听书**：阅读器里点耳机图标（或按 `R`）开始朗读，高亮当前句子、自动翻页；语速、音色随时换；睡眠定时（15 / 30 / 60 分钟或读完本章）；手机锁屏也能暂停、切句。声音来源三选一：
  - 微软自然语音（默认，免费，和 Edge 浏览器“大声朗读”同一套：晓晓、晓伊、云希、云健、云扬、粤语、台湾腔等）。NAS 需要能访问 `speech.platform.bing.com`。
  - 任何 OpenAI 兼容的语音接口：OpenAI、硅基流动，或 NAS 上的 Kokoro、openai-edge-tts 等本地服务。
  - 设备自带语音：前两种用不了时自动切换，断网也能读。
  合成过的段落缓存在 NAS 上（默认最多 400MB），换设备接着听不用重新合成。
- **书内搜索**：给正文建索引，书架搜索框输入两个字以上，就列出哪本书的哪一章提到过，点一下直接跳到原文。支持 EPUB、TXT、MOBI、AZW3、FB2、PDF（有文字层的）；繁体简体互搜。
- **联网补全**：从豆瓣读书、Open Library、Google Books、Bangumi（漫画）查封面、简介、年份、ISBN、出版社。入库时自动核对书名作者，对得上就直接归档；详情页“联网查找”列出候选，挑一个填进去；“设置 → 联网补全”可以一键补全已有的书。只补空着的信息，不覆盖你改过的内容。NAS 连不上外网时自动暂停，不拖慢入库。
- **阅读统计**：今天、本周读了多久，连续阅读天数，今年读完几本，近 30 天每天的时长，读得最多的书。多设备一起算。
- **OPDS**：KOReader、静读天下、Librera、Panels、Yomu 等阅读器添加 `http://NAS的IP:8090/opds`（用户名随意，密码是书库密码），就能浏览、搜索、下载书库里的书。
- **内外网自动切换**：填好内网地址和 Tailscale 地址后，打开时同时探测，内网通走内网，不通走 Tailscale；切换网络、请求失败时自动重新选择。网页、安卓 App、手表、浏览器插件都是这样。
- **发现**：在“发现”页浏览、搜索公开书库，一键加入书库：维基文库（中文古籍、近代作品，整部作品生成 EPUB）、古腾堡计划、互联网档案馆（可以直接下载的书）、Standard Ebooks。还能添加任何 OPDS 书源（Calibre-Web、COPS、Kavita 等，支持用户名密码）。
- **从链接添加**：书架页“添加”里粘贴下载链接，书库在 NAS 上直接下载入库（最多同时 3 个，单个最大 4GB）。链接打开的是网页时会说明原因。需要登录才能下载的网站，用浏览器插件。
- **设备**：“设置 → 手机、手表和浏览器插件”里生成配对二维码和配对码（10 分钟内有效，只能用一次），给安卓 App、手表、浏览器插件登录用；所有登录过的设备都列在这里，可以单独移除。
- **回收站**：删除的书移到每个书库目录下的 `_回收站`，“设置”里能看占用空间、一键清空。

## 部署到威联通

所有安装文件都在 GitHub 仓库的 **Releases** 里（仓库页面右边栏“Releases”，或者 `https://github.com/smallyjh-boop/shuku/releases`，要先登录 GitHub）。最新的一个里有：

| 文件 | 装在哪 |
| --- | --- |
| `shuku-server-amd64.tar.gz` | Intel / AMD 处理器的威联通（大多数 x86 型号） |
| `shuku-server-arm64.tar.gz` | ARM 处理器的威联通（比如 TS-x33、TS-x28A 这类） |
| `shuku-compose.yml` | 在 Container Station 里创建应用程序时粘贴的内容 |
| `shuku-android.apk` | 安卓手机 |
| `shuku-wear.apk` | Wear OS 手表 |
| `shuku-extension.zip` | 电脑上的 Chrome / Edge |

不知道 NAS 是哪种处理器：控制台 → 系统 → 系统状态 → 系统信息，看“CPU”一栏；Intel、AMD 选 amd64，ARM、Annapurna Labs、Realtek 选 arm64（32 位 ARM 的老型号这两个镜像都用不了）。

### 1. 准备文件夹

1. 在 App Center 安装 **Container Station**（装好后会自动建一个 `Container` 共享文件夹）。
2. 控制台 → 权限 → 共享文件夹 →“创建”一个共享文件夹，比如叫 `Books`。**一定要先建成共享文件夹**，否则数据会写进系统分区。
3. 在 File Station 里，`Books` 下面建三个文件夹：`投递`、`图书`、`漫画`；`Container` 下面建一个 `shuku`。

| NAS 上的文件夹 | 用途 |
| --- | --- |
| `/share/Books/投递` | 投递目录，丢进来的书自动入库 |
| `/share/Books/图书` | 整理好的电子书 |
| `/share/Books/漫画` | 整理好的漫画 |
| `/share/Container/shuku` | 数据库、封面、缓存 |

已经有书的话，直接把现有的书库文件夹挂载成 `/books`，启动后会原地登记，不会移动；之后可以在“整理”里一键按规范整理（可撤销）。

### 2. 导入镜像、创建应用程序

1. Container Station →**映像**→**导入**，选下载的 `shuku-server-amd64.tar.gz`（或 arm64 那个），可以直接从电脑选。导入后能看到 `localhost/shuku`。注意要在“映像”里导入，不是“容器”。
2. Container Station →**应用程序**→**创建**（旧版 Container Station 是“创建”→“创建应用程序”），应用程序名称填 `shuku`，把 `shuku-compose.yml` 的全部内容粘贴进去：
   - 共享文件夹不叫 `Books` 的，改 `volumes` 里冒号**左边**的路径；
   - `TZ=Asia/Bangkok` 是时区，在中国改成 `Asia/Shanghai`；
   - 点“验证 YAML”，再点“创建”。
3. 浏览器打开 `http://NAS的IP:8090`，设置访问密码，就能用了。

端口用 8090 是因为威联通自己的管理页面占用了 8080。8090 也被占用的话，把 `"8090:8080"` 左边的数字换一个。

**升级**：Releases 里有新版本时，下载新的镜像文件，在“映像”里导入（会覆盖 `localhost/shuku:latest`），再到“应用程序”里删除 shuku、用同样的内容重新创建一次（或 SSH 执行 `docker compose up -d`）。数据都在 `/share/Container/shuku` 和书库文件夹里，删除应用程序不会删掉它们。

**自己从源码构建**（一般用不到）：把整个仓库复制到 NAS，SSH 进去执行 `docker build -t localhost/shuku:latest .`，再按上面第 2 步创建应用程序。需要转换 DOCX、CHM、RTF 等冷门格式时，构建时加 `--build-arg FULL=1`（带 Calibre，镜像会大约 600MB）。

### 3. Tailscale（出门访问）

1. 在威联通 App Center 安装 **Tailscale**，打开后登录你的账号、点“Connect”。NAS 会得到一个 `100.x.x.x` 地址和 MagicDNS 名称（例如 `nas.tail1234.ts.net`）。
2. 手机、电脑也装上 Tailscale 并登录同一个账号。这时在外面就能用 `http://100.x.x.x:8090` 打开书库了。
3. 建议再给书库开 HTTPS（iPhone 只有 https 才能把书库“添加到主屏幕”当 App 用）：
   1. 打开 Tailscale 管理后台的 DNS 页面（`https://login.tailscale.com/admin/dns`），打开 MagicDNS 和 HTTPS Certificates。
   2. 控制台 → 网络和文件服务 → Telnet / SSH，打开 SSH。电脑上打开 PowerShell，`ssh admin@NAS的IP` 登录（出现菜单的话按 `Q` 再按 `Y` 进入命令行），执行：
      ```sh
      TS=$(getcfg SHARE_DEF defVolMP -f /etc/config/def_share.info)/.qpkg/Tailscale/tailscale
      $TS serve --bg --https=443 http://127.0.0.1:8090
      ```
   3. 之后用 `https://nas.tail1234.ts.net`（换成你的 MagicDNS 名称）访问。
4. 在书库“设置 → 网络”里填：
   - 内网地址：`http://192.168.x.x:8090`
   - Tailscale 地址：`https://nas.tail1234.ts.net`（没开 HTTPS 就填 `http://100.x.x.x:8090`）
   点“检测两个地址”确认都能连上。

**说明**：浏览器不允许 https 页面去请求 http 地址。所以 iPhone 上用 https 的 Tailscale 地址打开时，会一直走 Tailscale——在家时 Tailscale 会自动走局域网直连，速度不受影响。电脑用 http 内网地址打开时，两条线路之间可以自动切换。安卓 App、手表和浏览器插件不受这个限制，都能自动切换。

### 4. AI 识别（可选，强烈建议）

“设置 → 识别与整理”里选服务商、填 API Key，点“用示例测试”看效果。支持任何 OpenAI 兼容接口：DeepSeek、通义千问、Kimi、智谱、OpenAI，或 NAS 上的 Ollama。只会发送文件名、内嵌信息和开头几百字，不上传整本书。不配置时也能用，只是乱名文件更多会进“待确认”。

## 安卓 App

用 WebView 直接打开 NAS 上的书库网页，界面和网页端一样、跟着书库一起更新；手机自己负责网络、离线、后台听书这些网页做不到的事。

**安装**：在仓库的 Releases 里下载 `shuku-android.apk`，在手机上点开安装（第一次要允许“安装未知应用”）。Android 8.0 及以上。

**连接**：

1. 电脑打开书库网页 →“设置 → 手机、手表和浏览器插件”→“显示配对二维码”。
2. 用手机相机扫二维码，会直接打开书库 App 并连好；或者打开 App 填书库地址和配对码。也可以改用访问密码登录。
3. 内网地址和 Tailscale 地址填一个就行，连上后 App 会自动在两者之间切换。

**功能**：

- **离线**：书的详情里点“下载到手机”，没网时书架只显示下载过的书，照样能读、能记进度，联网后自动同步进度。“设置 → 手机 App”可以打开“自动下载在读的书”（只在连着内网时下载）。界面文件也缓存在手机上，断网打开不会白屏。
- **后台听书**：锁屏、切到别的 App 也会继续朗读；通知栏、锁屏、蓝牙耳机按键都能暂停和切句；拔耳机自动暂停；来电时暂停。声音可以选书库的微软自然语音，或者手机自带的语音（不联网也能用）。Wear OS 手表上会自动出现播放控制。
- **阅读**：音量键翻页（听书时音量键还是调音量）、阅读时屏幕常亮、状态栏颜色跟着阅读主题，返回键先关面板再退出阅读器。
- **加入书库**：在文件管理器、浏览器下载、微信等 App 里把电子书“分享”给书库，或者用书库“打开”，就会上传到书库自动入库。分享的是下载链接（比如在手机浏览器里长按链接 → 分享）时，书库会在 NAS 上直接下载。
- **手表**：“设置 → 手机 App → 连接手表”把连接信息发给手表。

需要的权限：网络、通知（显示听书控制和上传进度）、前台服务（后台听书）。

## Wear OS 手表

**安装**：手表 App 没上架应用商店，要用电脑的 adb 装（Wear OS 3 及以上）。adb 在 Google 的 [SDK Platform-Tools](https://developer.android.com/tools/releases/platform-tools) 里，下载 Windows 版解压就能用：

1. 手表上：设置 → 系统 → 关于 → 连点“版本号”7 次打开开发者选项；开发者选项里打开“ADB 调试”和“无线调试”。
2. 电脑和手表连同一个 Wi-Fi。在无线调试里点“配对新设备”，电脑上执行 `adb pair 手表IP:配对端口`，输入配对码；再执行 `adb connect 手表IP:端口`。
3. `adb install shuku-wear.apk`

手表 App 和手机 App 的包名、签名必须一样（同一次打包出来的就是一样的），手表才能和手机通信。

**连接**：手机上打开书库 App →“设置 → 连接手表”；或者在手表上点“从手机获取”。手机不在身边时，也可以在手表上“手动输入地址”，填书库地址和配对码。

**功能**：

- 首页显示手机上正在朗读的书，可以暂停、继续、上一句、下一句、停止。
- “在读”列表，点开就在手表上读：按章节显示，表冠滚动，可以调字号、跳目录；读到哪里同步回书库，手机和网页接着读。
- “找书”：搜索书名、作者（可以用手表的语音输入），或者看最近加入的书。
- 打开过的那一章和下一章存在手表上，路上没网也能接着读；没网时记下的进度，联网后补传（期间在别处读过的话保留那边的位置）。
- 漫画、扫描版 PDF 没有文字，手表上不能读。

手表通过手机的蓝牙或自己的 Wi-Fi 上网。在外面用 Tailscale 时，手表经手机转发的流量能不能走 Tailscale 取决于手机系统，连不上时只能在家里用手表读（控制手机听书不受影响）。

## 浏览器插件（Chrome / Edge）

在任何网站上，点一下工具栏里的书库图标，就列出这个页面上所有的电子书链接（EPUB、MOBI、AZW3、FB2、PDF、DJVU、漫画包等，包括页面里的框架），同一本书的几种格式合成一组、默认选最合适的格式，勾选后一次加入书库。也可以在下载链接上点右键 →“添加到书库”。

**安装**：解压打包出来的 `shuku-extension.zip`（或者直接用源码里的 `extension` 文件夹）→ Chrome 打开 `chrome://extensions`（Edge 打开 `edge://extensions`）→ 打开“开发者模式”→“加载已解压的扩展程序”→ 选这个文件夹。

**连接**：装好后会自动打开插件设置，填书库地址和配对码（网页“设置 → 手机、手表和浏览器插件 → 显示配对二维码”）。插件和手机一样自动在内网和 Tailscale 之间切换。

**加入方式**（插件设置里选）：

- **自动**（默认）：古腾堡计划、Standard Ebooks、互联网档案馆、维基文库这些公开书库交给书库直接下载；其他网站用浏览器下载后传到书库，能带上你在那个网站的登录状态和来源页。书库下载失败时自动改用浏览器再试一次。
- **都用浏览器下载后上传**：适合 NAS 上不了外网的情况，单个文件最大 1GB。
- **都让书库下载**：不占电脑的网络，浏览器关了也会继续。

在古腾堡计划、Standard Ebooks、互联网档案馆、维基文库的书页上，插件会把各种格式合成一本，互联网档案馆还能“整本加入”（书库自动挑 EPUB 或 PDF），维基文库会生成整部作品的 EPUB。下载到的其实是网页（比如要先登录）时会说明原因，可以换一种方式重试。

插件只在你打开弹窗或使用右键菜单时读取当前页面上的链接，下载的文件只发到你自己的书库。请只添加你有权下载的内容。

## 打包（GitHub Actions）

NAS 服务器镜像、安卓 App、手表 App 和浏览器插件都用 GitHub Actions 打包，**只在手动运行时才打包**，不会因为推送代码自动运行：

1. GitHub 仓库页面 → Actions →“打包 App 和插件”→ Run workflow。“打包什么”选全部、只打包 App 和插件、或者只打包 NAS 服务器；可以填版本号（只用数字和点，比如 `1.0.1`），留空就用 `android/gradle.properties` 里的。
2. 全部打包大约 8～10 分钟，结果在仓库的 Releases 里（文件见上面“部署到威联通”的表格），另有校验值 `SHA256SUMS.txt`。手机登录 GitHub 打开 Releases 就能直接下载 APK。

为了省 Actions 的分钟数和存储额度：整个打包只有一个任务；手机和手表在同一次 Gradle 里并行打包；服务器的 x86 和 ARM 两个版本在 Debian 里同时交叉编译，不用模拟器编译；不建缓存、不上传 Artifacts（Release 附件不占 Actions 存储），超过 30 分钟自动停止。只改了一边时，只打包那一边更省。服务器镜像打包后会先试运行，接口和网页都能打开才发布。每次运行的内部版本号自动加一，新的 APK 可以直接覆盖安装。

签名：默认用仓库里的 `android/signing/shuku.jks`（密码 `shuku-app`），自己用没问题。如果要公开分发，请换成自己的签名：在仓库 Settings → Secrets and variables → Actions 添加 `SHUKU_KEYSTORE_BASE64`（签名文件 `base64 -w0` 后的内容）、`SHUKU_KEYSTORE_PASSWORD`、`SHUKU_KEY_ALIAS`、`SHUKU_KEY_PASSWORD`。换签名后手机和手表都要卸载重装。

也可以在自己电脑上用 Android Studio 打开 `android` 文件夹，或者执行 `cd android && ./gradlew :app:assembleRelease :wear:assembleRelease`（需要 JDK 17 和 Android SDK 35）。

## 命名规则

默认：

- 图书：`{分类}/{作者}/{书名}{ (年份)}/{书名} - {作者}` → `小说/刘慈欣/球状闪电 (2005)/球状闪电 - 刘慈欣.epub`
- 系列：`{分类}/{作者}/{系列}/{序号:02} {书名}` → `小说/刘慈欣/三体/02 三体Ⅱ·黑暗森林.epub`
- 漫画：`{系列}/{系列} 第{序号:03}卷` → `进击的巨人/进击的巨人 第003卷.cbz`

可用：`{分类} {作者} {书名} {原名} {系列} {序号:02} {年份} {译者}`。花括号里可以带前后缀，比如 `{ (年份)}`，没有年份时整段省略。每个书库可以单独设置，在“设置 → 书库目录”里改，会实时显示示例。

## 键盘快捷键（阅读器）

`←` `→` / 空格 翻页 · `T` 目录 · `B` 书签 · `N` 日间/夜间 · `R` 听书 · `F` 全屏 · `Esc` 返回 · 书架页按 `/` 搜索

## 目录结构

```
server/        Go 服务端（API、入库、识别、整理、格式转换、配对、链接下载、公开书库）
  vendor/      依赖（构建时不需要联网下载 Go 模块）
  qr/          生成配对二维码
web/           网页端（原生 JavaScript，不需要打包）
  js/reader/   阅读器：flow.js（EPUB 等）、pdf.js、comic.js、tts.js（听书）
  js/native.js 在安卓 App 里运行时和原生代码之间的桥
  vendor/      foliate-js（电子书渲染）、pdf.js 4.10 legacy（兼容旧版 Safari）
android/       安卓 App（app/）和 Wear OS 手表 App（wear/），共用 common/ 里的网络代码
  tools/       不装 Android SDK 也能做的代码检查（check.sh）
extension/     Chrome / Edge 浏览器插件
tools/         测试工具：生成乱名测试书、端到端检查（e2e.py）
.github/       打包服务器镜像、App 和插件的 GitHub Actions（只能手动运行）
Dockerfile
docker-compose.yml
```

本地开发：

```sh
cd server && go build -mod=vendor -tags sqlite_fts5 -o shuku . && go test -mod=vendor -tags sqlite_fts5 ./...
SHUKU_DATA=./data SHUKU_INBOX=./inbox SHUKU_WEB=../web SHUKU_LIBRARIES="图书:$PWD/books:book,漫画:$PWD/comics:comic" ./shuku
```

`tools/run-test.sh` 会生成一批文件名很乱的测试书（GBK 的 TXT、带内嵌信息的 EPUB、Word 导出的 PDF、漫画、重复文件、写法不同的作者文件夹），启动服务器并自动入库。

`python3 tools/e2e.py` 把所有功能自动走一遍：启动服务器和模拟的联网服务（豆瓣、Open Library、语音、AI、OPDS、维基文库、互联网档案馆），从接口、网页（电脑、手机尺寸、深色、模拟安卓 App 的原生桥）、浏览器插件（无头 Chromium 里装上插件）三方面检查，截图在 `/tmp/shuku-e2e/shots`。`--api` 只查接口，`--api --ext` 只查接口和插件。需要 Go、Python 的 requests / Pillow / reportlab / playwright。

`android/tools/check.sh` 不需要 Android SDK：用 Kotlin 编译器加 `android.jar` 检查手机和手表两个 App 的代码能否编译，再检查清单和资源引用。

## 关于微软自然语音

微软的朗读接口需要一个每 5 分钟变化的校验值（`Sec-MS-GEC`），书库按开源项目 edge-tts 的算法生成（第二版格式），并带上和 Edge 浏览器一样的版本号。微软改了接口导致试听失败时：

- 先在“设置 → 听书”里试听，看出错信息；可以用环境变量 `SHUKU_EDGE_VERSION` 换成新的 Edge 版本号试试。
- 备用办法：在 NAS 上用 Docker 跑一个 [openai-edge-tts](https://github.com/travisvn/openai-edge-tts)（它会跟进微软的变化），书库“设置 → 听书”的声音来源选“自定义语音接口（OpenAI 兼容）”，地址填 `http://NAS的IP:5050/v1`，API Key 填 openai-edge-tts 里设置的 `API_KEY`，音色填 `zh-CN-XiaoxiaoNeural` 这样的名字。声音还是同一套微软自然语音。

## 还没在真实环境验证的部分

开发环境连不上外网，也没有安卓手机和手表，下面这些只用本地模拟测过协议和逻辑，部署后请实际试一下：

- 微软自然语音（`设置 → 听书 → 试听`）
- 豆瓣、Open Library、Google Books、Bangumi 联网查找（详情页“联网查找”）
- AI 识别（`设置 → 识别与整理 → 用示例测试`）
- “发现”页连真实的维基文库、古腾堡计划、互联网档案馆、Standard Ebooks
- 服务器镜像在 GitHub Actions 上构建并试运行过，但还没在威联通上实际导入运行
- 安卓 App 和手表 App：已在 GitHub Actions 上打包成功（两个 APK 同一个签名、包名都是 `app.shuku`），网页和原生之间的接口用模拟的原生桥测过；但还没在真机上运行过（离线、后台听书、手表通信、表冠滚动等）
- 浏览器插件在真实网站上的表现（在无头 Chromium 里用模拟网站测过扫描、配对、下载上传、来源页、各种出错情况）

## 待办

- **各端自动升级**（先记下，检查完 bug 后一起做）：NAS 服务器、安卓 App、Wear OS 手表 App、浏览器插件都能发现新版本并升级。要定下来的问题：
  - 新版本从哪里取：仓库是私有的，直接查 GitHub Releases 需要令牌；也可以只让 NAS 去查，再由 NAS 把安装包转给手机、手表和插件。
  - 安卓 App、手表 App：下载新的 APK 后调用系统安装（签名一致才能覆盖安装；手表上可能需要由手机推送）。
  - 浏览器插件：现在是“加载已解压的扩展程序”，Chrome 不会自动更新，需要提示下载新版并重新加载，或者改成别的分发方式。
  - NAS 服务器：在网页里提示有新版本；镜像替换要 Container Station 操作，或者提供一键更新的脚本。

