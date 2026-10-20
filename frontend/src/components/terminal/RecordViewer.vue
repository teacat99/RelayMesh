<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import type { Terminal } from '@xterm/xterm'
import type { SearchAddon } from '@xterm/addon-search'
import '@xterm/xterm/css/xterm.css'
import { ArrowDownToLine, ChevronDown, ChevronUp, CircleAlert, Download, History, Info, Loader2, Lock, RadioTower, RotateCw, Search, WifiOff, X } from 'lucide-vue-next'
import { toast } from 'vue-sonner'
import Button from '../ui/button/Button.vue'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from '../ui/dropdown-menu'
import { TERM_FONT_FAMILY, terminalTheme } from '../../lib/terminal/theme'
import { UnlockCancelled, terminalApi, toApiError, type HistoryEntry } from '../../api/terminal'
import { useTerminalStore } from '../../stores/terminal'
import { useThemeStore } from '../../stores/theme'
import { useTopOverscroll } from '../../composables/useTopOverscroll'
import { isImeKey } from '../../lib/ime'
import { Flattener, type FlattenHost, type ScreenProbe } from '../../lib/terminal/flatten'
import { EMPTY_PROBE, probeScreen } from '../../lib/terminal/xtermProbe'
import {
  KIND_MARK,
  RecordCorruptError,
  RecordTimeline,
  decodeEvents,
  decodeMark,
  endReasonLabel,
  extendWindowStart,
  formatSize,
  formatSpan,
  gapReasonLabel,
  initialSize,
  parseRecInfo,
  pickWindowStart,
  rawBytes,
  recStateInfo,
  safeFileName,
  toAsciicast,
  toPlainText,
  type Item,
  type RecEvent,
  type RecInfo,
} from '../../lib/terminal/record'

// 终端记录回放：只读的 xterm，按窗口加载（默认最近约 3 MiB），可向前加载更早的内容、搜索、下载。
// 记录的是「终端上显示的内容」，内容保存在主机上，这里每次按需经 Hub 转发读取，Hub 不落盘内容。
const props = defineProps<{
  code: string
  sid: string
  label: string
  active: boolean
  fontSize: number
  /** 嵌在实时终端里作为「历史日志」图层：标题换成历史日志，多一个回到实时的按钮，Esc 也能退回。 */
  embedded?: boolean
}>()
const emit = defineEmits<{ (e: 'close'): void }>()

const store = useTerminalStore()
const themeStore = useThemeStore()

type Phase = 'loading' | 'ready' | 'locked' | 'offline' | 'gone' | 'error'
const phase = ref<Phase>('loading')
const errorText = ref('')
const progress = ref('')

const info = ref<RecInfo | null>(null)
const index = ref<HistoryEntry | null>(null)
const online = ref(false)
const startIdx = ref(0)
const loadedBytes = ref(0)
const gapCount = ref(0)
const itemCount = ref(0)
const scrollbackCut = ref(false)
const endInfo = ref<{ reason: string; code: number | null; signal: string } | null>(null)
const follow = ref(true)
const followStopped = ref('')
const busy = ref(false)
const downloading = ref('')

// 显示方式：平铺（默认）让 clear 之类的清屏不再抹掉更早的内容，向上滚就能看到；还原则一模一样地重放。
type ViewMode = 'flat' | 'faithful'
const MODE_KEY = 'relaymesh_rec_mode'
function readMode(): ViewMode {
  try {
    return localStorage.getItem(MODE_KEY) === 'faithful' ? 'faithful' : 'flat'
  } catch {
    return 'flat'
  }
}
const viewMode = ref<ViewMode>(readMode())

const SCROLLBACK = 30_000
const PAGE_BYTES = 1024 * 1024
const POLL_BYTES = 256 * 1024
const POLL_MS = 1500
/** 一个窗口里最多载入的原始字节：再多浏览器会明显变慢，更早的内容请下载查看。 */
const MAX_LOADED = 24 * 1024 * 1024
const MAX_DOWNLOAD = 256 * 1024 * 1024

const rootEl = ref<HTMLElement | null>(null)
const scrollEl = ref<HTMLElement | null>(null)
const hostEl = ref<HTMLElement | null>(null)

let term: Terminal | null = null
let flat: Flattener | null = null
let search: SearchAddon | null = null
let resizeObserver: ResizeObserver | null = null
let disposed = false
let gen = 0
let allItems: Item[] = []
let timeline = new RecordTimeline()
let cursor = { seg: 0, off: 0 }
let carry: Uint8Array = new Uint8Array(0)
let pollTimer: ReturnType<typeof setTimeout> | null = null
let polling = false
let recRows = 0
let recCols = 0
let layoutFrame = 0
let downloadAbort: AbortController | null = null
// 整块重绘期间不让「跟随最新」往终端里写，免得新内容和重绘出来的内容重叠。
let rendering = false

class Cancelled extends Error {}

function stale(token: number): boolean {
  return disposed || token !== gen
}

// ---------- 展示状态 ----------
const isEnded = computed(() => !!endInfo.value || (info.value?.ended_ms ?? 0) > 0 || !!index.value?.ended_at)
const running = computed(() => !isEnded.value && (phase.value === 'ready' || phase.value === 'offline'))
const segs = computed(() => info.value?.segments ?? [])
// 终端缓冲只保留最近 SCROLLBACK 行：窗口已经超出时，再往前加载也看不到更多内容，按钮没有意义（改用「下载」）。
const canLoadEarlier = computed(() => phase.value === 'ready' && startIdx.value > 0 && !scrollbackCut.value && loadedBytes.value < MAX_LOADED)
const live = computed(() => phase.value === 'ready' && online.value && !isEnded.value && !followStopped.value)

