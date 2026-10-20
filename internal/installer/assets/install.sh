#!/bin/sh
# ==============================================================================
# RelayMesh 安装脚本（POSIX sh：`curl … | sh` 与 `| bash` 都能运行）
#
#   服务端（单二进制）
#     curl -fsSL https://raw.githubusercontent.com/teacat99/RelayMesh/main/install.sh | sh
#     curl -fsSL …/install.sh | sh -s -- v1.7.0                    # 指定版本
#
#   终端 Agent（让 Web 终端连接这台主机；命令可直接从 Web「接入主机」面板复制）
#     curl -fsSL …/install.sh | sudo sh -s -- agent install --server https://relay.example.com    # 系统级需要 root；已是 root（容器 / 云主机）去掉 sudo
#     curl -fsSL …/install.sh | sh -s -- agent install --user --server https://relay.example.com   # 仅当前用户；Linux 上会自动开启 linger，退出登录后继续运行
#     curl -fsSL …/install.sh | sh -s -- agent run --temp --server https://relay.example.com
#     curl -fsSL …/install.sh | sudo sh -s -- agent upgrade --server https://relay.example.com      # 升级已安装的 Agent（仅当前用户装的加 --user，不用 sudo）
#     其余子命令（uninstall / status / id / reset-id …）原样转交给已安装的 relaymesh-agent。
#
#   从 RelayMesh 服务端直接取脚本与 Agent（推荐；Web「接入主机」面板生成的命令就是这种形式）
#     curl -fsSL https://relay.example.com/i/<票据>/install.sh | sudo sh -s -- agent install --server https://relay.example.com
#     票据只在服务端「发现窗口」开启期间有效；脚本与 Agent 二进制都从同一台服务端下载，不依赖 GitHub。
#     Web 面板生成的系统级命令会自己判断：root 不加 sudo，普通用户且有 sudo 才加，不必手动改。
#
#   环境变量
#     RELAYMESH_VERSION         指定版本（例如 v1.7.0），默认最新 Release
#     RELAYMESH_DOWNLOAD_BASE   自建镜像的下载前缀；其下需有同名二进制与 checksums.txt
# ==============================================================================
set -eu

REPO="${RELAYMESH_REPO:-teacat99/RelayMesh}"

# @relaymesh-prelude@
# 服务端发放这份脚本时，会把上面这一行替换成本次发现窗口的票据（RELAYMESH_HUB_TICKET）与
# 内置 HTTPS 的公钥指纹（RELAYMESH_HUB_SPKI）；直接从 GitHub 取到的脚本里它只是一行注释。
HUB_TICKET="${RELAYMESH_HUB_TICKET:-}"
HUB_SPKI="${RELAYMESH_HUB_SPKI:-}"
HUB_SERVER=""
HUB_PINNED=""
DL_SPKI=""

say() { printf '%s\n' "$*"; }
warn() { printf '⚠️  %s\n' "$*" >&2; }
die() { printf '❌ %s\n' "$*" >&2; exit 1; }

usage() {
    cat <<'EOF'
用法:
  install.sh [版本]                      安装 RelayMesh 服务端单二进制（默认最新 Release）
  install.sh agent install [选项]        安装终端 Agent 为常驻服务（系统级需 root；--user 仅当前用户）
  install.sh agent run [选项]            前台运行 Agent（--temp 使用一次性身份，退出后不留痕迹）
  install.sh agent upgrade [选项]        升级已安装的 Agent：换上新版本并只重启 Agent（服务配置、机器码、会话守护进程里的会话都不动）
  install.sh agent <其他子命令> [选项]   转交给已安装的 relaymesh-agent（uninstall / status / id …）

Agent 选项由 relaymesh-agent 解析，常用：--server <地址> --pin sha256:<指纹> --insecure --temp --user
EOF
}

# ---------- 平台与下载 ----------
detect_platform() {
    _os="$(uname -s | tr '[:upper:]' '[:lower:]')"
    _arch="$(uname -m)"
    case "${_os}" in
        linux*) OS="linux" ;;
        darwin*) OS="darwin" ;;
        msys* | mingw* | cygwin*) OS="windows" ;;
        *) die "不支持的操作系统: ${_os}" ;;
    esac
    case "${_arch}" in
        x86_64 | amd64) ARCH="amd64" ;;
        arm64 | aarch64) ARCH="arm64" ;;
        *) die "不支持的 CPU 架构: ${_arch}" ;;
    esac
}

