<script setup lang="ts">
// 任务详情：桌面（≥768px）是居中的弹窗，手机保持原来的右侧抽屉；两者共用同一份内容（createReusableTemplate）。
//
// 内容的取舍：打开它是为了「看懂这是什么任务、做到哪了、验证过没有」，所以首屏只放这几样——
//   内容（描述）→ 状态 / 阶段 → 当前进度与结论 → 批次 → 最近进展；
// 改名、所属模块、完成度、类型、标签、引用、关联会话、来源这些「属性」折叠进「更多信息」。
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import dayjs from 'dayjs'
import { toast } from 'vue-sonner'
import { createReusableTemplate, useMediaQuery } from '@vueuse/core'
import { ArrowRight, Bot, Check, ChevronDown, Copy, ExternalLink, Layers, Link2, MessageSquarePlus, Pencil, Plus, Trash2, Unlink, User } from 'lucide-vue-next'
import { Dialog, DialogContent, DialogDescription, DialogTitle } from '../ui/dialog'
import { Sheet, SheetContent } from '../ui/sheet'
import Button from '../ui/button/Button.vue'
import WorkProgressBar from './WorkProgressBar.vue'
import WorkTrack from './WorkTrack.vue'
import TaskConfirmSection from '../confirms/TaskConfirmSection.vue'
import LinkedText from './LinkedText.vue'
import DraftRestoredNote from '../DraftRestoredNote.vue'
import { useWorkItemStore } from '../../stores/workItems'
import { useLocalDraft } from '../../composables/useLocalDraft'
import { workErrorMessage, workItemsApi, type WorkItemPatch } from '../../api/workItems'
import { draftScopes } from '../../lib/localDraft'
import { copyText } from '../../lib/terminal/clipboard'
import type { FeedbackSession } from '../../api/types'
import {
  WORK_KINDS,
  WORK_KIND_META,
  WORK_LIMITS,
  WORK_STAGES,
  WORK_STAGE_META,
  WORK_STATES,
  WORK_STATE_META,
  assigneeView,
  batchProgress,
  checkTitle,
  describeTransition,
  eventLabel,
  eventTone,
  isCompacted,
  isWorkKind,
  lengthHint,
  moduleOptions,
  parseLabelInput,
  parseRefInput,
  progressView,
  relativeTime,
  stageView,
  stateLabel,
  UNASSIGNED_LABEL,
  workRef,
  workflowLabel,
  type WorkItem,
  type WorkKind,
  type WorkStage,
  type WorkState,
} from '../../lib/workItems'
import { TONE_CLASSES } from './tone'

const props = withDefaults(
  defineProps<{
    open: boolean
    /** 当前可选的会话（用于「关联会话」）。 */
    sessions: FeedbackSession[]
    now: number
    /** 是否显示「添加子任务」：只有挂着新建任务弹窗的地方（任务页）才有意义。 */
    allowAddChild?: boolean
  }>(),
  { allowAddChild: true }
)

const emit = defineEmits<{
  (e: 'update:open', v: boolean): void
  (e: 'open-session', sessionId: string): void
  (e: 'open-item', id: number): void
  (e: 'add-child', parent: WorkItem): void
}>()

/**
 * 描述 / 进展正文里的引用（#161）被点中之后：任务 #N 就地换成那个任务（弹窗不关）；
 * 确认项 / 会话号 / 文档会把人带到别处——弹窗压在页面上，会挡住点名的效果，所以先关掉它。
 * 带修饰键的点击没被拦下（浏览器在新标签页打开深链）：弹窗保持原样。
 */
function onRefClick(event: MouseEvent) {
  const el = (event.target as HTMLElement | null)?.closest?.('a.link-ref') as HTMLElement | null
  if (!el || !event.defaultPrevented) return
  if (el.dataset.refKind !== 'task') emit('update:open', false)
}

/** 主区域直接展示的最近进展条数；其余折叠在「查看全部」后面。 */
const RECENT_UPDATES = 5

const store = useWorkItemStore()
const item = computed(() => store.detail)

// ---------- 承接者（共享任务板，#175 B2） ----------
// 派给同一张板上的另一个工作流：它接取 / 拒绝 / 交付，由你（或派发者）验收。候选工作流打开详情时按需取，
// 板上只有一个工作流、任务也没有承接者时整块都不出现，单工作流的界面不变。
const knownWorkflows = computed(() => props.sessions.map(s => ({ workflow_id: s.workflow_id || '', title: s.title })))
const assigneeInfo = computed(() => (item.value ? assigneeView(item.value, knownWorkflows.value) : null))
const assignCandidates = ref<string[]>([])
let candidateSeq = 0
async function loadCandidates(workflowId: string) {
  const seq = ++candidateSeq
  try {
    const res = await workItemsApi.assignees(workflowId)
    if (seq === candidateSeq) assignCandidates.value = res.members
  } catch {
    if (seq === candidateSeq) assignCandidates.value = []
  }
}
watch(
  () => item.value?.workflow_id,
  wf => {
    assignCandidates.value = []
    if (wf) void loadCandidates(wf)
  },
  { immediate: true }
)
const showAssign = computed(() => !!assigneeInfo.value || assignCandidates.value.length >= 2)
const assignPick = ref('')
watch(
  () => item.value?.id,
  () => (assignPick.value = '')
)
const assignBusy = ref(false)
/** 只有待办 / 进行中的任务能指派或改派（服务端同样拒绝验证中与已结束的）。 */
const canAssign = computed(() => item.value?.state === 'todo' || item.value?.state === 'doing')
const assignChoices = computed(() =>
  assignCandidates.value
    .filter(id => id !== item.value?.assignee_workflow_id)
    .map(id => ({ id, label: workflowLabel(id, knownWorkflows.value) }))
)
const assignBlockedNote = computed(() =>
  item.value?.state === 'review'
    ? '验证中的任务不能指派或改派：先验收通过（置为已完成），或退回到进行中。'
    : '已结束的任务不能指派或改派；先把它重新打开。'
)
const assigneeNote = computed(() => {
  const a = assigneeInfo.value
  if (!a) return ''
  if (!a.collaborative) return '自己派给自己的任务，和普通任务一样由它自己完成。'
  if (a.claim === 'declined') return '承接者拒绝了这次指派（理由见下面的进展）：改派给别人，或撤销指派。'
  if (a.claim === 'accepted') return '承接者已接下这条任务；做完它会交付（状态变成验证中），由你或派发者验收通过、或退回。'
  return '已派出，等承接者接取或拒绝；接取后状态变成进行中，做完交付再由你或派发者验收。'
})
async function doAssign(to: string) {
  const it = item.value
  if (!it || assignBusy.value) return
  assignBusy.value = true
  try {
    await store.assign(it, to)
    assignPick.value = ''
    toast.success(to ? `已指派给 ${workflowLabel(to, knownWorkflows.value)}` : '已撤销指派')
  } catch (err) {
    toast.error(workErrorMessage(err))
  } finally {
    assignBusy.value = false
  }
}

