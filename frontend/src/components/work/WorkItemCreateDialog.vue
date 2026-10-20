<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { toast } from 'vue-sonner'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '../ui/dialog'
import Button from '../ui/button/Button.vue'
import { useWorkItemStore } from '../../stores/workItems'
import { workErrorMessage, workItemsApi } from '../../api/workItems'
import {
  WORK_KINDS,
  WORK_KIND_META,
  WORK_LIMITS,
  WORK_STAGES,
  WORK_STAGE_META,
  UNASSIGNED_LABEL,
  defaultStageForKind,
  checkTitle,
  lengthHint,
  moduleOptions,
  parseLabelInput,
  workRef,
  type WorkItem,
  type WorkKind,
  type WorkModule,
  type WorkStage,
  type WorkState,
  type WorkflowOption,
} from '../../lib/workItems'
import { TONE_CLASSES } from './tone'

const props = defineProps<{
  open: boolean
  workflowOptions: WorkflowOption[]
  /** 默认归属的工作流（看板当前筛选 / 正在查看的会话所在的工作流）。 */
  defaultWorkflowId?: string
  /** 预选的父任务（从详情里「添加子任务」打开时）。 */
  presetParent?: Pick<WorkItem, 'id' | 'workflow_id' | 'seq' | 'title'> | null
  /** 预选的模块 / 功能（任务页左侧树里正选着某个节点时）。 */
  defaultModuleId?: number
}>()

const emit = defineEmits<{
  (e: 'update:open', v: boolean): void
  (e: 'created', item: WorkItem): void
}>()

const store = useWorkItemStore()

const title = ref('')
const workflowId = ref('')
const kind = ref<WorkKind>('feature')
/** 空串 = 不标注（界面按类型推导一个默认值）。 */
const stage = ref<WorkStage | ''>('')
const state = ref<WorkState>('todo')
const progress = ref('')
const labelText = ref('')
const parentId = ref<number>(0)
const submitting = ref(false)
const serverError = ref('')
const titleInput = ref<HTMLInputElement | null>(null)

const parentCandidates = ref<WorkItem[]>([])
let candidateSeq = 0

// 所属模块：跟着所选工作流走（该工作流的规划树）；没有规划时不显示这一项。
const moduleId = ref(0)
const moduleChoices = ref<WorkModule[]>([])
const moduleChoiceOptions = computed(() => moduleOptions(moduleChoices.value))

const titleCheck = computed(() => checkTitle(title.value))
const progressHint = computed(() => lengthHint(progress.value, WORK_LIMITS.progress))
const workflowTrimmed = computed(() => workflowId.value.trim())
const labels = computed(() => parseLabelInput(labelText.value))
const canSubmit = computed(
  () => titleCheck.value.ok && workflowTrimmed.value !== '' && !progressHint.value.over && !submitting.value
)

function reset() {
  title.value = ''
  kind.value = 'feature'
  stage.value = ''
  state.value = 'todo'
  progress.value = ''
  labelText.value = ''
  serverError.value = ''
  submitting.value = false
  workflowId.value = props.presetParent?.workflow_id || props.defaultWorkflowId || props.workflowOptions[0]?.id || ''
  parentId.value = props.presetParent?.id || 0
  moduleId.value = props.defaultModuleId || 0
  // 先用任务页已经加载的规划树，免得下拉框先空一下再出现
  moduleChoices.value = workflowId.value === store.filter.workflowId ? store.modules : []
}

watch(
  () => props.open,
  async open => {
    if (!open) return
    reset()
    await nextTick()
    titleInput.value?.focus()
  }
)

