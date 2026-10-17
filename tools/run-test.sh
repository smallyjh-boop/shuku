#!/bin/bash
# 用测试书重新跑一遍入库：tools/run-test.sh <测试目录>
set -e
T=${1:-/tmp/shuku-test}
HERE=$(cd "$(dirname "$0")/.." && pwd)
pkill -f "shuku-test-server" 2>/dev/null || true
rm -rf "$T" && mkdir -p "$T" && cd "$T"
python3 "$HERE/tools/make-samples.py" "$T"
cp "$HERE/server/shuku" ./shuku-test-server
SHUKU_PORT=${PORT:-8090} SHUKU_DATA=./data SHUKU_INBOX=./inbox SHUKU_WEB="$HERE/web" \
SHUKU_LIBRARIES="图书:$T/books:book,漫画:$T/comics:comic" nohup ./shuku-test-server > server.log 2>&1 &
sleep 1.5
curl -s -XPOST http://127.0.0.1:${PORT:-8090}/api/auth/setup -d '{"Password":"test123"}' | python3 -c "import sys,json;print(json.load(sys.stdin)['token'])" > tok
echo "started in $T"
