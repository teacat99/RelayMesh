<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import type { Terminal } from '@xterm/xterm'
import type { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { Check, CircleAlert, ClipboardPaste, Copy, History, Loader2, RotateCw, WifiOff } from 'lucide-vue-next'
import { toast } from 'vue-sonner'
import Button from '../ui/button/Button.vue'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from '../ui/dropdown-menu'
import ConfirmDialog from './ConfirmDialog.vue'
import RecordViewer from './RecordViewer.vue'
import { TerminalConnection, type ConnectionState, type Env, type SocketLike, type StateInfo } from '../../lib/terminal/connection'
import { TERM_FONT_FAMILY, terminalTheme } from '../../lib/terminal/theme'
import { applyModifiers, cursorKey, type CursorKey } from '../../lib/terminal/keys'
import { ReplayMute } from '../../lib/terminal/replayMute'
import { DiagLog } from '../../lib/terminal/diag'
import { SETTLE, settleVerdict } from '../../lib/terminal/policy'
import type { RecordSource } from '../../lib/terminal/historyLoader'
import { tailLinesFor } from '../../lib/terminal/historyBudget'
import { describeFixedWidth, solveFixedFont, type FixedWidthState } from '../../lib/terminal/fixedWidth'
import { copyText, readClipboardText } from '../../lib/terminal/clipboard'
import { needsPasteConfirm, pasteDescription, pasteTitle, summarizePaste } from '../../lib/terminal/paste'
import { setPasteConfirm, useTerminalPasteConfirm } from '../../composables/useTerminalPasteConfirm'
import { terminalApi } from '../../api/terminal'
import { useInlineHistory } from '../../composables/useInlineHistory'
import { useTopOverscroll } from '../../composables/useTopOverscroll'
import { useTerminalStore } from '../../stores/terminal'
import { useThemeStore } from '../../stores/theme'

// 一个正在附着的终端会话：xterm 负责渲染与键盘，TerminalConnection 负责票据 / WebSocket / 断线续传。
const props = defineProps<{
  code: string
  sid: string
  label: string
  active: boolean
  fontSize: number
  /** 变为可见时是否抢走键盘焦点；并排时只有被选中的那个窗格需要。 */
  autofocus?: boolean
}>()

const emit = defineEmits<{
  (e: 'state', payload: { state: ConnectionState; info: StateInfo }): void
  (e: 'close'): void
  /** 隐藏（后台）状态下收到了新输出，用于在标签与侧栏上提示。 */
  (e: 'activity'): void
  /** 收到了会话输出（含回放）。只告诉「来了一段」，不带内容；启动器用它判断 shell 的提示符是否已经出来。 */
  (e: 'output'): void
}>()

// 手机辅助条的粘滞修饰键（由视图持有，切换标签时不丢失）。
const ctrl = defineModel<boolean>('ctrl', { default: false })
const alt = defineModel<boolean>('alt', { default: false })

const store = useTerminalStore()
const themeStore = useThemeStore()

const hostEl = ref<HTMLDivElement | null>(null)
const connState = ref<ConnectionState>('idle')
const connInfo = ref<StateInfo>({ attempt: 0, message: '', over: false, exitCode: null, signal: '', nextRetryAt: null })
const nowMs = ref(Date.now())

let term: Terminal | null = null
let fit: FitAddon | null = null
let conn: TerminalConnection | null = null
let resizeObserver: ResizeObserver | null = null
let fitFrame = 0
let resizeTimer: ReturnType<typeof setTimeout> | null = null
let countdownTimer: ReturnType<typeof setInterval> | null = null
let started = false
let disposed = false
// 解析回放数据期间，终端自己生成的查询应答（DA / CPR / 颜色 / 模式查询）不能写回会话输入，见 replayMute.ts。
const replayMute = new ReplayMute()

// 连接诊断事件环（#101）：只记连接 / 尺寸 / 追赶这些事件，不含终端内容；右键菜单里「复制连接诊断」复制成文字。
const diag = new DiagLog()

// 「打开即显示最新内容」：全新附着的回放（主机只给末尾约 20 屏）在终端里解析完之前盖一层遮罩，用户看不到回放在屏幕上滚动；
// 回放渲染完 → 落到底部再揭开。很快的回放（150 毫秒内）不显示遮罩，免得一闪而过。
// 服务端告知「跟不上输出」（lag）时同样盖遮罩：持续下去服务端会断开并要求重新附着，跳到最新输出，期间不让用户看着几十 MiB 的重画一路滚过去。
const SEED_SHOW_DELAY_MS = 150
const SEED_GUARD_MS = 15_000
const seeding = ref(false)
const seedShow = ref(false)
const seedTotal = ref(0)
const seedGot = ref(0)
const lagging = ref(false)
// 回放渲染完之后实时输出还在高速涌来（附着时的尺寸调整让程序整段重画）：遮罩继续盖着，等输出平静再揭开（见 policy.ts 的 SETTLE）。
const settling = ref(false)
// 「追赶」遮罩之后紧跟着的全新附着：遮罩不撤，直接接上载入遮罩（不经过 150 毫秒的延迟，否则会先露出一个空屏）。
let lagSeed = false
let seedShowTimer: ReturnType<typeof setTimeout> | null = null
let seedGuardTimer: ReturnType<typeof setTimeout> | null = null
let settleTimer: ReturnType<typeof setInterval> | null = null
// off：不在观察；watch：回放渲染完时还算安静，观察一小段时间，忙起来就盖遮罩；hold：遮罩盖着，等输出平静
let settleMode: 'off' | 'watch' | 'hold' = 'off'
let settleStartAt = 0
let settleHoldAt = 0
let settleLastBytes = 0
// 自本次回放开始以来收到的实时输出字节数（回放之外的输出）；seedStart 清零，settle 每个周期看增量
let liveBytes = 0

function clearSeedTimers() {
  if (seedShowTimer) clearTimeout(seedShowTimer)
  if (seedGuardTimer) clearTimeout(seedGuardTimer)
  seedShowTimer = seedGuardTimer = null
  settleEnd('clear')
}

function settleBegin(mode: 'watch' | 'hold') {
  settleMode = mode
  settling.value = mode === 'hold'
  settleStartAt = settleHoldAt = Date.now()
  settleLastBytes = liveBytes
  settleTimer = setInterval(() => {
    if (settleMode === 'off') return
    const now = Date.now()
    const delta = liveBytes - settleLastBytes
    settleLastBytes = liveBytes
    if (settleMode === 'watch') {
      if (delta >= SETTLE.busyBytes) {
        settleMode = 'hold'
        settling.value = true
        settleHoldAt = now
        diag.add('settle-start', { live: liveBytes, late: true })
      } else if (now - settleStartAt >= SETTLE.watchMs) {
        settleEnd('quiet')
      }
      return
    }
    const verdict = settleVerdict(delta, now - settleHoldAt)
    if (verdict !== 'busy') settleEnd(verdict)
  }, SETTLE.pollMs)
  if (mode === 'hold') diag.add('settle-start', { live: liveBytes })
}

function settleEnd(why: string) {
  if (settleTimer) clearInterval(settleTimer)
  settleTimer = null
  const held = settleMode === 'hold'
  settleMode = 'off'
  if (!held) return
  settling.value = false
  diag.add('settle-end', { why, ms: Date.now() - settleHoldAt })
  void nextTick(() => term?.scrollToBottom())
}

function seedStart(total: number) {
  clearSeedTimers()
  liveBytes = 0
  seeding.value = true
  seedShow.value = false
  seedTotal.value = total
  seedGot.value = 0
  if (lagSeed) {
    seedShow.value = true
  } else {
    seedShowTimer = setTimeout(() => {
      seedShowTimer = null
      if (seeding.value) seedShow.value = true
    }, SEED_SHOW_DELAY_MS)
  }
  // 保险丝：回放迟迟没有渲染完（例如渲染被浏览器节流）也不能一直盖着
  seedGuardTimer = setTimeout(() => seedEnd('guard'), SEED_GUARD_MS)
  diag.add('seed-start', { total })
}

/** keepHold：连接进入「等待重连」时不要丢掉「追赶之后接载入」的标记（重连成功的回放开始时还要用）。 */
function seedEnd(why: string, keepHold = false) {
  const afterLag = lagSeed
  if (!keepHold) lagSeed = false
  // 连接掉了 / 守护超时等「不是回放正常渲染完」的收尾：正在等输出平静的遮罩也一并撤掉
  if (why !== 'done') settleEnd(why)
  if (!seeding.value && !seedShow.value) return
  clearSeedTimers()
  const shown = seedShow.value
  seeding.value = false
  seedShow.value = false
  diag.add('seed-end', { why, shown, got: seedGot.value })
  // 回放渲染完时：实时输出已经很忙、或者这次附着本来就是「跟不上输出」之后的重新附着，遮罩继续盖着等输出平静；
  // 否则放手让用户看，再观察一小段——重画往往在回放之后才开始涌来，一旦忙起来就把遮罩盖上。安静的会话不会多盖，打开不会因此变慢。
  if (why === 'done') {
    const busy = liveBytes >= SETTLE.busyBytes || afterLag
    settleBegin(busy ? 'hold' : 'watch')
    if (busy) return
  }
  // 回放的末尾就是最新屏幕：落到底部，再让遮罩下一帧揭开
  void nextTick(() => term?.scrollToBottom())
}

const mask = computed<null | { kind: 'seed' | 'lag'; text: string; progress: number | null }>(() => {
  if (!props.active) return null
  const s = connState.value
  if (s === 'ended' || s === 'failed' || s === 'closed' || s === 'idle') return null
  if ((seedShow.value || settling.value) && s === 'live') {
    const p = settling.value ? null : seedTotal.value > 0 ? Math.min(1, seedGot.value / seedTotal.value) : null
    return { kind: 'seed', text: '正在载入最新内容…', progress: p }
  }
  if (lagging.value) return { kind: 'lag', text: '输出太快，正在跳到最新输出…', progress: null }
  return null
})

const isMac = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)

