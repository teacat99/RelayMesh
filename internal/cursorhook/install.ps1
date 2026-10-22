# RelayMesh Cursor hook 安装（Windows PowerShell，尽力而为）
#
# hook 程序的下载来源，按顺序：
#   1. 设置了 RELAYMESH_DOWNLOAD_BASE（自建镜像，其下要有同名 exe 与 checksums.txt）→ 只用它；
#   2. 本 Hub 的 /hooks/cursor/bin/（Docker 镜像和带 agent-dist 目录的部署里都有）；
#   3. 本 Hub 下载不到（单二进制、go run、stdio 直驱的部署没有 agent-dist 目录）→ 自动改从 GitHub Release 下载：
#      Hub 是正式版本时取同一个版本，否则取最新 Release。
# 不论从哪里下载，都用同一来源的 checksums.txt 校验 SHA256；校验失败不会换源重试。
# 可选环境变量：RELAYMESH_VERSION（Release 版本，例如 v2.0.0；latest 表示最新）、RELAYMESH_REPO（默认 teacat99/RelayMesh）。
param(
  [string]$HubBase = $env:RELAYMESH_HOOK_INSTALL_BASE,
  [string]$HookDir = $(if ($env:RELAYMESH_HOOK_DIR) { $env:RELAYMESH_HOOK_DIR } else { "$env:USERPROFILE\.local\bin" })
)

$ErrorActionPreference = "Stop"
$ProgressPreference = 'SilentlyContinue'
try { [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12 } catch { }

# 本 Hub 的版本：由 Hub 发放脚本时写入（只写正式版与预发布版的版本号，开发版与未知留空）；回退到 GitHub Release 时据此选版本。
$hubVersion = ''  # @relaymesh-hub-version@
$mirror = $env:RELAYMESH_DOWNLOAD_BASE
$repo = if ($env:RELAYMESH_REPO) { $env:RELAYMESH_REPO } else { 'teacat99/RelayMesh' }
$tag = if ($env:RELAYMESH_VERSION) { $env:RELAYMESH_VERSION } else { $hubVersion }

if (-not $HubBase -and -not $mirror) {
  Write-Error "请设置 RELAYMESH_HOOK_INSTALL_BASE 为本 Hub 的根地址。"
}
$arch = if ([Environment]::Is64BitOperatingSystem) { "amd64" } else { "386" }
if ($arch -ne "amd64") { Write-Error "仅支持 amd64 Windows。" }
$name = "relaymesh-hook-windows-$arch.exe"
New-Item -ItemType Directory -Force -Path $HookDir | Out-Null
$dest = Join-Path $HookDir $name
$tmp = Join-Path $env:TEMP "relaymesh-hook.exe"

# Get-Pair：把校验文件与 exe 都下载到临时文件；任何一个失败就返回 $false（由调用方决定换源还是退出）。
function Get-Pair([string]$prefix) {
  try {
    Invoke-WebRequest -Uri "$prefix/checksums.txt" -OutFile "$tmp.sums" -UseBasicParsing
    Invoke-WebRequest -Uri "$prefix/$name" -OutFile $tmp -UseBasicParsing
    return $true
  } catch {
    return $false
  }
}

# Stop-Download：下载失败时说清楚试过哪里、还能怎么装，然后退出。
function Stop-Download([string]$from) {
  $msg = "从 $from 下载 $name 失败。"
  if ($tag -and $tag -ne 'latest' -and -not $mirror) {
    $msg += "`n如果 $tag 的 Release 里没有 hook 程序，可以设置 RELAYMESH_VERSION=latest 再试。"
  }
  $msg += "`n也可以手动从 https://github.com/$repo/releases 下载 $name，放进 $HookDir，再运行：`n  $dest install --workflow <你的 workflow_id>"
  Write-Error $msg
}

$from = '本 Hub'
if ($mirror) {
  $from = "自建镜像 $($mirror.TrimEnd('/'))"
  if (-not (Get-Pair $mirror.TrimEnd('/'))) { Stop-Download $from }
} elseif (-not (Get-Pair "$($HubBase.TrimEnd('/'))/hooks/cursor/bin")) {
  Write-Warning "本 Hub 没有提供 hook 程序（部署里没有 agent-dist 目录，例如单二进制、go run、stdio 直驱；也可能是地址不可达或被限流），改从 GitHub Release 下载。"
  if (-not $tag -or $tag -eq 'latest') {
    Write-Warning "没有可对应的正式版本号，取最新 Release；hook 与 Hub 的版本可能不完全一致。"
    $from = 'GitHub Release（最新）'
    $prefix = "https://github.com/$repo/releases/latest/download"
  } else {
    $from = "GitHub Release $tag"
    $prefix = "https://github.com/$repo/releases/download/$tag"
  }
  if (-not (Get-Pair $prefix)) { Stop-Download $from }
}

$line = Get-Content "$tmp.sums" | Where-Object { $_ -match "  $name$" }
if (-not $line) { Write-Error "校验文件里没有 $name（来源：$from）。" }
$want = ($line -split '\s+')[0]
$got = (Get-FileHash -Path $tmp -Algorithm SHA256).Hash.ToLower()
if ($want -ne $got) { Write-Error "SHA256 校验失败（来源：$from）。" }
if ($from -ne '本 Hub') { Write-Host "已从 $from 下载 $name，SHA256 校验通过。" }
Remove-Item -Force "$tmp.sums" -ErrorAction SilentlyContinue
Move-Item -Force $tmp $dest
& $dest install @args
