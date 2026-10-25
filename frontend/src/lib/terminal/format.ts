// 终端页面用到的展示格式化（纯函数，便于单元测试）。

import { HOLDER_RESTART_TIP } from './holderRestart'

export function hostTitle(h: { remark?: string; hostname?: string; machine_code: string }): string {
  return (h.remark || '').trim() || (h.hostname || '').trim() || h.machine_code
}

/** 列表里的紧凑机器码：RM-7K2F-…-XXXX；格式不符原样返回。 */
export function shortCode(code: string): string {
  const m = /^(RM-[0-9A-Z]{4})(?:-[0-9A-Z]{4})+-([0-9A-Z]{4})$/.exec(code)
  return m ? `${m[1]}-…-${m[2]}` : code
}

export function privilegeLabel(p: string): { text: string; elevated: boolean } {
  switch (p) {
    case 'root':
      return { text: 'root 最高权限', elevated: true }
    case 'admin':
      return { text: '管理员（可提权）', elevated: true }
    case 'user':
      return { text: '普通用户', elevated: false }
    default:
      return { text: p || '未知', elevated: false }
  }
}

/** 会话显示名：用户起的名字优先，否则用「会话 + 序号后缀」。 */
export function sessionTitle(s: { sid: string; name?: string }): string {
  const n = (s.name || '').trim()
  return n || `会话 ${s.sid.slice(0, 6)}`
}

export function relativeTime(iso: string | undefined, nowMs: number = Date.now()): string {
  if (!iso) return '—'
  const t = Date.parse(iso)
  if (!Number.isFinite(t)) return '—'
  const sec = Math.round((nowMs - t) / 1000)
  if (sec < 10) return '刚刚'
  if (sec < 60) return `${sec} 秒前`
  const min = Math.floor(sec / 60)
  if (min < 60) return `${min} 分钟前`
  const hour = Math.floor(min / 60)
  if (hour < 24) return `${hour} 小时前`
  const day = Math.floor(hour / 24)
  if (day < 30) return `${day} 天前`
  return new Date(t).toLocaleDateString('zh-CN')
}

/**
 * 运行时长上限（秒）的口语写法（#141）：整天「7 天」、整小时「2 小时」、整分钟「30 分钟」，其余「1 分 30 秒」「45 秒」。
 * 不取整（不把时限说宽）；不是正数时返回空串，调用方据此省略。与服务端 terminal.runLimitText 一致。
 */
export function runLimitText(sec: number | undefined): string {
  if (typeof sec !== 'number' || !Number.isFinite(sec) || sec <= 0) return ''
  const s = Math.floor(sec)
  if (s <= 0) return ''
  if (s % 86400 === 0) return `${s / 86400} 天`
  if (s % 3600 === 0) return `${s / 3600} 小时`
  if (s % 60 === 0) return `${s / 60} 分钟`
  if (s < 60) return `${s} 秒`
  return `${Math.floor(s / 60)} 分 ${s % 60} 秒`
}

/** 倒计时：不足一小时用 m:ss，否则 h:mm:ss。 */
export function formatCountdown(totalSeconds: number): string {
  const s = Math.max(0, Math.floor(totalSeconds))
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  const sec = s % 60
  const pad = (n: number) => String(n).padStart(2, '0')
  return h > 0 ? `${h}:${pad(m)}:${pad(sec)}` : `${m}:${pad(sec)}`
}