const banner = computed<null | { kind: 'busy' | 'wait' | 'fail' | 'ended'; text: string }>(() => {
  const s = connState.value
  const info = connInfo.value
  if (s === 'live' && !info.over) return null
  if (s === 'idle' || s === 'closed') return null
  if (s === 'connecting') return { kind: 'busy', text: info.message || '正在连接…' }
  if (s === 'waiting') {
    const left = info.nextRetryAt ? Math.max(0, Math.ceil((info.nextRetryAt - nowMs.value) / 1000)) : 0
    return { kind: 'wait', text: `${info.message}${left > 0 ? ` · ${left} 秒后重试` : ''}` }
  }
  if (s === 'failed') return { kind: 'fail', text: info.message }
  const exit =
    info.exitCode !== null ? `退出码 ${info.exitCode}` : info.signal ? `信号 ${info.signal}` : ''
  return { kind: 'ended', text: `${info.message || '会话已结束'}${exit ? ` · ${exit}` : ''}` }
})

/**
 * 会话结束后，如果它的内容有记录，横幅上提供「查看记录」。无痕 / 未录制的会话没有。
 * 会话还在主机清单里就看清单；已被清出清单（例如被清理 / 终止）则看历史索引。
 */
const canViewRecord = computed(() => {
  const live = store.sessionOf(props.code, props.sid)
  const rec = live ? live.rec : store.history[props.code]?.sessions.find(x => x.sid === props.sid && !x.rec_purged)?.rec
  return rec === 'on' || rec === 'paused' || rec === 'error'
})

