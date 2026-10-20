// 主机类型相关的纯逻辑（便于单元测试）：区分 Agent 主机与 SSH 主机，并生成 SSH 主机的展示文案与表单校验。
//
// SSH 主机有两条接入路径，管理方式与权限分配完全一致：都有人工终端（网页上的人不受限制），模型在授权后也都能执行命令，
// 都没有文件传输。区别只在「由谁去连、终端会话放在哪」：
//   Hub 直连——由 Hub 出站连接。登录凭据加密保存在 Hub（需要服务端配置 RELAYMESH_SSH_SECRET_KEY）；终端会话由 Hub 持有，
//             不做记录，Hub 重启（以及停用 / 删除主机、修改连接信息）时会结束；没有主机本机的一票否决（那是 Agent 的能力）。
//   经网关——由一台装了 Agent 的主机出站连接（网关能力随 Agent 默认开启；目标不必能被 Hub 访问）。登录凭据只保存在网关自己的
//             加密存储里，Hub 没有副本，也就不需要 SSH 主密钥；终端会话由网关上的会话守护进程持有，Hub 重启后仍在，并有终端记录
//             （无痕会话除外）；网关本机用 --deny-model-exec 可以一票否决模型，用 --ssh-allow 限定能连到哪里。
// 凭据永远不会从服务端回显：界面上只能看到「凭据在哪里、是否还在」。

import type { HostView, SshAuthType } from '../../api/terminal'
import { privilegeLabel, relativeTime, shortCode } from './format'
import { idleSummary } from './lifecycle'

type KindOnly = { kind?: string } | null | undefined
type SshRef = { kind?: string; ssh?: { gateway?: string; term_only?: boolean } | null } | null | undefined

/** SSH 主机对话框的三种用法：接入 / 修改连接信息 / 密钥不一致后重新确认。 */
export type SshDialogMode = 'add' | 'edit' | 'rekey'

export function isSshHost(h: KindOnly): boolean {
  return h?.kind === 'ssh'
}

/** SSH 主机是经网关接入的（由网关主机出站连接、持有会话与凭据）；Hub 直连的 SSH 主机返回 false。 */
export function isGatewayHost(h: SshRef): boolean {
  return isSshHost(h) && !!h?.ssh?.gateway
}

/**
 * 这台主机能不能给模型用（出现在模型授权矩阵与令牌的主机名单里）。
 * Agent 主机与 Hub 直连的 SSH 主机可以；经网关的 SSH 主机也可以，除非登记时探测不出 POSIX 环境（term_only）——那种只能当人工终端用。
 */
export function supportsModel(h: SshRef): boolean {
  return !(isGatewayHost(h) && !!h?.ssh?.term_only)
}

/** 本机 shell 的人工终端、终端记录与文件传输只有装了 Agent 的主机才有；SSH 主机的终端见 canOpenSshTerminal。 */
export function canHumanTerminal(h: KindOnly): boolean {
  return !isSshHost(h)
}

/**
 * 这台 SSH 主机此刻能不能打开人工终端：已启用、密钥没有不一致，并且连接条件具备——
 *   Hub 直连：服务端有可用的 SSH 主密钥（backend_enabled），并且保存着登录凭据（has_secret）；
 *   经网关：网关就绪，并且凭据还在网关上（凭据在网关自己的存储里，Hub 既没有副本也不需要 SSH 主密钥）。
 * 不看「是否在线」：在线只是最近一次探测的结果，真正打开时会现场连接，连不上会给出明确原因。
 */
export function canOpenSshTerminal(h: { kind?: string; status?: string; ssh?: HostView['ssh'] | null } | null | undefined): boolean {
  if (!isSshHost(h)) return false
  const v = h?.ssh
  if (!v) return false
  if (h?.status !== 'enabled' || v.key_state === 'mismatch') return false
  if (v.gateway) return !v.cred_missing && v.gateway_ready === true
  return v.backend_enabled === true && v.has_secret === true
}

