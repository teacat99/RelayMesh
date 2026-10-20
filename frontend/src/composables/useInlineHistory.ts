import { ref } from 'vue'
import type { IMarker, Terminal } from '@xterm/xterm'
import { UnlockCancelled, toApiError } from '../api/terminal'
import { dividerLine } from '../lib/terminal/divider'
import { Flattener, pushScreenSequence } from '../lib/terminal/flatten'
import { extScrollback, maxInlineLoaded } from '../lib/terminal/historyBudget'
import { HistoryLoader, LoaderCancelled, type RecordSource } from '../lib/terminal/historyLoader'
import { planExtend, type PlanResult } from '../lib/terminal/historyPlan'
import { UnloadClock, type ViewSample } from '../lib/terminal/historyUnload'
import { OutputTape, splitTape, streamOf, type TapeChunk } from '../lib/terminal/outputTape'
import { formatSize, gapReasonLabel } from '../lib/terminal/record'
import type { ReplayMute } from '../lib/terminal/replayMute'
import { resyncViewport } from '../lib/terminal/viewportSync'
import { probeScreen } from '../lib/terminal/xtermProbe'

// 实时终端里的「历史日志」：往上滚到顶之后继续往上滚（不需要再确认），把主机上记录里更早的输出接在实时内容的上方，分割线隔开，连续地往上滚着看。
//
// xterm 的缓冲区只能往后写，没法在最前面插入内容，所以做法是：先读取记录，用实时内容开头的一小段字节在记录里定位
// （两者是同一条字节流，见 lib/terminal/historyPlan.ts），然后把终端清空重建为「历史（平铺）→ 分割线 → 实时内容」。
// 实时内容取自输出带（lib/terminal/outputTape.ts，页面打开以来写进终端的字节）；重建期间到达的新输出先排队，重建完成后再写，
// 终端的尺寸不变，所以不会给会话的 PTY 发送任何尺寸变化。
//
// 分割线只是写进本地缓冲区的一行文字：不会发给主机，也不会进入记录；再次重建时会按新的内容重新生成。

export type InlineState = 'none' | 'loading' | 'extended'

export interface InlineHistoryOptions {
  term: () => Terminal | null
  mute: ReplayMute
  source: () => RecordSource
  canView: () => boolean
  /** 当前会话状态（运行中 / 已结束……），写进分割线。 */
  status: () => string
  /** 记录里找不到实时内容的开头、无法无缝接续时调用；调用方退回「完整记录」图层。 */
  onFallback: (why: string) => void
  /**
   * 当前视图状态，供「用完即还」判断（见 lib/terminal/historyUnload.ts）：回到实时底部一阵子、或面板被切走很久，
   * 就把接续进来的历史卸掉。不提供就不卸载。
   */
  view?: () => ViewSample
}

/**
 * 普通状态的回滚行数（与终端创建时一致）；接续历史后按内存预算放大（见 lib/terminal/historyBudget.ts 的 extScrollback），
 * 用完卸载时回到这个值。
 */
export const BASE_SCROLLBACK = 5000
/** 重建时每次写入的上限：回放期间压制应答的窗口以「写入完成」为界，单次不能太大。 */
const WRITE_CHUNK = 256 * 1024
const NOTICE_MS = 6000
/** 接续之后多久检查一次「该不该卸载」。 */
const MONITOR_MS = 2000

const enc = new TextEncoder()
const noop = () => {}

type OkPlan = Extract<PlanResult, { kind: 'ok' }>

function gapNotice(reason: string, bytes: number): string {
  const extra = bytes > 0 ? `，约 ${formatSize(bytes)} 输出未保存` : ''
  // 先发 CAN 终止可能被缺口截断的转义序列，保证提示本身不被吞掉。
  return `\x18\x1b[0m\r\n\x1b[2;3m── 此处缺少记录：${gapReasonLabel(reason)}${extra} ──\x1b[0m\r\n`
}

