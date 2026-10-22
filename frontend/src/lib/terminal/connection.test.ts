// 运行：pnpm --dir frontend run test:unit
// 用假 WebSocket / 假定时器驱动 TerminalConnection 的整条状态机。
import assert from 'node:assert/strict'
import { beforeEach, describe, it } from 'node:test'
import { TerminalConnection, type ConnectionState, type Env, type SocketLike, type StateInfo } from './connection'
import { DiagLog } from './diag'
import { MAX_INPUT_PAYLOAD } from './frames'
import { MAX_TICKET_FAILURES } from './policy'

class FakeSocket implements SocketLike {
  binaryType = 'blob'
  readyState = 1
  onopen: ((ev: unknown) => void) | null = null
  onmessage: ((ev: { data: unknown }) => void) | null = null
  onclose: ((ev: { code: number; reason: string }) => void) | null = null
  onerror: ((ev: unknown) => void) | null = null
  sent: (string | Uint8Array)[] = []
  closedWith: { code?: number; reason?: string } | null = null

  constructor(
    readonly url: string,
    readonly protocols: string[],
  ) {}

  send(data: string | ArrayBufferLike | ArrayBufferView): void {
    this.sent.push(typeof data === 'string' ? data : (data as Uint8Array))
  }

  close(code?: number, reason?: string): void {
    this.closedWith = { code, reason }
    this.readyState = 3
  }

  text(obj: unknown): void {
    this.onmessage?.({ data: JSON.stringify(obj) })
  }

  output(offset: number, payload: string | number[]): void {
    const body = typeof payload === 'string' ? new TextEncoder().encode(payload) : Uint8Array.from(payload)
    const buf = new Uint8Array(9 + body.length)
    buf[0] = 0x01
    new DataView(buf.buffer).setBigUint64(1, BigInt(offset), false)
    buf.set(body, 9)
    this.onmessage?.({ data: buf.buffer })
  }

  serverClose(code: number, reason = ''): void {
    this.readyState = 3
    this.onclose?.({ code, reason })
  }

  get controlSent(): Record<string, unknown>[] {
    return this.sent.filter((s): s is string => typeof s === 'string').map(s => JSON.parse(s))
  }

  get inputSent(): Uint8Array[] {
    return this.sent.filter((s): s is Uint8Array => typeof s !== 'string')
  }
}

class FakeTimers {
  now = 0
  private seq = 0
  private timers = new Map<number, { at: number; fn: () => void }>()

  setTimer = (fn: () => void, ms: number): unknown => {
    const id = ++this.seq
    this.timers.set(id, { at: this.now + ms, fn })
    return id
  }

  clearTimer = (h: unknown): void => {
    this.timers.delete(h as number)
  }

  get pendingCount(): number {
    return this.timers.size
  }

  advance(ms: number): void {
    const target = this.now + ms
    for (;;) {
      let nextId = -1
      let nextAt = Infinity
      for (const [id, t] of this.timers) {
        if (t.at <= target && t.at < nextAt) {
          nextAt = t.at
          nextId = id
        }
      }
      if (nextId < 0) break
      const t = this.timers.get(nextId)!
      this.timers.delete(nextId)
      this.now = t.at
      t.fn()
    }
    this.now = target
  }
}

interface Harness {
  conn: TerminalConnection
  timers: FakeTimers
  sockets: FakeSocket[]
  tickets: { since: number; rows: number; cols: number; tailLines?: number }[]
  states: { state: ConnectionState; info: StateInfo }[]
  writes: string[]
  /** 与 writes 一一对应：该次写入是否被标记为回放。 */
  replays: boolean[]
  /** 与 writes 一一对应：该次写入的种类（'prefix' = 附着时的模式恢复序列；省略 = 会话输出）。 */
  kinds: Array<'prefix' | undefined>
  notices: string[]
  resets: { n: number }
  ticketImpl: { fn: (req: { since: number; rows: number; cols: number; tailLines?: number }) => Promise<string> }
  deferWrites: { on: boolean; pending: (() => void)[] }
  /** Sink.replay 收到的事件序列。 */
  replayEvents: Array<{ phase: 'start'; total: number } | { phase: 'done' }>
  /** Sink.lag 收到的取值序列。 */
  lagEvents: boolean[]
  visible: { on: boolean }
  diag: DiagLog
  last: () => FakeSocket
}

const tick = () => new Promise<void>(r => setImmediate(r))