/**
 * 这台 SSH 主机的终端会话会不会被记录（也就谈得上「无痕」）：只有经网关的会话由网关上的守护进程持有并记录；
 * Hub 直连的会话由 Hub 持有，本来就不做记录，界面上不提供无痕入口。
 */
export function sshTerminalRecorded(h: SshRef): boolean {
  return isGatewayHost(h)
}

type GatewayCandidate = Pick<HostView, 'kind' | 'status' | 'online' | 'holder' | 'temporary'>

/**
 * 可以充当 SSH 网关的主机：已启用、在线、非临时、会话守护进程就绪，并且网关能力在线（Agent 默认开启，--no-ssh-gateway 才会关闭）。
 * 没有符合条件的主机时这个列表为空，界面只提供 Hub 直连。
 */
export function gatewayHosts<T extends GatewayCandidate>(hosts: readonly T[]): T[] {
  return hosts.filter(
    h => !isSshHost(h) && h.status === 'enabled' && h.online && !h.temporary && h.holder?.state === 'ready' && !!h.holder.caps?.includes('ssh'),
  )
}

/** SSH 主机卡片上的类型徽标：两条路径的管理方式与权限完全一致，所以只在文字上点明「网关」。 */
export function sshKindBadge(h: SshRef): { text: string; title: string } {
  if (isGatewayHost(h)) {
    return {
      text: 'SSH · 网关',
      title: h?.ssh?.term_only
        ? '经网关主机接入：由网关出站连接目标，登录凭据只保存在网关上。这台主机探测不出 POSIX 环境，只能当人工终端用，不支持模型执行'
        : '经网关主机接入：由网关出站连接目标，登录凭据只保存在网关上；终端会话由网关持有并记录。有人工终端，模型获得授权后也可以执行命令；没有文件传输',
    }
  }
  return {
    text: 'SSH',
    title: '由 Hub 直连：登录凭据加密保存在 Hub；终端会话由 Hub 持有，不做记录，Hub 重启时会结束。有人工终端，模型获得授权后也可以执行命令；没有文件传输',
  }
}

/** `用户@地址` 形式的连接摘要；没有用户名时只显示地址。 */
export function sshEndpoint(v?: { user?: string; address?: string } | null): string {
  if (!v?.address) return ''
  return v.user ? `${v.user}@${v.address}` : v.address
}

/** 列表里的紧凑指纹（`SHA256:前 10 位…后 6 位`）；核对时必须看完整指纹，所以对话框里不用它。 */
export function shortFingerprint(fp?: string): string {
  const s = (fp ?? '').trim()
  if (!s) return ''
  const m = /^(SHA256:)(.+)$/.exec(s)
  if (!m) return s.length > 20 ? `${s.slice(0, 12)}…${s.slice(-6)}` : s
  return m[2].length > 20 ? `${m[1]}${m[2].slice(0, 10)}…${m[2].slice(-6)}` : s
}

export interface SshBadge {
  text: string
  tone: 'ok' | 'warn' | 'bad' | 'muted'
  title: string
}

/**
 * SSH 主机卡片上的状态徽标；优先级：后端未启用（仅 Hub 直连）> 密钥不一致 > 网关不可用 > 凭据缺失（仅经网关）> 可连接 > 连接失败。
 * 经网关的主机凭据在网关上，不依赖 Hub 的 SSH 主密钥，所以「后端未启用」不适用于它们。
 */
