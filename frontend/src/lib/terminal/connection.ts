// 单个终端会话的浏览器端连接：取票据 → 建立 WebSocket → 回放 / 实时输出 → 断线按偏移续传。
//
// 这里不碰 DOM / xterm：终端通过 Sink 接口注入、WebSocket 与定时器通过 Env 注入，
// 因此整条状态机（含重连、去重、流控确认）都能在 node:test 里用假对象验证。

import {
  ackFrame,
  clampDim,
  decodeOutput,
  encodeBinaryString,
  encodeInput,
  parseControl,
  resizeFrame,
  SUBPROTOCOL,
  TICKET_PREFIX,
  CLOSE,
  type ReadyFrame,
} from './frames'
import { AckBatcher, OutputAssembler } from './stream'
import { classifyClose, classifyTicketFailure, LAGGING_MESSAGE, type Action, type TicketFailure } from './policy'
import { DiagLog } from './diag'

/**
 * idle 尚未开始；connecting 取票据 / 握手中；live 已附着；waiting 断线后等待重连；
 * ended 会话已结束（不再重连）；failed 需要人工处理（可点「重试」）；closed 用户主动关闭。
 */
export type ConnectionState = 'idle' | 'connecting' | 'live' | 'waiting' | 'ended' | 'failed' | 'closed'

export interface StateInfo {
  attempt: number
  message: string
  /** 会话本身已经结束（收到 exit、附着时即为 exited，或服务端以 4410 关闭）。 */
  over: boolean
  exitCode: number | null
  signal: string
  /** 下一次自动重连的时间戳（毫秒），仅 waiting 状态有值，供界面显示倒计时。 */
  nextRetryAt: number | null
}

export interface SocketLike {
  binaryType: string
  readyState: number
  onopen: ((ev: unknown) => void) | null
  onmessage: ((ev: { data: unknown }) => void) | null
  onclose: ((ev: { code: number; reason: string }) => void) | null
  onerror: ((ev: unknown) => void) | null
  send(data: string | ArrayBufferLike | ArrayBufferView): void
  close(code?: number, reason?: string): void
}

export interface Sink {
  /** 全新附着前清空终端。 */
  reset(): void
  /**
   * 写入会话输出；done 在终端真正渲染（解析）完这段数据后调用，驱动流控确认。
   *
   * replay 为 true 表示这是附着之前就已产生的历史输出（回放）。终端在解析它们时会照常应答其中的查询
   * （设备属性、光标位置、颜色、模式查询……），但这些查询的发起者早就不在等应答了：把应答再写进会话输入，
   * 只会在 shell 提示符或应用里冒出 `^[[?1;2c` 之类的乱码。所以 Sink 必须在解析 replay 数据期间丢弃终端自动生成的应答。
   *
   * kind 为 'prefix' 表示这是附着时主机给的「模式恢复序列」：它是为了让新终端的模式与输出流一致而合成的，不属于会话输出，
   * 需要按字节流整理输出的实现（例如记下写进终端的内容用于重建）要把它和真正的会话输出区分开。省略时就是会话输出。
   */
  write(data: Uint8Array, done: () => void, replay: boolean, kind?: 'prefix'): void
  /** 本地提示（不属于会话输出）。 */
  notice(text: string): void
  /** 终端当前的行列数。 */
  size(): { rows: number; cols: number }
  /**
   * 全新附着时希望主机只回放末尾多少行（rows 是终端当前行数）；省略或返回 0 = 整体回放。
   * 只是请求：主机只在缓冲很大、会话记录又在正常录制（更早的内容读得到）时才照办，旧版本主机会忽略它。
   */
  tailLines?(rows: number): number
  /**
   * 全新附着的回放进度（可选，供界面在回放期间盖遮罩，打开时不让用户看到回放在屏幕上滚动）：
   * start 给出回放总字节数（会话输出里偏移小于 ready.end 的部分）；done 在回放的最后一段被终端解析完成之后调用。
   * 回放为空时 start 之后立即 done。连接中途断开（回放没完成）不会再有 done，界面应在连接状态变化时撤掉遮罩。
   */
  replay?(ev: { phase: 'start'; total: number } | { phase: 'done' }): void
  /** 服务端告知「跟不上输出」（on 为真）或「已经追上」（on 为假）；跟不上持续下去服务端会断开并要求重新附着。 */
  lag?(on: boolean): void
  /** 这个窗格现在是否正被看着（省略 = 是）。被服务端因跟不上输出断开时，看不到的窗格不自动重连，等它再被看到时全新附着。 */
  visible?(): boolean
}

