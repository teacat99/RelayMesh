// 终端记录的浏览器侧解码、时间线整理与导出（纯函数，不依赖 DOM / Vue，便于单元测试）。
//
// 记录的是「终端上显示的内容」：PTY 输出字节（含 Shell 对键入内容的回显）、窗口尺寸变化与少量标记。
// 事件编码与 Go 侧 internal/agent/record 完全一致，并有同一份对照文件（record/testdata/golden_events.hex）互相约束：
//   kind(1) | dt uvarint（距上一事件的毫秒数）| len uvarint | 净荷

export const KIND_OUTPUT = 1
export const KIND_RESIZE = 2
export const KIND_MARK = 3

/** 单个事件净荷的上限（对应 record.MaxEventPayload）；超过说明数据已损坏。 */
export const MAX_EVENT_PAYLOAD = 32 * 1024

export interface RecEvent {
  kind: number
  /** 距上一个事件的毫秒数（分段内第一个事件为 0）。 */
  dt: number
  payload: Uint8Array
}

export class RecordCorruptError extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'RecordCorruptError'
  }
}

/** 读一个无符号变长整数；字节不够返回 null（写入中的尾部属于正常情况），格式非法抛 RecordCorruptError。 */
function readUvarint(buf: Uint8Array, start: number): { value: number; next: number } | null {
  let value = 0
  let scale = 1
  for (let i = 0; i < 10; i++) {
    const pos = start + i
    if (pos >= buf.length) return null
    const b = buf[pos]
    value += (b & 0x7f) * scale
    if ((b & 0x80) === 0) {
      // 第 10 个字节只允许 0 或 1（超出 uint64）。
      if (i === 9 && b > 1) throw new RecordCorruptError('变长整数溢出')
      return { value, next: pos + 1 }
    }
    scale *= 128
  }
  throw new RecordCorruptError('变长整数过长')
}

/** 解出尽可能多的完整事件；末尾不足一个事件的字节不算错误（used 告诉调用方消耗了多少）。 */
export function decodeEvents(data: Uint8Array): { events: RecEvent[]; used: number } {
  const events: RecEvent[] = []
  let pos = 0
  while (data.length - pos >= 3) {
    const kind = data[pos]
    const dt = readUvarint(data, pos + 1)
    if (!dt) break
    const ln = readUvarint(data, dt.next)
    if (!ln) break
    if (ln.value > MAX_EVENT_PAYLOAD) throw new RecordCorruptError('事件长度超出上限')
    const end = ln.next + ln.value
    if (end > data.length) break
    events.push({ kind, dt: dt.value, payload: data.subarray(ln.next, end) })
    pos = end
  }
  return { events, used: pos }
}

// ---------- 标记与时间线 ----------

export type MarkType = 'seg' | 'gap' | 'end'

export interface RawMark {
  t: string
  seq?: number
  t0?: number
  rows?: number
  cols?: number
  reason?: string
  bytes?: number
  code?: number
  signal?: string
}

export function decodeMark(payload: Uint8Array): RawMark | null {
  try {
    const m = JSON.parse(new TextDecoder().decode(payload)) as RawMark
    return m && typeof m.t === 'string' ? m : null
  } catch {
    return null
  }
}

export function decodeResize(payload: Uint8Array): { rows: number; cols: number } | null {
  if (payload.length !== 4) return null
  return { rows: (payload[0] << 8) | payload[1], cols: (payload[2] << 8) | payload[3] }
}

export type Item =
  | { type: 'output'; at: number; data: Uint8Array }
  | { type: 'resize'; at: number; rows: number; cols: number }
  | { type: 'seg'; at: number; seq: number; rows: number; cols: number }
  | { type: 'gap'; at: number; reason: string; bytes: number }
  | { type: 'end'; at: number; reason: string; code: number | null; signal: string }

/**
 * 把按页读到的事件整理成带绝对时间的条目序列。
 * 每个分段以 seg 标记开头，标记里带着该分段起点的绝对时间；其后事件的时间由 dt 累加。
 * 窗口从分段中间开始（没有 seg 标记）时，时间从 0 起算，只有相对关系有意义。
 */
export class RecordTimeline {
  readonly items: Item[] = []
  private clock = 0

