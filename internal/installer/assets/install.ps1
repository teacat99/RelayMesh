# ==============================================================================
# RelayMesh 安装脚本（Windows PowerShell 5.1 / PowerShell 7+）：终端 Agent
#
#   临时运行（一次性身份，退出后不留痕迹；命令可直接从 Web「接入主机」面板复制）
#     & ([scriptblock]::Create((irm https://raw.githubusercontent.com/teacat99/RelayMesh/main/install.ps1).TrimStart([char]0xFEFF))) agent run --temp --server https://relay.example.com
#   为当前用户常驻（登录 Windows 后自动启动，不需要管理员）
#     … agent install --user --server https://relay.example.com
#   系统级常驻（开机自启，以 SYSTEM 运行；需要「以管理员身份运行」的 PowerShell）
#     … agent install --server https://relay.example.com
#   其余子命令（uninstall / status / id / reset-id …）原样转交给已安装的 relaymesh-agent。
#
#   从 RelayMesh 服务端直接取脚本与 Agent（推荐；Web「接入主机」面板生成的命令就是这种形式）
#     & ([scriptblock]::Create((irm https://relay.example.com/i/<票据>/install.ps1).TrimStart([char]0xFEFF))) agent install --server https://relay.example.com
#     票据只在服务端「发现窗口」开启期间有效；脚本与 Agent 二进制都从同一台服务端下载，不依赖 GitHub。
#
#   环境变量
#     RELAYMESH_VERSION         指定版本（例如 v1.7.0），默认最新 Release
#     RELAYMESH_DOWNLOAD_BASE   自建镜像的下载前缀（http(s):// 或 file:///）；其下需有同名二进制与 checksums.txt
#
#   RelayMesh 服务端请直接从 GitHub Releases 下载 relaymesh-windows-amd64.exe，或使用 Docker。
# ==============================================================================
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
try {
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
} catch { }

$RelayMeshRepo = if ($env:RELAYMESH_REPO) { $env:RELAYMESH_REPO } else { 'teacat99/RelayMesh' }

# 服务端自带安装源的状态。用哈希表而不是 $script: 变量：脚本常以 & ([scriptblock]::Create(...)) 方式运行，
# $script: 在这种情况下指向全局作用域，会把变量留在用户的会话里。
$RelayMeshHub = @{ Ticket = ''; Server = ''; PinHex = '' }
# @relaymesh-prelude@
# 服务端发放这份脚本时，会把上面这一行替换成本次发现窗口的安装票据（$RelayMeshHub.Ticket = '...'）；
# 直接从 GitHub 取到的脚本里它只是一行注释。

function Write-Say([string]$Message) { Write-Host $Message }
function Write-Warn([string]$Message) { Write-Host ('⚠️  ' + $Message) -ForegroundColor Yellow }
function Stop-Install([string]$Message) { throw (New-Object System.InvalidOperationException $Message) }

function Show-Usage {
    Write-Host @'
用法:
  install.ps1 agent install [选项]       安装终端 Agent 为常驻任务（系统级需管理员；--user 仅当前用户；--user --highest 当前用户 + 最高权限，需管理员）
  install.ps1 agent run [选项]           前台运行 Agent（--temp 使用一次性身份，退出后不留痕迹）
  install.ps1 agent upgrade [选项]       升级已安装的 Agent：换上新版本并只重启 Agent（任务定义、机器码、会话守护进程里的会话都不动；用户级安装加 --user）
  install.ps1 agent <其他子命令> [选项]  转交给已安装的 relaymesh-agent（uninstall / status / id …）

Agent 选项由 relaymesh-agent 解析，常用：--server <地址> --pin sha256:<指纹> --insecure --temp --user --highest
'@
}

# ---------- 平台与下载 ----------
function Get-AgentAsset {
    $arch = $env:PROCESSOR_ARCHITEW6432
    if (-not $arch) { $arch = $env:PROCESSOR_ARCHITECTURE }
    switch ($arch) {
        'AMD64' { }
        'ARM64' { Write-Warn 'Windows ARM64 使用 x64 版本（由系统模拟运行）。' }
        default { Stop-Install "不支持的 CPU 架构: $arch（目前提供 64 位 Windows 版本）" }
    }
    return 'relaymesh-agent-windows-amd64.exe'
}