const statusChip = computed(() => {
  if (phase.value === 'locked') return null
  if (running.value) {
    return online.value
      ? { text: '进行中', cls: 'bg-emerald-500/10 text-emerald-600 dark:text-emerald-400' }
      : { text: '进行中 · 主机离线', cls: 'bg-amber-500/10 text-amber-600 dark:text-amber-400' }
  }
  const reason = endInfo.value?.reason || info.value?.ended_reason || index.value?.ended_reason
  if (!reason) return null
  const code = endInfo.value?.code ?? info.value?.exit_code ?? index.value?.exit_code
  const text = endReasonLabel(reason) + (reason === 'exit' && typeof code === 'number' ? `（退出码 ${code}）` : '')
  return { text, cls: 'bg-muted text-muted-foreground' }
})

const createdMs = computed(() => info.value?.created_ms || (index.value?.started_at ? index.value.started_at * 1000 : 0))
const metaText = computed(() => {
  const parts: string[] = []
  if (createdMs.value > 0) parts.push(`开始 ${new Date(createdMs.value).toLocaleString('zh-CN', { hour12: false })}`)
  const end = info.value?.ended_ms || (index.value?.ended_at ? Date.parse(index.value.ended_at) : 0)
  if (createdMs.value > 0) parts.push(`时长 ${formatSpan((end > 0 ? end : Date.now()) - createdMs.value)}`)
  if (info.value) parts.push(`${formatSize(info.value.raw_bytes)}`)
  if (startIdx.value > 0 && segs.value.length) parts.push(`已载入最近 ${segs.value.length - startIdx.value}/${segs.value.length} 段`)
  return parts.join(' · ')
})

const recHint = computed(() => {
  const echo = '记录的是终端上显示的内容（含 Shell 对键入的回显）；隐藏输入的密码因为不回显，不会出现在记录里。'
  const st = recStateInfo(index.value?.rec)
  if (!st) return echo
  // 「正被记录」只对还在运行的会话成立；已结束的会话说明内容放在哪里就够了。
  return isEnded.value ? `${echo}内容保存在主机上，不经过服务端存储。` : st.title
})

const showHeadBanner = computed(() => !!info.value?.head && startIdx.value === 0)

// ---------- 终端 ----------
function themeOptions() {
  return terminalTheme(themeStore.isDark)
}

async function ensureTerm(): Promise<boolean> {
  if (term) return true
  const el = hostEl.value
  if (!el) return false
  const [{ Terminal }, { Unicode11Addon }, { WebLinksAddon }, { SearchAddon }] = await Promise.all([
    import('@xterm/xterm'),
    import('@xterm/addon-unicode11'),
    import('@xterm/addon-web-links'),
    import('@xterm/addon-search'),
  ])
  if (disposed || term || !hostEl.value) return !!term
  const t = new Terminal({
    fontFamily: TERM_FONT_FAMILY,
    fontSize: props.fontSize,
    lineHeight: 1.15,
    cursorBlink: false,
    cursorStyle: 'underline',
    cursorInactiveStyle: 'none',
    disableStdin: true,
    scrollback: SCROLLBACK,
    allowProposedApi: true,
    minimumContrastRatio: 4.5,
    theme: themeOptions(),
  })
  t.loadAddon(new Unicode11Addon())
  t.unicode.activeVersion = '11'
  t.loadAddon(
    new WebLinksAddon((_event, uri) => {
      try {
        const u = new URL(uri)
        if (u.protocol === 'http:' || u.protocol === 'https:') window.open(u.href, '_blank', 'noopener,noreferrer')
      } catch {
        /* 非法链接：忽略 */
      }
    }),
  )
  const s = new SearchAddon()
  t.loadAddon(s)
  s.onDidChangeResults(r => {
    if (!searchText.value) searchStat.value = ''
    else if (r.resultCount === 0) searchStat.value = '没有匹配'
    else if (r.resultIndex < 0) searchStat.value = `${r.resultCount}+ 处`
    else searchStat.value = `${r.resultIndex + 1} / ${r.resultCount}`
  })
  t.open(hostEl.value)
  // 仅供端到端测试读取屏幕内容（不可枚举，不参与任何业务逻辑）。
  Object.defineProperty(hostEl.value, '__xterm', { value: t, configurable: true })
  term = t
  search = s
  void loadWebgl(t)
  return true
}

async function loadWebgl(t: Terminal) {
  try {
    const { WebglAddon } = await import('@xterm/addon-webgl')
    if (disposed || term !== t) return
    const gl = new WebglAddon()
    gl.onContextLoss(() => gl.dispose())
    t.loadAddon(gl)
  } catch {
    /* 没有 WebGL 支持：保持 DOM 渲染 */
  }
}

/** 单元格尺寸取自 xterm 自己排出来的屏幕元素，不依赖内部接口。 */
function cellSize(): { w: number; h: number } | null {
  const screen = hostEl.value?.querySelector<HTMLElement>('.xterm-screen')
  if (!screen || !term || screen.offsetWidth === 0 || screen.offsetHeight === 0) return null
  return { w: screen.offsetWidth / term.cols, h: screen.offsetHeight / term.rows }
}

function fitDims(): { cols: number; rows: number } | null {
  const sc = scrollEl.value
  const cell = cellSize()
  if (!sc || !cell || sc.clientWidth < 40 || sc.clientHeight < 40) return null
  return { cols: Math.floor(sc.clientWidth / cell.w), rows: Math.floor(sc.clientHeight / cell.h) }
}

const clampInt = (n: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, Math.round(n)))

/**
 * 列数忠实于录制时的窗口（全屏程序的排版依赖它；容器放不下时横向滚动），
 * 行数不小于录制时的行数，也不小于容器能放下的行数（高窗口里不留大片空白）。
 */
function applySize() {
  if (!term) return
  const fit = fitDims()
  const cols = clampInt(recCols || fit?.cols || 80, 10, 500)
  const rows = clampInt(Math.max(recRows || 0, fit?.rows || 24), 2, 400)
  if (term.cols !== cols || term.rows !== rows) term.resize(cols, rows)
}

function scheduleLayout() {
  if (layoutFrame) return
  layoutFrame = requestAnimationFrame(() => {
    layoutFrame = 0
    if (props.active) applySize()
  })
}

function writeAsync(data: Uint8Array | string): Promise<void> {
  return new Promise(resolve => {
    if (!term) return resolve()
    term.write(data, resolve)
  })
}