function harness(opts: { tailLines?: (rows: number) => number; withVisible?: boolean } = {}): Harness {
  const timers = new FakeTimers()
  const sockets: FakeSocket[] = []
  const tickets: Harness['tickets'] = []
  const states: Harness['states'] = []
  const writes: string[] = []
  const replays: boolean[] = []
  const kinds: Array<'prefix' | undefined> = []
  const notices: string[] = []
  const resets = { n: 0 }
  const deferWrites = { on: false, pending: [] as (() => void)[] }
  const replayEvents: Harness['replayEvents'] = []
  const lagEvents: boolean[] = []
  const visible = { on: true }
  const diag = new DiagLog(200, () => timers.now)
  let ticketSeq = 0
  const ticketImpl = {
    fn: async (req: { since: number; rows: number; cols: number; tailLines?: number }): Promise<string> => {
      tickets.push(req)
      return `ticket-${++ticketSeq}`
    },
  }
  const env: Env = {
    wsUrl: 'wss://hub.test/api/v1/terminal/ws',
    openSocket: (url, protocols) => {
      const s = new FakeSocket(url, protocols)
      sockets.push(s)
      return s
    },
    getTicket: req => ticketImpl.fn(req),
    setTimer: timers.setTimer,
    clearTimer: timers.clearTimer,
    random: () => 0.5,
    now: () => timers.now,
  }
  const dec = new TextDecoder()
  const conn = new TerminalConnection(
    env,
    {
      reset: () => {
        resets.n++
      },
      write: (data, done, replay, kind) => {
        writes.push(dec.decode(data))
        replays.push(replay)
        kinds.push(kind)
        if (deferWrites.on) deferWrites.pending.push(done)
        else done()
      },
      notice: t => notices.push(t),
      size: () => ({ rows: 24, cols: 80 }),
      replay: ev => replayEvents.push(ev),
      lag: on => lagEvents.push(on),
      ...(opts.withVisible ? { visible: () => visible.on } : {}),
      ...(opts.tailLines ? { tailLines: opts.tailLines } : {}),
    },
    (state, info) => states.push({ state, info }),
    diag,
  )
  return {
    conn, timers, sockets, tickets, states, writes, replays, kinds, notices, resets, ticketImpl, deferWrites,
    replayEvents, lagEvents, visible, diag,
    last: () => sockets[sockets.length - 1],
  }
}

function ready(extra: Record<string, unknown> = {}) {
  return { t: 'ready', sid: 's1', base: 0, end: 0, from: 0, rows: 24, cols: 80, state: 'running', ...extra }
}

describe('TerminalConnection：全新附着', () => {
  let h: Harness
  beforeEach(() => {
    h = harness()
  })

  it('带票据子协议连接，ready 后进入 live，并先重置终端再写 prefix', async () => {
    h.conn.start()
    await tick()
    assert.deepEqual(h.tickets, [{ since: 0, rows: 24, cols: 80 }])
    const s = h.last()
    assert.equal(s.url, 'wss://hub.test/api/v1/terminal/ws')
    assert.deepEqual(s.protocols, ['relaymesh.term.v1', 'tk.ticket-1'])
    assert.equal(s.binaryType, 'arraybuffer')
    assert.equal(h.conn.state, 'connecting')

    s.text(ready({ prefix: btoa('\x1b[?1049h'), from: 100, end: 100 }))
    assert.equal(h.conn.state, 'live')
    assert.equal(h.resets.n, 1)
    assert.deepEqual(h.writes, ['\x1b[?1049h'])
    assert.equal(h.conn.offset, 100)
  })

  it('模式恢复序列以 prefix 种类写入，之后的会话输出不带种类（供输出带区分两者）', async () => {
    h.conn.start()
    await tick()
    const s = h.last()
    s.text(ready({ prefix: btoa('\x1b[?2004h'), from: 5, end: 5 }))
    s.output(5, 'out')
    assert.deepEqual(h.writes, ['\x1b[?2004h', 'out'])
    assert.deepEqual(h.kinds, ['prefix', undefined])
  })

  it('输出写入终端，渲染完成后批量确认（低流量由定时器兜底）', async () => {
    h.conn.start()
    await tick()
    const s = h.last()
    s.text(ready())
    s.output(0, 'hello')
    assert.deepEqual(h.writes, ['hello'])
    assert.equal(s.controlSent.filter(c => c.t === 'ack').length, 0, '未到阈值不应立刻确认')
    h.timers.advance(50)
    assert.deepEqual(s.controlSent.filter(c => c.t === 'ack'), [{ t: 'ack', offset: 5 }])
  })

  it('累计超过阈值立即确认', async () => {
    h.conn.start()
    await tick()
    const s = h.last()
    s.text(ready())
    const chunk = 'x'.repeat(20 * 1024)
    s.output(0, chunk)
    s.output(chunk.length, chunk)
    const acks = s.controlSent.filter(c => c.t === 'ack')
    assert.equal(acks.length, 1)
    assert.equal(acks[0].offset, 40 * 1024)
  })

  it('终端尚未渲染完时不确认（流控由渲染进度驱动）', async () => {
    h.conn.start()
    await tick()
    const s = h.last()
    s.text(ready())
    h.deferWrites.on = true
    s.output(0, 'x'.repeat(64 * 1024))
    h.timers.advance(200)
    assert.equal(s.controlSent.filter(c => c.t === 'ack').length, 0)
    h.deferWrites.pending.forEach(f => f())
    assert.equal(s.controlSent.filter(c => c.t === 'ack').length, 1)
  })

  it('重复的输出帧被去重', async () => {
    h.conn.start()
    await tick()
    const s = h.last()
    s.text(ready())
    s.output(0, 'abcdef')
    s.output(0, 'abcdef')
    s.output(3, 'defGHI')
    assert.deepEqual(h.writes, ['abcdef', 'GHI'])
    assert.equal(h.conn.offset, 9)
  })

  it('exit 后以 4410 关闭：会话结束，携带退出码，不再重连', async () => {
    h.conn.start()
    await tick()
    const s = h.last()
    s.text(ready())
    s.text({ t: 'exit', code: 7 })
    assert.equal(h.states[h.states.length - 1].info.over, true)
    s.serverClose(4410, 'exited')
    const last = h.states[h.states.length - 1]
    assert.equal(last.state, 'ended')
    assert.equal(last.info.exitCode, 7)
    h.timers.advance(60_000)
    await tick()
    assert.equal(h.tickets.length, 1)
  })

  it('附着到已退出的会话：ready 即标记结束，输入被丢弃', async () => {
    h.conn.start()
    await tick()
    const s = h.last()
    s.text(ready({ state: 'exited', exit_code: 0 }))
    assert.equal(h.states[h.states.length - 1].info.over, true)
    assert.equal(h.conn.sendText('x'), false)
  })
})