// 会话已结束、又不在主机清单里（页面刷新后恢复出来的标签常见）：历史索引可能还没加载，补一次。
watch(
  () => banner.value?.kind === 'ended',
  ended => {
    if (ended && !store.sessionOf(props.code, props.sid)) void store.loadHistory(props.code)
  },
  { immediate: true },
)

// ---------- 历史日志 ----------
// clear、一些智能体界面会把终端里的内容抹掉，页面刚打开时也只回放得到主机缓冲区里最近的一段；这些更早的输出在主机上的记录里还在。
// 往上滚到顶后继续往上滚（滚轮、触屏下拉、Shift + PageUp 都行），不需要再确认：直接把记录里更早的内容（平铺显示）
// 接在实时内容的上方，中间用一条文字分割线隔开（分割线标出「上面是历史、下面是实时」，不进入主机上的记录），
// 连续地往上滚着看（做法见 useInlineHistory）。实时终端本身照常运行，尺寸不变。读取期间、没有更早的内容（已是记录的开头 /
// 没有保存记录）时，终端顶端会出现一条带当前状态（运行中 / 已结束……）的提示。
// 记录里找不到实时内容的开头、没法无缝接续时，退回「完整记录」图层；右上角的按钮也是打开这个图层的入口
// （不用滚动，全屏程序里滚轮归程序用时也靠它）。
const historyOpen = ref(false)
const sessionEnded = computed(() => banner.value?.kind === 'ended')

/** 分割线与提示里的「当前状态」。 */
const statusText = computed(() => {
  if (sessionEnded.value) return '已结束'
  switch (connState.value) {
    case 'live':
      return '运行中'
    case 'waiting':
      return '重连中'
    case 'failed':
      return '连接失败'
    default:
      return '连接中'
  }
})

const RECORD_PAGE_BYTES = 1024 * 1024
const RECORD_UNLOCK_REASON = '查看终端记录需要再次验证密码'

function recordSource(): RecordSource {
  return {
    info: () => store.withUnlock(() => terminalApi.recordInfo(props.code, props.sid), RECORD_UNLOCK_REASON),
    page: (seg, off) =>
      store.withUnlock(() => terminalApi.recordPage(props.code, props.sid, { seg, off, max: RECORD_PAGE_BYTES }), RECORD_UNLOCK_REASON),
  }
}

const inline = useInlineHistory({
  term: () => term,
  mute: replayMute,
  source: recordSource,
  canView: () => canViewRecord.value,
  status: () => statusText.value,
  onFallback: why => {
    toast.info(`${why}，已打开完整记录`)
    openHistory()
  },
  // 用完即还：停回实时底部一阵子、或面板被切走 / 页面被隐藏很久，就卸掉接续进来的历史（回滚缓冲区回到基础大小）。
  view: () => {
    const b = term?.buffer.active
    return { atBottom: !!b && b.viewportY >= b.baseY, away: document.hidden || !props.active }
  },
})
// 历史接续 / 卸载进诊断环：用户怀疑「历史没有被卸载」时，一眼能看出它什么时候接上、什么时候卸掉
watch(inline.state, st => diag.add('history', { state: st }))

function openHistory() {
  if (!canViewRecord.value) return
  historyOpen.value = true
  term?.blur()
}

function closeHistory() {
  historyOpen.value = false
  void nextTick(() => {
    if (props.active && props.autofocus !== false) term?.focus()
  })
}

/** 到顶后继续向上滚动：首次是接续历史，接上之后是再往前加载一批更早的。返回 false = 没有更多可加载的。 */
function loadMoreHistory(): boolean {
  if (inline.state.value === 'none') {
    if (!canViewRecord.value) return false
    void inline.run('first')
    return true
  }
  if (inline.state.value === 'extended' && inline.earlier.value) {
    void inline.run('earlier')
    return true
  }
  return false // 没有更多可加载的：陈述事实的状态提示显示一会儿
}

const { hint: topHint } = useTopOverscroll({
  host: hostEl,
  term: () => term,
  enabled: () =>
    props.active &&
    !historyOpen.value &&
    inline.state.value !== 'loading' &&
    !!term &&
    term.buffer.active.type === 'normal' &&
    term.modes.mouseTrackingMode === 'none',
  onFire: loadMoreHistory,
})

/** 终端顶端的那条状态提示：读取进度 / 短暂的失败或「没有更早的内容」提示 / 到顶后没有更多内容时的状态。 */
const historyBar = computed<null | { tone: 'hint' | 'busy' | 'warn'; text: string }>(() => {
  if (inline.state.value === 'loading') return { tone: 'busy', text: inline.progress.value || '正在读取历史日志…' }
  if (inline.notice.value) return { tone: inline.noticeTone.value, text: inline.notice.value }
  if (!topHint.value || historyOpen.value) return null
  const st = statusText.value
  if (inline.state.value === 'extended') {
    const why = inline.headCut.value ? '更早的记录已按配额清理' : '已是这个会话记录的开头'
    return { tone: 'hint', text: `已到顶 · ${st} · ${why}` }
  }
  if (!canViewRecord.value) return { tone: 'hint', text: `已到顶 · ${st} · 这个会话没有保存记录，更早的输出已经看不到了` }
  return { tone: 'hint', text: `已到顶 · ${st}` }
})

function onConnChange(state: ConnectionState, info: StateInfo) {
  connState.value = state
  // 连接掉了：回放不会再有「完成」，遮罩必须撤掉；彻底停下（失败 / 结束 / 关闭）时「正在追赶」也没有意义了
  if (state === 'waiting' || state === 'failed' || state === 'ended' || state === 'closed') seedEnd('state:' + state, state === 'waiting')
  if (state === 'failed' || state === 'ended' || state === 'closed') lagging.value = false
  connInfo.value = info
  nowMs.value = Date.now()
  emit('state', { state, info })
  if (state === 'waiting') {
    if (!countdownTimer) countdownTimer = setInterval(() => (nowMs.value = Date.now()), 500)
  } else if (countdownTimer) {
    clearInterval(countdownTimer)
    countdownTimer = null
  }
}

