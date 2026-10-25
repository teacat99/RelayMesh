<script setup lang="ts">
// 新建分支会话（#236）：给当前会话（主会话）开一个分支，把一块工作拆出去单独做。
//
// 分支是一个独立的会话：有自己的页面、收件箱和智能体，只多一条「属于哪个主会话」的关系。
// 两步：① 填名字（唯一必填）、选「向主会话开放多少」的预设、决定要不要把我在分支里说的话抄送一行给主会话；
//      ② 建好后显示启动提示词（发给要在分支里干活的智能体），关闭时进入分支。
// 服务端在同一个事务里落占位会话和分支关系（见 internal/store/web_workflow.go）；用户建的分支会往主会话的收件箱里写一行说明。
// 协作 / 托管预设含「改参数」授权（#236 P3），属于放宽授权：终端已解锁就直接建，没解锁时服务端回 unlock_required，这里弹出密码框再重试；
// 终端功能不可用（没设 Web 密码等）时服务端照建，只是把放宽的项压回关闭，建好后在结果页写明。
import { computed, nextTick, reactive, ref, watch } from 'vue'
import { toast } from 'vue-sonner'
import { Check, ChevronDown, ChevronRight, Copy, GitBranch } from 'lucide-vue-next'
import Dialog from '../ui/dialog/Dialog.vue'
import DialogContent from '../ui/dialog/DialogContent.vue'
import DialogDescription from '../ui/dialog/DialogDescription.vue'
import DialogTitle from '../ui/dialog/DialogTitle.vue'
import Button from '../ui/button/Button.vue'
import { useSessionStore } from '../../stores/session'
import { useBranchStore } from '../../stores/branches'
import { useTerminalStore } from '../../stores/terminal'
import { UnlockCancelled } from '../../api/terminal'
import type { BranchPreset } from '../../api/branches'
import type { CreateWorkflowResult } from '../../api/client'
import type { FeedbackSession } from '../../api/types'
import {
  FIRST_MESSAGE_MAX,
  SESSION_NAME_MAX,
  appliedLines,
  blankNewSessionDraft,
  buildCreateRequest,
  collectNewSessionSuggestions,
  warningLines,
  type NewSessionDraft,
} from '../../lib/newSession'
import { BRANCH_PRESETS, NEW_BRANCH_UNLOCK_REASON, PARAMS_UNLOCK_NOTE, clampedNotice } from '../../lib/branch'
import { copyText } from '../../lib/terminal/clipboard'

const props = defineProps<{
  open: boolean
  /** 要从哪个会话（主会话）上开分支。 */
  parentWorkflowId: string
  parentTitle?: string
}>()
const emit = defineEmits<{
  (e: 'update:open', val: boolean): void
  /** 建好之后关闭弹窗时触发（不论点「进入分支」还是点遮罩 / Esc 关闭）：调用方据此切到新分支。 */
  (e: 'opened', session: FeedbackSession): void
}>()

const sessionStore = useSessionStore()
const branchStore = useBranchStore()
const terminalStore = useTerminalStore()

const draft = reactive<NewSessionDraft>(blankNewSessionDraft())
const preset = ref<BranchPreset>('collab')
const copyNotes = ref(true)
const advancedOpen = ref(false)
const busy = ref(false)
const error = ref('')
const created = ref<CreateWorkflowResult | null>(null)
const copied = ref(false)
const inputRef = ref<HTMLInputElement | null>(null)

const validation = computed(() => buildCreateRequest(draft))
const errors = computed(() => (validation.value.ok ? {} : validation.value.errors))
const nameHint = computed(() => (draft.name.trim() ? (errors.value.name ?? '') : ''))
const canSubmit = computed(() => validation.value.ok && !busy.value && !!props.parentWorkflowId)
const suggestions = computed(() => collectNewSessionSuggestions(sessionStore.sessions))
const appliedList = computed(() => appliedLines(created.value?.applied))
const warningList = computed(() => warningLines(created.value?.warnings))
const presetHint = computed(() => BRANCH_PRESETS.find(p => p.id === preset.value)?.hint ?? '')
/** 创建结果里被压回的放宽项（终端不可用时）；没有则为空串。 */
const clampedText = computed(() => clampedNotice(created.value?.clamped))

// immediate：本组件是懒加载的，第一次是「加载完成、同一帧里就置为打开」，挂载时 open 已经是 true，普通 watch 不会触发
watch(
  () => props.open,
  isOpen => {
    if (!isOpen) return
    Object.assign(draft, blankNewSessionDraft())
    preset.value = 'collab'
    copyNotes.value = true
    advancedOpen.value = false
    error.value = ''
    created.value = null
    copied.value = false
    busy.value = false
    void nextTick(() => inputRef.value?.focus())
  },
  { immediate: true },
)

