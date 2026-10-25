<script setup lang="ts">
// 分支面板（#236）：一个会话的分支关系一览与设置。
//   · 这个会话是主会话：列出它的分支（开放范围、最近活动、在等回复 / 未读，进行中的再加：智能体此刻的状态、还欠几项任务、
//     能不能被启动——#236 P4、能不能派任务——P5），可打开、可结束（结束时没做完的派单任务退回主会话，提示里写明），也能再新建；
//   · 这个会话是分支：显示它属于哪个主会话，改「向主会话开放多少」（三个预设，或逐项设置，含「启动」「任务」开关）与「抄送我说的话」，
//     看「启动方式」（开着没有、有没有续聊预设、去哪里设置）与「任务派单」（开着没有、是不是同一张任务板）、
//     看主会话的智能体改过它的哪些参数并能撤销（#236 P3），可结束分支；
//   · 没有任何关系：说明分支是什么，给一个「新建分支」的入口。
// 关系的数据都在分支仓库里（侧栏第一次挂载时取，实时流校准）；只有分支角色下的「参数记录」是打开面板时现取的。
// 放宽「改参数」档位或打开「启动」要终端解锁：先直接提交，服务端要求时弹出密码框再重试，取消就什么都不保存；
// 终端不可用时服务端把放宽的项压回原值、其余照存，这里把原因写给用户。撤销只要登录。
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { toast } from 'vue-sonner'
import { ArrowUpRight, ChevronDown, ChevronRight, Copy, GitBranch, Lock, Plus } from 'lucide-vue-next'
import { settleDialogHistory } from '../../composables/useDialogBackGesture'
import Dialog from '../ui/dialog/Dialog.vue'
import DialogContent from '../ui/dialog/DialogContent.vue'
import DialogDescription from '../ui/dialog/DialogDescription.vue'
import DialogTitle from '../ui/dialog/DialogTitle.vue'
import Button from '../ui/button/Button.vue'
import { branchErrorMessage, useBranchStore } from '../../stores/branches'
import { useAgentStateStore } from '../../stores/agentState'
import { useTerminalStore } from '../../stores/terminal'
import { UnlockCancelled } from '../../api/terminal'
import type { BranchBrief, BranchExpose, BranchParamEvent, BranchPreset, UpdateBranchRequest } from '../../api/branches'
import {
  BRANCH_PRESETS,
  BRANCH_UNLOCK_REASON,
  HISTORY_OPTIONS,
  MAX_ACTIVE_BRANCHES,
  PARAMS_LEVELS,
  PARAMS_UNLOCK_NOTE,
  branchAssignStatus,
  branchLaunchStatus,
  canCreateBranch,
  clampedNotice,
  exposeSummary,
  openTaskTotal,
  openTasksText,
  paramEventKind,
  paramEventTag,
  paramsLevelHint,
  presetLabel,
  returnedTasksText,
  widensExpose,
  type AssignKind,
  type LaunchKind,
} from '../../lib/branch'
import { dotToneClass, stateView, textToneClass, type AgentStateView } from '../../lib/agentState'
import { copyText } from '../../lib/terminal/clipboard'
import { relativeTime } from '../../lib/workItems'

const props = defineProps<{ open: boolean; workflowId: string }>()
const emit = defineEmits<{
  (e: 'update:open', val: boolean): void
  /** 要跳到另一个工作流的会话页（主会话 / 某个分支）：调用方负责切换。 */
  (e: 'open-workflow', workflowId: string): void
  (e: 'new-branch'): void
}>()

const branchStore = useBranchStore()
const terminalStore = useTerminalStore()
const agentStore = useAgentStateStore()

const asBranch = computed(() => branchStore.asBranch(props.workflowId))
const asMain = computed(() => branchStore.asMain(props.workflowId))
const activeMain = computed(() => asMain.value.filter(b => b.status === 'active'))
const role = computed<'branch' | 'main' | 'none'>(() => (asBranch.value ? 'branch' : asMain.value.length ? 'main' : 'none'))
const createCheck = computed(() => canCreateBranch(role.value, activeMain.value.length))

const busyKey = ref('')
const confirmKey = ref('')
let confirmTimer: ReturnType<typeof setTimeout> | undefined

watch(
  () => props.open,
  isOpen => {
    if (!isOpen) resetConfirm()
    // 任务数与启动方式（有没有续聊预设）没有自己的实时事件：每次打开重取一遍关系，面板上的数字才是此刻的
    else void branchStore.loadRelations()
  },
)
onBeforeUnmount(resetConfirm)

function resetConfirm() {
  confirmKey.value = ''
  if (confirmTimer) clearTimeout(confirmTimer)
  confirmTimer = undefined
}

