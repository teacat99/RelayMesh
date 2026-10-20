// 模型运行（ExecMesh 的「线程」与「卡片」）的纯逻辑：状态文案、标题、折叠预览、时长、列表合并、事件流解析与实时输出累积。
// 与 Vue 无关，便于单元测试；状态保存在 stores/execRuns.ts。

import type { RunState, RunView, ThreadView } from '../../api/terminal'
import { runLimitText } from './format'

// ---------- 状态 ----------

const FINAL_STATES: ReadonlySet<string> = new Set<RunState>(['succeeded', 'failed', 'timeout', 'cancelled', 'rejected', 'denied', 'lost'])

/** 终态：不会再变化。 */
export function isFinalRun(state: string): boolean {
  return FINAL_STATES.has(state)
}

/** 进行中：待批准或运行中。 */
export function isActiveRun(state: string): boolean {
  return state === 'awaiting_approval' || state === 'running'
}

export type RunTone = 'run' | 'ok' | 'warn' | 'bad' | 'muted'

export interface RunStateInfo {
  label: string
  tone: RunTone
  /** 补充说明（退出码、信号、原因）；没有则为空串。 */
  detail: string
}

const DENY_REASONS: Record<string, string> = {
  rejected_by_user: '被你拒绝',
  approval_timeout: '等待批准超时',
  hub_restart: '服务端重启，待批准的命令已作废',
  withdrawn: '被撤回',
  host_denied: '主机本机策略拒绝了模型执行',
  host_not_found: '主机不存在',
  host_model_off: '这台主机没有开放给模型',
  host_offline: '主机不在线',
  host_unsupported: '主机不支持模型执行',
  paused: '模型已被暂停',
  readonly: '只读模式不允许执行',
  limit_exceeded: '超出令牌限额',
  not_allowed: '令牌无权执行',
  busy: '系统繁忙',
  not_on_host: '主机上找不到这次运行',
  overdue: '运行超期未报告',
  // 启动失败（两种后端共用）
  bad_request: '请求参数无效',
  bad_cwd: '工作目录不存在或不是目录',
  no_shell: '主机上找不到可用的 shell',
  spawn_failed: '进程启动失败',
  exec_unavailable: '主机没有可保存运行输出的目录',
  duplicate: '同一运行已存在',
  holder_unavailable: '主机的会话守护进程不可用',
  shutting_down: '主机正在关闭',
  denied_by_host: '主机本机策略拒绝了模型执行',
  // SSH 主机
  ssh_unavailable: 'SSH 后端未启用（服务端缺少有效的 RELAYMESH_SSH_SECRET_KEY）',
  ssh_untrusted: 'SSH 主机的密钥尚未固定或已不一致，需要管理员重新确认指纹',
  ssh_unreachable: '无法连接到 SSH 主机',
  ssh_auth_failed: 'SSH 认证失败，请检查用户名与凭据',
  ssh_host_key_mismatch: 'SSH 主机密钥与已固定的不一致，已拒绝连接',
  ssh_secret_unreadable: '无法解开保存的 SSH 凭据，请在主机页重新填写',
  ssh_unsupported: '远端环境不满足执行条件（缺少所需的 shell 工具）',
  ssh_host_removed: 'SSH 主机已被停用或删除',
  // 经网关接入的 SSH 主机
  ssh_cred_missing: '网关上已经没有这台 SSH 主机的登录凭据，请在主机页重新填写',
  ssh_gateway_unavailable: '承载这台 SSH 主机的网关当前不可用（不在线、没有开启 SSH 网关或凭据库不可用）',
  ssh_target_denied: '网关的 --ssh-allow 白名单不允许连接这台 SSH 主机的地址',
  ssh_terminal_only: '这台 SSH 主机探测不出 POSIX 环境，只能当人工终端用',
  too_large: '命令或结果超过了网关一次能传输的大小上限',
  proc_gone: '远端进程已经不存在，没有留下结果',
  output_limit: '输出超过上限，进程已被终止',
  // 取消来源（运行状态是「已取消」时 deny_reason 里写的是谁、为什么取消）
  user: '你取消了这次运行',
  model: '模型取消了这次运行',
  pause: '模型执行被暂停（全局或这台主机），进行中的运行被取消',
  revoked: '这台主机的模型授权被收回（或改成了只读），进行中的运行被取消',
  gate_closed: '终端总闸关闭（服务端的终端功能被整体停用），进行中的运行被一并取消',
  host_removed: '这台主机被停用或删除，进行中的运行被取消',
  token_changed: '这次运行所用的令牌被修改、停用、删除或重新生成，进行中的运行被取消',
  unknown_run: '服务端不认识这次运行（记录已不存在），主机上的进程被取消',
  system: '被系统取消（旧版本统一记成 system：总闸关闭、主机停用、令牌变更等都会这样，当时没有记下具体是哪一种）',
}