describe('TerminalConnection：回放与实时输出的区分', () => {
  let h: Harness
  beforeEach(() => {
    h = harness()
  })

  it('附着前已产生的输出（含 prefix）标记为回放，之后的输出是实时', async () => {
    h.conn.start()
    await tick()
    const s = h.last()
    s.text(ready({ prefix: btoa('\x1b[?1049h'), base: 0, from: 0, end: 10 }))
    s.output(0, 'history-01') // 偏移 0..10 = 回放
    s.output(10, 'live') // 偏移 10.. = 实时
    assert.deepEqual(h.writes, ['\x1b[?1049h', 'history-01', 'live'])
    assert.deepEqual(h.replays, [true, true, false])
  })

  it('跨越分界线的帧被拆成回放 + 实时两段，确认偏移仍然连续', async () => {
    h.conn.start()
    await tick()
    const s = h.last()
    s.text(ready({ from: 0, end: 4 }))
    s.output(0, 'OLD-new!')
    assert.deepEqual(h.writes, ['OLD-', 'new!'])
    assert.deepEqual(h.replays, [true, false])
    assert.equal(h.conn.offset, 8)
    h.timers.advance(50)
    assert.deepEqual(s.controlSent.filter(c => c.t === 'ack'), [{ t: 'ack', offset: 8 }])
  })

  it('没有历史（end = from）时全部是实时输出', async () => {
    h.conn.start()
    await tick()
    const s = h.last()
    s.text(ready({ from: 100, end: 100 }))
    s.output(100, 'fresh')
    assert.deepEqual(h.replays, [false])
  })

  it('历史被环形缓冲覆盖（from > 期望位置）时，缺口之后的回放仍然标记为回放', async () => {
    h.conn.start()
    await tick()
    const s = h.last()
    s.text(ready({ base: 50, from: 50, end: 60, truncated: true }))
    s.output(50, 'abcdefghij')
    s.output(60, 'Z')
    assert.deepEqual(h.replays, [true, false])
  })

  it('断线续传：断线期间错过的输出是回放，续传之后的输出是实时；重复帧不会被误标', async () => {
    h.conn.start()
    await tick()
    const s = h.last()
    s.text(ready({ from: 0, end: 0 }))
    s.output(0, 'hello') // 实时
    s.serverClose(1006)
    h.timers.advance(10_000)
    await tick()
    const s2 = h.last()
    assert.notEqual(s2, s)
    // 服务端从 since=5 开始补发，附着时会话输出已到 12。
    s2.text(ready({ from: 5, end: 12 }))
    s2.output(0, 'hello') // 与已渲染的重复，丢弃
    s2.output(5, 'missed!') // 5..12 = 断线期间错过的输出 → 回放
    s2.output(12, 'now') // 实时
    assert.deepEqual(h.writes, ['hello', 'missed!', 'now'])
    assert.deepEqual(h.replays, [false, true, false])
  })
})

