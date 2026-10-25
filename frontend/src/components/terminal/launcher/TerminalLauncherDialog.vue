<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { toast } from 'vue-sonner'
import { SquareTerminal, X } from 'lucide-vue-next'
import { Dialog, DialogContent, DialogDescription, DialogTitle } from '../../ui/dialog'
import LauncherList from './LauncherList.vue'
import LauncherForm from './LauncherForm.vue'
import { useTerminalStore } from '../../../stores/terminal'
import { useSessionLaunchersStore } from '../../../stores/sessionLaunchers'
import { toApiError, UnlockCancelled, type HostView, type LauncherOtherRun, type LauncherRunView, type TerminalLauncher } from '../../../api/terminal'
import { draftOf, headlessDraft, launcherHosts, launcherLimits, type LauncherDraft } from '../../../lib/terminal/launcher'
import { runNotice, runReadiness } from '../../../lib/terminal/launcherRun'
import { projectScope, suggestDraft, suggestLauncher, type ListTab, type TabChoice } from '../../../lib/terminal/sessionLaunchers'
import { execThreadPath } from '../../../lib/mode'
import { settleDialogHistory } from '../../../composables/useDialogBackGesture'

/**
 * 终端启动器：会话页顶栏「终端」按钮打开的弹窗。现在只管「非交互续聊」预设（#126）：
 *   list  预设清单（运行 / 编辑 / 删除，加上「其他运行」）；
 *   edit  新建或编辑一条预设（只能是非交互续聊）。
 * 点「运行」由服务端经主机的执行通道提交一次 `agent -p --resume …`，结果（状态、用时、最后一条回复）显示在清单里；
 * 浏览器什么都不用执行，也不开终端。
 *
 * 旧版的交互式「开终端」预设已经停用（#245）：不能再新建、启动、编辑，清单里只留删除；要在主机上开终端，用 ExecMesh 的网页终端。
 *
 * 从会话页打开时带着这个会话的工作流编号（#140）：清单分「本项目 / 全部」，本项目里没有对应的续聊预设时，
 * 顶上给一张预填好的「为这个会话创建续聊预设」建议卡（数据来自 GET /terminal/launchers/for-session）。
 */
const props = defineProps<{ open: boolean; workflowId?: string }>()
const emit = defineEmits<{ (e: 'update:open', v: boolean): void }>()

const terminal = useTerminalStore()
const sessionLaunchers = useSessionLaunchersStore()
const router = useRouter()

type View = 'list' | 'edit'
const view = ref<View>('list')

const limits = computed(() => launcherLimits(terminal.prefs?.limits))
const launchers = computed(() => terminal.launchers)
const hosts = computed<HostView[]>(() => terminal.hosts)

// ---------- 清单 ----------
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
  if (runBusyId.value) return
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
  if (!s || s.needs_host || suggestBusy.value) return
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
  draftIsNew.value = true
  formError.value = ''
  view.value = 'edit'
}

// ---------- 编辑 ----------
const draft = ref<LauncherDraft | null>(null)
const draftIsNew = ref(true)
const saving = ref(false)
const formError = ref('')

const defaultHost = () => launcherHosts(hosts.value)[0]?.machine_code ?? ''

function newHeadlessPreset() {
  draft.value = headlessDraft(defaultHost())
  draftIsNew.value = true
  formError.value = ''
  view.value = 'edit'
}

function editPreset(l: TerminalLauncher) {
  draft.value = draftOf(l)
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
    // 以服务端保存下来的那份为准（名字被规整过），别用表单里的草稿运行。
    const saved = terminal.launchers.find(x => x.id === l.id) ?? l
    await runPreset(saved)
  } else {
    toast.success(draftIsNew.value ? `已保存预设「${l.name}」` : `已更新预设「${l.name}」`)
  }
}

