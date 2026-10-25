// ntfy 推送设置的纯逻辑：表单模型、与服务端一致的前置校验、提交体的生成、随机主题、状态文案。
// 服务端（internal/ntfy）才是权威，这里的校验只是让用户在点保存之前就看到问题。

import type { NtfyConfig, NtfyKindGroup, NtfyKindInfo, NtfyStatus, NtfyUpdate } from '../api/ntfy'

export const NTFY_DEFAULT_SERVER = 'https://ntfy.sh'
export const NTFY_MAX_DELAY_SECONDS = 900
export const NTFY_DEFAULT_PRIORITY = 4
/** 运行结束推送的默认门槛与上限（与服务端 internal/ntfy 一致：1~86400 秒，0 视为默认）。 */
export const NTFY_DEFAULT_RUN_MIN_SECONDS = 60
export const NTFY_MAX_RUN_MIN_SECONDS = 86400

export type NtfyAuthMode = 'none' | 'token' | 'basic'

export const NTFY_AUTH_OPTIONS: ReadonlyArray<{ id: NtfyAuthMode; label: string; hint: string }> = [
  { id: 'none', label: '无认证', hint: '公共 ntfy.sh 的默认用法：主题名本身就是口令' },
  { id: 'token', label: '访问令牌', hint: '自建或付费账号：ntfy 的 tk_ 开头的访问令牌' },
  { id: 'basic', label: '用户名与密码', hint: '自建且开了访问控制的 ntfy' },
]

export const NTFY_PRIORITY_OPTIONS: ReadonlyArray<{ value: number; label: string }> = [
  { value: 1, label: '最低（1）' },
  { value: 2, label: '较低（2）' },
  { value: 3, label: '默认（3）' },
  { value: 4, label: '较高（4，推荐）' },
  { value: 5, label: '紧急（5）' },
]

export const NTFY_DELAY_OPTIONS: ReadonlyArray<{ value: number; label: string }> = [
  { value: 0, label: '立即推送' },
  { value: 30, label: '30 秒后仍未处理再推' },
  { value: 60, label: '1 分钟后仍未处理再推' },
  { value: 120, label: '2 分钟后仍未处理再推' },
  { value: 300, label: '5 分钟后仍未处理再推' },
]

/** 运行结束推送的门槛预设：短命令（查状态、跑小脚本）不打扰你，长任务才推。 */
export const NTFY_RUN_MIN_OPTIONS: ReadonlyArray<{ value: number; label: string }> = [
  { value: 30, label: '30 秒' },
  { value: 60, label: '1 分钟（推荐）' },
  { value: 300, label: '5 分钟' },
  { value: 600, label: '10 分钟' },
  { value: 1800, label: '30 分钟' },
  { value: 3600, label: '1 小时' },
]

/** 门槛的写法：整小时写小时、整分钟写分钟，其余写秒。 */
export function formatRunMinSeconds(n: number): string {
  if (n >= 3600 && n % 3600 === 0) return `${n / 3600} 小时`
  if (n >= 60 && n % 60 === 0) return `${n / 60} 分钟`
  return `${n} 秒`
}

/**
 * 下拉框的选项。当前值不在预设里（比如经接口设成了 1 秒）时补上一项显示它，
 * 否则 select 会悄悄显示成别的值，保存时又把它改掉。
 */
export function runMinOptions(current: number): Array<{ value: number; label: string }> {
  if (!Number.isFinite(current) || NTFY_RUN_MIN_OPTIONS.some(o => o.value === current)) return [...NTFY_RUN_MIN_OPTIONS]
  const extra = { value: current, label: `${formatRunMinSeconds(current)}（自定义）` }
  return [...NTFY_RUN_MIN_OPTIONS, extra].sort((a, b) => a.value - b.value)
}

