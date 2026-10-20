// 「平铺」：把终端输出流整理之后再交给 xterm，让 clear 之类的清屏不会把更早的内容抹掉。
//
// 为什么需要：xterm 按字节流忠实地重放，所以 `clear`（ESC[H ESC[2J ESC[3J）、一些智能体界面的重绘（ESC[2J ESC[3J ESC[H）
// 回放出来也是一块空屏，之前的内容连同回滚缓冲区一起没了，向上滚什么都看不到。平铺做两件事：
//   1. 清屏（ESC[2J、光标在左上角时的 ESC[J、ESC c）之前，先把屏幕上已有的内容顺着滚动推进回滚缓冲区；
//   2. 忽略「清除回滚缓冲区」（ESC[3J），ESC c 改成不清历史的软复位。
// 全屏程序（备用屏幕：vim / htop / less 等）里的清屏照常处理——备用屏幕本来就没有回滚。
//
// 全屏程序退出（ESC[?1049l / ?1047l / ?47l）时，备用屏幕连同它的内容一起消失，历史里就看不出它曾经显示过什么。
// 平铺在这一刻先读出备用屏幕上的最后一屏，等终端回到普通屏幕之后，把它以「分割线 + 纯文本 + 分割线」的形式
// 接在原位置（见 altScreenBlock）：整体仍是一条平铺的历史，只是多了一块标明「这是全屏程序退出前的最后一屏」的内容。
//
// 一个例外：程序在高频重绘整屏（spinner、watch -n1、整屏重排版的 TUI）时，每一帧都推进历史只会把历史灌满重复的画面，
// 而它重新画出来的整屏内容本身已经包含了之前的内容。所以相隔很近的清屏（rapidMs 内）被认为是「程序在刷新画面」，
// 按原样处理；只有孤立的、隔了一阵才出现的清屏（人敲了 clear、脚本清屏）才平铺。需要一模一样的回放时用「还原」模式。
//
// 本模块只做字节流的扫描与改写，对终端状态的读取（屏幕上有几行内容、光标在哪、是否在备用屏幕）通过 FlattenHost 注入，
// 因此可以用假的 host 做单元测试，生产里由 xterm 的 buffer 实现。

import { dividerLine } from './divider'

export interface ScreenProbe {
  /** 终端的行数（可视区域高度）。 */
  rows: number
  /** 终端的列数（分割线按它画满一行）。 */
  cols: number
  /** 可视区域每一行的文字（去掉行尾空白），按从上到下排列；全屏程序退出前靠它保留「最后一屏」。 */
  lines: string[]
  /** 光标在可视区域内的位置（0 起）。 */
  cursorX: number
  cursorY: number
  /** 当前是否在备用屏幕（全屏程序）。 */
  alt: boolean
  /** 可视区域里最后一个非空白行的序号 + 1；0 表示整屏是空的。 */
  used: number
  /** 可视区域里已用的行（去掉行尾空白）按行拼起来的文字，用来判断两次清屏前的画面是否一模一样。 */
  fingerprint: string
}

export interface FlattenHost {
  /** 写入终端，并在这批数据被解析完之后返回（之后读到的 probe 才是写入后的状态）。 */
  write(data: Uint8Array): Promise<void>
  probe(): ScreenProbe
}

export interface FlattenOptions {
  /** 两次清屏相隔不到这么多毫秒，视为程序在刷新画面，按原样处理。 */
  rapidMs?: number
  /** 一次渲染里最多往历史里推多少次屏幕，防止病态输出把回滚缓冲区灌满。 */
  maxPushes?: number
  /** 没有清屏时，攒到这么多字节再一起写入，减少写入次数。 */
  batchBytes?: number
  /** 一次渲染里最多保留多少块「全屏程序退出前的最后一屏」，防止病态输出把历史灌满。 */
  maxAltScreens?: number
}

export const DEFAULT_RAPID_MS = 3000
export const DEFAULT_MAX_PUSHES = 4000
export const DEFAULT_BATCH_BYTES = 128 * 1024
export const DEFAULT_MAX_ALT_SCREENS = 500
/** 紧跟在清屏后面的「清除回滚缓冲区」（ncurses 的 clear 会连发 2J 和 3J）跟随清屏的判定。 */
const GROUP_MS = 120
/** ESC[? 开头的私有模式序列，参数部分超过这么多字节就不当作「退出备用屏幕」来等（也不会是 47 / 1047 / 1049 的写法）。 */
const MAX_PRIVATE_PARAM_BYTES = 24

