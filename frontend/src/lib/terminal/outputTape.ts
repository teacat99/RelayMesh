// 实时终端的「输出带」：按顺序记下写进终端的每一段内容（纯逻辑，不依赖 DOM / xterm，便于单元测试）。
//
// 为什么需要：xterm 的缓冲区只能往后写，没法在最前面插入内容。要让历史日志「接」在实时内容的上方，
// 只能先把终端清空，再依次写入「历史 → 分割线 → 实时内容」。其中「实时内容」就取自这里：
// 它是页面打开以来写进终端的字节（含本地提示），超过上限时从最旧的一端丢弃，并重新对齐到行首。
//
// 输出带里有三类内容：
//   stream  会话输出（Hub 转来的字节，和主机上的记录是同一条字节流）；
//   local   本地提示（「断线期间有 N 字节输出已超出缓冲」之类，不属于会话输出）；
//   prefix  附着时主机给的「模式恢复序列」（为了让新终端的模式与输出流一致而合成的，不属于会话输出）。

import { findClear } from './flatten'

export type TapeKind = 'stream' | 'local' | 'prefix'

export interface TapeChunk {
  kind: TapeKind
  data: Uint8Array
}

/** 输出带最多保留的字节数；够装下终端 5000 行回滚缓冲的内容，又不至于占用太多内存。 */
export const TAPE_CAP = 6 * 1024 * 1024

const enc = new TextEncoder()
const ESC = 0x1b
const NL = 0x0a
/** 丢弃最旧内容之后，向后找换行的最大距离：找不到就原地开始（宁可开头有半行，也不丢太多）。 */
const ALIGN_SCAN = 8192

export class OutputTape {
  private chunks: TapeChunk[] = []
  private total = 0
  private streamTotal = 0
  private cut = false

  constructor(readonly cap: number = TAPE_CAP) {}

  /** 清空（终端被重置、重新回放时调用）。 */
  reset(): void {
    this.chunks = []
    this.total = 0
    this.streamTotal = 0
    this.cut = false
  }

  /** 最旧的内容是否已因超出上限被丢弃（此时输出带的开头不再是会话的开头）。 */
  get trimmed(): boolean {
    return this.cut
  }

  /** 会话输出的字节数（不含本地提示和模式恢复序列）。 */
  get streamBytes(): number {
    return this.streamTotal
  }

  push(kind: TapeKind, data: Uint8Array | string): void {
    const bytes = typeof data === 'string' ? enc.encode(data) : data
    if (bytes.length === 0) return
    this.chunks.push({ kind, data: bytes })
    this.total += bytes.length
    if (kind === 'stream') this.streamTotal += bytes.length
    if (this.total > this.cap) this.trim()
  }

  /** 当前内容的快照（块本身不可变，复制数组即可）。 */
  snapshot(): TapeChunk[] {
    return this.chunks.slice()
  }

  /** 把会话输出拼成一整块（用来在记录里定位、查找清屏位置）。 */
  concatStream(): Uint8Array {
    return streamOf(this.chunks)
  }

  private trim(): void {
    while (this.total > this.cap && this.chunks.length > 1) this.drop()
    this.cut = true
    // 开头必须是会话输出，并落在行首：先丢掉开头的本地提示 / 模式恢复序列，再让第一块跳到换行之后。
    while (this.chunks.length > 1 && this.chunks[0].kind !== 'stream') this.drop()
    const first = this.chunks[0]
    if (!first || first.kind !== 'stream') return
    const nl = first.data.subarray(0, Math.min(first.data.length, ALIGN_SCAN)).indexOf(NL)
    if (nl < 0) return
    if (nl + 1 >= first.data.length) {
      if (this.chunks.length > 1) this.drop()
      return
    }
    const rest = first.data.subarray(nl + 1)
    this.total -= nl + 1
    this.streamTotal -= nl + 1
    this.chunks[0] = { kind: 'stream', data: rest }
  }

  private drop(): void {
    const c = this.chunks.shift()
    if (!c) return
    this.total -= c.data.length
    if (c.kind === 'stream') this.streamTotal -= c.data.length
  }
}

/** 把一组块里的会话输出按顺序拼成一整块（本地提示和模式恢复序列不算）。 */
export function streamOf(chunks: TapeChunk[]): Uint8Array {
  let n = 0
  for (const c of chunks) if (c.kind === 'stream') n += c.data.length
  const out = new Uint8Array(n)
  let pos = 0
  for (const c of chunks) {
    if (c.kind !== 'stream') continue
    out.set(c.data, pos)
    pos += c.data.length
  }
  return out
}

/** ESC [ H：光标回到左上角（紧跟在 ESC[J 前面时，ESC[J 等于清整屏）。 */
function homeBefore(buf: Uint8Array, at: number): boolean {
  return at >= 3 && buf[at - 3] === ESC && buf[at - 2] === 0x5b && buf[at - 1] === 0x48
}

/**
 * 输出流里最后一次「清掉屏幕内容」之后的位置（字节下标，0 表示没有清屏过）。
 *
 * 清屏之前的内容在实时终端里已经没了，但它们在记录里还在：历史日志要把它们也带上，
 * 所以分割线放在最后一次清屏的位置，而不是输出带的开头，这样历史与实时内容之间既不重叠也不留缺口。
 */
export function lastClearEnd(buf: Uint8Array): number {
  let pos = 0
  let last = 0
  for (;;) {
    const hit = findClear(buf, pos)
    if (!hit || 'partial' in hit) break
    if (hit.kind !== 'below' || homeBefore(buf, hit.start)) last = hit.end
    pos = hit.end
  }
  return last
}

export interface TapeSplit {
  /** 分割线之前的部分：会话输出（按顺序，夹着本地提示）。不含模式恢复序列。 */
  head: TapeChunk[]
  /** 分割线之后的部分：实时终端里现在看到的内容。 */
  tail: TapeChunk[]
}

/**
 * 在第 cut 个会话输出字节处把输出带一分为二。模式恢复序列丢弃（历史回放出来的终端状态已经是对的）。
 * 横跨分割点的输出块被拆开；恰好落在分割点上的本地提示归入分割线之后。
 */
export function splitTape(chunks: TapeChunk[], cut: number): TapeSplit {
  const head: TapeChunk[] = []
  const tail: TapeChunk[] = []
  let seen = 0
  let past = cut <= 0
  for (const c of chunks) {
    if (c.kind === 'prefix') continue
    if (past) {
      tail.push(c)
      continue
    }
    if (c.kind === 'local') {
      head.push(c)
      continue
    }
    const room = cut - seen
    if (c.data.length < room) {
      head.push(c)
      seen += c.data.length
    } else if (c.data.length === room) {
      head.push(c)
      seen += c.data.length
      past = true
    } else {
      head.push({ kind: 'stream', data: c.data.subarray(0, room) })
      tail.push({ kind: 'stream', data: c.data.subarray(room) })
      seen = cut
      past = true
    }
  }
  return { head, tail }
}
