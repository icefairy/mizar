#!/usr/bin/env bash
# fetch-zig.sh —— 下载官方 Zig 工具链，优先缓存到本地，可选内嵌进 Mizar 构建。
#
# 用法:
#   scripts/fetch-zig.sh                  # 按当前架构下载并缓存到 ~/.cache/zig（不碰仓库）
#   scripts/fetch-zig.sh --embed          # 下载后复制到 internal/zigboot/zigdist/（会被 go:embed 打进二进制）
#   scripts/fetch-zig.sh --arch arm64     # 下载指定架构的 linux 归档（交叉准备用）
#   scripts/fetch-zig.sh --version 0.17.1 # 指定版本（默认读 zigboot.Version）
#   scripts/fetch-zig.sh --offline-check  # 只检查缓存里是否已有当前架构归档
#   scripts/fetch-zig.sh --clean          # 清理本地缓存
#   scripts/fetch-zig.sh --jobs 64        # 并行分片数（默认 32；单连接被限速时很关键）
#   scripts/fetch-zig.sh --single         # 强制单连接下载
#
# 缓存布局:
#   ~/.cache/zig/<归档名>                  # 原始压缩包（多版本/多架构共存）
#   ~/.cache/zig/index.json                # 官方下载索引（用于取 shasum）
#   ~/.cache/mizar/zig/<平台>/zig          # 运行时 /zig init 释放出的工具链
#
# 设计原则:
#   - 缓存永远优先：本地已有且校验通过则不再联网（内网友好）。
#   - 仓库不存归档：internal/zigboot/zigdist/ 默认被 .gitignore 忽略，
#     只有显式 --embed 才放入，属于构建产物。
#   - 校验优先用官方 shasum；离线无索引时退化为 size 比对。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
EMBED_DIR="$ROOT/internal/zigboot/zigdist"
CACHE_DIR="${ZIG_CACHE_DIR:-$HOME/.cache/zig}"
IDX="$CACHE_DIR/index.json"
ZIG_SITE="https://ziglang.org/download"

VERSION="0.17.1"
# 默认按当前机器架构
case "$(uname -m)" in
  x86_64|amd64)   DEFAULT_PLAT="x86_64-linux" ;;
  aarch64|arm64)  DEFAULT_PLAT="aarch64-linux" ;;
  armv7l|armv6l)  DEFAULT_PLAT="arm-linux" ;;
  i386|i686)      DEFAULT_PLAT="x86-linux" ;;
  riscv64)        DEFAULT_PLAT="riscv64-linux" ;;
  ppc64le)        DEFAULT_PLAT="powerpc64le-linux" ;;
  loongarch64)    DEFAULT_PLAT="loongarch64-linux" ;;
  s390x)          DEFAULT_PLAT="s390x-linux" ;;
  *) echo "未知架构 $(uname -m)，请用 --arch 指定官方平台名（如 x86_64-linux）" >&2; exit 1 ;;
esac

PLAT="$DEFAULT_PLAT"
EMBED=0
OFFLINE_CHECK=0
CLEAN=0
FORCE=0
JOBS=32
SINGLE=0

while [ $# -gt 0 ]; do
  case "$1" in
    --embed)          EMBED=1 ;;
    --arch)           PLAT="$2"; shift ;;
    --version)        VERSION="$2"; shift ;;
    --offline-check)  OFFLINE_CHECK=1 ;;
    --clean)          CLEAN=1 ;;
    --force)          FORCE=1 ;;
    --jobs)           JOBS="$2"; shift ;;
    --single)         SINGLE=1 ;;
    -h|--help)        sed -n '2,23p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "未知参数: $1" >&2; exit 1 ;;
  esac
  shift
done

if [ "$CLEAN" = "1" ]; then
  echo "清理缓存: $CACHE_DIR"
  rm -rf "$CACHE_DIR"
  echo "清理运行时释放目录: $HOME/.cache/mizar/zig"
  rm -rf "$HOME/.cache/mizar/zig"
  exit 0
fi

case "$PLAT" in
  *-windows) EXT=".zip" ;;
  *)         EXT=".tar.xz" ;;
esac

NAME="zig-${PLAT}-${VERSION}${EXT}"
URL="${ZIG_SITE}/${VERSION}/${NAME}"
CACHED="$CACHE_DIR/$NAME"

mkdir -p "$CACHE_DIR"

# 读取官方索引里的 shasum（离线无索引时返回空）
fetch_index() {
  if [ -f "$IDX" ] && [ "$FORCE" = "0" ]; then
    return 0
  fi
  if [ "$OFFLINE_CHECK" = "1" ]; then
    return 0
  fi
  echo "获取官方下载索引..."
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "${ZIG_SITE}/index.json" -o "$IDX.tmp" 2>/dev/null && mv "$IDX.tmp" "$IDX" || rm -f "$IDX.tmp"
  elif command -v wget >/dev/null 2>&1; then
    wget -qO "$IDX.tmp" "${ZIG_SITE}/index.json" 2>/dev/null && mv "$IDX.tmp" "$IDX" || rm -f "$IDX.tmp"
  fi
}

# 从 index.json 提取 shasum 与 size（无 python 时返回空）
index_field() {
  local field="$1"
  [ -f "$IDX" ] || return 0
  if command -v python3 >/dev/null 2>&1; then
    python3 - "$IDX" "$VERSION" "$PLAT" "$field" <<'PY' 2>/dev/null || true
import json,sys
idx,ver,plat,field=sys.argv[1:5]
try:
    d=json.load(open(idx))[ver][plat]
    print(d.get(field,""))
except Exception:
    pass
PY
  elif command -v jq >/dev/null 2>&1; then
    jq -r --arg v "$VERSION" --arg p "$PLAT" --arg f "$field" '.[$v][$p][$f] // empty' "$IDX" 2>/dev/null || true
  fi
}

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

