<script setup lang="ts">
// 会话的「唤醒词」页签（#232 D3，D-376）：这个会话被 Hub 叫醒时（续聊 / 新开对话）给智能体的那段话，可以按会话单独写。
// 分层（谁在前谁生效）：启动器预设里自己写的正文 > 这里的会话模板 > 设置页的全局覆盖 > 内置默认。
// 留空 = 不覆盖，用下一层。界面里没有历史版本与回滚（和常驻规则不同）：改的是「怎么叫醒」，不是智能体每次都要读的规矩；后台按版本留了最近 10 份记录，暂无界面。
import { computed, ref, watch } from 'vue'
import { toast } from '@/components/ui/sonner'
import { promptsErrorMessage } from '@/api/workflowPrompts'
import { useWakePromptsStore } from '@/stores/wakePrompts'
import { useWorkflowPromptsStore } from '@/stores/workflowPrompts'
import { wakeDirty, wakeTemplateProblem } from '@/lib/wakePrompts'
import WakeTemplateField from './WakeTemplateField.vue'

const props = defineProps<{ workflowId: string }>()
const emit = defineEmits<{ (e: 'saved'): void }>()

const store = useWorkflowPromptsStore()
const wakeStore = useWakePromptsStore()
const view = computed(() => store.view(props.workflowId))

const resume = ref('')
const created = ref('')
/** 用户在这个页签里改过哪一项：之后到达的服务端状态不再覆盖他正在写的文字。 */
const touched = ref({ resume: false, new: false })
const formError = ref('')
const refreshKey = ref(0)

function sync() {
  const v = view.value
  if (!touched.value.resume) resume.value = v?.wake_resume ?? ''
  if (!touched.value.new) created.value = v?.wake_new ?? ''
}
watch(
  () => [view.value?.wake_resume, view.value?.wake_new],
  () => sync(),
  { immediate: true },
)
watch(
  () => props.workflowId,
  () => {
    touched.value = { resume: false, new: false }
    formError.value = ''
    sync()
  },
)

const resumeDirty = computed(() => wakeDirty(resume.value, view.value?.wake_resume ?? ''))
const newDirty = computed(() => wakeDirty(created.value, view.value?.wake_new ?? ''))
const resumeProblem = computed(() => wakeTemplateProblem('resume', resume.value, wakeStore.max))
const newProblem = computed(() => wakeTemplateProblem('new', created.value, wakeStore.max))
const dirty = computed(() => resumeDirty.value || newDirty.value)
const canSave = computed(() => dirty.value && !resumeProblem.value && !newProblem.value && !store.busy)

function setResume(v: string) {
  resume.value = v
  touched.value.resume = true
  formError.value = ''
}
function setNew(v: string) {
  created.value = v
  touched.value.new = true
  formError.value = ''
}

async function save() {
  if (!canSave.value) return
  formError.value = ''
  const patch: { wake_resume?: string; wake_new?: string } = {}
  if (resumeDirty.value) patch.wake_resume = resume.value
  if (newDirty.value) patch.wake_new = created.value
  try {
    const res = await store.saveWake(props.workflowId, patch)
    touched.value = { resume: false, new: false }
    resume.value = res.view.wake_resume ?? ''
    created.value = res.view.wake_new ?? ''
    refreshKey.value++
    toast.success(res.changed ? '已保存唤醒词模板' : '内容没有变化', {
      description: res.changed ? '下一次 Hub 叫醒这个会话时就会用它。' : undefined,
      duration: 2200,
    })
    emit('saved')
  } catch (err) {
    formError.value = promptsErrorMessage(err, '保存失败')
  }
}

const supported = computed(() => view.value?.wake_resume !== undefined)
</script>

<template>
  <div class="space-y-4" data-testid="wake-panel">
    <p class="text-[11px] text-muted-foreground leading-relaxed">
      Hub 在<b class="text-foreground">叫醒</b>这个会话的智能体时，会发一段话给它。这里可以给<b class="text-foreground">这一个会话</b>单独写；
      留空就用下一层。优先级从高到低：
      <b class="text-foreground">启动器预设里自己写的正文</b> → <b class="text-foreground">这里的会话模板</b> →
      <b class="text-foreground">设置 › 会话提示词 里的全局覆盖</b> → <b class="text-foreground">内置默认</b>。
      文字里可以用 <code class="px-1 rounded-xs bg-muted">%变量</code>，发出那一刻由 Hub 换成这个会话当时的真实情况（常驻规则、未读追加、未完成任务、下一步……）。
      授权档位不会因此放宽：回执里的 auth 行永远以更严的为准。
    </p>

    <p v-if="view && !supported" class="text-[11px] text-amber-600 dark:text-amber-400" data-testid="wake-unsupported">
      这台 Hub 的版本还不支持会话级唤醒词模板，升级后再来。
    </p>
    <template v-else>
      <WakeTemplateField
        kind="resume"
        :model-value="resume"
        :workflow-id="workflowId"
        :refresh-key="refreshKey"
        testid="wake-resume"
        empty-hint="留空：用全局覆盖，没有就用内置默认。例如：先读收件箱（%inbox 条未读），再按任务清单继续。"
        @update:model-value="setResume"
      />
      <WakeTemplateField
        kind="new"
        :model-value="created"
        :workflow-id="workflowId"
        :refresh-key="refreshKey"
        testid="wake-new"
        empty-hint="留空：用全局覆盖，没有就用内置默认。必须包含 %workflow：新对话靠这个编号接入会话。"
        @update:model-value="setNew"
      />

      <p v-if="formError" class="text-[11px] text-destructive" data-testid="wake-error">{{ formError }}</p>
      <div class="flex items-center gap-2 pt-1">
        <button
          type="button"
          data-testid="wake-save"
          :disabled="!canSave"
          class="px-3 h-7 rounded-xs bg-primary text-primary-foreground text-[11px] font-semibold cursor-pointer hover:brightness-110 disabled:opacity-50 disabled:cursor-not-allowed"
          @click="save"
        >{{ store.busy ? '保存中…' : '保存唤醒词' }}</button>
        <span v-if="dirty" class="text-[10px] text-amber-600 dark:text-amber-400" data-testid="wake-dirty">有未保存的改动</span>
      </div>
    </template>
  </div>
</template>