async function submit() {
  const v = validation.value
  if (!v.ok || busy.value || !props.parentWorkflowId) return
  busy.value = true
  error.value = ''
  try {
    const res = await terminalStore.unlockOnDemand(
      () =>
        sessionStore.createWorkflow({
          ...v.request,
          parent_workflow_id: props.parentWorkflowId,
          expose_preset: preset.value,
          copy_user_notes: copyNotes.value,
        }),
      NEW_BRANCH_UNLOCK_REASON,
    )
    if (res.branch) branchStore.applyRelation(res.branch)
    created.value = res
  } catch (e: any) {
    // 用户在密码框里点了取消：什么都没建，静默停在表单上，可以改选「仅可见」或重试
    if (!(e instanceof UnlockCancelled)) error.value = e?.response?.data?.error || e?.message || '创建失败，请稍后重试'
  } finally {
    busy.value = false
  }
}

async function copyPrompt() {
  if (!created.value) return
  if (await copyText(created.value.start_prompt)) {
    copied.value = true
    toast.success('已复制启动提示词')
  } else {
    toast.error('复制失败，请在文本框里手动全选复制')
  }
}

function onOpenChange(next: boolean) {
  // 先关弹窗再通知调用方：关闭时要弹掉压在浏览器历史里的哨兵记录，调用方跳转前会等它走完（见 useDialogBackGesture）
  emit('update:open', next)
  if (!next && created.value) emit('opened', created.value.session)
}

const fieldClass =
  'w-full px-2.5 py-1.5 rounded-sm border border-border/80 bg-background text-sm font-mono text-foreground focus:outline-none focus:ring-1 focus:ring-primary/50'
const labelClass = 'text-[11px] font-bold text-muted-foreground uppercase tracking-wider'
</script>