/** 清单里点「运行记录」：关掉弹窗，到 ExecMesh 的模型线程里看这次运行的完整输出。 */
async function openThread(key: string) {
  emit('update:open', false)
  await nextTick()
  await settleDialogHistory()
  await router.push(execThreadPath(key))
}

// ---------- 弹窗行为 ----------
/**
 * 编辑表单里 Esc 回到清单（不丢弃清单，也不是整个关掉）；点外面不关，免得填了一半的内容丢了。
 */
function onEscape(e: KeyboardEvent) {
  if (view.value === 'edit') {
    e.preventDefault()
    if (!saving.value) view.value = 'list'
  }
}
function onOutside(e: Event) {
  if (view.value !== 'list') e.preventDefault()
}

// 弹窗内容是个 grid：子项默认的最小宽度是内容宽度，一条很长的目录 / 命令会把整列撑出弹窗（右边的按钮被挤出屏幕）。
const contentClass = 'max-w-xl gap-3 p-5 [&>*]:min-w-0'

const hostsLoaded = computed(() => terminal.hostsLoaded)
const gateClosed = computed(() => terminal.status !== null && !terminal.gateOpen)

// ---------- 打开 / 关闭 ----------
// 弹窗开着期间轮询主机与总闸状态：「在线 / 离线」「已在运行」要跟着实际情况走；终端里有操作时它也负责给解锁令牌保活。
// 注意要放在上面所有状态声明之后：immediate 的回调在声明处立刻执行，会读到后面才声明的变量。
let polling = false
let runPolling = false
// 会话对应的预设视图（#140）：开着期间按它自己的节奏读（有运行在进行时勤一点）；记下读的是哪个工作流，换会话 / 关窗时才能对称地停掉。
let sessionPolled = ''
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
      // 非交互续聊的运行现状：开着弹窗期间轮询（有运行在进行时勤一点）。
      if (!runPolling) {
        terminal.startLauncherRunPolling()
        runPolling = true
      }
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
    stopSessionPolling()
    listTab.value = 'auto'
    suggestBusy.value = false
    // 关掉弹窗：下次打开回到清单。
    view.value = 'list'
    draft.value = null
    formError.value = ''
    runBusyId.value = ''
    cancelBusyId.value = ''
  },
  { immediate: true },
)
// 回到清单（保存 / 删除预设之后）：「本项目」里有哪些、还要不要给建议都变了，马上读新，不等下一轮。
watch(view, v => {
  if (v === 'list' && props.open && props.workflowId) void sessionLaunchers.refresh(props.workflowId)
})
onBeforeUnmount(() => {
  if (polling) terminal.stopPolling()
  if (runPolling) terminal.stopLauncherRunPolling()
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
      <div class="flex items-start gap-2">
        <div class="min-w-0 flex-1 space-y-1">
          <DialogTitle class="flex items-center gap-2 text-sm font-semibold">
            <SquareTerminal class="size-4 text-primary" aria-hidden="true" />
            {{ view === 'edit' ? (draftIsNew ? '新建续聊预设' : '编辑续聊预设') : '续聊预设' }}
          </DialogTitle>
          <DialogDescription class="text-xs leading-relaxed text-muted-foreground">
            <template v-if="view === 'edit'">选一台主机和一个目录，写明接着哪个对话；之后点「运行」，由 Hub 在那台主机上接着原对话运行一次，不开终端。</template>
            <template v-else>
              「非交互续聊」预设点「运行」：由 Hub 在预设的主机和目录里接着原对话运行一次，不开终端，结果显示在预设下面。
              旧版的交互式「开终端」预设已停用，只能删除；要在主机上开终端，请直接用 ExecMesh 的网页终端。
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
        :limits="limits"
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
        @run="runPreset"
        @open-thread="openThread"
        @cancel-run="cancelOther"
        @cancel-auto="cancelAuto"
        @edit="editPreset"
        @remove="removePreset"
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
        @save="savePreset"
        @cancel="view = 'list'"
      />
    </DialogContent>
  </Dialog>
</template>
