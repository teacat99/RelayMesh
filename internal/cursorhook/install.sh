#!/bin/sh
# RelayMesh Cursor hook 一键安装（POSIX）
# 由 Hub 在 GET /hooks/cursor/install.sh 提供；不含任何凭据。
#
# hook 程序的下载来源，按顺序：
#   1. 设置了 RELAYMESH_DOWNLOAD_BASE（自建镜像，其下要有同名二进制与 checksums.txt）→ 只用它；
#   2. 本 Hub 的 /hooks/cursor/bin/（Docker 镜像和带 agent-dist 目录的部署里都有）；
#   3. 本 Hub 下载不到（单二进制、go run、stdio 直驱的部署没有 agent-dist 目录）→ 自动改从 GitHub Release 下载：
#      Hub 是正式版本时取同一个版本，否则取最新 Release。
# 不论从哪里下载，都用同一来源的 checksums.txt 校验 SHA256，没有对应条目就拒绝安装；校验失败不会换源重试。
# 可选环境变量：RELAYMESH_VERSION（Release 版本，例如 v2.0.0；latest 表示最新）、RELAYMESH_REPO（默认 teacat99/RelayMesh）。
set -eu

HOOK_DIR="${RELAYMESH_HOOK_DIR:-$HOME/.local/bin}"
BASE="${RELAYMESH_HOOK_INSTALL_BASE:-}"
MIRROR="${RELAYMESH_DOWNLOAD_BASE:-}"
REPO="${RELAYMESH_REPO:-teacat99/RelayMesh}"
# 本 Hub 的版本：由 Hub 发放脚本时写入（只写正式版与预发布版的版本号，开发版与未知留空）；回退到 GitHub Release 时据此选版本。
HUB_VERSION=""  # @relaymesh-hub-version@
TAG="${RELAYMESH_VERSION:-$HUB_VERSION}"

if [ -z "$BASE" ] && [ -z "$MIRROR" ]; then
  echo "请设置 RELAYMESH_HOOK_INSTALL_BASE 为本 Hub 的根地址（例如 https://hub.example.com）。" >&2
  echo "或从 RelayMesh 会话页复制完整安装命令。" >&2
  exit 1
fi

case "$(uname -s)" in
  Linux*)  OS=linux ;;
  Darwin*) OS=darwin ;;
  MINGW*|MSYS*|CYGWIN*) OS=windows ;;
  *) echo "不支持的操作系统：$(uname -s)" >&2; exit 1 ;;
esac

case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) echo "不支持的 CPU 架构：$(uname -m)" >&2; exit 1 ;;
esac

EXT=""
[ "$OS" = "windows" ] && EXT=".exe"
NAME="relaymesh-hook-${OS}-${ARCH}${EXT}"

if command -v curl >/dev/null 2>&1; then
  # fetch <地址> <输出文件>：成功返回 0，失败返回非 0（由调用方决定换源还是退出）。
  fetch() { curl -fsSL --connect-timeout 15 "$1" -o "$2"; }
elif command -v wget >/dev/null 2>&1; then
  fetch() { wget -q -T 15 -O "$2" "$1"; }
else
  echo "需要 curl 或 wget。" >&2
  exit 1
fi

mkdir -p "$HOOK_DIR"
TMP="$(mktemp "${TMPDIR:-/tmp}/relaymesh-hook.XXXXXX")"
trap 'rm -f "$TMP" "${TMP}.sums"' EXIT

# get_pair <地址前缀>：把校验文件与二进制都下载到临时文件；任何一个失败就返回非 0。
get_pair() {
  rm -f "${TMP}.sums"
  fetch "$1/checksums.txt" "${TMP}.sums" && fetch "$1/${NAME}" "$TMP"
}

# die_download <来源描述>：下载失败时说清楚试过哪里、还能怎么装，然后退出。
die_download() {
  echo "从 $1 下载 ${NAME} 失败。" >&2
  if [ -n "$TAG" ] && [ "$TAG" != "latest" ] && [ -z "$MIRROR" ]; then
    echo "如果 ${TAG} 的 Release 里没有 hook 程序，可以设置 RELAYMESH_VERSION=latest 再试。" >&2
  fi
  echo "也可以手动从 https://github.com/${REPO}/releases 下载 ${NAME}，放进 ${HOOK_DIR} 并 chmod +x，再运行：" >&2
  echo "  ${HOOK_DIR}/${NAME} install --workflow <你的 workflow_id>" >&2
  exit 1
}

FROM="本 Hub"
if [ -n "$MIRROR" ]; then
  FROM="自建镜像 ${MIRROR%/}"
  get_pair "${MIRROR%/}" || die_download "$FROM"
elif ! get_pair "${BASE%/}/hooks/cursor/bin"; then
  echo "本 Hub 没有提供 hook 程序（部署里没有 agent-dist 目录，例如单二进制、go run、stdio 直驱；也可能是地址不可达或被限流），改从 GitHub Release 下载。" >&2
  if [ -z "$TAG" ] || [ "$TAG" = "latest" ]; then
    echo "没有可对应的正式版本号，取最新 Release；hook 与 Hub 的版本可能不完全一致。" >&2
    FROM="GitHub Release（最新）"
    PREFIX="https://github.com/${REPO}/releases/latest/download"
  else
    FROM="GitHub Release ${TAG}"
    PREFIX="https://github.com/${REPO}/releases/download/${TAG}"
  fi
  get_pair "$PREFIX" || die_download "$FROM"
fi

WANT="$(grep "  ${NAME}$" "${TMP}.sums" | awk '{print $1}')"
if [ -z "$WANT" ]; then
  echo "校验文件里没有 ${NAME}（来源：${FROM}）。" >&2
  exit 1
fi
if command -v sha256sum >/dev/null 2>&1; then
  GOT="$(sha256sum "$TMP" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
  GOT="$(shasum -a 256 "$TMP" | awk '{print $1}')"
else
  echo "需要 sha256sum 或 shasum。" >&2
  exit 1
fi
if [ "$GOT" != "$WANT" ]; then
  echo "SHA256 校验失败（来源：${FROM}）。" >&2
  exit 1
fi
[ "$FROM" = "本 Hub" ] || echo "已从 ${FROM} 下载 ${NAME}，SHA256 校验通过。" >&2

DEST="$HOOK_DIR/$NAME"
mv "$TMP" "$DEST"
chmod 755 "$DEST" 2>/dev/null || true
# exec 之后 EXIT 钩子不会再跑，校验文件在这里清掉。
rm -f "${TMP}.sums"

exec "$DEST" install "$@"