# 当前是否从发放这份脚本的服务端下载（设置了 RELAYMESH_DOWNLOAD_BASE 自建镜像时，一律按普通方式处理）。
function Test-HubSource {
    return [bool]($RelayMeshHub.Ticket -and $RelayMeshHub.Server -and -not $env:RELAYMESH_DOWNLOAD_BASE)
}

function Get-ReleaseUrl([string]$File) {
    if ($env:RELAYMESH_DOWNLOAD_BASE) { return ($env:RELAYMESH_DOWNLOAD_BASE.TrimEnd('/') + '/' + $File) }
    if (Test-HubSource) { return ($RelayMeshHub.Server.TrimEnd('/') + '/i/' + $RelayMeshHub.Ticket + '/' + $File) }
    $tag = if ($env:RELAYMESH_VERSION) { $env:RELAYMESH_VERSION } else { 'latest' }
    if ($tag -eq 'latest') { return "https://github.com/$RelayMeshRepo/releases/latest/download/$File" }
    return "https://github.com/$RelayMeshRepo/releases/download/$tag/$File"
}

# 服务端用自签名证书、命令里带 --pin 时，下载按证书指纹校验：只放行与指纹完全一致的证书，其余一律拒绝。
# 回调必须是编译出来的静态方法：用脚本块做回调，在 PowerShell 7 的线程池线程上会因为没有运行空间而失败。
# 修改下面这段 C# 源码时，frontend/src/lib/terminal/installCommand.ts 里的 PS_CERT_PIN_SOURCE 必须同步（单元测试会比对）；
# 回调的注册与恢复写在 PowerShell 一侧，不要放进 C#：PowerShell 7.6（.NET 10）里编译期引用 ServicePointManager 会报 SYSLIB0014 错误。
function Enable-CertPin([string]$Hex) {
    if (-not ('RelayMeshCertPin' -as [type])) {
        Add-Type -TypeDefinition @'
using System.Net.Security;using System.Security.Cryptography;using System.Security.Cryptography.X509Certificates;
public static class RelayMeshCertPin{public static string Expected="";public static readonly RemoteCertificateValidationCallback Callback=Check;public static bool Check(object s,X509Certificate c,X509Chain h,SslPolicyErrors e){if(c==null)return false;using(var a=SHA256.Create())return System.BitConverter.ToString(a.ComputeHash(c.GetRawCertData())).Replace("-","").ToLowerInvariant()==Expected;}}
'@
    }
    [RelayMeshCertPin]::Expected = $Hex.ToLowerInvariant()
    [Net.ServicePointManager]::ServerCertificateValidationCallback = [RelayMeshCertPin]::Callback
}

function Save-Url([string]$Url, [string]$Dest) {
    $pinned = (Test-HubSource) -and $RelayMeshHub.PinHex
    $previous = [Net.ServicePointManager]::ServerCertificateValidationCallback
    if ($pinned) { Enable-CertPin $RelayMeshHub.PinHex }
    $wc = New-Object System.Net.WebClient
    try {
        $wc.Headers['User-Agent'] = 'relaymesh-install.ps1'
        $wc.DownloadFile($Url, $Dest)
    } finally {
        $wc.Dispose()
        if ($pinned) { [Net.ServicePointManager]::ServerCertificateValidationCallback = $previous }
    }
}