export interface Env {
  wsUrl: string
  openSocket(url: string, protocols: string[]): SocketLike
  /** 申请一次性票据；失败时 reject 一个带 code / status / cancelled 的对象。 */
  getTicket(req: { since: number; rows: number; cols: number; tailLines?: number }): Promise<string>
  setTimer(fn: () => void, ms: number): unknown
  clearTimer(handle: unknown): void
  random?: () => number
  now?: () => number
}

const READY_TIMEOUT_MS = 10_000
const ACK_FLUSH_MS = 40
const OPEN = 1

/** 「跟不上输出」重连等待期间最多攒多少字节的按键、最长攒多久（超过就当作普通断线丢弃）。 */
const TYPE_AHEAD_MAX_BYTES = 512
const TYPE_AHEAD_TTL_MS = 10_000

export class TerminalConnection {
  private ws: SocketLike | null = null
  private _state: ConnectionState = 'idle'
  private attempt = 0
  private ticketFailures = 0
  /** 当前的 failed 状态是否只是传输层暂时性故障（网络恢复后值得自动再试）。 */
  private failTransient = false
  private generation = 0
  private stopped = false
  private everReady = false
  private sessionOver = false
  private exitCode: number | null = null
  private signal = ''
  private closedReason = ''
  private nextRetryAt: number | null = null
  private message = ''
  private lastSentSize = ''
  private lastSize: { rows: number; cols: number } | null = null
  /** 本次附着时会话输出的累计字节数：偏移小于它的都是回放，大于等于它的才是实时输出。 */
  private replayEnd = 0
  /** 下一次连接必须是全新附着（since=0、清屏）：被服务端因跟不上输出断开之后，旧的偏移已经没有意义。 */
  private forceFresh = false
  /** 连续因跟不上输出被断开的次数（连接稳定一阵之后清零），决定下次等多久再附着。 */
  private lagStreak = 0
  private readyAt = 0
  /** 回放还没渲染完（等 Sink.replay 的 done）。 */
  private replayPending = false
  /**
   * 因跟不上输出被断开、等待重新附着的这几秒里，用户敲的键先攒在这里，重新附着成功后按顺序送出——最常见的是想用 Ctrl+C
   * 打断刷屏，按键落在断开的空档里就丢了。只在这种「有计划的短暂中断」里攒（lagWaiting）；普通断线（网络掉了）照旧丢弃，
   * 免得很久以前的输入在恢复之后被送给程序。有总量与时间上限。
   */
  private typeAhead: Uint8Array[] = []
  private typeAheadLen = 0
  private lagWaiting = false
  private lagWaitSince = 0

  private readonly asm = new OutputAssembler()
  private readonly acks = new AckBatcher()

  private retryTimer: unknown = null
  private ackTimer: unknown = null
  private readyTimer: unknown = null

  constructor(
    private readonly env: Env,
    private readonly sink: Sink,
    private readonly onChange: (state: ConnectionState, info: StateInfo) => void,
    /** 诊断事件环（可选）：只记连接生命周期与尺寸，不记终端内容。 */
    private readonly diag?: DiagLog,
  ) {}

  get state(): ConnectionState {
    return this._state
  }

  /** 已经交给终端的累计偏移（重连时向服务端索要的 since）。 */
  get offset(): number {
    return this.asm.nextOffset
  }

  start(): void {
    if (this._state !== 'idle' || this.stopped) return
    void this.connectOnce()
  }

  /** 用户主动关闭（分离）：不再重连。会话本身仍在主机上继续运行。 */
  stop(): void {
    if (this.stopped) return
    this.stopped = true
    this.generation++
    this.dropTypeAhead()
    this.clearTimers()
    const ws = this.ws
    this.ws = null
    if (ws) {
      detach(ws)
      try {
        ws.close(1000, 'detach')
      } catch {
        /* 已经关闭 */
      }
    }
    this.setState('closed', '')
  }

