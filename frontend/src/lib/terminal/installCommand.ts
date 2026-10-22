// 「接入新主机」时展示的 Agent 安装命令生成器。纯函数，便于单元测试。
//
// 命令里的每个可变部分（主机名、端口、指纹、安装票据）都先做白名单校验再拼接，
// 这样即使这些值来自用户输入、URL 或服务端响应，也不可能把 shell 元字符带进用户将要粘贴执行的命令里。
//
// 安装文件有两种来源：
//   - 服务端自带（hub）：命令从 <服务端>/i/<票据>/install.sh|ps1 下载，脚本再从同一服务端下载 Agent。
//     目标主机只需要能访问服务端本身（本来就必须能访问），不依赖 GitHub；票据随「发现窗口」生灭。
//   - GitHub（github）：服务端没有附带 Agent 时的备用来源，需要目标主机能访问 GitHub。

import { HOLDER_RESTART_TIP } from './holderRestart'

export type CertMode = 'pinned' | 'trusted' | 'insecure'
export type TargetOS = 'linux' | 'macos' | 'windows'
export type RunMode = 'service' | 'temporary'
/**
 * 安装范围：
 *   - system：系统级（Linux/macOS 需要 root，命令按需自动加 sudo；Windows 以 SYSTEM 账户开机自启，需要管理员）；
 *   - user：仅当前用户（不需要 root / 管理员；Windows 上登录后自启，最低权限）；
 *   - user-admin：仅 Windows——当前用户身份 + 最高权限（登录时自动带完整管理员令牌，无 UAC 弹窗；创建任务需要管理员）。
 */
export type Scope = 'system' | 'user' | 'user-admin'

/** 服务端自带安装源的参数（来自 /status 的 install 字段）。 */
export interface HubSource {
  /** 当前发现窗口的安装票据（32 位小写十六进制）。 */
  ticket: string
  /** 内置 HTTPS 叶子证书的公钥指纹（curl --pinnedpubkey 格式）。cert === 'pinned' 且目标是 Linux / macOS 时必填。 */
  spki: string
}

/**
 * SSH 网关的安装选项（Agent 的 --no-ssh-gateway / --ssh-allow）。
 * 网关和「主机服务」一样是 Agent 的标配能力，默认开启，不分版本：不写任何参数就是开，服务端可以让这台主机去连接它登记的 SSH 目标，
 * 目标的登录凭据只保存在这台主机上。
 *   - enabled=false：命令带 --no-ssh-gateway，这台主机只提供自己的终端 / 执行能力，不替服务端连接别的主机；
 *   - enabled=true：allow 是网关能连到哪里的唯一边界，留空表示不限制目标。
 * 省略整个选项（null / undefined）等同于默认：命令里没有任何 ssh 参数。
 */
export interface SshGatewayOption {
  enabled: boolean
  allow: string[]
}

/**
 * 装好之后同时开启「自动升级」（Agent 的 --auto-upgrade / --auto-window，见 docs/terminal-operations.md「Agent 自动升级」）。
 *   - notify：Agent 定期问 Hub 有没有更新的版本，只在日志与主机卡片上提示，不下载、不安装；
 *   - silent：在允许的时间段内自己下载、验证发布签名、升级；window 是允许「开始」的时间段（HH:MM-HH:MM，主机本地时间，可跨午夜），空 = 不限。
 * 同意由安装命令写在主机本地（Hub 改不了），随时可以在主机上 `auto-upgrade disable` 收回；Hub 管理员另有一个总闸，没打开时 Hub 不批准静默升级。
 * 省略整个选项（null / undefined）等同于默认：不碰自动升级。
 */
export interface AutoUpgradeOption {
  mode: 'notify' | 'silent'
  window?: string
}

/** HH:MM-HH:MM（24 小时制，起止不能相同）；与 Agent 的 ParseAutoWindow 同一个写法，写错时 Agent 会在安装之前就拒绝。 */
const AUTO_WINDOW_RE = /^([01]\d|2[0-3]):([0-5]\d)-([01]\d|2[0-3]):([0-5]\d)$/

/** 自动升级的时间段是否合法（空串 = 不限，合法）。 */
export function isValidAutoWindow(text: string): boolean {
  if (text === '') return true
  const m = AUTO_WINDOW_RE.exec(text)
  return !!m && `${m[1]}:${m[2]}` !== `${m[3]}:${m[4]}`
}

/** 允许名单最多的条目数（命令行不宜过长）。 */
export const MAX_SSH_ALLOW = 32
/**
 * 允许名单条目：CIDR（10.0.0.0/8、fd00::/8）、IP 或主机名（可含 * 通配）。这里只做 shell 安全的字符白名单；
 * 网段是否有效（例如前缀长度）由 Agent 在安装时校验，写错会在目标主机上得到一句明确的报错。
 */
