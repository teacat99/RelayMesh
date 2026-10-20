<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useMediaQuery } from '@vueuse/core'
import { toast } from 'vue-sonner'
import { ArrowLeft, ExternalLink, Loader2, Power, SquareTerminal, X } from 'lucide-vue-next'
import { Dialog, DialogContent, DialogDescription, DialogTitle } from '../../ui/dialog'
import Button from '../../ui/button/Button.vue'
import LauncherList from './LauncherList.vue'
import LauncherForm from './LauncherForm.vue'
import LauncherTerminal from './LauncherTerminal.vue'
import { useTerminalStore } from '../../../stores/terminal'
import { useSessionLaunchersStore } from '../../../stores/sessionLaunchers'
import { toApiError, UnlockCancelled, type GuardItem, type HostView, type LauncherGuard, type LauncherOtherRun, type LauncherRunView, type TerminalLauncher } from '../../../api/terminal'
import type { ConnectionState, StateInfo } from '../../../lib/terminal/connection'
import { copyText } from '../../../lib/terminal/clipboard'
import { hostTitle } from '../../../lib/terminal/format'
import { guardLimits, guardSupported, wakeNotice, wakeStartLine } from '../../../lib/terminal/guard'
import {
  blankDraft,
  cursorCliDraft,
  draftOf,
  headlessDraft,
  hostPlatformOf,
  hostReadiness,
  isHeadlessPreset,
  launcherHosts,
  launcherLimits,
  normalizeName,
  sessionCwd,
  startLine,
  type LauncherDraft,
} from '../../../lib/terminal/launcher'
import { runNotice, runReadiness } from '../../../lib/terminal/launcherRun'
import { projectScope, suggestDraft, suggestLauncher, type ListTab, type TabChoice } from '../../../lib/terminal/sessionLaunchers'
import { execSessionPath, execThreadPath } from '../../../lib/mode'
import { settleDialogHistory } from '../../../composables/useDialogBackGesture'

/**
 * 终端启动器：会话页顶栏「终端」按钮打开的弹窗。三个画面：
 *   list  预设清单（启动 / 继续 / 运行 / 编辑 / 删除）；
 *   edit  新建或编辑一条预设；
 *   run   一个连着会话的终端（会话在主机上运行，关掉弹窗只是断开，ExecMesh 里同样能看到、能接着用）。
 * 会话就是普通的人工交互式终端会话：用的是 ExecMesh「新建会话」同一套接口，没有任何特殊通道。
 * 「非交互续聊」预设（#126）不开终端：点「运行」由服务端经主机的执行通道提交一次 `agent -p --resume …`，
 * 结果（状态、用时、最后一条回复）显示在清单里；浏览器什么都不用执行。
 *
 * 从会话页打开时带着这个会话的工作流编号（#140）：清单分「本项目 / 全部」，本项目里没有对应的续聊预设时，
 * 顶上给一张预填好的「为这个会话创建续聊预设」建议卡（数据来自 GET /terminal/launchers/for-session）。
 */
const props = defineProps<{ open: boolean; focusPresetId?: string; workflowId?: string }>()
const emit = defineEmits<{ (e: 'update:open', v: boolean): void }>()

const terminal = useTerminalStore()
const sessionLaunchers = useSessionLaunchersStore()
const router = useRouter()

type View = 'list' | 'edit' | 'run'
const view = ref<View>('list')

const limits = computed(() => launcherLimits(terminal.prefs?.limits))
const launchers = computed(() => terminal.launchers)
const hosts = computed<HostView[]>(() => terminal.hosts)

// 会话守护（D-214）：旧版 Hub 不认识时整个功能不露出。
const guardOk = computed(() => guardSupported(terminal.prefs?.limits))
const gLimits = computed(() => guardLimits(terminal.prefs?.limits, limits.value.command))

/**
 * 唤醒时文字与回车之间的间隔：Cursor CLI 这类 TUI 会把一次性送进去的整串当成粘贴，回车成了换行；
 * 隔一小会儿再单独敲回车，才是「发送」。
 */
const WAKE_SUBMIT_DELAY_MS = 400