  /** 立即重连（标签页重新可见、网络恢复、用户点了「重试」）。只在 waiting / failed 时有效。 */
  retryNow(): void {
    if (this.stopped || this.sessionOver) return
    if (this._state !== 'waiting' && this._state !== 'failed') return
    this.clearTimers()
    this.attempt = 0
    this.ticketFailures = 0
    void this.connectOnce()
  }

  /**
   * 环境好转的信号（网络恢复、标签页重新可见）：连接正在等待重连，或是因为暂时性的传输故障而暂停时，立刻再试一次。
   * 需要人工处理的失败（总闸关闭、主机被停用、登录失效、用户取消解锁……）不会被它悄悄重连。
   */
  resumeIfPossible(): void {
    if (this._state === 'waiting' || (this._state === 'failed' && this.failTransient)) this.retryNow()
  }

  /** 键盘输入 / 粘贴文本。未附着或会话已结束时丢弃并返回 false，由界面提示。 */
  sendText(text: string): boolean {
    const frames = encodeInput(text)
    return this.sendFrames(frames) || this.holdTypeAhead(frames)
  }

  /** xterm 的 onBinary 数据（每字符一字节）。 */
  sendBinary(data: string): boolean {
    return this.sendFrames(encodeBinaryString(data))
  }

  resize(rows: number, cols: number): void {
    rows = clampDim(rows)
    cols = clampDim(cols)
    this.lastSize = { rows, cols }
    this.sendSize()
  }

  // ---------- 内部 ----------

  /** 「跟不上输出」重连等待期间攒下按键（不记内容，诊断里只有字节数）。攒住返回 true。 */
  private holdTypeAhead(frames: Uint8Array[]): boolean {
    if (!this.lagWaiting || this.stopped || this.sessionOver) return false
    const now = this.env.now ? this.env.now() : Date.now()
    if (now - this.lagWaitSince > TYPE_AHEAD_TTL_MS) {
      this.dropTypeAhead()
      return false
    }
    let n = 0
    for (const f of frames) n += f.length
    if (this.typeAheadLen + n > TYPE_AHEAD_MAX_BYTES) return false
    for (const f of frames) this.typeAhead.push(f)
    this.typeAheadLen += n
    this.diag?.add('type-ahead', { bytes: this.typeAheadLen })
    return true
  }

  private dropTypeAhead(): void {
    this.typeAhead = []
    this.typeAheadLen = 0
    this.lagWaiting = false
  }

  /** 重新附着成功（live）之后，把攒下的按键按顺序送出。 */
  private flushTypeAhead(): void {
    const frames = this.typeAhead
    const since = this.lagWaitSince
    this.typeAhead = []
    this.typeAheadLen = 0
    this.lagWaiting = false
    if (frames.length === 0) return
    // 等了太久（重连一直不成功）才连上：这些按键已经过期，不再补发
    const now = this.env.now ? this.env.now() : Date.now()
    if (now - since > TYPE_AHEAD_TTL_MS) return
    this.diag?.add('type-ahead-flush', { frames: frames.length })
    this.sendFrames(frames)
  }

  private sendFrames(frames: Uint8Array[]): boolean {
    const ws = this.ws
    if (!ws || ws.readyState !== OPEN || this._state !== 'live' || this.sessionOver) return false
    try {
      for (const f of frames) ws.send(f)
      return true
    } catch {
      return false
    }
  }

  private sendSize(): void {
    const ws = this.ws
    if (!ws || ws.readyState !== OPEN || this._state !== 'live' || !this.lastSize) return
    const key = `${this.lastSize.rows}x${this.lastSize.cols}`
    if (key === this.lastSentSize) return
    try {
      ws.send(resizeFrame(this.lastSize.rows, this.lastSize.cols))
      this.lastSentSize = key
    } catch {
      /* 断线由 onclose 处理 */
    }
  }