/** 表单模型：凭据输入框只装「这次新输入的值」，已保存的凭据不会回填。 */
export interface NtfyForm {
  enabled: boolean
  server: string
  topic: string
  authMode: NtfyAuthMode
  token: string
  username: string
  password: string
  clickUrl: string
  priority: number
  includeSummary: boolean
  delaySeconds: number
  /** 模型发起的命令运行结束时也推送。 */
  runNotify: boolean
  /** 运行结束推送的门槛（秒）：更短的命令不推送。 */
  runMinSeconds: number
  /** 按消息类型的覆盖表：0 = 不推送，1~5 = 指定优先级；没出现的类型用内置默认。 */
  priorityByKind: Record<string, number>
}

/** 认证方式取决于已保存的状态：有令牌（含环境变量提供的）→ 令牌；有用户名 → 用户名密码；否则无。 */
export function authModeOf(c: NtfyConfig): NtfyAuthMode {
  if (c.token_set || c.token_from_env) return 'token'
  if (c.username || c.password_set) return 'basic'
  return 'none'
}

export function formFromConfig(c: NtfyConfig): NtfyForm {
  return {
    enabled: c.enabled,
    server: c.server || NTFY_DEFAULT_SERVER,
    topic: c.topic,
    authMode: authModeOf(c),
    token: '',
    username: c.username,
    password: '',
    clickUrl: c.click_url,
    priority: c.priority || NTFY_DEFAULT_PRIORITY,
    includeSummary: c.include_summary,
    delaySeconds: c.delay_seconds,
    // 服务端没给这两项（旧版本的响应）时按「关闭 + 默认门槛」处理
    runNotify: c.run_notify === true,
    runMinSeconds: c.run_min_seconds || NTFY_DEFAULT_RUN_MIN_SECONDS,
    // 旧版本的服务端没有这个字段：按空表（全部内置默认）处理；复制一份，表单里的修改不能碰到已保存的配置
    priorityByKind: { ...(c.priority_by_kind ?? {}) },
  }
}

/**
 * 把表单翻译成提交体。凭据的规则：
 *  - 选了某种认证方式 → 另一种认证方式的凭据一并清掉（令牌与用户名密码二选一，免得残留的旧凭据悄悄生效）；
 *  - 本方式的凭据输入框留空 → 不提交，服务端保持已保存的值；填了才替换。
 */
export function buildUpdate(form: NtfyForm, saved: NtfyConfig): NtfyUpdate {
  const out: NtfyUpdate = {
    enabled: form.enabled,
    server: form.server.trim(),
    topic: form.topic.trim(),
    username: '',
    click_url: form.clickUrl.trim(),
    priority: form.priority,
    include_summary: form.includeSummary,
    delay_seconds: form.delaySeconds,
    run_notify: form.runNotify,
    run_min_seconds: form.runMinSeconds,
    // 整张表一起提交（空表 = 清空，全部回到内置默认），与其它非凭据字段一样「整体替换」
    priority_by_kind: { ...form.priorityByKind },
  }
  switch (form.authMode) {
    case 'none':
      out.clear_token = true
      out.clear_password = true
      break
    case 'token':
      out.clear_password = true
      // 令牌由环境变量提供时，界面里没有令牌可填，也没有数据库里的令牌要保留
      if (form.token.trim()) out.token = form.token.trim()
      else if (saved.token_from_env) out.clear_token = true
      break
    case 'basic':
      out.clear_token = true
      out.username = form.username.trim()
      if (form.password) out.password = form.password
      break
  }
  return out
}

const TOPIC_RE = /^[A-Za-z0-9_-]{1,64}$/