describe('TerminalConnection：断线续传', () => {
  let h: Harness
  beforeEach(() => {
    h = harness()
  })

  async function liveWith(output: string): Promise<FakeSocket> {
    h.conn.start()
    await tick()
    const s = h.last()
    s.text(ready())
    s.output(0, output)
    return s
  }

  it('异常断线后按退避重连，since 取已交付偏移，且不重置终端', async () => {
    const s1 = await liveWith('hello world')
    s1.serverClose(1006)
    assert.equal(h.conn.state, 'waiting')
    assert.ok(h.states[h.states.length - 1].info.nextRetryAt !== null)

    h.timers.advance(499)
    await tick()
    assert.equal(h.tickets.length, 1, '退避期内不应重连')
    h.timers.advance(2)
    await tick()
    assert.equal(h.tickets.length, 2)
    assert.equal(h.tickets[1].since, 11)

    const s2 = h.last()
    assert.notEqual(s2, s1)
    s2.text(ready({ from: 11, end: 20, prefix: btoa('SHOULD-NOT-MATTER') }))
    assert.equal(h.resets.n, 1, '续传不应重置终端')
    assert.equal(h.writes.includes('SHOULD-NOT-MATTER'), false, '续传不应再写 prefix')
    s2.output(11, '!!!')
    assert.deepEqual(h.writes, ['hello world', '!!!'])
    assert.equal(h.conn.state, 'live')
  })

  it('历史已被覆盖（from > since）：中间缺了一段，按全新附着清屏重建，并提示丢失字节数', async () => {
    const s1 = await liveWith('abc')
    const resetsBefore = h.resets.n
    s1.serverClose(1006)
    h.timers.advance(1000)
    await tick()
    const s2 = h.last()
    s2.text(ready({ from: 1000, base: 1000, end: 1004, truncated: true, prefix: btoa('\x1b[?25h') }))
    assert.equal(h.resets.n, resetsBefore + 1, '缺了一段的续传不能叠在旧画面上：必须先清屏')
    assert.equal(h.notices.length, 1)
    assert.match(h.notices[0], /997/)
    assert.deepEqual(h.kinds.slice(-1), ['prefix'], '清屏后要先写模式恢复序列')
    s2.output(1000, 'zzzz')
    assert.equal(h.conn.offset, 1004)
    assert.equal(h.writes[h.writes.length - 1], 'zzzz')
  })

  it('续传时服务端标 truncated（即使 from 没有越过 since）也按全新附着处理', async () => {
    const s1 = await liveWith('abc')
    const resetsBefore = h.resets.n
    s1.serverClose(1006)
    h.timers.advance(1000)
    await tick()
    const s2 = h.last()
    s2.text(ready({ from: 3, base: 0, end: 5, truncated: true }))
    assert.equal(h.resets.n, resetsBefore + 1)
  })

  it('服务端从更早的位置重放时只写入新增部分', async () => {
    const s1 = await liveWith('abcdef')
    s1.serverClose(1006)
    h.timers.advance(1000)
    await tick()
    const s2 = h.last()
    s2.text(ready({ from: 2, end: 10 }))
    s2.output(2, 'cdefGH')
    assert.deepEqual(h.writes, ['abcdef', 'GH'])
  })

  it('重连成功后退避计数清零', async () => {
    const s1 = await liveWith('a')
    s1.serverClose(1006)
    h.timers.advance(600)
    await tick()
    const s2 = h.last()
    s2.serverClose(1006) // 第二次失败
    h.timers.advance(1100)
    await tick()
    const s3 = h.last()
    s3.text(ready({ from: 1 }))
    s3.serverClose(1006)
    // 成功附着后再断线，应回到最短退避（500ms）
    h.timers.advance(550)
    await tick()
    assert.equal(h.tickets.length, 4)
  })

  it('4413 / 4412 这类瞬时关闭很快重连', async () => {
    const s1 = await liveWith('a')
    s1.serverClose(4412, 'stalled')
    h.timers.advance(250)
    await tick()
    assert.equal(h.tickets.length, 2)
  })

  it('4403 总闸关闭：失败，不再重连', async () => {
    const s1 = await liveWith('a')
    s1.serverClose(4403)
    assert.equal(h.conn.state, 'failed')
    h.timers.advance(120_000)
    await tick()
    assert.equal(h.tickets.length, 1)
  })

  it('握手阶段收不到 ready：超时后按断线处理并重试', async () => {
    h.conn.start()
    await tick()
    const s = h.last()
    h.timers.advance(10_001)
    assert.equal(s.closedWith?.code, 4000)
    assert.equal(h.conn.state, 'waiting')
    h.timers.advance(1000)
    await tick()
    assert.equal(h.tickets.length, 2)
  })

  it('retryNow 立即重连，并重置退避', async () => {
    const s1 = await liveWith('a')
    s1.serverClose(1006)
    assert.equal(h.conn.state, 'waiting')
    h.conn.retryNow()
    await tick()
    assert.equal(h.tickets.length, 2)
  })

  it('旧连接的迟到事件不会影响新连接', async () => {
    const s1 = await liveWith('a')
    s1.serverClose(1006)
    h.timers.advance(600)
    await tick()
    const s2 = h.last()
    s2.text(ready({ from: 1 }))
    s1.onclose?.({ code: 1006, reason: '' }) // 迟到的重复关闭事件
    s1.output(1, 'stale')
    assert.equal(h.conn.state, 'live')
    assert.equal(h.writes.includes('stale'), false)
  })
})

