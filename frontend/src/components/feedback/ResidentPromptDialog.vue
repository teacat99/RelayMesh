<script setup lang="ts">
// 会话常驻规则的编辑弹窗（#232）：写给这个会话里的智能体看的规则，每次 RelayMesh 回执都带（含上下文压缩后的恢复路径）。
// 能做的事：套用「主线 / 分支」模板（先放进文本框，保存才生效）、看智能体在回执里实际看到的样子（服务端渲染的预览）、
// 回看最近 10 个版本并回滚、保存之后看智能体「已读到 / 还没读到」。常驻规则不能放宽授权档位，这里也写明。
// 第二个页签「唤醒词」（#232 D3）：Hub 叫醒这个会话时发给智能体的那段话，可按会话单独写（WakePromptsPanel）。
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useDebounceFn } from '@vueuse/core'
import { Eye, EyeOff, History, ScrollText } from 'lucide-vue-next'
import Dialog from '../ui/dialog/Dialog.vue'
import DialogContent from '../ui/dialog/DialogContent.vue'
import DialogDescription from '../ui/dialog/DialogDescription.vue'
import DialogTitle from '../ui/dialog/DialogTitle.vue'
import { toast } from '@/components/ui/sonner'
import { promptsErrorMessage, workflowPromptsApi, type ResidentPreview } from '@/api/workflowPrompts'
import {
  RESIDENT_MAX_DEFAULT,
  formatRevisionTime,
  normalizeResidentText,
  residentLength,
  residentSummary,
  residentToldLine,
  templateOptions,
} from '@/lib/residentPrompt'
import { useSettingsStore } from '@/stores/settings'
import { useWorkflowPromptsStore } from '@/stores/workflowPrompts'
import WakePromptsPanel from './WakePromptsPanel.vue'

const props = defineProps<{ open: boolean; workflowId: string }>()
const emit = defineEmits<{ (e: 'update:open', value: boolean): void }>()

const store = useWorkflowPromptsStore()
const settingsStore = useSettingsStore()

const view = computed(() => store.view(props.workflowId))
const max = computed(() => view.value?.max || RESIDENT_MAX_DEFAULT)
const options = computed(() => templateOptions(settingsStore.settings.promptTemplates))

const draft = ref('')
/** 用户在这次打开期间改过文本框：之后到达的服务端状态不再覆盖他正在写的文字。 */
const touched = ref(false)
const formError = ref('')
const previewOpen = ref(false)
const preview = ref<ResidentPreview | null>(null)
const previewError = ref('')
const revisionsOpen = ref(false)
/** 当前页签；唤醒词页签第一次点开才挂载，挂载后用 v-show 保留里面没保存的文字。 */
const tab = ref<'resident' | 'wake'>('resident')
const wakeSeen = ref(false)
/** 点过一次「套用」、等待二次确认的模板（文本框里有没保存的改动时才需要确认）。 */
const confirmingTemplate = ref('')
let confirmTimer: ReturnType<typeof setTimeout> | null = null
let previewSeq = 0

const count = computed(() => residentLength(draft.value))
const overLimit = computed(() => count.value > max.value)
const dirty = computed(() => normalizeResidentText(draft.value) !== normalizeResidentText(view.value?.resident ?? ''))
const canSave = computed(() => dirty.value && !overLimit.value && !store.busy)
const revisions = computed(() => view.value?.revisions ?? [])
const loadError = computed(() => store.errors[props.workflowId] ?? '')

watch(
  () => props.open,
  isOpen => {
    if (!isOpen) {
      resetTransient()
      return
    }
    touched.value = false
    formError.value = ''
    previewOpen.value = false
    preview.value = null
    revisionsOpen.value = false
    tab.value = 'resident'
    wakeSeen.value = false
    draft.value = view.value?.resident ?? ''
    void store.load(props.workflowId, true)
  },
  { immediate: true },
)

// 服务端状态到达（首次加载 / 另一个标签页改了）：没动过文本框就跟着更新
watch(
  () => view.value?.resident_rev,
  () => {
    if (props.open && !touched.value) draft.value = view.value?.resident ?? ''
  },
)

watch(draft, () => {
  if (props.open && previewOpen.value) refreshPreview()
})

watch(previewOpen, isOn => {
  if (isOn) void fetchPreview()
})

function resetTransient() {
  confirmingTemplate.value = ''
  if (confirmTimer) clearTimeout(confirmTimer)
  confirmTimer = null
}