// 工作流变了就重新取该工作流下的顶层任务，作为可选的「批次」。批次只允许一层，所以只列没有父任务的。
watch(
  [() => props.open, workflowTrimmed],
  async ([open, wf]) => {
    const seq = ++candidateSeq
    if (!open || !wf) {
      parentCandidates.value = []
      moduleChoices.value = []
      return
    }
    try {
      // top_level 的列表接口同时带回该工作流的规划树，一次请求拿到「可选批次」和「可选模块」
      const res = await workItemsApi.list({ workflow_id: wf, top_level: true, closed_days: 30 })
      if (seq !== candidateSeq) return
      parentCandidates.value = res.items.filter(i => i.state !== 'done' && i.state !== 'shelved')
      moduleChoices.value = res.modules
    } catch {
      if (seq === candidateSeq) {
        parentCandidates.value = []
        moduleChoices.value = []
      }
    }
    if (parentId.value && !parentCandidates.value.some(c => c.id === parentId.value) && props.presetParent?.id !== parentId.value) {
      parentId.value = 0
    }
    // 换了工作流或模块已被删除：预选的模块不再有效
    if (moduleId.value && !moduleChoices.value.some(m => m.id === moduleId.value)) moduleId.value = 0
  },
  { immediate: true }
)

async function submit() {
  if (!canSubmit.value) return
  submitting.value = true
  serverError.value = ''
  try {
    const item = await store.create({
      workflow_id: workflowTrimmed.value,
      title: titleCheck.value.title,
      kind: kind.value,
      stage: stage.value || undefined,
      state: state.value,
      labels: labels.value.length ? labels.value : undefined,
      progress: progress.value.trim() || undefined,
      parent_id: parentId.value || undefined,
      module_id: moduleId.value || undefined,
    })
    toast.success(`已创建任务 ${workRef(item)}`)
    emit('created', item)
    emit('update:open', false)
  } catch (err) {
    serverError.value = workErrorMessage(err)
  } finally {
    submitting.value = false
  }
}

const fieldClass =
  'w-full rounded-xs border border-border/80 bg-background px-2 text-xs text-foreground placeholder:text-muted-foreground focus:border-primary focus:outline-none focus:ring-1 focus:ring-primary/30'
</script>

