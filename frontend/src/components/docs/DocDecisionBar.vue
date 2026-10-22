<script setup lang="ts">
// 文档页顶部的「待决策」条：这篇文档里还有几张决策卡等你拍板，并给出一键「采用全部推荐」。
// 提交前的确认对话框会列出将要提交的全部结果；L2 与没有推荐选项的卡不在其中，需要在卡片上逐张选择。
import { computed, ref } from 'vue'
import { toast } from 'vue-sonner'
import { Sparkles } from 'lucide-vue-next'
import Button from '../ui/button/Button.vue'
import ConfirmBatchDialog from '../confirms/ConfirmBatchDialog.vue'
import { confirmErrorMessage } from '../../api/confirms'
import { decisionAdoptPreview, summarizeDecisions, type DocDecisionView } from '@/lib/confirms'
import { useProjectDocStore } from '@/stores/projectDocs'

const props = defineProps<{ workflowId: string; decisions: DocDecisionView[]; historic?: boolean }>()

const store = useProjectDocStore()
const summary = computed(() => summarizeDecisions(props.decisions, props.historic))
const rows = computed(() => decisionAdoptPreview(summary.value.adoptable))
const open = ref(false)
const busy = ref(false)
const error = ref('')

function openDialog() {
  error.value = ''
  open.value = true
}

async function submit() {
  busy.value = true
  error.value = ''
  try {
    const refs = summary.value.adoptable.map(v => v.confirm_ref!).filter(Boolean)
    const done = await store.adoptDecisions(props.workflowId, refs)
    open.value = false
    toast.success(`已采用推荐，提交 ${done.length} 项`, { duration: 2500 })
  } catch (err) {
    error.value = confirmErrorMessage(err)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div
    v-if="summary.pending > 0"
    class="mb-4 flex flex-wrap items-center gap-x-3 gap-y-1.5 rounded-xs border border-amber-500/40 bg-amber-500/5 px-3 py-2 text-xs"
    data-testid="doc-decision-bar"
    :data-pending="summary.pending"
  >
    <span class="font-semibold text-foreground">待决策 {{ summary.pending }}</span>
    <span class="text-muted-foreground">这篇文档里还有 {{ summary.pending }} 张决策卡等你拍板<template v-if="summary.answered">，已作答 {{ summary.answered }} 张</template>。</span>
    <Button
      v-if="summary.adoptable.length > 0"
      type="button"
      size="sm"
      variant="outline"
      class="ml-auto h-7 gap-1 border-amber-500/40 px-2 text-[11px]"
      data-testid="doc-decision-adopt-all"
      @click="openDialog"
    >
      <Sparkles class="size-3" />一键采用全部推荐（{{ summary.adoptable.length }}）
    </Button>
    <ConfirmBatchDialog v-model:open="open" :rows="rows" :busy="busy" :error="error" @confirm="submit" />
  </div>
</template>