const SSH_ALLOW_RE = /^[A-Za-z0-9*:][A-Za-z0-9*._:/-]{0,253}$/

/** 把用户输入的允许名单（逗号、空白或换行分隔）拆成条目，去重并保持顺序。 */
export function parseSshAllow(text: string): string[] {
  const seen = new Set<string>()
  const out: string[] = []
  for (const raw of text.split(/[\s,，;；]+/)) {
    const e = raw.trim()
    if (e && !seen.has(e)) {
      seen.add(e)
      out.push(e)
    }
  }
  return out
}

export interface InstallInput {
  os: TargetOS
  mode: RunMode
  scope: Scope
  cert: CertMode
  /** Agent 访问服务端用的主机名或 IP（不含协议与端口；IPv6 可带或不带方括号）。 */
  host: string
  /** null 表示省略端口（使用协议默认端口，适合反向代理 / 域名证书）。 */
  port: number | null
  /** 内置 HTTPS 叶子证书指纹，cert === 'pinned' 时必填。 */
  pin: string
  /** 提供时命令从服务端下载安装文件；缺省时从 GitHub 下载。 */
  hub?: HubSource | null
  /**
   * GitHub 来源的命令固定到哪个 Release（vX.Y.Z / vX.Y.Z-rc.N，取自服务端 /status 的 install.version）：
   * 命令取这个标签上的安装脚本，并用 RELAYMESH_VERSION 让脚本下载同一个版本的 Agent。
   * 省略或不是合法标签（开发版、未知版本的服务端）时取 main 上的脚本与最新正式版，并带一条提示。服务端自带来源（hub）时不参与。
   */
  release?: string | null
  /** SSH 网关选项；省略即默认（开启）。临时运行不提供网关，选项被忽略（但不能给允许名单）。 */
  sshGateway?: SshGatewayOption | null
  /**
   * 装好后同时开启自动升级；省略即默认（不碰）。只有「Linux + 常驻服务 + 服务端自带的安装源」可用：
   * 只有 Linux 的 systemd 常驻服务有「自己升级」的能力；而选项要求 Agent 认识 --auto-upgrade，只有和这个 Web 同一次发布的、
   * 服务端自带的 Agent 才一定认识（GitHub 上的版本可能更旧）。升级命令不改服务配置，选项不参与。
   */
  autoUpgrade?: AutoUpgradeOption | null
  /**
   * 要生成的命令：安装（默认），或升级已经装好的 Agent。
   * 升级只认服务端自带的来源（hub 必填，票据是这台主机的升级窗口票据）、不改服务配置，所以 mode 与 sshGateway 不参与，
   * scope 只用来决定要不要 --user（user-admin 与 user 等价）与是否需要管理员。
   */
  action?: 'install' | 'upgrade'
}

export interface InstallOutput {
  serverUrl: string
  command: string
  /** 命令从哪里下载安装文件。 */
  source: 'hub' | 'github'
  /** 可以生成命令，但用户应当知晓的风险或限制。 */
  warnings: string[]
  /** 无法生成命令的原因；非空时 command 为空串。 */
  errors: string[]
}

/**
 * 系统级安装需要 root：命令自己判断该不该加 sudo，使用者不必先弄清自己是不是 root。
 *   - 已经是 root（含容器、云主机默认的 root 登录）→ 不加：这类环境常常根本没有 sudo；
 *   - 不是 root 且装了 sudo → 加 sudo；
 *   - 不是 root 也没有 sudo → 什么都不加，脚本会以一句明白的话说明需要 root（而不是报 `sudo: command not found`）。
 * 这是一段固定的静态文本，不含任何可变值；放在管道之后、`sh` 之前，空展开时命令退化成普通的 `| sh -s -- …`。
 */
export const SUDO_IF_NEEDED = '$([ "$(id -u)" -ne 0 ] && command -v sudo >/dev/null 2>&1 && echo sudo)'

/** 安装脚本（公开仓库根目录的 install.sh，支持 `agent` 子模式）。 */
export const INSTALLER_URL = 'https://raw.githubusercontent.com/teacat99/RelayMesh/main/install.sh'
/** Windows PowerShell 版安装脚本（同样在公开仓库根目录）。 */
export const INSTALLER_PS_URL = 'https://raw.githubusercontent.com/teacat99/RelayMesh/main/install.ps1'