export function sshBadge(h: Pick<HostView, 'online' | 'ssh'>): SshBadge {
  const v = h.ssh
  if (!v) return { text: 'SSH', tone: 'muted', title: '' }
  if (!v.gateway && !v.backend_enabled) {
    return { text: '后端未启用', tone: 'bad', title: '服务端没有配置有效的 RELAYMESH_SSH_SECRET_KEY，Hub 直连的 SSH 主机暂时不可用（经网关接入的主机不受影响）。' }
  }
  if (v.key_state === 'mismatch') {
    return { text: '主机密钥不一致', tone: 'bad', title: '这台主机出示的密钥与已固定的不一致：已停止连接，不会向它发送任何凭据。核对指纹后才能重新启用。' }
  }
  // 经网关的主机：网关本身不可用时，目标主机是否可达无从判断，别把责任算到目标头上。
  if (v.gateway && v.gateway_ready !== true) {
    return {
      text: '网关不可用',
      tone: 'warn',
      title: '经由的网关主机当前不在线、没有开启 SSH 网关，或会话守护进程不可用。网关恢复后会自动重新检测。',
    }
  }
  if (v.gateway && v.cred_missing) {
    return {
      text: '凭据缺失',
      tone: 'warn',
      title: '网关上已经没有这台主机的登录凭据（凭据只保存在网关自己的存储里，例如网关换了数据目录）。请在「修改连接」里重新填写。',
    }
  }
  if (h.online) return { text: '可连接', tone: 'ok', title: '' }
  return { text: v.error ? '连接失败' : '待检测', tone: 'warn', title: v.error ?? '' }
}

export interface SshFormInput {
  address: string
  user: string
  auth: SshAuthType
  password: string
  privateKey: string
}

const USER_RE = /^[A-Za-z0-9._$-]{1,64}$/
const MAX_PRIVATE_KEY = 16 << 10

