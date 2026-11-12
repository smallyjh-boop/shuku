#!/bin/sh
# 书库 NAS 本地语音服务（shuku-tts）一键安装 / 更新。SSH 登录 NAS 后运行：
#   curl -fsSL https://raw.githubusercontent.com/smallyjh-boop/shuku/main/deploy/tts/install.sh | sh
#
# 做的事：找到 Container Station 的 docker → 检查端口 27210 → 下载镜像 → 用宿主机网络启动（重启 NAS 自动起来，
# 带自动升级标签，和书库一样由 taihaoshuo-updater 自动换新版本）→ 等它启动好，显示在这台 NAS 上的实际速度。
# 书库会自动发现它（中文、英文先用本地语音，不行用微软），不用改设置。卸载：docker rm -f shuku-tts
set -e
NAME=shuku-tts
IMAGE=ghcr.io/smallyjh-boop/shuku-tts:latest
PORT=27210
THREADS=${THREADS:-3}   # NAS 是 4 核：留一个核给书库和别的程序

say() { printf '%s\n' "$*"; }
fail() { say ""; say "✗ $*"; exit 1; }

# Container Station 的 docker 不一定在 PATH 里
DOCKER=$(command -v docker 2>/dev/null || true)
if [ -z "$DOCKER" ]; then
  for d in /share/CACHEDEV1_DATA/.qpkg/container-station/bin/docker /share/*/.qpkg/container-station/bin/docker /usr/local/bin/docker; do
    [ -x "$d" ] && DOCKER=$d && break
  done
fi
[ -n "$DOCKER" ] || fail "找不到 docker：请先在 App Center 里装好并打开 Container Station"
say "① docker：$DOCKER"

# 端口：27210 被别的程序占了就停下（是以前装的 shuku-tts 没关系，下面会换掉）
if "$DOCKER" ps -a --format '{{.Names}}' | grep -qx "$NAME"; then
  say "② 已经装过，换成最新版本"
  "$DOCKER" rm -f "$NAME" > /dev/null
elif (netstat -tln 2>/dev/null || ss -tln 2>/dev/null) | grep -q ":$PORT "; then
  fail "端口 $PORT 被别的程序占用了，请把这段输出发给书库的作者"
else
  say "② 端口 $PORT 空着"
fi

say "③ 下载镜像（第一次大约 240MB，请稍等）…"
"$DOCKER" pull "$IMAGE" || fail "下载失败：确认 NAS 能上网、登录过 ghcr.io（和书库用的同一个令牌）"

say "④ 启动"
"$DOCKER" run -d --name "$NAME" --network host --restart unless-stopped \
  --memory 1500m \
  --label com.centurylinklabs.watchtower.enable=true \
  -e TTS_PORT=$PORT -e TTS_THREADS=$THREADS -e TZ=Asia/Bangkok \
  "$IMAGE" > /dev/null || fail "启动失败"

say "⑤ 等它加载模型（大约半分钟到两分钟）…"
i=0
until curl -fs "http://127.0.0.1:$PORT/health" > /tmp/shuku-tts-health 2>/dev/null; do
  i=$((i+1))
  if [ $i -gt 90 ]; then
    "$DOCKER" logs --tail 20 "$NAME" 2>&1 || true
    fail "三分钟还没启动好，请把上面这段输出发给书库的作者"
  fi
  sleep 2
done
RTF=$(sed -n 's/.*"rtf": *\([0-9.]*\).*/\1/p' /tmp/shuku-tts-health)
say ""
say "✓ 装好了：NAS 本地语音（MeloTTS 中英混读），端口 $PORT，$THREADS 线程"
say "  在这台 NAS 上合成 1 秒语音大约要 ${RTF:-?} 秒"
case "$RTF" in
  0.*) say "  速度够用。书库一分钟内会自动用上：中文、英文不经过外网，网络不好也不卡" ;;
  1.*) say "  有点慢，不过书库会在你读书时提前合成，一般不会卡；觉得卡可以在书库“设置 → 听书 → NAS 本地语音”里选“不用”" ;;
  *)   say "  太慢了，建议在书库“设置 → 听书 → NAS 本地语音”里选“不用”" ;;
esac
say "  以后有新版本会自动更新。卸载：$DOCKER rm -f $NAME"