// ---------- 弹窗 / 抽屉 ----------

// 形态在「打开的那一刻」定下来，打开期间不随窗口宽度来回切换：两种形态各自在浏览器历史里压了一条返回手势的哨兵记录，
// 打开着换根组件会让它们对不上。
const wide = useMediaQuery('(min-width: 768px)')
const variant = ref<'dialog' | 'sheet'>(wide.value ? 'dialog' : 'sheet')
watch(
  () => props.open,
  isOpen => {
    if (isOpen) variant.value = wide.value ? 'dialog' : 'sheet'
  },
  { immediate: true }
)

const [DefineBody, ReuseBody] = createReusableTemplate()

// ---------- 编辑表单 ----------

interface Form {
  title: string
  kind: WorkKind
  progress: string
  conclusion: string
  labels: string
  refs: string
  /** 0 = 未归类。 */
  moduleId: number
  /** 空串 = 不手填完成度（由子任务 / 状态推导）。 */
  percent: string
}

const form = ref<Form>({ title: '', kind: 'feature', progress: '', conclusion: '', labels: '', refs: '', moduleId: 0, percent: '' })
let hydratedId = 0
let hydratedStamp = ''
const saving = ref(false)
const stateBusy = ref(false)
const errorText = ref('')

function formOf(d: WorkItem): Form {
  return {
    title: d.title,
    kind: (isWorkKind(d.kind) ? d.kind : 'feature') as WorkKind,
    progress: d.progress || '',
    conclusion: d.conclusion || '',
    labels: (d.labels || []).join(', '),
    refs: (d.refs || []).join('\n'),
    moduleId: d.module_id || 0,
    percent: typeof d.percent === 'number' ? String(d.percent) : '',
  }
}

const dirty = computed(() => {
  const d = item.value
  if (!d) return false
  const base = formOf(d)
  const f = form.value
  return (
    f.title !== base.title ||
    f.kind !== base.kind ||
    f.progress !== base.progress ||
    f.conclusion !== base.conclusion ||
    f.moduleId !== base.moduleId ||
    f.percent.trim() !== base.percent ||
    parseLabelInput(f.labels).join('\u0000') !== (d.labels || []).join('\u0000') ||
    parseRefInput(f.refs).join('\u0000') !== (d.refs || []).join('\u0000')
  )
})

// 切换到另一个任务时直接载入；同一个任务被服务端更新（智能体改了进度）时，只在你没有未保存修改的情况下才覆盖表单。
watch(
  item,
  d => {
    if (!d) {
      hydratedId = 0
      hydratedStamp = ''
      return
    }
    // 模块与完成度的修改不算「进展」、不更新 updated_at，所以签名里要带上它们，别人改了才会刷新表单
    const signature = `${d.updated_at}|${d.module_id || 0}|${d.percent ?? ''}`
    if (d.id !== hydratedId || (!dirty.value && signature !== hydratedStamp)) {
      form.value = formOf(d)
      errorText.value = ''
    }
    hydratedId = d.id
    hydratedStamp = signature
  },
  { immediate: true }
)

const titleCheck = computed(() => checkTitle(form.value.title))
const progressHint = computed(() => lengthHint(form.value.progress, WORK_LIMITS.progress))
const conclusionHint = computed(() => lengthHint(form.value.conclusion, WORK_LIMITS.conclusion))

/** 完成度输入：空 = 自动；否则必须是 0–100 的整数。 */
const percentCheck = computed<{ ok: boolean; value: number; error?: string }>(() => {
  const raw = form.value.percent.trim()
  if (raw === '') return { ok: true, value: -1 }
  if (!/^\d{1,3}$/.test(raw) || Number(raw) > 100) return { ok: false, value: -1, error: '完成度请填 0–100 的整数，留空表示自动' }
  return { ok: true, value: Number(raw) }
})

const canSave = computed(
  () => dirty.value && titleCheck.value.ok && percentCheck.value.ok && !progressHint.value.over && !conclusionHint.value.over && !saving.value
)

async function save() {
  const d = item.value
  if (!d || !canSave.value) return
  const f = form.value
  const patch: WorkItemPatch = {}
  if (titleCheck.value.title !== d.title) patch.title = titleCheck.value.title
  if (f.kind !== d.kind) patch.kind = f.kind
  if (f.progress !== (d.progress || '')) patch.progress = f.progress
  if (f.conclusion !== (d.conclusion || '')) patch.conclusion = f.conclusion
  if (f.moduleId !== (d.module_id || 0)) patch.module_id = f.moduleId
  if (f.percent.trim() !== (typeof d.percent === 'number' ? String(d.percent) : '')) patch.percent = percentCheck.value.value
  const labels = parseLabelInput(f.labels)
  if (labels.join('\u0000') !== (d.labels || []).join('\u0000')) patch.labels = labels
  const refs = parseRefInput(f.refs)
  if (refs.join('\u0000') !== (d.refs || []).join('\u0000')) patch.refs = refs
  saving.value = true
  errorText.value = ''
  try {
    const updated = await store.patch(d.id, patch)
    form.value = formOf(updated)
    hydratedStamp = `${updated.updated_at}|${updated.module_id || 0}|${updated.percent ?? ''}`
    toast.success('已保存')
  } catch (err) {
    errorText.value = workErrorMessage(err)
  } finally {
    saving.value = false
  }
}

function revert() {
  if (item.value) form.value = formOf(item.value)
  errorText.value = ''
}

async function changeState(next: WorkState) {
  const d = item.value
  if (!d || next === d.state) return
  stateBusy.value = true
  errorText.value = ''
  try {
    await store.setState(d, next)
  } catch (err) {
    errorText.value = workErrorMessage(err)
  } finally {
    stateBusy.value = false
  }
}

// ---------- 内容（描述） ----------
// 描述是「这件事是什么、做到什么算完成」：平时是只读的一段文字，要改时才展开成输入框，自己保存。
// 它和表单里的字段分开：只改描述不算进展（服务端不动「最近进展」时间、不占时间线），所以没有理由绑在页脚的「保存」上。