  push(events: RecEvent[]): void {
    for (const ev of events) {
      this.clock += ev.dt
      if (ev.kind === KIND_OUTPUT) {
        if (ev.payload.length > 0) this.items.push({ type: 'output', at: this.clock, data: ev.payload })
      } else if (ev.kind === KIND_RESIZE) {
        const r = decodeResize(ev.payload)
        if (r) this.items.push({ type: 'resize', at: this.clock, rows: r.rows, cols: r.cols })
      } else if (ev.kind === KIND_MARK) {
        const m = decodeMark(ev.payload)
        if (!m) continue
        if (m.t === 'seg') {
          if (typeof m.t0 === 'number' && m.t0 > 0) this.clock = m.t0
          this.items.push({ type: 'seg', at: this.clock, seq: m.seq ?? 0, rows: m.rows ?? 0, cols: m.cols ?? 0 })
        } else if (m.t === 'gap') {
          this.items.push({ type: 'gap', at: this.clock, reason: m.reason ?? '', bytes: m.bytes ?? 0 })
        } else if (m.t === 'end') {
          this.items.push({ type: 'end', at: this.clock, reason: m.reason ?? '', code: typeof m.code === 'number' ? m.code : null, signal: m.signal ?? '' })
        }
      }
      // 未知 kind：向前兼容，忽略。
    }
  }

  get length(): number {
    return this.items.length
  }
}

/** 时间线里第一个有绝对时间的 seg 标记的尺寸；没有则返回 null。 */
export function initialSize(items: Item[]): { rows: number; cols: number } | null {
  for (const it of items) {
    if (it.type === 'seg' && it.rows > 0 && it.cols > 0) return { rows: it.rows, cols: it.cols }
    if (it.type === 'resize' && it.rows > 0 && it.cols > 0) return { rows: it.rows, cols: it.cols }
    if (it.type === 'output') break
  }
  return null
}

// ---------- 导出 ----------

/** 原始输出字节流（按顺序拼接，不含任何元数据）。 */
export function rawBytes(items: Item[]): Uint8Array {
  let total = 0
  for (const it of items) if (it.type === 'output') total += it.data.length
  const out = new Uint8Array(total)
  let pos = 0
  for (const it of items) {
    if (it.type !== 'output') continue
    out.set(it.data, pos)
    pos += it.data.length
  }
  return out
}

/**
 * 把终端输出整理成纯文本：丢弃屏幕控制序列（颜色、光标移动、标题等），
 * 按回车 / 退格 / 制表符还原一行最终的样子（进度条覆盖、命令行编辑）。
 * 全屏程序（vim / top）的画面无法用线性文本还原，需要原样内容请导出原始流。
 */
export function toPlainText(items: Item[]): string {
  const decoder = new TextDecoder('utf-8')
  const parts: string[] = []
  for (const it of items) if (it.type === 'output') parts.push(decoder.decode(it.data, { stream: true }))
  parts.push(decoder.decode())
  return stripScreenControls(parts.join(''))
}

type EscState = 'none' | 'esc' | 'csi' | 'osc' | 'oscEsc' | 'str' | 'strEsc' | 'skip1'

export function stripScreenControls(text: string): string {
  const lines: string[] = []
  let line: string[] = []
  let col = 0
  let state: EscState = 'none'

  const commit = () => {
    lines.push(line.join('').replace(/\s+$/, ''))
    line = []
    col = 0
  }
  const put = (ch: string) => {
    if (col < line.length) line[col] = ch
    else {
      while (line.length < col) line.push(' ')
      line.push(ch)
    }
    col++
  }

  for (const ch of text) {
    const code = ch.codePointAt(0) as number
    switch (state) {
      case 'esc':
        if (ch === '[') state = 'csi'
        else if (ch === ']') state = 'osc'
        else if (ch === 'P' || ch === 'X' || ch === '^' || ch === '_') state = 'str'
        else if ('()*+-./'.includes(ch)) state = 'skip1'
        else state = 'none'
        continue
      case 'skip1':
        state = 'none'
        continue
      case 'csi':
        if (code >= 0x40 && code <= 0x7e) state = 'none'
        continue
      case 'osc':
        if (code === 0x07) state = 'none'
        else if (code === 0x1b) state = 'oscEsc'
        continue
      case 'oscEsc':
        state = ch === '\\' ? 'none' : 'osc'
        continue
      case 'str':
        if (code === 0x1b) state = 'strEsc'
        continue
      case 'strEsc':
        state = ch === '\\' ? 'none' : 'str'
        continue
      default:
        break
    }
    if (code === 0x1b) {
      state = 'esc'
    } else if (ch === '\n') {
      commit()
    } else if (ch === '\r') {
      col = 0
    } else if (ch === '\b') {
      if (col > 0) col--
    } else if (ch === '\t') {
      const next = (Math.floor(col / 8) + 1) * 8
      while (col < next) put(' ')
    } else if (code < 0x20 || code === 0x7f) {
      // 其余控制字符（响铃、移位等）：丢弃
    } else {
      put(ch)
    }
  }
  if (line.length > 0) commit()
  return lines.join('\n') + (lines.length > 0 ? '\n' : '')
}

