#!/usr/bin/env bash
# ==============================================================================
# RelayMesh - One-Line Installer for Precompiled Binaries
# ==============================================================================
set -euo pipefail

REPO="teacat99/RelayMesh"
TAG="${1:-latest}"

echo "========================================================"
echo "⚡ RelayMesh - 一键安装预编译单二进制"
echo "   开源地址: https://github.com/${REPO}"
echo "========================================================"

# 1. 检测操作系统与架构
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"

case "${OS}" in
    linux*)     OS="linux" ;;
    darwin*)    OS="darwin" ;;
    msys*|mingw*|cygwin*) OS="windows" ;;
    *)          echo "❌ 不支持的操作系统: ${OS}"; exit 1 ;;
esac

case "${ARCH}" in
    x86_64|amd64)   ARCH="amd64" ;;
    arm64|aarch64)  ARCH="arm64" ;;
    *)              echo "❌ 不支持的 CPU 架构: ${ARCH}"; exit 1 ;;
esac

BINARY_NAME="relaymesh-${OS}-${ARCH}"
if [ "${OS}" = "windows" ]; then
    BINARY_NAME="${BINARY_NAME}.exe"
fi

# 2. 获取目标版本与下载 URL
if [ "${TAG}" = "latest" ]; then
    DOWNLOAD_URL="https://github.com/${REPO}/releases/latest/download/${BINARY_NAME}"
else
    DOWNLOAD_URL="https://github.com/${REPO}/releases/download/${TAG}/${BINARY_NAME}"
fi

echo "📦 正在匹配平台架构: ${OS}/${ARCH} (${BINARY_NAME})..."

# 3. 确定安装目录
INSTALL_DIR="/usr/local/bin"
TARGET_FILE="${INSTALL_DIR}/relaymesh"

if [ ! -w "${INSTALL_DIR}" ] 2>/dev/null; then
    INSTALL_DIR="${HOME}/.local/bin"
    TARGET_FILE="${INSTALL_DIR}/relaymesh"
    mkdir -p "${INSTALL_DIR}"
fi

# 4. 下载二进制产物
TMP_DIR="$(mktemp -d /tmp/relaymesh-install.XXXXXX)"
TMP_FILE="${TMP_DIR}/${BINARY_NAME}"

echo "⬇️ 开始下载最新预编译单二进制..."
if command -v curl &> /dev/null; then
    curl -fsSL --progress-bar "${DOWNLOAD_URL}" -o "${TMP_FILE}" || {
        echo "⚠️ 暂未从 GitHub Release 找到对应二进制产物，建议使用 go run 或 docker compose 运行。"
        rm -rf "${TMP_DIR}"
        exit 1
    }
elif command -v wget &> /dev/null; then
    wget -q --show-progress "${DOWNLOAD_URL}" -O "${TMP_FILE}" || {
        echo "⚠️ 暂未从 GitHub Release 找到对应二进制产物，建议使用 go run 或 docker compose 运行。"
        rm -rf "${TMP_DIR}"
        exit 1
    }
else
    echo "❌ 错误: 未找到 curl 或 wget 下载工具。"
    exit 1
fi

chmod +x "${TMP_FILE}"
mv -f "${TMP_FILE}" "${TARGET_FILE}"
rm -rf "${TMP_DIR}"

echo ""
echo "✅ RelayMesh 单二进制已成功安装至: ${TARGET_FILE}"
echo ""
echo "🚀 启动命令："
echo "   直接在终端运行: relaymesh"
echo "   - Web UI: http://localhost:18775/"
echo "   - HTTPS (麦克风权限): https://localhost:18776/"
echo "   - 本地原生桌面环境将自动唤醒默认浏览器！"
echo "========================================================"