const EVENT_LABELS: Record<string, string> = {
  gate_denied: '总闸关闭时的访问被拒',
  gate_changed: '总闸状态变化',
  unlock_ok: '解锁成功',
  unlock_fail: '解锁失败（密码错误）',
  unlock_locked: '解锁尝试过多，已临时锁定',
  lock: '主动上锁',
  discovery_open: '打开发现窗口',
  discovery_close: '关闭发现窗口',
  upgrade_window: '主机的 Agent 升级窗口（开启 / 延时 / 结束）',
  agent_web_upgrade: '在 Web 上一键升级主机的 Agent（发起 / 交接 / 完成 / 失败）',
  agent_auto_upgrade_switch: 'Agent 自动升级总闸（开启 / 关闭）',
  agent_auto_upgrade_start: '服务端批准了主机的静默自动升级请求（主机自己发起）',
  agent_auto_upgrade_ok: '主机的静默自动升级完成（新版本已连上）',
  agent_auto_upgrade_fail: '主机的静默自动升级失败（含发布签名验证失败）',
  agent_auto_upgrade_rollback: '静默自动升级后新版本没连上服务端，已自动还原为旧版本',
  host_pending: '出现待启用主机',
  host_enabled: '启用主机',
  host_ignored: '忽略主机',
  host_unignored: '取消忽略',
  host_disabled: '停用主机',
  host_deleted: '删除主机',
  host_remark: '修改备注',
  agent_connected: '主机上线',
  agent_handshake_bad: 'Agent 握手失败',
  session_open: '新建会话',
  session_kill: '终止会话',
  session_attach: '连接会话',
  session_detach: '断开会话',
  holder_state: '主机上的会话守护进程状态变化',
  sessions_lost: '有会话在主机重启后已不存在',
  record_read: '查看会话记录',
  record_download: '下载会话记录',
  record_policy: '修改终端记录策略',
  lifecycle_changed: '修改会话生命周期',
  launcher_save: '新建或修改终端启动预设',
  launcher_delete: '删除终端启动预设',
  launcher_run: '运行「非交互续聊」预设（只记预设与权限姿态，不记提示词与命令）',
  launcher_auto_run: '系统按「异常退出时自动续跑」运行了预设（只记预设与第几次，不记提示词与命令）',
  launcher_auto_stop: '取消了一次排期中的自动续跑（只记预设）',
  launcher_switch: '「新会话」运行成功之后，系统把预设改成了「最近会话」（只记预设，不记提示词与命令）',
  launcher_agent_run: '项目管理 Agent 通过 MCP 运行了预设（只记谁、预设与目标工作流，不记提示词与命令；被拒绝的尝试也会记录）',
  session_run: '在会话页运行「非交互续聊」（只记工作流与权限姿态，不记提示词与命令）',
  session_enable: '在会话页「启用 Agent」：新开对话的非交互运行（只记工作流、预设与权限姿态，不记提示词与命令）',
  guard_wake: '唤醒终端预设里中断的会话（功能已下线，只剩旧记录；只记预设与方式，不记提示词内容）',
  guard_permit: '标记或取消「任务全部完成」（功能已下线，只剩旧记录；不记备注）',
  oauth_consent_granted: '批准 OAuth 应用访问 ExecMesh',
  oauth_consent_denied: '拒绝 OAuth 应用授权',
  oauth_token_issued: 'OAuth 访问令牌签发',
  oauth_token_refresh: 'OAuth 刷新令牌轮换',
  oauth_refresh_replay: 'OAuth 刷新令牌重放（整条授权已作废）',
  oauth_code_replay: 'OAuth 授权码被重复兑换（整条授权已作废）',
  oauth_revoke: '撤销 OAuth 授权',
  oauth_client_register: '动态注册 OAuth 客户端',
  oauth_policy_changed: '修改 OAuth 策略（总开关 / 对外地址 / 自动注册方式；只记开关状态）',
  oauth_window_open: '开启 / 续期 OAuth 接入窗口（窗口期间客户端才能自动注册；只记到期时间）',
  oauth_window_close: '关闭 OAuth 接入窗口',
  oauth_client_delete: '删除一条还没授权的 OAuth 客户端登记（只记客户端名）',
  credentials_changed: '登录凭据变更（全部终端凭据已撤销）',
  credential_reveal: '查看 MCP 令牌（需再次验证 Web 密码）',
  exec_start: '模型发起命令',
  exec_denied: '模型的命令被拒绝',
  exec_cancel: '取消模型命令',
  exec_approve: '批准模型命令',
  exec_reject: '拒绝模型命令（或批准超时）',
  exec_pause: '暂停模型执行',
  exec_resume: '恢复模型执行',
  host_model: '修改主机的模型访问设置',
  model_host_grant: '把主机写入模型令牌的授权名单',
  run_lost: '模型命令的结果无法确认',
  exec_output_read: '查看模型命令的完整输出',
  file_op: '模型查看文件（元数据 / 校验值）',
  file_pull: '模型取得文件的一次性下载地址',
  file_download: '模型下载文件',
  file_push: '模型让主机下载网址内容并写入文件',
  files_scope: '开启 / 关闭令牌的「允许整个文件系统」（或因没有刚刚重新验证密码而被拒）',
  ssh_probe: '探测 SSH 主机的主机密钥',
  ssh_host_add: '接入 SSH 主机',
  ssh_host_test: '测试 SSH 主机连接',
  ssh_host_conn: '修改 SSH 主机的连接信息',
  ssh_host_rekey: '重新固定 SSH 主机的主机密钥',
  ssh_cred_sync: '与网关核对它保存的 SSH 登录凭据（清理已不存在的主机）',
  install_serve: '服务端分发 Agent 安装文件（或拒绝了错误的安装票据）',
}