  private async connectOnce(): Promise<void> {
    const gen = ++this.generation
    this.nextRetryAt = null
    this.failTransient = false
    this.setState('connecting', this.attempt > 0 ? '正在重新连接…' : '正在连接…')

    const size = this.sink.size()
    const since = this.everReady && !this.forceFresh ? this.asm.nextOffset : 0
    // 「只回放末尾若干行」是给全新附着的；续传从 since 接着发，用不着。但续传时缓冲若已被越过（truncated），
    // 主机也会按全新附着处理，所以这个请求始终带上（旧版本主机只在 since=0 时才看它，多带无害）。
    const tail = Math.floor(this.sink.tailLines?.(clampDim(size.rows)) ?? 0)
    this.diag?.add('connect', { since, rows: clampDim(size.rows), cols: clampDim(size.cols), attempt: this.attempt, fresh: since === 0 })
    let ticket: string
    try {
      ticket = await this.env.getTicket({
        since,
        rows: clampDim(size.rows),
        cols: clampDim(size.cols),
        ...(tail > 0 ? { tailLines: tail } : {}),
      })
    } catch (e) {
      if (gen !== this.generation || this.stopped) return
      this.ticketFailures++
      this.apply(classifyTicketFailure(toTicketFailure(e), this.attempt, this.ticketFailures, this.env.random))
      return
    }
    if (gen !== this.generation || this.stopped) return

    let ws: SocketLike
    try {
      ws = this.env.openSocket(this.env.wsUrl, [SUBPROTOCOL, TICKET_PREFIX + ticket])
    } catch (e) {
      this.apply({ kind: 'fail', message: `无法建立 WebSocket：${errText(e)}` })
      return
    }
    ws.binaryType = 'arraybuffer'
    this.ws = ws
    this.lastSentSize = ''
    this.closedReason = ''
    ws.onmessage = ev => {
      if (gen === this.generation) this.onMessage(ev.data, since)
    }
    ws.onclose = ev => {
      if (gen === this.generation) this.onClose(ev.code, ev.reason)
    }
    ws.onerror = () => {
      /* 随后一定会触发 onclose，统一在那里处理 */
    }
    this.readyTimer = this.env.setTimer(() => {
      if (gen !== this.generation || this._state === 'live') return
      // 握手迟迟没有 ready：按异常断线处理。
      detach(ws)
      try {
        ws.close(4000, 'ready timeout')
      } catch {
        /* ignore */
      }
      this.ws = null
      this.apply(classifyClose(1006, this.attempt, '', this.env.random))
    }, READY_TIMEOUT_MS)
  }

  private onMessage(data: unknown, requestedSince: number): void {
    if (typeof data === 'string') {
      const f = parseControl(data)
      if (!f) return
      switch (f.t) {
        case 'ready':
          this.onReady(f, requestedSince)
          break
        case 'exit':
          this.sessionOver = true
          this.exitCode = f.code
          this.signal = f.signal
          this.setState(this._state, this.message) // 保持附着直到服务端关闭，让 exit 前的输出都能渲染完
          break
        case 'closed':
          this.closedReason = f.reason
          break
        case 'lag':
          this.diag?.add('lag', { on: f.on })
          this.sink.lag?.(f.on)
          break
      }
      return
    }
    let bytes: Uint8Array
    if (data instanceof ArrayBuffer) bytes = new Uint8Array(data)
    else if (ArrayBuffer.isView(data)) bytes = new Uint8Array(data.buffer, data.byteOffset, data.byteLength)
    else return
    const out = decodeOutput(bytes)
    if (!out) return
    this.onOutput(out.offset, out.payload)
  }