function concatAll(parts: Uint8Array[]): Uint8Array {
  let n = 0
  for (const p of parts) n += p.length
  const out = new Uint8Array(n)
  let pos = 0
  for (const p of parts) {
    out.set(p, pos)
    pos += p.length
  }
  return out
}

function gapNotice(reason: string, bytes: number): string {
  const extra = bytes > 0 ? `，约 ${formatSize(bytes)} 输出未保存` : ''
  // 先发 CAN 终止可能被缺口截断的转义序列，保证提示本身不被吞掉。
  return `\x18\x1b[0m\r\n\x1b[2;3m── 此处缺少记录：${gapReasonLabel(reason)}${extra} ──\x1b[0m\r\n`
}

/** 平铺用：把清屏前屏幕上已有的内容推进历史、保留全屏程序退出前的最后一屏，都需要读 xterm 当前的屏幕状态。 */
function probeCurrent(): ScreenProbe {
  return term ? probeScreen(term) : EMPTY_PROBE
}

const flatHost: FlattenHost = { write: data => writeAsync(data), probe: probeCurrent }

/** 平铺模式下的输出通道；还原模式返回 null（直接按原样写）。跟随最新时复用同一个，清屏的判定跨批次连续。 */
function sink(): Flattener | null {
  if (viewMode.value !== 'flat' || !term) return null
  if (!flat) flat = new Flattener(flatHost)
  return flat
}

/** 把条目依次写入终端；输出合并成较大的块，尺寸变化与缺口在块之间按顺序生效。 */
async function writeItems(list: Item[], from: number, token: number, total = 0): Promise<void> {
  const sk = sink()
  let pending: Uint8Array[] = []
  let pendingBytes = 0
  let done = 0
  const report = () => {
    if (total > 0) progress.value = `正在渲染 ${Math.min(99, Math.floor((done / total) * 100))}%`
  }
  const flush = async () => {
    if (sk) await sk.flush()
    if (!pending.length) return
    const data = pending.length === 1 ? pending[0] : concatAll(pending)
    pending = []
    pendingBytes = 0
    await writeAsync(data)
    done += data.length
    report()
  }
  let sinceReport = 0
  for (let i = from; i < list.length; i++) {
    if (stale(token) || !term) return
    const it = list[i]
    if (it.type === 'output') {
      if (sk) {
        await sk.feed(it.data, it.at)
        done += it.data.length
        sinceReport += it.data.length
        if (sinceReport >= 128 * 1024) {
          sinceReport = 0
          report()
        }
        continue
      }
      pending.push(it.data)
      pendingBytes += it.data.length
      if (pendingBytes >= 128 * 1024) await flush()
    } else if (it.type === 'resize' || it.type === 'seg') {
      if (it.rows > 0 && it.cols > 0 && (it.rows !== recRows || it.cols !== recCols)) {
        await flush()
        if (sk) await sk.finish()
        recRows = it.rows
        recCols = it.cols
        applySize()
      }
    } else if (it.type === 'gap') {
      await flush()
      if (sk) await sk.finish()
      await writeAsync(gapNotice(it.reason, it.bytes))
    } else if (it.type === 'end') {
      if (sk) await sk.finish()
      endInfo.value = { reason: it.reason, code: it.code, signal: it.signal }
    }
  }
  await flush()
}

function refreshCounters() {
  let gaps = 0
  for (const it of allItems) if (it.type === 'gap') gaps++
  gapCount.value = gaps
  itemCount.value = allItems.length
}

function noteScrollback() {
  const b = term?.buffer.active
  scrollbackCut.value = !!b && !!term && b.length >= SCROLLBACK + term.rows
}

async function renderAll(token: number) {
  if (!(await ensureTerm()) || !term || stale(token)) return
  rendering = true
  try {
    term.reset()
    flat = null
    const size = initialSize(allItems)
    recRows = size?.rows ?? 0
    recCols = size?.cols ?? 0
    applySize()
    let total = 0
    for (const it of allItems) if (it.type === 'output') total += it.data.length
    progress.value = '正在渲染…'
    await writeItems(allItems, 0, token, total)
    if (stale(token)) return
    progress.value = ''
    refreshCounters()
    noteScrollback()
    term?.scrollToBottom()
  } finally {
    rendering = false
  }
}

// ---------- 读取 ----------
function describeError(e: unknown): { phase: Phase; text: string } {
  if (e instanceof UnlockCancelled) return { phase: 'locked', text: '查看终端记录需要先解锁。' }
  if (e instanceof RecordCorruptError) return { phase: 'error', text: '记录数据已损坏，无法解析。可以尝试下载原始文件排查。' }
  const err = toApiError(e)
  switch (err.code) {
    case 'record_gone':
      return { phase: 'gone', text: err.message }
    case 'session_not_found':
      return { phase: 'gone', text: '没有找到这个会话的记录。' }
    case 'host_offline':
      return { phase: 'offline', text: err.message }
    case 'busy':
    case 'agent_timeout':
      // 服务端的提示本身已带「请稍后重试」的就不再重复。
      return { phase: 'error', text: err.message.includes('稍后') ? `${err.message}。` : `${err.message}，请稍后重试。` }
    default:
      return { phase: 'error', text: err.message }
  }
}

function fetchPage(seg: number, off: number, o: { max?: number; download?: boolean; signal?: AbortSignal; interactive?: boolean } = {}) {
  const run = () => terminalApi.recordPage(props.code, props.sid, { seg, off, max: o.max ?? PAGE_BYTES, download: o.download, signal: o.signal })
  // 跟随最新是后台轮询：不为它弹出解锁对话框，锁定了就停下并提示。
  return o.interactive === false ? run() : store.withUnlock(run, '查看终端记录需要再次验证密码')
}

interface RangeResult {
  events: RecEvent[]
  next: { seg: number; off: number } | null
  eof: boolean
}

