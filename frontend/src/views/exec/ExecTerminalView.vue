<script setup lang="ts">
import { computed, onBeforeUnmount, reactive, ref, watch, watchEffect } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useElementSize, useMediaQuery } from '@vueuse/core'
import { toast } from 'vue-sonner'
import { Bot, Columns2, History, Hourglass, Infinity as InfinityIcon, Loader2, Pause, Plus, Radar, RotateCcw, SquareTerminal, TriangleAlert, X } from 'lucide-vue-next'
import Button from '../../components/ui/button/Button.vue'
import ExecPageHeader from '../../components/exec/ExecPageHeader.vue'
import LifecycleMenu from '../../components/exec/LifecycleMenu.vue'
import TerminalPane from '../../components/terminal/TerminalPane.vue'
import RecordViewer from '../../components/terminal/RecordViewer.vue'
import TerminalKeybar from '../../components/terminal/TerminalKeybar.vue'
import { useExecStore } from '../../stores/exec'
import { useExecRunsStore } from '../../stores/execRuns'
import { useTerminalStore } from '../../stores/terminal'
import type { HostView } from '../../api/terminal'
import type { ConnectionState, StateInfo } from '../../lib/terminal/connection'
import type { CursorKey } from '../../lib/terminal/keys'
import { findBySid, sessionHostName } from '../../lib/terminal/execList'
import { holderBadge, hostTitle, sessionTitle, shortCode } from '../../lib/terminal/format'
import { canHumanTerminal, canOpenSshTerminal, isSshHost } from '../../lib/terminal/hosts'
import { canRebuild, endedNote, idleChoiceOf, idleSummary, lifecycleBadge, type IdleChoice } from '../../lib/terminal/lifecycle'
import { recStateInfo } from '../../lib/terminal/record'
import { ratioFromPointer } from '../../lib/terminal/split'
import { accumulateWheel, isZoomResetKey, zoomHint } from '../../lib/terminal/fontSize'
import { useTerminalFont } from '../../composables/useTerminalFont'
import { EXEC_HOME, execSessionPath, execThreadPath } from '../../lib/mode'

// 终端页：右侧显示在侧栏里选中的会话。切换会话在左侧侧栏里完成；已经看过的会话会保持连接
// （见 live 集合），切回来不丢滚动缓冲。离开只是分离，会话继续在主机上运行。
// 页面上没有标签条：它与侧栏重复。内部仍然用「标签」记录哪些会话被打开过（刷新后恢复、并排、live 集合都依赖它）。

const route = useRoute()
const router = useRouter()
const store = useTerminalStore()
const exec = useExecStore()
const execRuns = useExecRunsStore()

// 字号：Ctrl + 滚轮缩放、Ctrl + 0 恢复默认（终端区域内）；与设置里的「终端偏好」共用同一个值。
const termFont = useTerminalFont()
const fontSize = termFont.fontSize

const zoomNote = ref('')
let zoomNoteTimer: ReturnType<typeof setTimeout> | undefined
function flashZoom() {
  zoomNote.value = zoomHint(fontSize.value)
  clearTimeout(zoomNoteTimer)
  zoomNoteTimer = setTimeout(() => (zoomNote.value = ''), 1400)
}

let wheelAcc = 0
let wheelAt = 0
/** 终端区域内按住 Ctrl 滚动滚轮：缩放字号，同时拦下事件，免得浏览器缩放整个页面、终端也跟着滚动。 */
function onZoomWheel(ev: WheelEvent) {
  if (!ev.ctrlKey) return
  ev.preventDefault()
  ev.stopPropagation()
  const now = performance.now()
  if (now - wheelAt > 350) wheelAcc = 0 // 停顿之后重新开始累计，不让上一次的零头影响这一次
  wheelAt = now
  const r = accumulateWheel(wheelAcc, ev.deltaY, ev.deltaMode)
  wheelAcc = r.rest
  if (r.steps === 0) return
  termFont.bump(r.steps)
  flashZoom() // 已经到头时也提示一下当前字号，而不是看起来像没反应
}

/** Ctrl + 0：恢复默认字号（浏览器自己的 Ctrl+0 是重置页面缩放，在终端里让位给终端）。 */
function onZoomKey(ev: KeyboardEvent) {
  if (!isZoomResetKey(ev)) return
  ev.preventDefault()
  ev.stopPropagation()
  termFont.reset()
  flashZoom()
}
onBeforeUnmount(() => clearTimeout(zoomNoteTimer))