export interface CastMeta {
  title?: string
  rows: number
  cols: number
  /** 会话（或窗口）起点的 Unix 毫秒；缺省不写 timestamp。 */
  startedMs?: number
}

/** asciicast v2：可用 asciinema 播放器回放。UTF-8 序列被拆在相邻事件里时流式解码，不会出现乱码。 */
export function toAsciicast(items: Item[], meta: CastMeta): string {
  const decoder = new TextDecoder('utf-8')
  const header: Record<string, unknown> = { version: 2, width: meta.cols, height: meta.rows, env: { TERM: 'xterm-256color' } }
  if (meta.startedMs && meta.startedMs > 0) header.timestamp = Math.floor(meta.startedMs / 1000)
  if (meta.title) header.title = meta.title
  const lines: string[] = [JSON.stringify(header)]

  let origin: number | null = null
  const rel = (at: number): number => {
    if (origin === null) origin = at
    return Math.max(0, Math.round(((at - origin) / 1000) * 1e6) / 1e6)
  }
  for (const it of items) {
    if (it.type === 'output') {
      const text = decoder.decode(it.data, { stream: true })
      if (text) lines.push(JSON.stringify([rel(it.at), 'o', text]))
    } else if (it.type === 'resize') {
      lines.push(JSON.stringify([rel(it.at), 'r', `${it.cols}x${it.rows}`]))
    } else if (it.type === 'seg') {
      // 分段起点只用来校准时间原点
      if (origin === null) origin = it.at
    }
  }
  const tail = decoder.decode()
  if (tail) lines.push(JSON.stringify([rel(origin ?? 0), 'o', tail]))
  return lines.join('\n') + '\n'
}

// ---------- 窗口（只加载记录的一部分）----------

export interface RecSegInfo {
  seq: number
  t0: number
  t1?: number
  raw: number
  disk: number
  rows?: number
  cols?: number
  gz?: boolean
}

export interface RecInfo {
  sid: string
  name?: string
  shell?: string
  created_ms: number
  ended_ms?: number
  ended_reason?: string
  exit_code?: number
  signal?: string
  rows?: number
  cols?: number
  first_ms?: number
  last_ms?: number
  raw_bytes: number
  disk_bytes: number
  dropped_bytes?: number
  /** 记录不完整（开头被清理或中途有缺口）。 */
  gap?: boolean
  /** 最早保留的分段不是第 1 段：更早的内容已按配额清理。 */
  head?: boolean
  segments: RecSegInfo[]
}

const num = (v: unknown, d = 0): number => (typeof v === 'number' && Number.isFinite(v) ? v : d)

/** 把 Hub 转来的主机端记录描述整理成强类型；格式不对返回 null。 */
export function parseRecInfo(raw: unknown): RecInfo | null {
  if (!raw || typeof raw !== 'object') return null
  const o = raw as Record<string, unknown>
  if (typeof o.sid !== 'string') return null
  const segs: RecSegInfo[] = []
  if (Array.isArray(o.segments)) {
    for (const s of o.segments) {
      if (!s || typeof s !== 'object') continue
      const x = s as Record<string, unknown>
      const seq = num(x.seq)
      if (seq <= 0) continue
      segs.push({
        seq,
        t0: num(x.t0),
        t1: x.t1 === undefined ? undefined : num(x.t1),
        raw: num(x.raw),
        disk: num(x.disk),
        rows: x.rows === undefined ? undefined : num(x.rows),
        cols: x.cols === undefined ? undefined : num(x.cols),
        gz: x.gz === true,
      })
    }
  }
  segs.sort((a, b) => a.seq - b.seq)
  return {
    sid: o.sid,
    name: typeof o.name === 'string' ? o.name : undefined,
    shell: typeof o.shell === 'string' ? o.shell : undefined,
    created_ms: num(o.created_ms),
    ended_ms: o.ended_ms === undefined ? undefined : num(o.ended_ms),
    ended_reason: typeof o.ended_reason === 'string' ? o.ended_reason : undefined,
    exit_code: typeof o.exit_code === 'number' ? o.exit_code : undefined,
    signal: typeof o.signal === 'string' ? o.signal : undefined,
    rows: o.rows === undefined ? undefined : num(o.rows),
    cols: o.cols === undefined ? undefined : num(o.cols),
    first_ms: o.first_ms === undefined ? undefined : num(o.first_ms),
    last_ms: o.last_ms === undefined ? undefined : num(o.last_ms),
    raw_bytes: num(o.raw_bytes),
    disk_bytes: num(o.disk_bytes),
    dropped_bytes: o.dropped_bytes === undefined ? undefined : num(o.dropped_bytes),
    gap: o.gap === true,
    head: o.head === true,
    segments: segs,
  }
}