# 下载并按 Release 的 checksums.txt 校验。校验文件不可用时只提示（自建镜像可能没有）；哈希不一致则中止。
function Get-VerifiedAsset([string]$Asset, [string]$Dest) {
    $url = Get-ReleaseUrl $Asset
    Write-Say "⬇️  下载 $Asset ..."
    try {
        Save-Url $url $Dest
    } catch {
        Remove-Item -LiteralPath $Dest -Force -ErrorAction SilentlyContinue
        if (Test-HubSource) {
            Stop-Install ("从服务端下载 $Asset 失败（" + $RelayMeshHub.Server.TrimEnd('/') + "/i/***/$Asset）：" + $_.Exception.Message + "`n   常见原因：`n   1) 安装票据只在服务端「发现窗口」开启期间有效——请回到 Web 的「接入主机」，确认发现窗口仍在开启，再复制新的安装命令；`n   2) 这台服务端没有附带该平台的 Agent（$Asset）；`n   3) 服务端使用自签名证书——请在 Web 里选择「内置 HTTPS（指纹校验）」，生成带 --pin 的命令。")
        }
        Stop-Install ("下载失败：$url`n   " + $_.Exception.Message + "`n   请确认该版本的 Release 已包含 $Asset，或设置 RELAYMESH_DOWNLOAD_BASE 指向可访问的镜像。")
    }

    $sums = "$Dest.checksums"
    $haveSums = $false
    try {
        Save-Url (Get-ReleaseUrl 'checksums.txt') $sums
        $haveSums = $true
    } catch {
        Remove-Item -LiteralPath $sums -Force -ErrorAction SilentlyContinue
    }
    if (-not $haveSums) {
        Write-Warn '无法获取 checksums.txt，已跳过校验。'
        return
    }

    $want = $null
    foreach ($line in (Get-Content -LiteralPath $sums)) {
        $parts = $line.Trim() -split '\s+', 2
        if ($parts.Count -eq 2 -and ($parts[1] -eq $Asset -or $parts[1] -eq ('*' + $Asset))) {
            $want = $parts[0].ToLowerInvariant()
            break
        }
    }
    Remove-Item -LiteralPath $sums -Force -ErrorAction SilentlyContinue
    if (-not $want) {
        Remove-Item -LiteralPath $Dest -Force -ErrorAction SilentlyContinue
        Stop-Install "checksums.txt 中没有 $Asset 的记录，拒绝使用未经校验的文件。"
    }
    $got = (Get-FileHash -LiteralPath $Dest -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($got -ne $want) {
        Remove-Item -LiteralPath $Dest -Force -ErrorAction SilentlyContinue
        Stop-Install "SHA256 校验失败（期望 $want，实际 $got），已删除下载的文件。"
    }
    Write-Say '🔐 SHA256 校验通过'
}

# ---------- 进程与目录 ----------
function Test-IsAdmin {
    $id = [Security.Principal.WindowsIdentity]::GetCurrent()
    return (New-Object Security.Principal.WindowsPrincipal $id).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

function New-TempDir {
    $dir = Join-Path ([IO.Path]::GetTempPath()) ('relaymesh-agent-' + [Guid]::NewGuid().ToString('N').Substring(0, 8))
    [void](New-Item -ItemType Directory -Path $dir -Force)
    return $dir
}

# 杀毒软件可能还在扫描刚下载的文件，删除失败时稍等重试。
function Remove-TempDir([string]$Dir) {
    for ($i = 0; $i -lt 10; $i++) {
        try {
            if (Test-Path -LiteralPath $Dir) { Remove-Item -LiteralPath $Dir -Recurse -Force -ErrorAction Stop }
            return
        } catch {
            Start-Sleep -Milliseconds 300
        }
    }
}

# 按 CommandLineToArgvW 的规则给单个参数加引号，保证 Agent 收到的参数与这里写的完全一致。
function ConvertTo-QuotedArg([string]$Arg) {
    if ($Arg.Length -gt 0 -and $Arg -notmatch '[\s"]') { return $Arg }
    $sb = New-Object System.Text.StringBuilder
    [void]$sb.Append('"')
    $bs = 0
    foreach ($c in $Arg.ToCharArray()) {
        if ($c -eq '\') {
            $bs++
        } elseif ($c -eq '"') {
            [void]$sb.Append('\' * ($bs * 2 + 1))
            [void]$sb.Append('"')
            $bs = 0
        } else {
            if ($bs -gt 0) { [void]$sb.Append('\' * $bs); $bs = 0 }
            [void]$sb.Append($c)
        }
    }
    if ($bs -gt 0) { [void]$sb.Append('\' * ($bs * 2)) }
    [void]$sb.Append('"')
    return $sb.ToString()
}

# 运行外部程序并返回退出码。直接继承控制台：Agent 的中文输出与 Ctrl+C 都按原样工作（不经过 PowerShell 的管道）。
function Invoke-Native([string]$Exe, [string[]]$ArgList) {
    $psi = New-Object System.Diagnostics.ProcessStartInfo
    $psi.FileName = $Exe
    $psi.Arguments = (@($ArgList | ForEach-Object { ConvertTo-QuotedArg $_ }) -join ' ')
    $psi.UseShellExecute = $false
    $p = [System.Diagnostics.Process]::Start($psi)
    try {
        $p.WaitForExit()
        return [int]$p.ExitCode
    } finally {
        $p.Dispose()
    }
}

# ---------- Agent ----------
# 从 Agent 参数里取出 --server（决定去哪里下载）与 --pin（决定下载时是否按证书指纹校验），并确定本次的下载来源。
function Initialize-HubSource([string[]]$ArgList) {
    $RelayMeshHub.Server = ''
    $RelayMeshHub.PinHex = ''
    for ($i = 0; $i -lt $ArgList.Count; $i++) {
        $a = $ArgList[$i]
        if ($a -eq '--server' -and ($i + 1) -lt $ArgList.Count) { $RelayMeshHub.Server = $ArgList[$i + 1]; $i++ }
        elseif ($a -like '--server=*') { $RelayMeshHub.Server = $a.Substring(9) }
        elseif ($a -eq '--pin' -and ($i + 1) -lt $ArgList.Count) { $RelayMeshHub.PinHex = $ArgList[$i + 1]; $i++ }
        elseif ($a -like '--pin=*') { $RelayMeshHub.PinHex = $a.Substring(6) }
    }
    if ($env:RELAYMESH_DOWNLOAD_BASE -or -not $RelayMeshHub.Ticket) { return }
    if (-not $RelayMeshHub.Server) {
        Stop-Install '这份安装脚本来自 RelayMesh 服务端，需要用 --server 指明服务端地址（Web「接入主机」面板里的命令已经带上了）。'
    }
    if ($RelayMeshHub.PinHex) {
        if ($RelayMeshHub.Server -notmatch '^https://') { Stop-Install '--pin 只适用于 https:// 的服务端地址。' }
        $hex = ($RelayMeshHub.PinHex -replace '^sha256:', '')
        if ($hex -notmatch '^[0-9a-fA-F]{64}$') { Stop-Install '--pin 的格式应为 sha256:<64 位十六进制>。' }
        $RelayMeshHub.PinHex = $hex.ToLowerInvariant()
    }
}

function Invoke-AgentInstall([string[]]$Rest) {
    if (-not ($Rest -contains '--user') -and -not (Test-IsAdmin)) {
        Stop-Install ("安装系统级服务需要管理员权限：请「以管理员身份运行」PowerShell 后重试；`n如果只想为当前用户安装（登录后自动启动，不需要管理员），请加 --user")
    }
    if (($Rest -contains '--highest') -and -not (Test-IsAdmin)) {
        # 在下载 Agent 之前就拦下：创建「最高权限」计划任务本身需要管理员令牌
        Stop-Install ("以「当前用户 + 最高权限」安装需要管理员权限来创建计划任务：请「以管理员身份运行」PowerShell 后重试；`n不需要管理员权限的话，去掉 --highest（仍以当前用户身份运行，但没有管理员令牌）")
    }
    Initialize-HubSource $Rest
    $dir = New-TempDir
    try {
        $exe = Join-Path $dir 'relaymesh-agent.exe'
        Get-VerifiedAsset (Get-AgentAsset) $exe
        # 由 Agent 自己把程序复制到固定位置并创建计划任务，这里的临时副本随后删除。
        return (Invoke-Native $exe (@('install') + $Rest))
    } finally {
        Remove-TempDir $dir
    }
}

# 升级：下载新版本并运行它的 `upgrade`——由它替换已安装的程序、只重启 Agent 的计划任务。
# 不重写任务定义，所以不需要（也不转交）--server 之外的安装参数；这里只把 --user 交给它，用来选择用户级安装。
# 不在这里检查管理员：管理员默认升级系统级、普通用户升级自己的用户级，范围由 Agent 按已装的任务判断，权限不够时它会明确说明。
function Invoke-AgentUpgrade([string[]]$Rest) {
    Initialize-HubSource $Rest
    $dir = New-TempDir
    try {
        $exe = Join-Path $dir 'relaymesh-agent.exe'
        Get-VerifiedAsset (Get-AgentAsset) $exe
        $fwd = @()
        if ($Rest -contains '--user') { $fwd += '--user' }
        return (Invoke-Native $exe (@('upgrade') + $fwd))
    } finally {
        Remove-TempDir $dir
    }
}

function Invoke-AgentRun([string[]]$Rest) {
    Initialize-HubSource $Rest
    $dir = New-TempDir
    try {
        $exe = Join-Path $dir 'relaymesh-agent.exe'
        Get-VerifiedAsset (Get-AgentAsset) $exe
        return (Invoke-Native $exe (@('run') + $Rest))
    } finally {
        Remove-TempDir $dir
    }
}

function Find-InstalledAgent([string[]]$Rest) {
    $user = if ($env:LOCALAPPDATA) { Join-Path $env:LOCALAPPDATA 'Programs\RelayMesh Agent\relaymesh-agent.exe' } else { $null }
    $system = if ($env:ProgramFiles) { Join-Path $env:ProgramFiles 'RelayMesh Agent\relaymesh-agent.exe' } else { $null }
    if ($Rest -contains '--user') {
        $order = @($user)
    } elseif (Test-IsAdmin) {
        $order = @($system, $user)
    } else {
        $order = @($user, $system)
    }
    foreach ($p in $order) {
        if ($p -and (Test-Path -LiteralPath $p)) { return $p }
    }
    $cmd = Get-Command 'relaymesh-agent.exe' -ErrorAction SilentlyContinue
    if ($cmd) { return $cmd.Source }
    return $null
}

function Invoke-AgentPassthrough([string]$Sub, [string[]]$Rest) {
    $exe = Find-InstalledAgent $Rest
    if (-not $exe) { Stop-Install '没有找到已安装的 relaymesh-agent；请先执行 agent install。' }
    return (Invoke-Native $exe (@($Sub) + $Rest))
}

function Invoke-RelayMeshInstaller([string[]]$Rest) {
    if ($Rest.Count -eq 0 -or @('-h', '--help', 'help', '/?') -contains $Rest[0]) {
        Show-Usage
        return 0
    }
    if ($Rest[0] -ne 'agent') {
        Show-Usage
        Stop-Install "未知命令：$($Rest[0])"
    }
    if ($Rest.Count -lt 2) {
        Show-Usage
        return 1
    }
    $sub = $Rest[1]
    $tail = if ($Rest.Count -gt 2) { [string[]]$Rest[2..($Rest.Count - 1)] } else { [string[]]@() }
    switch ($sub) {
        'install' { return (Invoke-AgentInstall $tail) }
        'run' { return (Invoke-AgentRun $tail) }
        'upgrade' { return (Invoke-AgentUpgrade $tail) }
        { @('-h', '--help', 'help') -contains $_ } { Show-Usage; return 0 }
        default { return (Invoke-AgentPassthrough $sub $tail) }
    }
}

# ---------- 入口 ----------
# 不用 exit 结束整个会话：这个脚本常被 `& ([scriptblock]::Create(...))` 调用，exit 会关掉用户的 PowerShell 窗口。
$RelayMeshExit = 1
try {
    $RelayMeshResult = @(Invoke-RelayMeshInstaller ([string[]]$args))
    $RelayMeshExit = [int]$RelayMeshResult[-1]
} catch {
    Write-Host ('❌ ' + $_.Exception.Message) -ForegroundColor Red
    $RelayMeshExit = 1
}
if ($PSCommandPath) { exit $RelayMeshExit }
$global:LASTEXITCODE = $RelayMeshExit