const descEditing = ref(false)
const descDraft = ref('')
const descSaving = ref(false)
const descInput = ref<HTMLTextAreaElement | null>(null)
const descHint = computed(() => lengthHint(descDraft.value, WORK_LIMITS.description))
const descChanged = computed(() => descDraft.value.trim() !== (item.value?.description || ''))

// 描述编辑的本机草稿（#144）：只有「正在编辑、且和已保存的不一样」才算草稿；
// 切到别的任务、关掉弹窗、刷新页面之后回来，编辑框连同没保存的内容一起回来。保存 / 取消 / 丢弃都会清掉。
interface TextDraft {
  text: string
}
function isTextDraft(d: unknown): d is TextDraft {
  return !!d && typeof (d as TextDraft).text === 'string'
}
const { restored: descRestored, discard: discardDescDraft } = useLocalDraft<TextDraft>({
  scope: () => (item.value ? draftScopes.workDesc(item.value.id) : null),
  read: () => ({ text: descEditing.value && descChanged.value ? descDraft.value : '' }),
  apply: saved => {
    descDraft.value = saved ? saved.text : ''
    descEditing.value = saved !== null
  },
  isEmpty: d => d.text === '',
  validate: isTextDraft,
})

async function startDescEdit() {
  descDraft.value = item.value?.description || ''
  descEditing.value = true
  errorText.value = ''
  await nextTick()
  const el = descInput.value
  if (el) {
    el.focus()
    el.setSelectionRange(el.value.length, el.value.length)
  }
}

function cancelDescEdit() {
  descEditing.value = false
  errorText.value = ''
}

async function saveDescription() {
  const d = item.value
  if (!d || descSaving.value || descHint.value.over) return
  const text = descDraft.value.trim()
  if (text === (d.description || '')) {
    descEditing.value = false
    return
  }
  descSaving.value = true
  errorText.value = ''
  try {
    await store.patch(d.id, { description: text })
    descEditing.value = false
  } catch (err) {
    errorText.value = workErrorMessage(err)
  } finally {
    descSaving.value = false
  }
}

// ---------- 阶段 ----------
// 阶段（评估 → 方案 → 开发 → 改进）和状态一样是进展标记，点一下立刻生效；空串 = 取消标注（界面按类型推导）。

const stageBusy = ref(false)
const stageInfo = computed(() => (item.value ? stageView(item.value) : null))
const stageNote = computed(() => {
  const v = stageInfo.value
  if (!v) return ''
  return v.derived ? `没有标注阶段，先按类型推导为「${v.label}」；你或智能体标注后以标注为准。` : `${v.label}：${v.hint}`
})

async function changeStage(next: WorkStage | '') {
  const d = item.value
  if (!d || next === (d.stage || '')) return
  stageBusy.value = true
  errorText.value = ''
  try {
    await store.patch(d.id, { stage: next })
  } catch (err) {
    errorText.value = workErrorMessage(err)
  } finally {
    stageBusy.value = false
  }
}

// ---------- 发起改进需求 ----------
// 发现问题、或想改成别的样子：不用另起任务，直接在原任务上写清楚。任务回到「进行中」并标为「改进」阶段，
// 这段话记在时间线上，同时交给工作流里的智能体（它正在等回复就是这一轮的回复，否则进收件箱，下次交互时读到）。
// 只给「验证中」（还在验证时就发现不对）与「已完成」（交付后才发现问题）的任务：进行中 / 待办的任务直接在会话里告诉智能体即可。

const improveOpen = ref(false)
const improveText = ref('')
const improveBusy = ref(false)
const improveInput = ref<HTMLTextAreaElement | null>(null)
const improveHint = computed(() => lengthHint(improveText.value, WORK_LIMITS.improve))
const canImprove = computed(() => item.value?.state === 'review' || item.value?.state === 'done')
const canSendImprove = computed(() => improveText.value.trim() !== '' && !improveHint.value.over && !improveBusy.value)

async function startImprove() {
  improveOpen.value = true
  errorText.value = ''
  await nextTick()
  improveInput.value?.focus()
}

function cancelImprove() {
  improveOpen.value = false
  improveText.value = ''
  errorText.value = ''
}

async function sendImprove() {
  const d = item.value
  if (!d || !canSendImprove.value) return
  improveBusy.value = true
  errorText.value = ''
  try {
    const res = await store.improve(d.id, improveText.value.trim())
    toast.success(`已发起改进需求 ${workRef(res.item)}`, {
      description:
        res.delivered === 'session'
          ? '任务回到进行中；智能体正在等回复，这段话就是这一轮的回复'
          : '任务回到进行中；这段话放进了收件箱，智能体下次交互时会收到',
    })
    cancelImprove()
  } catch (err) {
    errorText.value = workErrorMessage(err)
  } finally {
    improveBusy.value = false
  }
}

// 没发出去的改进需求存本机草稿（#144，作用域按任务）：换到别的任务、关掉弹窗、刷新页面之后回来，输入框连同内容一起回来。
// 发出去（cancelImprove 里清空）/ 点「取消」/ 点「丢弃」才清掉。
// 任务暂时不在「验证中 / 已完成」时整块区域不显示，内容留着，回到这两个状态就又出现。
// 换任务时的收起 / 清空也由草稿回填统一处理（没有草稿就收起并清空），不再在这里另写一份。
const { restored: improveRestored, discard: discardImproveDraft } = useLocalDraft<TextDraft>({
  scope: () => (item.value ? draftScopes.workImprove(item.value.id) : null),
  read: () => ({ text: improveOpen.value ? improveText.value : '' }),
  apply: saved => {
    improveText.value = saved ? saved.text : ''
    improveOpen.value = saved !== null
  },
  isEmpty: d => d.text.trim() === '',
  validate: isTextDraft,
})

// ---------- 删除（两步确认） ----------

const deleteArmed = ref(false)
let deleteTimer: ReturnType<typeof setTimeout> | null = null

async function removeItem() {
  const d = item.value
  if (!d) return
  if (!deleteArmed.value) {
    deleteArmed.value = true
    deleteTimer = setTimeout(() => (deleteArmed.value = false), 3000)
    return
  }
  if (deleteTimer) clearTimeout(deleteTimer)
  deleteArmed.value = false
  try {
    await store.remove(d.id)
    toast.success(`已删除任务 ${workRef(d)}`)
    emit('update:open', false)
  } catch (err) {
    errorText.value = workErrorMessage(err)
  }
}

onBeforeUnmount(() => {
  if (deleteTimer) clearTimeout(deleteTimer)
})

// ---------- 关联会话（折叠在「更多信息」里） ----------