export function auditEventLabel(event: string): string {
  return EVENT_LABELS[event] ?? event
}

export function resultLabel(result: string): { text: string; tone: 'ok' | 'bad' | 'warn' | 'none' } {
  switch (result) {
    case 'fail':
      return { text: '失败', tone: 'bad' }
    case 'deny':
      return { text: '拒绝', tone: 'warn' }
    case 'ok':
      return { text: '成功', tone: 'ok' }
    default:
      return { text: '', tone: 'none' }
  }
}

/** 总闸关闭原因 → 给管理员的说明与处理办法（不含任何密钥内容）。 */
export function gateReasonInfo(reason: string): { title: string; body: string } {
  switch (reason) {
    case 'no_password':
      return {
        title: '终端需要先设置 Web 登录密码',
        body: '终端能直接在主机上执行命令，未设置登录密码时任何能访问本服务的人都等同于管理员，因此必须保持关闭。请先在「设置 → MCP 与安全」中设置登录账号与密码。',
      }
    case 'weak_jwt_secret':
      return {
        title: '登录令牌的签名密钥强度不足',
        body: '终端要求 RELAYMESH_JWT_SECRET 是足够长的随机值（默认值、过短或低熵的值会被拒绝），否则登录令牌可被伪造。请用环境变量配置一个随机密钥后重启服务。',
      }
    case 'disabled_by_env':
      return {
        title: '终端功能已被运维关闭',
        body: '服务端通过环境变量 RELAYMESH_TERMINAL=off 强制关闭了终端。如需启用，请移除该变量（或设为 auto）后重启服务。',
      }
    default:
      return {
        title: '暂时无法确认终端的启用条件',
        body: '服务端读取登录凭据时出错，出于安全考虑按关闭处理。请查看服务端日志后重试。',
      }
  }
}

/** 会话守护进程不可用的原因（对应 HolderView.reason）。 */
export function holderReasonLabel(reason?: string): string {
  switch (reason) {
    case 'connecting':
      return '正在连接守护进程'
    case 'superseded':
      return '守护进程已被另一个 Agent 接管'
    case 'version':
      return 'Agent 与守护进程的本地协议版本不兼容，请把两者升级到同一版本'
    case 'unreachable':
      return '守护进程没有运行或无法连接'
    default:
      return '守护进程不可用'
  }
}

/** 会话丢失的原因（对应 LostSession.reason）。 */
export function lostReasonLabel(reason?: string): string {
  switch (reason) {
    case 'host_reboot':
      return '主机重启'
    case 'holder_stopped':
      return '守护进程被停止'
    case 'holder_exit':
      return '守护进程意外退出'
    default:
      return reason || '原因未知'
  }
}

export interface HolderBadge {
  text: string
  /** ok：重启不中断（会话由守护进程持有）；warn：重启会中断（会话随 Agent 结束）；bad：守护进程当前不可用 */
  tone: 'ok' | 'warn' | 'bad'
  title: string
}

/**
 * 主机行上的「Agent 重启会不会中断会话」标注（主机能力，只读）。离线主机没有可信的实时状态，不标注。
 * 与会话的「空闲不回收 / 空闲 N 回收」（lifecycle.ts，会话策略，可改）是两件事：这里说的是升级 / 重启 / 崩溃时会话还在不在。
 */