// ---------- 清单 ----------
const busyId = ref('')
const wakeBusyId = ref('')
const permitBusyId = ref('')
/** 正在提交运行的「非交互续聊」预设（要先解锁、再等服务端接收）。 */
const runBusyId = ref('')
/** 正在取消的「其他非交互运行」（#147）：取消只需要登录，不需要解锁。 */
const cancelBusyId = ref('')

/**
 * 取消一次不属于任何预设的运行：主机会终止它，对话本身不受影响（之后还可以续聊）。
 * 运行刚好结束了也不算错——服务端把最终记录原样返回，清单随即刷新成真实状态。
 */
async function cancelOther(r: LauncherOtherRun) {
  if (cancelBusyId.value) return
  cancelBusyId.value = r.run_id
  try {
    await terminal.cancelOtherRun(r.run_id)
    toast.success('已取消这次运行', { description: '主机会终止它；对话本身没有结束，之后还可以续聊。' })
  } catch (e) {
    terminal.notifyError(e, '取消运行失败')
  } finally {
    cancelBusyId.value = ''
  }
}

/** 正在取消自动续跑的出事运行（#151）：取消只需要登录，不需要解锁。 */
const autoBusyId = ref('')

/**
 * 取消一次排期中的自动续跑：只是让系统少做一件事，已经开跑 / 作废的服务端回 409，清单随即刷新成真实状态。
 */
async function cancelAuto(r: LauncherRunView) {
  if (autoBusyId.value) return
  autoBusyId.value = r.run_id
  try {
    await terminal.cancelAutoResume(r.run_id)
    toast.success('已取消这次自动续跑', { description: '这次失败不会再自动重试；想接着聊，可以直接点「运行」。' })
  } catch (e) {
    terminal.notifyError(e, '取消自动续跑失败')
  } finally {
    autoBusyId.value = ''
  }
}

/**
 * 运行一次「非交互续聊」预设：服务端直接提交（需要刚刚解锁），这里只负责说清楚发生了什么、去哪里看结果。
 * 前置条件不满足（主机离线、对话还在运行、上一次还没跑完……）时服务端不产生任何运行记录，原因就地提示。
 */
async function runPreset(l: TerminalLauncher) {
  if (busyId.value || runBusyId.value) return
  const ready = runReadiness(terminal.hostByCode(l.host), terminal.launcherRuns[l.id])
  if (!ready.ok) {
    toast.error(`没能运行「${l.name}」`, { description: ready.text })
    return
  }
  runBusyId.value = l.id
  try {
    const plan = await terminal.runLauncher(l.id)
    const n = runNotice(plan)
    toast.success(n.title, { description: n.description, duration: plan.warnings?.length ? 15_000 : 8_000 })
  } catch (e) {
    terminal.notifyError(e, `运行「${l.name}」失败`)
  } finally {
    runBusyId.value = ''
  }
}

async function startPreset(l: TerminalLauncher) {
  if (busyId.value) return
  const host = terminal.hostByCode(l.host)
  const ready = hostReadiness(host)
  if (!ready.ok || !host) {
    toast.error(`没能启动「${l.name}」`, { description: ready.ok ? '主机不存在' : ready.text })
    return
  }
  // 「先进入目录」那一句怎么写、~ 怎么展开，都按这台主机的系统与 shell 来（Windows 上是 PowerShell / cmd，不是 bash）。
  const platform = hostPlatformOf(host)
  busyId.value = l.id
  try {
    // 会话名取预设的名字：ExecMesh 里就叫这个，启动器也靠它认出这个预设已经在运行的会话。
    const s = await terminal.createSession(l.host, normalizeName(l.name), undefined, { cwd: sessionCwd(l.dir, platform), open: false, compact: !!l.guard?.enabled })
    enterRun({ code: l.host, sid: s.sid, name: l.name, hostName: hostTitle(host), dir: l.dir ?? '', line: startLine(l, platform), fresh: true })
  } catch (e) {
    terminal.notifyError(e, `启动「${l.name}」失败`)
  } finally {
    busyId.value = ''
  }
}

