<script setup lang="ts">
// 「全部采用推荐」提交前的最后一眼：把将要提交的每一项结果逐条列出来（哪一项、选了什么）。
// 待确认区与文档页顶部共用；不可逆（L2）的项不会出现在这里，它们必须逐条确认。
import { ref, watch } from 'vue'
import { CheckCheck } from 'lucide-vue-next'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '../ui/dialog'
import Button from '../ui/button/Button.vue'
import { KIND_LABEL, type AdoptPreviewRow } from '../../lib/confirms'

const props = defineProps<{
  open: boolean
  rows: AdoptPreviewRow[]
  busy?: boolean
  /** 提交失败时服务端的说明（批量失败会逐条列出原因，整批都没有保存）。 */
  error?: string
}>()

const emit = defineEmits<{
  (e: 'update:open', v: boolean): void
  (e: 'confirm'): void
}>()

// 提交中与淡出收起期间保持列表不变，免得提交成功后列表先变空再关
const shown = ref<AdoptPreviewRow[]>([...props.rows])
watch(
  () => props.rows,
  rows => {
    if (props.open && !props.busy) shown.value = [...rows]
  },
)
watch(
  () => props.open,
  open => {
    if (open) shown.value = [...props.rows]
  },
)
</script>

<template>
  <Dialog :open="open" @update:open="(v: boolean) => emit('update:open', v)">
    <DialogContent class="max-w-xl gap-3 p-5" data-testid="confirm-batch-dialog">
      <DialogHeader class="space-y-1 pr-6">
        <DialogTitle class="text-sm font-semibold">采用推荐并提交 {{ shown.length }} 项</DialogTitle>
        <DialogDescription class="text-xs leading-relaxed text-muted-foreground">
          下面是将要提交的全部结果；整批要么全部生效、要么都不保存。不可逆（L2）或没有推荐选项的项不在其中，需要逐条确认。
        </DialogDescription>
      </DialogHeader>

      <ul class="max-h-[50vh] divide-y divide-border/60 overflow-y-auto rounded-md border border-border/70 bg-muted/10" data-testid="confirm-batch-list">
        <li v-for="r in shown" :key="r.ref || r.title" class="space-y-0.5 px-3 py-2" :data-testid="`confirm-batch-row-${r.ref}`">
          <div class="flex items-start gap-2">
            <span class="mt-px shrink-0 font-mono text-[11px] font-semibold text-foreground/70">{{ r.ref || '—' }}</span>
            <span class="min-w-0 flex-1 break-words text-[13px] font-medium leading-snug text-foreground">{{ r.title }}</span>
            <span class="shrink-0 text-[10px] text-muted-foreground">{{ KIND_LABEL[r.kind] }}</span>
          </div>
          <p class="break-words pl-6 text-xs leading-relaxed text-foreground/80">→ {{ r.choice }}</p>
        </li>
      </ul>

      <p v-if="error" class="whitespace-pre-line rounded-xs border border-destructive/40 bg-destructive/5 px-2.5 py-1.5 text-xs text-destructive" role="alert" data-testid="confirm-batch-error">{{ error }}</p>

      <DialogFooter class="gap-2 pt-1 sm:gap-2">
        <Button type="button" variant="ghost" size="sm" class="h-8 text-xs" :disabled="busy" data-testid="confirm-batch-cancel" @click="emit('update:open', false)">取消</Button>
        <Button type="button" size="sm" class="h-8 gap-1 text-xs" :disabled="busy || !shown.length" data-testid="confirm-batch-submit" @click="emit('confirm')">
          <CheckCheck class="size-3.5" />{{ busy ? '提交中…' : `提交 ${shown.length} 项` }}
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