function goto(workflowId: string) {
  emit('update:open', false)
  emit('open-workflow', workflowId)
}

async function startNew() {
  emit('update:open', false)
  // 面板关闭时要弹掉压在浏览器历史里的哨兵记录；等它走完再开「新建分支」，两个弹窗的回退才不会互相串（useDialogBackGesture）
  await nextTick()
  await settleDialogHistory()
  emit('new-branch')
}

/**
 * 保存一次对开放范围 / 抄送的修改。放宽「改参数」档位或打开「启动」要终端解锁：先直接提交，服务端要求时弹出密码框、成功后重试一次；
 * 用户在密码框里取消就静默放弃（什么都没保存）。终端不可用时服务端把放宽的项压回原值、其余照存，这里写明没放宽的原因。
 */
async function saveSettings(b: BranchBrief, kind: string, patch: UpdateBranchRequest, okText: string | null) {
  if (busyKey.value) return
  busyKey.value = `${kind}:${b.branch_workflow_id}`
  try {
    const res = await terminalStore.unlockOnDemand(() => branchStore.updateSettings(b.branch_workflow_id, patch), BRANCH_UNLOCK_REASON)
    const note = clampedNotice(res.clamped)
    if (note) toast.warning('部分设置没有放宽', { description: note, duration: 10000 })
    else if (okText) toast.success(okText)
  } catch (err) {
    if (err instanceof UnlockCancelled) return
    toast.error('没改成', { description: branchErrorMessage(err, '请稍后重试') })
  } finally {
    busyKey.value = ''
  }
}

function pickPreset(b: BranchBrief, id: BranchPreset) {
  if (busyKey.value || b.preset === id) return
  return saveSettings(b, 'preset', { expose_preset: id }, '已改开放范围')
}

function toggleCopy(b: BranchBrief, on: boolean) {
  return saveSettings(b, 'copy', { copy_user_notes: on }, null)
}

/** 逐项设置：只改这一项，其余项保持当前取值（服务端要求显式开放范围是完整对象）。 */
function setExpose(b: BranchBrief, change: Partial<BranchExpose>) {
  if (busyKey.value) return
  return saveSettings(b, 'expose', { expose: { ...b.expose, ...change } }, '已改开放范围')
}

function pickHistory(b: BranchBrief, id: BranchExpose['history']) {
  if (b.expose.history !== id) return setExpose(b, { history: id })
}

function pickParams(b: BranchBrief, level: BranchExpose['params']) {
  if (b.expose.params !== level) return setExpose(b, { params: level })
}

/** 这个档位相对当前是放宽（需要验证密码）吗：只用来在按钮上标一把小锁，真正的判断在服务端。 */
function levelNeedsUnlock(b: BranchBrief, level: BranchExpose['params']): boolean {
  return widensExpose(b.expose, { ...b.expose, params: level })
}

/**
 * 「启动」开关：打开要终端解锁。保存没成功（取消了解锁、终端不可用被压回、网络错误）时数据没变、画面不会重画，
 * 复选框得自己回到真实取值，不然画面上打着勾而服务端其实还是关的。
 */
async function toggleLaunch(b: BranchBrief, ev: Event) {
  const box = ev.target as HTMLInputElement
  await setExpose(b, { launch: box.checked })
  box.checked = !!branchStore.asBranch(props.workflowId)?.expose.launch
}

/**
 * 「任务」派单开关（#236 P5）：打开不放宽任何需要再验证密码的授权（只是让主会话把它自己建的任务放进这个分支的待办），所以只要登录。
 * 保存没成功时复选框同样要回到真实取值。
 */
async function toggleAssign(b: BranchBrief, ev: Event) {
  const box = ev.target as HTMLInputElement
  await setExpose(b, { tasks: box.checked ? 'assign' : 'read' })
  box.checked = branchStore.asBranch(props.workflowId)?.expose.tasks === 'assign'
}

// ---------- 状态：Agent 状态点、任务数、启动方式（#236 P4）----------

/**
 * 分支的智能体此刻的状态：用状态 store 的实时数据（与侧栏小圆点、页头状态芯片同一来源，智能体一动就刷新）；
 * 关系已结束、还没有任何会话或状态未知时不画。服务端 BranchBrief.agent_state 是给 MCP 这类没有实时流的消费者的同口径快照，这里不用。
 */
function stateOf(b: BranchBrief): AgentStateView {
  return stateView(agentStore.entryFor(b.branch_workflow_id).primary)
}

function stateShown(b: BranchBrief): boolean {
  return b.status === 'active' && stateOf(b).key !== 'unknown'
}