/** 表单校验：返回第一条问题的说明，没有问题返回空串。服务端会再校验一遍，这里只为尽早提示。 */
export function validateSshForm(f: SshFormInput, opts: { secretRequired?: boolean } = {}): string {
  const addr = f.address.trim()
  if (!addr) return '请填写主机地址（IP 或域名，可带端口，例如 10.0.0.5:2222）'
  if (/[\s/@\\?#%]/.test(addr)) return '主机地址只能是「主机名 / IP」或「主机:端口」，不要带用户名或路径'
  if (!USER_RE.test(f.user.trim())) return '用户名无效（只允许字母、数字和 - _ . $，最长 64 个字符）'
  const secretRequired = opts.secretRequired ?? true
  if (f.auth === 'key') {
    if (secretRequired && !f.privateKey.trim()) return '请粘贴私钥（PEM / OpenSSH 格式）'
    if (new TextEncoder().encode(f.privateKey).length > MAX_PRIVATE_KEY) return '私钥过大（上限 16 KiB）'
  } else if (secretRequired && !f.password) {
    return '请填写登录口令'
  }
  return ''
}

/** 指纹里允许出现的字符：用来在输入框里粘贴核对值时去掉首尾空白，其余原样交给服务端比对。 */
export function normalizeFingerprint(s: string): string {
  return s.trim()
}

/** 两个指纹是否一致（忽略首尾空白；SHA256 指纹区分大小写，所以不做大小写折叠）。 */
export function sameFingerprint(a?: string, b?: string): boolean {
  const x = normalizeFingerprint(a ?? '')
  return x !== '' && x === normalizeFingerprint(b ?? '')
}

/** 目标主机上保存对应类型主机公钥的文件：核对指纹时用 `ssh-keygen -lf <文件>`。未知类型返回空串。 */
export function hostKeyFile(keyType?: string): string {
  const t = (keyType ?? '').toLowerCase()
  if (t === 'ssh-ed25519') return '/etc/ssh/ssh_host_ed25519_key.pub'
  if (t === 'ssh-rsa' || t.startsWith('rsa-sha2-')) return '/etc/ssh/ssh_host_rsa_key.pub'
  if (t.startsWith('ecdsa-sha2-')) return '/etc/ssh/ssh_host_ecdsa_key.pub'
  return ''
}

// ---------- 主机卡片的收起摘要 ----------
// 主机页的卡片默认收起，只剩名字、徽标和一行摘要；摘要要让人不展开就能回答三个问题：
//   这台主机现在是什么状态（在线 / 离线、有几个会话在跑）？
//   关键设置是什么（空闲回收、终端记录、模型访问）？
//   有没有需要处理的问题（会话丢失、凭据不可用、Agent 不支持……）？
// 展开后看到的才是完整设置，所以这里只放「值」，不放解释；解释留在展开后的说明文字里。

export type SummaryTone = 'muted' | 'ok' | 'warn' | 'bad'

/**
 * 「同名」的说明。名字以界面上显示的为准——备注，没有备注才是主机名（见 format.ts 的 hostTitle）——模型在主机列表里看到的 name 也是它，
 * 所以两台主机显示的名字相同就算同名：此时模型无法靠名字区分，必须使用机器码。
 */
export const DUPLICATE_NAME_HINT = '与另一台主机显示的名字相同（备注优先，没有备注按主机名）：请以机器码为准，模型也只能用机器码指定它'

export interface SummaryPart {
  text: string
  tone: SummaryTone
  /** 悬停时的完整说明（可选）。 */
  title?: string
  /** 需要逐字符核对的内容（地址、机器码）用等宽字体。 */
  mono?: boolean
}

type SummarySource = Partial<
  Pick<
    HostView,
    | 'machine_code' | 'kind' | 'online' | 'temporary' | 'os' | 'arch' | 'privilege' | 'last_seen_at'
    | 'rec_off' | 'rec_server_off' | 'idle_sec' | 'allow_model' | 'exec_mode' | 'model_paused'
    | 'model_running' | 'sessions' | 'holder' | 'ssh' | 'duplicate_name'
  >
>

export interface HostSummaryOptions {
  /** 服务端是否启用了「模型执行」；没启用时界面上没有模型相关的开关，摘要里也不出现。 */
  modelEnabled: boolean
  /** 当前时间（毫秒），仅用于测试时固定「几分钟前」。 */
  nowMs?: number
  /** 把网关主机的机器码换成界面上的名字；缺省时显示缩写的机器码。 */
  gatewayTitle?: (code: string) => string
}

const EXEC_MODE_LABEL: Record<string, string> = { '': '自动', auto: '自动', approve: '需批准', readonly: '只读' }

function modelParts(h: SummarySource): SummaryPart[] {
  // 临时启用的主机重启即消失，不支持模型访问（展开后有同样的说明）。
  if (h.temporary) return []
  // 经网关接入、登记时探测不出 POSIX 环境的主机：模型用不了，直接说明，不展示任何模型开关的状态。
  if (!supportsModel(h)) {
    return [{ text: '仅人工终端', tone: 'muted', title: '登记时探测不出 POSIX 环境（需要 sh 与 base64），只能当人工终端用，不支持模型执行。' }]
  }
  if (h.holder?.deny_exec) {
    return [{ text: '本机已一票否决模型', tone: 'warn', title: '这台主机用 --deny-model-exec 启动：本机一票否决，服务端无法更改。' }]
  }
  if (h.ssh?.gateway_denied) {
    return [{ text: '网关已一票否决模型', tone: 'warn', title: '承载这台主机的网关用 --deny-model-exec 启动：网关本机一票否决，服务端无法更改。' }]
  }
  if (!h.allow_model) return [{ text: '不允许模型使用', tone: 'muted' }]

  const out: SummaryPart[] = []
  if (h.model_paused) {
    out.push({ text: '模型已暂停', tone: 'warn' })
  } else {
    const mode = h.exec_mode ?? ''
    out.push({ text: `模型可用（${EXEC_MODE_LABEL[mode] ?? mode}）`, tone: 'ok' })
  }
  const running = h.model_running ?? 0
  if (running > 0) out.push({ text: `${running} 条模型命令进行中`, tone: 'ok' })
  // 只有装了 Agent 的主机才有「Agent 版本不支持模型执行」这回事。
  if (!isSshHost(h) && h.online && h.holder && !h.holder.exec) {
    out.push({ text: 'Agent 不支持模型执行', tone: 'warn', title: '这台主机的 Agent 版本不支持模型执行，请升级。' })
  }
  return out
}

function sshParts(h: SummarySource, opts: HostSummaryOptions): SummaryPart[] {
  const out: SummaryPart[] = []
  const endpoint = sshEndpoint(h.ssh)
  if (endpoint) out.push({ text: endpoint, tone: 'muted', mono: true })
  if (h.ssh) out.push({ text: h.ssh.auth === 'key' ? '私钥认证' : '口令认证', tone: 'muted' })
  const platform = [h.os, h.arch].filter(Boolean).join(' · ')
  if (platform) out.push({ text: platform, tone: 'muted' })
  // 密钥不一致 / 连接失败 / 网关不可用已经由卡片标题行上的状态徽标（sshBadge）常驻显示，这里只补那条徽标不会说的：凭据是否可用。
  // Hub 直连的凭据在 Hub 上（has_secret）；经网关的凭据在网关上，Hub 没有副本，看的是 cred_missing。
  if (isGatewayHost(h)) {
    const gw = h.ssh?.gateway ?? ''
    out.push({
      text: `经网关 ${opts.gatewayTitle?.(gw) ?? shortCode(gw)}`,
      tone: 'muted',
      title: '由这台网关主机出站连接目标，登录凭据只保存在网关上；人工终端与模型执行都经网关',
    })
    if (h.ssh?.cred_missing) out.push({ text: '凭据缺失，需重新填写', tone: 'warn', title: '网关上已经没有这台主机的登录凭据，请在「修改连接」里重新填写。' })
  } else {
    out.push({
      text: 'Hub 直连',
      tone: 'muted',
      title: '由 Hub 出站连接目标；登录凭据加密保存在 Hub，终端会话由 Hub 持有（不记录，Hub 重启时结束）',
    })
    // 经网关的会话列在网关主机名下；直连的会话就在这台主机自己名下。
    const running = (h.sessions ?? []).filter(s => s.state === 'running').length
    if (running > 0) out.push({ text: `${running} 个会话运行中`, tone: 'ok' })
    if (h.ssh && !h.ssh.has_secret) out.push({ text: '凭据不可用，需重新填写', tone: 'warn' })
  }
  if (opts.modelEnabled) out.push(...modelParts(h))
  return out
}

function agentParts(h: SummarySource, opts: HostSummaryOptions): SummaryPart[] {
  const out: SummaryPart[] = []
  // 同名主机只能靠机器码区分：放在最前面，免得收起后分不清是哪一台。
  if (h.duplicate_name && h.machine_code) {
    out.push({ text: shortCode(h.machine_code), tone: 'warn', mono: true, title: DUPLICATE_NAME_HINT })
  }
  if (h.online) {
    const running = (h.sessions ?? []).filter(s => s.state === 'running').length
    if (running > 0) out.push({ text: `${running} 个会话运行中`, tone: 'ok' })
  } else {
    out.push({ text: h.last_seen_at ? `离线，最后在线 ${relativeTime(h.last_seen_at, opts.nowMs)}` : '离线', tone: 'muted' })
  }
  const platform = [h.os, h.arch].filter(Boolean).join(' · ')
  if (platform) out.push({ text: platform, tone: 'muted' })
  if (h.privilege) out.push({ text: privilegeLabel(h.privilege).text, tone: 'muted' })
  out.push({ text: idleSummary(h.idle_sec ?? 0), tone: 'muted', title: '没有单独设置的会话，没人使用时怎么处理（默认生命周期）' })
  out.push({
    text: h.rec_server_off ? '终端记录被服务端关闭' : h.rec_off ? '终端记录已关闭' : '终端记录开启',
    tone: 'muted',
  })
  const lost = h.holder?.lost?.length ?? 0
  if (lost > 0) out.push({ text: `${lost} 个会话已不存在`, tone: 'warn', title: '展开后可以看到原因，并在原目录重建' })
  if (opts.modelEnabled) out.push(...modelParts(h))
  return out
}

/** 收起状态下的一行摘要：按重要程度排好的若干片段（界面用「·」连接，各片段按语气着色）。 */
export function hostSummary(h: SummarySource, opts: HostSummaryOptions): SummaryPart[] {
  return isSshHost(h) ? sshParts(h, opts) : agentParts(h, opts)
}

/** 摘要的纯文本形式（用于无障碍标签与测试）。 */
export function summaryText(parts: SummaryPart[]): string {
  return parts.map(p => p.text).join(' · ')
}