  private onReady(f: ReadyFrame, requestedSince: number): void {
    if (this.readyTimer !== null) {
      this.env.clearTimer(this.readyTimer)
      this.readyTimer = null
    }
    // 续传时缓冲已被越过（truncated）：中间缺了一段，终端里旧画面的光标位置 / 擦除范围已经和新输出对不上，
    // 把回放直接写在旧画面上只会得到错位的残影——按全新附着处理（清屏 + 回放），更早的内容在历史里。
    const fresh = !this.everReady || requestedSince === 0 || f.truncated || f.from > requestedSince
    this.replayEnd = f.end
    this.forceFresh = false
    this.readyAt = this.env.now ? this.env.now() : Date.now()
    this.sink.lag?.(false)
    this.diag?.add('ready', {
      from: f.from,
      end: f.end,
      fresh,
      truncated: f.truncated,
      state: f.state,
      rows: f.rows,
      cols: f.cols,
    })
    if (fresh) {
      if (this.everReady && requestedSince > 0 && f.from > requestedSince) {
        this.sink.notice(`断线期间有 ${f.from - requestedSince} 字节输出已超出缓冲，已重新载入最新内容（更早的在历史里）`)
      }
      this.sink.reset()
      if (f.prefix.length > 0) this.sink.write(f.prefix, noop, true, 'prefix')
      this.asm.start(f.from)
      this.replayPending = f.end > f.from
      this.sink.replay?.({ phase: 'start', total: Math.max(0, f.end - f.from) })
      if (!this.replayPending) this.sink.replay?.({ phase: 'done' })
    } else {
      // 续传：服务端从 since 开始重放（缓冲没被越过，所以一定等于 since）。
      this.replayPending = false
      this.asm.start(Math.max(f.from, requestedSince))
    }
    this.acks.reset(f.from)
    this.everReady = true
    this.attempt = 0
    this.ticketFailures = 0
    if (f.state === 'exited') {
      this.sessionOver = true
      this.exitCode = f.exitCode
    }
    this.setState('live', '')
    this.sendSize()
    this.flushTypeAhead()
  }

  private onOutput(offset: number, payload: Uint8Array): void {
    const gen = this.generation
    const r = this.asm.push(offset, payload)
    if (r.data === null) return
    if (r.gap > 0) this.sink.notice(`有 ${r.gap} 字节输出丢失`)
    // 一个帧可能横跨「回放 / 实时」的分界线（附着瞬间恰好有新输出），拆成两段分别标记。
    const start = r.end - r.data.length
    const cut = Math.min(Math.max(this.replayEnd - start, 0), r.data.length)
    if (cut > 0) this.writeChunk(r.data.subarray(0, cut), start + cut, true, gen)
    if (cut < r.data.length) this.writeChunk(cut > 0 ? r.data.subarray(cut) : r.data, r.end, false, gen)
  }

  private writeChunk(data: Uint8Array, end: number, replay: boolean, gen: number): void {
    try {
      this.sink.write(
        data,
        () => {
          if (gen !== this.generation) return
          if (replay && this.replayPending && end >= this.replayEnd) {
            this.replayPending = false
            this.diag?.add('replay-done', { end })
            this.sink.replay?.({ phase: 'done' })
          }
          const a = this.acks.onRendered(end)
          if (a !== null) this.sendAck(a)
          else this.armAckTimer()
        },
        replay,
      )
    } catch {
      /* 终端已被销毁 */
    }
  }

  private armAckTimer(): void {
    if (this.ackTimer !== null || !this.acks.pending) return
    const gen = this.generation
    this.ackTimer = this.env.setTimer(() => {
      this.ackTimer = null
      if (gen !== this.generation) return
      const a = this.acks.flush()
      if (a !== null) this.sendAck(a)
    }, ACK_FLUSH_MS)
  }

  private sendAck(offset: number): void {
    const ws = this.ws
    if (!ws || ws.readyState !== OPEN) return
    try {
      ws.send(ackFrame(offset))
    } catch {
      /* 断线由 onclose 处理 */
    }
  }

  private onClose(code: number, reason: string): void {
    this.ws = null
    this.clearTimers()
    this.diag?.add('close', { code, reason })
    if (this.stopped) return
    if (this.sessionOver || code === 4410) {
      this.sessionOver = true
      this.setState('ended', endMessage(reason || this.closedReason))
      return
    }
    if (code === CLOSE.lagging) {
      this.onLagging()
      return
    }
    // 其余断开：「正在追赶」的遮罩没有意义了；回放没渲染完也不会再有 done，让界面撤掉载入遮罩；攒着的按键也作废（普通断线不补发）。
    this.sink.lag?.(false)
    this.replayPending = false
    this.dropTypeAhead()
    this.apply(classifyClose(code, this.attempt, this.closedReason || friendlyReason(reason), this.env.random))
  }