function resumePreset(l: TerminalLauncher, sid: string) {
  const host = terminal.hostByCode(l.host)
  enterRun({ code: l.host, sid, name: l.name, hostName: host ? hostTitle(host) : l.host, dir: l.dir ?? '', line: '', fresh: false })
}

/**
 * 唤醒：服务端生成并登记一份计划（要求刚刚解锁），这里照着做——
 *   type      连上存活的会话，把提示词敲进去（文字、停一小会儿、再单独敲回车）；
 *   relaunch  会话已经结束：在预设的目录里新开会话，重新拉起智能体（接着原来的对话时提示词随命令一起带上）。
 * 服务端只登记不执行；这一步等于人在键盘上敲，所以敲了什么在终端里看得见。
 * 注意计划是先登记再执行的：执行中途失败（例如新开会话失败），这次唤醒仍然计入次数。
 */
async function toggleGuardPermit(item: GuardItem, permit: 0 | 1) {
  if (busyId.value || wakeBusyId.value || permitBusyId.value) return
  permitBusyId.value = item.preset_id
  try {
    await terminal.setGuardPermit(item.preset_id, permit)
    toast.success(permit === 0 ? '已标记任务全部完成' : '已取消标记', {
      description: permit === 0 ? '本会话结束或停摆时不告警；触发一次后会自动恢复守护。' : '守护将按约定告警与推送。',
    })
  } catch (e) {
    terminal.notifyError(e, permit === 0 ? '标记失败' : '取消标记失败')
  } finally {
    permitBusyId.value = ''
  }
}

async function wakePreset(item: GuardItem) {
  if (busyId.value || wakeBusyId.value) return
  const l = launchers.value.find(x => x.id === item.preset_id)
  const host = terminal.hostByCode(item.host)
  if (!l || !host) {
    toast.error(`没能唤醒「${item.name}」`, { description: l ? '主机已不在列表里（可能被删除了）' : '这个预设已经不存在了' })
    return
  }
  const platform = hostPlatformOf(host)
  wakeBusyId.value = item.preset_id
  try {
    const plan = await terminal.wakeGuard(item.preset_id, item.wake_mode === 'none' ? undefined : item.wake_mode)
    if (plan.mode === 'type' && plan.session_sid) {
      const n = wakeNotice(plan, false)
      enterRun({
        code: plan.host,
        sid: plan.session_sid,
        name: l.name,
        hostName: hostTitle(host),
        dir: l.dir ?? '',
        line: plan.prompt,
        fresh: false,
        submitDelayMs: WAKE_SUBMIT_DELAY_MS,
      })
      toast.success(n.title, { description: n.description })
      return
    }
    const s = await terminal.createSession(plan.host, normalizeName(plan.name), undefined, { cwd: sessionCwd(plan.dir, platform), open: false, compact: true })
    const { line, promptInline } = wakeStartLine(plan, platform)
    enterRun({ code: plan.host, sid: s.sid, name: l.name, hostName: hostTitle(host), dir: plan.dir ?? '', line, fresh: true })
    const n = wakeNotice(plan, promptInline)
    toast.success(n.title, {
      description: n.description,
      // 没法随命令带上提示词时，把它交到你手上：等智能体起来后粘贴给它。
      ...(promptInline
        ? {}
        : {
            duration: 20_000,
            action: {
              label: '复制提示词',
              onClick: () => void copyText(plan.prompt).then(ok => (ok ? toast.success('已复制提示词') : toast.error('复制失败，请在终端里手动输入'))),
            },
          }),
    })
  } catch (e) {
    terminal.notifyError(e, `唤醒「${item.name}」失败`)
  } finally {
    wakeBusyId.value = ''
    void terminal.refreshGuard()
  }
}

/** 非交互唤醒：服务端直接提交运行（需要刚刚解锁），浏览器什么都不用执行，只告诉用户去哪里看。 */
async function wakeHeadless(item: GuardItem) {
  if (busyId.value || wakeBusyId.value) return
  wakeBusyId.value = item.preset_id
  try {
    const plan = await terminal.wakeGuard(item.preset_id, 'headless')
    const n = wakeNotice(plan, false)
    toast.success(n.title, { description: n.description, duration: 12_000 })
  } catch (e) {
    terminal.notifyError(e, `非交互唤醒「${item.name}」失败`)
  } finally {
    wakeBusyId.value = ''
    void terminal.refreshGuard()
  }
}