/** 从分段 fromSeq 的开头往后读；遇到序号 ≥ stopSeq 的分段起点就停（用于向前补读，避免和已载入的重叠）。 */
async function readRange(fromSeq: number, stopSeq: number | null, token: number, base = 0): Promise<RangeResult> {
  let seg = fromSeq
  let off = 0
  let rest: Uint8Array = new Uint8Array(0)
  const out: RecEvent[] = []
  let loaded = base
  let idle = 0
  for (;;) {
    const page = await fetchPage(seg, off)
    if (stale(token)) throw new Cancelled()
    const merged = rest.length ? concatAll([rest, page.data]) : page.data
    const { events, used } = decodeEvents(merged)
    rest = merged.subarray(used)
    for (const ev of events) {
      if (stopSeq !== null && ev.kind === KIND_MARK) {
        const m = decodeMark(ev.payload)
        if (m?.t === 'seg' && (m.seq ?? 0) >= stopSeq) return { events: out, next: null, eof: false }
      }
      out.push(ev)
    }
    loaded += page.data.length
    progress.value = `正在读取记录… ${formatSize(loaded)}`
    seg = page.nextSeg
    off = page.nextOff
    if (page.eof) return { events: out, next: { seg, off }, eof: true }
    idle = page.data.length === 0 ? idle + 1 : 0
    if (idle > 3) throw new Error('记录读取没有进展，请稍后重试')
  }
}

function resetData() {
  allItems = []
  timeline = new RecordTimeline()
  cursor = { seg: 0, off: 0 }
  carry = new Uint8Array(0)
  info.value = null
  startIdx.value = 0
  loadedBytes.value = 0
  gapCount.value = 0
  itemCount.value = 0
  scrollbackCut.value = false
  endInfo.value = null
  followStopped.value = ''
  stopPolling()
}

async function load() {
  const token = ++gen
  resetData()
  phase.value = 'loading'
  errorText.value = ''
  progress.value = '正在读取记录信息…'
  try {
    const r = await store.withUnlock(() => terminalApi.recordInfo(props.code, props.sid), '查看终端记录需要再次验证密码')
    if (stale(token)) return
    index.value = r.index
    online.value = r.online
    if (!r.online) {
      phase.value = 'offline'
      return
    }
    info.value = parseRecInfo(r.info)
    const list = info.value?.segments ?? []
    if (list.length > 0) {
      // 进行中与已结束的会话一视同仁：先只载入最近一段（默认约 3 MiB）并停在最末尾，更早的内容往上滚到顶后继续往上滚时
      // 再按窗口加载；整份记录只在「下载」时才传输。已结束的会话以前直接载入至多 24 MiB——小带宽链路上要等很久，
      // 浏览器一次渲染这么多内容也很慢，而人打开一个结束的会话通常只想看最后发生了什么。
      startIdx.value = pickWindowStart(list)
      const base = list.slice(startIdx.value).reduce((n, s) => n + s.raw, 0)
      const res = await readRange(list[startIdx.value].seq, null, token)
      if (stale(token)) return
      timeline.push(res.events)
      // timeline 只负责后续（跟随最新）事件的时钟；allItems 是整个窗口，向前补读时会整体替换，所以二者必须分开。
      allItems = timeline.items.slice()
      loadedBytes.value = base
      if (res.next) cursor = res.next
    }
    phase.value = 'ready'
    await nextTick()
    await renderAll(token)
    if (stale(token)) return
    startPolling()
  } catch (e) {
    if (e instanceof Cancelled || stale(token)) return
    const d = describeError(e)
    phase.value = d.phase
    errorText.value = d.text
  }
}

async function loadEarlier() {
  const i = info.value
  if (!i || busy.value || startIdx.value <= 0) return
  const token = gen
  busy.value = true
  await waitPollIdle()
  if (stale(token)) {
    busy.value = false
    return
  }
  const newStart = extendWindowStart(i.segments, startIdx.value)
  const stopSeq = i.segments[startIdx.value].seq
  try {
    progress.value = '正在读取更早的记录…'
    const res = await readRange(i.segments[newStart].seq, stopSeq, token)
    if (stale(token)) return
    const tl = new RecordTimeline()
    tl.push(res.events)
    allItems = tl.items.concat(allItems)
    loadedBytes.value += i.segments.slice(newStart, startIdx.value).reduce((n, s) => n + s.raw, 0)
    startIdx.value = newStart
    // 终端状态是整段输出的函数，不能往前拼接：整体重绘。重绘后停在新旧内容的接缝附近，继续往上滚就是刚加载的内容。
    const linesBefore = term?.buffer.active.length ?? 0
    await renderAll(token)
    if (stale(token)) return
    const t = term
    if (t) {
      const added = t.buffer.active.length - linesBefore
      if (added > t.rows && !scrollbackCut.value) t.scrollToLine(Math.max(0, added - Math.floor(t.rows / 3)))
      else t.scrollToTop()
    }
  } catch (e) {
    if (!(e instanceof Cancelled) && !stale(token)) {
      const d = describeError(e)
      if (d.phase === 'locked') toast.error('需要解锁后才能继续读取')
      else toast.error(`读取更早的记录失败：${d.text}`)
    }
  } finally {
    progress.value = ''
    busy.value = false
  }
}

/** 等正在进行的一次跟随读取结束（它写终端的过程不能和整块重绘交错）。 */
async function waitPollIdle(maxMs = 4000) {
  const t0 = Date.now()
  while (polling && Date.now() - t0 < maxMs) await new Promise(r => setTimeout(r, 25))
}

async function setViewMode(m: ViewMode) {
  if (m === viewMode.value) return
  viewMode.value = m
  try {
    localStorage.setItem(MODE_KEY, m)
  } catch {
    /* 禁用了本地存储：只在这一次生效 */
  }
  if (phase.value !== 'ready') return
  const token = gen
  await waitPollIdle()
  if (stale(token)) return
  await renderAll(token)
}