<template>
  <Dialog :open="open" @update:open="onOpenChange">
    <DialogContent
      class="sm:max-w-lg p-4 bg-card border border-border rounded-md shadow-modal font-mono max-h-[92vh] overflow-y-auto"
      data-testid="new-branch-dialog"
    >
      <!-- ② 建好了：启动提示词 -->
      <template v-if="created">
        <DialogTitle class="flex items-center gap-2 text-sm font-bold text-foreground">
          <Check class="w-4 h-4 text-primary shrink-0" />
          <span class="truncate" data-testid="new-branch-created-title">已创建分支「{{ created.session.title }}」</span>
        </DialogTitle>
        <DialogDescription class="text-[11px] text-muted-foreground leading-relaxed">
          把下面的启动提示词发给要在分支里干活的智能体：它第一次调用 RelayMesh 时带上这个工作流编号，就接进了这个分支。
          主会话的收件箱里已经写了一行说明，主会话的智能体会知道有了新分支。
        </DialogDescription>

        <div class="space-y-3">
          <ul
            v-if="appliedList.length"
            class="space-y-0.5 rounded-sm border border-border/60 bg-muted/30 px-2.5 py-1.5 text-[11px] leading-relaxed text-foreground"
            data-testid="new-branch-applied"
          >
            <li v-for="line in appliedList" :key="line" class="flex gap-1.5">
              <Check class="w-3 h-3 text-primary shrink-0 mt-[3px]" />
              <span class="min-w-0 break-words">{{ line }}</span>
            </li>
          </ul>
          <div
            v-if="warningList.length"
            class="space-y-0.5 rounded-sm border border-destructive/40 bg-destructive/5 px-2.5 py-1.5 text-[11px] leading-relaxed text-destructive"
            role="alert"
            data-testid="new-branch-warnings"
          >
            <p v-for="line in warningList" :key="line" class="break-words">{{ line }}</p>
          </div>
          <p
            v-if="clampedText"
            class="rounded-sm border border-amber-500/50 bg-amber-500/5 px-2.5 py-1.5 text-[11px] leading-relaxed text-amber-700 dark:text-amber-400"
            role="status"
            data-testid="new-branch-clamped"
          >{{ clampedText }}</p>

          <div class="space-y-1.5">
            <div class="flex items-center justify-between gap-2">
              <span :class="labelClass">启动提示词</span>
              <code class="text-[10px] text-muted-foreground truncate" data-testid="new-branch-wfid">{{ created.workflow_id }}</code>
            </div>
            <textarea
              readonly
              rows="9"
              class="w-full min-h-[8rem] max-h-[40vh] resize-y rounded-sm border border-border/80 bg-background px-2.5 py-1.5 text-[11px] leading-relaxed font-mono text-foreground focus:outline-none focus:ring-1 focus:ring-primary/50"
              data-testid="new-branch-prompt"
              :value="created.start_prompt"
              @focus="($event.target as HTMLTextAreaElement).select()"
            />
          </div>

          <div class="flex items-center justify-end gap-2 pt-2 border-t border-border/50">
            <Button type="button" variant="outline" size="sm" class="text-xs gap-1.5" data-testid="new-branch-copy" @click="copyPrompt">
              <Check v-if="copied" class="w-3.5 h-3.5 text-primary" />
              <Copy v-else class="w-3.5 h-3.5" />
              {{ copied ? '已复制' : '复制启动提示词' }}
            </Button>
            <Button type="button" size="sm" class="text-xs px-3" data-testid="new-branch-open" @click="onOpenChange(false)">
              进入分支
            </Button>
          </div>
        </div>
      </template>

      <!-- ① 名字、向主会话开放多少、是否抄送 -->
      <template v-else>
        <DialogTitle class="flex items-center gap-2 text-sm font-bold text-foreground">
          <GitBranch class="w-4 h-4 text-primary shrink-0" />
          <span>新建分支</span>
        </DialogTitle>
        <DialogDescription class="text-[11px] text-muted-foreground leading-relaxed">
          把「{{ parentTitle || parentWorkflowId }}」里的一块工作拆成一个分支：它是独立的会话，有自己的页面和智能体；
          两边的智能体只能通过受控的通道互相说话，你在两边都能看到。
        </DialogDescription>

        <div class="space-y-3">
          <div class="space-y-1.5">
            <label for="new-branch-name" :class="labelClass">分支名称</label>
            <input
              id="new-branch-name"
              ref="inputRef"
              v-model="draft.name"
              type="text"
              autocomplete="off"
              :class="fieldClass"
              placeholder="例如：登录页重构"
              data-testid="new-branch-name"
              :aria-invalid="!!nameHint"
              :disabled="busy"
              @keydown.enter.prevent="submit"
            />
            <p v-if="nameHint" class="text-[10px] text-destructive leading-normal" data-testid="new-branch-hint">{{ nameHint }}</p>
            <p v-else class="text-[10px] text-muted-foreground leading-normal">最多 {{ SESSION_NAME_MAX }} 个字符。</p>
          </div>

          <div class="space-y-1.5">
            <span :class="labelClass" id="new-branch-preset-label">向主会话开放多少</span>
            <div class="grid grid-cols-3 gap-1.5" role="radiogroup" aria-labelledby="new-branch-preset-label">
              <button
                v-for="p in BRANCH_PRESETS"
                :key="p.id"
                type="button"
                role="radio"
                :aria-checked="preset === p.id"
                class="rounded-sm border px-2 py-1.5 text-[11px] text-center focus:outline-none focus-visible:ring-1 focus-visible:ring-primary/50"
                :class="preset === p.id
                  ? 'border-primary bg-primary/10 text-foreground font-bold'
                  : 'border-border/80 bg-background text-muted-foreground hover:text-foreground'"
                :data-testid="`new-branch-preset-${p.id}`"
                :disabled="busy"
                @click="preset = p.id"
              >{{ p.label }}</button>
            </div>
            <p class="text-[10px] text-muted-foreground leading-normal" data-testid="new-branch-preset-hint">{{ presetHint }}</p>
            <p v-if="preset !== 'visible'" class="text-[10px] text-muted-foreground leading-normal" data-testid="new-branch-unlock-note">{{ PARAMS_UNLOCK_NOTE }}</p>
          </div>

          <label class="flex items-start gap-2 rounded-sm border border-border/60 px-2.5 py-2 text-[11px] leading-relaxed cursor-pointer">
            <input
              v-model="copyNotes"
              type="checkbox"
              class="mt-0.5 size-3.5 shrink-0 accent-[var(--color-primary)]"
              data-testid="new-branch-copy-notes"
              :disabled="busy"
            />
            <span class="min-w-0">
              <span class="font-bold text-foreground">把我在分支里说的话抄送给主会话</span>
              <span class="block text-muted-foreground">只抄送一行摘要（前 200 字）；主会话的智能体读到时知道这是你在分支里说的。随时可以在分支页的「分支」面板里关掉。</span>
            </span>
          </label>

          <!-- 高级选项：默认收起，都是可选项 -->
          <div class="rounded-sm border border-border/60">
            <button
              type="button"
              class="flex w-full items-center gap-1.5 px-2.5 py-1.5 text-left text-[11px] font-bold text-muted-foreground hover:text-foreground focus:outline-none focus-visible:ring-1 focus-visible:ring-primary/50"
              data-testid="new-branch-advanced-toggle"
              :aria-expanded="advancedOpen"
              aria-controls="new-branch-advanced"
              :disabled="busy"
              @click="advancedOpen = !advancedOpen"
            >
              <ChevronDown v-if="advancedOpen" class="w-3.5 h-3.5 shrink-0" />
              <ChevronRight v-else class="w-3.5 h-3.5 shrink-0" />
              <span>更多选项</span>
              <span class="font-normal text-muted-foreground/80">（可选）</span>
            </button>

            <div v-if="advancedOpen" id="new-branch-advanced" class="space-y-3 border-t border-border/50 px-2.5 py-2.5" data-testid="new-branch-advanced">
              <div class="space-y-1.5">
                <label for="new-branch-first-message" :class="labelClass">首条指令</label>
                <textarea
                  id="new-branch-first-message"
                  v-model="draft.firstMessage"
                  rows="4"
                  :class="[fieldClass, 'min-h-[5rem] max-h-[30vh] resize-y']"
                  placeholder="建好后直接排进分支的收件箱，分支的智能体接入后第一件事就是读到它"
                  data-testid="new-branch-first-message"
                  :aria-invalid="!!errors.firstMessage"
                  :disabled="busy"
                  @keydown.enter.ctrl.prevent="submit"
                  @keydown.enter.meta.prevent="submit"
                />
                <p v-if="errors.firstMessage" class="text-[10px] text-destructive leading-normal">{{ errors.firstMessage }}</p>
                <p v-else class="text-[10px] text-muted-foreground leading-normal">最多 {{ FIRST_MESSAGE_MAX }} 个字符；Ctrl / ⌘ + Enter 直接创建。</p>
              </div>

              <div class="space-y-1.5">
                <label for="new-branch-project-dir" :class="labelClass">项目目录</label>
                <input
                  id="new-branch-project-dir"
                  v-model="draft.projectDirectory"
                  type="text"
                  autocomplete="off"
                  autocapitalize="off"
                  spellcheck="false"
                  list="new-branch-dir-list"
                  :class="fieldClass"
                  placeholder="留空（推荐）"
                  data-testid="new-branch-project-dir"
                  :aria-invalid="!!errors.projectDirectory"
                  :disabled="busy"
                  @keydown.enter.prevent="submit"
                />
                <datalist id="new-branch-dir-list">
                  <option v-for="d in suggestions.dirs" :key="d" :value="d" />
                </datalist>
                <p v-if="errors.projectDirectory" class="text-[10px] text-destructive leading-normal">{{ errors.projectDirectory }}</p>
                <p v-else class="text-[10px] text-muted-foreground leading-normal">
                  默认不绑定：分支的智能体接入后以它自己汇报的目录为准。不要填成和主会话相同的目录——绑定之后，该目录里没带工作流编号的汇报会被并进这个分支。
                </p>
              </div>

              <div class="space-y-1.5">
                <label for="new-branch-host" :class="labelClass">主机</label>
                <input
                  id="new-branch-host"
                  v-model="draft.hostName"
                  type="text"
                  autocomplete="off"
                  autocapitalize="off"
                  spellcheck="false"
                  list="new-branch-host-list"
                  :class="fieldClass"
                  placeholder="留空则用默认主机标签"
                  data-testid="new-branch-host"
                  :aria-invalid="!!errors.hostName"
                  :disabled="busy"
                  @keydown.enter.prevent="submit"
                />
                <datalist id="new-branch-host-list">
                  <option v-for="h in suggestions.hosts" :key="h" :value="h" />
                </datalist>
                <p v-if="errors.hostName" class="text-[10px] text-destructive leading-normal">{{ errors.hostName }}</p>
              </div>
            </div>
          </div>

          <p v-if="error" class="text-[11px] text-destructive leading-normal" role="alert" data-testid="new-branch-error">{{ error }}</p>

          <div class="flex items-center justify-end gap-2 pt-2 border-t border-border/50">
            <Button type="button" variant="ghost" size="sm" class="text-xs" :disabled="busy" @click="onOpenChange(false)">取消</Button>
            <Button type="button" size="sm" class="text-xs px-3" data-testid="new-branch-submit" :disabled="!canSubmit" @click="submit">
              {{ busy ? '创建中…' : '创建分支' }}
            </Button>
          </div>
        </div>
      </template>
    </DialogContent>
  </Dialog>
</template>