type ClearKind = 'screen' | 'below' | 'scrollback' | 'reset'
type FlattenKind = ClearKind | 'altexit'

export interface ClearHit {
  start: number
  end: number
  kind: ClearKind
}

export interface FlattenHit {
  start: number
  end: number
  kind: FlattenKind
}

export interface PartialHit {
  /** 缓冲区末尾有一段可能是清屏序列的开头，位置从这里起；要等后续数据才能判断。 */
  partial: number
}

const ESC = 0x1b
const EMPTY = new Uint8Array(0)
const enc = new TextEncoder()

/** buf[from:to] 是 ESC[? 之后的参数：其中有 47 / 1047 / 1049 之一，表示「切换备用屏幕」（配合终止字节 l 就是退出）。 */
function hasAltScreenParam(buf: Uint8Array, from: number, to: number): boolean {
  if (to - from > MAX_PRIVATE_PARAM_BYTES) return false
  let text = ''
  for (let k = from; k < to; k++) text += String.fromCharCode(buf[k])
  return text.split(';').some(p => p === '47' || p === '1047' || p === '1049')
}

/**
 * 扫描的共用实现。altExit 为真时还认「退出备用屏幕」（ESC[?47l / ?1047l / ?1049l，参数里有它们之一即可）。
 */
function scan(buf: Uint8Array, from: number, altExit: boolean): FlattenHit | PartialHit | null {
  let i = from
  for (;;) {
    i = buf.indexOf(ESC, i)
    if (i < 0) return null
    if (i + 1 >= buf.length) return { partial: i }
    const c = buf[i + 1]
    if (c === 0x63) return { start: i, end: i + 2, kind: 'reset' } // ESC c（RIS）
    if (c !== 0x5b) {
      i += 1
      continue
    }
    // CSI：参数字节 0x30–0x3f，其后是中间字节 / 终止字节
    let j = i + 2
    while (j < buf.length && buf[j] >= 0x30 && buf[j] <= 0x3f) j++
    const priv = buf[i + 2] === 0x3f // 参数以 ? 开头：私有模式（DECSET / DECRST）
    if (j >= buf.length) {
      // 序列在缓冲区末尾被截断：参数只有 0~1 个字符时可能是 ESC[2 / ESC[3 / ESC[0 的开头，留到下一批再判断。
      const n = j - (i + 2)
      if (n <= 1) return { partial: i }
      // 退出备用屏幕的写法是 ESC[?1049l 之类，被截断在中间（如 ESC[?10）时同样要等后续数据。
      if (altExit && priv && n <= MAX_PRIVATE_PARAM_BYTES) return { partial: i }
      return null
    }
    if (buf[j] === 0x4a) {
      const n = j - (i + 2)
      const p = n === 0 ? '' : n === 1 ? String.fromCharCode(buf[i + 2]) : null
      if (p === '3') return { start: i, end: j + 1, kind: 'scrollback' }
      if (p === '2') return { start: i, end: j + 1, kind: 'screen' }
      if (p === '' || p === '0') return { start: i, end: j + 1, kind: 'below' }
    } else if (altExit && priv && buf[j] === 0x6c && hasAltScreenParam(buf, i + 3, j)) {
      return { start: i, end: j + 1, kind: 'altexit' }
    }
    i = j
  }
}

/**
 * 在 buf[from:] 里找下一个我们关心的清屏序列：ESC[2J、ESC[3J、ESC[J / ESC[0J、ESC c。
 * 其他转义序列（颜色、光标移动……）原样跳过。末尾可能被截断的序列以 partial 返回。
 */
export function findClear(buf: Uint8Array, from: number): ClearHit | PartialHit | null {
  return scan(buf, from, false) as ClearHit | PartialHit | null
}

/** 平铺关心的全部序列：清屏类（同 findClear），以及全屏程序「退出备用屏幕」。 */
export function findEvent(buf: Uint8Array, from: number): FlattenHit | PartialHit | null {
  return scan(buf, from, true)
}

function isPartial(h: FlattenHit | PartialHit): h is PartialHit {
  return 'partial' in h
}

/** 去掉控制字符（写回终端时不能让屏幕上读出来的文字被当成指令），并去掉行尾空白。 */
function cleanLine(s: string): string {
  // eslint-disable-next-line no-control-regex
  return s.replace(/[\u0000-\u001f\u007f-\u009f]/g, ' ').replace(/\s+$/, '')
}

/** 备用屏幕上读出的各行：清掉控制字符，去掉开头和结尾的整行空白（行间的空行保留）。全是空白返回 []。 */
export function trimScreen(lines: string[]): string[] {
  const rows = lines.map(cleanLine)
  let a = 0
  let z = rows.length
  while (a < z && rows[a] === '') a++
  while (z > a && rows[z - 1] === '') z--
  return rows.slice(a, z)
}