function wsURL(): string {
  return `${location.protocol === 'https:' ? 'wss:' : 'ws:'}//${location.host}/api/v1/terminal/ws`
}

function buildEnv(): Env {
  return {
    wsUrl: wsURL(),
    openSocket: (url, protocols) => new WebSocket(url, protocols) as unknown as SocketLike,
    getTicket: req => store.fetchTicket(props.code, props.sid, req),
    setTimer: (fn, ms) => window.setTimeout(fn, ms),
    clearTimer: h => window.clearTimeout(h as number),
  }
}

// ---------- 布局 ----------
// 「宽度保持」（设置 → 终端偏好，服务端保存、所有设备共用）：开启后终端的列数固定为设定值，不随窗口变化；
// 窗口放不下就缩小字号（宽窗口保持用户设置的字号）。关闭时照旧：列数跟随窗口。
const fixedCols = computed(() => store.fixedCols)
const fixedState = ref<FixedWidthState | null>(null)
const fixedHint = computed(() => (fixedState.value ? describeFixedWidth(fixedState.value) : null))
// 上一次求解时的条件；窗口宽度、字号、列数与像素比都没变时只调整行数，不重新求字号（每次探测都会让 xterm 重新量一次单元格）。
let fixedKey = ''

function fitNow() {
  const el = hostEl.value
  if (!term || !fit || !el || el.clientWidth < 40 || el.clientHeight < 20) return
  try {
    if (fixedCols.value > 0) {
      fitFixed(fixedCols.value, el)
    } else {
      releaseFixed()
      fit.fit()
    }
  } catch {
    /* 容器尚未布局完成 */
  }
}

/** 固定列数：先求出放得下这么多列的最大字号，再把终端设成「固定列数 × 当前窗口能放下的行数」。 */
function fitFixed(cols: number, el: HTMLElement) {
  const t = term
  const f = fit
  if (!t || !f) return
  const key = `${cols}|${props.fontSize}|${el.clientWidth}|${window.devicePixelRatio}`
  if (key !== fixedKey) {
    // 改字号后 xterm 会立刻重新量单元格，proposeDimensions 读到的就是新字号下能放下几列（实测同步生效）。
    const r = solveFixedFont(props.fontSize, font => {
      t.options.fontSize = font
      const d = f.proposeDimensions()
      return !!d && d.cols >= cols
    })
    t.options.fontSize = r.font
    fixedKey = key
    fixedState.value = { cols, font: r.font, base: props.fontSize, fits: r.fits }
  }
  const d = f.proposeDimensions()
  if (!d) return
  const rows = Math.max(1, d.rows)
  if (t.cols !== cols || t.rows !== rows) t.resize(cols, rows)
}

/** 退出固定列数：字号回到用户设置的值，列数交还给 fit.fit()。 */
function releaseFixed() {
  if (fixedKey === '' && !fixedState.value) return
  fixedKey = ''
  fixedState.value = null
  if (term && term.options.fontSize !== props.fontSize) term.options.fontSize = props.fontSize
}

function scheduleFit() {
  if (fitFrame) return
  fitFrame = requestAnimationFrame(() => {
    fitFrame = 0
    if (props.active) fitNow()
  })
}

/**
 * 把终端尺寸同步给会话里进程的收尾防抖（毫秒）。每次同步都是一次 SIGWINCH：cursor-cli 这类程序会在尺寸变化后
 * 把整段对话重画一遍，单次几 MiB 到十几 MiB，在小带宽链路上要十几秒。窗口拖拽、侧栏收起展开、软键盘弹出这类
 * 连续的布局变化只该同步最后稳定下来的那一次；实测页面打开时布局抖动的两次变化相隔 100~210 ms，留 2 倍余量。
 */
const RESIZE_SETTLE_MS = 400

function reportSize(rows: number, cols: number) {
  if (resizeTimer) clearTimeout(resizeTimer)
  resizeTimer = setTimeout(() => {
    resizeTimer = null
    diag.add('size-report', { rows, cols })
    conn?.resize(rows, cols)
  }, RESIZE_SETTLE_MS)
}