async function removePreset(l: TerminalLauncher) {
  try {
    await terminal.deleteLauncher(l.id)
    toast.success(`已删除预设「${l.name}」`, { description: '已经在运行的会话不受影响。' })
  } catch (e) {
    terminal.notifyError(e, '删除预设失败')
  }
}

// ---------- 本项目 / 全部、为这个会话创建续聊预设（#140） ----------
const sessionView = computed(() => (props.workflowId ? sessionLaunchers.views[props.workflowId] ?? null : null))
const scope = computed(() => projectScope(sessionView.value))
const suggest = computed(() => sessionView.value?.suggest ?? null)
const suggestReason = computed(() => sessionView.value?.reason ?? '')
/** 标签的选择：auto = 还没选过。放在这里（而不是清单里）是因为进编辑页再回来清单会重新挂载，选择不该丢。 */
const listTab = ref<TabChoice>('auto')
const suggestBusy = ref(false)

/** 一键创建建议的预设：保存要先解锁（预设决定点「运行」之后会运行什么）；建好后视图马上读新，建议卡收起、会话页的「运行」按钮就位。 */
async function createSuggested() {
  const s = suggest.value
  if (!s || s.needs_host || suggestBusy.value || busyId.value) return
  const l = suggestLauncher(s)
  suggestBusy.value = true
  try {
    await terminal.saveLauncher(l)
    toast.success(`已创建预设「${l.name}」`, {
      description: '这个会话停下来时，输入区底部会出现「运行」按钮；运行前会先请你确认。',
      duration: 8_000,
    })
    if (props.workflowId) await sessionLaunchers.refresh(props.workflowId)
    listTab.value = 'project'
  } catch (e) {
    terminal.notifyError(e, '创建预设失败')
  } finally {
    suggestBusy.value = false
  }
}

/** 调整后创建：打开新建表单，名字 / 目录 / 工作流 / 权限姿态已按建议填好；主机没法唯一确定时取名字对上的第一台（人可以改）。 */
function customizeSuggested() {
  const s = suggest.value
  if (!s) return
  draft.value = suggestDraft(s, s.host || sessionView.value?.hosts?.[0] || '')
  draftGuard.value = undefined
  draftIsNew.value = true
  formError.value = ''
  view.value = 'edit'
}

// ---------- 编辑 ----------
const draft = ref<LauncherDraft | null>(null)
/** 正在编辑的预设已有的守护配置（保存是整条覆盖，表单要把它带回去）。 */
const draftGuard = ref<LauncherGuard | undefined>(undefined)
const draftIsNew = ref(true)
const saving = ref(false)
const formError = ref('')

const defaultHost = () => launcherHosts(hosts.value)[0]?.machine_code ?? ''

function newPreset() {
  draft.value = blankDraft(defaultHost())
  draftGuard.value = undefined
  draftIsNew.value = true
  formError.value = ''
  view.value = 'edit'
}

function newHeadlessPreset() {
  draft.value = headlessDraft(defaultHost())
  draftGuard.value = undefined
  draftIsNew.value = true
  formError.value = ''
  view.value = 'edit'
}

function newCursorPreset() {
  draft.value = cursorCliDraft(defaultHost())
  draftGuard.value = undefined
  draftIsNew.value = true
  formError.value = ''
  view.value = 'edit'
}

function editPreset(l: TerminalLauncher) {
  draft.value = draftOf(l)
  draftGuard.value = l.guard
  draftIsNew.value = false
  formError.value = ''
  view.value = 'edit'
}

const others = computed(() => launchers.value.filter(l => l.id !== draft.value?.id))

