<script setup lang="ts">
// 会话页页头下方的一行「项目任务总览」：这个项目（工作流）一共有多少任务、阶段与状态的分布、整体完成度。
// 点一下进入任务清单整页。没有任务时不出现。数据与每一轮下方的「本轮任务」同源（任务 store 的 feed）。
//
// 占位（#246）：任务清单要等整张会话表让路之后才取（lib/secondaryGate.ts），生产上比第一屏晚到 0.2 ～ 1.5 秒，晚到的那一刻
// 整张会话列表被推下去 30px（浏览器还会把同一帧里新挂载的右侧导轨、末尾容器算成「从原点移过来」，版面位移分数放大几十倍）。
// 所以上次这个项目有任务时，清单还在路上就先画一个同样大小的占位（lib/shellHints.ts 记着有无任务），取到之后原地换成真内容；
// 上次没有任务的项目不占位（不留空带）；记忆过期（任务被删光）、取到的是空清单时占位收起并改写记忆。第一次来的项目没有记忆，仍会晚到一次。
//
// 深链接冷打开（点通知里的 ?wf=… 链接）：目标工作流要等简略会话列表回来才解析出来，那之前 store.feed.workflowId 是空的（FeedbackView
// 在深链处理完之前不为占位会话取任务清单，#169）。占位的键因此是 shellWorkflowKey(当前工作流, 地址上深链指向的 wf)，第一帧就能画出来；
// 「取到了没有」只认 store.feed 确实是这个键的工作流的那一份（不会拿占位会话的清单顶替目标的）。
import { computed, watch } from 'vue'
import { useRouter } from 'vue-router'
import { ChevronRight, ListChecks } from 'lucide-vue-next'
import WorkTrackSummary from '../work/WorkTrackSummary.vue'
import { usePendingDeepLinkWf } from '../../composables/usePendingDeepLinkWf'
import { patchShellHint, readShellHint, shellWorkflowKey, stripMode } from '../../lib/shellHints'
import { useWorkItemStore } from '../../stores/workItems'

const store = useWorkItemStore()
const router = useRouter()
const pendingWf = usePendingDeepLinkWf()

/** 占位与真内容共用的盒子：同一组样式，高度才一致（e2e shell-shift 比两者的高）。 */
const boxClass = 'flex w-full shrink-0 items-center gap-2 border-b border-border/60 bg-muted/20 px-3 py-1.5 sm:px-6'
/** 只有真内容才是可点的。 */
const activeClass = 'cursor-pointer text-left transition-colors hover:bg-muted/40 focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-inset focus-visible:ring-primary/50'

const total = computed(() => store.feed.items.length)
// 占位的键：当前工作流，深链接冷打开时是地址上深链指向的 wf
const hintKey = computed(() => shellWorkflowKey(store.feed.workflowId, pendingWf.value))
// store.feed 是不是这个键的工作流的那一份（深链解析之前 feed.workflowId 为空，不是）
const feedIsKey = computed(() => !!hintKey.value && store.feed.workflowId === hintKey.value)
// 上次这个项目有多少任务：只依赖键，换工作流时读一次；取到任务之后记忆改写成这次的数（下面的 watch）。
const hinted = computed(() => readShellHint(hintKey.value)?.tasks ?? 0)
const mode = computed(() =>
  stripMode({
    workflowId: hintKey.value,
    loaded: feedIsKey.value && store.feed.loaded,
    error: feedIsKey.value ? store.feed.error : '',
    total: feedIsKey.value ? total.value : 0,
    hinted: hinted.value,
  }),
)

watch(
  () => [store.feed.workflowId, store.feed.loaded, total.value] as const,
  ([workflowId, loaded, n]) => {
    if (workflowId && loaded) patchShellHint(workflowId, { tasks: n })
  },
  { immediate: true },
)

function openWork() {
  if (!store.feed.workflowId) return
  void router.push({ name: 'work', params: { workflowId: store.feed.workflowId } })
}
</script>

<template>
  <button
    v-if="mode === 'ready'"
    type="button"
    :class="[boxClass, activeClass]"
    title="打开这个项目的任务清单"
    data-testid="workflow-task-strip"
    data-prefetch="work"
    :data-total="total"
    @click="openWork"
  >
    <ListChecks class="size-3.5 shrink-0 text-muted-foreground" />
    <span class="shrink-0 text-xs font-medium text-foreground">项目任务</span>
    <span class="shrink-0 rounded-xs bg-muted px-1.5 py-px font-mono text-[10px] tabular-nums text-foreground/80">{{ total }}</span>
    <span class="ml-1 min-w-0"><WorkTrackSummary :items="store.feed.items" compact /></span>
    <span class="ml-auto hidden shrink-0 items-center gap-0.5 text-[11px] text-muted-foreground sm:inline-flex">任务清单<ChevronRight class="size-3" /></span>
  </button>
  <!-- 占位：任务还在路上，位置先占住。里面的静态部分与真内容一样，只有总览条换成骨架；不可点、对读屏隐藏，也不带 data-prefetch。 -->
  <div v-else-if="mode === 'reserved'" :class="boxClass" aria-hidden="true" data-testid="workflow-task-strip-reserved" :data-total="hinted">
    <ListChecks class="size-3.5 shrink-0 text-muted-foreground" />
    <span class="shrink-0 text-xs font-medium text-foreground">项目任务</span>
    <span class="shrink-0 rounded-xs bg-muted px-1.5 py-px font-mono text-[10px] tabular-nums text-foreground/80">{{ hinted }}</span>
    <span class="ml-1 h-1.5 w-40 min-w-0 rounded-full bg-muted/70 motion-safe:animate-pulse"></span>
    <span class="ml-auto hidden shrink-0 items-center gap-0.5 text-[11px] text-muted-foreground sm:inline-flex">任务清单<ChevronRight class="size-3" /></span>
  </div>
</template>