verify_archive() {
  local file="$1"
  fetch_index
  local want_sum want_size
  want_sum="$(index_field shasum)"
  want_size="$(index_field size)"
  if [ -n "$want_sum" ]; then
    local got
    got="$(sha256_of "$file")"
    if [ -n "$got" ] && [ "$got" != "$want_sum" ]; then
      echo "  ✗ sha256 不匹配（期望 ${want_sum:0:12}… 实际 ${got:0:12}…）" >&2
      return 1
    fi
    echo "  ✓ sha256 校验通过"
    return 0
  fi
  if [ -n "$want_size" ]; then
    local got_size
    got_size="$(wc -c < "$file" | tr -d ' ')"
    if [ "$got_size" != "$want_size" ]; then
      echo "  ✗ 文件大小不匹配（期望 $want_size 实际 $got_size）" >&2
      return 1
    fi
    echo "  ✓ 大小校验通过（无索引 sha256）"
    return 0
  fi
  echo "  ⚠ 无索引信息，跳过校验"
  return 0
}

# ---- 离线检查模式 ----
if [ "$OFFLINE_CHECK" = "1" ]; then
  if [ -f "$CACHED" ]; then
    verify_archive "$CACHED" && { echo "✓ 缓存已就绪: $CACHED"; exit 0; }
    echo "✗ 缓存文件校验失败: $CACHED" >&2
    exit 1
  fi
  echo "✗ 缓存中无 $NAME（需要联网下载或先用 --embed 的二进制发布）" >&2
  exit 1
fi

# ---- 下载（缓存优先）----
if [ -f "$CACHED" ] && [ "$FORCE" = "0" ]; then
  echo "缓存命中: $CACHED"
  verify_archive "$CACHED" || { echo "缓存损坏，重新下载..."; rm -f "$CACHED"; }
fi

# 并行分片下载：ziglang.org 对单连接限速极狠（实测 ~15 KB/s，57 MB 要一小时），
# 但对并发 range 请求宽松得多。按 N 份切片并发拉，每片独立重试续传。
# 失败（服务器不支持 Range 等）时回落到单连接下载。
download_parallel() {
  local url="$1" out="$2" total="$3" jobs="$4"
  local tmpd; tmpd="$(mktemp -d)"
  local piece=$(( (total + jobs - 1) / jobs ))
  local i
  for i in $(seq 0 $((jobs-1))); do
    (
      local s=$(( i * piece )) e=$(( i*piece + piece - 1 ))
      [ "$e" -ge "$total" ] && e=$(( total - 1 ))
      [ "$s" -ge "$total" ] && exit 0
      local want=$(( e - s + 1 )) f="$tmpd/p$(printf %05d "$i")"
      for _try in $(seq 1 40); do
        local have=0
        [ -f "$f" ] && have=$(wc -c < "$f" | tr -d ' ')
        [ "$have" -ge "$want" ] && exit 0
        curl -fsS --max-time 30 -r "$(( s + have ))-${e}" "$url" >> "$f" 2>/dev/null || true
      done
      exit 1
    ) &
  done
  wait
  local got; got=$(cat "$tmpd"/p* 2>/dev/null | wc -c | tr -d ' ')
  if [ "$got" = "$total" ]; then
    cat "$tmpd"/p* > "$out"
    rm -rf "$tmpd"
    return 0
  fi
  rm -rf "$tmpd"
  return 1
}

download_single() {
  local url="$1" out="$2"
  if command -v curl >/dev/null 2>&1; then
    curl -fL --progress-bar "$url" -o "$out"
  elif command -v wget >/dev/null 2>&1; then
    wget -O "$out" "$url"
  else
    echo "需要 curl 或 wget" >&2
    return 1
  fi
}

if [ ! -f "$CACHED" ]; then
  echo "下载: $URL"
  echo "  目标: $CACHED"
  WANT_SIZE="$(index_field size)"
  OK=0
  if [ "$SINGLE" = "0" ] && [ -n "$WANT_SIZE" ] && command -v curl >/dev/null 2>&1; then
    echo "  并行分片下载（$JOBS 路；单连接常被限速，这是默认策略）"
    if download_parallel "$URL" "$CACHED.part" "$WANT_SIZE" "$JOBS"; then OK=1; fi
    [ "$OK" = "0" ] && echo "  ⚠ 并行下载未完成（服务器可能不支持 Range），回落到单连接" >&2
  fi
  if [ "$OK" = "0" ]; then
    download_single "$URL" "$CACHED.part" || { rm -f "$CACHED.part"; exit 1; }
  fi
  mv "$CACHED.part" "$CACHED"
  verify_archive "$CACHED"
fi

SZ="$(wc -c < "$CACHED" | tr -d ' ')"
echo "✓ 就绪: $CACHED（$((SZ/1024/1024)) MB）"

# ---- 可选：复制进仓库供 go:embed ----
if [ "$EMBED" = "1" ]; then
  mkdir -p "$EMBED_DIR"
  cp -f "$CACHED" "$EMBED_DIR/$NAME"
  echo "✓ 已复制到内嵌目录: internal/zigboot/zigdist/$NAME"
  echo "  下一步: make build   （归档会被 go:embed 打进二进制）"
  echo "  注意: 二进制将增大 ~$((SZ/1024/1024)) MB"
else
  echo
  echo "提示: 要让 Mizar 二进制自带 Zig，执行  scripts/fetch-zig.sh --embed  后重新 make build"
fi
