<script setup lang="ts">
// 确认卡上的关联入口：绑定的任务（#12）与来源文档。待答复的卡与回看已处理的卡共用，
// 这样「点文档跳到哪」只有一处要维护。testid 前缀由调用方给（待答复卡是 confirm-Q5，回看是 confirm-tl-Q5）。
import { useRouter } from 'vue-router'
import { BookOpen, ListChecks } from 'lucide-vue-next'
import type { ConfirmItem } from '../../api/confirms'
import { useTaskRefs } from '../../composables/useTaskRefs'
import { openDoc as navigateToDoc } from '../../lib/docNavClient'

const props = defineProps<{ item: ConfirmItem; workflowId: string; testid: string }>()

const router = useRouter()
const taskRefs = useTaskRefs(() => props.workflowId)

// 属于项目的工作流进项目内的文档页，否则老文档页（统一出口见 lib/docNavClient）。
function openDoc() {
  if (!props.item.doc_path) return
  void navigateToDoc(router, { workflowId: props.workflowId, path: props.item.doc_path, anchor: props.item.anchor })
}
</script>

<template>
  <div v-if="item.task_seq || item.doc_path" class="flex flex-wrap items-center gap-2 text-[11px]">
    <button
      v-if="item.task_seq"
      type="button"
      class="inline-flex cursor-pointer items-center gap-1 rounded-xs border border-border/70 px-1.5 py-0.5 text-muted-foreground hover:border-primary/40 hover:text-primary"
      :title="item.kind === 'plan' ? '这份计划绑定的任务' : '关联的任务'"
      :data-testid="`${testid}-task`"
      @click="taskRefs.open(item.task_seq!)"
    >
      <ListChecks class="size-3" />#{{ item.task_seq }}
    </button>
    <button
      v-if="item.doc_path"
      type="button"
      class="inline-flex max-w-full cursor-pointer items-center gap-1 rounded-xs border border-border/70 px-1.5 py-0.5 text-muted-foreground hover:border-primary/40 hover:text-primary"
      :title="item.doc_title || item.doc_path"
      :data-testid="`${testid}-doc`"
      @click="openDoc"
    >
      <BookOpen class="size-3 shrink-0" /><span class="truncate">{{ item.doc_title || item.doc_path }}</span>
    </button>
  </div>
</template>