/**
 * Release 标签：正式版 vX.Y.Z，预发布版 vX.Y.Z-alpha|beta|rc.N（与 Go 侧 buildinfo.ReleaseTag 的输出一致）。
 * 标签会被写进用户复制去执行的 shell / PowerShell 命令，所以这里对服务端给的值再校验一次，不信任任何字符串。
 */
export const RELEASE_TAG_RE = /^v\d{1,4}\.\d{1,4}\.\d{1,4}(?:-(?:alpha|beta|rc)\.\d{1,3})?$/

/** 合法的 Release 标签原样返回，其余（含开发版、空值）返回 null。 */
export function validReleaseTag(tag: string | null | undefined): string | null {
  return typeof tag === 'string' && RELEASE_TAG_RE.test(tag) ? tag : null
}

/**
 * GitHub 来源的安装脚本地址：给了合法的 Release 标签就取该标签上的那一份，否则取 main 上的（main 在 2.0 正式版发布前还是 1.x 的脚本）。
 * 只换脚本地址不够：脚本没有指定版本时取「最新正式版」，所以命令里还要带 RELAYMESH_VERSION（见 buildInstallCommand）。
 */
export function githubInstallerUrl(os: TargetOS, release?: string | null): string {
  const ref = validReleaseTag(release) ?? 'main'
  return `https://raw.githubusercontent.com/teacat99/RelayMesh/${ref}/${os === 'windows' ? 'install.ps1' : 'install.sh'}`
}

const HOST_RE = /^[A-Za-z0-9]([A-Za-z0-9._-]*[A-Za-z0-9])?$/
const IPV6_RE = /^[0-9A-Fa-f:.]+(%[A-Za-z0-9._~-]+)?$/
const PIN_RE = /^sha256:[0-9a-f]{64}$/
const TICKET_RE = /^[0-9a-f]{32}$/
const SPKI_RE = /^sha256\/\/[A-Za-z0-9+/]{43}=$/

/**
 * PowerShell 版「按证书指纹校验」的回调类型源码。必须与 internal/installer/assets/install.ps1 里的 Enable-CertPin 保持一致
 * （同名类型 RelayMeshCertPin，脚本发现它已经存在就直接复用）；installCommand.test.ts 会比对两处源码，防止改了一处忘了另一处。
 * 回调必须是编译出来的静态方法：用脚本块做回调，在 PowerShell 7 的线程池线程上会因为没有运行空间而失败。
 * 回调的注册与恢复写在 PowerShell 一侧，不要放进 C#：PowerShell 7.6（.NET 10）里编译期引用 ServicePointManager 会报 SYSLIB0014 错误。
 */
export const PS_CERT_PIN_SOURCE =
  'using System.Net.Security;using System.Security.Cryptography;using System.Security.Cryptography.X509Certificates;' +
  'public static class RelayMeshCertPin{public static string Expected="";public static readonly RemoteCertificateValidationCallback Callback=Check;' +
  'public static bool Check(object s,X509Certificate c,X509Chain h,SslPolicyErrors e){if(c==null)return false;using(var a=SHA256.Create())return System.BitConverter.ToString(a.ComputeHash(c.GetRawCertData())).Replace("-","").ToLowerInvariant()==Expected;}}'

export function isLoopbackHost(host: string): boolean {
  const h = host.trim().toLowerCase().replace(/^\[|\]$/g, '')
  return h === 'localhost' || h === '::1' || h === '0.0.0.0' || h === '::' || /^127\./.test(h) || h.endsWith('.localhost')
}

/** 规范化并校验主机名；不合法返回 null。IPv6 字面量统一输出为带方括号的形式。 */
export function normalizeHost(raw: string): string | null {
  const h = raw.trim().replace(/^\[|\]$/g, '')
  if (!h || h.length > 253) return null
  if (h.includes(':')) return IPV6_RE.test(h) && !h.includes('%') ? `[${h}]` : null
  return HOST_RE.test(h) ? h : null
}

export function buildServerUrl(cert: CertMode, host: string, port: number | null): string {
  const scheme = cert === 'insecure' ? 'http' : 'https'
  return `${scheme}://${host}${port !== null ? `:${port}` : ''}`
}

/** 校验过的值里只有 URL 的方括号（IPv6）会被 shell 当成通配符，所以含特殊字符时才加引号，常见情况保持命令简洁。 */
function shWord(s: string): string {
  return /^[A-Za-z0-9._:/@%+=,-]+$/.test(s) ? s : `'${s}'`
}

/** 服务端自带的安装脚本地址：<服务端>/i/<票据>/install.sh|ps1。 */
export function hubScriptUrl(serverUrl: string, ticket: string, os: TargetOS): string {
  return `${serverUrl}/i/${ticket}/${os === 'windows' ? 'install.ps1' : 'install.sh'}`
}