// ---------- 启动 ----------
async function boot() {
  const [{ Terminal }, { FitAddon }, { Unicode11Addon }, { WebLinksAddon }, webglMod] = await Promise.all([
    import('@xterm/xterm'),
    import('@xterm/addon-fit'),
    import('@xterm/addon-unicode11'),
    import('@xterm/addon-web-links'),
    // WebGL 渲染器量出的单元格宽度和 DOM 渲染器不一样（整数设备像素 vs 小数），换渲染器会让同一个窗口多出几列。
    // 如果等页面打开后再异步装它，终端会先后报出两个不同的尺寸，会话里的程序被通知两次、整段重画两次
    // （cursor-cli 一次几 MiB 到十几 MiB）。所以它的代码块与其余几个并行下载，并在第一次适配尺寸「之前」装好。
    import('@xterm/addon-webgl').catch(() => null),
  ])
  if (disposed || !hostEl.value) return

  term = new Terminal({
    fontFamily: TERM_FONT_FAMILY,
    fontSize: props.fontSize,
    lineHeight: 1.15,
    cursorBlink: true,
    scrollback: 5000,
    allowProposedApi: true,
    minimumContrastRatio: 4.5,
    macOptionIsMeta: true,
    theme: terminalTheme(themeStore.isDark),
  })
  fit = new FitAddon()
  term.loadAddon(fit)
  term.loadAddon(new Unicode11Addon())
  term.unicode.activeVersion = '11'
  term.loadAddon(
    new WebLinksAddon((_event, uri) => {
      try {
        const u = new URL(uri)
        if (u.protocol === 'http:' || u.protocol === 'https:') window.open(u.href, '_blank', 'noopener,noreferrer')
      } catch {
        /* 非法链接：忽略 */
      }
    }),
  )
  term.open(hostEl.value)
  inline.attach(term)
  // 仅供端到端测试读取屏幕内容（不可枚举，不参与任何业务逻辑）。
  Object.defineProperty(hostEl.value, '__xterm', { value: term, configurable: true })
  loadWebgl(webglMod)

  term.onData(d => {
    if (replayMute.active) return
    store.noteActivity()
    let data = d
    if (ctrl.value || alt.value) {
      const r = applyModifiers(d, { ctrl: ctrl.value, alt: alt.value })
      if (r.applied) {
        data = r.data
        ctrl.value = false
        alt.value = false
      }
    }
    conn?.sendText(data)
  })
  term.onBinary(d => {
    if (replayMute.active) return
    store.noteActivity()
    conn?.sendBinary(d)
  })
  term.onResize(({ rows, cols }) => {
    diag.add('term-resize', { rows, cols, font: term?.options.fontSize ?? 0, dpr: Math.round(window.devicePixelRatio * 100) / 100 })
    reportSize(rows, cols)
  })
  term.attachCustomKeyEventHandler(ev => {
    if (ev.type !== 'keydown') return true
    const mod = ev.ctrlKey && !ev.altKey && !ev.metaKey
    if (mod && ev.shiftKey && ev.code === 'KeyC') {
      void copySelection()
      return false
    }
    // 非 macOS：有选区时 Ctrl+C 复制（没有选区时照常发送 ^C 中断）
    if (!isMac && mod && !ev.shiftKey && ev.code === 'KeyC' && term?.hasSelection()) {
      void copySelection()
      return false
    }
    // Ctrl+Shift+V 交给浏览器的 paste 事件，由 xterm 按括号粘贴模式处理
    if (mod && ev.shiftKey && ev.code === 'KeyV') return false
    return true
  })

  resizeObserver = new ResizeObserver(scheduleFit)
  resizeObserver.observe(hostEl.value)
  maybeStart()
}

/**
 * WebGL 渲染器在大输出（cat 大文件、日志刷屏）时快得多；不可用时自动退回 DOM 渲染器。
 * 同步安装（代码块已在 boot 里随其他模块一起下载好），保证第一次适配尺寸时渲染器已经是最终的那一个。
 */
function loadWebgl(mod: typeof import('@xterm/addon-webgl') | null) {
  if (!mod || disposed || !term) return
  try {
    const gl = new mod.WebglAddon()
    gl.onContextLoss(() => {
      diag.add('webgl-lost')
      gl.dispose()
    })
    term.loadAddon(gl)
  } catch {
    /* 没有 WebGL 支持：保持 DOM 渲染 */
  }
}

/** 只在标签页第一次变为可见时连接：隐藏的容器量不出尺寸，而票据里需要正确的行列数。 */
function maybeStart() {
  if (started || disposed || !props.active || !term) return
  fitNow()
  started = true
  conn = new TerminalConnection(
    buildEnv(),
    {
      // 写入终端的内容都经过 inline：它同时记下这些字节（用于「接续历史日志」时重建缓冲区），重建期间的新输出也由它排队。
      reset: () => inline.reset(),
      write: (data, done, replay, kind) => {
        if (!replay && !props.active) emit('activity')
        if (replay && seeding.value && kind !== 'prefix') seedGot.value += data.length
        else if (!replay) liveBytes += data.length
        emit('output')
        inline.write(data, done, replay, kind)
      },
      notice: text => inline.localNotice(text),
      size: () => ({ rows: term?.rows ?? 24, cols: term?.cols ?? 80 }),
      replay: ev => {
        if (ev.phase === 'start') {
          if (ev.total > 0) seedStart(ev.total)
        } else {
          seedEnd('done')
        }
      },
      lag: on => {
        lagging.value = on
        diag.add('lag-mask', { on })
        if (on) lagSeed = true
        // 撤销提醒后紧跟着的全新附着会在同一个同步调用里开始回放（seedStart 会接住这个标记）；没有接住就清掉
        else queueMicrotask(() => {
          if (!seeding.value) lagSeed = false
        })
      },
      // 看不到的窗格（后台标签 / 页面被隐藏）被服务端因跟不上输出断开后不自动重连，再被看到时才全新附着
      visible: () => props.active && !document.hidden,
      // 全新打开时只要末尾约 20 屏；更早的内容滚到顶时再从会话记录里按需读（主机只在记录可读时才照办）。
      tailLines: rows => tailLinesFor(rows),
    },
    onConnChange,
    diag,
  )
  conn.resize(term.rows, term.cols)
  conn.start()
  // 第一次可见就是「刚打开」。从主机页的「打开终端」或侧栏进来时，窗格是带着 active=true 直接挂载的，
  // 下面的 watch(active) 不会触发；不在这里聚焦，就得先点一下终端才能输入。
  // 触屏设备不自动聚焦，免得一打开就弹出软键盘、遮住刚出现的输出。
  if (props.autofocus !== false && !coarsePointer()) term.focus()
}

function coarsePointer(): boolean {
  return typeof matchMedia === 'function' && matchMedia('(pointer: coarse)').matches
}

// ---------- 对外能力（供辅助条 / 视图调用） ----------
function focus() {
  term?.focus()
}

function sendText(text: string) {
  store.noteActivity()
  conn?.sendText(text)
}

function sendCursor(key: CursorKey) {
  sendText(cursorKey(key, term?.modes.applicationCursorKeysMode ?? false))
}

async function copySelection() {
  const text = term?.getSelection() ?? ''
  if (!text) {
    toast.info('没有选中的文字')
    return
  }
  if (await copyText(text)) {
    term?.clearSelection()
    toast.success('已复制')
  } else {
    toast.error('复制失败：浏览器拒绝了剪贴板访问')
  }
}