// 往上滚到已载入内容的最顶端后继续往上滚，就直接加载更早的记录（和实时终端里的历史日志手势一致，不需要再确认；
// 读取期间工具栏里的「加载更早」按钮显示进度）。没有更早的记录可加载时这个手势什么也不做。
useTopOverscroll({
  host: hostEl,
  term: () => term,
  enabled: () => phase.value === 'ready' && canLoadEarlier.value && !busy.value,
  onFire: () => void loadEarlier(),
})

// ---------- 跟随最新 ----------
function stopPolling() {
  if (pollTimer) clearTimeout(pollTimer)
  pollTimer = null
}

function startPolling() {
  stopPolling()
  if (disposed || !live.value || !follow.value) return
  pollTimer = setTimeout(() => void pollOnce(), POLL_MS)
}

async function pollOnce() {
  pollTimer = null
  if (polling || disposed) return
  if (!live.value || !follow.value || !props.active || document.hidden || rendering) {
    startPolling()
    return
  }
  const token = gen
  polling = true
  try {
    const page = await fetchPage(cursor.seg, cursor.off, { max: POLL_BYTES, interactive: false })
    if (stale(token) || rendering) return
    cursor = { seg: page.nextSeg, off: page.nextOff }
    if (page.data.length > 0) {
      const merged = carry.length ? concatAll([carry, page.data]) : page.data
      const { events, used } = decodeEvents(merged)
      carry = merged.subarray(used)
      const before = timeline.length
      timeline.push(events)
      const fresh = timeline.items.slice(before)
      for (const it of fresh) allItems.push(it)
      loadedBytes.value += page.data.length
      await writeItems(fresh, 0, token)
      if (stale(token)) return
      refreshCounters()
      noteScrollback()
      if (endInfo.value) await refreshInfoQuiet(token)
      if (loadedBytes.value >= MAX_LOADED) followStopped.value = '已载入的内容达到上限，停止自动跟随；需要更多内容请下载完整记录。'
    }
  } catch (e) {
    if (!(e instanceof Cancelled) && !stale(token)) {
      const d = describeError(e)
      if (d.phase === 'locked' || toApiError(e).code === 'unlock_required') followStopped.value = '已锁定，自动跟随已暂停。解锁后点「刷新」继续。'
      else if (d.phase === 'offline') online.value = false
      else if (d.phase === 'gone') followStopped.value = '主机上的记录已被清理，自动跟随已停止。'
      // 其他暂时性错误（繁忙 / 超时）：下一轮再试。
    }
  } finally {
    polling = false
    if (!disposed && token === gen) startPolling()
  }
}

/** 会话结束后补读一次记录描述（结束原因、退出码、总大小）。失败不打扰。 */
async function refreshInfoQuiet(token: number) {
  try {
    const r = await terminalApi.recordInfo(props.code, props.sid)
    if (stale(token)) return
    index.value = r.index
    if (r.online) info.value = parseRecInfo(r.info) ?? info.value
  } catch {
    /* 描述只是锦上添花 */
  }
}

// ---------- 搜索 ----------
const searchOpen = ref(false)
const searchText = ref('')
const searchStat = ref('')
const searchInput = ref<HTMLInputElement | null>(null)

function decorations() {
  const dark = themeStore.isDark
  return dark
    ? { matchBackground: '#5b4a00', matchBorder: '#a16207', matchOverviewRuler: '#a16207', activeMatchBackground: '#a16207', activeMatchBorder: '#fbbf24', activeMatchColorOverviewRuler: '#fbbf24' }
    : { matchBackground: '#fde68a', matchBorder: '#f59e0b', matchOverviewRuler: '#f59e0b', activeMatchBackground: '#fb923c', activeMatchBorder: '#c2410c', activeMatchColorOverviewRuler: '#c2410c' }
}

function runSearch(dir: 'next' | 'prev', incremental = false) {
  if (!search) return
  if (!searchText.value) {
    search.clearDecorations()
    searchStat.value = ''
    return
  }
  const opts = { caseSensitive: false, incremental, decorations: decorations() }
  const ok = dir === 'next' ? search.findNext(searchText.value, opts) : search.findPrevious(searchText.value, opts)
  if (!ok) searchStat.value = '没有匹配'
}

async function openSearch() {
  searchOpen.value = true
  await nextTick()
  searchInput.value?.focus()
  searchInput.value?.select()
}

function closeSearch() {
  searchOpen.value = false
  searchStat.value = ''
  search?.clearDecorations()
  term?.clearSelection()
}

// 搜索框里的回车 / Esc：输入法组合期间是在确认或取消候选词，不能当成「跳到下一个匹配」或「关闭搜索」。
function onSearchEnter(ev: KeyboardEvent) {
  if (isImeKey(ev) || ev.ctrlKey || ev.altKey || ev.metaKey) return
  ev.preventDefault()
  runSearch(ev.shiftKey ? 'prev' : 'next')
}

function onSearchEsc(ev: KeyboardEvent) {
  if (isImeKey(ev)) return
  ev.preventDefault()
  closeSearch()
}

function onKeydown(ev: KeyboardEvent) {
  if (isImeKey(ev)) return
  // 嵌在实时终端里时，Esc 退回实时（搜索框里的 Esc 先用来关搜索，它会 preventDefault）。
  if (props.embedded && ev.key === 'Escape' && !ev.defaultPrevented) {
    ev.preventDefault()
    emit('close')
    return
  }
  if ((ev.ctrlKey || ev.metaKey) && !ev.altKey && ev.key.toLowerCase() === 'f' && phase.value === 'ready') {
    ev.preventDefault()
    void openSearch()
  }
}

// ---------- 下载 ----------
async function collectAll(signal: AbortSignal): Promise<Item[]> {
  const tl = new RecordTimeline()
  let seg = 0
  let off = 0
  let rest: Uint8Array = new Uint8Array(0)
  let total = 0
  let idle = 0
  for (;;) {
    const page = await fetchPage(seg, off, { download: true, signal })
    const merged = rest.length ? concatAll([rest, page.data]) : page.data
    const { events, used } = decodeEvents(merged)
    rest = merged.subarray(used)
    tl.push(events)
    total += page.data.length
    if (total > MAX_DOWNLOAD) throw new Error('记录超过下载上限')
    downloading.value = `正在下载 ${formatSize(total)}…`
    seg = page.nextSeg
    off = page.nextOff
    if (page.eof) return tl.items
    idle = page.data.length === 0 ? idle + 1 : 0
    if (idle > 3) throw new Error('记录读取没有进展，请稍后重试')
  }
}