export function buildInstallCommand(input: InstallInput): InstallOutput {
  const errors: string[] = []
  const warnings: string[] = []
  const hub = input.hub ?? null
  // GitHub 来源时固定到哪个 Release；null 表示没有可对应的版本（取 main 上的脚本与最新正式版）。
  const release = hub ? null : validReleaseTag(input.release)
  const upgrade = input.action === 'upgrade'
  const source: InstallOutput['source'] = hub ? 'hub' : 'github'
  const fail = (): InstallOutput => ({ serverUrl: '', command: '', source, warnings, errors })
  if (upgrade && !hub) {
    errors.push('升级只能经服务端的升级窗口完成：需要服务端附带 Agent 安装文件，并先开启这台主机的升级窗口')
  }

  const host = normalizeHost(input.host)
  if (!host) errors.push('服务端地址不合法：只允许主机名或 IP（不含协议、路径与端口）')
  if (input.port !== null && (!Number.isInteger(input.port) || input.port < 1 || input.port > 65535)) {
    errors.push('端口必须是 1–65535 之间的整数')
  }
  if (input.cert === 'pinned' && !PIN_RE.test(input.pin)) {
    errors.push('缺少内置 HTTPS 证书指纹：请确认服务端已启用内置 HTTPS，或改选「受信任证书」')
  }
  if (input.scope === 'user-admin' && input.os !== 'windows') {
    errors.push('「当前用户 + 最高权限」仅适用于 Windows；Linux / macOS 请选择「系统级」或「仅当前用户」')
  }
  // 临时运行的一次性身份不能当网关（网关要把凭据保存在本机，需要稳定的身份）：默认开启的网关在那里静默不提供，所以选项直接忽略；
  // 但给了允许名单就是自相矛盾，明确报错而不是悄悄丢掉。
  // 升级不改服务配置，所以运行方式与网关选项都不参与（升级的对象一定是已经常驻安装的 Agent）。
  const temporary = !upgrade && input.mode === 'temporary'
  const gw = upgrade ? null : (input.sshGateway ?? null)
  if (gw && temporary && gw.enabled && gw.allow.length > 0) {
    errors.push('临时运行不提供 SSH 网关，不能设置允许名单：网关要把目标的登录凭据保存在本机，需要常驻安装（选择「常驻服务」）')
  }
  if (gw && gw.enabled && !temporary) {
    if (gw.allow.length > MAX_SSH_ALLOW) errors.push(`SSH 网关的允许名单最多 ${MAX_SSH_ALLOW} 条`)
    for (const a of gw.allow) {
      if (!SSH_ALLOW_RE.test(a)) {
        errors.push(`允许名单里的「${a.length > 40 ? `${a.slice(0, 40)}…` : a}」不合法：只允许 CIDR（如 10.0.0.0/8）、IP 或主机名（可含 * 通配）`)
        break
      }
    }
  }
  const auto = upgrade ? null : (input.autoUpgrade ?? null)
  if (auto) {
    if (auto.mode !== 'notify' && auto.mode !== 'silent') {
      errors.push('自动升级的模式应为「提示」或「静默」')
    } else if (input.os !== 'linux' || temporary) {
      errors.push('自动升级目前只有 Linux 上由 systemd 管理的常驻服务支持：请选择 Linux +「常驻服务」，或取消自动升级（之后用升级命令手动升级）')
    } else if (!hub) {
      errors.push('自动升级选项只在从服务端自带的安装源安装时可用（GitHub 上的 Agent 版本可能还不认识它）：请先开启发现窗口')
    } else if (auto.mode === 'notify' && (auto.window ?? '') !== '') {
      errors.push('时间段只用于静默升级：提示模式不需要时间段')
    } else if (!isValidAutoWindow(auto.window ?? '')) {
      errors.push('自动升级的时间段应写成 HH:MM-HH:MM（主机本地时间，24 小时制，起止不能相同），例如 03:00-05:00')
    }
  }
  if (hub) {
    if (!TICKET_RE.test(hub.ticket)) errors.push(upgrade ? '升级票据无效：请重新开启升级窗口后再复制命令' : '安装票据无效：请重新开启发现窗口后再复制命令')
    // sh 版用 curl 的公钥指纹固定证书；PowerShell 版用上面的证书指纹（--pin），不需要公钥指纹。
    if (input.cert === 'pinned' && input.os !== 'windows' && !SPKI_RE.test(hub.spki)) {
      errors.push('服务端没有提供内置 HTTPS 的公钥指纹：请改选「受信任证书」，或重启服务端后再试')
    }
  }
  if (errors.length > 0 || !host) return fail()

  const serverUrl = buildServerUrl(input.cert, host, input.port)
  if (isLoopbackHost(host)) {
    warnings.push('这是本机回环地址，其他主机无法通过它连接到服务端；请改成局域网 IP 或域名')
  }
  if (input.cert === 'insecure') {
    warnings.push('明文 ws:// 连接不加密也不校验服务端身份，只应在你完全控制的内网中使用')
  }
  // GitHub 来源又没有可对应的 Release 标签（开发版 / 版本未知的服务端）：脚本与 Agent 只能取 main 与最新正式版。
  // 2.0 正式版发布之前，那两处都还没有 Agent，命令会失败；与其生成一条看似能用的命令，不如明说。
  if (!hub && !release && !upgrade) {
    warnings.push(
      '这台服务端是开发版或版本未知，命令对应不到具体的 Release：2.0 正式版发布之前，公开仓库 main 与最新正式版里都还没有 Agent，命令会失败。' +
        '请改用 Docker 部署、给服务端放一个 agent-dist 目录，或用正式 / 预览版的二进制启动服务端。',
    )
  }

  const flags = [`--server ${shWord(serverUrl)}`]
  if (input.cert === 'pinned') flags.push(`--pin ${input.pin}`)
  if (input.cert === 'insecure') flags.push('--insecure')
  // 网关默认开启，所以只写与默认不同的部分：关闭 → --no-ssh-gateway；开启 → 每条 --ssh-allow。
  // 含 * 的条目会被 shWord 加上单引号，避免被 shell 当成通配符展开。
  const gwOn = !!gw && !temporary
  if (gwOn) flags.push(...(gw.enabled ? gw.allow.map(a => `--ssh-allow ${shWord(a)}`) : ['--no-ssh-gateway']))
  // 自动升级：已通过上面的校验（Linux + 常驻 + 服务端来源），模式与时间段都是白名单格式，可以直接拼接。
  if (auto) {
    flags.push(`--auto-upgrade ${auto.mode}`)
    if (auto.mode === 'silent' && auto.window) flags.push(`--auto-window ${auto.window}`)
  }

  let command: string
  if (input.os === 'windows') {
    // PowerShell：把脚本内容建成脚本块再带参数调用（iex 无法传参）。TrimStart 去掉可能残留的 BOM 字符。
    // 所有值都先过白名单校验，再用单引号包起来，PowerShell 不会对其做任何展开。
    const psFlags = [`--server '${serverUrl}'`]
    if (input.cert === 'pinned') psFlags.push(`--pin '${input.pin}'`)
    if (input.cert === 'insecure') psFlags.push('--insecure')
    if (gwOn) psFlags.push(...(gw.enabled ? gw.allow.map(a => `--ssh-allow '${a}'`) : ['--no-ssh-gateway']))
    const scriptUrl = hub ? hubScriptUrl(serverUrl, hub.ticket, 'windows') : githubInstallerUrl('windows', release)
    // GitHub 来源固定到某个 Release 时，先在这个会话里设 RELAYMESH_VERSION，脚本才会下载同一个版本的 Agent（默认取最新正式版）。
    // 标签已经过 RELEASE_TAG_RE 校验，只含字母、数字、点与连字符，放进单引号里不会被 PowerShell 展开。
    const verEnv = release ? `$env:RELAYMESH_VERSION='${release}'; ` : ''
    const ps = (sub: string) => {
      const tail = `agent ${sub} ${psFlags.join(' ')}`
      if (!hub || input.cert !== 'pinned') {
        return `${verEnv}& ([scriptblock]::Create((irm '${scriptUrl}').TrimStart([char]0xFEFF))) ${tail}`
      }
      // 自签名证书：连「取得安装脚本」这一步也要按指纹校验，所以命令自带回调；脚本取回后，由脚本自己用同一指纹校验 Agent 的下载。
      // 下载失败时 $s 保持为空，直接结束，不会执行任何内容；回调在 finally 里恢复，不影响这个窗口里的其他操作。
      const pinHex = input.pin.slice('sha256:'.length)
      return (
        `& { $s = $null; if (-not ('RelayMeshCertPin' -as [type])) { Add-Type -TypeDefinition '${PS_CERT_PIN_SOURCE}' }; ` +
        `[RelayMeshCertPin]::Expected = '${pinHex}'; $prev = [Net.ServicePointManager]::ServerCertificateValidationCallback; ` +
        `[Net.ServicePointManager]::ServerCertificateValidationCallback = [RelayMeshCertPin]::Callback; ` +
        `try { $s = [Text.Encoding]::UTF8.GetString((New-Object Net.WebClient).DownloadData('${scriptUrl}')) } finally { [Net.ServicePointManager]::ServerCertificateValidationCallback = $prev }; ` +
        `if (-not $s) { return }; & ([scriptblock]::Create($s.TrimStart([char]0xFEFF))) ${tail} }`
      )
    }
    if (upgrade) {
      // 升级：只替换程序并重启 Agent 的计划任务，不会重新创建任务；系统级需要管理员，其余两种范围（含「最高权限」）都是当前用户的任务。
      command = ps(input.scope === 'system' ? 'upgrade' : 'upgrade --user')
      warnings.push(
        input.scope === 'system'
          ? '请在「以管理员身份运行」的 PowerShell 中执行（替换系统级任务使用的程序需要管理员令牌）。'
          : '请用安装这个 Agent 的那个 Windows 账户执行；「当前用户 + 最高权限」的任务需要管理员 PowerShell。',
      )
    } else if (input.mode === 'temporary') {
      command = ps('run --temp')
      warnings.push('临时运行使用一次性身份，机器码每次启动都会变化；关闭该窗口或按 Ctrl+C 即退出')
      warnings.push('从普通（非管理员）PowerShell 运行时会弹出 UAC 确认，并在新的管理员窗口里连接，使终端拥有管理员身份；不需要管理员权限可在命令末尾追加 --no-elevate')
    } else if (input.scope === 'user-admin') {
      command = ps('install --user --highest')
      warnings.push(
        '请在「以管理员身份运行」的 PowerShell 中执行（创建最高权限的计划任务需要管理员令牌）。任务以你自己的账户身份与环境运行，登录 Windows 后自动启动并带完整的管理员令牌，不弹 UAC；其中打开的 Shell 拥有管理员权限，注销后停止。之后修改或卸载它同样需要管理员 PowerShell',
      )
    } else if (input.scope === 'user') {
      command = ps('install --user')
      warnings.push('仅当前用户：登录 Windows 后自动启动（计划任务），注销后停止；不需要管理员权限，Shell 为普通用户权限')
    } else {
      command = ps('install')
      warnings.push(
        '请在「以管理员身份运行」的 PowerShell 中执行。系统级任务开机自启，以 SYSTEM 身份运行，其中打开的 Shell 权限等同本机最高；若希望以自己的账户运行，请改选「仅当前用户」（需要管理员权限的命令可选「当前用户 + 最高权限」）',
      )
    }
  } else {
    // 自签名证书（内置 HTTPS）：-k 只是跳过「是否由受信任机构签发」，--pinnedpubkey 仍会逐字校验服务端公钥，指纹不符立即中止。
    const fetch = hub
      ? input.cert === 'pinned'
        ? `curl -fsSL -k --pinnedpubkey '${hub.spki}' ${shWord(hubScriptUrl(serverUrl, hub.ticket, input.os))}`
        : `curl -fsSL ${shWord(hubScriptUrl(serverUrl, hub.ticket, input.os))}`
      : `curl -fsSL ${githubInstallerUrl(input.os, release)}`
    // GitHub 来源固定到某个 Release 时，用 env 给脚本传 RELAYMESH_VERSION（只对这一条命令有效）：脚本没有指定版本时取「最新正式版」，
    // 预览期间那里没有 Agent。env 放在 sudo 之后、sh 之前，所以 sudo 与不加 sudo 两种情形都带得过去。标签已经过 RELEASE_TAG_RE 校验。
    const sh = release ? `env RELAYMESH_VERSION=${release} sh` : 'sh'
    if (upgrade) {
      // 升级：系统级与安装时一样，命令自己判断 root / sudo；用户级必须由安装它的那个账户执行。
      command =
        input.scope === 'system'
          ? `${fetch} | ${SUDO_IF_NEEDED} sh -s -- agent upgrade ${flags.join(' ')}`
          : `${fetch} | sh -s -- agent upgrade --user ${flags.join(' ')}`
      if (input.scope !== 'system') warnings.push('请用安装这个 Agent 的那个账户执行：仅当前用户的服务只属于该账户，换账户执行会找不到它。')
    } else if (input.mode === 'temporary') {
      command = `${fetch} | ${sh} -s -- agent run --temp ${flags.join(' ')}`
      warnings.push('临时运行使用一次性身份，机器码每次启动都会变化；关闭该终端或按 Ctrl+C 即退出')
    } else if (input.scope === 'user') {
      command = `${fetch} | ${sh} -s -- agent install --user ${flags.join(' ')}`
      if (input.os === 'linux') {
        warnings.push('仅当前用户：安装时会自动执行 loginctl enable-linger 命令，让服务在退出登录后继续运行（卸载时一并关闭）；系统不允许时会提示你手动执行')
      }
    } else {
      command = `${fetch} | ${SUDO_IF_NEEDED} ${sh} -s -- agent install ${flags.join(' ')}`
    }
  }
  // 默认开启的网关不需要任何提示；只有用户改动了它（关闭，或设置了允许名单）才说明含义与生效方式。
  if (gwOn && !gw.enabled) {
    warnings.push('已关闭 SSH 网关（--no-ssh-gateway）：这台主机只提供自己的终端与执行能力，服务端不能借它去连接别的主机；以后想开启，重新执行不带该参数的安装命令即可。')
  }
  if (gwOn && gw.enabled && gw.allow.length > 0) {
    warnings.push('SSH 网关的允许名单是它能连到哪里的唯一边界：不在名单里的地址，网关一律拒绝；目标的登录凭据只保存在这台主机自己的数据目录里（密封存放），服务端不保存。')
  }
  if (auto?.mode === 'notify') {
    warnings.push('自动升级（提示）：Agent 会定期问 Hub 有没有更新的版本，只在日志与 Web 主机卡片上提示，不会下载、不会安装；随时可以在这台主机上执行 relaymesh-agent auto-upgrade disable 收回（立刻生效）。Hub 管理员需要先在设置里打开「Agent 自动升级」总闸，Hub 才会回答检查。')
  }
  if (auto?.mode === 'silent') {
    warnings.push(
      `自动升级（静默）：发现新版本后，在${auto.window ? `允许的时间段（${auto.window}，主机本地时间）内` : '任何时间'}自己下载、验证发布签名并升级——没有签名或签名不对的版本一律不安装，新版本连不上 Hub 会自动还原；会话由守护进程持有，不受 Agent 重启影响。` +
        '同意写在这台主机本地，Hub 改不了，随时可以在这台主机上执行 relaymesh-agent auto-upgrade disable 收回。Hub 管理员需要先在设置里打开「Agent 自动升级」总闸，否则 Hub 不批准。',
    )
  }
  if (gwOn && (!gw.enabled || gw.allow.length > 0)) {
    warnings.push(
      '这台主机已经装过 Agent 时，重新执行这条命令会更新服务单元；但正在运行的会话守护进程要重启后才会采用新的网关设置——在合适的时机，在这台主机上执行 relaymesh-agent holder restart 重启它（会结束其中的会话和模型正在运行的命令），或在命令末尾加 --drain，等它们自然结束后再重启。' + HOLDER_RESTART_TIP,
    )
  }
  return { serverUrl, command, source, warnings, errors }
}