describe('TerminalConnection：票据失败', () => {
  let h: Harness
  beforeEach(() => {
    h = harness()
  })

  it('会话不存在：直接结束', async () => {
    h.ticketImpl.fn = async () => {
      throw { code: 'session_not_found', status: 404 }
    }
    h.conn.start()
    await tick()
    assert.equal(h.conn.state, 'ended')
    assert.equal(h.sockets.length, 0)
  })

  it('主机离线：等待并慢速重试，恢复后连上', async () => {
    let calls = 0
    h.ticketImpl.fn = async () => {
      calls++
      if (calls === 1) throw { code: 'host_offline', status: 409 }
      return 'late-ticket'
    }
    h.conn.start()
    await tick()
    assert.equal(h.conn.state, 'waiting')
    h.timers.advance(5000)
    await tick()
    assert.equal(calls, 2)
    assert.deepEqual(h.last().protocols, ['relaymesh.term.v1', 'tk.late-ticket'])
  })

  it('用户取消解锁：失败，可点重试', async () => {
    let cancel = true
    h.ticketImpl.fn = async () => {
      if (cancel) throw { cancelled: true, message: 'cancelled' }
      return 'ok-ticket'
    }
    h.conn.start()
    await tick()
    assert.equal(h.conn.state, 'failed')
    cancel = false
    h.conn.retryNow()
    await tick()
    assert.equal(h.sockets.length, 1)
  })

  it('网络错误连续失败很多次后暂停（不是几次就放弃）', async () => {
    let calls = 0
    h.ticketImpl.fn = async () => {
      calls++
      throw new Error('Network Error')
    }
    h.conn.start()
    for (let i = 0; i < 60 && h.conn.state !== 'failed'; i++) {
      await tick()
      h.timers.advance(11_000)
    }
    await tick()
    assert.equal(h.conn.state, 'failed')
    assert.equal(calls, MAX_TICKET_FAILURES, '应当先尝试满上限次数再暂停')
  })

  it('一分钟级的服务端中断：一直重试，服务端回来后自动连上，无需人工点击', async () => {
    let up = false
    let calls = 0
    h.ticketImpl.fn = async () => {
      calls++
      if (!up) throw new Error('Network Error')
      return 'back-online'
    }
    h.conn.start()
    // 模拟 90 秒不可用（旧策略在约 25 秒时就已经放弃）
    for (let elapsed = 0; elapsed < 90_000; elapsed += 1_000) {
      await tick()
      h.timers.advance(1_000)
    }
    await tick()
    assert.equal(h.conn.state, 'waiting', '90 秒内仍应处于自动重试')
    up = true
    for (let i = 0; i < 12 && h.sockets.length === 0; i++) {
      await tick()
      h.timers.advance(1_000)
    }
    await tick()
    assert.equal(h.sockets.length, 1, '服务端恢复后应自动建立连接')
    assert.deepEqual(h.last().protocols, ['relaymesh.term.v1', 'tk.back-online'])
    assert.ok(calls > 5)
  })

  it('暂停之后网络恢复 / 标签页重新可见：resumeIfPossible 立即重连', async () => {
    let up = false
    h.ticketImpl.fn = async () => {
      if (!up) throw new Error('Network Error')
      return 'resumed'
    }
    h.conn.start()
    for (let i = 0; i < 60 && h.conn.state !== 'failed'; i++) {
      await tick()
      h.timers.advance(11_000)
    }
    await tick()
    assert.equal(h.conn.state, 'failed')
    up = true
    h.conn.resumeIfPossible()
    await tick()
    assert.equal(h.sockets.length, 1)
    assert.deepEqual(h.last().protocols, ['relaymesh.term.v1', 'tk.resumed'])
  })

  it('waiting 时 resumeIfPossible 同样立即重连，并重置退避', async () => {
    let up = false
    h.ticketImpl.fn = async () => {
      if (!up) throw new Error('Network Error')
      return 'quick'
    }
    h.conn.start()
    await tick()
    assert.equal(h.conn.state, 'waiting')
    up = true
    h.conn.resumeIfPossible()
    await tick()
    assert.equal(h.sockets.length, 1)
  })

  it('需要人工处理的失败不会被 resumeIfPossible 悄悄重连', async () => {
    let calls = 0
    h.ticketImpl.fn = async () => {
      calls++
      throw { cancelled: true, message: 'cancelled' }
    }
    h.conn.start()
    await tick()
    assert.equal(h.conn.state, 'failed')
    h.conn.resumeIfPossible()
    await tick()
    assert.equal(calls, 1, '用户取消解锁后，网络恢复事件不应再次弹出解锁')
    assert.equal(h.conn.state, 'failed')
    assert.equal(h.sockets.length, 0)
  })

  it('已结束 / 已关闭的连接对 resumeIfPossible 无反应', async () => {
    h.conn.start()
    await tick()
    h.last().text(ready())
    h.last().text({ t: 'exit', code: 0 })
    h.last().serverClose(4410, 'exited')
    await tick()
    assert.equal(h.conn.state, 'ended')
    const n = h.sockets.length
    h.conn.resumeIfPossible()
    await tick()
    assert.equal(h.sockets.length, n)
    h.conn.stop()
    h.conn.resumeIfPossible()
    await tick()
    assert.equal(h.sockets.length, n)
  })
})