export function holderBadge(h: {
  online: boolean
  kind?: string
  ssh?: { gateway?: string }
  holder?: { state: string; persistent: boolean; reason?: string }
}): HolderBadge | null {
  if (!h.online) return null
  if (h.kind === 'ssh') {
    // 经网关的 SSH 主机自己不持有会话（会话在网关 Agent 上，网关那一行有自己的标注）。
    if (h.ssh?.gateway) return null
    // Hub 直连的 SSH 主机：会话由 Hub 持有，说的是 Hub 而不是“旧版 Agent”。
    return {
      text: 'Hub 重启会中断',
      tone: 'warn',
      title: 'Hub 直连的 SSH 主机：终端会话由 Hub 持有（不记录），Hub 重启、停用或删除这台主机都会结束全部会话。需要重启也不中断，请经网关 Agent 接入这台主机',
    }
  }
  const v = h.holder
  if (!v) {
    return { text: '重启会中断', tone: 'warn', title: '旧版 Agent：会话与 Agent 同进程，Agent 重启会结束全部会话' }
  }
  if (v.state !== 'ready') {
    return { text: '守护进程不可用', tone: 'bad', title: holderReasonLabel(v.reason) + '；新建会话暂不可用，已有会话仍在主机上运行（若守护进程还活着）' }
  }
  if (v.persistent) {
    return { text: '重启不中断', tone: 'ok', title: '会话由独立的守护进程持有：升级 / 重启 / 崩溃的 Agent 都不会中断正在运行的命令。这与「空闲不回收」无关——后者决定没人用时会话要不要被收走' }
  }
  return { text: '重启会中断', tone: 'warn', title: '会话与 Agent 同进程：Agent 重启会结束全部会话。安装会话守护进程（install 默认会装）即可让会话在 Agent 重启后保留' }
}

/** 守护进程声明「新建会话时把字符类别钉在 LC_CTYPE」的能力位（与 internal/agent/holder 的 CapCharset 一致）。 */
export const HOLDER_CAP_CHARSET = 'charset'

/** Agent 侧代码指纹的写法：小写十六进制 6–40 位（与服务端 termproto.ValidAgentRev 一致）；其他一律当作没有。 */
const AGENT_REV_RE = /^[0-9a-f]{6,40}$/

/**
 * 比较两个「Agent 侧代码指纹」：same = 两边都有且一致；differs = 两边都有且不同；unknown = 任何一边没有（旧版本、非发布构建）。
 * 指纹跟着 Agent 依赖的代码走，版本号则跟着整个仓库的构建走——Agent 升级后版本号几乎总是不同，指纹一致才说明代码真的没变。
 */
export function compareAgentRev(a?: string, b?: string): 'same' | 'differs' | 'unknown' {
  if (!a || !b || !AGENT_REV_RE.test(a) || !AGENT_REV_RE.test(b)) return 'unknown'
  return a === b ? 'same' : 'differs'
}

/**
 * 守护进程较旧的提醒：会话是守护进程创建的，而升级只替换 Agent、不会重启守护进程，
 * 所以守护进程里的改动（例如与「新建会话」有关的修复）要等它重启后才生效。
 *
 * 判断依据按精确程度依次是：
 * 1. 主机 Agent 与守护进程都带着「Agent 侧代码指纹」（agent_rev / holder.rev）：一致 = 守护进程跑的就是现在这份 Agent 代码，不提醒；
 *    不同 = 确实是旧一版，提醒。这一条不看版本号（Agent 升级后版本号总是不同，哪怕守护进程的代码一模一样），也不看系统。
 * 2. 任何一边没有指纹（旧版本）：回退到能力位——守护进程每次启动都会声明自己的能力，缺少某项修复所需的能力位，
 *    就说明它是在那项修复之前启动的（目前只有 charset 这一项，所以只对 Linux / macOS 主机提示：Windows 不涉及 locale）。
 *
 * 只对「有守护进程且就绪」的主机提示：旧版 Agent（没有守护进程）与守护进程不可用另有标注；SSH 主机没有守护进程。
 */