async function paste() {
  const text = await readClipboardText()
  if (text === null) {
    toast.info('浏览器不允许读取剪贴板：请在地址栏允许剪贴板权限，或直接按 Ctrl+Shift+V（Mac 为 ⌘V）粘贴；手机上可长按输入区域粘贴')
    return
  }
  if (text) requestPaste(text)
  else toast.info('剪贴板里没有文字')
  term?.focus()
}

// ---------- 右键菜单与多行粘贴确认 ----------
// 右键：有选区时可「复制」，「粘贴」始终可用。Shift+右键仍是浏览器自己的菜单；程序开着鼠标报告（vim / tmux 的鼠标模式）时，
// 右键归程序；触屏长按不弹（手机用辅助条里的粘贴按钮）。
// 多行粘贴：括号粘贴模式没开时，每个换行都是一次回车，多行内容会被逐行立刻执行——先让用户看一眼预览再发送。
// 菜单「粘贴」、辅助条的粘贴按钮、浏览器自己的粘贴（Ctrl+Shift+V / ⌘V / 中键 / 手机长按）都走同一个判断。
// Ctrl+V 不做任何特殊处理：它和终端本身的含义冲突（readline 的 quoted-insert），按原样把 ^V 交给程序。
const ctxMenu = reactive({ open: false, x: 0, y: 0, canCopy: false })
const pasteConfirm = useTerminalPasteConfirm()
const pendingPaste = ref<string | null>(null)
const neverAsk = ref(false)
const pasteSummary = computed(() => (pendingPaste.value === null ? null : summarizePaste(pendingPaste.value)))
const copyKeyHint = isMac ? '⌘C' : 'Ctrl+Shift+C'
const pasteKeyHint = isMac ? '⌘V' : 'Ctrl+Shift+V'

function onContextMenu(ev: MouseEvent) {
  if (!term || ev.shiftKey) return
  if ((ev as PointerEvent).pointerType === 'touch') return
  if (term.modes.mouseTrackingMode !== 'none') return
  ev.preventDefault()
  ctxMenu.x = ev.clientX
  ctxMenu.y = ev.clientY
  ctxMenu.canCopy = term.hasSelection()
  ctxMenu.open = true
}

function requestPaste(text: string) {
  if (!term || !text) return
  if (needsPasteConfirm(text, { bracketed: term.modes.bracketedPasteMode, enabled: pasteConfirm.enabled.value })) {
    neverAsk.value = false
    pendingPaste.value = text
    return
  }
  term.paste(text)
}

function confirmPaste() {
  const text = pendingPaste.value
  pendingPaste.value = null
  if (neverAsk.value) setPasteConfirm(false)
  if (text) term?.paste(text)
  term?.focus()
}

function onPasteDialogOpen(open: boolean) {
  if (open) return
  pendingPaste.value = null
  term?.focus()
}

/** 浏览器自己的粘贴：需要确认的多行内容拦下来走确认框，其余原样交给 xterm（它会按括号粘贴模式处理）。 */
function onNativePaste(ev: ClipboardEvent) {
  if (!term) return
  const text = ev.clipboardData?.getData('text/plain') ?? ''
  if (!text || !needsPasteConfirm(text, { bracketed: term.modes.bracketedPasteMode, enabled: pasteConfirm.enabled.value })) return
  ev.preventDefault()
  ev.stopPropagation()
  requestPaste(text)
}

function retry() {
  conn?.retryNow()
}

/** 复制连接诊断（不含终端内容）：遇到「忽然滚屏 / 重新加载」时贴给开发者。 */
async function copyDiag() {
  const b = term?.buffer.active
  const text = diag.text({
    session: props.sid.slice(0, 8),
    host: props.code.slice(0, 8),
    state: connState.value,
    pane: term ? `${term.cols}x${term.rows}` : '?',
    buffer: b ? `${b.type} len=${b.length} viewportY=${b.viewportY} baseY=${b.baseY}` : '?',
    dpr: Math.round(window.devicePixelRatio * 100) / 100,
    visible: !document.hidden,
  })
  if (await copyText(text)) toast.success('已复制连接诊断（不含终端内容）')
  else toast.error('复制失败：浏览器拒绝了剪贴板访问')
}

defineExpose({ focus, sendText, sendCursor, paste, copySelection, retry, openHistory })

// ---------- 联动 ----------
watch(
  () => props.active,
  active => {
    diag.add('active', { on: active })
    if (!active) return
    void nextTick(() => {
      fitNow()
      maybeStart()
      // 后台时因跟不上输出被断开（暂停）的连接，被看到时立刻全新附着；等待重连中的也不必再等
      conn?.resumeIfPossible()
      if (props.autofocus !== false && !historyOpen.value) term?.focus()
    })
  },
)

watch(
  () => props.fontSize,
  size => {
    if (!term) return
    // 固定列数时字号由 fitFixed 按窗口宽度求解（用户设置的字号只是上限），不在这里直接改。
    if (fixedCols.value === 0) term.options.fontSize = size
    void nextTick(fitNow)
  },
)

// 在设置里打开 / 关闭 / 修改宽度保持（本机或其他设备改的，状态轮询约 15 秒内同步过来）：立刻重新适配。
watch(fixedCols, () => {
  fixedKey = ''
  void nextTick(fitNow)
})

watch(
  () => themeStore.isDark,
  dark => {
    if (term) term.options.theme = terminalTheme(dark)
  },
)

// 标签页重新可见 / 网络恢复：正在等待重连，或因暂时性的网络故障而暂停的连接，立刻再试一次。
function onVisible() {
  if (!document.hidden && props.active) conn?.resumeIfPossible()
}
function onOnline() {
  conn?.resumeIfPossible()
}

onMounted(() => {
  document.addEventListener('visibilitychange', onVisible)
  window.addEventListener('online', onOnline)
  void boot()
})