describe('TerminalConnection：输入与尺寸', () => {
  let h: Harness
  beforeEach(() => {
    h = harness()
  })

  it('未附着时输入返回 false，附着后编码为输入帧', async () => {
    assert.equal(h.conn.sendText('x'), false)
    h.conn.start()
    await tick()
    const s = h.last()
    assert.equal(h.conn.sendText('x'), false, 'ready 之前不发送')
    s.text(ready())
    assert.equal(h.conn.sendText('ls\r'), true)
    assert.equal(s.inputSent.length, 1)
    assert.equal(s.inputSent[0][0], 0x02)
    assert.equal(new TextDecoder().decode(s.inputSent[0].subarray(1)), 'ls\r')
  })

  it('粘贴大文本会分帧', async () => {
    h.conn.start()
    await tick()
    const s = h.last()
    s.text(ready())
    h.conn.sendText('y'.repeat(MAX_INPUT_PAYLOAD * 2 + 1))
    assert.equal(s.inputSent.length, 3)
  })

  it('尺寸变化只在真正变化时上报', async () => {
    h.conn.start()
    await tick()
    const s = h.last()
    s.text(ready())
    h.conn.resize(40, 120)
    h.conn.resize(40, 120)
    h.conn.resize(41, 120)
    const resizes = s.controlSent.filter(c => c.t === 'resize')
    assert.deepEqual(resizes, [
      { t: 'resize', rows: 40, cols: 120 },
      { t: 'resize', rows: 41, cols: 120 },
    ])
  })

  it('重连后会把最新尺寸再报告一次', async () => {
    h.conn.start()
    await tick()
    const s1 = h.last()
    s1.text(ready())
    h.conn.resize(30, 100)
    s1.serverClose(1006)
    h.timers.advance(600)
    await tick()
    const s2 = h.last()
    s2.text(ready({ from: 0 }))
    assert.deepEqual(s2.controlSent.filter(c => c.t === 'resize'), [{ t: 'resize', rows: 30, cols: 100 }])
  })
})

describe('TerminalConnection：主动关闭', () => {
  it('stop 以 1000 关闭并忽略之后的一切事件', async () => {
    const h = harness()
    h.conn.start()
    await tick()
    const s = h.last()
    s.text(ready())
    h.conn.stop()
    assert.deepEqual(s.closedWith, { code: 1000, reason: 'detach' })
    assert.equal(h.conn.state, 'closed')
    s.onclose?.({ code: 1006, reason: '' })
    h.timers.advance(60_000)
    await tick()
    assert.equal(h.tickets.length, 1)
    assert.equal(h.timers.pendingCount, 0)
  })

  it('取票据途中 stop：票据返回后不会再建立连接', async () => {
    const h = harness()
    let release: (t: string) => void = () => {}
    h.ticketImpl.fn = () => new Promise<string>(r => (release = r))
    h.conn.start()
    await tick()
    h.conn.stop()
    release('t')
    await tick()
    assert.equal(h.sockets.length, 0)
  })

  it('等待重连期间 stop：取消重连定时器', async () => {
    const h = harness()
    h.conn.start()
    await tick()
    const s = h.last()
    s.text(ready())
    s.serverClose(1006)
    h.conn.stop()
    h.timers.advance(60_000)
    await tick()
    assert.equal(h.tickets.length, 1)
  })
})

describe('TerminalConnection：按窗口大小只回放末尾', () => {
  it('全新附着请求 tailLines，行数由终端当前行数算出', async () => {
    const h = harness({ tailLines: rows => rows * 20 })
    h.conn.start()
    await tick()
    assert.deepEqual(h.tickets, [{ since: 0, rows: 24, cols: 80, tailLines: 480 }])
  })

  it('没有提供 tailLines 或返回 0：不带这个字段（旧行为）', async () => {
    const none = harness()
    none.conn.start()
    await tick()
    assert.deepEqual(none.tickets, [{ since: 0, rows: 24, cols: 80 }])

    const zero = harness({ tailLines: () => 0 })
    zero.conn.start()
    await tick()
    assert.deepEqual(zero.tickets, [{ since: 0, rows: 24, cols: 80 }])
  })

  it('断线续传（since > 0）也带 tailLines：缓冲若已被越过，主机按全新附着只回放末尾；没被越过时主机不看它', async () => {
    const h = harness({ tailLines: () => 800 })
    h.conn.start()
    await tick()
    assert.equal(h.tickets[0].tailLines, 800)
    const s1 = h.last()
    s1.text(ready({ from: 0, end: 0 }))
    s1.output(0, 'hello')
    s1.serverClose(1006)
    h.timers.advance(600)
    await tick()
    assert.equal(h.tickets.length, 2)
    assert.equal(h.tickets[1].since, 5)
    assert.equal(h.tickets[1].tailLines, 800)
  })

  it('回放从中间开始（from > 0）时，续传的 since 仍按累计偏移计算', async () => {
    const h = harness({ tailLines: () => 800 })
    h.conn.start()
    await tick()
    const s1 = h.last()
    s1.text(ready({ base: 0, from: 5000, end: 5000 }))
    s1.output(5000, 'tail')
    s1.serverClose(1006)
    h.timers.advance(600)
    await tick()
    assert.equal(h.tickets[1].since, 5004)
  })
})