const isTouch = useMediaQuery('(pointer: coarse)')
const isNarrow = useMediaQuery('(max-width: 767px)')
const showKeybar = computed(() => isTouch.value || isNarrow.value)

// 手机辅助条的粘滞 Ctrl / Alt
const ctrl = ref(false)
const alt = ref(false)

type PaneApi = {
  focus: () => void
  sendText: (t: string) => void
  sendCursor: (k: CursorKey) => void
  paste: () => Promise<void>
}
const panes = new Map<string, PaneApi>()
function setPane(key: string, el: unknown) {
  if (el) panes.set(key, el as PaneApi)
  else panes.delete(key)
}
const activePane = () => panes.get(store.activeKey)

const paneState = reactive<Record<string, ConnectionState>>({})
function onPaneState(key: string, p: { state: ConnectionState; info: StateInfo }) {
  paneState[key] = p.state === 'live' && p.info.over ? 'ended' : p.state
}

interface TabView {
  key: string
  code: string
  sid: string
  hostName: string
  sessionName: string
  label: string
  /** 只读的记录回放标签（不连接会话）。 */
  replay: boolean
}

const tabViews = computed<TabView[]>(() =>
  store.tabs.map(t => {
    const h = store.hostByCode(t.code)
    const s = store.sessionOf(t.code, t.sid)
    // 回放的会话多半已经结束，不在主机的会话列表里：名字取自历史索引。
    const past = store.history[t.code]?.sessions.find(x => x.sid === t.sid)
    // 经网关的 SSH 会话运行在网关上，但标签上要让人一眼看出它连的是哪台机器。
    const hostName = h ? sessionHostName(h, s?.target, s?.shell ?? past?.shell, store.hostByCode) : shortCode(t.code)
    const sessionName = s ? sessionTitle(s) : past ? sessionTitle(past) : `会话 ${t.sid.slice(0, 6)}`
    return { key: t.key, code: t.code, sid: t.sid, hostName, sessionName, label: `${hostName} / ${sessionName}`, replay: !!t.replay }
  }),
)
const liveTabs = computed(() => tabViews.value.filter(t => !t.replay))
// 只为 live 集合里的会话挂载终端面板；其余标签先断开（会话仍在主机上运行），再点开时增量回放。
const livePanes = computed(() => liveTabs.value.filter(t => exec.liveOrder.includes(t.key)))
const replayTabs = computed(() => tabViews.value.filter(t => t.replay))
const active = computed(() => tabViews.value.find(t => t.key === store.activeKey))
const activeIsLive = computed(() => !!active.value && !active.value.replay)

// ---------- 并排 ----------
const canSplit = computed(() => !isNarrow.value)
/** 此刻是否真的在并排显示（窄屏不并排；主窗格是回放标签时也不并排）。 */
const splitShown = computed(() => canSplit.value && exec.splitShown)
const secondaryKey = computed(() => (splitShown.value ? exec.split.secondary : ''))
const isVisible = (key: string) => key === store.activeKey || key === secondaryKey.value

const splitCandidates = computed(() => store.tabs.filter(t => !t.replay && t.key !== store.activeKey).length)
const canToggleSplit = computed(() => !!active.value && !active.value.replay && (exec.split.on || splitCandidates.value > 0))

function toggleSplit() {
  if (!exec.toggleSplit()) toast.info('先再打开一个会话', { description: '并排需要两个会话：在左侧再点一个会话，或从会话的菜单里选「在右侧并排打开」。' })
}

/** 并排时键盘焦点所在的窗格（用边框高亮），平时就是当前标签。 */
const focusedKey = ref('')
const focusKey = computed(() => (splitShown.value && secondaryKey.value && focusedKey.value === secondaryKey.value ? secondaryKey.value : store.activeKey))

function paneStyle(key: string): Record<string, string> {
  if (!splitShown.value) return { flex: '1 1 0%' }
  if (key === secondaryKey.value) return { order: '3', flex: '1 1 0%' }
  return { order: '1', flex: `0 0 calc(${(exec.splitRatio * 100).toFixed(3)}% - 3px)` }
}