/** 拒绝 / 丢失原因码对应的中文；未知的原样返回，方便排查。 */
export function denyReasonLabel(code: string | undefined): string {
  if (!code) return ''
  return DENY_REASONS[code] ?? code
}

type StateFields = Pick<RunView, 'state'> & Partial<Pick<RunView, 'exit_code' | 'signal' | 'deny_reason' | 'timeout_sec'>>

/** 状态位的文案、色调与补充说明。 */
export function runStateInfo(r: StateFields): RunStateInfo {
  const reason = denyReasonLabel(r.deny_reason)
  switch (r.state) {
    case 'awaiting_approval':
      return { label: '待批准', tone: 'warn', detail: '' }
    case 'running':
      return { label: '运行中', tone: 'run', detail: '' }
    case 'succeeded':
      return { label: '成功', tone: 'ok', detail: `退出码 ${r.exit_code ?? 0}` }
    case 'failed':
      if (r.signal) return { label: '失败', tone: 'bad', detail: reason ? `被信号 ${r.signal} 终止（${reason}）` : `被信号 ${r.signal} 终止` }
      // 没有退出码说明命令根本没有跑起来（或连接 / 认证失败）：把原因直接说出来。
      return { label: '失败', tone: 'bad', detail: r.exit_code === undefined ? reason : `退出码 ${r.exit_code}` }
    case 'timeout': {
      // 到点被主机强制结束：说清是「达到运行时长上限」以及上限是多少（预设的最长运行时间 / 命令的 timeout_seconds，#141）。
      const limit = runLimitText(r.timeout_sec)
      return { label: '超时', tone: 'bad', detail: limit ? `达到运行时长上限（${limit}）` : '达到运行时长上限' }
    }
    case 'cancelled':
      return { label: '已取消', tone: 'muted', detail: reason }
    case 'rejected':
      return { label: '已拒绝', tone: 'muted', detail: reason }
    case 'denied':
      return { label: '已拒绝（策略）', tone: 'bad', detail: reason }
    case 'lost':
      return { label: '主机丢失', tone: 'warn', detail: reason }
    default:
      return { label: r.state || '未知', tone: 'muted', detail: reason }
  }
}

/** 待批准的命令多久不处理会被自动拒绝（与服务端一致：10 分钟）。 */
export const APPROVAL_TTL_MS = 10 * 60 * 1000

/** 待批准还剩多少毫秒（已超时返回 0）；不是待批准则为 null。 */
export function approvalRemaining(r: Pick<RunView, 'state' | 'created_at'>, nowMs: number): number | null {
  if (r.state !== 'awaiting_approval') return null
  const t = Date.parse(r.created_at)
  if (!Number.isFinite(t)) return null
  return Math.max(0, t + APPROVAL_TTL_MS - nowMs)
}

/** 执行模式的中文名。 */
export function modeLabel(mode: string | undefined): string {
  switch (mode) {
    case 'approve':
      return '需批准'
    case 'readonly':
      return '只读'
    case 'auto':
    case '':
    case undefined:
      return '自动'
    default:
      return mode
  }
}

// ---------- 标题与展示 ----------

/** 卡片标题：模型填写的意图；没有则取命令的第一行。 */
export function runTitle(r: Pick<RunView, 'description' | 'command'>, max = 80): string {
  const desc = (r.description ?? '').trim()
  const first = desc || (r.command ?? '').trim().split('\n', 1)[0]
  const t = first.trim()
  if (!t) return '（空命令）'
  return t.length > max ? `${t.slice(0, max - 1)}…` : t
}