const LAUNCH_TONE: Readonly<Record<LaunchKind, string>> = {
  ready: 'border-emerald-500/50 text-emerald-600 dark:text-emerald-400',
  'no-preset': 'border-amber-500/50 text-amber-600 dark:text-amber-400',
  off: 'border-border text-muted-foreground',
  unknown: 'border-border text-muted-foreground',
}

function launchOf(b: BranchBrief) {
  return branchLaunchStatus(b)
}

/** 分支视角的「启动方式」说明（只在关系活动时有）。 */
const ownLaunch = computed(() => (asBranch.value && asBranch.value.status === 'active' ? branchLaunchStatus(asBranch.value) : null))

// ---------- 任务派单（#236 P5）----------

const ASSIGN_TONE: Readonly<Record<AssignKind, string>> = {
  ready: 'border-emerald-500/50 text-emerald-600 dark:text-emerald-400',
  unshared: 'border-amber-500/50 text-amber-600 dark:text-amber-400',
  off: 'border-border text-muted-foreground',
  unknown: 'border-border text-muted-foreground',
}

function assignOf(b: BranchBrief) {
  return branchAssignStatus(b)
}

/** 分支视角的「任务派单」说明（只在关系活动时有）。 */
const ownAssign = computed(() => (asBranch.value && asBranch.value.status === 'active' ? branchAssignStatus(asBranch.value) : null))

const detailOpen = ref(false)

// ---------- 主会话改过的参数（#236 P3）----------

/** 面板里最多列最近这么多条参数记录。 */
const PARAM_EVENTS_SHOWN = 10
const paramEvents = ref<BranchParamEvent[]>([])
let eventsSeq = 0

/** 打开面板时（以及主会话改了参数 / 自己撤销之后）重取这个分支的参数记录；读不到不影响面板本身。 */
async function loadEvents() {
  const b = asBranch.value
  if (!props.open || !b) return
  const mine = ++eventsSeq
  try {
    const list = await branchStore.loadParamEvents(b.branch_workflow_id, PARAM_EVENTS_SHOWN)
    if (mine === eventsSeq) paramEvents.value = list
  } catch {
    // 只是记录读不到：沿用上一次的内容，下一次打开 / 刷新会再取
  }
}

watch(
  () => [props.open, asBranch.value?.branch_workflow_id ?? ''] as const,
  ([, branchId], prev) => {
    if (branchId !== (prev?.[1] ?? '')) paramEvents.value = []
    void loadEvents()
  },
  { immediate: true },
)
watch(
  () => branchStore.refreshTick,
  () => void loadEvents(),
)

/** 撤销主会话的一次修改（只要登录）。不能撤销时服务端写明原因（之后又改过、当前值已变……），原样告诉用户。 */
async function revertEvent(ev: BranchParamEvent) {
  const b = asBranch.value
  if (!b || busyKey.value) return
  busyKey.value = `revert:${ev.id}`
  try {
    await branchStore.revertParamEvent(b.branch_workflow_id, ev.id)
    toast.success('已撤销', { description: ev.summary })
  } catch (err) {
    toast.error('没能撤销', { description: branchErrorMessage(err, '请稍后重试') })
  } finally {
    busyKey.value = ''
    void loadEvents() // 不论成败都重取：失败多半是状态已经变了（别处撤销了、又改过）
  }
}

function eventTagClass(ev: BranchParamEvent): string {
  const k = paramEventKind(ev)
  if (k === 'agent') return 'border-primary/40 text-primary'
  if (k === 'revert') return 'border-sky-500/50 text-sky-600 dark:text-sky-400'
  return 'border-border text-muted-foreground'
}

/** 复制分支的工作流编号：主会话的智能体按编号指到某个分支（session_params 等工具）。 */
async function copyWorkflowId(id: string) {
  if (await copyText(id)) toast.success('已复制编号')
  else toast.error('复制失败，请手动选中编号复制')
}

/** 结束分支是两下点击：第一下按钮变成「确认结束」（4 秒内有效），第二下才真正结束。 */
async function endBranch(b: BranchBrief) {
  const key = `close:${b.branch_workflow_id}`
  if (confirmKey.value !== key) {
    resetConfirm()
    confirmKey.value = key
    confirmTimer = setTimeout(resetConfirm, 4000)
    return
  }
  resetConfirm()
  if (busyKey.value) return
  busyKey.value = key
  try {
    const { returnedTasks } = await branchStore.closeBranch(b.branch_workflow_id)
    const returned = returnedTasksText(returnedTasks)
    toast.success('分支已结束', { description: `「${b.title || b.branch_workflow_id}」的会话与历史都保留着${returned ? `；${returned}` : ''}` })
  } catch (err) {
    toast.error('没能结束分支', { description: branchErrorMessage(err, '请稍后重试') })
  } finally {
    busyKey.value = ''
  }
}