onBeforeUnmount(() => {
  disposed = true
  document.removeEventListener('visibilitychange', onVisible)
  window.removeEventListener('online', onOnline)
  if (fitFrame) cancelAnimationFrame(fitFrame)
  if (resizeTimer) clearTimeout(resizeTimer)
  if (countdownTimer) clearInterval(countdownTimer)
  clearSeedTimers()
  resizeObserver?.disconnect()
  conn?.stop()
  conn = null
  inline.dispose()
  term?.dispose()
  term = null
})
</script>

<template>
  <div v-show="active" class="relative h-full w-full flex flex-col min-h-0 bg-background">
    <div class="group/term relative flex-1 min-h-0">
      <div
        ref="hostEl"
        role="group"
        :aria-label="`终端：${label}`"
        class="term-host h-full w-full px-2 py-1.5"
        :class="fixedState && !fixedState.fits ? 'overflow-x-auto overflow-y-hidden' : 'overflow-hidden'"
        :data-fixed-cols="fixedState ? fixedState.cols : undefined"
        :data-fixed-font="fixedState ? fixedState.font : undefined"
        :data-fixed-fits="fixedState ? String(fixedState.fits) : undefined"
        @click="focus"
        @contextmenu="onContextMenu"
        @paste.capture="onNativePaste"
      />

      <!-- 载入 / 追赶遮罩：盖住终端本体（终端在下面照常解析输出与接收按键），回放渲染完或追上之后揭开。
           不拦截鼠标；z 轴在状态条与底部横幅之下。 -->
      <div
        v-if="mask"
        class="absolute inset-0 z-[5] flex flex-col items-center justify-center gap-2 bg-background text-xs text-muted-foreground select-none pointer-events-none"
        role="status"
        aria-live="polite"
        data-testid="term-mask"
        :data-kind="mask.kind"
      >
        <Loader2 class="w-4 h-4 animate-spin" />
        <span>{{ mask.text }}</span>
        <div v-if="mask.progress !== null" class="h-1 w-40 rounded-full bg-border overflow-hidden" aria-hidden="true">
          <div class="h-full bg-primary transition-[width] duration-150" :style="{ width: Math.round(mask.progress * 100) + '%' }" />
        </div>
      </div>

      <!-- 到顶后继续向上滚动时的状态条：读取进度、读取失败，以及没有更早的内容时（已是记录的开头 / 没有保存记录）写明当前状态。
           读取本身不需要确认：到顶后的第一次向上滚动就开始。它只是盖在终端上的一行 DOM，不是终端缓冲区的内容，
           滚动一离开顶端或键入就消失（接上历史之后，缓冲区里的那条分割线才是历史与实时的边界）。 -->
      <div
        v-if="historyBar"
        class="absolute inset-x-0 top-0 z-10 flex items-center gap-2 py-1 pl-2 bg-background/85 backdrop-blur-sm text-[11px] leading-4 pointer-events-none"
        :class="[
          historyBar.tone === 'warn' ? 'text-amber-600 dark:text-amber-400' : 'text-muted-foreground',
          // 右上角的「历史日志」按钮悬浮在同一角落，给它让出位置，免得压住提示条上的「加载」。
          canViewRecord && !historyOpen ? 'pr-12' : 'pr-2',
        ]"
        role="status"
        aria-live="polite"
        data-testid="term-history-bar"
        :data-state="inline.state.value"
        :data-tone="historyBar.tone"
      >
        <span class="h-px flex-1 min-w-3 bg-border" aria-hidden="true" />
        <Loader2 v-if="historyBar.tone === 'busy'" class="w-3 h-3 shrink-0 animate-spin" />
        <History v-else class="w-3 h-3 shrink-0 text-primary" />
        <span class="min-w-0 text-center" data-testid="term-history-bar-text">{{ historyBar.text }}</span>
        <span class="h-px flex-1 min-w-3 bg-border" aria-hidden="true" />
      </div>

      <!-- 宽度保持下字号被缩小（或窗口窄到放不下）时的角落提示：说明现在是固定列数、字号是多少；字号没变时不出现。 -->
      <div
        v-if="fixedHint"
        class="absolute bottom-1.5 right-4 z-10 max-w-[70%] truncate px-1.5 py-0.5 rounded-sm border bg-card/80 text-[10px] font-mono select-none pointer-events-auto"
        :class="fixedHint.tone === 'warn' ? 'border-amber-500/40 text-amber-600 dark:text-amber-400' : 'border-border/60 text-muted-foreground'"
        :title="fixedHint.title"
        role="status"
        data-testid="term-fixed-width"
        :data-tone="fixedHint.tone"
      >{{ fixedHint.text }}</div>

      <button
        v-if="canViewRecord && !historyOpen"
        type="button"
        class="absolute top-1.5 right-4 z-10 p-1 rounded-sm border border-border/60 bg-card/80 text-muted-foreground hover:text-foreground focus-visible:text-foreground opacity-0 group-hover/term:opacity-100 focus-visible:opacity-100 [@media(hover:none)]:opacity-70 transition-opacity cursor-pointer"
        aria-label="历史日志"
        title="历史日志：查看已被清屏、滚出缓冲区的更早输出"
        data-testid="term-history-open"
        @click.stop="openHistory"
      >
        <History class="w-3.5 h-3.5" />
      </button>

      <RecordViewer
        v-if="historyOpen"
        class="absolute inset-0 z-20"
        embedded
        :code="code"
        :sid="sid"
        :label="label"
        :active="active"
        :font-size="fontSize"
        @close="closeHistory"
      />
    </div>

    <!-- 连接状态条。连接中 / 重连等待 / 连接失败是盖在终端底部的浮层，**不占布局高度**：
         它一占布局，每次打开页面就会让终端容器先矮 36 px（少 2 行）、连上后再恢复，xterm 重算出两个行数，
         两次发给会话里的进程（SIGWINCH）；cursor-cli 这类在尺寸变化后会把整段对话重画一遍的程序，每次要输出几 MiB 到十几 MiB，
         在小带宽链路上要十几秒（实测：附着时的这一对「少 2 行 / 恢复」之后 60 秒内输出 7~16 MiB）。
         会话已结束时进程已经不在，尺寸无所谓，所以结束条仍然占布局。 -->
    <div
      v-if="banner"
      class="min-h-9 px-3 py-1.5 border-t border-border/70 flex items-center gap-2 text-xs"
      :class="[
        banner.kind === 'ended' ? 'shrink-0 bg-card' : 'absolute inset-x-0 bottom-0 z-10 bg-card/95 backdrop-blur-sm',
        {
          'text-muted-foreground': banner.kind === 'busy' || banner.kind === 'ended',
          'text-amber-600 dark:text-amber-400': banner.kind === 'wait',
          'text-destructive': banner.kind === 'fail',
        },
      ]"
      role="status"
      aria-live="polite"
      data-testid="term-banner"
      :data-kind="banner.kind"
    >
      <Loader2 v-if="banner.kind === 'busy'" class="w-3.5 h-3.5 animate-spin shrink-0" />
      <WifiOff v-else-if="banner.kind === 'wait'" class="w-3.5 h-3.5 shrink-0" />
      <CircleAlert v-else-if="banner.kind === 'fail'" class="w-3.5 h-3.5 shrink-0" />
      <Check v-else class="w-3.5 h-3.5 shrink-0" />
      <span class="min-w-0 flex-1 truncate" :title="banner.text">{{ banner.text }}</span>
      <Button
        v-if="banner.kind === 'wait' || banner.kind === 'fail'"
        variant="outline"
        size="sm"
        class="h-6 px-2 text-[11px] rounded-sm gap-1 shrink-0"
        @click="retry"
      >
        <RotateCw class="w-3 h-3" />
        {{ banner.kind === 'fail' ? '重试' : '立即重试' }}
      </Button>
      <Button
        v-if="banner.kind === 'ended' && canViewRecord"
        variant="outline"
        size="sm"
        class="h-6 px-2 text-[11px] rounded-sm gap-1 shrink-0"
        @click="store.openReplay(code, sid)"
      >
        <History class="w-3 h-3" />
        查看记录
      </Button>
      <Button v-if="banner.kind === 'ended' || banner.kind === 'fail'" variant="ghost" size="sm" class="h-6 px-2 text-[11px] rounded-sm shrink-0" @click="emit('close')">
        关闭
      </Button>
    </div>

    <!-- 右键菜单：用一个跟着鼠标位置的隐形锚点驱动下拉菜单；锚点挂到 body，免得被带 transform 的祖先改变定位基准。 -->
    <Teleport to="body">
      <DropdownMenu v-model:open="ctxMenu.open" :modal="false">
        <DropdownMenuTrigger as-child>
          <span aria-hidden="true" class="fixed block w-px h-px pointer-events-none" :style="{ left: ctxMenu.x + 'px', top: ctxMenu.y + 'px' }" />
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start" :side-offset="2" class="min-w-[11rem]" data-testid="term-context-menu" @close-auto-focus.prevent="focus">
          <DropdownMenuItem :disabled="!ctxMenu.canCopy" data-testid="term-menu-copy" @select="copySelection">
            <Copy />
            <span>复制</span>
            <span class="ml-auto pl-4 text-[10px] font-mono text-muted-foreground">{{ copyKeyHint }}</span>
          </DropdownMenuItem>
          <DropdownMenuItem data-testid="term-menu-diag" @select="copyDiag">
            <History class="w-3.5 h-3.5" />
            复制连接诊断
          </DropdownMenuItem>
          <DropdownMenuItem data-testid="term-menu-paste" @select="paste">
            <ClipboardPaste />
            <span>粘贴</span>
            <span class="ml-auto pl-4 text-[10px] font-mono text-muted-foreground">{{ pasteKeyHint }}</span>
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </Teleport>

    <!-- 多行粘贴确认：只有括号粘贴模式没开、内容含换行（会被逐行立刻执行）时才出现。 -->
    <ConfirmDialog
      :open="pendingPaste !== null"
      :title="pasteSummary ? pasteTitle(pasteSummary) : ''"
      :description="pasteSummary ? pasteDescription(pasteSummary) : ''"
      confirm-text="粘贴"
      initial-focus="confirm"
      @update:open="onPasteDialogOpen"
      @confirm="confirmPaste"
    >
      <div v-if="pasteSummary" class="space-y-2" data-testid="paste-confirm">
        <pre
          class="max-h-40 overflow-auto rounded-sm border border-border/70 bg-muted/40 p-2 text-[11px] leading-snug font-mono whitespace-pre-wrap break-all"
          data-testid="paste-preview"
        >{{ pasteSummary.preview.join('\n') }}</pre>
        <p class="text-[10px] text-muted-foreground" data-testid="paste-stats">
          共 {{ pasteSummary.lines }} 行 · {{ pasteSummary.chars }} 个字符<template v-if="pasteSummary.hiddenLines > 0"> · 还有 {{ pasteSummary.hiddenLines }} 行未显示</template>
        </p>
        <label class="flex items-center gap-1.5 text-[11px] text-muted-foreground cursor-pointer select-none">
          <input v-model="neverAsk" type="checkbox" class="accent-primary" data-testid="paste-never-ask" />
          以后不再询问（可在 设置 → 终端偏好 里恢复）
        </label>
      </div>
    </ConfirmDialog>
  </div>
</template>

<style scoped>
/* 终端滚动条与页面其他区域保持同一种细滚动条风格 */
.term-host :deep(.xterm-viewport) {
  scrollbar-width: thin;
  scrollbar-color: var(--border) transparent;
}
</style>