async function savePreset(l: TerminalLauncher, thenStart: boolean) {
  if (saving.value) return
  saving.value = true
  formError.value = ''
  try {
    await terminal.saveLauncher(l)
  } catch (e) {
    // 取消解锁：什么都没发生，留在表单里；其余失败（重名、目录不是完整路径……）把服务端的原因就地写出来。
    if (!(e instanceof UnlockCancelled)) formError.value = toApiError(e).message
    saving.value = false
    return
  }
  saving.value = false
  draft.value = null
  view.value = 'list'
  if (thenStart) {
    // 以服务端保存下来的那份为准（名字被规整过），别用表单里的草稿启动。
    const saved = terminal.launchers.find(x => x.id === l.id) ?? l
    await (isHeadlessPreset(saved) ? runPreset(saved) : startPreset(saved))
  } else {
    toast.success(draftIsNew.value ? `已保存预设「${l.name}」` : `已更新预设「${l.name}」`)
  }
}

// ---------- 终端 ----------
interface RunTarget {
  code: string
  sid: string
  name: string
  hostName: string
  dir: string
  /** 会话就绪后要敲的那一行；空串 = 不敲。 */
  line: string
  /** 刚新开的会话（false = 连回已有的）。 */
  fresh: boolean
  /** 大于 0：文字敲进去之后隔这么久再单独敲回车（唤醒 TUI 时用）。 */
  submitDelayMs?: number
}
const run = ref<RunTarget | null>(null)
const conn = ref<ConnectionState>('idle')
const ended = ref(false)

function enterRun(t: RunTarget) {
  run.value = t
  conn.value = 'idle'
  ended.value = false
  confirmKill.value = false
  view.value = 'run'
}

function onTermState(p: { state: ConnectionState; info: StateInfo }) {
  conn.value = p.state
  ended.value = p.state === 'live' && p.info.over
}

const DOTS: Record<string, { cls: string; label: string }> = {
  live: { cls: 'bg-emerald-500', label: '已连接' },
  connecting: { cls: 'bg-amber-500 animate-pulse', label: '连接中' },
  waiting: { cls: 'bg-amber-500 animate-pulse', label: '重连中' },
  failed: { cls: 'bg-destructive', label: '连接失败' },
  ended: { cls: 'bg-muted-foreground/40', label: '会话已结束' },
  idle: { cls: 'border border-muted-foreground/50', label: '未连接' },
}
const dot = computed(() => DOTS[ended.value ? 'ended' : conn.value] ?? DOTS.idle)

function backToList() {
  run.value = null
  view.value = 'list'
}

async function openInExec() {
  const t = run.value
  if (!t) return
  emit('update:open', false)
  // 弹窗打开时往浏览器历史里压了一条哨兵记录，关闭时用 history.back() 弹掉（useDialogBackGesture），那是异步的：
  // 不等它走完就跳转，随后到来的回退会把刚跳转的页面又退回去（ExecMesh 的页面一闪就回到了原处）。
  await nextTick()
  await settleDialogHistory()
  await router.push(execSessionPath(t.sid))
}

/** 清单里点「运行记录」：关掉弹窗，到 ExecMesh 的模型线程里看这次运行的完整输出。 */
async function openThread(key: string) {
  emit('update:open', false)
  await nextTick()
  await settleDialogHistory()
  await router.push(execThreadPath(key))
}

// 结束会话：点两次（第一次变成「确认结束」），没有确认就几秒后恢复。结束会话需要解锁。
const confirmKill = ref(false)
const killing = ref(false)
let killTimer: ReturnType<typeof setTimeout> | undefined
async function askKill() {
  const t = run.value
  if (!t || killing.value) return
  if (!confirmKill.value) {
    confirmKill.value = true
    clearTimeout(killTimer)
    killTimer = setTimeout(() => (confirmKill.value = false), 4000)
    return
  }
  clearTimeout(killTimer)
  confirmKill.value = false
  killing.value = true
  try {
    await terminal.killSession(t.code, t.sid)
    toast.success(`已结束「${t.name}」`)
    backToList()
  } catch (e) {
    terminal.notifyError(e, '结束会话失败')
  } finally {
    killing.value = false
  }
}
onBeforeUnmount(() => clearTimeout(killTimer))

// ---------- 弹窗行为 ----------
const isTouch = useMediaQuery('(pointer: coarse)')
const isNarrow = useMediaQuery('(max-width: 767px)')
const keybar = computed(() => isTouch.value || isNarrow.value)