// ---------- 安装来源的选择 ----------

/** 各系统需要的全部平台：服务端必须全部附带，才能保证「不管目标主机是什么架构」都能从服务端取到 Agent。 */
const OS_PLATFORMS: Record<TargetOS, string[]> = {
  linux: ['linux-amd64', 'linux-arm64'],
  macos: ['darwin-amd64', 'darwin-arm64'],
  windows: ['windows-amd64'],
}
const OS_LABEL: Record<TargetOS, string> = { linux: 'Linux', macos: 'macOS', windows: 'Windows' }

/** /status 里的 install 字段（见 api/terminal.ts 的 InstallSourceView）。 */
export interface InstallSourceStatus {
  available: boolean
  ticket?: string
  platforms?: string[]
  spki_pin?: string
  /** 这个服务端对应的 Release 标签（vX.Y.Z / vX.Y.Z-rc.N）；开发版与未知版本为空。有没有安装源都会返回。 */
  version?: string
}

export type SourceChoice =
  /** 从服务端下载。 */
  | { kind: 'hub'; hub: HubSource }
  /**
   * 从 GitHub 下载；note 说明为什么没有用服务端自带的来源。
   * release 是命令要固定到的 Release 标签（与服务端同一个版本）；服务端是开发版 / 版本未知时为 null（取 main 与最新正式版）。
   */
  | { kind: 'github'; note: string; release: string | null }
  /** 服务端有安装文件，但此刻拿不到票据：发现窗口没开（或票据还在路上），先不生成命令。 */
  | { kind: 'waiting'; reason: 'window-closed' | 'ticket-pending' }