const ALT_TOP_LABELS = [' ↓ 全屏程序退出前的最后一屏 ', ' ↓ 全屏程序的最后一屏 ', ' ↓ 最后一屏 ']
const ALT_BOTTOM_LABELS = [' ↑ 全屏程序退出前的最后一屏 ', ' ↑ 全屏程序的最后一屏 ', ' ↑ 最后一屏 ']

/**
 * 全屏程序退出之后写进历史的那一块：分割线、最后一屏的纯文本、分割线。
 * 光标不在行首时先换行，让分割线独占一行；以复位属性开头和结尾，不把全屏程序遗留的颜色带进来。
 * rows 是 trimScreen 之后的行；没有内容返回空串。
 */
export function altScreenBlock(rows: string[], cols: number, atLineStart: boolean): string {
  if (rows.length === 0) return ''
  const top = dividerLine(cols, ALT_TOP_LABELS)
  const bottom = dividerLine(cols, ALT_BOTTOM_LABELS)
  return `${atLineStart ? '' : '\r\n'}\x1b[0m\x1b[2m${top}\x1b[0m\r\n${rows.join('\r\n')}\r\n\x1b[2m${bottom}\x1b[0m\r\n`
}

function concat(a: Uint8Array, b: Uint8Array): Uint8Array {
  const out = new Uint8Array(a.length + b.length)
  out.set(a, 0)
  out.set(b, a.length)
  return out
}

/** 把一整屏已用的行顺着滚动推进回滚缓冲区；DECSC / DECRC 保住光标位置与当前的颜色属性。 */
export function pushScreenSequence(rows: number, used: number): string {
  return `\x1b7\x1b[0m\x1b[${rows};1H${'\n'.repeat(used)}\x1b8`
}

export class Flattener {
  private carry: Uint8Array = EMPTY
  private pending: Uint8Array[] = []
  private pendingBytes = 0
  private lastClearAt: number | null = null
  private lastDecision: 'keep' | 'faithful' = 'keep'
  private lastPushed = ''
  private pushes = 0
  /** 上一块「最后一屏」的内容与时间：同样内容相隔很近地反复出现，说明程序在来回切换备用屏幕，只留一块。 */
  private lastAltFp = ''
  private lastAltAt: number | null = null
  private altKept = 0
  private readonly rapidMs: number
  private readonly maxPushes: number
  private readonly batchBytes: number
  private readonly maxAltScreens: number

  constructor(
    private readonly host: FlattenHost,
    opts: FlattenOptions = {},
  ) {
    this.rapidMs = opts.rapidMs ?? DEFAULT_RAPID_MS
    this.maxPushes = opts.maxPushes ?? DEFAULT_MAX_PUSHES
    this.batchBytes = opts.batchBytes ?? DEFAULT_BATCH_BYTES
    this.maxAltScreens = opts.maxAltScreens ?? DEFAULT_MAX_ALT_SCREENS
  }

  /** 到目前为止往历史里推了多少次屏幕。 */
  get pushed(): number {
    return this.pushes
  }

  /** 到目前为止保留了多少块「全屏程序退出前的最后一屏」。 */
  get altKeptCount(): number {
    return this.altKept
  }

  /** 送入一段输出。at 是这段输出的时间（毫秒，只用相对差）。 */
  async feed(data: Uint8Array, at = 0): Promise<void> {
    if (data.length === 0) return
    const buf = this.carry.length ? concat(this.carry, data) : data
    this.carry = EMPTY
    let pos = 0
    for (;;) {
      const hit = findEvent(buf, pos)
      if (!hit) {
        this.push(buf.subarray(pos))
        break
      }
      if (isPartial(hit)) {
        this.push(buf.subarray(pos, hit.partial))
        this.carry = buf.slice(hit.partial)
        break
      }
      this.push(buf.subarray(pos, hit.start))
      pos = hit.end
      await this.handle(hit.kind, buf.subarray(hit.start, hit.end), at)
    }
    if (this.pendingBytes >= this.batchBytes) await this.flush()
  }

  /** 把已经攒下的普通输出写入终端。 */
  async flush(): Promise<void> {
    if (this.pendingBytes === 0) return
    const parts = this.pending
    const total = this.pendingBytes
    this.pending = []
    this.pendingBytes = 0
    let data: Uint8Array
    if (parts.length === 1) data = parts[0]
    else {
      data = new Uint8Array(total)
      let off = 0
      for (const p of parts) {
        data.set(p, off)
        off += p.length
      }
    }
    await this.host.write(data)
  }