/**
 * 终端里按 Esc 是给程序的（Cursor CLI 用它打断正在进行的操作，vim 用它退出插入模式），不能顺手把弹窗关了；
 * 点到弹窗外面也不能关——长时间的交互会话被一次误触丢掉画面，代价太大。终端画面只能用右上角的 × 或「返回清单」离开。
 * 编辑表单里 Esc 回到清单（不丢弃清单，也不是整个关掉）；点外面不关，免得填了一半的内容丢了。
 */
function onEscape(e: KeyboardEvent) {
  if (view.value === 'run') {
    e.preventDefault()
  } else if (view.value === 'edit') {
    e.preventDefault()
    if (!saving.value) view.value = 'list'
  }
}
function onOutside(e: Event) {
  if (view.value !== 'list') e.preventDefault()
}

const contentClass = computed(() =>
  view.value === 'run'
    ? [
        'flex flex-col gap-0 overflow-hidden p-0',
        'h-[min(86vh,860px)] w-[calc(100vw-1.5rem)] max-w-6xl',
        // 手机：铺满整个屏幕（软键盘弹出时留给终端的空间本来就小）。
        'max-sm:left-0 max-sm:top-0 max-sm:h-[100dvh] max-sm:max-h-[100dvh] max-sm:w-screen max-sm:max-w-none max-sm:translate-x-0 max-sm:translate-y-0 max-sm:rounded-none max-sm:border-0',
      ].join(' ')
    // 弹窗内容是个 grid：子项默认的最小宽度是内容宽度，一条很长的目录 / 命令会把整列撑出弹窗（右边的按钮被挤出屏幕）。
    : 'max-w-xl gap-3 p-5 [&>*]:min-w-0',
)

const hostsLoaded = computed(() => terminal.hostsLoaded)
const gateClosed = computed(() => terminal.status !== null && !terminal.gateOpen)

// ---------- 打开 / 关闭 ----------
// 弹窗开着期间轮询主机与总闸状态：「在线 / 离线」「已在运行」要跟着实际情况走；终端里有操作时它也负责给解锁令牌保活。
// 注意要放在上面所有状态声明之后：immediate 的回调在声明处立刻执行，会读到后面才声明的变量。
let polling = false
let runPolling = false
// 会话对应的预设视图（#140）：开着期间按它自己的节奏读（有运行在进行时勤一点）；记下读的是哪个工作流，换会话 / 关窗时才能对称地停掉。
let sessionPolled = ''
// 守护状态（唤醒还是重新拉起、有没有出问题）由服务端按「主机上现在有没有会话」现算：弹窗开着时比顶栏的半分钟一次更勤地读。
const GUARD_DIALOG_POLL_MS = 10_000
let guardTimer: ReturnType<typeof setInterval> | undefined
function stopGuardTimer() {
  if (guardTimer) clearInterval(guardTimer)
  guardTimer = undefined
}
function stopSessionPolling() {
  if (sessionPolled) sessionLaunchers.stopPolling(sessionPolled)
  sessionPolled = ''
}
watch(
  () => props.open,
  open => {
    if (open) {
      if (!polling) {
        terminal.startPolling()
        polling = true
      }
      // 轮询是共享的（别的页面可能已经在轮询、不会立刻刷新）：打开时主动读一次，免得「在线 / 已在运行」是旧的。
      void terminal.refreshHosts()
      void terminal.loadStatus()
      void terminal.refreshGuard()
      // 非交互续聊的运行现状：开着弹窗期间轮询（有运行在进行时勤一点）。
      if (!runPolling) {
        terminal.startLauncherRunPolling()
        runPolling = true
      }
      guardTimer ??= setInterval(() => {
        if (!document.hidden && terminal.guardWatching) void terminal.refreshGuard()
      }, GUARD_DIALOG_POLL_MS)
      if (props.workflowId && !sessionPolled) {
        sessionPolled = props.workflowId
        sessionLaunchers.startPolling(sessionPolled)
      }
      return
    }
    if (polling) {
      terminal.stopPolling()
      polling = false
    }
    if (runPolling) {
      terminal.stopLauncherRunPolling()
      runPolling = false
    }
    stopGuardTimer()
    stopSessionPolling()
    listTab.value = 'auto'
    suggestBusy.value = false
    // 关掉弹窗：终端只是断开（会话继续在主机上运行）；下次打开回到清单，想接着用点「继续」。
    view.value = 'list'
    run.value = null
    draft.value = null
    draftGuard.value = undefined
    formError.value = ''
    wakeBusyId.value = ''
    runBusyId.value = ''
    cancelBusyId.value = ''
    killing.value = false
    confirmKill.value = false
  },
  { immediate: true },
)
// 回到清单（新开 / 结束会话、保存 / 删除预设之后）：清单上的守护状态要马上跟上——刚开的会话让「重新拉起」变回「唤醒」，不必等下一轮。
watch(view, v => {
  if (v === 'list' && props.open) {
    void terminal.refreshGuard()
    // 保存 / 删除预设之后，「本项目」里有哪些、还要不要给建议都变了：马上读新，不等下一轮。
    if (props.workflowId) void sessionLaunchers.refresh(props.workflowId)
  }
})
onBeforeUnmount(() => {
  if (polling) terminal.stopPolling()
  if (runPolling) terminal.stopLauncherRunPolling()
  stopGuardTimer()
  stopSessionPolling()
})
</script>

