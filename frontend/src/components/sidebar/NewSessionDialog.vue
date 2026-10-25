<script setup lang="ts">
// 网页手动新建会话（#174 第一期；#184 高级选项）：先在网页里建好会话，再把「启动提示词」发给要使用的智能体，
// 省去第一轮「先让智能体把会话建出来」的沟通。
//
// 两步：① 填名字（唯一必填）；想在建会话时就把别的信息填上，展开默认收起的「高级选项」：自己指定的工作流编号、
//        首条指令、起始阶段、项目目录与主机——都是可选项，一项都不填就和以前完全一样；
//      ② 建好后显示启动提示词（可复制）与实际生效的设置，关闭时进入新会话。
// 服务端落一行占位会话（见 internal/store/web_workflow.go），启动提示词同时也是这个会话的第一条消息，随时能再复制。
import { computed, nextTick, reactive, ref, watch } from 'vue'
import { toast } from 'vue-sonner'
import { Check, ChevronDown, ChevronRight, Copy, MessageSquarePlus } from 'lucide-vue-next'
import Dialog from '../ui/dialog/Dialog.vue'
import DialogContent from '../ui/dialog/DialogContent.vue'
import DialogDescription from '../ui/dialog/DialogDescription.vue'
import DialogTitle from '../ui/dialog/DialogTitle.vue'
import Button from '../ui/button/Button.vue'
import { useSessionStore } from '../../stores/session'
import { useSettingsStore } from '../../stores/settings'
import type { CreateWorkflowResult } from '../../api/client'
import type { FeedbackSession } from '../../api/types'
import {
  FIRST_MESSAGE_MAX,
  NEW_SESSION_PHASES,
  NEW_SESSION_RESIDENTS,
  SESSION_NAME_MAX,
  WORKFLOW_ID_MAX,
  WORKFLOW_ID_MIN,
  advancedFilledCount,
  appliedLines,
  blankNewSessionDraft,
  buildCreateRequest,
  collectNewSessionSuggestions,
  warningLines,
  type NewSessionDraft,
  type PhaseChoice,
  type ResidentChoice,
} from '../../lib/newSession'
import { copyText } from '../../lib/terminal/clipboard'

const props = defineProps<{ open: boolean }>()
const emit = defineEmits<{
  (e: 'update:open', val: boolean): void
  /** 建好之后关闭弹窗时触发（不论点「进入会话」还是点遮罩 / Esc 关闭）：调用方据此切到新会话。 */
  (e: 'opened', session: FeedbackSession): void
}>()

const sessionStore = useSessionStore()
const settingsStore = useSettingsStore()

const draft = reactive<NewSessionDraft>(blankNewSessionDraft())
const advancedOpen = ref(false)
const busy = ref(false)
const error = ref('')
/** 服务端回 409（编号已被占用）时的原因：挂在「工作流编号」输入框下面，改动编号就清掉。 */
const idConflict = ref('')
const created = ref<CreateWorkflowResult | null>(null)
const copied = ref(false)
const inputRef = ref<HTMLInputElement | null>(null)

const validation = computed(() => buildCreateRequest(draft, settingsStore.settings.promptTemplates))
const errors = computed(() => (validation.value.ok ? {} : validation.value.errors))
// 名字：只在填了内容又不合规时提示（空着不吓人，按钮灰着就够了）；高级选项里的错误只要有就提示（空的本来就合规）
const nameHint = computed(() => (draft.name.trim() ? (errors.value.name ?? '') : ''))
const idHint = computed(() => idConflict.value || errors.value.workflowId || '')
const advancedErrorCount = computed(
  () => (['workflowId', 'firstMessage', 'projectDirectory', 'hostName'] as const).filter(k => !!errors.value[k]).length,
)
const filledCount = computed(() => advancedFilledCount(draft))
const canSubmit = computed(() => validation.value.ok && !idConflict.value && !busy.value)
const suggestions = computed(() => collectNewSessionSuggestions(sessionStore.sessions))
const appliedList = computed(() => appliedLines(created.value?.applied))
const warningList = computed(() => warningLines(created.value?.warnings))

watch(
  () => props.open,
  isOpen => {
    if (!isOpen) return
    Object.assign(draft, blankNewSessionDraft())
    advancedOpen.value = false
    error.value = ''
    idConflict.value = ''
    created.value = null
    copied.value = false
    busy.value = false
    void nextTick(() => inputRef.value?.focus())
  },
)

watch(
  () => draft.workflowId,
  () => {
    idConflict.value = ''
  },
)

function pickPhase(id: PhaseChoice) {
  draft.phase = draft.phase === id ? '' : id
}

function pickResident(id: ResidentChoice) {
  draft.resident = draft.resident === id ? '' : id
}

