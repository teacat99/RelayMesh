<script setup lang="ts">
// 输入区工具栏上的「常驻」小标签（#232）：这个会话有没有常驻规则、智能体读到没有；点开是编辑弹窗。
// 同步与异步会话都显示——常驻规则与授权档位、阶段滑块是各自独立的通道。
import { computed, watch } from 'vue'
import { ScrollText } from 'lucide-vue-next'
import ResidentPromptDialog from './ResidentPromptDialog.vue'
import { residentChipText, residentChipTitle, residentReadState } from '@/lib/residentPrompt'
import { useWorkflowPromptsStore } from '@/stores/workflowPrompts'

const props = defineProps<{ workflowId: string }>()

const store = useWorkflowPromptsStore()
// 弹窗开关放在 store 里：手机页头「⋯」菜单里的「常驻规则」条目打开的是同一个弹窗
const open = computed({
  get: () => store.dialogFor === props.workflowId,
  set: (v: boolean) => {
    if (v) store.openDialog(props.workflowId)
    else if (store.dialogFor === props.workflowId) store.closeDialog()
  },
})

const view = computed(() => store.view(props.workflowId))
const state = computed(() => residentReadState(view.value))

watch(
  () => props.workflowId,
  id => {
    if (id) void store.load(id)
    if (store.dialogFor && store.dialogFor !== id) store.closeDialog()
  },
  { immediate: true },
)

const dotClass = computed(() => {
  switch (state.value) {
    case 'unread':
      return 'bg-amber-500 animate-pulse'
    case 'read':
      return 'bg-emerald-500'
    default:
      return 'bg-muted-foreground/40'
  }
})
</script>

<template>
  <button
    type="button"
    data-testid="resident-chip"
    :data-state="state"
    :title="residentChipTitle(view)"
    :aria-label="`常驻规则 ${residentChipText(view)}`"
    class="text-[11px] sm:text-xs px-2 sm:px-2.5 py-1 rounded-sm border transition-all shadow-2xs flex items-center gap-1.5 cursor-pointer shrink-0 bg-card/95 hover:bg-muted border-border/80 text-foreground backdrop-blur-xs select-none font-mono outline-none focus-visible:ring-1 focus-visible:ring-ring"
    :class="state === 'unset' ? 'text-muted-foreground' : ''"
    @click="store.openDialog(workflowId)"
  >
    <ScrollText class="w-3.5 h-3.5 text-primary" />
    <span data-testid="resident-chip-text">{{ residentChipText(view) }}</span>
    <span class="w-1.5 h-1.5 rounded-full shrink-0" :class="dotClass" aria-hidden="true" />
  </button>
  <ResidentPromptDialog v-model:open="open" :workflow-id="workflowId" />
</template>