<template>
  <Dialog :open="open" @update:open="(v: boolean) => emit('update:open', v)">
    <DialogContent
      :show-close="false"
      :class="contentClass"
      data-testid="terminal-launcher"
      :data-view="view"
      @escape-key-down="onEscape"
      @pointer-down-outside="onOutside"
      @interact-outside="onOutside"
    >
      <!-- 清单 / 编辑 -->
      <template v-if="view !== 'run'">
        <div class="flex items-start gap-2">
          <div class="min-w-0 flex-1 space-y-1">
            <DialogTitle class="flex items-center gap-2 text-sm font-semibold">
              <SquareTerminal class="size-4 text-primary" aria-hidden="true" />
              {{ view === 'edit' ? (draftIsNew ? '新建启动预设' : '编辑启动预设') : '终端启动器' }}
            </DialogTitle>
            <DialogDescription class="text-xs leading-relaxed text-muted-foreground">
              <template v-if="view === 'edit'">选一台主机、一个目录和一条命令；之后点一下就能在那里开终端并运行它。「非交互续聊」则不开终端，由 Hub 在主机上接着原对话运行一次。</template>
              <template v-else>
                点「启动」在预设的主机和目录里开一个终端会话并运行命令。会话在主机上运行，关掉这个窗口只是断开——在
                ExecMesh 里同样能看到它、接着用。「非交互续聊」预设点「运行」：不开终端，结果显示在预设下面。
              </template>
            </DialogDescription>
          </div>
          <button
            type="button"
            class="-mr-1 -mt-1 shrink-0 cursor-pointer rounded-sm p-1.5 text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
            aria-label="关闭"
            data-testid="launcher-close"
            @click="emit('update:open', false)"
          >
            <X class="size-4" />
          </button>
        </div>

        <p v-if="gateClosed" class="rounded-sm border border-amber-500/40 bg-amber-500/5 px-3 py-2 text-xs leading-relaxed text-amber-700 dark:text-amber-400" data-testid="launcher-gate-closed">
          终端功能当前没有开启（需要先设置登录密码），暂时不能启动会话。
        </p>

        <LauncherList
          v-if="view === 'list'"
          :launchers="launchers"
          :hosts="hosts"
          :hosts-loaded="hostsLoaded"
          :busy-id="busyId"
          :limits="limits"
          :guard="guardOk ? terminal.guard : null"
          :wake-busy-id="wakeBusyId"
          :permit-busy-id="permitBusyId"
          :focus-id="focusPresetId"
          :runs="terminal.launcherRuns"
          :run-busy-id="runBusyId"
          :others="terminal.launcherOthers"
          :cancel-busy-id="cancelBusyId"
          :auto-busy-id="autoBusyId"
          :scope="scope"
          :tab="listTab"
          :suggest="suggest"
          :suggest-reason="suggestReason"
          :suggest-busy="suggestBusy"
          @update:tab="(t: ListTab) => (listTab = t)"
          @create-suggested="createSuggested"
          @customize-suggested="customizeSuggested"
          @permit="toggleGuardPermit"
          @wake="wakePreset"
          @wake-headless="wakeHeadless"
          @start="startPreset"
          @run="runPreset"
          @open-thread="openThread"
          @cancel-run="cancelOther"
          @cancel-auto="cancelAuto"
          @resume="resumePreset"
          @edit="editPreset"
          @remove="removePreset"
          @create="newPreset"
          @create-cursor="newCursorPreset"
          @create-headless="newHeadlessPreset"
        />
        <LauncherForm
          v-else-if="draft"
          :draft="draft"
          :is-new="draftIsNew"
          :hosts="hosts"
          :others="others"
          :limits="limits"
          :saving="saving"
          :server-error="formError"
          :guard-supported="guardOk"
          :initial-guard="draftGuard"
          :guard-limits="gLimits"
          @save="savePreset"
          @cancel="view = 'list'"
        />
      </template>

      <!-- 终端 -->
      <template v-else-if="run">
        <div class="flex shrink-0 items-center gap-2 border-b border-border/70 bg-card px-3 py-2" data-testid="launcher-run-header">
          <span class="size-2 shrink-0 rounded-full" :class="dot.cls" :title="dot.label" :data-state="ended ? 'ended' : conn" data-testid="launcher-run-dot" />
          <div class="min-w-0 flex-1">
            <DialogTitle class="truncate text-[13px] font-semibold leading-tight" data-testid="launcher-run-title">{{ run.name }}</DialogTitle>
            <DialogDescription class="truncate font-mono text-[11px] leading-tight text-muted-foreground" :title="`${run.hostName}${run.dir ? ' · ' + run.dir : ''}`">
              {{ run.hostName }}<template v-if="run.dir"> · {{ run.dir }}</template><template v-if="!run.fresh"> · 已连回运行中的会话</template>
            </DialogDescription>
          </div>
          <Button type="button" variant="ghost" size="sm" class="h-7 gap-1 px-2 text-[11px]" title="回到预设清单（会话继续运行）" data-testid="launcher-back" @click="backToList">
            <ArrowLeft class="size-3.5" /><span class="max-sm:hidden">清单</span>
          </Button>
          <Button type="button" variant="ghost" size="sm" class="h-7 gap-1 px-2 text-[11px]" title="在 ExecMesh 里打开这个会话" data-testid="launcher-open-exec" @click="openInExec">
            <ExternalLink class="size-3.5" /><span class="max-sm:hidden">ExecMesh</span>
          </Button>
          <Button
            type="button"
            variant="ghost"
            size="sm"
            class="h-7 gap-1 px-2 text-[11px]"
            :class="confirmKill ? 'text-destructive' : 'text-muted-foreground hover:text-destructive'"
            :disabled="killing"
            :title="confirmKill ? '再点一次确认：结束会话，里面运行的程序会被终止' : '结束这个会话（里面运行的程序会被终止）'"
            data-testid="launcher-kill"
            @click="askKill"
          >
            <Loader2 v-if="killing" class="size-3.5 animate-spin" /><Power v-else class="size-3.5" />
            <span :class="confirmKill ? '' : 'max-sm:hidden'">{{ confirmKill ? '确认结束' : '结束' }}</span>
          </Button>
          <button
            type="button"
            class="shrink-0 cursor-pointer rounded-sm p-1.5 text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
            aria-label="关闭（会话继续在主机上运行）"
            title="关闭窗口（会话继续在主机上运行）"
            data-testid="launcher-close"
            @click="emit('update:open', false)"
          >
            <X class="size-4" />
          </button>
        </div>
        <LauncherTerminal
          :key="run.sid"
          :code="run.code"
          :sid="run.sid"
          :label="`${run.hostName} / ${run.name}`"
          :line="run.line"
          :submit-delay-ms="run.submitDelayMs"
          :active="open"
          :keybar="keybar"
          @state="onTermState"
          @close="backToList"
        />
      </template>
    </DialogContent>
  </Dialog>
</template>