<template>
  <Dialog :open="open" @update:open="(v: boolean) => emit('update:open', v)">
    <DialogContent class="max-w-md gap-3 p-5" data-testid="work-create-dialog">
      <DialogHeader class="space-y-1">
        <DialogTitle class="text-sm font-semibold">{{ presetParent ? `为 ${workRef(presetParent)} 添加子任务` : '新建任务' }}</DialogTitle>
        <DialogDescription class="text-xs text-muted-foreground">
          任务独立于会话：智能体用同一个工作流 ID 读写它，「已完成」始终由你确认。
        </DialogDescription>
      </DialogHeader>

      <form class="space-y-3" @submit.prevent="submit">
        <label class="block space-y-1">
          <span class="text-[11px] font-medium text-foreground">标题</span>
          <input
            ref="titleInput"
            v-model="title"
            type="text"
            maxlength="260"
            placeholder="一句话说清楚要做什么"
            data-testid="work-create-title"
            :class="[fieldClass, 'h-8']"
          />
          <span v-if="title && titleCheck.error" class="text-[11px] text-destructive">{{ titleCheck.error }}</span>
        </label>

        <label class="block space-y-1">
          <span class="text-[11px] font-medium text-foreground">所属工作流</span>
          <input
            v-model="workflowId"
            type="text"
            list="work-create-workflows"
            maxlength="128"
            placeholder="选择或输入工作流 ID"
            data-testid="work-create-workflow"
            :disabled="!!presetParent"
            :class="[fieldClass, 'h-8 font-mono disabled:opacity-60']"
          />
          <datalist id="work-create-workflows">
            <option v-for="w in workflowOptions" :key="w.id" :value="w.id">{{ w.label }}</option>
          </datalist>
        </label>

        <div class="space-y-1">
          <span class="text-[11px] font-medium text-foreground">类型</span>
          <div class="flex flex-wrap gap-1.5">
            <button
              v-for="k in WORK_KINDS"
              :key="k"
              type="button"
              class="rounded-xs border px-2 py-0.5 text-[11px] transition-colors cursor-pointer"
              :class="kind === k ? TONE_CLASSES[WORK_KIND_META[k].tone].badge + ' font-medium' : 'border-border/70 text-muted-foreground hover:bg-muted'"
              :aria-pressed="kind === k"
              @click="kind = k"
            >{{ WORK_KIND_META[k].label }}</button>
          </div>
        </div>

        <label v-if="moduleChoiceOptions.length" class="block space-y-1">
          <span class="text-[11px] font-medium text-foreground">所属模块（可选）</span>
          <select v-model.number="moduleId" data-testid="work-create-module" :class="[fieldClass, 'h-8 cursor-pointer']">
            <option :value="0">{{ UNASSIGNED_LABEL }}</option>
            <option v-for="o in moduleChoiceOptions" :key="o.id" :value="o.id">{{ o.label }}</option>
          </select>
        </label>

        <div class="grid grid-cols-2 gap-3">
          <label class="block space-y-1">
            <span class="text-[11px] font-medium text-foreground">阶段（可选）</span>
            <select v-model="stage" data-testid="work-create-stage" :class="[fieldClass, 'h-8 cursor-pointer']">
              <option value="">未标注（按类型推导为「{{ WORK_STAGE_META[defaultStageForKind(kind)].label }}」）</option>
              <option v-for="g in WORK_STAGES" :key="g" :value="g">{{ WORK_STAGE_META[g].label }}</option>
            </select>
          </label>
          <label class="block space-y-1">
            <span class="text-[11px] font-medium text-foreground">初始状态</span>
            <select v-model="state" :class="[fieldClass, 'h-8 cursor-pointer']">
              <option value="todo">待办</option>
              <option value="doing">进行中</option>
            </select>
          </label>
        </div>

        <label class="block space-y-1">
          <span class="text-[11px] font-medium text-foreground">标签（可选）</span>
          <input v-model="labelText" type="text" placeholder="终端, 设置" :class="[fieldClass, 'h-8']" />
        </label>

        <label v-if="parentCandidates.length || presetParent" class="block space-y-1">
          <span class="text-[11px] font-medium text-foreground">属于批次（可选）</span>
          <select v-model.number="parentId" :disabled="!!presetParent" :class="[fieldClass, 'h-8 cursor-pointer disabled:opacity-60']">
            <option :value="0">不属于任何批次</option>
            <option v-if="presetParent && !parentCandidates.some(c => c.id === presetParent!.id)" :value="presetParent.id">
              {{ workRef(presetParent) }} {{ presetParent.title }}
            </option>
            <option v-for="c in parentCandidates" :key="c.id" :value="c.id">{{ workRef(c) }} {{ c.title }}</option>
          </select>
        </label>

        <label class="block space-y-1">
          <span class="flex items-center justify-between text-[11px] font-medium text-foreground">
            <span>说明或当前进展（可选）</span>
            <span class="font-mono font-normal" :class="progressHint.over ? 'text-destructive' : 'text-muted-foreground'">
              {{ progressHint.count }}/{{ WORK_LIMITS.progress }}
            </span>
          </span>
          <textarea
            v-model="progress"
            rows="3"
            placeholder="背景、验收标准，或做到哪了"
            :class="[fieldClass, 'resize-y py-1.5 leading-relaxed']"
          />
        </label>

        <p v-if="serverError" class="rounded-xs border border-destructive/30 bg-destructive/5 px-2 py-1.5 text-[11px] text-destructive" role="alert">
          {{ serverError }}
        </p>

        <DialogFooter class="gap-2 sm:gap-2 pt-1">
          <Button type="button" variant="ghost" size="sm" class="h-8 text-xs" @click="emit('update:open', false)">取消</Button>
          <Button type="submit" size="sm" class="h-8 text-xs" :disabled="!canSubmit" data-testid="work-create-submit">
            {{ submitting ? '创建中…' : '创建任务' }}
          </Button>
        </DialogFooter>
      </form>
    </DialogContent>
  </Dialog>
</template>