# hub_base：服务端自带安装源的下载前缀 <--server 的地址>/i/<票据>；没有票据或没有 --server 时为空。
hub_base() {
    if [ -n "${HUB_TICKET}" ] && [ -n "${HUB_SERVER}" ]; then
        printf '%s/i/%s' "${HUB_SERVER%/}" "${HUB_TICKET}"
    fi
}

# release_url <文件名>：自建镜像优先，其次是发放这份脚本的服务端，最后才是 GitHub Release（最新版或指定版本）。
release_url() {
    if [ -n "${RELAYMESH_DOWNLOAD_BASE:-}" ]; then
        printf '%s/%s' "${RELAYMESH_DOWNLOAD_BASE%/}" "$1"
    elif [ -n "$(hub_base)" ]; then
        printf '%s/%s' "$(hub_base)" "$1"
    elif [ -z "${TAG:-}" ] || [ "${TAG}" = "latest" ]; then
        printf 'https://github.com/%s/releases/latest/download/%s' "${REPO}" "$1"
    else
        printf 'https://github.com/%s/releases/download/%s/%s' "${REPO}" "${TAG}" "$1"
    fi
}

# download <url> <目标文件>
# DL_SPKI 非空时（服务端用自签名证书、命令里带 --pin）按公钥指纹固定：curl 的 -k 只关闭 CA 校验，
# --pinnedpubkey 仍然强制，指纹不符会在收发任何数据之前中止连接。
download() {
    if command -v curl >/dev/null 2>&1; then
        if [ -t 2 ]; then _prog="--progress-bar"; else _prog="-sS"; fi
        if [ -n "${DL_SPKI}" ]; then
            curl -fL ${_prog} -k --pinnedpubkey "${DL_SPKI}" "$1" -o "$2"
        else
            curl -fL ${_prog} "$1" -o "$2"
        fi
    elif command -v wget >/dev/null 2>&1; then
        [ -z "${DL_SPKI}" ] || die "按证书指纹校验下载需要 curl（wget 不支持指纹固定），请先安装 curl。"
        wget -q "$1" -O "$2"
    else
        die "未找到 curl 或 wget，无法下载。"
    fi
}

sha256_of() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{print $1}'
    elif command -v shasum >/dev/null 2>&1; then
        shasum -a 256 "$1" | awk '{print $1}'
    else
        return 1
    fi
}

# fetch_asset <文件名> <目标文件>：下载并按 Release 的 checksums.txt 校验。
# 校验文件或哈希工具不可用时只提示（自建镜像可能没有）；哈希不一致则中止。
fetch_asset() {
    _asset="$1"
    _dest="$2"
    _url="$(release_url "${_asset}")"
    say "⬇️  下载 ${_asset} ..."
    if ! download "${_url}" "${_dest}"; then
        rm -f "${_dest}"
        if [ -z "${RELAYMESH_DOWNLOAD_BASE:-}" ] && [ -n "$(hub_base)" ]; then
            die "从服务端下载 ${_asset} 失败（${HUB_SERVER%/}/i/***/${_asset}）。
   常见原因：
   1) 安装票据只在服务端「发现窗口」开启期间有效——请回到 Web 的「接入主机」，确认发现窗口仍在开启，再复制新的安装命令；
   2) 这台服务端没有附带该平台的 Agent（${_asset}）；
   3) 服务端使用自签名证书——请在 Web 里选择「内置 HTTPS（指纹校验）」，生成带 --pin 的命令。"
        fi
        die "下载失败：${_url}
   请确认该版本的 Release 已包含 ${_asset}，或设置 RELAYMESH_DOWNLOAD_BASE 指向可访问的镜像。"
    fi

    _sums="${_dest}.checksums"
    if download "$(release_url checksums.txt)" "${_sums}" 2>/dev/null; then
        _want="$(awk -v n="${_asset}" '$2 == n || $2 == "*" n { print $1; exit }' "${_sums}")"
        rm -f "${_sums}"
        if [ -z "${_want}" ]; then
            rm -f "${_dest}"
            die "checksums.txt 中没有 ${_asset} 的记录，拒绝使用未经校验的文件。"
        fi
        if _got="$(sha256_of "${_dest}")"; then
            if [ "${_got}" != "${_want}" ]; then
                rm -f "${_dest}"
                die "SHA256 校验失败（期望 ${_want}，实际 ${_got}），已删除下载的文件。"
            fi
            say "🔐 SHA256 校验通过"
        else
            warn "本机没有 sha256sum / shasum，已跳过校验。"
        fi
    else
        rm -f "${_sums}"
        warn "无法获取 checksums.txt，已跳过校验。"
    fi
    chmod 755 "${_dest}"
}