const splitHost = ref<HTMLElement | null>(null)
let dragging = false
function startDrag(ev: PointerEvent) {
  const host = splitHost.value
  if (!host) return
  dragging = true
  try {
    ;(ev.currentTarget as HTMLElement).setPointerCapture(ev.pointerId)
  } catch {
    /* 没有活动的指针（例如被脚本合成的事件）：拖动照常进行，只是不捕获指针 */
  }
  ev.preventDefault()
}
function onDrag(ev: PointerEvent) {
  const host = splitHost.value
  if (!dragging || !host) return
  const r = host.getBoundingClientRect()
  exec.setSplitRatio(ratioFromPointer(ev.clientX, r.left, r.width), false)
}
function endDrag(ev: PointerEvent) {
  if (!dragging) return
  dragging = false
  try {
    ;(ev.currentTarget as HTMLElement).releasePointerCapture(ev.pointerId)
  } catch {
    /* 已经释放 */
  }
  exec.setSplitRatio(exec.splitRatio)
}
function onDividerKey(ev: KeyboardEvent) {
  if (ev.key === 'ArrowLeft') exec.setSplitRatio(exec.splitRatio - 0.02)
  else if (ev.key === 'ArrowRight') exec.setSplitRatio(exec.splitRatio + 0.02)
  else return
  ev.preventDefault()
}

// 侧栏菜单「在右侧并排打开」发来的请求。
watch(
  () => exec.splitRequest,
  key => {
    if (!key) return
    exec.splitRequest = ''
    if (!store.tabs.some(t => t.key === key)) return
    if (!store.activeKey || store.activeKey === key) {
      store.activateTab(key)
      return
    }
    exec.setSecondary(key)
    focusedKey.value = key
  },
  { immediate: true },
)

// 被淘汰（断开）的会话：连接状态点回到「未连接」。
watch(
  () => exec.liveOrder,
  order => {
    for (const k of Object.keys(paneState)) if (!order.includes(k)) delete paneState[k]
  },
)

function dotClass(key: string): string {
  switch (paneState[key]) {
    case 'live':
      return 'bg-emerald-500'
    case 'connecting':
    case 'waiting':
      return 'bg-amber-500 animate-pulse'
    case 'failed':
      return 'bg-destructive'
    case 'ended':
      return 'bg-muted-foreground/40'
    default:
      return 'border border-muted-foreground/50'
  }
}

function dotLabel(key: string): string {
  switch (paneState[key]) {
    case 'live':
      return '已连接'
    case 'connecting':
      return '连接中'
    case 'waiting':
      return '重连中'
    case 'failed':
      return '连接失败'
    case 'ended':
      return '已结束'
    default:
      return '未连接'
  }
}

function closeTab(key: string) {
  delete paneState[key]
  store.closeTab(key)
}

// 记录回放用完即走：没有标签条可以手动关，离开它（点了侧栏里别的会话）就把它收掉，
// 否则每看过一次回放，就会在后台留下一份已经解析好的记录占着内存。想再看随时重新打开。
watch(
  () => store.activeKey,
  (_, prev) => {
    if (!prev) return
    const old = store.tabs.find(t => t.key === prev)
    if (old?.replay && store.activeKey !== prev) closeTab(prev)
  },
)

// ---------- 路由 ⇄ 标签 ----------
// 地址栏永远反映当前选中的会话：/exec/t/<会话ID>（回放标签带 ?view=record）。
// 侧栏点会话 → 路由变化 → 打开 / 激活对应标签；点标签或关闭标签 → 标签变化 → 路由跟随。
const routeSid = computed(() => (typeof route.params.sid === 'string' ? route.params.sid : ''))
const routeRecord = computed(() => route.query.view === 'record')

function pathOfTab(key: string): string {
  const t = store.tabs.find(x => x.key === key)
  return t ? execSessionPath(t.sid, !!t.replay) : EXEC_HOME
}

/** 地址栏指向的会话是否已经有对应的标签。 */
function routeTab() {
  const sid = routeSid.value
  if (!sid) return undefined
  const wantReplay = routeRecord.value
  return store.tabs.find(t => t.sid === sid && !!t.replay === wantReplay)
}

