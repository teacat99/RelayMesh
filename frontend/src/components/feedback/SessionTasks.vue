<script setup lang="ts">
// 会话页里「每一轮」下方的可展开任务条：这一轮创建、推进过哪些任务，每个任务属于哪个模块 / 批次，
// 这一轮对它做了什么（本轮标记），以及它现在的整体进度（阶段 + 状态双进度条）。
// 数据来自任务 store 的 feed（整个工作流一次取回），没有任务的轮次什么也不渲染。
import { computed, ref } from 'vue'
import { ChevronRight, ListChecks } from 'lucide-vue-next'
import WorkTrack from '../work/WorkTrack.vue'
import WorkTrackSummary from '../work/WorkTrackSummary.vue'
import { useWorkItemStore } from '../../stores/workItems'
import { describeSessionCtx, displayModulePath, workRef } from '../../lib/workItems'

const props = defineProps<{ sessionId: string }>()

const store = useWorkItemStore()

const tasks = computed(() => store.feedBySession.get(props.sessionId) ?? [])
const items = computed(() => tasks.value.map(t => t.item))
const createdCount = computed(() => tasks.value.filter(t => t.ctx.created).length)
const touchedCount = computed(() => tasks.value.length - createdCount.value)

const open = ref(false)
// 第一次展开才渲染列表：绝大多数轮次从不展开，不必为它们每个都建一份 DOM。
const everOpened = ref(false)

function toggle() {
  open.value = !open.value
  if (open.value) everOpened.value = true
}

const headline = computed(() => {
  const parts: string[] = []
  if (createdCount.value) parts.push(`创建 ${createdCount.value}`)
  if (touchedCount.value) parts.push(`推进 ${touchedCount.value}`)
  return parts.join(' · ')
})

// 就地打开任务详情弹窗（由会话页挂的详情弹窗承接），不离开当前的对话；要看整个清单走页头的「任务」入口。
function openTask(id: number) {
  void store.openDetail(id)
}
</script>

<template>
  <section v-if="tasks.length" class="rounded-md border border-border/70 bg-muted/20" data-testid="session-tasks" :data-session="sessionId" :data-count="tasks.length">
    <button
      type="button"
      class="flex w-full cursor-pointer items-center gap-2 rounded-md px-3 py-1.5 text-left transition-colors hover:bg-muted/50 focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-primary/50"
      :aria-expanded="open"
      data-testid="session-tasks-toggle"
      @click="toggle"
    >
      <ChevronRight class="size-3.5 shrink-0 text-muted-foreground transition-transform duration-200 motion-reduce:transition-none" :class="open ? 'rotate-90' : ''" />
      <ListChecks class="size-3.5 shrink-0 text-muted-foreground" />
      <span class="shrink-0 text-xs font-medium text-foreground">本轮任务</span>
      <span class="shrink-0 rounded-xs bg-muted px-1.5 py-px font-mono text-[10px] tabular-nums text-foreground/80" data-testid="session-tasks-count">{{ tasks.length }}</span>
      <span v-if="headline" class="hidden min-w-0 truncate text-[10px] text-muted-foreground sm:inline">{{ headline }}</span>
      <!-- min-w-0（不是 shrink-0）：窄屏装不下时让汇总里的两条短条收窄，数字不被切掉 -->
      <span class="ml-auto min-w-0"><WorkTrackSummary :items="items" compact /></span>
    </button>

    <!-- 展开 / 折叠：一次性的行高过渡（grid-template-rows），不用 backdrop-filter、没有常驻动画；折叠后内容对键盘与读屏不可达 -->
    <div
      class="grid transition-[grid-template-rows] duration-200 ease-out motion-reduce:transition-none"
      :class="open ? 'grid-rows-[1fr]' : 'grid-rows-[0fr]'"
      :aria-hidden="!open"
    >
      <div
        class="min-h-0 overflow-hidden transition-[visibility] motion-reduce:delay-0"
        :class="open ? 'visible' : 'invisible'"
        :style="{ transitionDelay: open ? '0ms' : '200ms' }"
      >
        <ul v-if="everOpened" class="divide-y divide-border/50 border-t border-border/50" data-testid="session-tasks-list">
          <li v-for="t in tasks" :key="t.item.id">
            <button
              type="button"
              class="flex w-full cursor-pointer flex-wrap items-start gap-x-3 gap-y-1.5 px-3 py-2 text-left transition-colors hover:bg-muted/50 focus-visible:bg-muted/50 focus-visible:outline-none"
              :title="`查看任务 ${workRef(t.item)} 的详情`"
              :data-testid="`session-task-${t.item.seq}`"
              :data-created="t.ctx.created ? '1' : '0'"
              @click="openTask(t.item.id)"
            >
              <div class="min-w-0 flex-1 basis-[14rem]">
                <div class="flex min-w-0 items-center gap-1.5">
                  <span class="shrink-0 font-mono text-[10px] font-semibold text-foreground/70">{{ workRef(t.item) }}</span>
                  <span class="truncate text-[13px] font-medium leading-snug text-foreground" :title="t.item.title">{{ t.item.title }}</span>
                </div>
                <p class="mt-0.5 flex flex-wrap items-center gap-x-1.5 gap-y-0.5 text-[10px] text-muted-foreground">
                  <span
                    class="rounded-xs px-1 py-px"
                    :class="t.ctx.created ? 'bg-primary/10 text-primary' : 'bg-muted text-foreground/80'"
                    data-testid="session-task-ctx"
                  >{{ describeSessionCtx(t.ctx) }}</span>
                  <span v-if="t.item.module" class="truncate rounded-xs bg-muted px-1 py-px" :title="t.item.module" data-testid="session-task-module">{{ displayModulePath(t.item.module) }}</span>
                  <span v-if="t.item.parent_seq" class="truncate" :title="t.item.parent_title" data-testid="session-task-parent">属于 #{{ t.item.parent_seq }} {{ t.item.parent_title }}</span>
                </p>
              </div>
              <WorkTrack :item="t.item" class="pl-0" />
            </button>
          </li>
        </ul>
      </div>
    </div>
  </section>
</template>