# ---------- 服务端 ----------
install_server() {
    TAG="${1:-${RELAYMESH_VERSION:-latest}}"
    say "========================================================"
    say "⚡ RelayMesh - 一键安装预编译单二进制"
    say "   开源地址: https://github.com/${REPO}"
    say "========================================================"

    detect_platform
    _name="relaymesh-${OS}-${ARCH}"
    [ "${OS}" = "windows" ] && _name="${_name}.exe"
    say "📦 正在匹配平台架构: ${OS}/${ARCH} (${_name})..."

    _dir="/usr/local/bin"
    if [ ! -w "${_dir}" ] 2>/dev/null; then
        _dir="${HOME}/.local/bin"
        mkdir -p "${_dir}"
    fi
    _target="${_dir}/relaymesh"

    _tmp="$(mktemp -d "${TMPDIR:-/tmp}/relaymesh-install.XXXXXX")"
    trap 'rm -rf "${_tmp}"' EXIT
    fetch_asset "${_name}" "${_tmp}/${_name}"
    mv -f "${_tmp}/${_name}" "${_target}"

    say ""
    say "✅ RelayMesh 单二进制已成功安装至: ${_target}"
    say ""
    say "🚀 启动命令："
    say "   直接在终端运行: relaymesh"
    say "   - Web UI: http://localhost:18775/"
    say "   - HTTPS (麦克风权限): https://localhost:18776/"
    say "   - 本地原生桌面环境将自动唤醒默认浏览器！"
    say "   - 想用 Web 终端连接其他主机：登录后在「终端」里按提示安装 Agent"
    say "========================================================"
}

# ---------- Agent ----------
agent_asset() {
    detect_platform
    [ "${OS}" = "windows" ] && die "Windows 请直接从 GitHub Releases 下载 relaymesh-agent-windows-${ARCH}.exe，然后在 PowerShell 中运行：.\\relaymesh-agent.exe run --temp --server <地址>"
    ASSET="relaymesh-agent-${OS}-${ARCH}"
}

# scan_agent_args：从 Agent 参数里取出 --server（决定去哪里下载）与 --pin（决定下载时是否按指纹校验）。
scan_agent_args() {
    HUB_SERVER=""
    HUB_PINNED=""
    _expect=""
    for _a in "$@"; do
        if [ -n "${_expect}" ]; then
            if [ "${_expect}" = "server" ]; then HUB_SERVER="${_a}"; else HUB_PINNED="${_a}"; fi
            _expect=""
            continue
        fi
        case "${_a}" in
            --server) _expect="server" ;;
            --server=*) HUB_SERVER="${_a#--server=}" ;;
            --pin) _expect="pin" ;;
            --pin=*) HUB_PINNED="${_a#--pin=}" ;;
        esac
    done
}

# prepare_source：确定本次下载的来源与校验方式。设置了 RELAYMESH_DOWNLOAD_BASE（自建镜像）时一律按普通方式处理。
prepare_source() {
    DL_SPKI=""
    if [ -n "${RELAYMESH_DOWNLOAD_BASE:-}" ] || [ -z "${HUB_TICKET}" ]; then
        return 0
    fi
    if [ -z "${HUB_SERVER}" ]; then
        die "这份安装脚本来自 RelayMesh 服务端，需要用 --server 指明服务端地址（Web「接入主机」面板里的命令已经带上了）。"
    fi
    if [ -n "${HUB_PINNED}" ]; then
        case "${HUB_SERVER}" in
            https://*) ;;
            *) die "--pin 只适用于 https:// 的服务端地址。" ;;
        esac
        if [ -z "${HUB_SPKI}" ]; then
            die "这台服务端没有启用内置 HTTPS，无法按证书指纹校验下载；请改用受信任证书的地址，或去掉 --pin。"
        fi
        DL_SPKI="${HUB_SPKI}"
    fi
}

has_flag() {
    _want="$1"
    shift
    for _a in "$@"; do
        [ "${_a}" = "${_want}" ] && return 0
    done
    return 1
}

agent_bin_dir() {
    if has_flag --user "$@"; then
        printf '%s' "${HOME:?HOME 未设置，无法确定用户级安装目录}/.local/bin"
    else
        printf '%s' "/usr/local/bin"
    fi
}