function syncFromRoute() {
  const sid = routeSid.value
  if (!sid) return
  // 已经打开过（含上次访问保存的标签，主机可能暂时离线）：直接激活。
  const open = routeTab()
  if (open) {
    store.activateTab(open.key)
    return
  }
  const it = findBySid(exec.items, sid)
  if (!it) return
  if (routeRecord.value || !it.live) store.openReplay(it.code, it.sid)
  else store.openTab(it.code, it.sid)
}

watch([routeSid, routeRecord], syncFromRoute, { immediate: true })
// 直接打开链接时，主机列表与历史索引稍后才到：到了之后补上。已经有标签时不重复处理，免得抢走用户刚点的标签。
watch(
  () => exec.items.length,
  () => {
    if (routeSid.value && !routeTab()) syncFromRoute()
  },
)

watch(
  () => store.activeKey,
  key => {
    const want = key ? pathOfTab(key) : EXEC_HOME
    if (route.fullPath !== want) void router.replace(want)
  },
)

// /exec 本身没有独立内容：只要有选中的标签，地址栏就换成它的地址。
// 刷新页面、点「回到终端首页」、解锁对话框关闭时浏览器回退到旧地址，都靠这条把地址栏拉回当前会话。
watch(
  () => route.fullPath,
  () => {
    if (route.name !== 'exec' || !store.activeKey) return
    const want = pathOfTab(store.activeKey)
    if (want !== EXEC_HOME) void router.replace(want)
  },
  { immediate: true },
)

/** 地址栏指向的会话找不到：既不在主机上，也不在历史索引里（读完历史才下结论，避免闪一下错误）。 */
const historyReady = computed(() =>
  store.hosts.filter(h => h.status === 'enabled').every(h => store.history[h.machine_code]?.loaded || !!store.history[h.machine_code]?.error),
)
const routeMissing = computed(
  () => !!routeSid.value && store.hostsLoaded && historyReady.value && !findBySid(exec.items, routeSid.value) && !store.tabs.some(t => t.sid === routeSid.value),
)
// 已经有别的标签可看时，不停在空页面上：提示一下，回到当前标签。
watch(routeMissing, missing => {
  if (!missing || !store.activeKey) return
  toast.error('找不到这个会话', { description: '它可能已被清除，或所在主机还没有上线。' })
  void router.replace(pathOfTab(store.activeKey))
})

// ---------- 当前会话的信息 ----------
const activeHost = computed<HostView | undefined>(() => (active.value ? store.hostByCode(active.value.code) : undefined))
const activeSession = computed(() => (active.value ? store.sessionOf(active.value.code, active.value.sid) : undefined))
const activePast = computed(() => (active.value ? store.history[active.value.code]?.sessions.find(x => x.sid === active.value!.sid) : undefined))

const recChip = computed(() => {
  const s = activeSession.value
  if (!activeIsLive.value || !s || s.state === 'exited') return null
  return recStateInfo(s.rec)
})

const persistence = computed(() => (activeHost.value ? holderBadge(activeHost.value) : null))
const lifecycle = computed(() => (activeIsLive.value && activeSession.value ? lifecycleBadge(activeSession.value, Math.floor(exec.now / 1000)) : null))

const canViewRecord = computed(() => {
  if (!active.value || active.value.replay) return false
  const s = activeSession.value
  if (s) return s.rec === 'on' || s.rec === 'paused' || s.rec === 'error'
  return !!activePast.value && activePast.value.rec_purged !== true && activePast.value.rec !== 'incognito'
})

function viewRecord() {
  if (active.value) void router.push(execSessionPath(active.value.sid, true))
}

async function changeLifecycle(v: IdleChoice) {
  const t = active.value
  if (!t) return
  try {
    await store.setSessionLifecycle(t.code, t.sid, v === 'inherit' ? null : v)
    toast.success('已修改这个会话的生命周期')
  } catch (e) {
    store.notifyError(e, '修改生命周期失败')
  }
}

const inheritLabel = computed(() => `沿用主机默认（${idleSummary(activeHost.value?.idle_sec ?? 0)}）`)

// 已结束的会话：说明原因；不可抗力导致的结束（主机重启等）提供「在原目录重建会话」。
const ended = computed(() => {
  const t = active.value
  if (!t) return null
  const s = activeSession.value
  const past = activePast.value
  const gone = t.replay ? false : s ? s.state === 'exited' : !!past
  if (!gone) return null
  const reason = past?.ended_reason
  return { reason, text: endedNote(reason, s?.exit_code ?? past?.exit_code), rebuild: canRebuild(reason) }
})