export function useInlineHistory(o: InlineHistoryOptions) {
  const state = ref<InlineState>('none')
  /** 读取 / 渲染进度（loading 时显示）。 */
  const progress = ref('')
  /** 一条短暂的提示（读取失败、没有更多历史……）。 */
  const notice = ref('')
  /** 提示的口吻：warn = 出了问题（读取失败、离线）；hint = 只是陈述事实（已是记录的开头、还没有历史）。 */
  const noticeTone = ref<'hint' | 'warn'>('warn')
  /** 重建之后：窗口之前是否还有记录可以继续往前读。 */
  const earlier = ref(false)
  /** 重建之后：记录最早保留的分段不是第 1 段，更早的内容已按配额清理。 */
  const headCut = ref(false)

  const tape = new OutputTape()
  let loader: HistoryLoader | null = null
  let generation = 0
  let rebuilding = false
  /** 接续历史之后，实时输出里的「清除回滚缓冲区」（ESC[3J，clear 命令会发）不能把刚接上的历史一并抹掉。 */
  let protect = false
  let queue: Array<{ data: Uint8Array; done: () => void; replay: boolean }> = []
  let marker: IMarker | null = null
  let noticeTimer: ReturnType<typeof setTimeout> | null = null
  let disposed = false
  const unloadClock = new UnloadClock()
  let monitor: ReturnType<typeof setInterval> | null = null

  const stale = (gen: number) => disposed || gen !== generation

  // ---------- 用完即还 ----------

  function stopMonitor() {
    if (monitor) clearInterval(monitor)
    monitor = null
    unloadClock.reset()
  }

  /** 接续成功之后开始盯着视图：停回实时底部一阵子、或面板被切走很久，就卸载。 */
  function startMonitor() {
    if (monitor || !o.view) return
    unloadClock.reset()
    monitor = setInterval(() => {
      if (disposed || state.value !== 'extended' || rebuilding) return
      const v = o.view?.()
      if (v && unloadClock.sample(Date.now(), v)) unload()
    }, MONITOR_MS)
  }

  /**
   * 卸载接续进来的历史：回滚缓冲区回到基础大小（xterm 在缩小 scrollback 的同时立刻丢掉最前面多出来的行，内存随之释放），
   * 记录读取器和已载入的记录页一并丢弃。再往上滚到顶时，会像第一次那样重新读取。
   * 视口只在「离开很久」的情形下被拉回底部（停在底部时本来就在底部）。
   */
  function unload() {
    if (disposed || state.value !== 'extended' || rebuilding) return
    generation++
    stopMonitor()
    loader?.cancel()
    loader = null
    protect = false
    marker?.dispose()
    marker = null
    state.value = 'none'
    progress.value = ''
    earlier.value = false
    headCut.value = false
    const t = o.term()
    if (t) {
      t.options.scrollback = BASE_SCROLLBACK
      t.scrollToBottom()
      // 缩小 scrollback 后 xterm 的滚动条还停在旧长度上，不重新同步的话往上滚会被卡住。
      resyncViewport(t)
    }
  }

  function setNotice(text: string, tone: 'hint' | 'warn' = 'warn') {
    if (noticeTimer) clearTimeout(noticeTimer)
    noticeTimer = null
    notice.value = text
    noticeTone.value = tone
    if (text) {
      noticeTimer = setTimeout(() => {
        noticeTimer = null
        notice.value = ''
      }, NOTICE_MS)
    }
  }

  /** 终端创建之后调用一次。 */
  function attach(t: Terminal) {
    t.parser.registerCsiHandler({ final: 'J' }, params => protect && params[0] === 3)
  }

  // ---------- 供 TerminalConnection 的 Sink 使用的写入通道 ----------

  function write(data: Uint8Array, done: () => void, replay: boolean, kind?: 'prefix') {
    tape.push(kind === 'prefix' ? 'prefix' : 'stream', data)
    if (rebuilding) {
      queue.push({ data, done, replay })
      return
    }
    o.term()?.write(data, o.mute.wrap(replay, done))
  }

  function localNotice(text: string) {
    const bytes = enc.encode(`\r\n\x1b[2m[RelayMesh] ${text}\x1b[0m\r\n`)
    tape.push('local', bytes)
    if (rebuilding) {
      queue.push({ data: bytes, done: noop, replay: false })
      return
    }
    o.term()?.write(bytes)
  }

  /** 全新附着前清空终端：已经接上的历史、输出带和读取进度都作废。 */
  function reset() {
    generation++
    stopMonitor()
    loader?.cancel()
    loader = null
    tape.reset()
    queue = []
    rebuilding = false
    protect = false
    state.value = 'none'
    progress.value = ''
    earlier.value = false
    headCut.value = false
    marker?.dispose()
    marker = null
    const t = o.term()
    if (t) {
      t.options.scrollback = BASE_SCROLLBACK
      t.reset()
    }
  }

  // ---------- 重建 ----------

  /** 写入并等它解析完；回放性质的写入期间压制终端生成的应答，大块拆开写。 */
  function writeMuted(t: Terminal, data: Uint8Array | string): Promise<void> {
    return new Promise<void>(resolve => {
      if (typeof data === 'string' || data.length <= WRITE_CHUNK) {
        t.write(data, o.mute.wrap(true, resolve))
        return
      }
      let pos = 0
      const next = () => {
        if (pos >= data.length) {
          resolve()
          return
        }
        const part = data.subarray(pos, pos + WRITE_CHUNK)
        pos += part.length
        t.write(part, o.mute.wrap(true, next))
      }
      next()
    })
  }

  /** 等已经交给终端、还没解析完的写入都处理完（重置前必须做，否则它们会写进重建后的缓冲区）。 */
  function drain(t: Terminal): Promise<void> {
    return new Promise<void>(resolve => {
      const timer = setTimeout(resolve, 2000)
      t.write('', () => {
        clearTimeout(timer)
        resolve()
      })
    })
  }

  function flushQueue(t: Terminal) {
    const q = queue
    queue = []
    for (const it of q) t.write(it.data, o.mute.wrap(it.replay, it.done))
  }

  async function writeDivider(t: Terminal) {
    // CAN 终止可能被截断的转义序列、复位属性；光标不在行首就先换行，分割线独占一行。
    await writeMuted(t, '\x18\x1b[0m')
    if (t.buffer.active.cursorX > 0) await writeMuted(t, '\r\n')
    marker?.dispose()
    marker = t.registerMarker(0) ?? null
    const line = dividerLine(t.cols, [
      ` ↑ 历史日志（会话记录） · ↓ 实时终端 · ${o.status()} `,
      ' ↑ 历史日志 · ↓ 实时终端 ',
      ' ↑ 历史 · ↓ 实时 ',
    ])
    await writeMuted(t, `\x1b[2;36m${line}\x1b[0m`)
    // 把屏幕上到分割线为止的内容整体推进回滚缓冲区：实时内容从一块空屏的左上角写起，和它最初在新终端里的样子一致
    // （清屏之后的内容常按「屏幕左上角」定位）。先撤销滚动区域 / 原点模式，免得它们挡住这次推进。
    const used = t.buffer.active.cursorY + 1
    await writeMuted(t, `\x1b[r\x1b[?6l${pushScreenSequence(t.rows, used)}\x1b[H`)
  }

  /** 失败后的恢复：把整条输出带原样写回，终端回到重建之前的样子（没有历史）。 */
  async function recover(t: Terminal, snap: TapeChunk[]) {
    try {
      protect = false
      t.reset()
      t.options.scrollback = BASE_SCROLLBACK
      for (const c of snap) await writeMuted(t, c.data)
    } catch {
      /* 终端已被销毁 */
    }
  }

  async function rebuild(gen: number, t: Terminal, plan: OkPlan, snap: TapeChunk[], keepSeam: boolean): Promise<boolean> {
    rebuilding = true
    const linesBefore = t.buffer.active.length
    const split = splitTape(snap, plan.tapeCut)
    // 记录里没有（落后或衔接处对不上）的那一段清屏前的内容，从输出带里补上。
    const mid = splitTape(split.head, plan.tapeHead).tail
    let ok = false
    try {
      await drain(t)
      if (stale(gen)) return false
      t.reset()
      t.options.scrollback = extScrollback(t.cols)
      protect = false
      const sk = new Flattener({ write: d => writeMuted(t, d), probe: () => probeScreen(t) })
      let clock = 0
      for (const it of plan.history) {
        if (stale(gen)) return false
        clock = it.at
        if (it.type === 'output') await sk.feed(it.data, it.at)
        else if (it.type === 'gap') {
          await sk.finish()
          await writeMuted(t, gapNotice(it.reason, it.bytes))
        }
        // 尺寸变化 / 分段 / 结束标记：历史按当前终端的宽度排版，不再跟着录制时的窗口大小变。
      }
      for (const c of mid) {
        if (stale(gen)) return false
        if (c.kind === 'stream') {
          // 输出带里没有可靠的时间，按「彼此相隔很久」处理：人敲的 clear 之前的屏幕会被保留。
          clock += 10_000
          await sk.feed(c.data, clock)
        } else {
          await sk.finish()
          await writeMuted(t, c.data)
        }
      }
      await sk.finish()
      if (stale(gen)) return false
      await writeDivider(t)
      protect = true
      for (const c of split.tail) {
        if (stale(gen)) return false
        await writeMuted(t, c.data)
      }
      ok = true
      return true
    } catch {
      await recover(t, snap)
      return false
    } finally {
      if (!stale(gen)) {
        rebuilding = false
        flushQueue(t)
        const m = marker
        if (ok && m && !m.isDisposed) {
          if (keepSeam) {
            // 又往前加载了一批：停在新旧历史的接缝附近，继续往上滚就是刚加载的内容。
            const added = t.buffer.active.length - linesBefore
            t.scrollToLine(Math.max(0, added - Math.floor(t.rows / 3)))
          } else {
            // 分割线落在屏幕偏下的位置：上面是历史，下面是实时内容的开头。
            t.scrollToLine(Math.max(0, m.line - Math.floor(t.rows * 0.6)))
          }
        }
      }
    }
  }

  // ---------- 入口 ----------

  function makePlan(): { plan: PlanResult; snap: TapeChunk[] } {
    // 快照与规划必须在同一个同步片段里完成，并紧接着开始重建：这样「快照之后到达的输出」恰好就是重建期间排队的那些。
    const snap = tape.snapshot()
    const plan: PlanResult = loader
      ? planExtend(loader.items, streamOf(snap), { atBeginning: loader.atBeginning })
      : { kind: 'not_found' }
    return { plan, snap }
  }

  /**
   * first：第一次接续（读取最近的窗口并定位）；earlier：已经接上之后再往前加载一批更早的历史。
   * 失败 / 取消时终端保持原样。
   */
  async function run(mode: 'first' | 'earlier'): Promise<void> {
    const t = o.term()
    if (!t || disposed || state.value === 'loading') return
    if (mode === 'first' && (!o.canView() || state.value !== 'none')) return
    if (mode === 'earlier' && (state.value !== 'extended' || !loader || !loader.canLoadEarlier)) return
    const back: InlineState = mode === 'first' ? 'none' : 'extended'
    const gen = ++generation
    state.value = 'loading'
    setNotice('')
    progress.value = '正在读取历史日志…'
    try {
      if (mode === 'first') {
        loader?.cancel()
        const l = new HistoryLoader(o.source(), {
          // 载入量跟着缓冲区的内存预算走：窗口再大，多出来的也会被缓冲区丢掉。
          maxLoaded: maxInlineLoaded(extScrollback(t.cols)),
          onBytes: n => {
            progress.value = `正在读取历史日志… ${formatSize(n)}`
          },
        })
        loader = l
        const r = await l.open()
        if (stale(gen)) return
        if (r !== 'ok') {
          loader = null
          state.value = back
          if (r === 'offline') setNotice('主机离线，暂时读不了历史日志。')
          else setNotice(`已到顶 · ${o.status()} · 这个会话还没有可显示的历史日志`, 'hint')
          return
        }
      } else {
        await loader!.loadEarlier()
        if (stale(gen)) return
      }

      let made = makePlan()
      for (let i = 0; i < 4 && loader; i++) {
        const k = made.plan.kind
        if ((k !== 'need_earlier' && k !== 'not_found') || !loader.canLoadEarlier) break
        // 实时内容的开头在窗口之前：多读一批再找。
        progress.value = '正在读取更早的历史日志…'
        await loader.loadEarlier()
        if (stale(gen)) return
        made = makePlan()
      }

      const { plan, snap } = made
      if (plan.kind === 'ok') {
        progress.value = '正在接续显示…'
        const ok = await rebuild(gen, t, plan, snap, mode === 'earlier')
        if (stale(gen)) return
        if (ok) {
          state.value = 'extended'
          earlier.value = !!loader?.canLoadEarlier
          headCut.value = !!loader?.headCut
          startMonitor()
        } else {
          state.value = back
          setNotice('接续显示历史日志时出错，已恢复原来的终端内容。')
        }
        return
      }
      state.value = back
      if (plan.kind === 'nothing') {
        setNotice(`已到顶 · ${o.status()} · 已是这个会话记录的开头`, 'hint')
        return
      }
      if (mode === 'earlier') {
        setNotice('无法继续往前接续，更早的内容请在右上角打开完整记录。')
        return
      }
      o.onFallback(plan.kind === 'no_sample' ? '实时内容太短，无法定位' : '记录里找不到实时内容的开头')
    } catch (e) {
      if (e instanceof LoaderCancelled || stale(gen)) return
      state.value = back
      if (e instanceof UnlockCancelled) {
        setNotice('需要先解锁才能读取历史日志。')
        return
      }
      setNotice(`读取历史日志失败：${toApiError(e).message}`)
    } finally {
      if (!stale(gen)) progress.value = ''
    }
  }

  function dispose() {
    disposed = true
    generation++
    stopMonitor()
    loader?.cancel()
    loader = null
    if (noticeTimer) clearTimeout(noticeTimer)
    marker?.dispose()
    marker = null
    queue = []
  }

  return {
    state,
    progress,
    notice,
    noticeTone,
    earlier,
    headCut,
    attach,
    write,
    localNotice,
    reset,
    run,
    unload,
    dispose,
  }
}