describe('TerminalConnection：跟不上输出（4414）', () => {
  let h: Harness
  beforeEach(() => {
    h = harness({ tailLines: () => 800, withVisible: true })
  })

  async function live(output = 'abc'): Promise<FakeSocket> {
    h.conn.start()
    await tick()
    const s = h.last()
    s.text(ready())
    s.output(0, output)
    return s
  }

  /** 服务端以 4414 断开当前连接，验证恰好等 waitMs 之后才重连，并把新连接带到 ready（返回新连接）。 */
  async function lagCycle(waitMs: number, nextOffset: number): Promise<FakeSocket> {
    const before = h.tickets.length
    h.last().serverClose(4414, 'lagging')
    h.timers.advance(waitMs - 2)
    await tick()
    assert.equal(h.tickets.length, before, `等待 ${waitMs}ms 之前不应重连`)
    h.timers.advance(4)
    await tick()
    assert.equal(h.tickets.length, before + 1)
    const s = h.last()
    s.text(ready({ from: nextOffset, end: nextOffset }))
    return s
  }

  it('lag 帧透传给界面；ready 与其他断线都会撤销「正在追赶」', async () => {
    const s = await live()
    assert.deepEqual(h.lagEvents, [false]) // ready 时撤销一次
    s.text({ t: 'lag', on: true })
    s.text({ t: 'lag', on: false })
    assert.deepEqual(h.lagEvents, [false, true, false])
    s.text({ t: 'lag', on: true })
    s.serverClose(1006) // 普通断线：遮罩没有意义了
    assert.equal(h.lagEvents[h.lagEvents.length - 1], false)
  })

  it('4414 断开：等 1.2 秒后全新附着（since=0、带 tailLines、清屏），之后的普通断线仍是续传', async () => {
    const s1 = await live('abc')
    s1.text({ t: 'lag', on: true })
    const resets = h.resets.n
    s1.serverClose(4414, 'lagging')
    assert.equal(h.conn.state, 'waiting')
    assert.match(h.states[h.states.length - 1].info.message, /跳到最新/)
    h.timers.advance(1400)
    await tick()
    assert.equal(h.tickets.length, 1, '要给程序一点时间把这一阵的输出打完')
    h.timers.advance(200)
    await tick()
    assert.equal(h.tickets.length, 2)
    assert.equal(h.tickets[1].since, 0, '旧偏移已经没有意义')
    assert.equal(h.tickets[1].tailLines, 800)

    const s2 = h.last()
    s2.text(ready({ from: 5000, base: 0, end: 5004 }))
    assert.equal(h.resets.n, resets + 1, '清屏重建')
    s2.output(5000, 'tail')
    assert.equal(h.conn.offset, 5004)

    s2.serverClose(1006)
    h.timers.advance(600)
    await tick()
    assert.equal(h.tickets[2].since, 5004, 'forceFresh 只管一次')
  })

  it('连续被断开逐次放慢（1.5 → 3 → 6 秒封顶）；连接稳定 20 秒之后被断开不算连续', async () => {
    await live()
    await lagCycle(1500, 100)
    await lagCycle(3000, 200)
    await lagCycle(6000, 300)
    await lagCycle(6000, 400)
    await lagCycle(6000, 500)
    // 这条连接稳定活过 25 秒才被断开：从头算
    h.timers.advance(25_000)
    await lagCycle(1500, 600)
  })

  it('等待重新附着期间敲的键先攒着，重新附着后按顺序送出（用来打断刷屏的 Ctrl+C 不会丢）', async () => {
    const s1 = await live()
    s1.serverClose(4414, 'lagging')
    assert.equal(h.conn.state, 'waiting')
    assert.equal(h.conn.sendText('\x03'), true, '攒住算成功')
    assert.equal(h.conn.sendText('secret-cmd\r'), true)
    assert.equal(h.conn.sendBinary('\x1b'), false, '鼠标之类的二进制输入不攒')
    h.timers.advance(1600)
    await tick()
    const s2 = h.last()
    assert.equal(s2.inputSent.length, 0, 'ready 之前不发送')
    s2.text(ready({ from: 5000, end: 5000 }))
    const got = s2.inputSent.map(f => new TextDecoder().decode(f.subarray(1)))
    assert.deepEqual(got, ['\x03', 'secret-cmd\r'], '按原来的顺序全部送出')
    assert.equal(h.conn.sendText('x'), true)
    assert.equal(s2.inputSent.length, 3)
    // 只有字节数进诊断，没有按键内容
    assert.equal(h.diag.text().includes('secret'), false)
  })

  it('攒按键有总量上限；普通断线（不是跟不上）不攒、也不补发', async () => {
    const s1 = await live()
    s1.serverClose(4414, 'lagging')
    assert.equal(h.conn.sendText('a'.repeat(400)), true)
    assert.equal(h.conn.sendText('b'.repeat(400)), false, '超过上限不再攒')
    h.timers.advance(1600)
    await tick()
    const s2 = h.last()
    s2.text(ready({ from: 5000, end: 5000 }))
    assert.equal(s2.inputSent.length, 1)

    // 普通断线：按键直接丢弃
    s2.serverClose(1006)
    assert.equal(h.conn.sendText('z'), false)
    h.timers.advance(5000)
    await tick()
    const s3 = h.last()
    s3.text(ready({ from: 5000, end: 5000 }))
    assert.equal(s3.inputSent.length, 0, '普通断线期间的按键不补发')
  })

  it('等得太久才重新附着：攒下的按键过期，不再补发；用户主动关闭也清空', async () => {
    const s1 = await live()
    s1.serverClose(4414, 'lagging')
    assert.equal(h.conn.sendText('q'), true)
    h.timers.advance(30_000) // 重连一直没成功（重试的定时器早就到了，但这条连接 30 秒后才真正就绪）
    await tick()
    const s2 = h.last()
    s2.text(ready({ from: 5000, end: 5000 }))
    assert.equal(s2.inputSent.length, 0, '过期的按键不补发')

    s2.serverClose(4414, 'lagging')
    assert.equal(h.conn.sendText('r'), true)
    h.conn.stop()
    assert.equal(h.conn.sendText('s'), false)
  })

  it('后台窗格被 4414 断开：不自动重连（暂时性故障），再被看到时全新附着', async () => {
    const s1 = await live()
    h.visible.on = false
    s1.serverClose(4414, 'lagging')
    assert.equal(h.conn.state, 'failed')
    assert.equal(h.lagEvents[h.lagEvents.length - 1], false)
    h.timers.advance(120_000)
    await tick()
    assert.equal(h.tickets.length, 1, '后台窗格不应反复追赶')
    h.visible.on = true
    h.conn.resumeIfPossible()
    await tick()
    assert.equal(h.tickets.length, 2)
    assert.equal(h.tickets[1].since, 0)
  })

  it('诊断环按顺序记下连接生命周期（不含终端内容）', async () => {
    const s1 = await live('secret-output')
    s1.text({ t: 'lag', on: true })
    s1.serverClose(4414, 'lagging')
    h.timers.advance(1600)
    await tick()
    const kinds = h.diag.events().map(e => e.k)
    assert.deepEqual(kinds, ['connect', 'ready', 'lag', 'close', 'lagging', 'connect'])
    assert.equal(h.diag.text().includes('secret-output'), false)
  })
})