function endLabel(b: BranchBrief): string {
  return confirmKey.value === `close:${b.branch_workflow_id}` ? '确认结束' : '结束分支'
}

function activityText(b: BranchBrief): string {
  if (b.missing) return '会话已不存在'
  const t = relativeTime(b.last_activity_at)
  return t ? `最近活动 ${t}` : '还没有动静'
}

function createdByText(by: string): string {
  if (by === 'human') return '你建立'
  if (by === 'migrate') return '迁移命令建立'
  if (by.startsWith('agent:')) return '主会话的智能体建立'
  return by
}

const chipClass = 'inline-flex items-center rounded-sm border px-1.5 text-[10px] leading-4'
const labelClass = 'text-[11px] font-bold text-muted-foreground uppercase tracking-wider'
const optionClass =
  'rounded-sm border px-2 py-1.5 text-[11px] text-center focus:outline-none focus-visible:ring-1 focus-visible:ring-primary/50 disabled:opacity-60'
const optionOn = 'border-primary bg-primary/10 text-foreground font-bold'
const optionOff = 'border-border/80 bg-background text-muted-foreground hover:text-foreground'
</script>

<template>
  <Dialog :open="open" @update:open="emit('update:open', $event)">
    <DialogContent
      class="sm:max-w-lg p-4 bg-card border border-border rounded-md shadow-modal font-mono max-h-[92vh] overflow-y-auto"
      data-testid="branch-panel"
    >
      <DialogTitle class="flex items-center gap-2 text-sm font-bold text-foreground">
        <GitBranch class="w-4 h-4 text-primary shrink-0" />
        <span>分支会话</span>
      </DialogTitle>

      <!-- 这个会话是分支 -->
      <template v-if="role === 'branch' && asBranch">
        <DialogDescription class="text-[11px] text-muted-foreground leading-relaxed">
          这个会话是主会话「{{ asBranch.parent_title || asBranch.parent_workflow_id }}」的一个分支：独立的会话，自己的页面与智能体；
          你在这里说的话不会被主会话直接看到（除非下面开了抄送）。
        </DialogDescription>

        <div class="space-y-3" data-testid="branch-panel-as-branch">
          <div class="flex flex-wrap items-center gap-1.5 text-[11px]">
            <span :class="[chipClass, asBranch.status === 'active' ? 'border-primary/40 text-primary' : 'border-border text-muted-foreground']" data-testid="branch-panel-status">
              {{ asBranch.status === 'active' ? '进行中' : '已结束' }}
            </span>
            <span class="text-muted-foreground">{{ createdByText(asBranch.created_by) }}</span>
            <Button
              type="button"
              variant="outline"
              size="sm"
              class="ml-auto h-7 gap-1 text-[11px]"
              data-testid="branch-panel-open-parent"
              @click="goto(asBranch.parent_workflow_id)"
            >
              <ArrowUpRight class="w-3.5 h-3.5" />
              打开主会话
            </Button>
          </div>

          <div class="flex items-center gap-1.5 text-[10px] text-muted-foreground" data-testid="branch-panel-id-row">
            <span class="shrink-0">分支编号</span>
            <code class="min-w-0 flex-1 truncate" :title="asBranch.branch_workflow_id" data-testid="branch-panel-wfid">{{ asBranch.branch_workflow_id }}</code>
            <button
              type="button"
              class="inline-flex shrink-0 items-center gap-1 rounded-sm border border-border/70 px-1.5 py-0.5 hover:text-foreground focus:outline-none focus-visible:ring-1 focus-visible:ring-primary/50"
              data-testid="branch-panel-copy-id"
              @click="copyWorkflowId(asBranch.branch_workflow_id)"
            >
              <Copy class="w-3 h-3" />
              复制
            </button>
          </div>

          <template v-if="asBranch.status === 'active'">
            <div class="space-y-1.5">
              <span class="text-[11px] font-bold text-muted-foreground uppercase tracking-wider" id="branch-panel-preset-label">向主会话开放多少</span>
              <div class="grid grid-cols-3 gap-1.5" role="radiogroup" aria-labelledby="branch-panel-preset-label">
                <button
                  v-for="p in BRANCH_PRESETS"
                  :key="p.id"
                  type="button"
                  role="radio"
                  :aria-checked="asBranch.preset === p.id"
                  class="rounded-sm border px-2 py-1.5 text-[11px] text-center focus:outline-none focus-visible:ring-1 focus-visible:ring-primary/50 disabled:opacity-60"
                  :class="asBranch.preset === p.id
                    ? 'border-primary bg-primary/10 text-foreground font-bold'
                    : 'border-border/80 bg-background text-muted-foreground hover:text-foreground'"
                  :data-testid="`branch-panel-preset-${p.id}`"
                  :disabled="!!busyKey"
                  @click="pickPreset(asBranch, p.id)"
                >{{ p.label.replace('（推荐）', '') }}</button>
              </div>
              <p class="text-[10px] text-muted-foreground leading-normal" data-testid="branch-panel-expose">
                当前：{{ presetLabel(asBranch) }} · {{ exposeSummary(asBranch.expose) }}。分支给主会话发消息恒允许。
              </p>
              <p class="text-[10px] text-muted-foreground leading-normal" data-testid="branch-panel-unlock-note">{{ PARAMS_UNLOCK_NOTE }}</p>
            </div>

            <!-- 启动方式：主会话的智能体能不能唤醒这个分支（「启动」开着没有 + 有没有续聊预设），以及下一步去哪里设置 -->
            <div v-if="ownLaunch" class="space-y-1" data-testid="branch-panel-launch-block">
              <span :class="labelClass">启动方式</span>
              <div class="flex flex-wrap items-center gap-1.5">
                <span
                  :class="[chipClass, LAUNCH_TONE[ownLaunch.kind]]"
                  :data-kind="ownLaunch.kind"
                  data-testid="branch-panel-launch-status"
                >{{ ownLaunch.label }}</span>
              </div>
              <p class="text-[10px] text-muted-foreground leading-normal" data-testid="branch-panel-launch-hint">{{ ownLaunch.hint }}</p>
            </div>

            <!-- 任务派单：主会话的智能体能不能把它自己建的任务派给这个分支（「任务」开着没有 + 两边是不是同一张任务板），以及下一步 -->
            <div v-if="ownAssign" class="space-y-1" data-testid="branch-panel-assign-block">
              <span :class="labelClass">任务派单</span>
              <div class="flex flex-wrap items-center gap-1.5">
                <span
                  :class="[chipClass, ASSIGN_TONE[ownAssign.kind]]"
                  :data-kind="ownAssign.kind"
                  data-testid="branch-panel-assign-status"
                >{{ ownAssign.label }}</span>
              </div>
              <p class="text-[10px] text-muted-foreground leading-normal" data-testid="branch-panel-assign-hint">{{ ownAssign.hint }}</p>
            </div>

            <!-- 逐项设置：预设之外的组合，默认收起 -->
            <div class="rounded-sm border border-border/60">
              <button
                type="button"
                class="flex w-full items-center gap-1.5 px-2.5 py-1.5 text-left text-[11px] font-bold text-muted-foreground hover:text-foreground focus:outline-none focus-visible:ring-1 focus-visible:ring-primary/50"
                data-testid="branch-panel-detail-toggle"
                :aria-expanded="detailOpen"
                aria-controls="branch-panel-detail"
                @click="detailOpen = !detailOpen"
              >
                <ChevronDown v-if="detailOpen" class="w-3.5 h-3.5 shrink-0" />
                <ChevronRight v-else class="w-3.5 h-3.5 shrink-0" />
                <span>逐项设置</span>
                <span class="font-normal text-muted-foreground/80">（预设之外的组合）</span>
              </button>

              <div v-if="detailOpen" id="branch-panel-detail" class="space-y-3 border-t border-border/50 px-2.5 py-2.5" data-testid="branch-panel-detail">
                <div class="space-y-1.5">
                  <span :class="labelClass" id="branch-panel-history-label">主会话读历史</span>
                  <div class="grid grid-cols-3 gap-1.5" role="radiogroup" aria-labelledby="branch-panel-history-label">
                    <button
                      v-for="h in HISTORY_OPTIONS"
                      :key="h.id"
                      type="button"
                      role="radio"
                      :aria-checked="asBranch.expose.history === h.id"
                      :title="h.hint"
                      :class="[optionClass, asBranch.expose.history === h.id ? optionOn : optionOff]"
                      :data-testid="`branch-panel-history-${h.id}`"
                      :disabled="!!busyKey"
                      @click="pickHistory(asBranch, h.id)"
                    >{{ h.label }}</button>
                  </div>
                </div>

                <label class="flex items-start gap-2 text-[11px] leading-relaxed cursor-pointer">
                  <input
                    type="checkbox"
                    class="mt-0.5 size-3.5 shrink-0 accent-[var(--color-primary)]"
                    :checked="asBranch.expose.say"
                    :disabled="!!busyKey"
                    data-testid="branch-panel-say"
                    @change="setExpose(asBranch, { say: ($event.target as HTMLInputElement).checked })"
                  />
                  <span class="min-w-0">
                    <span class="font-bold text-foreground">主会话可以给分支发消息</span>
                    <span class="block text-muted-foreground">关掉后主会话的智能体不能再给这个分支发消息；分支给主会话发消息不受影响。</span>
                  </span>
                </label>

                <label class="flex items-start gap-2 text-[11px] leading-relaxed cursor-pointer">
                  <input
                    type="checkbox"
                    class="mt-0.5 size-3.5 shrink-0 accent-[var(--color-primary)]"
                    :checked="asBranch.expose.decide"
                    :disabled="!!busyKey"
                    data-testid="branch-panel-decide"
                    @change="setExpose(asBranch, { decide: ($event.target as HTMLInputElement).checked })"
                  />
                  <span class="min-w-0">
                    <span class="font-bold text-foreground">主会话可以替分支定夺请示</span>
                    <span class="block text-muted-foreground">关掉后分支的请示不先交给主会话的智能体，直接由你定夺。</span>
                  </span>
                </label>

                <div class="space-y-1.5">
                  <span :class="labelClass" id="branch-panel-params-label">主会话改参数</span>
                  <div class="grid grid-cols-2 gap-1.5 sm:grid-cols-4" role="radiogroup" aria-labelledby="branch-panel-params-label">
                    <button
                      v-for="l in PARAMS_LEVELS"
                      :key="l.id"
                      type="button"
                      role="radio"
                      :aria-checked="asBranch.expose.params === l.id"
                      :class="[optionClass, 'inline-flex items-center justify-center gap-1', asBranch.expose.params === l.id ? optionOn : optionOff]"
                      :data-testid="`branch-panel-params-${l.id}`"
                      :disabled="!!busyKey"
                      @click="pickParams(asBranch, l.id)"
                    >
                      <span>{{ l.label }}</span>
                      <Lock v-if="levelNeedsUnlock(asBranch, l.id)" class="w-3 h-3 shrink-0 text-muted-foreground" aria-label="需要再次验证密码" />
                    </button>
                  </div>
                  <p class="text-[10px] text-muted-foreground leading-normal" data-testid="branch-panel-params-hint">
                    {{ paramsLevelHint(asBranch.expose.params) }}
                  </p>
                </div>

                <label class="flex items-start gap-2 text-[11px] leading-relaxed cursor-pointer">
                  <input
                    type="checkbox"
                    class="mt-0.5 size-3.5 shrink-0 accent-[var(--color-primary)]"
                    :checked="asBranch.expose.launch"
                    :disabled="!!busyKey"
                    data-testid="branch-panel-launch"
                    @change="toggleLaunch(asBranch, $event)"
                  />
                  <span class="min-w-0">
                    <span class="inline-flex items-center gap-1 font-bold text-foreground">
                      主会话可以启动这个分支
                      <Lock v-if="!asBranch.expose.launch" class="w-3 h-3 shrink-0 text-muted-foreground" aria-label="打开要再次验证密码" data-testid="branch-panel-launch-lock" />
                    </span>
                    <span class="block text-muted-foreground">
                      打开后，主会话的智能体可以用「运行预设」唤醒这个分支（要先有指向它的续聊预设）。关掉随时生效，主会话下一次调用立刻被拒；已经提交的运行不会被撤销。
                    </span>
                  </span>
                </label>

                <label class="flex items-start gap-2 text-[11px] leading-relaxed cursor-pointer">
                  <input
                    type="checkbox"
                    class="mt-0.5 size-3.5 shrink-0 accent-[var(--color-primary)]"
                    :checked="asBranch.expose.tasks === 'assign'"
                    :disabled="!!busyKey"
                    data-testid="branch-panel-assign"
                    @change="toggleAssign(asBranch, $event)"
                  />
                  <span class="min-w-0">
                    <span class="font-bold text-foreground">主会话可以给分支派任务</span>
                    <span class="block text-muted-foreground">
                      打开后，主会话的智能体可以把它自己建的任务指派给这个分支（要两边在同一个项目的任务板上）；分支接取、交付后由主会话验收。派单只是放进分支的待办，不会主动唤醒它。
                      关掉随时生效，已经派出的任务保留；结束分支时没做完的任务会退回主会话。不需要再验证密码。
                    </span>
                  </span>
                </label>
              </div>
            </div>

            <label class="flex items-start gap-2 rounded-sm border border-border/60 px-2.5 py-2 text-[11px] leading-relaxed cursor-pointer">
              <input
                type="checkbox"
                class="mt-0.5 size-3.5 shrink-0 accent-[var(--color-primary)]"
                :checked="asBranch.copy_user_notes"
                :disabled="!!busyKey"
                data-testid="branch-panel-copy-notes"
                @change="toggleCopy(asBranch, ($event.target as HTMLInputElement).checked)"
              />
              <span class="min-w-0">
                <span class="font-bold text-foreground">把我在这里说的话抄送给主会话</span>
                <span class="block text-muted-foreground">只抄送一行摘要；关掉后主会话看不到你在分支里说了什么。</span>
              </span>
            </label>

          </template>
          <p v-else class="text-[11px] text-muted-foreground leading-relaxed" data-testid="branch-panel-closed-note">
            这个分支已经结束：两边不能再互发消息，主会话不能再读它的历史；会话与历史都保留着。
          </p>

          <!-- 主会话改过的参数：最近几条，能撤销的带「撤销」（只要登录，分支结束后也能撤销） -->
          <div v-if="paramEvents.length" class="space-y-1.5" data-testid="branch-panel-param-events">
            <span :class="labelClass">主会话改过的参数</span>
            <ul class="space-y-1">
              <li
                v-for="ev in paramEvents"
                :key="ev.id"
                class="rounded-sm border border-border/60 px-2 py-1.5 text-[11px]"
                :class="paramEventKind(ev) === 'reverted' ? 'opacity-70' : ''"
                :data-testid="`branch-panel-event-${ev.id}`"
                :data-kind="paramEventKind(ev)"
                :data-field="ev.field"
              >
                <div class="flex items-start gap-1.5">
                  <span :class="[chipClass, 'mt-px shrink-0', eventTagClass(ev)]">{{ paramEventTag(ev) }}</span>
                  <span class="min-w-0 flex-1 break-words text-foreground">{{ ev.summary }}</span>
                  <Button
                    v-if="ev.revertable"
                    type="button"
                    variant="outline"
                    size="sm"
                    class="h-6 shrink-0 px-2 text-[11px]"
                    :disabled="!!busyKey"
                    :data-testid="`branch-panel-revert-${ev.id}`"
                    @click="revertEvent(ev)"
                  >撤销</Button>
                </div>
                <p class="mt-0.5 text-[10px] text-muted-foreground leading-normal">
                  {{ relativeTime(ev.created_at) }}
                  <template v-if="ev.revert_blocked && !ev.reverted_at"> · 暂时不能撤销：{{ ev.revert_blocked }}</template>
                </p>
              </li>
            </ul>
            <p v-if="paramEvents.length >= PARAM_EVENTS_SHOWN" class="text-[10px] text-muted-foreground leading-normal">只列出最近 {{ PARAM_EVENTS_SHOWN }} 条。</p>
          </div>

          <div v-if="asBranch.status === 'active'" class="flex items-center justify-between gap-2 border-t border-border/50 pt-2">
            <p class="min-w-0 text-[10px] text-muted-foreground leading-normal">
              结束后两边的智能体不能再互发消息，主会话不能再读它的历史；会话与历史都保留，你仍可以在两边查看。
            </p>
            <Button
              type="button"
              variant="outline"
              size="sm"
              class="shrink-0 text-[11px]"
              :class="confirmKey === `close:${asBranch.branch_workflow_id}` ? 'border-destructive text-destructive' : ''"
              :disabled="!!busyKey"
              data-testid="branch-panel-close"
              @click="endBranch(asBranch)"
            >{{ endLabel(asBranch) }}</Button>
          </div>
        </div>
      </template>

      <!-- 这个会话是主会话 -->
      <template v-else-if="role === 'main'">
        <DialogDescription class="text-[11px] text-muted-foreground leading-relaxed">
          这个会话有 {{ activeMain.length }} 个进行中的分支（最多 {{ MAX_ACTIVE_BRANCHES }} 个）。分支是独立的会话，各有自己的页面与智能体；
          分支里的消息会出现在这个会话的对话里。
        </DialogDescription>

        <div class="space-y-3" data-testid="branch-panel-as-main">
          <ul class="space-y-1.5">
            <li
              v-for="b in asMain"
              :key="b.branch_workflow_id"
              class="rounded-sm border border-border/60 px-2.5 py-2 text-[11px]"
              :class="b.status === 'closed' ? 'opacity-70' : ''"
              :data-testid="`branch-panel-row-${b.branch_workflow_id}`"
            >
              <div class="flex items-center gap-1.5">
                <span class="min-w-0 flex-1 truncate font-bold text-foreground" :title="b.title">{{ b.title || b.branch_workflow_id }}</span>
                <span v-if="b.status === 'closed'" :class="[chipClass, 'border-border text-muted-foreground']">已结束</span>
                <span v-if="b.waiting" :class="[chipClass, 'border-amber-500/50 text-amber-600 dark:text-amber-400']" title="分支有一轮汇报在等你回复">等你回复</span>
                <span v-if="b.unread_to_main" :class="[chipClass, 'border-sky-500/50 text-sky-600 dark:text-sky-400']" title="分支发给主会话、主会话的智能体还没读的消息">
                  {{ b.unread_to_main }} 条未读
                </span>
              </div>
              <p class="mt-0.5 text-[10px] text-muted-foreground leading-normal">
                {{ presetLabel(b) }} · {{ exposeSummary(b.expose) }} · {{ activityText(b) }}
              </p>
              <!-- 状态（只给进行中的分支）：智能体此刻在做什么、还欠几项任务、能不能被主会话启动 -->
              <div
                v-if="b.status === 'active'"
                class="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-[10px]"
                :data-testid="`branch-panel-status-${b.branch_workflow_id}`"
              >
                <span
                  v-if="stateShown(b)"
                  class="inline-flex items-center gap-1"
                  :title="stateOf(b).title"
                  :data-state="stateOf(b).key"
                  :data-testid="`branch-panel-state-${b.branch_workflow_id}`"
                >
                  <span class="inline-block h-1.5 w-1.5 shrink-0 rounded-full" :class="dotToneClass(stateOf(b).tone, stateOf(b).pulse)"></span>
                  <span class="font-medium" :class="textToneClass(stateOf(b).tone)">{{ stateOf(b).text }}</span>
                </span>
                <span
                  v-if="openTaskTotal(b.tasks)"
                  class="text-muted-foreground"
                  title="归属这个分支、还没做完的任务"
                  :data-testid="`branch-panel-tasks-${b.branch_workflow_id}`"
                >{{ openTasksText(b.tasks) }}</span>
                <span
                  :class="[chipClass, LAUNCH_TONE[launchOf(b).kind]]"
                  :title="launchOf(b).hint"
                  :data-kind="launchOf(b).kind"
                  :data-testid="`branch-panel-launch-chip-${b.branch_workflow_id}`"
                >{{ launchOf(b).label }}</span>
                <span
                  :class="[chipClass, ASSIGN_TONE[assignOf(b).kind]]"
                  :title="assignOf(b).hint"
                  :data-kind="assignOf(b).kind"
                  :data-testid="`branch-panel-assign-chip-${b.branch_workflow_id}`"
                >{{ assignOf(b).label }}</span>
              </div>
              <div class="mt-1.5 flex items-center justify-end gap-1.5">
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  class="h-6 gap-1 px-2 text-[11px]"
                  :title="`复制分支编号 ${b.branch_workflow_id}`"
                  :data-testid="`branch-panel-copy-id-${b.branch_workflow_id}`"
                  @click="copyWorkflowId(b.branch_workflow_id)"
                >
                  <Copy class="w-3 h-3" />
                  编号
                </Button>
                <Button
                  v-if="b.status === 'active'"
                  type="button"
                  variant="ghost"
                  size="sm"
                  class="h-6 px-2 text-[11px]"
                  :class="confirmKey === `close:${b.branch_workflow_id}` ? 'text-destructive' : ''"
                  :disabled="!!busyKey"
                  :data-testid="`branch-panel-close-${b.branch_workflow_id}`"
                  @click="endBranch(b)"
                >{{ endLabel(b) }}</Button>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  class="h-6 gap-1 px-2 text-[11px]"
                  :data-testid="`branch-panel-open-${b.branch_workflow_id}`"
                  @click="goto(b.branch_workflow_id)"
                >
                  <ArrowUpRight class="w-3 h-3" />
                  打开
                </Button>
              </div>
            </li>
          </ul>

          <div class="flex items-center justify-between gap-2 border-t border-border/50 pt-2">
            <p v-if="!createCheck.ok" class="min-w-0 text-[10px] text-muted-foreground leading-normal" data-testid="branch-panel-new-reason">{{ createCheck.reason }}</p>
            <span v-else class="flex-1" />
            <Button
              type="button"
              size="sm"
              class="shrink-0 gap-1 text-xs"
              :disabled="!createCheck.ok"
              data-testid="branch-panel-new"
              @click="startNew"
            >
              <Plus class="w-3.5 h-3.5" />
              新建分支
            </Button>
          </div>
        </div>
      </template>

      <!-- 没有任何关系 -->
      <template v-else>
        <DialogDescription class="text-[11px] text-muted-foreground leading-relaxed">
          分支会话：把这个会话里的一块工作拆出去，交给另一个智能体单独做。分支是独立的会话（自己的页面、收件箱与智能体），
          只多一条「属于哪个主会话」的关系；两边的智能体只能通过受控的通道互相说话，你在两边都能看到。
        </DialogDescription>
        <div class="flex items-center justify-end gap-2 border-t border-border/50 pt-2" data-testid="branch-panel-as-none">
          <Button type="button" size="sm" class="gap-1 text-xs" data-testid="branch-panel-new" @click="startNew">
            <Plus class="w-3.5 h-3.5" />
            新建分支
          </Button>
        </div>
      </template>
    </DialogContent>
  </Dialog>
</template>