  /** 输出到此为止：把末尾可能被截断的序列也原样写出去。 */
  async finish(): Promise<void> {
    await this.flush()
    if (this.carry.length) {
      const rest = this.carry
      this.carry = EMPTY
      await this.host.write(rest)
    }
  }

  private push(chunk: Uint8Array): void {
    if (chunk.length === 0) return
    this.pending.push(chunk)
    this.pendingBytes += chunk.length
  }

  /**
   * 一次清屏常常由几条指令组成（ncurses 的 clear 是 H、2J、3J，Ink 的 clearTerminal 是 2J、3J、H），
   * 它们紧挨在一起，共享同一个判定；与上一次清屏相隔很近的新清屏是程序在刷新画面（faithful）；孤立的清屏才平铺（keep）。
   */
  private decide(at: number): 'keep' | 'faithful' {
    if (this.lastClearAt !== null && at >= this.lastClearAt && at - this.lastClearAt <= GROUP_MS) return this.lastDecision
    const rapid = this.lastClearAt !== null && at >= this.lastClearAt && at - this.lastClearAt < this.rapidMs
    this.lastClearAt = at
    this.lastDecision = rapid ? 'faithful' : 'keep'
    return this.lastDecision
  }

  /**
   * 全屏程序退出备用屏幕：先在它还在备用屏幕时读出最后一屏，原样发出退出序列（终端回到普通屏幕），
   * 再把这一屏以「分割线 + 纯文本 + 分割线」写在原位置。不在备用屏幕、屏幕是空的、与刚保留的一模一样（程序在来回切换）、
   * 或数量超过上限时，只发原序列。
   */
  private async handleAltExit(raw: Uint8Array, at: number): Promise<void> {
    const before = this.host.probe()
    if (!before.alt) {
      await this.host.write(raw)
      return
    }
    const rows = trimScreen(before.lines)
    const fp = rows.join('\n')
    const repeat = rows.length > 0 && fp === this.lastAltFp && this.lastAltAt !== null && at >= this.lastAltAt && at - this.lastAltAt < this.rapidMs
    if (rows.length > 0) {
      this.lastAltFp = fp
      this.lastAltAt = at
    }
    await this.host.write(raw)
    if (rows.length === 0 || repeat || this.altKept >= this.maxAltScreens) return
    const after = this.host.probe()
    if (after.alt) return // 没有回到普通屏幕（不该发生）：不往备用屏幕里写东西
    this.altKept++
    await this.host.write(enc.encode(altScreenBlock(rows, before.cols, after.cursorX === 0)))
  }

  private async handle(kind: FlattenKind, raw: Uint8Array, at: number): Promise<void> {
    await this.flush()
    if (kind === 'altexit') {
      await this.handleAltExit(raw, at)
      return
    }
    const p = this.host.probe()
    // 全屏程序里的清屏：备用屏幕没有回滚，照常处理。
    if (p.alt) {
      await this.host.write(raw)
      return
    }

    if (kind === 'scrollback') {
      if (this.decide(at) === 'faithful') {
        this.lastPushed = '' // 历史被照原样抹掉了：之前推进去的画面不在了，之后相同的画面应当能再推一次
        await this.host.write(raw)
      }
      return // 平铺时忽略「清除回滚缓冲区」
    }

    // ESC[J 只有光标在左上角时才等于清整屏；其他位置是清光标之后的部分，不属于清屏。
    if (kind === 'below' && !(p.cursorX === 0 && p.cursorY === 0)) {
      await this.host.write(raw)
      return
    }

    const decision = this.decide(at)
    if (decision === 'keep' && p.used > 0 && p.fingerprint !== this.lastPushed && this.pushes < this.maxPushes) {
      await this.host.write(enc.encode(pushScreenSequence(p.rows, p.used)))
      this.lastPushed = p.fingerprint
      this.pushes++
    }

    if (kind === 'reset') {
      // RIS 会连回滚缓冲区一起清掉：平铺时换成软复位（恢复模式与属性），屏幕内容已经推进历史，再清屏回到左上角。
      if (decision === 'keep') await this.host.write(enc.encode('\x1b[!p\x1b[H\x1b[2J'))
      else {
        this.lastPushed = ''
        await this.host.write(raw)
      }
      return
    }
    // 屏幕现在是空的；原来的清屏序列照常发出，以保留它对背景色等状态的影响。
    await this.host.write(raw)
  }
}