onBeforeUnmount(resetTransient)

async function fetchPreview() {
  const mine = ++previewSeq
  try {
    const res = await workflowPromptsApi.preview(props.workflowId, draft.value)
    if (mine !== previewSeq) return
    preview.value = res
    previewError.value = ''
  } catch (err) {
    if (mine !== previewSeq) return
    preview.value = null
    previewError.value = promptsErrorMessage(err, '无法生成预览')
  }
}
const refreshPreview = useDebounceFn(() => void fetchPreview(), 300)

function onInput() {
  touched.value = true
  formError.value = ''
}

function applyTemplate(id: string, text: string) {
  const unsaved = dirty.value && normalizeResidentText(draft.value) !== '' && normalizeResidentText(draft.value) !== normalizeResidentText(text)
  if (unsaved && confirmingTemplate.value !== id) {
    confirmingTemplate.value = id
    if (confirmTimer) clearTimeout(confirmTimer)
    confirmTimer = setTimeout(() => {
      confirmingTemplate.value = ''
    }, 4000)
    return
  }
  resetTransient()
  draft.value = text
  touched.value = true
  formError.value = ''
}

function clearDraft() {
  draft.value = ''
  touched.value = true
  formError.value = ''
  resetTransient()
}

function loadRevision(text: string) {
  draft.value = text
  touched.value = true
  formError.value = ''
  resetTransient()
}

async function save() {
  if (!canSave.value) return
  formError.value = ''
  try {
    const res = await store.save(props.workflowId, draft.value)
    const v = res.view
    toast.success(
      !res.changed ? '内容没有变化，没有新增版本' : v.resident === '' ? `已清除常驻规则（v${v.resident_rev}）` : `已保存 v${v.resident_rev}`,
      { description: res.changed ? '智能体下一次调用 RelayMesh 时会收到。' : undefined, duration: 2200 },
    )
    emit('update:open', false)
  } catch (err) {
    formError.value = promptsErrorMessage(err, '保存失败')
  }
}

async function restore(id: number, rev: number) {
  formError.value = ''
  try {
    const res = await store.restore(props.workflowId, id)
    draft.value = res.view.resident
    touched.value = false
    toast.success(res.changed ? `已回滚：v${rev} 的文字存成了 v${res.view.resident_rev}` : '当前内容已经是这个版本', { duration: 2400 })
  } catch (err) {
    formError.value = promptsErrorMessage(err, '回滚失败')
  }
}

const toldLine = computed(() => residentToldLine(view.value))

function pickTab(t: 'resident' | 'wake') {
  tab.value = t
  if (t === 'wake') wakeSeen.value = true
}
</script>

