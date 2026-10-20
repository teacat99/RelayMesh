// 从主机上的记录里按窗口读取历史（纯逻辑，读取接口由调用方注入，便于单元测试）。
//
// 和独立的「记录回放」视图读的是同一份数据，但这里只服务于「接续显示历史日志」：不跟随最新（实时内容由终端自己显示），
// 只关心「实时内容开头之前」的那一段，所以窗口默认只取最近约 3 MiB，需要时再往前多带一批。

import {
  KIND_MARK,
  RecordTimeline,
  decodeEvents,
  decodeMark,
  extendWindowStart,
  parseRecInfo,
  pickWindowStart,
  WINDOW_BUDGET,
  type Item,
  type RecEvent,
  type RecInfo,
} from './record'

export interface RecordPageLike {
  data: Uint8Array
  nextSeg: number
  nextOff: number
  eof: boolean
}

/** 记录的读取接口；解锁、并发闸门等都在注入的实现里处理。 */
export interface RecordSource {
  info(): Promise<{ online: boolean; info?: unknown }>
  page(seg: number, off: number): Promise<RecordPageLike>
}

/** 历史窗口最多载入的原始字节：再多浏览器渲染会明显变慢（更早的内容在「完整记录」里看）。 */
export const MAX_INLINE_LOADED = 12 * 1024 * 1024

export type OpenResult = 'ok' | 'offline' | 'empty'

export class LoaderCancelled extends Error {
  constructor() {
    super('cancelled')
    this.name = 'LoaderCancelled'
  }
}

function concat2(a: Uint8Array, b: Uint8Array): Uint8Array {
  const out = new Uint8Array(a.length + b.length)
  out.set(a, 0)
  out.set(b, a.length)
  return out
}

export class HistoryLoader {
  /** 已载入窗口里的全部条目（按时间顺序）。 */
  items: Item[] = []
  info: RecInfo | null = null
  /** 窗口起点在分段列表里的下标；0 表示窗口已经从记录的第一个分段开始。 */
  startIdx = 0
  loadedBytes = 0
  private cancelled = false

  constructor(
    private readonly src: RecordSource,
    private readonly opts: { windowBytes?: number; maxLoaded?: number; onBytes?: (loaded: number) => void } = {},
  ) {}

  private get windowBytes(): number {
    return this.opts.windowBytes ?? WINDOW_BUDGET
  }

  private get maxLoaded(): number {
    return this.opts.maxLoaded ?? MAX_INLINE_LOADED
  }

  /** 窗口的开头就是记录的开头。 */
  get atBeginning(): boolean {
    return this.startIdx === 0
  }

  /** 还能再往前读：窗口前面还有分段，且已载入量没有碰到上限。 */
  get canLoadEarlier(): boolean {
    return this.startIdx > 0 && this.loadedBytes < this.maxLoaded
  }

  /** 记录最早保留的分段不是第 1 段：更早的内容已按配额清理。 */
  get headCut(): boolean {
    return !!this.info?.head
  }

  cancel(): void {
    this.cancelled = true
  }

  private check(): void {
    if (this.cancelled) throw new LoaderCancelled()
  }

  /** 读取记录描述和最近的一个窗口。 */
  async open(): Promise<OpenResult> {
    this.check()
    const r = await this.src.info()
    this.check()
    if (!r.online) return 'offline'
    this.info = parseRecInfo(r.info)
    const list = this.info?.segments ?? []
    if (list.length === 0) return 'empty'
    this.startIdx = pickWindowStart(list, this.windowBytes)
    const events = await this.readRange(list[this.startIdx].seq, null)
    const tl = new RecordTimeline()
    tl.push(events)
    this.items = tl.items.slice()
    this.loadedBytes = list.slice(this.startIdx).reduce((n, s) => n + s.raw, 0)
    return 'ok'
  }

  /** 在现有窗口之前再多带一批分段（同样受预算限制，至少多带一个）。 */
  async loadEarlier(): Promise<void> {
    const info = this.info
    if (!info || this.startIdx <= 0) return
    this.check()
    const newStart = extendWindowStart(info.segments, this.startIdx, this.windowBytes)
    const stopSeq = info.segments[this.startIdx].seq
    const events = await this.readRange(info.segments[newStart].seq, stopSeq)
    const tl = new RecordTimeline()
    tl.push(events)
    this.items = tl.items.concat(this.items)
    this.loadedBytes += info.segments.slice(newStart, this.startIdx).reduce((n, s) => n + s.raw, 0)
    this.startIdx = newStart
  }

  /** 从分段 fromSeq 的开头往后读；遇到序号 ≥ stopSeq 的分段起点就停（向前补读时避免和已载入的重叠）。 */
  private async readRange(fromSeq: number, stopSeq: number | null): Promise<RecEvent[]> {
    let seg = fromSeq
    let off = 0
    let rest: Uint8Array = new Uint8Array(0)
    const out: RecEvent[] = []
    let loaded = 0
    let idle = 0
    for (;;) {
      this.check()
      const page = await this.src.page(seg, off)
      this.check()
      const merged = rest.length ? concat2(rest, page.data) : page.data
      const { events, used } = decodeEvents(merged)
      rest = merged.subarray(used)
      for (const ev of events) {
        if (stopSeq !== null && ev.kind === KIND_MARK) {
          const m = decodeMark(ev.payload)
          if (m?.t === 'seg' && (m.seq ?? 0) >= stopSeq) return out
        }
        out.push(ev)
      }
      loaded += page.data.length
      this.opts.onBytes?.(loaded)
      seg = page.nextSeg
      off = page.nextOff
      if (page.eof) return out
      idle = page.data.length === 0 ? idle + 1 : 0
      if (idle > 3) throw new Error('记录读取没有进展，请稍后重试')
    }
  }
}