function saveBlob(data: BlobPart, name: string, type: string) {
  const url = URL.createObjectURL(new Blob([data], { type }))
  const a = document.createElement('a')
  a.href = url
  a.download = name
  document.body.appendChild(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(url), 10_000)
}

function stamp(ms: number): string {
  const d = new Date(ms > 0 ? ms : Date.now())
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}${p(d.getMonth() + 1)}${p(d.getDate())}-${p(d.getHours())}${p(d.getMinutes())}${p(d.getSeconds())}`
}

async function download(kind: 'raw' | 'text' | 'cast') {
  if (downloading.value) return
  downloadAbort = new AbortController()
  downloading.value = '正在准备下载…'
  try {
    const items = await collectAll(downloadAbort.signal)
    const base = `${safeFileName(props.label)}-${stamp(createdMs.value)}`
    if (kind === 'raw') saveBlob(rawBytes(items), `${base}-raw.log`, 'application/octet-stream')
    else if (kind === 'text') saveBlob(toPlainText(items), `${base}.txt`, 'text/plain;charset=utf-8')
    else {
      const size = initialSize(items)
      const cast = toAsciicast(items, {
        title: props.label,
        rows: size?.rows ?? info.value?.rows ?? 24,
        cols: size?.cols ?? info.value?.cols ?? 80,
        startedMs: createdMs.value,
      })
      saveBlob(cast, `${base}.cast`, 'application/x-asciicast')
    }
    toast.success('已开始下载', { description: '内容已在浏览器里整理好，没有经过第三方。' })
  } catch (e) {
    if (e instanceof UnlockCancelled) return
    const err = toApiError(e)
    if ((e as { name?: string })?.name === 'CanceledError' || (err.code === 'network' && downloadAbort?.signal.aborted)) return
    toast.error(`下载失败：${err.message}`)
  } finally {
    downloading.value = ''
    downloadAbort = null
  }
}

// ---------- 其他 ----------
function reload() {
  void load()
}

function jumpToEnd() {
  term?.scrollToBottom()
}

watch(
  () => props.fontSize,
  size => {
    if (!term) return
    term.options.fontSize = size
    void nextTick(scheduleLayout)
  },
)
watch(
  () => themeStore.isDark,
  () => {
    if (term) term.options.theme = themeOptions()
  },
)
watch(
  () => props.active,
  a => {
    if (!a) return
    void nextTick(() => {
      applySize()
      startPolling()
    })
    // 后台期间错过了主机上线：切回来时马上确认一次，不用等下一轮定时探测。
    if (needsProbe.value) void probeOnce()
  },
)
watch(follow, v => {
  if (v) startPolling()
  else stopPolling()
})
// 主机重新上线：之前因离线读不到的记录自动再读一次。
watch(
  () => store.hostByCode(props.code)?.online,
  now => {
    // 只有正在看的标签才立刻重读；后台标签等切回来时由 probeOnce 处理，避免几个标签同时涌向同一台主机。
    if (now && props.active && (phase.value === 'offline' || (phase.value === 'ready' && !online.value))) void load()
  },
)

// 主机离线时的自动恢复：直接问服务端它是否已重新在线，比等主机列表的轮询更快，也不依赖列表恰好看到过那次离线。
// 用静默探测而不是 load()，避免界面每隔几秒闪一次「正在读取」；确认上线后才整体重新加载。
const PROBE_MS = 5000
let probeTimer: ReturnType<typeof setTimeout> | null = null
const needsProbe = computed(() => phase.value === 'offline' || (phase.value === 'ready' && !online.value && !isEnded.value))

function stopProbe() {
  if (probeTimer) clearTimeout(probeTimer)
  probeTimer = null
}

function scheduleProbe() {
  stopProbe()
  if (disposed || !needsProbe.value) return
  probeTimer = setTimeout(() => void probeOnce(), PROBE_MS)
}

async function probeOnce() {
  probeTimer = null
  if (disposed || !needsProbe.value) return
  if (!props.active || document.hidden || !store.isUnlocked) {
    scheduleProbe()
    return
  }
  const token = gen
  try {
    const r = await terminalApi.recordInfo(props.code, props.sid)
    if (stale(token) || !needsProbe.value) return
    if (r.online) {
      void load()
      return
    }
  } catch {
    /* 暂时性错误：下一轮再试 */
  }
  scheduleProbe()
}

watch(needsProbe, v => {
  if (v) scheduleProbe()
  else stopProbe()
})

function onVisible() {
  if (!document.hidden) startPolling()
}

onMounted(() => {
  document.addEventListener('visibilitychange', onVisible)
  if (props.embedded) void nextTick(() => rootEl.value?.focus())
  if (scrollEl.value) {
    resizeObserver = new ResizeObserver(scheduleLayout)
    resizeObserver.observe(scrollEl.value)
  }
  void load()
})

onBeforeUnmount(() => {
  disposed = true
  gen++
  document.removeEventListener('visibilitychange', onVisible)
  stopPolling()
  stopProbe()
  downloadAbort?.abort()
  if (layoutFrame) cancelAnimationFrame(layoutFrame)
  resizeObserver?.disconnect()
  search = null
  term?.dispose()
  term = null
  allItems = []
})
</script>

<template>
  <div
    v-show="active"
    ref="rootEl"
    class="h-full w-full flex flex-col min-h-0 bg-background"
    tabindex="-1"
    role="region"
    :aria-label="`终端记录：${label}`"
    @keydown="onKeydown"
  >
    <!-- 信息条 -->
    <div class="shrink-0 border-b border-border/70 bg-card/40 px-3 py-1.5 flex flex-wrap items-center gap-x-2.5 gap-y-1 text-[11px]">
      <Button
        v-if="embedded"
        size="sm"
        class="h-6 px-2 text-[11px] rounded-sm gap-1"
        data-testid="rec-back"
        :title="isEnded ? '关闭历史日志（Esc）' : '回到实时终端（Esc）'"
        @click="emit('close')"
      >
        <ArrowDownToLine class="w-3 h-3" />{{ isEnded ? '返回' : '回到实时' }}
      </Button>
      <span class="inline-flex items-center gap-1 font-medium text-foreground"><History class="w-3.5 h-3.5 text-primary" />{{ embedded ? '历史日志' : '记录回放' }}</span>
      <span v-if="statusChip" class="px-1.5 py-0.5 rounded-sm" :class="statusChip.cls">{{ statusChip.text }}</span>
      <span v-if="metaText" class="text-muted-foreground tabular-nums">{{ metaText }}</span>
      <span class="inline-flex" :title="recHint"><Info class="w-3.5 h-3.5 text-muted-foreground" aria-hidden="true" /><span class="sr-only">{{ recHint }}</span></span>
      <span class="flex-1" />

      <div v-if="phase === 'ready'" class="inline-flex rounded-sm border border-border overflow-hidden" role="radiogroup" aria-label="显示方式" data-testid="rec-mode">
        <button
          type="button"
          role="radio"
          :aria-checked="viewMode === 'flat'"
          class="px-1.5 h-6 text-[11px] cursor-pointer transition-colors"
          :class="viewMode === 'flat' ? 'bg-primary/15 text-foreground font-medium' : 'text-muted-foreground hover:text-foreground hover:bg-muted/60'"
          title="平铺：clear 之类的清屏不再抹掉更早的内容，可以一路向上翻看（程序高频重绘产生的清屏仍按原样处理）；全屏程序（vim / less 等）退出时，它退出前的最后一屏会用分割线夹着保留下来"
          @click="setViewMode('flat')"
        >
          平铺
        </button>
        <button
          type="button"
          role="radio"
          :aria-checked="viewMode === 'faithful'"
          class="px-1.5 h-6 text-[11px] cursor-pointer border-l border-border transition-colors"
          :class="viewMode === 'faithful' ? 'bg-primary/15 text-foreground font-medium' : 'text-muted-foreground hover:text-foreground hover:bg-muted/60'"
          title="还原：完全按终端当时的样子重放，清屏之后更早的内容也一并消失"
          @click="setViewMode('faithful')"
        >
          还原
        </button>
      </div>

      <label v-if="live" class="inline-flex items-center gap-1 text-muted-foreground cursor-pointer select-none" title="会话仍在运行：自动读取新产生的内容">
        <input v-model="follow" type="checkbox" class="accent-primary" />
        <RadioTower class="w-3 h-3" />跟随最新
      </label>
      <Button v-if="canLoadEarlier" variant="outline" size="sm" class="h-6 px-2 text-[11px] rounded-sm gap-1" :disabled="busy" @click="loadEarlier">
        <Loader2 v-if="busy" class="w-3 h-3 animate-spin" />
        <ChevronUp v-else class="w-3 h-3" />
        加载更早
      </Button>
      <template v-if="phase === 'ready'">
        <Button variant="ghost" size="sm" class="h-6 w-6 p-0 rounded-sm" aria-label="搜索（Ctrl+F）" title="搜索（Ctrl+F）" @click="openSearch"><Search class="w-3.5 h-3.5" /></Button>
        <Button variant="ghost" size="sm" class="h-6 w-6 p-0 rounded-sm" aria-label="跳到最新" title="跳到最新" @click="jumpToEnd"><ArrowDownToLine class="w-3.5 h-3.5" /></Button>
        <DropdownMenu>
          <DropdownMenuTrigger as-child>
            <Button variant="ghost" size="sm" class="h-6 px-1.5 gap-1 text-[11px] rounded-sm" :disabled="!online || !!downloading" aria-label="下载完整记录">
              <Loader2 v-if="downloading" class="w-3.5 h-3.5 animate-spin" />
              <Download v-else class="w-3.5 h-3.5" />
              <span class="hidden sm:inline">{{ downloading || '下载' }}</span>
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" class="text-xs min-w-[11rem]">
            <DropdownMenuItem class="text-xs" @select="download('text')">纯文本（.txt）</DropdownMenuItem>
            <DropdownMenuItem class="text-xs" @select="download('raw')">原始输出流（.log）</DropdownMenuItem>
            <DropdownMenuItem class="text-xs" @select="download('cast')">asciicast（.cast）</DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </template>
      <Button variant="ghost" size="sm" class="h-6 w-6 p-0 rounded-sm" aria-label="刷新" title="重新读取" :disabled="phase === 'loading'" @click="reload"><RotateCw class="w-3.5 h-3.5" /></Button>
    </div>

    <!-- 提示条 -->
    <p v-if="showHeadBanner" class="shrink-0 px-3 py-1 text-[11px] bg-amber-500/10 text-amber-700 dark:text-amber-300 border-b border-amber-500/20" role="status">
      从此处开始，更早内容已按配额清理。
    </p>
    <p v-if="phase === 'ready' && gapCount > 0" class="shrink-0 px-3 py-1 text-[11px] bg-amber-500/10 text-amber-700 dark:text-amber-300 border-b border-amber-500/20" role="status">
      这份记录中间有 {{ gapCount }} 处缺口（写盘跟不上、磁盘空间不足等），缺口处已用灰色提示标出；缺口附近的画面可能不完整。
    </p>
    <p v-if="phase === 'ready' && scrollbackCut" class="shrink-0 px-3 py-1 text-[11px] bg-muted/60 text-muted-foreground border-b border-border/60" role="status">
      为控制浏览器内存，这里只显示最近 {{ SCROLLBACK }} 行；更早的内容请用「下载」查看完整记录。
    </p>
    <p v-else-if="phase === 'ready' && startIdx > 0 && loadedBytes >= MAX_LOADED" class="shrink-0 px-3 py-1 text-[11px] bg-muted/60 text-muted-foreground border-b border-border/60" role="status">
      已载入的内容达到上限；更早的记录请用「下载」查看完整内容。
    </p>
    <p v-if="phase === 'ready' && !online" class="shrink-0 px-3 py-1 text-[11px] bg-amber-500/10 text-amber-700 dark:text-amber-300 border-b border-amber-500/20 flex items-center gap-1.5" role="status">
      <WifiOff class="w-3 h-3 shrink-0" />主机已离线，显示的是离线前已读取的内容；主机恢复后会自动刷新。
    </p>
    <p v-if="followStopped" class="shrink-0 px-3 py-1 text-[11px] bg-muted/60 text-muted-foreground border-b border-border/60" role="status">{{ followStopped }}</p>

    <!-- 搜索 -->
    <div v-if="searchOpen && phase === 'ready'" class="shrink-0 border-b border-border/70 bg-card/60 px-3 py-1.5 flex items-center gap-1.5">
      <Search class="w-3.5 h-3.5 text-muted-foreground shrink-0" />
      <input
        ref="searchInput"
        v-model="searchText"
        type="text"
        class="h-6 flex-1 min-w-0 max-w-xs px-1.5 rounded-sm bg-background border border-border text-xs focus:outline-none focus:border-primary"
        placeholder="在已载入的内容里搜索"
        aria-label="搜索记录"
        @input="runSearch('next', true)"
        @keydown.enter="onSearchEnter($event)"
        @keydown.esc="onSearchEsc($event)"
      />
      <span class="text-[11px] text-muted-foreground tabular-nums min-w-12" aria-live="polite">{{ searchStat }}</span>
      <Button variant="ghost" size="sm" class="h-6 w-6 p-0 rounded-sm" aria-label="上一个" @click="runSearch('prev')"><ChevronUp class="w-3.5 h-3.5" /></Button>
      <Button variant="ghost" size="sm" class="h-6 w-6 p-0 rounded-sm" aria-label="下一个" @click="runSearch('next')"><ChevronDown class="w-3.5 h-3.5" /></Button>
      <Button variant="ghost" size="sm" class="h-6 w-6 p-0 rounded-sm" aria-label="关闭搜索" @click="closeSearch"><X class="w-3.5 h-3.5" /></Button>
    </div>

    <!-- 内容 -->
    <div class="flex-1 min-h-0 relative">
      <div v-show="phase === 'ready'" ref="scrollEl" class="record-scroll absolute inset-0 overflow-auto px-2 py-1.5">
        <div ref="hostEl" class="inline-block align-top" role="group" :aria-label="`终端内容：${label}`" />
        <p v-if="phase === 'ready' && itemCount === 0 && !live && !progress" class="absolute inset-0 flex items-center justify-center text-xs text-muted-foreground pointer-events-none">
          这个会话没有可显示的记录内容。
        </p>
      </div>

      <div v-if="phase === 'loading' || (phase === 'ready' && progress && !busy)" class="absolute inset-0 flex items-center justify-center gap-2 text-xs text-muted-foreground bg-background/70" aria-busy="true">
        <Loader2 class="w-3.5 h-3.5 animate-spin" />{{ progress || '正在读取记录…' }}
      </div>

      <div v-else-if="phase !== 'ready'" class="absolute inset-0 overflow-y-auto">
        <div class="max-w-md px-6 py-10 space-y-3">
          <template v-if="phase === 'locked'">
            <h2 class="text-sm font-bold text-foreground flex items-center gap-1.5"><Lock class="w-4 h-4 text-amber-500" />需要解锁</h2>
            <p class="text-xs leading-relaxed text-muted-foreground">终端记录属于敏感内容，查看前需要再次验证 Web 访问密码。</p>
            <Button size="sm" class="h-7 rounded-sm text-xs" @click="reload">解锁并查看</Button>
          </template>
          <template v-else-if="phase === 'offline'">
            <h2 class="text-sm font-bold text-foreground flex items-center gap-1.5"><WifiOff class="w-4 h-4 text-amber-500" />主机当前离线</h2>
            <p class="text-xs leading-relaxed text-muted-foreground">
              记录内容保存在主机上，主机离线时无法读取。主机重新上线后会自动加载。
              <template v-if="index">这个会话的基本信息仍保留在服务端：{{ index.name || `会话 ${sid.slice(0, 6)}` }}<template v-if="index.ended_reason">，{{ endReasonLabel(index.ended_reason) }}</template>。</template>
            </p>
            <Button variant="outline" size="sm" class="h-7 rounded-sm text-xs gap-1" @click="reload"><RotateCw class="w-3 h-3" />重试</Button>
          </template>
          <template v-else-if="phase === 'gone'">
            <h2 class="text-sm font-bold text-foreground flex items-center gap-1.5"><Info class="w-4 h-4 text-muted-foreground" />记录已不存在</h2>
            <p class="text-xs leading-relaxed text-muted-foreground">{{ errorText || '主机上已没有这个会话的记录（可能已按保留策略清理）。' }}</p>
            <Button variant="ghost" size="sm" class="h-7 rounded-sm text-xs" @click="emit('close')">关闭</Button>
          </template>
          <template v-else>
            <h2 class="text-sm font-bold text-destructive flex items-center gap-1.5"><CircleAlert class="w-4 h-4" />读取失败</h2>
            <p class="text-xs leading-relaxed text-muted-foreground">{{ errorText }}</p>
            <div class="flex gap-2">
              <Button variant="outline" size="sm" class="h-7 rounded-sm text-xs gap-1" @click="reload"><RotateCw class="w-3 h-3" />重试</Button>
              <Button variant="ghost" size="sm" class="h-7 rounded-sm text-xs" @click="emit('close')">关闭</Button>
            </div>
          </template>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.record-scroll {
  scrollbar-width: thin;
  scrollbar-color: var(--border) transparent;
}
.record-scroll :deep(.xterm-viewport) {
  scrollbar-width: thin;
  scrollbar-color: var(--border) transparent;
}
</style>