async function submit() {
  const v = validation.value
  if (!v.ok || idConflict.value || busy.value) return
  busy.value = true
  error.value = ''
  try {
    created.value = await sessionStore.createWorkflow(v.request)
  } catch (e: any) {
    const msg = e?.response?.data?.error || e?.message || '创建失败，请稍后重试'
    if (e?.response?.status === 409 && draft.workflowId.trim()) {
      // 只有指定的编号会撞：把原因挂到编号输入框上，并保证它是展开的
      idConflict.value = msg
      advancedOpen.value = true
    } else {
      error.value = msg
    }
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
  if (!next && created.value) emit('opened', created.value.session)
  emit('update:open', next)
}

const fieldClass =
  'w-full px-2.5 py-1.5 rounded-sm border border-border/80 bg-background text-sm font-mono text-foreground focus:outline-none focus:ring-1 focus:ring-primary/50'
const labelClass = 'text-[11px] font-bold text-muted-foreground uppercase tracking-wider'
</script>

<template>
  <Dialog :open="open" @update:open="onOpenChange">
    <DialogContent
      class="sm:max-w-lg p-4 bg-card border border-border rounded-md shadow-modal font-mono max-h-[92vh] overflow-y-auto"
      data-testid="new-session-dialog"
    >
      <!-- ② 建好了：启动提示词 -->
      <template v-if="created">
        <DialogTitle class="flex items-center gap-2 text-sm font-bold text-foreground">
          <Check class="w-4 h-4 text-primary shrink-0" />
          <span class="truncate" data-testid="new-session-created-title">已创建「{{ created.session.title }}」</span>
        </DialogTitle>
        <DialogDescription class="text-[11px] text-muted-foreground leading-relaxed">
          把下面的启动提示词发给要使用的智能体：它第一次调用 RelayMesh 时带上这个工作流编号，就会接进这个会话。
        </DialogDescription>

        <div class="space-y-3">
          <ul
            v-if="appliedList.length"
            class="space-y-0.5 rounded-sm border border-border/60 bg-muted/30 px-2.5 py-1.5 text-[11px] leading-relaxed text-foreground"
            data-testid="new-session-applied"
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
            data-testid="new-session-warnings"
          >
            <p v-for="line in warningList" :key="line" class="break-words">{{ line }}</p>
          </div>

          <div class="space-y-1.5">
            <div class="flex items-center justify-between gap-2">
              <span :class="labelClass">启动提示词</span>
              <code class="text-[10px] text-muted-foreground truncate" data-testid="new-session-wfid">{{ created.workflow_id }}</code>
            </div>
            <textarea
              readonly
              rows="11"
              class="w-full min-h-[10rem] max-h-[40vh] resize-y rounded-sm border border-border/80 bg-background px-2.5 py-1.5 text-[11px] leading-relaxed font-mono text-foreground focus:outline-none focus:ring-1 focus:ring-primary/50"
              data-testid="new-session-prompt"
              :value="created.start_prompt"
              @focus="($event.target as HTMLTextAreaElement).select()"
            />
            <p class="text-[10px] text-muted-foreground leading-normal">
              也可以先在会话页下方的输入框写好第一条指令，智能体接入后会在收件箱里读到。这段提示词同时留在了会话的第一条消息里，随时能再复制。
            </p>
          </div>

          <div class="flex items-center justify-end gap-2 pt-2 border-t border-border/50">
            <Button type="button" variant="outline" size="sm" class="text-xs gap-1.5" data-testid="new-session-copy" @click="copyPrompt">
              <Check v-if="copied" class="w-3.5 h-3.5 text-primary" />
              <Copy v-else class="w-3.5 h-3.5" />
              {{ copied ? '已复制' : '复制启动提示词' }}
            </Button>
            <Button type="button" size="sm" class="text-xs px-3" data-testid="new-session-open" @click="onOpenChange(false)">
              进入会话
            </Button>
          </div>
        </div>
      </template>

      <!-- ① 填名字（+ 默认收起的高级选项） -->
      <template v-else>
        <DialogTitle class="flex items-center gap-2 text-sm font-bold text-foreground">
          <MessageSquarePlus class="w-4 h-4 text-primary shrink-0" />
          <span>新建会话</span>
        </DialogTitle>
        <DialogDescription class="text-[11px] text-muted-foreground leading-relaxed">
          先在网页里建好会话，再把启动提示词发给要使用的智能体——它接入后直接在这个会话里工作，不用再来一轮「先建会话」的沟通。
        </DialogDescription>

        <div class="space-y-3">
          <div class="space-y-1.5">
            <label for="new-session-name" :class="labelClass">会话名称</label>
            <input
              id="new-session-name"
              ref="inputRef"
              v-model="draft.name"
              type="text"
              autocomplete="off"
              :class="fieldClass"
              placeholder="例如：支付模块重构"
              data-testid="new-session-name"
              :aria-invalid="!!nameHint"
              :disabled="busy"
              @keydown.enter.prevent="submit"
            />
            <p v-if="nameHint" class="text-[10px] text-destructive leading-normal" data-testid="new-session-hint">{{ nameHint }}</p>
            <p v-else class="text-[10px] text-muted-foreground leading-normal">
              最多 {{ SESSION_NAME_MAX }} 个字符。名称建好后固定，之后可在侧栏里重命名。
            </p>
          </div>

          <!-- 高级选项：默认收起，都是可选项 -->
          <div class="rounded-sm border border-border/60">
            <button
              type="button"
              class="flex w-full items-center gap-1.5 px-2.5 py-1.5 text-left text-[11px] font-bold text-muted-foreground hover:text-foreground focus:outline-none focus-visible:ring-1 focus-visible:ring-primary/50"
              data-testid="new-session-advanced-toggle"
              :aria-expanded="advancedOpen"
              aria-controls="new-session-advanced"
              :disabled="busy"
              @click="advancedOpen = !advancedOpen"
            >
              <ChevronDown v-if="advancedOpen" class="w-3.5 h-3.5 shrink-0" />
              <ChevronRight v-else class="w-3.5 h-3.5 shrink-0" />
              <span>高级选项</span>
              <span class="font-normal text-muted-foreground/80">（可选）</span>
              <span
                v-if="advancedErrorCount && !advancedOpen"
                class="ml-auto font-normal text-destructive"
                data-testid="new-session-advanced-errors"
              >有 {{ advancedErrorCount }} 项需要修改</span>
              <span v-else-if="filledCount && !advancedOpen" class="ml-auto font-normal text-primary" data-testid="new-session-advanced-filled">
                已填 {{ filledCount }} 项
              </span>
            </button>

            <div v-if="advancedOpen" id="new-session-advanced" class="space-y-3 border-t border-border/50 px-2.5 py-2.5" data-testid="new-session-advanced">
              <div class="space-y-1.5">
                <label for="new-session-workflow-id" :class="labelClass">工作流编号</label>
                <input
                  id="new-session-workflow-id"
                  v-model="draft.workflowId"
                  type="text"
                  autocomplete="off"
                  autocapitalize="off"
                  spellcheck="false"
                  :class="fieldClass"
                  placeholder="留空则自动生成"
                  data-testid="new-session-workflow-id"
                  :aria-invalid="!!idHint"
                  :disabled="busy"
                  @keydown.enter.prevent="submit"
                />
                <p v-if="idHint" class="text-[10px] text-destructive leading-normal" data-testid="new-session-field-error-workflowId">{{ idHint }}</p>
                <p v-else class="text-[10px] text-muted-foreground leading-normal">
                  {{ WORKFLOW_ID_MIN }}～{{ WORKFLOW_ID_MAX }} 个字符，字母或数字开头，可含 . _ -；智能体第一次调用时填这个编号接入。
                </p>
              </div>

              <div class="space-y-1.5">
                <label for="new-session-first-message" :class="labelClass">首条指令</label>
                <textarea
                  id="new-session-first-message"
                  v-model="draft.firstMessage"
                  rows="4"
                  :class="[fieldClass, 'min-h-[5rem] max-h-[30vh] resize-y']"
                  placeholder="建好后直接排进收件箱，智能体接入后第一件事就是读到它"
                  data-testid="new-session-first-message"
                  :aria-invalid="!!errors.firstMessage"
                  :disabled="busy"
                  @keydown.enter.ctrl.prevent="submit"
                  @keydown.enter.meta.prevent="submit"
                />
                <p v-if="errors.firstMessage" class="text-[10px] text-destructive leading-normal" data-testid="new-session-field-error-firstMessage">
                  {{ errors.firstMessage }}
                </p>
                <p v-else class="text-[10px] text-muted-foreground leading-normal">
                  最多 {{ FIRST_MESSAGE_MAX }} 个字符；Ctrl / ⌘ + Enter 直接创建。
                </p>
              </div>

              <div class="space-y-1.5">
                <span :class="labelClass" id="new-session-phase-label">起始阶段</span>
                <div class="flex flex-wrap gap-1.5" role="radiogroup" aria-labelledby="new-session-phase-label">
                  <button
                    v-for="p in NEW_SESSION_PHASES"
                    :key="p.id || 'none'"
                    type="button"
                    role="radio"
                    :aria-checked="draft.phase === p.id"
                    :title="p.hint"
                    class="rounded-sm border px-2.5 py-1 text-[11px] focus:outline-none focus-visible:ring-1 focus-visible:ring-primary/50"
                    :class="draft.phase === p.id
                      ? 'border-primary bg-primary/10 text-foreground font-bold'
                      : 'border-border/80 bg-background text-muted-foreground hover:text-foreground'"
                    :data-testid="`new-session-phase-${p.id || 'none'}`"
                    :disabled="busy"
                    @click="pickPhase(p.id)"
                  >{{ p.label }}</button>
                </div>
                <p class="text-[10px] text-muted-foreground leading-normal">
                  讨论、方案阶段智能体只读不改文件；开发阶段才动手实现。不指定则沿用默认阶段。
                </p>
              </div>

              <div class="space-y-1.5">
                <span :class="labelClass" id="new-session-resident-label">常驻规则</span>
                <div class="flex flex-wrap gap-1.5" role="radiogroup" aria-labelledby="new-session-resident-label">
                  <button
                    v-for="r in NEW_SESSION_RESIDENTS"
                    :key="r.id || 'none'"
                    type="button"
                    role="radio"
                    :aria-checked="draft.resident === r.id"
                    :title="r.hint"
                    class="rounded-sm border px-2.5 py-1 text-[11px] focus:outline-none focus-visible:ring-1 focus-visible:ring-primary/50"
                    :class="draft.resident === r.id
                      ? 'border-primary bg-primary/10 text-foreground font-bold'
                      : 'border-border/80 bg-background text-muted-foreground hover:text-foreground'"
                    :data-testid="`new-session-resident-${r.id || 'none'}`"
                    :disabled="busy"
                    @click="pickResident(r.id)"
                  >{{ r.label }}</button>
                </div>
                <p class="text-[10px] text-muted-foreground leading-normal">
                  把模板文字复制进新会话，作为它的常驻规则：智能体每次回执都会读到，不能放宽授权档位。模板文字可在「设置 → 会话提示词」里改；建好后随时能在会话输入区的「常驻」里调整。
                </p>
              </div>

              <div class="space-y-1.5">
                <label for="new-session-project-dir" :class="labelClass">项目目录</label>
                <input
                  id="new-session-project-dir"
                  v-model="draft.projectDirectory"
                  type="text"
                  autocomplete="off"
                  autocapitalize="off"
                  spellcheck="false"
                  list="new-session-dir-list"
                  :class="fieldClass"
                  placeholder="例如：/home/me/projects/app（绝对路径）"
                  data-testid="new-session-project-dir"
                  :aria-invalid="!!errors.projectDirectory"
                  :disabled="busy"
                  @keydown.enter.prevent="submit"
                />
                <datalist id="new-session-dir-list">
                  <option v-for="d in suggestions.dirs" :key="d" :value="d" />
                </datalist>
                <p v-if="errors.projectDirectory" class="text-[10px] text-destructive leading-normal" data-testid="new-session-field-error-projectDirectory">
                  {{ errors.projectDirectory }}
                </p>
                <p v-else class="text-[10px] text-muted-foreground leading-normal">
                  填了就把这个会话绑定到该目录：智能体第一次汇报必须带同一个目录，换目录会被拒绝；「启用 Agent」也会默认用它；之后该目录里没带工作流编号的汇报也会被并进来（它成了这个目录最近的工作流）。不填则沿用智能体第一次汇报的目录。
                </p>
              </div>

              <div class="space-y-1.5">
                <label for="new-session-host" :class="labelClass">主机</label>
                <input
                  id="new-session-host"
                  v-model="draft.hostName"
                  type="text"
                  autocomplete="off"
                  autocapitalize="off"
                  spellcheck="false"
                  list="new-session-host-list"
                  :class="fieldClass"
                  placeholder="留空则用默认主机标签"
                  data-testid="new-session-host"
                  :aria-invalid="!!errors.hostName"
                  :disabled="busy"
                  @keydown.enter.prevent="submit"
                />
                <datalist id="new-session-host-list">
                  <option v-for="h in suggestions.hosts" :key="h" :value="h" />
                </datalist>
                <p v-if="errors.hostName" class="text-[10px] text-destructive leading-normal" data-testid="new-session-field-error-hostName">
                  {{ errors.hostName }}
                </p>
                <p v-else class="text-[10px] text-muted-foreground leading-normal">
                  会话归属的主机标签，侧栏按它分组；智能体接入后以它自己凭据绑定的主机为准。
                </p>
              </div>
            </div>
          </div>

          <p v-if="error" class="text-[11px] text-destructive leading-normal" role="alert" data-testid="new-session-error">{{ error }}</p>

          <div class="flex items-center justify-end gap-2 pt-2 border-t border-border/50">
            <Button type="button" variant="ghost" size="sm" class="text-xs" :disabled="busy" @click="onOpenChange(false)">取消</Button>
            <Button type="button" size="sm" class="text-xs px-3" data-testid="new-session-submit" :disabled="!canSubmit" @click="submit">
              {{ busy ? '创建中…' : '创建会话' }}
            </Button>
          </div>
        </div>
      </template>
    </DialogContent>
  </Dialog>
</template>