/** 命令名清单（仅展示，来自服务端的提取）。 */
export function toolList(r: Pick<RunView, 'tools'>): string[] {
  return (r.tools ?? '')
    .split(',')
    .map(s => s.trim())
    .filter(Boolean)
}

/** 时长的紧凑写法：`0.3s`、`12s`、`2m05s`、`1h03m`。 */
export function formatDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) return ''
  if (ms < 100) return '<0.1s'
  if (ms < 10_000) {
    const v = Math.round(ms / 100) / 10
    return v >= 10 ? '10s' : `${v.toFixed(1)}s`
  }
  const s = Math.floor(ms / 1000)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m${String(s % 60).padStart(2, '0')}s`
  const h = Math.floor(m / 60)
  return `${h}h${String(m % 60).padStart(2, '0')}m`
}

function parseTime(iso: string | undefined): number {
  if (!iso) return 0
  const t = Date.parse(iso)
  return Number.isFinite(t) ? t : 0
}

/** 一次运行已经花的时间：运行中按开始时刻与当前时间算，结束后用服务端记录的时长。待批准的运行没有时长。 */
export function runElapsed(r: Pick<RunView, 'state' | 'started_at' | 'duration_ms'>, nowMs: number): number | null {
  if (r.state === 'awaiting_approval') return null
  if (r.state === 'running') {
    const t = parseTime(r.started_at)
    return t ? Math.max(0, nowMs - t) : null
  }
  if (!parseTime(r.started_at)) return null // 从未开始（被拒绝 / 取消）
  return r.duration_ms >= 0 ? r.duration_ms : null
}

/** 字节数的紧凑写法。 */
export function formatBytes(n: number): string {
  if (!Number.isFinite(n) || n < 0) return ''
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(n < 10 * 1024 ? 1 : 0)} KiB`
  return `${(n / 1024 / 1024).toFixed(n < 10 * 1024 * 1024 ? 1 : 0)} MiB`
}

// ---------- 输出文本的展示净化 ----------

// 终端输出里常见的转义序列（颜色、光标、标题）：卡片按纯文本显示，不解释它们。
// eslint-disable-next-line no-control-regex
const ANSI_RE = /\u001b(?:\[[0-?]*[ -/]*[@-~]|\][^\u0007\u001b]*(?:\u0007|\u001b\\)|[@-Z\\-_])/g
// 其余控制字符与双向文本控制符（后者可用来伪造显示顺序）。
// eslint-disable-next-line no-control-regex
const CONTROL_RE = /[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f-\u009f\u200e\u200f\u202a-\u202e\u2066-\u2069]/g

/**
 * 把命令输出整理成可安全显示的纯文本：去掉转义序列与控制字符；
 * 同一行里的回车（进度条之类）只保留最后一次刷新的内容。
 */
export function cleanDisplay(text: string): string {
  if (!text) return ''
  const lines = text
    .replace(ANSI_RE, '')
    .split('\n')
    .map(line => {
      if (!line.includes('\r')) return line
      const segs = line.split('\r').filter(x => x !== '')
      return segs.length ? segs[segs.length - 1] : ''
    })
  return lines.join('\n').replace(CONTROL_RE, '')
}

// ---------- 折叠预览 ----------

export interface LinePreview {
  /** 折叠态显示的前 N 行。 */
  lines: string[]
  /** 预览文本里的总行数。 */
  total: number
  /** 展开后还有更多内容（预览本身被截断，或超过 N 行）。 */
  more: boolean
  /** 预览覆盖了全部输出：此时 total 就是输出的总行数。 */
  exact: boolean
}

/** 取一段文本的前 n 行；`beyond` 表示文本之外（预览没有覆盖的部分）还有更多内容。 */
export function previewLines(text: string | undefined, n: number, beyond = false): LinePreview {
  const norm = cleanDisplay(text ?? '')
  const all = norm === '' ? [] : norm.replace(/\n$/, '').split('\n')
  const limit = Math.max(0, Math.floor(n))
  return { lines: all.slice(0, limit), total: all.length, more: all.length > limit || beyond, exact: !beyond }
}