const linkedIds = computed(() => new Set((item.value?.sessions || []).map(s => s.session_id)))
const linkCandidates = computed(() => {
  const wf = item.value?.workflow_id
  return props.sessions
    .filter(s => s.workflow_id === wf && !linkedIds.value.has(s.session_id) && s.status !== 'archived')
    .sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at))
    .slice(0, 30)
})

function sessionLabel(s: FeedbackSession) {
  const text = (s.title || s.summary || s.session_id).replace(/\s+/g, ' ').trim()
  return `${dayjs(s.created_at).format('MM-DD HH:mm')} · ${text.length > 36 ? text.slice(0, 36) + '…' : text}`
}

const linkSelect = ref('')
async function linkSelected() {
  const d = item.value
  const sid = linkSelect.value
  linkSelect.value = ''
  if (!d || !sid) return
  try {
    await store.linkSession(d.id, sid)
  } catch (err) {
    errorText.value = workErrorMessage(err)
  }
}

async function unlink(sessionId: string) {
  const d = item.value
  if (!d) return
  try {
    await store.unlinkSession(d.id, sessionId)
  } catch (err) {
    errorText.value = workErrorMessage(err)
  }
}

// ---------- 展示辅助 ----------

const batch = computed(() => (item.value ? batchProgress(item.value) : null))
const compacted = computed(() => (item.value ? isCompacted(item.value, props.now) : false))
const stateTone = computed(() => (item.value ? TONE_CLASSES[WORK_STATE_META[item.value.state]?.tone || 'slate'] : TONE_CLASSES.slate))
const canAddChild = computed(() => props.allowAddChild && !!item.value && !item.value.parent_id)
const progress = computed(() => (item.value ? progressView(item.value) : null))
const options = computed(() => moduleOptions(store.detailModules))
const modulePath = computed(() => options.value.find(o => o.id === (item.value?.module_id || 0))?.path || item.value?.module || '')

const updates = computed(() => item.value?.updates || [])
const recentUpdates = computed(() => updates.value.slice(0, RECENT_UPDATES))
const olderUpdates = computed(() => updates.value.slice(RECENT_UPDATES))
const showOlder = ref(false)
const showMore = ref(false)

// 换一个任务时，折叠状态回到默认
watch(
  () => item.value?.id,
  () => {
    showOlder.value = false
    showMore.value = false
    // 描述编辑框不在这里收：没保存的内容在本机草稿里，由 useLocalDraft 回填决定要不要展开
  }
)

function stamp(iso: string) {
  return dayjs(iso).format('MM-DD HH:mm')
}

async function copyRef() {
  const d = item.value
  if (!d) return
  if (await copyText(workRef(d))) toast.success(`已复制 ${workRef(d)}`)
  else toast.error('复制失败，请手动选择')
}

const fieldClass =
  'w-full rounded-xs border border-border/80 bg-background px-2 text-xs text-foreground placeholder:text-muted-foreground focus:border-primary focus:outline-none focus:ring-1 focus:ring-primary/30'
const labelClass = 'text-[11px] font-semibold uppercase tracking-wide text-muted-foreground'
const chipBase = 'inline-flex items-center gap-1 rounded-xs border px-2 py-1 text-[11px] transition-colors disabled:opacity-60 cursor-pointer'
const chipIdle = 'border-border/70 text-muted-foreground hover:bg-muted hover:text-foreground'
</script>