agent_install() {
    agent_asset
    _dir="$(agent_bin_dir "$@")"
    if ! has_flag --user "$@" && [ "$(id -u)" != "0" ]; then
        if command -v sudo >/dev/null 2>&1; then
            die "系统级安装需要 root 权限：请在命令前加 sudo；只想为当前用户安装请加 --user。"
        fi
        die "系统级安装需要 root 权限，而当前用户不是 root，这台机器上也没有 sudo：请先切换到 root（例如 su -）再执行；只想为当前用户安装请加 --user。"
    fi
    mkdir -p "${_dir}"
    # 下载到同目录再原子替换：正在运行的旧 Agent 不受影响，安装器随后会让服务换用新文件。
    fetch_asset "${ASSET}" "${_dir}/relaymesh-agent.new"
    mv -f "${_dir}/relaymesh-agent.new" "${_dir}/relaymesh-agent"
    say "✅ 已安装 ${_dir}/relaymesh-agent"
    exec "${_dir}/relaymesh-agent" install "$@"
}

agent_run() {
    agent_asset
    _tmp="$(mktemp -d "${TMPDIR:-/tmp}/relaymesh-agent.XXXXXX")"
    trap 'rm -rf "${_tmp}"' EXIT
    # Ctrl+C 同时发给脚本与 Agent：脚本只需等 Agent 优雅退出后再清理（非空处理器不会被子进程继承为「忽略」）。
    trap 'true' INT TERM HUP
    fetch_asset "${ASSET}" "${_tmp}/relaymesh-agent"
    _rc=0
    "${_tmp}/relaymesh-agent" run "$@" || _rc=$?
    exit "${_rc}"
}

# agent_upgrade：下载新版本并运行它的 `upgrade`——由它替换已安装的程序、只重启 Agent 服务。
# 不重写服务配置，所以不需要（也不转交）--server 之外的安装参数；这里只把 --user 交给它，用来选择用户级安装。
agent_upgrade() {
    agent_asset
    _dir="$(agent_bin_dir "$@")"
    _tmp=""
    if [ -d "${_dir}" ] && [ -w "${_dir}" ]; then
        # 就近放在安装目录里：/tmp 常被挂成 noexec，新版本在那里根本跑不起来。
        _stage="${_dir}/.relaymesh-agent.upgrade"
    else
        _tmp="$(mktemp -d "${TMPDIR:-/tmp}/relaymesh-agent.XXXXXX")"
        _stage="${_tmp}/relaymesh-agent"
    fi
    trap 'rm -f "${_stage}"; [ -z "${_tmp}" ] || rm -rf "${_tmp}"' EXIT
    # Ctrl+C 同时发给脚本与 Agent：脚本只需等 Agent 退出后再清理。
    trap 'true' INT TERM HUP
    fetch_asset "${ASSET}" "${_stage}"
    _rc=0
    if has_flag --user "$@"; then
        "${_stage}" upgrade --user || _rc=$?
    else
        "${_stage}" upgrade || _rc=$?
    fi
    exit "${_rc}"
}

agent_passthrough() {
    _sub="$1"
    shift
    _bin=""
    if has_flag --user "$@"; then
        [ -x "${HOME:-}/.local/bin/relaymesh-agent" ] && _bin="${HOME}/.local/bin/relaymesh-agent"
    else
        [ -x /usr/local/bin/relaymesh-agent ] && _bin="/usr/local/bin/relaymesh-agent"
    fi
    if [ -z "${_bin}" ] && command -v relaymesh-agent >/dev/null 2>&1; then
        _bin="$(command -v relaymesh-agent)"
    fi
    [ -n "${_bin}" ] || die "没有找到已安装的 relaymesh-agent；请先执行 agent install。"
    exec "${_bin}" "${_sub}" "$@"
}

agent_main() {
    [ $# -ge 1 ] || { usage; exit 1; }
    TAG="${RELAYMESH_VERSION:-latest}"
    _sub="$1"
    shift
    case "${_sub}" in
        install)
            scan_agent_args "$@"
            prepare_source
            agent_install "$@"
            ;;
        run)
            scan_agent_args "$@"
            prepare_source
            agent_run "$@"
            ;;
        upgrade)
            scan_agent_args "$@"
            prepare_source
            agent_upgrade "$@"
            ;;
        -h | --help | help) usage ;;
        *) agent_passthrough "${_sub}" "$@" ;;
    esac
}

# ---------- 入口 ----------
case "${1:-}" in
    agent)
        shift
        agent_main "$@"
        ;;
    -h | --help | help)
        usage
        ;;
    *)
        install_server "${1:-}"
        ;;
esac