/** 一次运行的折叠预览：优先取预览头部；预览之外还有输出（字节数大于预览）时标记有更多。 */
export function collapsedPreview(r: Pick<RunView, 'preview_head' | 'stdout_bytes' | 'stderr_bytes'>, n: number): LinePreview {
  const head = r.preview_head ?? ''
  const shown = new TextEncoder().encode(head).length
  const beyond = r.stdout_bytes + r.stderr_bytes > shown && shown > 0
  return previewLines(head, n, beyond)
}

// ---------- 列表 ----------

function sortKey(r: RunView): number {
  return parseTime(r.created_at)
}

/** 按创建时间排序（相同时按运行 ID 保持稳定）。asc = 最早在前（像聊天，最新在底）。 */
export function sortRuns(list: readonly RunView[], order: 'asc' | 'desc' = 'asc'): RunView[] {
  const sign = order === 'asc' ? 1 : -1
  return [...list].sort((a, b) => sign * (sortKey(a) - sortKey(b) || (a.run_id < b.run_id ? -1 : a.run_id > b.run_id ? 1 : 0)))
}

/** 把一次运行更新进列表（按运行 ID 去重；终态不会被迟到的旧状态覆盖）。返回新数组。 */
export function mergeRun(list: readonly RunView[], run: RunView): RunView[] {
  const i = list.findIndex(r => r.run_id === run.run_id)
  if (i < 0) return sortRuns([...list, run])
  const cur = list[i]
  if (isFinalRun(cur.state) && !isFinalRun(run.state)) return list as RunView[]
  if (isFinalRun(cur.state) && isFinalRun(run.state) && parseTime(run.updated_at) < parseTime(cur.updated_at)) return list as RunView[]
  const next = list.slice()
  next[i] = run
  return next
}

/** 合并一批运行（翻页 / 重连后补拉）。 */
export function mergeRuns(list: readonly RunView[], incoming: readonly RunView[]): RunView[] {
  let out = list as RunView[]
  for (const r of incoming) out = mergeRun(out, r)
  return out
}

export type RunFilter = 'all' | 'active' | 'failed' | 'denied'

export const RUN_FILTERS: { id: RunFilter; label: string }[] = [
  { id: 'all', label: '全部' },
  { id: 'active', label: '进行中' },
  { id: 'failed', label: '失败' },
  { id: 'denied', label: '被拒绝' },
]

/** 按状态筛选与关键词过滤（关键词匹配意图、命令、命令名、调用方）。 */
export function filterRuns(list: readonly RunView[], filter: RunFilter, query: string): RunView[] {
  const q = query.trim().toLowerCase()
  return list.filter(r => {
    if (filter === 'active' && !isActiveRun(r.state)) return false
    if (filter === 'failed' && !(r.state === 'failed' || r.state === 'timeout' || r.state === 'lost')) return false
    if (filter === 'denied' && !(r.state === 'rejected' || r.state === 'denied')) return false
    if (!q) return true
    return [r.description, r.command, r.tools, r.credential_name].some(s => (s ?? '').toLowerCase().includes(q))
  })
}

/** 列表里出现了几个不同的调用方（一个线程里有多个凭据时，卡片需要逐条标注）。 */
export function distinctCallers(list: readonly RunView[]): string[] {
  return [...new Set(list.map(r => r.credential_name).filter(Boolean))]
}

// ---------- 线程 ----------

/** 线程的显示名：ExecMesh 档为「凭据 · 主机」，混合令牌按工作流为「工作流 · 主机」。 */
export function threadTitle(t: Pick<ThreadView, 'credential_name' | 'workflow_id'>, hostName: string): string {
  const who = t.workflow_id ? t.workflow_id : t.credential_name || '未命名调用方'
  return hostName ? `${who} · ${hostName}` : who
}

/** 线程的第二行：线程标签 + 最近一次运行的标题。 */
export function threadSubtitle(t: Pick<ThreadView, 'thread' | 'last_title' | 'active' | 'awaiting'>): string {
  const parts: string[] = []
  if (t.thread) parts.push(`#${t.thread}`)
  if (t.awaiting > 0) parts.push(`${t.awaiting} 条待批准`)
  else if (t.active > 0) parts.push(`${t.active} 条运行中`)
  if (t.last_title) parts.push(`最近：${t.last_title}`)
  return parts.join(' · ')
}

