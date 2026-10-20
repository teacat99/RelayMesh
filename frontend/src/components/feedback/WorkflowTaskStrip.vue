<script setup lang="ts">
// 会话页页头下方的一行「项目任务总览」：这个项目（工作流）一共有多少任务、阶段与状态的分布、整体完成度。
// 点一下进入任务清单整页。没有任务时不出现。数据与每一轮下方的「本轮任务」同源（任务 store 的 feed）。
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import { ChevronRight, ListChecks } from 'lucide-vue-next'
import WorkTrackSummary from '../work/WorkTrackSummary.vue'
import { useWorkItemStore } from '../../stores/workItems'

const store = useWorkItemStore()
const router = useRouter()

const total = computed(() => store.feed.items.length)
const visible = computed(() => !!store.feed.workflowId && store.feed.loaded && total.value > 0)

function openWork() {
  if (!store.feed.workflowId) return
  void router.push({ name: 'work', params: { workflowId: store.feed.workflowId } })
}
</script>

<template>
  <button
    v-if="visible"
    type="button"
    class="flex w-full shrink-0 cursor-pointer items-center gap-2 border-b border-border/60 bg-muted/20 px-3 py-1.5 text-left transition-colors hover:bg-muted/40 focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-inset focus-visible:ring-primary/50 sm:px-6"
    title="打开这个项目的任务清单"
    data-testid="workflow-task-strip"
    :data-total="total"
    @click="openWork"
  >
    <ListChecks class="size-3.5 shrink-0 text-muted-foreground" />
    <span class="shrink-0 text-xs font-medium text-foreground">项目任务</span>
    <span class="shrink-0 rounded-xs bg-muted px-1.5 py-px font-mono text-[10px] tabular-nums text-foreground/80">{{ total }}</span>
    <span class="ml-1 min-w-0"><WorkTrackSummary :items="store.feed.items" compact /></span>
    <span class="ml-auto hidden shrink-0 items-center gap-0.5 text-[11px] text-muted-foreground sm:inline-flex">任务清单<ChevronRight class="size-3" /></span>
  </button>
</template>