const rebuilding = ref(false)
async function rebuild() {
  const t = active.value
  if (!t || rebuilding.value) return
  const lost = activeHost.value?.holder?.lost?.find(l => l.sid === t.sid)
  rebuilding.value = true
  try {
    const s = await store.createSession(t.code, activeSession.value?.name || activePast.value?.name || undefined, undefined, { cwd: lost?.cwd, open: false })
    void router.push(execSessionPath(s.sid))
    toast.success('已新建会话', { description: lost?.cwd ? `在原来的目录 ${lost.cwd} 启动。` : '原来的目录没有记录，已在默认目录启动。' })
  } catch (e) {
    store.notifyError(e, '重建会话失败')
  } finally {
    rebuilding.value = false
  }
}

// ---------- 模型正在这台主机上运行 ----------
// 人在终端里时，要一眼看到「模型也在用这台主机」，并能立刻进入线程页或一键暂停。
const modelRunning = computed(() => activeHost.value?.model_running ?? 0)
const modelThreadKey = computed(() => {
  const code = activeHost.value?.machine_code
  if (!code) return ''
  const ts = execRuns.threads.filter(t => t.target === code)
  ts.sort((a, b) => b.active - a.active || Date.parse(b.last_at) - Date.parse(a.last_at))
  return ts[0]?.key ?? ''
})
const pausingModel = ref(false)
async function pauseHostModel() {
  const code = activeHost.value?.machine_code
  if (!code || pausingModel.value) return
  pausingModel.value = true
  try {
    await execRuns.setPaused('host', code, true)
  } finally {
    pausingModel.value = false
  }
}

// ---------- 新建 ----------
// 新建会话的目标：在线的 Agent 主机，或此刻能打开终端的 SSH 主机（Hub 直连与经网关都一样）。
const hasEnabledHost = computed(() => store.hosts.some(h => h.status === 'enabled'))
function canCreateOn(h: HostView): boolean {
  if (h.status !== 'enabled') return false
  return isSshHost(h) ? canOpenSshTerminal(h) : h.online && canHumanTerminal(h)
}
const newTarget = computed<HostView | undefined>(() => {
  const host = activeHost.value
  if (host && canCreateOn(host)) return host
  return store.hosts.find(canCreateOn)
})

const creating = ref(false)
async function newSession() {
  const h = newTarget.value
  if (!h || creating.value) return
  creating.value = true
  try {
    const s = isSshHost(h)
      ? await store.openSshTerminal(h, { open: false })
      : await store.createSession(h.machine_code, undefined, undefined, { open: false })
    void router.push(execSessionPath(s.sid))
  } catch (e) {
    store.notifyError(e, '新建会话失败')
  } finally {
    creating.value = false
  }
}

// ---------- 辅助条 ----------
// 消息提示（vue-sonner）贴在终端区域的底边：辅助条显示时，它的高度写到 <html> 的 --exec-keybar-height 上，
// 让消息停在辅助条上方（见 globals.css 的 .toaster-exec）；辅助条不在时清掉。
const keybarRef = ref<{ $el: HTMLElement } | null>(null)
const { height: keybarHeight } = useElementSize(computed(() => keybarRef.value?.$el ?? null))
watchEffect(() => {
  const h = showKeybar.value && activeIsLive.value ? Math.round(keybarHeight.value) : 0
  document.documentElement.style.setProperty('--exec-keybar-height', `${h}px`)
})
onBeforeUnmount(() => document.documentElement.style.removeProperty('--exec-keybar-height'))

const keyText = (seq: string) => activePane()?.sendText(seq)
const keyCursor = (k: CursorKey) => activePane()?.sendCursor(k)
const keyPaste = () => void activePane()?.paste()
function toggleModifier(m: 'ctrl' | 'alt') {
  if (m === 'ctrl') ctrl.value = !ctrl.value
  else alt.value = !alt.value
}

onBeforeUnmount(() => {
  panes.clear()
})
</script>