describe('TerminalConnection：全新附着的回放进度', () => {
  it('start 给出总字节数，最后一段回放被渲染完才 done；实时输出不算', async () => {
    const h = harness()
    h.deferWrites.on = true
    h.conn.start()
    await tick()
    const s = h.last()
    s.text(ready({ from: 0, end: 10 }))
    assert.deepEqual(h.replayEvents, [{ phase: 'start', total: 10 }])
    s.output(0, '12345')
    s.output(5, '67890')
    s.output(10, 'live!') // 实时输出，不影响回放是否完成
    h.deferWrites.pending.shift()!()
    assert.equal(h.replayEvents.length, 1, '第一段渲染完，回放还没完')
    h.deferWrites.pending.shift()!()
    assert.deepEqual(h.replayEvents, [{ phase: 'start', total: 10 }, { phase: 'done' }])
  })

  it('回放为空：start 之后立即 done', async () => {
    const h = harness()
    h.conn.start()
    await tick()
    h.last().text(ready({ from: 7, base: 7, end: 7 }))
    assert.deepEqual(h.replayEvents, [{ phase: 'start', total: 0 }, { phase: 'done' }])
  })

  it('一个帧横跨回放与实时：回放部分渲染完就 done', async () => {
    const h = harness()
    h.deferWrites.on = true
    h.conn.start()
    await tick()
    const s = h.last()
    s.text(ready({ from: 0, end: 3 }))
    s.output(0, 'abcdef') // abc 是回放，def 是实时
    assert.deepEqual(h.writes, ['abc', 'def'])
    h.deferWrites.pending.shift()!()
    assert.deepEqual(h.replayEvents[h.replayEvents.length - 1], { phase: 'done' })
  })

  it('续传（非全新附着）没有回放事件', async () => {
    const h = harness()
    h.conn.start()
    await tick()
    const s1 = h.last()
    s1.text(ready())
    s1.output(0, 'hello')
    const before = h.replayEvents.length
    s1.serverClose(1006)
    h.timers.advance(600)
    await tick()
    h.last().text(ready({ from: 5, end: 5 }))
    assert.equal(h.replayEvents.length, before)
  })

  it('回放没渲染完就断线：不会再有 done（旧连接的渲染回调被忽略），界面靠状态变化撤遮罩', async () => {
    const h = harness()
    h.deferWrites.on = true
    h.conn.start()
    await tick()
    const s = h.last()
    s.text(ready({ from: 0, end: 10 }))
    s.output(0, '12345')
    s.serverClose(1006)
    h.deferWrites.pending.shift()!()
    assert.deepEqual(h.replayEvents, [{ phase: 'start', total: 10 }])
  })
})