/** 默认一次渲染的原始字节预算：够看一段很长的输出，又不至于把浏览器拖垮。 */
export const WINDOW_BUDGET = 3 * 1024 * 1024

/**
 * 从最新的分段往回数，选出总大小不超过预算的最早一个分段（至少包含最后一个）。
 * 返回该分段在 segments 里的下标；segments 为空返回 -1。
 */
export function pickWindowStart(segments: RecSegInfo[], budget: number = WINDOW_BUDGET): number {
  if (segments.length === 0) return -1
  let start = segments.length - 1
  let total = segments[start].raw
  while (start > 0 && total + segments[start - 1].raw <= budget) {
    start--
    total += segments[start].raw
  }
  return start
}

/** 「加载更早」：在现有窗口之前再多带一批分段（同样受预算限制，至少多带一个）。 */
export function extendWindowStart(segments: RecSegInfo[], startIdx: number, budget: number = WINDOW_BUDGET): number {
  if (startIdx <= 0) return 0
  let i = startIdx - 1
  let total = segments[i].raw
  while (i > 0 && total + segments[i - 1].raw <= budget) {
    i--
    total += segments[i].raw
  }
  return i
}

// ---------- 文案 ----------

const END_REASONS: Record<string, string> = {
  exit: '会话正常退出',
  killed: '被手动终止',
  idle_ttl: '闲置超时被回收',
  policy: '服务端策略关闭了终端',
  holder_stopped: '会话守护进程被停止',
  holder_exit: '会话守护进程异常退出',
  host_reboot: '主机重启',
  aborted: '打开会话失败',
  gone: '会话已不在主机上（原因不明）',
  unknown: '原因不明',
}

export function endReasonLabel(reason: string | undefined): string {
  if (!reason) return '进行中'
  return END_REASONS[reason] ?? reason
}

const GAP_REASONS: Record<string, string> = {
  backlog: '写盘跟不上输出速度',
  disk_low: '磁盘剩余空间不足',
  write_error: '写入记录文件失败',
  policy_off: '记录被策略暂时关闭',
}

export function gapReasonLabel(reason: string): string {
  return GAP_REASONS[reason] ?? (reason || '原因不明')
}

export type RecStateTone = 'ok' | 'warn' | 'muted'

/** 会话录制状态的展示文案；ok 表示正在记录。 */
export function recStateInfo(rec: string | undefined, retentionNote = ''): { text: string; title: string; tone: RecStateTone } | null {
  switch (rec) {
    case 'on':
      return { text: '录制中', title: `这个会话终端上显示的内容正被记录在主机上${retentionNote}。需要不留痕请新建无痕会话。`, tone: 'ok' }
    case 'paused':
      return { text: '录制暂停', title: '记录暂时停止（磁盘剩余空间不足或服务端策略临时关闭），恢复后自动继续，中间会留下缺口标记。', tone: 'warn' }
    case 'error':
      return { text: '录制出错', title: '写入记录文件失败，正在退避重试；会话本身不受影响。', tone: 'warn' }
    case 'off':
      return { text: '未录制', title: '服务端策略关闭了终端记录。', tone: 'muted' }
    case 'veto':
      return { text: '未录制', title: '这台主机在本机用 --no-record 禁止了记录，服务端无法覆盖。', tone: 'muted' }
    case 'incognito':
      return { text: '无痕', title: '无痕会话：主机上不留任何记录文件。', tone: 'muted' }
    default:
      return null
  }
}

export function formatSize(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return '—'
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(bytes < 10 * 1024 ? 1 : 0)} KiB`
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / 1024 / 1024).toFixed(bytes < 10 * 1024 * 1024 ? 1 : 0)} MiB`
  return `${(bytes / 1024 / 1024 / 1024).toFixed(1)} GiB`
}

/** 时长：不足一小时 m:ss，否则 h:mm:ss；无效返回 —。 */
export function formatSpan(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) return '—'
  const s = Math.floor(ms / 1000)
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  const sec = s % 60
  const pad = (n: number) => String(n).padStart(2, '0')
  return h > 0 ? `${h}:${pad(m)}:${pad(sec)}` : `${m}:${pad(sec)}`
}

/** 导出文件名里不允许的字符一律换成下划线，并限制长度。 */
export function safeFileName(name: string, fallback = 'terminal'): string {
  const cleaned = name
    .replace(/[\u0000-\u001f\u007f/\\:*?"<>|]+/g, '_')
    .replace(/\s+/g, ' ')
    .trim()
    .slice(0, 60)
  return cleaned || fallback
}