/** 回退到 GitHub 时的说明：固定到服务端同一个 Release；服务端是开发版 / 版本未知时说明只能取 main 与最新正式版。 */
function githubNote(why: string, release: string | null): string {
  return release
    ? `${why}，命令改从 GitHub Release ${release}（与这台服务端同一个版本）获取：目标主机需要能访问 GitHub。`
    : `${why}，而且它是开发版或版本未知，命令只能从 GitHub 取公开仓库 main 上的安装脚本与最新正式版：目标主机需要能访问 GitHub。`
}

/**
 * 决定某个目标系统的安装命令从哪里下载。
 *   - 服务端没有附带 Agent，或缺少该系统的某个架构 → 退回 GitHub（并说明原因），不会生成一条半截能用的命令；
 *   - 服务端有，但发现窗口没开 → 等待（票据只在窗口开启期间存在，没有票据的命令是下不动的）；
 *   - 其余 → 服务端自带来源。
 * windowActive 取自实时的发现窗口状态：票据随窗口失效，不能只看 /status 里缓存的票据。
 */
export function chooseSource(os: TargetOS, install: InstallSourceStatus | null | undefined, windowActive: boolean): SourceChoice {
  // 回退到 GitHub 时固定到服务端同一个 Release（取自 /status 的 install.version，再校验一次）；开发版 / 版本未知则为 null。
  const release = validReleaseTag(install?.version)
  if (!install || !install.available) {
    return { kind: 'github', note: githubNote('这台服务端没有附带 Agent 安装文件', release), release }
  }
  const have = new Set(install.platforms ?? [])
  const missing = OS_PLATFORMS[os].filter(p => !have.has(p))
  if (missing.length > 0) {
    return { kind: 'github', note: githubNote(`这台服务端附带的 Agent 不包含 ${OS_LABEL[os]} 的 ${missing.join('、')}`, release), release }
  }
  if (!windowActive) return { kind: 'waiting', reason: 'window-closed' }
  if (!install.ticket) return { kind: 'waiting', reason: 'ticket-pending' }
  return { kind: 'hub', hub: { ticket: install.ticket, spki: install.spki_pin ?? '' } }
}