<template>
  <div class="flex-1 min-h-0 flex flex-col">
    <ExecPageHeader>
      <SquareTerminal class="w-4 h-4 text-primary shrink-0" aria-hidden="true" />
      <div class="min-w-0">
        <h1 class="truncate text-sm font-medium text-foreground leading-tight">{{ active ? active.sessionName : '终端' }}</h1>
        <p class="truncate text-[11px] text-muted-foreground leading-tight">
          <template v-if="active">{{ active.hostName }}<template v-if="active.replay"> · 记录回放</template></template>
          <template v-else>在左侧选择一个会话，或新建一个</template>
        </p>
      </div>

      <template #actions>
        <div v-if="modelRunning > 0" class="flex items-center">
          <button
            type="button"
            class="h-7 px-2 rounded-l-sm border border-emerald-500/30 bg-emerald-500/10 text-emerald-700 dark:text-emerald-300 text-[11px] flex items-center gap-1.5 cursor-pointer hover:bg-emerald-500/15 disabled:cursor-default"
            :disabled="!modelThreadKey"
            :title="modelThreadKey ? '这台主机上有模型的命令正在进行（运行中或等待批准），点击查看' : '这台主机上有模型的命令正在进行（运行中或等待批准）'"
            @click="router.push(execThreadPath(modelThreadKey))"
          >
            <Bot class="w-3.5 h-3.5" /><span class="tabular-nums">{{ modelRunning }}<span class="hidden sm:inline"> 个模型命令进行中</span></span>
          </button>
          <button
            type="button"
            class="h-7 w-7 rounded-r-sm border border-l-0 border-emerald-500/30 text-emerald-700 dark:text-emerald-300 flex items-center justify-center cursor-pointer hover:bg-emerald-500/15 disabled:opacity-50"
            :disabled="pausingModel"
            aria-label="暂停这台主机的模型"
            title="暂停这台主机的模型：取消进行中的命令并拒绝新的命令"
            @click="pauseHostModel"
          >
            <Pause class="w-3.5 h-3.5" />
          </button>
        </div>

        <span
          v-if="persistence"
          class="hidden md:inline-flex h-7 px-2 items-center rounded-sm border text-[11px] select-none"
          :class="{
            'border-emerald-500/30 bg-emerald-500/10 text-emerald-700 dark:text-emerald-300': persistence.tone === 'ok',
            'border-amber-500/30 bg-amber-500/10 text-amber-700 dark:text-amber-300': persistence.tone === 'warn',
            'border-destructive/30 bg-destructive/10 text-destructive': persistence.tone === 'bad',
          }"
          :title="persistence.title"
        >{{ persistence.text }}</span>

        <span
          v-if="recChip"
          class="hidden sm:flex h-7 px-2 rounded-sm border text-[11px] items-center gap-1.5 select-none"
          :class="{
            'border-emerald-500/30 bg-emerald-500/10 text-emerald-700 dark:text-emerald-300': recChip.tone === 'ok',
            'border-amber-500/30 bg-amber-500/10 text-amber-700 dark:text-amber-300': recChip.tone === 'warn',
            'border-border/80 text-muted-foreground': recChip.tone === 'muted',
          }"
          :title="recChip.title"
        >
          <span v-if="recChip.tone !== 'muted'" class="w-1.5 h-1.5 rounded-full bg-current" :class="recChip.tone === 'ok' && 'animate-pulse'" aria-hidden="true" />
          {{ recChip.text }}
        </span>

        <LifecycleMenu
          v-if="lifecycle && activeSession"
          :model-value="idleChoiceOf(activeSession)"
          :inherit-label="inheritLabel"
          title="这个会话没人使用时怎么处理"
          @update:model-value="changeLifecycle"
        >
          <template #trigger>
            <button
              type="button"
              class="h-7 px-2 rounded-sm border text-[11px] flex items-center gap-1.5 cursor-pointer transition-colors"
              :class="lifecycle.tone === 'ok' ? 'border-border/80 text-muted-foreground hover:text-foreground hover:bg-muted' : 'border-sky-500/30 bg-sky-500/10 text-sky-700 dark:text-sky-300 hover:bg-sky-500/15'"
              :title="lifecycle.title"
              :aria-label="`生命周期：${lifecycle.text}，点击修改`"
            >
              <InfinityIcon v-if="lifecycle.tone === 'ok'" class="w-3.5 h-3.5" />
              <Hourglass v-else class="w-3.5 h-3.5" />
              <span class="hidden sm:inline">{{ lifecycle.text }}</span>
            </button>
          </template>
        </LifecycleMenu>

        <button
          v-if="canSplit && active && !active.replay"
          type="button"
          class="h-7 w-7 rounded-sm border flex items-center justify-center cursor-pointer transition-colors disabled:opacity-40 disabled:cursor-not-allowed"
          :class="exec.split.on ? 'border-primary/40 bg-primary/10 text-primary' : 'border-border/80 text-muted-foreground hover:text-foreground hover:bg-muted'"
          :aria-pressed="exec.split.on"
          :aria-label="exec.split.on ? '退出并排' : '并排显示两个会话'"
          :title="exec.split.on ? '退出并排' : canToggleSplit ? '并排显示两个会话' : '先再打开一个会话，才能并排'"
          :disabled="!canToggleSplit"
          @click="toggleSplit"
        >
          <Columns2 class="w-3.5 h-3.5" />
        </button>

        <Button
          v-if="canViewRecord"
          variant="ghost"
          size="sm"
          class="h-7 px-2 gap-1.5 text-[11px] rounded-sm"
          title="查看这个会话的记录"
          aria-label="查看记录"
          @click="viewRecord"
        >
          <History class="w-3.5 h-3.5" /><span class="hidden sm:inline">查看记录</span>
        </Button>
      </template>
    </ExecPageHeader>

    <!-- 会话已结束：原因；不可抗力（主机重启等）时可以在原目录重建 -->
    <div
      v-if="ended"
      class="shrink-0 px-3 sm:px-4 py-2 border-b border-border/70 bg-muted/40 flex flex-wrap items-center gap-x-3 gap-y-1.5 text-xs"
      role="status"
    >
      <TriangleAlert v-if="ended.rebuild" class="w-3.5 h-3.5 shrink-0 text-amber-500" aria-hidden="true" />
      <span class="min-w-0 flex-1 text-muted-foreground leading-relaxed">{{ ended.text }}</span>
      <Button v-if="ended.rebuild" size="sm" class="h-7 rounded-sm text-xs gap-1.5" :disabled="rebuilding" @click="rebuild">
        <Loader2 v-if="rebuilding" class="w-3.5 h-3.5 animate-spin" />
        <RotateCcw v-else class="w-3.5 h-3.5" />
        在原目录重建会话
      </Button>
    </div>

    <!-- 终端区域：live 集合里的会话常驻（v-show 切换，不丢滚动缓冲与连接）；并排时左右两个窗格同时显示 -->
    <div
      ref="splitHost"
      class="flex-1 min-h-0 relative flex"
      role="region"
      aria-label="终端"
      @wheel.capture="onZoomWheel"
      @keydown.capture="onZoomKey"
    >
      <div
        v-for="t in livePanes"
        :key="t.key"
        v-show="isVisible(t.key)"
        class="min-w-0 min-h-0 flex flex-col"
        :class="splitShown && 'border-t-2 ' + (t.key === focusKey ? 'border-primary/70' : 'border-transparent')"
        :style="paneStyle(t.key)"
        @focusin="focusedKey = t.key"
        @pointerdown.capture="focusedKey = t.key"
      >
        <div v-if="splitShown" class="h-7 shrink-0 px-2 flex items-center gap-1.5 border-b border-border/60 bg-card/50 text-[11px]">
          <span class="w-1.5 h-1.5 rounded-full shrink-0" :class="dotClass(t.key)" role="img" :aria-label="dotLabel(t.key)" />
          <span class="min-w-0 flex-1 truncate" :title="t.label"><span class="font-medium text-foreground">{{ t.sessionName }}</span><span class="text-muted-foreground"> · {{ t.hostName }}</span></span>
          <button
            type="button"
            class="p-1 rounded-sm text-muted-foreground hover:text-foreground hover:bg-muted cursor-pointer"
            :aria-label="`关闭窗格 ${t.label}`"
            title="关闭这个窗格（会话继续在主机上运行）"
            @click="exec.closeSplitPane(t.key)"
          >
            <X class="w-3 h-3" />
          </button>
        </div>
        <TerminalPane
          :ref="(el: unknown) => setPane(t.key, el)"
          v-model:ctrl="ctrl"
          v-model:alt="alt"
          class="flex-1 min-h-0"
          :code="t.code"
          :sid="t.sid"
          :label="t.label"
          :active="isVisible(t.key)"
          :autofocus="t.key === focusKey"
          :font-size="fontSize"
          @state="p => onPaneState(t.key, p)"
          @activity="exec.markUnread(t.key)"
          @close="closeTab(t.key)"
        />
      </div>
      <div
        v-if="splitShown"
        class="w-1.5 shrink-0 cursor-col-resize bg-border/60 hover:bg-primary/40 focus-visible:bg-primary/60 focus-visible:outline-none touch-none transition-colors"
        style="order: 2"
        role="separator"
        aria-orientation="vertical"
        aria-label="调整左右窗格宽度"
        :aria-valuenow="Math.round(exec.splitRatio * 100)"
        aria-valuemin="25"
        aria-valuemax="75"
        tabindex="0"
        @pointerdown="startDrag"
        @pointermove="onDrag"
        @pointerup="endDrag"
        @pointercancel="endDrag"
        @keydown="onDividerKey"
      />
      <RecordViewer
        v-for="t in replayTabs"
        :key="t.key"
        :code="t.code"
        :sid="t.sid"
        :label="t.label"
        :active="t.key === store.activeKey"
        :font-size="fontSize"
        @close="closeTab(t.key)"
      />

      <div
        v-if="zoomNote"
        class="pointer-events-none absolute right-3 bottom-3 z-10 px-2.5 py-1 rounded-sm border border-border bg-popover/95 text-popover-foreground text-[11px] tabular-nums shadow-sm"
        role="status"
        aria-live="polite"
      >{{ zoomNote }}</div>

      <div v-if="!tabViews.length" class="absolute inset-0 overflow-y-auto">
        <div class="max-w-md px-6 py-10 space-y-3">
          <template v-if="routeMissing">
            <h1 class="text-sm font-bold text-foreground">找不到这个会话</h1>
            <p class="text-xs leading-relaxed text-muted-foreground">
              它可能已被清除，或所在主机还没有上线。主机重新连接后，仍在运行的会话会自动出现在左侧。
            </p>
            <Button variant="outline" size="sm" class="h-8 rounded-sm text-xs" @click="router.replace(EXEC_HOME)">回到终端首页</Button>
          </template>
          <template v-else-if="!store.hostsLoaded">
            <div class="h-4 w-40 rounded-sm bg-muted/60 animate-pulse" />
            <div class="h-3 w-64 rounded-sm bg-muted/60 animate-pulse" />
          </template>
          <template v-else-if="!hasEnabledHost">
            <h1 class="text-sm font-bold text-foreground">还没有可用的主机</h1>
            <p class="text-xs leading-relaxed text-muted-foreground">
              在目标主机上运行 RelayMesh Agent，核对机器码后启用，就能在这里打开它的终端。会话在主机上持续运行，关掉浏览器也不会中断。
            </p>
            <Button size="sm" class="h-8 rounded-sm text-xs gap-1.5" @click="exec.discoveryOpen = true"><Radar class="w-3.5 h-3.5" />接入主机</Button>
          </template>
          <template v-else>
            <h1 class="text-sm font-bold text-foreground">选择一个会话</h1>
            <p class="text-xs leading-relaxed text-muted-foreground">
              <span class="hidden md:inline">从左侧的清单里打开已有会话，或新建一个。</span>
              <span class="md:hidden">点左上角的菜单图标打开已有会话，或新建一个。</span>
              离开页面只是断开，会话会继续在主机上运行，下次接着用。在终端里按住 Ctrl 滚动滚轮可以缩放字号，Ctrl+0 恢复默认。
            </p>
            <Button v-if="newTarget" size="sm" class="h-8 rounded-sm text-xs gap-1.5" :disabled="creating" @click="newSession">
              <Loader2 v-if="creating" class="w-3.5 h-3.5 animate-spin" />
              <Plus v-else class="w-3.5 h-3.5" />
              在 {{ hostTitle(newTarget) }} 上新建会话
            </Button>
          </template>
        </div>
      </div>
    </div>

    <TerminalKeybar
      v-if="showKeybar && activeIsLive"
      ref="keybarRef"
      :ctrl="ctrl"
      :alt="alt"
      @text="keyText"
      @cursor="keyCursor"
      @toggle="toggleModifier"
      @paste="keyPaste"
    />
  </div>
</template>
