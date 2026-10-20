// 「接续显示历史日志」的定位与切分（纯逻辑，不依赖 DOM / xterm，便于单元测试）。
//
// 实时终端里的内容是主机输出流的一段（页面打开时从主机缓冲区的某个位置回放起，再加上之后的实时输出）；
// 主机上的记录保存了整条输出流。两者是同一条字节流，所以可以用实时内容开头的一小段字节，
// 在记录里找到「实时内容从哪里开始」，把这个位置之前的记录作为历史，接在实时内容的上方，既不重叠也不留缺口。
//
//   记录：  [ 更早的历史 ........... | 实时终端回放的起点 ........ 现在 ]
//   实时：                           [ 页面打开后收到的内容 ........ 现在 ]
//                                    ↑ 用这里开头的字节在记录里定位

import { lastClearEnd } from './outputTape'
import type { Item } from './record'

/** 用来定位的样本长度：足够长，不会和别处巧合重复；输出带里不足这个数就用全部（但不能少于 MIN_SAMPLE）。 */
export const SAMPLE_BYTES = 512
export const MIN_SAMPLE = 48

export function outputLength(items: Item[]): number {
  let n = 0
  for (const it of items) if (it.type === 'output') n += it.data.length
  return n
}

/** 把条目里的输出字节按顺序拼成一整块（不含尺寸变化 / 缺口等标记）。 */
export function concatOutput(items: Item[]): Uint8Array {
  const out = new Uint8Array(outputLength(items))
  let pos = 0
  for (const it of items) {
    if (it.type !== 'output') continue
    out.set(it.data, pos)
    pos += it.data.length
  }
  return out
}

/**
 * 在 hay 里找 needle 出现的位置；出现多次时取离 expected 最近的一个。找不到返回 -1。
 * 先用 indexOf 跳到首字节相同的位置，再逐字节比较——样本几百字节、记录几 MiB 时是毫秒级。
 */
export function locateSample(hay: Uint8Array, needle: Uint8Array, expected: number): number {
  const n = needle.length
  if (n === 0 || hay.length < n) return -1
  const first = needle[0]
  const last = hay.length - n
  let best = -1
  let bestDist = Number.POSITIVE_INFINITY
  let i = hay.indexOf(first, 0)
  while (i >= 0 && i <= last) {
    let ok = true
    for (let k = 1; k < n; k++) {
      if (hay[i + k] !== needle[k]) {
        ok = false
        break
      }
    }
    if (ok) {
      const d = Math.abs(i - expected)
      if (d < bestDist) {
        best = i
        bestDist = d
      }
    }
    i = hay.indexOf(first, i + 1)
  }
  return best
}

/**
 * 只保留输出字节下标 < at 的部分：横跨 at 的输出块被截断；排在 at 之后的条目（含缺口 / 结束标记）全部丢弃。
 * at 之前的尺寸变化、缺口等标记保留，历史回放时它们仍然按顺序生效。
 */
export function cutItems(items: Item[], at: number): Item[] {
  const out: Item[] = []
  let seen = 0
  for (const it of items) {
    if (it.type !== 'output') {
      out.push(it)
      continue
    }
    const room = at - seen
    if (room <= 0) break
    if (it.data.length <= room) {
      out.push(it)
      seen += it.data.length
      if (seen >= at) break
      continue
    }
    out.push({ type: 'output', at: it.at, data: it.data.subarray(0, room) })
    break
  }
  return out
}

/** 逐字节比较 a[ai..ai+n) 与 b[bi..bi+n)。 */
function sameBytes(a: Uint8Array, ai: number, b: Uint8Array, bi: number, n: number): boolean {
  if (ai < 0 || bi < 0 || ai + n > a.length || bi + n > b.length) return false
  for (let k = 0; k < n; k++) if (a[ai + k] !== b[bi + k]) return false
  return true
}

/** 校验记录与输出带在衔接处是否仍是同一条字节流时，比较末尾这么多字节。 */
const JOIN_CHECK_BYTES = 64

export type PlanResult =
  /**
   * history：分割线之上要回放的记录条目（已经带上了实时内容里清屏之前、记录也有的那一部分）。
   * tapeHead：输出带的会话输出里，已经被 history 覆盖的字节数。
   * tapeCut：分割线在输出带会话输出里的位置（最后一次清屏之后；0 = 没有清屏，分割线就在实时内容的开头）。
   * [tapeHead, tapeCut) 这一段记录里没有（记录比实时内容落后，或衔接处对不上），要从输出带里补上。
   */
  | { kind: 'ok'; history: Item[]; tapeHead: number; tapeCut: number }
  /** 没有任何更早的内容可显示（已经是会话开头，也没有清屏吃掉的内容）。 */
  | { kind: 'nothing' }
  /** 样本落在已载入窗口的最开头，窗口之前还有记录：需要再往前读一些。 */
  | { kind: 'need_earlier' }
  /** 记录里找不到实时内容的开头（记录有缺口、记录比实时内容更早结束，或窗口太短）。 */
  | { kind: 'not_found' }
  /** 实时内容太短，不足以可靠定位。 */
  | { kind: 'no_sample' }

export interface PlanOptions {
  /** 已载入窗口的开头就是记录的开头（没有更早的分段了）。 */
  atBeginning: boolean
  sampleBytes?: number
}

/**
 * 根据已载入的记录条目和实时终端的输出（会话输出部分），算出历史该回放哪些内容。
 * 实时内容里若有清屏，分割线放在最后一次清屏处：清屏前的内容在实时终端里已经没有了，由历史接上。
 * 这段内容优先取自记录（带真实时间，平铺时能区分「人敲的 clear」和「程序在刷新画面」）；
 * 记录里没有或衔接处对不上时，才取输出带里的字节。
 */
export function planExtend(items: Item[], tapeStream: Uint8Array, opts: PlanOptions): PlanResult {
  if (tapeStream.length < MIN_SAMPLE) return { kind: 'no_sample' }
  const tapeCut = lastClearEnd(tapeStream)
  const needle = tapeStream.subarray(0, Math.min(opts.sampleBytes ?? SAMPLE_BYTES, tapeStream.length))
  const hay = concatOutput(items)
  // 记录通常比实时内容稍微落后一点，所以样本应该出现在「末尾往前数一个输出带长度」的附近。
  const expected = Math.max(0, hay.length - tapeStream.length)
  const at = locateSample(hay, needle, expected)
  if (at < 0) return { kind: 'not_found' }
  if (at === 0) {
    if (!opts.atBeginning) return { kind: 'need_earlier' }
    if (tapeCut === 0) return { kind: 'nothing' }
  }
  // 记录里从 at 起还有多少字节；清屏前的那一段能取多少取多少。
  const covered = Math.min(tapeCut, hay.length - at)
  const k = Math.min(JOIN_CHECK_BYTES, covered)
  const joined = k === 0 || sameBytes(hay, at + covered - k, tapeStream, covered - k, k)
  const tapeHead = joined ? covered : 0
  return { kind: 'ok', history: cutItems(items, at + tapeHead), tapeHead, tapeCut }
}