export function holderStaleBadge(h: {
  online: boolean
  os?: string
  kind?: string
  agent_rev?: string
  holder?: { state: string; caps?: string[]; rev?: string }
}): { text: string; title: string } | null {
  if (!h.online || h.kind === 'ssh') return null
  const v = h.holder
  if (!v || v.state !== 'ready') return null
  const rev = compareAgentRev(h.agent_rev, v.rev)
  if (rev === 'same') return null
  if (rev === 'differs') {
    return {
      text: '守护进程较旧',
      title:
        `这台主机的会话守护进程运行的是旧一版的 Agent 代码（守护进程指纹 ${v.rev}，当前 Agent 指纹 ${h.agent_rev}）：`
        + '升级只替换 Agent，不会重启守护进程，守护进程里的改动要重启它才会生效。'
        + '在主机上执行 relaymesh-agent holder restart 即可（会结束这台主机上现有的终端会话和模型正在运行的命令；加 --drain 则等它们自然结束后再重启）。'
        + HOLDER_RESTART_TIP
        + '没有重要会话时再做也不迟，这只是提醒。',
    }
  }
  if ((h.os || '').toLowerCase().includes('windows')) return null
  if (v.caps?.includes(HOLDER_CAP_CHARSET)) return null
  return {
    text: '守护进程较旧',
    title:
      '这台主机的会话守护进程是在一项修复之前启动的（升级只替换 Agent，不会重启守护进程）。'
      + '影响：在这里新建的终端会话里，如果登录脚本（例如 ~/.profile 里的 LANG=C）把 locale 改回了 C，shell 提示符下就无法输入中文。'
      + '在主机上执行 relaymesh-agent holder restart 即可（会结束这台主机上现有的终端会话和模型正在运行的命令；加 --drain 则等它们自然结束后再重启）。'
      + HOLDER_RESTART_TIP
      + '没有重要会话时再做也不迟，这只是提醒。',
  }
}

export interface HolderVersionNote {
  text: string
  title: string
  tone: 'muted' | 'warn'
}

/**
 * 展开的主机详情里「守护进程 <版本>」一行。守护进程只在手动 holder restart 时才换，所以它的版本常常与 Agent 不同——
 * 版本号不同本身说明不了什么，要看代码指纹：一致 = 代码一样（只是构建号不同），不同 = 守护进程确实较旧，
 * 没有指纹 = 旧版本、无法判断。版本号与 Agent 相同、没有守护进程、守护进程不可用或 SSH 主机时返回 null（没有多余信息）。
 */
export function holderVersionNote(h: {
  online: boolean
  kind?: string
  agent_version?: string
  agent_rev?: string
  holder?: { state: string; version?: string; rev?: string }
}): HolderVersionNote | null {
  if (!h.online || h.kind === 'ssh') return null
  const v = h.holder
  if (!v || v.state !== 'ready' || !v.version) return null
  if (v.version === h.agent_version) return null
  switch (compareAgentRev(h.agent_rev, v.rev)) {
    case 'same':
      return {
        text: `守护进程 ${v.version}（代码一致）`,
        tone: 'muted',
        title: `守护进程与当前 Agent 的 Agent 侧代码指纹相同（${v.rev}）：版本号不同只是构建号不同，代码完全一致，不需要重启守护进程。`,
      }
    case 'differs':
      return {
        text: `守护进程 ${v.version}（代码较旧）`,
        tone: 'warn',
        title: `守护进程的代码指纹是 ${v.rev}，当前 Agent 是 ${h.agent_rev}：守护进程跑的是旧一版的 Agent 代码，重启它（relaymesh-agent holder restart）才会换上新的。`,
      }
    default:
      return {
        text: `守护进程 ${v.version}`,
        tone: 'muted',
        title: '守护进程（或主机上的 Agent）是不带代码指纹的旧版本，无法判断它的代码与 Agent 是否一致。',
      }
  }
}

/** 守护进程报告的丢失会话汇总成一行文字；没有则返回空串。 */
export function lostSummary(lost?: { name?: string; sid: string; reason: string }[]): string {
  if (!lost || lost.length === 0) return ''
  const reasons = new Map<string, number>()
  for (const l of lost) reasons.set(l.reason, (reasons.get(l.reason) || 0) + 1)
  const parts = [...reasons.entries()].map(([r, n]) => `${n} 个因${lostReasonLabel(r)}`)
  return `有会话已不存在：${parts.join('、')}`
}