// ---------- 事件流（SSE 格式，fetch 流式读取） ----------

export interface SseEvent {
  event: string
  data: string
}

/**
 * 增量解析 `text/event-stream`：喂入任意切分的文本，返回已完整的事件。
 * 只认 `event:` 与 `data:` 两个字段（多行 data 用换行拼接），忽略以 `:` 开头的注释（心跳）。
 */
export class SseParser {
  private buf = ''
  private event = ''
  private data: string[] = []

  push(chunk: string): SseEvent[] {
    this.buf += chunk
    const out: SseEvent[] = []
    for (;;) {
      const i = this.buf.search(/\r\n|\n|\r/)
      if (i < 0) break
      const m = /^(\r\n|\n|\r)/.exec(this.buf.slice(i))
      const nl = m ? m[0].length : 1
      // 末尾恰好是单个 '\r' 时可能后面还跟 '\n'，等下一块再处理。
      if (this.buf[i] === '\r' && i + 1 === this.buf.length) break
      const line = this.buf.slice(0, i)
      this.buf = this.buf.slice(i + nl)
      if (line === '') {
        if (this.data.length > 0 || this.event) out.push({ event: this.event || 'message', data: this.data.join('\n') })
        this.event = ''
        this.data = []
        continue
      }
      if (line.startsWith(':')) continue
      const c = line.indexOf(':')
      const field = c < 0 ? line : line.slice(0, c)
      let value = c < 0 ? '' : line.slice(c + 1)
      if (value.startsWith(' ')) value = value.slice(1)
      if (field === 'event') this.event = value
      else if (field === 'data') this.data.push(value)
    }
    return out
  }
}

// ---------- 实时输出 ----------

export interface LiveChunk {
  stream: 'stdout' | 'stderr'
  text: string
}

export interface LiveOut {
  chunks: LiveChunk[]
  /** 已收到的字节偏移（按流）。 */
  off: { stdout: number; stderr: number }
  /** 累积的文本字节数（粗略，用于封顶）。 */
  size: number
  /** 因封顶或丢包而不完整。 */
  partial: boolean
}

export function emptyLive(): LiveOut {
  return { chunks: [], off: { stdout: 0, stderr: 0 }, size: 0, partial: false }
}

/** 单次运行在浏览器里最多缓存的实时输出（超出后丢弃最早的片段）。 */
export const LIVE_CAP_BYTES = 256 * 1024

/**
 * 追加一个实时输出片段。`off` 是该片段在其流里的起始偏移：
 * 小于已收到的偏移说明重复（跳过重叠部分）；大于说明中间有丢包，标记 partial 但照常显示。
 * 返回新的 LiveOut（不修改入参）。
 */
export function appendLive(cur: LiveOut, stream: 'stdout' | 'stderr', off: number, text: string, byteLen: number, cap = LIVE_CAP_BYTES): LiveOut {
  const have = cur.off[stream]
  if (off + byteLen <= have) return cur // 完全重复
  let partial = cur.partial
  if (off > have) partial = true
  const nextOff = { ...cur.off, [stream]: off + byteLen }
  // 与已有部分重叠时只能整体接受或跳过（文本已解码，无法按字节切），重叠极少见，直接接受后者。
  let chunks = [...cur.chunks, { stream, text }]
  let size = cur.size + byteLen
  while (size > cap && chunks.length > 1) {
    size -= new TextEncoder().encode(chunks[0].text).length
    chunks = chunks.slice(1)
    partial = true
  }
  return { chunks, off: nextOff, size, partial }
}

/** 把实时片段拼成一段文本（按到达顺序）。 */
export function liveText(l: LiveOut | undefined): string {
  return l ? l.chunks.map(c => c.text).join('') : ''
}

/** base64（标准字母表）解码成字节；非法输入返回空数组。 */
export function decodeBase64(b64: string): Uint8Array {
  try {
    const bin = atob(b64)
    const out = new Uint8Array(bin.length)
    for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i)
    return out
  } catch {
    return new Uint8Array(0)
  }
}
