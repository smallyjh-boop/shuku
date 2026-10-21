#!/bin/bash
# 本地类型检查安卓、手表两个 App 的 Kotlin 代码，不需要安装 Android SDK：
# 用 Gradle 自带的 Kotlin 编译器 + android.jar（API 35）+ 库的签名存根（tools/stubs：Play 服务、Wear Tiles、
# ProtoLayout、表盘复杂功能等，签名照各库发布版本的 api/*.txt 写；Java 写的库用 Java 存根，Kotlin 写的库用 Kotlin 存根）。
# 用法：ANDROID_JAR=/path/to/android.jar tools/check.sh
# 正式打包用 GitHub Actions（.github/workflows/android.yml）。
set -e
HERE=$(cd "$(dirname "$0")/.." && pwd)
ANDROID_JAR=${ANDROID_JAR:-$HOME/androidsdk/android-35.jar}
[ -f "$ANDROID_JAR" ] || ANDROID_JAR=$(ls /home/*/androidsdk/android-35.jar 2>/dev/null | head -1)
[ -f "$ANDROID_JAR" ] || { echo "找不到 android.jar，请用 ANDROID_JAR=... 指定（Android SDK 的 platforms/android-35/android.jar）"; exit 2; }
GL=${GRADLE_LIB:-$(dirname "$(readlink -f "$(command -v gradle)")")/../lib}
KC=$(ls "$GL"/kotlin-compiler-embeddable-*.jar | head -1)
STD=$(ls "$GL"/kotlin-stdlib-[0-9]*.jar | head -1)
CP_COMPILER="$KC:$STD"
for j in kotlin-reflect kotlin-script-runtime trove4j kotlinx-coroutines-core-jvm annotations kotlin-daemon-embeddable; do
  CP_COMPILER="$CP_COMPILER:$(ls "$GL"/$j-[0-9]*.jar | head -1)"
done
OUT=${OUT:-/tmp/shuku-android-check}
rm -rf "$OUT" && mkdir -p "$OUT/stubs" "$OUT/gen"
kotlinc() { java -cp "$CP_COMPILER" org.jetbrains.kotlin.cli.jvm.K2JVMCompiler -no-stdlib -no-reflect -jvm-target 17 "$@" 2>&1 | grep -v "^Picked up" ; }
# 库的存根：先编 Java 写的，再编 Kotlin 写的（Kotlin 存根可以用到 Java 存根）
log=$(javac -nowarn -d "$OUT/stubs" -cp "$ANDROID_JAR" $(find "$HERE/tools/stubs/src" -name '*.java') 2>&1 | grep -v "^Picked up") || true
if echo "$log" | grep -q "error"; then echo "$log"; echo "存根：有编译错误"; exit 1; fi
KT_STUBS=$(find "$HERE/tools/stubs/src" -name '*.kt')
if [ -n "$KT_STUBS" ]; then
  log=$(kotlinc -cp "$ANDROID_JAR:$STD:$OUT/stubs" -d "$OUT/stubs" $KT_STUBS) || true
  if echo "$log" | grep -q "error:"; then echo "$log" | grep -E "error:"; echo "存根：有编译错误"; exit 1; fi
fi
check() {
  local mod=$1 pkg=$2
  mkdir -p "$OUT/gen/$mod/$(echo $pkg | tr . /)"
  # Gradle 构建时会自动生成 BuildConfig，这里补一个同样签名的
  cat > "$OUT/gen/$mod/$(echo $pkg | tr . /)/BuildConfig.java" <<JAVA
package $pkg;
public final class BuildConfig { public static final boolean DEBUG = true; public static final String APPLICATION_ID = "app.shuku"; public static final String VERSION_NAME = "check"; public static final int VERSION_CODE = 1; }
JAVA
  # R 也是 Gradle 生成的：按 res/drawable 里的文件补一个（代码里引用了不存在的图标，这里就编不过）
  {
    echo "package $pkg; public final class R { public static final class drawable {"
    n=0
    for f in "$HERE/$mod/src/main/res/drawable/"*.xml; do
      [ -f "$f" ] || continue
      n=$((n+1)); echo "public static final int $(basename "$f" .xml) = $((0x7f080000 + n));"
    done
    echo "} }"
  } > "$OUT/gen/$mod/$(echo $pkg | tr . /)/R.java"
  javac -nowarn -d "$OUT/gen/$mod/classes" "$OUT/gen/$mod/$(echo $pkg | tr . /)/BuildConfig.java" "$OUT/gen/$mod/$(echo $pkg | tr . /)/R.java" 2>&1 | grep -v "^Picked up" || true
  echo "== $mod"
  local log
  log=$(kotlinc -cp "$ANDROID_JAR:$STD:$OUT/stubs:$OUT/gen/$mod/classes" -d "$OUT/$mod" $(find "$HERE/$mod/src/main/java" "$HERE/common/src" -name '*.kt'))
  echo "$log" | grep -E "error:|warning:" | grep -v "is deprecated" || true
  if echo "$log" | grep -q "error:"; then echo "$mod：有编译错误"; return 1; fi
  echo "$mod：通过"
}
status=0
check app app.shuku || status=1
[ -d "$HERE/wear/src/main/java" ] && { check wear app.shuku.wear || status=1; }
# 资源和清单（没有 aapt，做一遍常见错误的检查）
python3 "$HERE/tools/check-res.py" app wear || status=1
exit $status