<template>
  <!-- 弹窗与抽屉共用的内容：头部固定、中间滚动、页脚固定 -->
  <DefineBody>
    <div class="shrink-0 space-y-1.5 border-b border-border/80 bg-muted/20 px-5 py-3.5 pr-12">
      <div class="flex items-start gap-2">
        <button
          v-if="item"
          type="button"
          class="mt-0.5 inline-flex shrink-0 items-center gap-1 rounded-xs border border-border/70 px-1.5 py-0.5 font-mono text-[11px] font-semibold text-foreground/80 hover:bg-muted cursor-pointer"
          title="复制编号（可写进提交说明）"
          data-testid="work-detail-ref"
          @click="copyRef"
        >
          {{ workRef(item) }}<Copy class="size-3 text-muted-foreground" />
        </button>
        <DialogTitle class="min-w-0 flex-1 break-words text-[15px] font-semibold leading-snug tracking-tight text-foreground" data-testid="work-detail-heading">
          {{ item ? item.title : '加载中…' }}
        </DialogTitle>
        <span
          v-if="item"
          class="mt-0.5 inline-flex shrink-0 items-center gap-1 rounded-xs border px-1.5 py-0.5 text-[11px]"
          :class="stateTone.badge"
        >
          <span class="size-1.5 rounded-full" :class="stateTone.dot" />{{ stateLabel(item.state) }}
        </span>
      </div>
      <DialogDescription class="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-[11px] text-muted-foreground">
        <template v-if="item">
          <span class="font-mono" title="创建它的工作流（同一个项目的工作流共用一张任务板）">{{ item.workflow_id }}</span>
          <span v-if="modulePath" class="rounded-xs bg-muted px-1.5 py-px" title="所属模块">{{ modulePath.split('/').join(' / ') }}</span>
          <span title="最近一次更新">更新于 {{ relativeTime(item.updated_at, now) }}</span>
        </template>
        <template v-else>任务详情</template>
      </DialogDescription>
      <WorkTrack v-if="item" :item="item" layout="row" class="pt-1" />
    </div>

    <div v-if="item" class="min-h-0 flex-1 space-y-5 overflow-y-auto px-5 py-4" data-testid="work-detail-body">
      <!-- 内容：这件事是什么、做到什么算完成。标题只是名字，进度只说做到哪，都回答不了「这到底是什么」 -->
      <section class="space-y-1.5" data-testid="work-detail-description">
        <div class="flex items-center justify-between">
          <h4 :class="labelClass">内容</h4>
          <button
            v-if="!descEditing && item.description"
            type="button"
            class="inline-flex cursor-pointer items-center gap-0.5 text-[11px] text-primary hover:underline"
            data-testid="work-detail-description-edit"
            @click="startDescEdit"
          >
            <Pencil class="size-3" />编辑
          </button>
        </div>

        <div v-if="descEditing" class="space-y-1.5">
          <textarea
            ref="descInput"
            v-model="descDraft"
            rows="5"
            data-testid="work-detail-description-input"
            placeholder="这件事是什么、为什么要做、做到什么算完成。写给几周后回来看的自己。"
            :class="[fieldClass, 'resize-y py-1.5 leading-relaxed']"
            @keydown.ctrl.enter.prevent="saveDescription"
            @keydown.meta.enter.prevent="saveDescription"
          />
          <DraftRestoredNote v-if="descRestored" what="内容编辑" testid="work-detail-description-draft-note" @discard="discardDescDraft" />
          <div class="flex items-center gap-2">
            <span class="font-mono text-[11px]" :class="descHint.over ? 'text-destructive' : 'text-muted-foreground'">{{ descHint.count }}/{{ WORK_LIMITS.description }}</span>
            <span class="text-[11px] text-muted-foreground">只改内容不会写进进展记录</span>
            <div class="ml-auto flex items-center gap-2">
              <Button variant="ghost" size="sm" class="h-7 text-xs" data-testid="work-detail-description-cancel" @click="cancelDescEdit">取消</Button>
              <Button size="sm" class="h-7 text-xs" :disabled="descSaving || descHint.over || !descChanged" data-testid="work-detail-description-save" @click="saveDescription">
                {{ descSaving ? '保存中…' : '保存' }}
              </Button>
            </div>
          </div>
        </div>
        <LinkedText
          v-else-if="item.description"
          :text="item.description"
          :workflow-id="item.workflow_id"
          class="whitespace-pre-wrap break-words rounded-md border border-border/60 bg-muted/20 px-3 py-2.5 text-[13px] leading-relaxed text-foreground"
          data-testid="work-detail-description-text"
          @click="onRefClick"
        />
        <div v-else class="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-md border border-dashed border-border/80 px-3 py-2.5" data-testid="work-detail-description-empty">
          <p class="min-w-0 flex-1 basis-56 text-xs leading-relaxed text-muted-foreground">
            还没有内容说明。标题只是名字，补几句「这是什么、做到什么算完成」，之后翻到它时一眼就能看懂。
          </p>
          <button
            type="button"
            class="inline-flex shrink-0 cursor-pointer items-center gap-1 rounded-xs border border-border/70 px-2 py-1 text-[11px] text-foreground hover:bg-muted"
            data-testid="work-detail-description-add"
            @click="startDescEdit"
          >
            <Pencil class="size-3" />补充内容
          </button>
        </div>
      </section>

      <!-- 状态与阶段：人可以置为任何状态；智能体在开发流程走完（回归 / e2e / 冒烟通过）后直接置为「已完成」并写下结论。阶段与「类型」「状态」互相独立 -->
      <section class="space-y-2 rounded-md border border-border/60 bg-muted/20 p-3">
        <div class="flex flex-wrap items-center gap-x-3 gap-y-1.5">
          <h4 class="w-9 shrink-0" :class="labelClass">状态</h4>
          <div class="flex flex-wrap gap-1.5" role="group" aria-label="任务状态">
            <button
              v-for="s in WORK_STATES"
              :key="s"
              type="button"
              :disabled="stateBusy"
              :aria-pressed="item.state === s"
              :data-testid="`work-state-${s}`"
              :class="[chipBase, item.state === s ? TONE_CLASSES[WORK_STATE_META[s].tone].badge + ' font-semibold' : chipIdle]"
              @click="changeState(s)"
            >
              <Check v-if="item.state === s" class="size-3" />{{ WORK_STATE_META[s].label }}
            </button>
          </div>
        </div>
        <p v-if="item.state === 'review'" class="text-[11px] text-muted-foreground" data-testid="work-detail-review-note">
          验证中：开发已完成，智能体在跑回归 / e2e / 热更冒烟，通过后会直接置为「已完成」并写下结论。你发现问题可以退回「进行中」，或在下面发起改进需求。
        </p>

        <div class="flex flex-wrap items-center gap-x-3 gap-y-1.5" data-testid="work-detail-stage">
          <h4 class="w-9 shrink-0" :class="labelClass">阶段</h4>
          <div class="flex flex-wrap gap-1.5" role="group" aria-label="任务阶段">
            <button
              type="button"
              :disabled="stageBusy"
              :aria-pressed="!item.stage"
              data-testid="work-stage-none"
              :class="[chipBase, !item.stage ? 'border-border bg-muted font-semibold text-foreground' : chipIdle]"
              @click="changeStage('')"
            >
              <Check v-if="!item.stage" class="size-3" />未标注
            </button>
            <button
              v-for="g in WORK_STAGES"
              :key="g"
              type="button"
              :disabled="stageBusy"
              :aria-pressed="item.stage === g"
              :data-testid="`work-stage-${g}`"
              :title="WORK_STAGE_META[g].hint"
              :class="[chipBase, item.stage === g ? TONE_CLASSES.violet.badge + ' font-semibold' : chipIdle]"
              @click="changeStage(g)"
            >
              <Check v-if="item.stage === g" class="size-3" />{{ WORK_STAGE_META[g].label }}
            </button>
          </div>
        </div>
        <p class="text-[11px] text-muted-foreground" data-testid="work-stage-note">{{ stageNote }}</p>
      </section>

      <!-- 承接者（共享任务板）：只有板上有别的工作流、或任务已经有承接者时才出现 -->
      <section v-if="showAssign" class="space-y-2 rounded-md border border-border/60 bg-muted/20 p-3" data-testid="work-detail-assignee">
        <div class="flex flex-wrap items-center gap-x-3 gap-y-1.5">
          <h4 class="w-9 shrink-0" :class="labelClass">承接</h4>
          <template v-if="assigneeInfo">
            <span
              class="inline-flex min-w-0 max-w-full items-center gap-1 rounded-xs border px-1.5 py-0.5 text-xs"
              :class="TONE_CLASSES[assigneeInfo.tone].badge"
              :data-claim="assigneeInfo.claim"
              data-testid="work-detail-assignee-view"
            >
              <ArrowRight class="size-3 shrink-0" aria-hidden="true" />
              <span class="truncate font-mono" :title="assigneeInfo.id">{{ assigneeInfo.label }}</span>
              <span v-if="assigneeInfo.claimLabel" class="shrink-0 font-semibold">{{ assigneeInfo.claimLabel }}</span>
            </span>
            <span class="text-[11px] text-muted-foreground" data-testid="work-detail-assignee-by">派发者：{{ assigneeInfo.byLabel }}</span>
          </template>
          <span v-else class="text-xs text-muted-foreground" data-testid="work-detail-assignee-none">未指派</span>
        </div>
        <p v-if="assigneeInfo" class="text-[11px] leading-relaxed text-muted-foreground" data-testid="work-detail-assignee-note">{{ assigneeNote }}</p>
        <div v-if="canAssign && assignChoices.length" class="flex flex-wrap items-center gap-2" data-testid="work-detail-assign-form">
          <select
            v-model="assignPick"
            :disabled="assignBusy"
            :class="[fieldClass, 'h-7 w-auto min-w-0 max-w-[14rem] cursor-pointer text-xs']"
            :aria-label="assigneeInfo ? '改派给哪个工作流' : '指派给哪个工作流'"
            data-testid="work-detail-assign-select"
          >
            <option value="">{{ assigneeInfo ? '改派给…' : '指派给…' }}</option>
            <option v-for="o in assignChoices" :key="o.id" :value="o.id">{{ o.label }}</option>
          </select>
          <Button size="sm" class="h-7 text-xs" :disabled="!assignPick || assignBusy" data-testid="work-detail-assign" @click="doAssign(assignPick)">
            {{ assigneeInfo ? '改派' : '指派' }}
          </Button>
          <Button v-if="assigneeInfo" variant="ghost" size="sm" class="h-7 text-xs" :disabled="assignBusy" data-testid="work-detail-unassign" @click="doAssign('')">
            撤销指派
          </Button>
        </div>
        <p v-else-if="!canAssign" class="text-[11px] text-muted-foreground" data-testid="work-detail-assign-blocked">{{ assignBlockedNote }}</p>
      </section>

      <!-- 发起改进需求：发现问题 / 想改成别的样子，不用另起任务 -->
      <section v-if="canImprove" class="space-y-2 rounded-md border border-violet-500/25 bg-violet-500/5 p-3" data-testid="work-detail-improve">
        <div v-if="!improveOpen" class="flex flex-wrap items-center gap-x-3 gap-y-1.5">
          <p class="min-w-0 flex-1 basis-56 text-xs leading-relaxed text-muted-foreground">
            {{ item.state === 'review' ? '验证时发现做得不到位？' : '交付后发现问题，或想改成别的样子？' }}不用另起任务：写下来，任务回到「进行中」，同时交给智能体去改。
          </p>
          <button
            type="button"
            class="inline-flex shrink-0 cursor-pointer items-center gap-1 rounded-xs border border-violet-500/40 px-2 py-1 text-[11px] font-medium text-violet-700 hover:bg-violet-500/10 dark:text-violet-300"
            data-testid="work-improve-open"
            @click="startImprove"
          >
            <MessageSquarePlus class="size-3" />发起改进需求
          </button>
        </div>
        <div v-else class="space-y-1.5">
          <h4 :class="labelClass">发起改进需求</h4>
          <textarea
            ref="improveInput"
            v-model="improveText"
            rows="4"
            data-testid="work-improve-input"
            placeholder="发现了什么问题，或想改成什么样。例如：导出的 CSV 在 Excel 里中文乱码，需要带 BOM"
            :class="[fieldClass, 'resize-y py-1.5 leading-relaxed']"
            @keydown.ctrl.enter.prevent="sendImprove"
            @keydown.meta.enter.prevent="sendImprove"
          />
          <DraftRestoredNote v-if="improveRestored" what="改进需求" testid="work-improve-draft-note" @discard="discardImproveDraft" />
          <div class="flex flex-wrap items-center gap-x-2 gap-y-1.5">
            <span class="font-mono text-[11px]" :class="improveHint.over ? 'text-destructive' : 'text-muted-foreground'" data-testid="work-improve-count">{{ improveHint.count }}/{{ WORK_LIMITS.improve }}</span>
            <span class="min-w-0 flex-1 basis-44 text-[11px] text-muted-foreground">发送后任务回到进行中（阶段「改进」）；更长的说明请直接在会话里告诉智能体</span>
            <div class="ml-auto flex items-center gap-2">
              <Button variant="ghost" size="sm" class="h-7 text-xs" :disabled="improveBusy" data-testid="work-improve-cancel" @click="cancelImprove">取消</Button>
              <Button size="sm" class="h-7 text-xs" :disabled="!canSendImprove" data-testid="work-improve-send" @click="sendImprove">
                {{ improveBusy ? '发送中…' : '发送改进需求' }}
              </Button>
            </div>
          </div>
        </div>
      </section>

      <!-- 进度与结论 -->
      <section class="space-y-3">
        <div v-if="progress" class="space-y-1" data-testid="work-detail-progressbar">
          <WorkProgressBar :item="item" show-label />
          <p class="text-[10px] text-muted-foreground">
            <template v-if="item.state === 'done'">已完成 = 100%</template>
            <template v-else-if="progress.from === 'explicit'">手动填写的完成度</template>
            <template v-else-if="progress.from === 'children'">按子任务完成比例计算{{ batch ? `（${batch.done}/${batch.total}）` : '' }}</template>
            <template v-else>没有填写具体完成度，进度条由状态推导；在「更多信息」里填写后以填写为准</template>
          </p>
        </div>

        <label class="block space-y-1">
          <span class="flex items-center justify-between" :class="labelClass">
            <span>当前进度</span>
            <span class="font-mono font-normal normal-case" :class="progressHint.over ? 'text-destructive' : ''">{{ progressHint.count }}/{{ WORK_LIMITS.progress }}</span>
          </span>
          <textarea v-model="form.progress" rows="3" data-testid="work-detail-progress" placeholder="现在做到哪了、下一步是什么" :class="[fieldClass, 'resize-y py-1.5 leading-relaxed']" />
        </label>

        <label class="block space-y-1">
          <span class="flex items-center justify-between" :class="labelClass">
            <span>结论</span>
            <span class="font-mono font-normal normal-case" :class="conclusionHint.over ? 'text-destructive' : ''">{{ conclusionHint.count }}/{{ WORK_LIMITS.conclusion }}</span>
          </span>
          <textarea v-model="form.conclusion" rows="2" data-testid="work-detail-conclusion" placeholder="1–3 行：最后做成了什么 / 为什么搁置。任务关闭很久之后，这是最终留下来的内容" :class="[fieldClass, 'resize-y py-1.5 leading-relaxed']" />
        </label>
      </section>

      <!-- 关联的确认项（计划确认 / 提问 / 决策）：没有时不出现 -->
      <TaskConfirmSection v-if="item.workflow_id" :workflow-id="item.workflow_id" :task-seq="item.seq" />

      <!-- 批次：有子任务或属于某个批次时才出现 -->
      <section v-if="item.parent_id || store.children.length || canAddChild" class="space-y-1.5">
        <div class="flex items-center justify-between">
          <h4 class="flex items-center gap-1" :class="labelClass">
            <Layers class="size-3" />批次
            <span v-if="batch" class="font-mono font-normal normal-case">{{ batch.done }}/{{ batch.total }}</span>
          </h4>
          <button v-if="canAddChild" type="button" class="inline-flex items-center gap-0.5 text-[11px] text-primary hover:underline cursor-pointer" @click="emit('add-child', item)">
            <Plus class="size-3" />添加子任务
          </button>
        </div>
        <button
          v-if="item.parent_id"
          type="button"
          class="flex w-full items-center gap-2 rounded-xs border border-border/70 px-2 py-1.5 text-left text-xs hover:bg-muted cursor-pointer"
          @click="emit('open-item', item.parent_id!)"
        >
          <span class="text-muted-foreground">属于</span>
          <span class="font-mono text-foreground/80">#{{ item.parent_seq }}</span>
          <span class="truncate">{{ item.parent_title }}</span>
        </button>
        <ul v-if="store.children.length" class="space-y-1">
          <li v-for="c in store.children" :key="c.id">
            <button
              type="button"
              class="flex w-full items-center gap-2 rounded-xs border border-border/70 px-2 py-1.5 text-left text-xs hover:bg-muted cursor-pointer"
              @click="emit('open-item', c.id)"
            >
              <span class="size-1.5 shrink-0 rounded-full" :class="TONE_CLASSES[WORK_STATE_META[c.state]?.tone || 'slate'].dot" />
              <span class="font-mono text-foreground/80">{{ workRef(c) }}</span>
              <span class="min-w-0 flex-1 truncate">{{ c.title }}</span>
              <span class="shrink-0 text-[10px] text-muted-foreground">{{ stateLabel(c.state) }}</span>
            </button>
          </li>
        </ul>
      </section>

      <!-- 最近进展：只露出最近几条，其余收起来 -->
      <section class="space-y-1.5" data-testid="work-detail-updates">
        <h4 :class="labelClass">
          最近进展
          <span v-if="updates.length" class="font-mono font-normal normal-case">{{ Math.min(RECENT_UPDATES, updates.length) }}/{{ updates.length }}</span>
        </h4>
        <p v-if="compacted" class="rounded-xs bg-muted/60 px-2 py-1.5 text-[11px] text-muted-foreground">
          这个任务关闭已超过 {{ WORK_LIMITS.compactAfterDays }} 天，进展时间线已被整理掉，只保留标题、结论、引用与关联会话。
        </p>
        <ol v-if="updates.length" class="relative space-y-2.5 border-l border-border/70 pl-3.5">
          <li v-for="u in showOlder ? updates : recentUpdates" :key="u.id" class="relative" data-testid="work-detail-update">
            <span class="absolute -left-[19px] top-1 size-2 rounded-full border-2 border-card" :class="u.actor_kind === 'agent' ? 'bg-sky-500' : 'bg-emerald-500'" />
            <div class="flex flex-wrap items-center gap-x-2 text-[10px] text-muted-foreground">
              <span class="inline-flex items-center gap-0.5">
                <Bot v-if="u.actor_kind === 'agent'" class="size-3" /><User v-else class="size-3" />{{ u.actor_kind === 'agent' ? '智能体' : '你' }}
              </span>
              <!-- 共享板：不是创建者自己写的，标出是哪个工作流；协作动作（指派 / 接取 / 拒绝 / 交付 / 验收 / 退回）带事件标签 -->
              <span
                v-if="u.actor_workflow_id && u.actor_workflow_id !== item.workflow_id"
                class="max-w-[10rem] truncate rounded-xs bg-muted px-1 py-px text-foreground/80"
                :title="u.actor_workflow_id"
                data-testid="work-detail-update-workflow"
              >{{ workflowLabel(u.actor_workflow_id, knownWorkflows) }}</span>
              <span
                v-if="u.event && eventLabel(u.event)"
                class="rounded-xs border px-1 py-px font-medium"
                :class="TONE_CLASSES[eventTone(u.event)].badge"
                data-testid="work-detail-update-event"
              >{{ eventLabel(u.event) }}</span>
              <span class="font-mono tabular-nums" :title="u.created_at">{{ stamp(u.created_at) }}</span>
              <span v-if="describeTransition(u.from_state, u.to_state)" class="rounded-xs bg-muted px-1 py-px text-foreground/80">{{ describeTransition(u.from_state, u.to_state) }}</span>
            </div>
            <LinkedText
              v-if="u.text"
              :text="u.text"
              :workflow-id="item.workflow_id"
              class="mt-0.5 whitespace-pre-wrap break-words text-xs leading-relaxed text-foreground/90"
              @click="onRefClick"
            />
          </li>
        </ol>
        <p v-else-if="!compacted" class="text-[11px] text-muted-foreground">还没有进展记录。</p>
        <button
          v-if="olderUpdates.length"
          type="button"
          class="cursor-pointer text-[11px] text-primary hover:underline"
          data-testid="work-detail-updates-more"
          @click="showOlder = !showOlder"
        >{{ showOlder ? '收起较早的进展' : `查看全部 ${updates.length} 条` }}</button>
      </section>

      <!-- 更多信息：改名、模块、完成度、类型、标签、引用、关联会话、来源 -->
      <section class="space-y-3 border-t border-border/60 pt-3">
        <button
          type="button"
          class="flex w-full cursor-pointer flex-wrap items-center gap-x-1 text-left"
          :class="labelClass"
          :aria-expanded="showMore"
          data-testid="work-detail-more"
          @click="showMore = !showMore"
        >
          <ChevronDown class="size-3.5 transition-transform" :class="showMore ? '' : '-rotate-90'" />更多信息
          <span class="font-normal normal-case">改名 · 模块 · 完成度 · 类型 · 标签 · 引用 · 关联会话 · 来源</span>
        </button>

        <div v-if="showMore" class="space-y-3" data-testid="work-detail-more-body">
          <label class="block space-y-1">
            <span :class="labelClass">标题</span>
            <input v-model="form.title" type="text" maxlength="260" data-testid="work-detail-title" :class="[fieldClass, 'h-8 text-[13px] font-medium']" />
            <span v-if="titleCheck.error" class="text-[11px] text-destructive">{{ titleCheck.error }}</span>
          </label>

          <!-- 模块 + 完成度：人能读懂进度，模型能按模块定位 -->
          <div class="grid gap-3 sm:grid-cols-2">
            <label class="block space-y-1">
              <span :class="labelClass">所属模块</span>
              <select v-model.number="form.moduleId" data-testid="work-detail-module" :class="[fieldClass, 'h-8 cursor-pointer']">
                <option :value="0">{{ UNASSIGNED_LABEL }}</option>
                <option v-for="o in options" :key="o.id" :value="o.id">{{ o.label }}</option>
              </select>
            </label>
            <label class="block space-y-1">
              <span :class="labelClass">完成度</span>
              <span class="flex items-center gap-2">
                <input
                  v-model="form.percent"
                  type="text"
                  inputmode="numeric"
                  maxlength="3"
                  placeholder="自动"
                  :disabled="item.state === 'done'"
                  data-testid="work-detail-percent"
                  :class="[fieldClass, 'h-8 w-20 font-mono disabled:opacity-60']"
                />
                <span class="text-[11px] text-muted-foreground">%</span>
                <button
                  v-if="form.percent !== ''"
                  type="button"
                  class="cursor-pointer text-[11px] text-primary hover:underline"
                  data-testid="work-detail-percent-auto"
                  @click="form.percent = ''"
                >改回自动</button>
              </span>
              <span v-if="percentCheck.error" class="block text-[11px] text-destructive">{{ percentCheck.error }}</span>
            </label>
          </div>

          <div class="space-y-1">
            <span :class="labelClass">类型</span>
            <div class="flex flex-wrap gap-1.5">
              <button
                v-for="k in WORK_KINDS"
                :key="k"
                type="button"
                class="rounded-xs border px-2 py-0.5 text-[11px] transition-colors cursor-pointer"
                :class="form.kind === k ? TONE_CLASSES[WORK_KIND_META[k].tone].badge + ' font-medium' : 'border-border/70 text-muted-foreground hover:bg-muted'"
                :aria-pressed="form.kind === k"
                @click="form.kind = k"
              >{{ WORK_KIND_META[k].label }}</button>
            </div>
          </div>

          <label class="block space-y-1">
            <span :class="labelClass">标签</span>
            <input v-model="form.labels" type="text" placeholder="终端, 设置" :class="[fieldClass, 'h-8']" />
          </label>

          <label class="block space-y-1">
            <span :class="labelClass">引用（一行一条）</span>
            <textarea v-model="form.refs" rows="2" data-testid="work-detail-refs" placeholder="commit:abc1234&#10;D-201" :class="[fieldClass, 'resize-y py-1.5 font-mono']" />
          </label>

          <div class="space-y-1.5">
            <h4 class="flex items-center gap-1" :class="labelClass">
              <Link2 class="size-3" />关联会话
              <span class="font-mono font-normal normal-case">{{ item.sessions?.length || 0 }}</span>
            </h4>
            <ul v-if="item.sessions?.length" class="space-y-1">
              <li v-for="s in item.sessions" :key="s.session_id" class="flex items-center gap-2 rounded-xs border border-border/70 px-2 py-1.5 text-xs">
                <button type="button" class="flex min-w-0 flex-1 items-center gap-1.5 text-left hover:text-primary cursor-pointer" @click="emit('open-session', s.session_id)">
                  <ExternalLink class="size-3 shrink-0 text-muted-foreground" />
                  <span class="truncate">{{ s.title || s.session_id }}</span>
                </button>
                <span class="shrink-0 text-[10px] text-muted-foreground">{{ relativeTime(s.linked_at, now) }}</span>
                <button type="button" class="shrink-0 text-muted-foreground hover:text-destructive cursor-pointer" title="解除关联（会话本身不受影响）" @click="unlink(s.session_id)">
                  <Unlink class="size-3.5" />
                </button>
              </li>
            </ul>
            <p v-else class="text-[11px] text-muted-foreground">还没有关联会话。智能体在会话里更新任务时会自动关联。</p>
            <select
              v-if="linkCandidates.length"
              v-model="linkSelect"
              :class="[fieldClass, 'h-8 cursor-pointer']"
              aria-label="关联一个会话"
              @change="linkSelected"
            >
              <option value="">＋ 关联同一工作流里的会话…</option>
              <option v-for="s in linkCandidates" :key="s.session_id" :value="s.session_id">{{ sessionLabel(s) }}</option>
            </select>
          </div>

          <p class="flex flex-wrap items-center gap-x-2 text-[11px] text-muted-foreground">
            <span class="inline-flex items-center gap-0.5">
              <Bot v-if="item.created_by === 'agent'" class="size-3" /><User v-else class="size-3" />
              {{ item.created_by === 'agent' ? '智能体创建' : '你创建' }} · {{ relativeTime(item.created_at, now) }}
            </span>
            <span v-if="item.closed_at">· 关闭于 {{ stamp(item.closed_at) }}</span>
          </p>
        </div>
      </section>
    </div>

    <div v-else class="flex flex-1 items-center justify-center text-xs text-muted-foreground">
      {{ store.detailLoading ? '加载中…' : '任务不存在或已被删除' }}
    </div>

    <p v-if="item && errorText" class="mx-5 mb-2 shrink-0 rounded-xs border border-destructive/30 bg-destructive/5 px-2 py-1.5 text-[11px] text-destructive" role="alert">{{ errorText }}</p>

    <footer v-if="item" class="flex shrink-0 items-center gap-2 border-t border-border/80 bg-muted/20 px-5 py-2.5">
      <button
        type="button"
        class="inline-flex h-8 items-center gap-1 rounded-xs border px-2 text-[11px] transition-colors cursor-pointer"
        :class="deleteArmed ? 'border-destructive/60 bg-destructive/10 text-destructive' : 'border-border/70 text-muted-foreground hover:text-destructive'"
        data-testid="work-delete"
        @click="removeItem"
      >
        <Trash2 class="size-3.5" />{{ deleteArmed ? '再点一次删除' : '删除' }}
      </button>
      <span v-if="dirty" class="text-[11px] text-amber-700 dark:text-amber-300">有未保存的修改</span>
      <div class="ml-auto flex items-center gap-2">
        <Button v-if="dirty" variant="ghost" size="sm" class="h-8 text-xs" @click="revert">放弃修改</Button>
        <Button size="sm" class="h-8 text-xs" :disabled="!canSave" data-testid="work-save" @click="save">{{ saving ? '保存中…' : '保存' }}</Button>
      </div>
    </footer>
  </DefineBody>

  <Dialog v-if="variant === 'dialog'" :open="open" @update:open="(v: boolean) => emit('update:open', v)">
    <DialogContent
      :show-close="true"
      class="flex h-[min(88vh,52rem)] w-[calc(100%-2rem)] max-w-2xl flex-col gap-0 overflow-hidden p-0"
      data-testid="work-detail"
      data-variant="dialog"
    >
      <ReuseBody />
    </DialogContent>
  </Dialog>
  <Sheet v-else :open="open" @update:open="(v: boolean) => emit('update:open', v)">
    <SheetContent
      side="right"
      :show-close="true"
      class="w-full sm:max-w-lg md:max-w-xl flex flex-col gap-0 p-0 border-l border-border bg-card overflow-hidden"
      data-testid="work-detail"
      data-variant="sheet"
    >
      <ReuseBody />
    </SheetContent>
  </Sheet>
</template>