<template>
  <Dialog :open="open" @update:open="(v: boolean) => emit('update:open', v)">
    <DialogContent
      class="sm:max-w-xl w-[94vw] p-0 gap-0 overflow-hidden rounded-md border-border/80 bg-card shadow-2xl font-mono"
      data-testid="resident-dialog"
    >
      <div class="px-5 h-11 border-b border-border/70 flex items-center shrink-0 bg-card/90 pr-12">
        <DialogTitle class="flex items-center gap-2 text-xs sm:text-sm font-bold tracking-tight text-foreground text-left">
          <div class="p-1 rounded-xs bg-primary/10 text-primary border border-primary/20 shrink-0">
            <ScrollText class="w-3.5 h-3.5" />
          </div>
          <span data-testid="resident-title">{{ tab === 'wake' ? '唤醒词 · 这个会话' : '常驻规则 · 这个会话' }}</span>
        </DialogTitle>
      </div>

      <div class="px-5 h-9 border-b border-border/60 flex items-end gap-4 shrink-0 bg-card/60" role="tablist" data-testid="resident-tabs">
        <button
          type="button"
          role="tab"
          data-testid="resident-tab-resident"
          :aria-selected="tab === 'resident'"
          class="h-full -mb-px px-0.5 border-b-2 text-[11px] cursor-pointer transition-colors"
          :class="tab === 'resident' ? 'border-primary text-foreground font-semibold' : 'border-transparent text-muted-foreground hover:text-foreground'"
          @click="pickTab('resident')"
        >常驻规则</button>
        <button
          type="button"
          role="tab"
          data-testid="resident-tab-wake"
          :aria-selected="tab === 'wake'"
          class="h-full -mb-px px-0.5 border-b-2 text-[11px] cursor-pointer transition-colors"
          :class="tab === 'wake' ? 'border-primary text-foreground font-semibold' : 'border-transparent text-muted-foreground hover:text-foreground'"
          @click="pickTab('wake')"
        >唤醒词</button>
      </div>

      <div v-if="wakeSeen" v-show="tab === 'wake'" class="p-5 text-xs max-h-[78vh] overflow-y-auto" role="tabpanel" data-testid="resident-panel-wake">
        <WakePromptsPanel :workflow-id="workflowId" />
      </div>

      <div v-show="tab === 'resident'" class="p-5 space-y-3 text-xs max-h-[78vh] overflow-y-auto" role="tabpanel" data-testid="resident-panel-resident">
        <DialogDescription class="text-[11px] text-muted-foreground leading-relaxed">
          写给这个会话里的智能体看的规则：<b class="text-foreground">每次回执都会带上它</b>（上下文被压缩后的恢复路径也带），
          不用再靠它自己记。它不能放宽授权档位——冲突时以更严的为准；只有你能在这里改，智能体只读。
        </DialogDescription>

        <p
          class="text-[11px] leading-relaxed rounded-xs border px-2.5 py-1.5"
          :class="view && view.resident_rev > 0 && view.resident.trim() !== '' && !view.told ? 'border-amber-500/50 bg-amber-500/10 text-foreground' : 'border-border/60 bg-muted/40 text-muted-foreground'"
          data-testid="resident-told"
          :data-told="view ? (view.told ? 'true' : 'false') : ''"
        >{{ toldLine }}</p>

        <p v-if="loadError && !view" class="text-[11px] text-destructive flex items-center gap-1.5" data-testid="resident-load-error">
          <span>{{ loadError }}</span>
          <button type="button" class="underline cursor-pointer" @click="store.load(workflowId, true)">重试</button>
        </p>

        <div class="space-y-1.5">
          <div class="flex flex-wrap items-center gap-1.5">
            <span class="text-[11px] text-muted-foreground">套用模板</span>
            <button
              v-for="t in options"
              :key="t.id"
              type="button"
              :title="t.hint + (t.overridden ? '（已在设置里改成你自己的文字）' : '')"
              :data-testid="`resident-template-${t.id}`"
              class="px-2 h-6 rounded-xs border text-[11px] cursor-pointer transition-colors"
              :class="confirmingTemplate === t.id
                ? 'border-destructive text-destructive font-semibold'
                : 'border-border/80 text-muted-foreground hover:text-foreground hover:bg-muted'"
              @click="applyTemplate(t.id, t.current)"
            >{{ confirmingTemplate === t.id ? '再点一次：覆盖未保存的文字' : t.label }}</button>
            <button
              type="button"
              data-testid="resident-template-clear"
              class="px-2 h-6 rounded-xs border border-border/80 text-[11px] text-muted-foreground hover:text-destructive hover:bg-muted cursor-pointer"
              @click="clearDraft"
            >清空</button>
            <span class="text-[10px] text-muted-foreground/80">（只放进文本框，保存才生效）</span>
          </div>

          <textarea
            v-model="draft"
            data-testid="resident-text"
            rows="10"
            spellcheck="false"
            placeholder="例如：1. 结束前把名下任务做完或置暂缓；2. 要我决策的事一律发确认卡……"
            class="w-full px-2.5 py-2 text-xs bg-background border border-border/80 rounded-xs focus:outline-none focus:border-primary text-foreground resize-y leading-relaxed min-h-[8rem] max-h-[40vh]"
            :class="overLimit ? 'border-destructive focus:border-destructive' : ''"
            :aria-invalid="overLimit"
            @input="onInput"
          />
          <div class="flex items-center justify-between gap-2 text-[11px]">
            <span
              class="tabular-nums"
              :class="overLimit ? 'text-destructive font-semibold' : 'text-muted-foreground'"
              data-testid="resident-count"
            >{{ count }} / {{ max }}</span>
            <span v-if="overLimit" class="text-destructive" data-testid="resident-over">超出 {{ count - max }} 个字，请删减后再保存</span>
            <button
              type="button"
              data-testid="resident-preview-toggle"
              class="flex items-center gap-1 text-muted-foreground hover:text-foreground cursor-pointer"
              :aria-expanded="previewOpen"
              @click="previewOpen = !previewOpen"
            >
              <EyeOff v-if="previewOpen" class="w-3 h-3" />
              <Eye v-else class="w-3 h-3" />
              <span>{{ previewOpen ? '收起预览' : '预览智能体看到的样子' }}</span>
            </button>
          </div>
        </div>

        <div v-if="previewOpen" class="space-y-1" data-testid="resident-preview-box">
          <p v-if="previewError" class="text-[11px] text-destructive" data-testid="resident-preview-error">{{ previewError }}</p>
          <p v-else-if="preview && preview.empty" class="text-[11px] text-muted-foreground" data-testid="resident-preview-empty">
            文字为空：回执里不会出现 resident: 这一行。
          </p>
          <template v-else-if="preview">
            <p class="text-[10px] text-muted-foreground">下面就是回执头里的这一行（按保存后的 v{{ preview.rev }} 渲染）：</p>
            <pre
              class="p-2 rounded-xs border border-border/70 bg-muted/40 text-[11px] leading-relaxed whitespace-pre-wrap break-words max-h-48 overflow-y-auto"
              data-testid="resident-preview"
            >{{ preview.preview }}</pre>
          </template>
          <p v-else class="text-[11px] text-muted-foreground">生成中…</p>
        </div>

        <div class="rounded-xs border border-border/60">
          <button
            type="button"
            data-testid="resident-revisions-toggle"
            class="flex w-full items-center gap-1.5 px-2.5 py-1.5 text-left text-[11px] text-muted-foreground hover:text-foreground cursor-pointer"
            :aria-expanded="revisionsOpen"
            @click="revisionsOpen = !revisionsOpen"
          >
            <History class="w-3 h-3" />
            <span>历史版本</span>
            <span class="tabular-nums">（{{ revisions.length }}）</span>
            <span class="ml-auto text-[10px] text-muted-foreground/80">最近 10 个，可载入或回滚</span>
          </button>
          <ul v-if="revisionsOpen" class="border-t border-border/50 divide-y divide-border/40" data-testid="resident-revisions">
            <li v-if="revisions.length === 0" class="px-2.5 py-2 text-[11px] text-muted-foreground">还没有历史版本。</li>
            <li
              v-for="r in revisions"
              :key="r.id"
              :data-testid="`resident-rev-${r.rev}`"
              class="px-2.5 py-1.5 space-y-0.5"
            >
              <div class="flex items-center gap-2 text-[11px]">
                <span class="font-semibold text-foreground tabular-nums">v{{ r.rev }}</span>
                <span class="text-muted-foreground tabular-nums">{{ formatRevisionTime(r.created_at) }}</span>
                <span v-if="r.rev === view?.resident_rev" class="px-1 rounded-xs border border-primary/40 bg-primary/10 text-primary text-[10px]">当前</span>
                <span class="flex-1" />
                <button
                  type="button"
                  :data-testid="`resident-rev-load-${r.rev}`"
                  class="text-muted-foreground hover:text-foreground underline underline-offset-2 cursor-pointer"
                  @click="loadRevision(r.text)"
                >载入</button>
                <button
                  v-if="r.rev !== view?.resident_rev"
                  type="button"
                  :data-testid="`resident-rev-restore-${r.rev}`"
                  :disabled="store.busy"
                  class="text-muted-foreground hover:text-foreground underline underline-offset-2 cursor-pointer disabled:opacity-50"
                  @click="restore(r.id, r.rev)"
                >回滚</button>
              </div>
              <p class="text-[11px] text-muted-foreground leading-relaxed break-words">{{ residentSummary(r.text) }}</p>
            </li>
          </ul>
        </div>

        <p v-if="formError" class="text-[11px] text-destructive" data-testid="resident-error">{{ formError }}</p>

        <div class="flex items-center gap-2 pt-1">
          <button
            type="button"
            data-testid="resident-save"
            :disabled="!canSave"
            class="px-3 h-7 rounded-xs bg-primary text-primary-foreground text-[11px] font-semibold cursor-pointer hover:brightness-110 disabled:opacity-50 disabled:cursor-not-allowed"
            @click="save"
          >{{ store.busy ? '保存中…' : '保存' }}</button>
          <button
            type="button"
            data-testid="resident-cancel"
            class="px-3 h-7 rounded-xs border border-border/80 text-[11px] text-muted-foreground hover:text-foreground cursor-pointer"
            @click="emit('update:open', false)"
          >取消</button>
          <span v-if="dirty" class="text-[10px] text-amber-600 dark:text-amber-400" data-testid="resident-dirty">有未保存的改动</span>
        </div>
      </div>
    </DialogContent>
  </Dialog>
</template>