/** 前置校验；返回第一条问题（给用户看的话），没有问题返回空串。requireTopic：启用或试发时主题必填。 */
export function validateForm(form: NtfyForm, saved: NtfyConfig, requireTopic: boolean): string {
  const server = form.server.trim()
  if (!/^https?:\/\//i.test(server)) return '服务地址要以 http:// 或 https:// 开头'
  if (/\s/.test(server)) return '服务地址里不能有空格'
  if (/^https?:\/\/[^/?#]*@/i.test(server)) return '服务地址里不要直接带用户名密码，请用下面的认证方式'
  const topic = form.topic.trim()
  if (!topic) {
    if (requireTopic) return '请填写主题（点「随机生成」最省事）'
  } else if (!TOPIC_RE.test(topic)) {
    return '主题只能由字母、数字、下划线和连字符组成，且不超过 64 个字符'
  }
  if (form.authMode === 'token' && !form.token.trim() && !saved.token_set && !saved.token_from_env) {
    return '请填写访问令牌，或改选「无认证」'
  }
  if (form.authMode === 'basic') {
    if (!form.username.trim()) return '请填写用户名'
    if (form.username.includes(':')) return '用户名不能包含冒号'
    if (!form.password && !(saved.password_set && form.username.trim() === saved.username)) return '请填写密码'
  }
  const click = form.clickUrl.trim()
  if (click && !/^https?:\/\/[^\s]+$/i.test(click)) return '点击通知打开的地址要以 http:// 或 https:// 开头'
  if (!(form.delaySeconds >= 0 && form.delaySeconds <= NTFY_MAX_DELAY_SECONDS)) return `延迟推送应在 0~${NTFY_MAX_DELAY_SECONDS} 秒之间`
  // 没打开运行结束推送时也要合法：这个值会随整张表单存进服务端
  if (!(Number.isInteger(form.runMinSeconds) && form.runMinSeconds >= 1 && form.runMinSeconds <= NTFY_MAX_RUN_MIN_SECONDS)) {
    return `运行结束推送的最短运行时长应在 1~${NTFY_MAX_RUN_MIN_SECONDS} 秒之间`
  }
  for (const [kind, v] of Object.entries(form.priorityByKind)) {
    if (!Number.isInteger(v) || v < 0 || v > 5) return `消息类型「${kind}」的优先级不合法（应是 0~5 的整数，0 表示不推送）`
  }
  return ''
}

/** 表单相对已保存的配置有没有改动（凭据输入框里有内容也算改动）。 */
export function isDirty(form: NtfyForm, saved: NtfyConfig): boolean {
  const base = formFromConfig(saved)
  return (
    form.enabled !== base.enabled ||
    form.server.trim() !== base.server ||
    form.topic.trim() !== base.topic ||
    form.authMode !== base.authMode ||
    form.token.trim() !== '' ||
    (form.authMode === 'basic' && form.username.trim() !== base.username) ||
    form.password !== '' ||
    form.clickUrl.trim() !== base.clickUrl ||
    form.priority !== base.priority ||
    form.includeSummary !== base.includeSummary ||
    form.delaySeconds !== base.delaySeconds ||
    form.runNotify !== base.runNotify ||
    form.runMinSeconds !== base.runMinSeconds ||
    !sameKindTable(form.priorityByKind, base.priorityByKind)
  )
}

function sameKindTable(a: Record<string, number>, b: Record<string, number>): boolean {
  const ka = Object.keys(a)
  return ka.length === Object.keys(b).length && ka.every(k => k in b && a[k] === b[k])
}

// 32 个字符（去掉了容易看错的 0 / O / 1 / l / i），每个字符 5 比特
const TOPIC_ALPHABET = 'abcdefghjkmnpqrstuvwxyz23456789_'

/**
 * 生成一个难以猜到的主题名：relaymesh- 加 24 个随机字符（120 比特）。
 * 公共 ntfy.sh 上知道主题名的人都能订阅，所以随机性必须来自密码学随机数。
 */
export function randomTopic(getRandom: (bytes: Uint8Array) => Uint8Array = c => crypto.getRandomValues(c)): string {
  const bytes = getRandom(new Uint8Array(24))
  let out = 'relaymesh-'
  for (const b of bytes) out += TOPIC_ALPHABET[b & 31]
  return out
}

/** 在 ntfy 应用里订阅用的完整地址。 */
export function subscribeUrl(server: string, topic: string): string {
  const base = (server.trim() || NTFY_DEFAULT_SERVER).replace(/\/+$/, '')
  return topic.trim() ? `${base}/${topic.trim()}` : ''
}

export function isPublicNtfySh(server: string): boolean {
  try {
    return new URL(server.trim() || NTFY_DEFAULT_SERVER).hostname.toLowerCase() === 'ntfy.sh'
  } catch {
    return false
  }
}

/** 需要提醒用户注意的事项（不阻止保存）。 */
export function privacyWarnings(form: NtfyForm): string[] {
  const out: string[] = []
  const topic = form.topic.trim()
  if (isPublicNtfySh(form.server)) {
    out.push('公共 ntfy.sh 上，知道主题名的人就能订阅到推送——主题名等同于口令，请用「随机生成」的长名字，且不要分享出去。')
    if (topic && topic.length < 16) out.push('这个主题名偏短、容易被别人猜到，建议点「随机生成」换一个。')
    if (form.includeSummary) out.push('已选择附带摘要：摘要内容（运行结束的推送里是命令的第一行）会经过 ntfy.sh 这个第三方服务，涉及敏感信息时建议关闭，或自建 ntfy。')
  }
  if (form.enabled && form.authMode === 'basic' && /^http:\/\//i.test(form.server.trim()) && !isLocalHost(form.server)) {
    out.push('用户名与密码会以明文经 http:// 发送，请改用 https://，或确保整条链路在可信内网里。')
  }
  if (form.enabled && form.authMode === 'token' && /^http:\/\//i.test(form.server.trim()) && !isLocalHost(form.server)) {
    out.push('访问令牌会以明文经 http:// 发送，请改用 https://，或确保整条链路在可信内网里。')
  }
  return out
}

function isLocalHost(server: string): boolean {
  try {
    const h = new URL(server.trim()).hostname
    return h === 'localhost' || h === '127.0.0.1' || h === '[::1]' || /^(10\.|192\.168\.|172\.(1[6-9]|2\d|3[01])\.)/.test(h)
  } catch {
    return false
  }
}

export interface LastAttempt {
  tone: 'none' | 'ok' | 'fail'
  text: string
}

/** 「最近一次推送」那一行的文案；状态只存在服务端内存里，重启后清零。 */
export function describeLast(status: NtfyStatus | null | undefined): LastAttempt {
  if (!status || !status.last_attempt_at) {
    return { tone: 'none', text: '还没有推送记录（服务重启后会清零）' }
  }
  const t = new Date(status.last_attempt_at)
  const when = Number.isNaN(t.getTime())
    ? ''
    : `${String(t.getHours()).padStart(2, '0')}:${String(t.getMinutes()).padStart(2, '0')}:${String(t.getSeconds()).padStart(2, '0')}`
  const kind = status.last_test ? '测试推送' : '推送'
  if (status.last_ok) return { tone: 'ok', text: `最近一次${kind}成功 · ${when}` }
  return { tone: 'fail', text: `最近一次${kind}失败 · ${when}：${status.last_error || '原因未知'}` }
}

/** 累计数字那一行；全是 0 时返回空串。 */
export function describeCounters(status: NtfyStatus | null | undefined): string {
  if (!status) return ''
  const parts: string[] = []
  if (status.sent > 0) parts.push(`已推送 ${status.sent} 条`)
  if (status.failed > 0) parts.push(`失败 ${status.failed} 条`)
  if (status.dropped > 0) parts.push(`被限流合并 / 丢弃 ${status.dropped} 条`)
  return parts.join(' · ')
}

// ───────────────────── 按消息类型设置优先级 ─────────────────────
// 服务端（internal/ntfy 的消息类型表）是权威：每一类的名字、说明、内置默认都来自它返回的目录，
// 这里只负责「覆盖表 ↔ 下拉选项」的翻译，规则与服务端的 PriorityFor 保持一致：
//   覆盖表里有这一类 → 0 = 不推送，1~5 = 这个优先级；没有 → 这一类的内置默认（异常类默认不推送）。

/** ntfy 优先级的中文名（ntfy 自己叫 min / low / default / high / max）。 */
const PRIORITY_NAMES: Record<number, string> = { 1: '最低', 2: '较低', 3: '默认', 4: '较高', 5: '紧急' }

/** 优先级在 ntfy 手机应用里的大致效果（以应用里的通知设置为准），给用户选的时候参考。 */
export const NTFY_PRIORITY_EFFECTS =
  '大致效果：1~2 不响不震，只在通知栏里；3 有提示音；4 提示音加弹出通知；5 持续震动加弹出通知（具体以 ntfy 应用里的设置为准）'

function clampPriority(p: number): number {
  if (!Number.isFinite(p)) return NTFY_DEFAULT_PRIORITY
  return Math.min(5, Math.max(1, Math.round(p)))
}

/** 优先级的写法，如「较高（4）」；note 会放进括号里，如「较高（4，建议）」。 */
export function priorityLabel(p: number, note = ''): string {
  const n = clampPriority(p)
  return `${PRIORITY_NAMES[n]}（${n}${note ? `，${note}` : ''}）`
}

/** 设置页里消息类型的分组与顺序。 */
export const NTFY_KIND_GROUPS: ReadonlyArray<{ id: NtfyKindGroup; title: string; hint: string }> = [
  { id: 'need_you', title: '需要你处理', hint: '智能体、群聊或终端在等你——默认都会推送。' },
  { id: 'run', title: '模型运行的命令', hint: '模型经 ExecMesh 在主机上跑的命令；要先打开上面的「模型运行的命令结束时也推送」才会推。' },
  {
    id: 'exception',
    title: '异常提醒',
    hint: '出了问题才推，每一类都有去重，不会刷屏。默认全部不推送；打开之前可以先点「试发」，在手机上看看它会怎么响。',
  },
]

/** 一行里下拉框的取值：default = 用内置默认，off = 不推送，1~5 = 指定优先级。 */
export type KindChoice = 'default' | 'off' | '1' | '2' | '3' | '4' | '5'

export interface KindGroupView {
  id: string
  title: string
  hint: string
  kinds: NtfyKindInfo[]
}

/** 把目录按分组排好；服务端新增了界面还不认识的分组时，归到「其他」，不会悄悄丢掉。 */
export function groupKinds(kinds: readonly NtfyKindInfo[] | undefined): KindGroupView[] {
  const list = kinds ?? []
  const out: KindGroupView[] = []
  for (const g of NTFY_KIND_GROUPS) {
    const ks = list.filter(k => k.group === g.id)
    if (ks.length) out.push({ ...g, kinds: ks })
  }
  const known = new Set<string>(NTFY_KIND_GROUPS.map(g => g.id))
  const rest = list.filter(k => !known.has(k.group))
  if (rest.length) out.push({ id: 'other', title: '其他', hint: '', kinds: rest })
  return out
}

/** 这一类的内置默认优先级：写死的用写死值；跟随默认优先级的跟随（顺利完成的命令最多按 3）。 */
export function builtinPriority(info: NtfyKindInfo, defaultPriority: number): number {
  if (info.default_priority > 0) return clampPriority(info.default_priority)
  const p = clampPriority(defaultPriority)
  return info.cap_normal && p > 3 ? 3 : p
}

/** 覆盖表里这一类现在对应哪个选项。默认不推送的类型设成 0 与「默认」等价，统一显示成「默认」。 */
export function kindChoice(info: NtfyKindInfo, table: Record<string, number> | undefined): KindChoice {
  const v = table?.[info.id]
  if (v === undefined || v === null || !Number.isFinite(v)) return 'default'
  if (v < 1) return info.default_on ? 'off' : 'default'
  return String(clampPriority(v)) as KindChoice
}

/** 把一次选择写进覆盖表（返回新对象，不改原表）：选「默认」就是把这一项删掉。 */
export function withKindChoice(table: Record<string, number> | undefined, info: NtfyKindInfo, choice: KindChoice): Record<string, number> {
  const next = { ...(table ?? {}) }
  if (choice === 'default') delete next[info.id]
  else if (choice === 'off') next[info.id] = 0
  else next[info.id] = clampPriority(Number(choice))
  return next
}

/** 这一类此刻实际会怎么推：on = 会不会推，priority = 用几级（不推时为 0）。 */
export function effectiveKind(
  info: NtfyKindInfo,
  table: Record<string, number> | undefined,
  defaultPriority: number
): { on: boolean; priority: number } {
  const v = table?.[info.id]
  if (v !== undefined && v !== null && Number.isFinite(v)) {
    return v < 1 ? { on: false, priority: 0 } : { on: true, priority: clampPriority(v) }
  }
  if (!info.default_on) return { on: false, priority: 0 }
  return { on: true, priority: builtinPriority(info, defaultPriority) }
}

/** 一行下拉框的选项。「默认」那一项会写明它此刻等于几级，用户不用去猜。 */
export function kindOptions(info: NtfyKindInfo, defaultPriority: number): Array<{ value: KindChoice; label: string }> {
  const levels: KindChoice[] = ['1', '2', '3', '4', '5']
  if (!info.default_on) {
    const suggested = info.suggested_priority ?? 0
    return [
      { value: 'default', label: '不推送（默认）' },
      ...levels.map(v => ({ value: v, label: `推送 · ${priorityLabel(Number(v), Number(v) === suggested ? '建议' : '')}` })),
    ]
  }
  // 「默认」那一项用箭头写明它此刻落在几级：默认 → 较高（4）
  const p = builtinPriority(info, defaultPriority)
  let def: string
  if (info.default_priority > 0) def = `默认 → ${priorityLabel(p)}`
  else if (info.cap_normal && clampPriority(defaultPriority) > 3) def = `跟随默认优先级（最多按 3）→ ${priorityLabel(p)}`
  else def = `跟随默认优先级 → ${priorityLabel(p)}`
  return [
    { value: 'default', label: def },
    { value: 'off', label: '不推送' },
    ...levels.map(v => ({ value: v, label: priorityLabel(Number(v)) })),
  ]
}

/** 表头下面的一行小结：让人一眼看出哪些改过、哪些关了、异常提醒开了几类。 */
export function describeKindTable(
  kinds: readonly NtfyKindInfo[] | undefined,
  table: Record<string, number> | undefined,
  defaultPriority: number
): string {
  const list = kinds ?? []
  if (!list.length) return ''
  const custom = list.filter(k => kindChoice(k, table) !== 'default').length
  const closed = list.filter(k => k.default_on && !effectiveKind(k, table, defaultPriority).on).length
  const exceptions = list.filter(k => k.group === 'exception')
  const exceptionsOn = exceptions.filter(k => effectiveKind(k, table, defaultPriority).on).length
  const parts = [custom > 0 ? `已单独设置 ${custom} 类` : '全部使用默认设置']
  if (closed > 0) parts.push(`${closed} 类已设成不推送`)
  if (exceptions.length) parts.push(exceptionsOn > 0 ? `异常提醒已开 ${exceptionsOn} / ${exceptions.length} 类` : '异常提醒全部关闭')
  return parts.join(' · ')
}

/** 「试发这一类」之后给用户看的话：写明实际用了几级；这一类目前不推送时提醒「这只是预览」。 */
export function describeKindTest(info: NtfyKindInfo, choice: KindChoice, res: { priority?: number; took_ms: number }): string {
  const level = res.priority ? priorityLabel(res.priority) : '默认优先级'
  const preview = choice === 'off' || (!info.default_on && choice === 'default')
  const base = `已按「${level}」试发「${info.label}」（用时 ${res.took_ms} 毫秒）。请到订阅了这个主题的设备上，看看声音和提醒方式是否合适。`
  return preview ? `${base}这一类目前是「不推送」，这条只是预览，并没有真的打开。` : base
}