  /** 服务端因为跟不上输出断开了本次附着：下次全新附着（清屏 + 只回放末尾），看不到的窗格先不连。 */
  private onLagging(): void {
    const now = this.env.now ? this.env.now() : Date.now()
    // 这条连接稳定活过一阵（>20 秒）才被断开，不算「连续」
    this.lagStreak = this.readyAt > 0 && now - this.readyAt >= 20_000 ? 1 : this.lagStreak + 1
    this.forceFresh = true
    this.replayPending = false
    this.diag?.add('lagging', { streak: this.lagStreak })
    if (this.sink.visible && !this.sink.visible()) {
      // 后台窗格：不自动重连（否则它会一直在后台反复追赶、白白占带宽与 CPU），再被看到时 resumeIfPossible 全新附着。
      this.sink.lag?.(false)
      this.apply({ kind: 'fail', transient: true, message: '输出过快，已暂停后台同步（切回来时会自动跳到最新输出）' })
      return
    }
    this.lagWaiting = true
    this.lagWaitSince = now
    this.apply(classifyClose(CLOSE.lagging, this.lagStreak, LAGGING_MESSAGE, this.env.random))
  }

  private apply(a: Action): void {
    this.clearRetry()
    switch (a.kind) {
      case 'end':
        this.sessionOver = true
        this.setState('ended', a.message)
        return
      case 'fail':
        this.failTransient = a.transient === true
        this.setState('failed', a.message)
        return
      case 'retry': {
        this.attempt++
        const now = this.env.now ? this.env.now() : Date.now()
        this.nextRetryAt = now + a.delayMs
        this.setState('waiting', a.message)
        const gen = this.generation
        this.retryTimer = this.env.setTimer(() => {
          this.retryTimer = null
          if (gen !== this.generation || this.stopped) return
          void this.connectOnce()
        }, a.delayMs)
      }
    }
  }

  private clearRetry(): void {
    if (this.retryTimer !== null) {
      this.env.clearTimer(this.retryTimer)
      this.retryTimer = null
    }
  }

  private clearTimers(): void {
    this.clearRetry()
    if (this.ackTimer !== null) {
      this.env.clearTimer(this.ackTimer)
      this.ackTimer = null
    }
    if (this.readyTimer !== null) {
      this.env.clearTimer(this.readyTimer)
      this.readyTimer = null
    }
  }

  private setState(state: ConnectionState, message: string): void {
    this._state = state
    this.message = message
    if (state === 'failed' || state === 'ended' || state === 'closed') this.dropTypeAhead()
    if (state !== 'waiting') this.nextRetryAt = null
    this.onChange(state, {
      attempt: this.attempt,
      message,
      over: this.sessionOver,
      exitCode: this.exitCode,
      signal: this.signal,
      nextRetryAt: this.nextRetryAt,
    })
  }
}

function noop(): void {
  /* prefix 不属于输出流偏移，不需要确认 */
}

function detach(ws: SocketLike): void {
  ws.onmessage = null
  ws.onclose = null
  ws.onerror = null
  ws.onopen = null
}

function errText(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}

function toTicketFailure(e: unknown): TicketFailure {
  if (e && typeof e === 'object') {
    const o = e as Record<string, unknown>
    return {
      code: typeof o.code === 'string' ? o.code : undefined,
      status: typeof o.status === 'number' ? o.status : undefined,
      cancelled: o.cancelled === true,
      message: typeof o.message === 'string' ? o.message : undefined,
    }
  }
  return { message: String(e) }
}

/** 服务端关闭帧里的原因是简短英文词（exited / killed / stalled …），翻成人话。 */
function friendlyReason(reason: string): string {
  switch (reason) {
    case 'stalled':
      return '渲染跟不上输出，已断开，正在重连'
    case 'credentials changed':
      return '登录凭据已变更，请重新登录后连接'
    default:
      return ''
  }
}

function endMessage(reason: string): string {
  switch (reason) {
    case 'killed':
      return '会话已被终止'
    case 'exited':
    case '':
      return '会话已结束'
    default:
      return reason
  }
}