/** 把机器码按「RM-XXXX-XXXX-XXXX-XXXX」规范显示；格式不符时原样返回。 */
export function formatMachineCode(code: string): string {
  return code.trim().toUpperCase()
}

export interface DefaultTarget {
  host: string
  port: number | null
  cert: CertMode
}

/**
 * 根据管理员当前访问页面的方式推断默认值：
 *   - 通过内置 HTTPS 端口访问（自签名证书）→ 用指纹校验；
 *   - 通过其他 https 访问（多半是域名 + 反向代理的受信任证书）→ 受信任证书，沿用同一端口；
 *   - http 访问 → 内置 HTTPS 可用时推荐用内置 HTTPS + 指纹，否则只能明文。
 */
export function inferDefaultTarget(loc: { protocol: string; hostname: string; port: string }, httpsPort: number | null, pin: string): DefaultTarget {
  const { reason: _reason, ...target } = inferDefault(loc, httpsPort, pin)
  return target
}

/** 与 inferDefaultTarget 相同，并附上一句「为什么这样选」，供界面把证书选项折叠成一行结论。 */
export function inferDefault(
  loc: { protocol: string; hostname: string; port: string },
  httpsPort: number | null,
  pin: string,
): DefaultTarget & { reason: string } {
  const host = loc.hostname
  const locPort = loc.port ? Number(loc.port) : null
  const hasPin = PIN_RE.test(pin)
  if (loc.protocol === 'https:') {
    if (httpsPort !== null && locPort === httpsPort && hasPin) {
      return { host, port: httpsPort, cert: 'pinned', reason: '你正在通过服务端的内置 HTTPS 端口访问' }
    }
    return { host, port: locPort, cert: 'trusted', reason: `你正在通过 https://${host}${locPort !== null ? `:${locPort}` : ''} 访问，目标主机沿用同一个地址与受信任的证书` }
  }
  if (httpsPort !== null && hasPin) {
    return { host, port: httpsPort, cert: 'pinned', reason: '你正在通过 http 访问，而服务端启用了内置 HTTPS，所以推荐按证书指纹校验' }
  }
  return { host, port: locPort, cert: 'insecure', reason: '服务端没有启用内置 HTTPS，你当前又是通过 http 访问，所以只能使用明文' }
}

export interface CertSummary {
  title: string
  detail: string
  /** 明文连接需要醒目地提示风险。 */
  risky: boolean
}

/** 证书方式的一行结论（折叠状态下显示）。 */
export const CERT_SUMMARY: Record<CertMode, CertSummary> = {
  pinned: { title: '内置 HTTPS · 按指纹校验', detail: '自签名证书也能安全连接：Agent 只信任这张证书的指纹，指纹不符就拒绝。', risky: false },
  trusted: { title: '受信任证书', detail: '服务端证书由公共机构签发（域名 + 反向代理的常见部署），目标主机直接按系统信任库校验。', risky: false },
  insecure: { title: '明文 HTTP', detail: '不加密，也不校验服务端身份，只应在你完全控制的内网中使用。', risky: true },
}
