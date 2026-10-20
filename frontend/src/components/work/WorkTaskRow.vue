<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { ArrowRight, ChevronRight, Layers } from 'lucide-vue-next'
import WorkTrack from './WorkTrack.vue'
import {
  WORK_KIND_META,
  WORK_STATE_META,
  assigneeView,
  batchProgress,
  displayModulePath,
  isClosedState,
  isWorkKind,
  quickActions,
  relativeTime,
  showCreator,
  stateLabel,
  workRef,
  workflowLabel,
  type TaskRow,
  type WorkItem,
  type WorkState,
} from '../../lib/workItems'
import { TONE_CLASSES } from './tone'

const props = defineProps<{
  row: TaskRow
  /** 当前时间戳（由任务页统一走表，行上的相对时间随之刷新）。 */
  now: number
  /** 显示所属模块路径：最近进展平铺时，或任务和它所在分组的模块不一致时。 */
  showModule?: boolean
  selected?: boolean
  /** 正在提交的操作（避免连点）。 */
  busy?: boolean
  /** 工作流的展示名来源（会话标题），承接者 / 创建者标签用。 */
  known?: { workflow_id: string; title?: string }[]
  /** 正在看的工作流；共享板上任务不是它创建的才标创建者。 */
  viewer?: string
  /** 这张板被多个工作流共用。 */
  sharedBoard?: boolean
}>()

const emit = defineEmits<{
  (e: 'open', id: number): void
  (e: 'action', item: WorkItem, to: WorkState): void
  (e: 'toggle', row: TaskRow): void
}>()

const item = computed(() => props.row.item)
const tone = computed(() => TONE_CLASSES[WORK_STATE_META[item.value.state]?.tone || 'slate'])
const kind = computed(() => (isWorkKind(item.value.kind) && item.value.kind !== 'feature' ? WORK_KIND_META[item.value.kind] : null))
const batch = computed(() => batchProgress(item.value))
// 已结束的任务读「结论」，其余读「当前进度」
const subtext = computed(() => (isClosedState(item.value.state) && item.value.conclusion ? item.value.conclusion : item.value.progress))
const primary = computed(() => quickActions(item.value.state).find(a => a.primary) || null)
// 共享任务板（#175 B2）：承接者 + 接取状态、别的工作流创建的任务标创建者；没有指派、也不是共享板时两个都为空，行与以前一样
const assignee = computed(() => assigneeView(item.value, props.known || []))
const creator = computed(() =>
  showCreator(item.value, props.viewer || '', !!props.sharedBoard) ? workflowLabel(item.value.workflow_id, props.known || []) : ''
)

function runPrimary() {
  const a = primary.value
  if (!a || props.busy) return
  emit('action', item.value, a.to)
}

// 这一行发生了实际变化（状态 / 阶段 / 完成度 / 进展被改写，不论是智能体还是你自己改的）：背景亮一下再淡回原色。
// 一次性的小动画（1.4 秒、只重绘这一行），首次渲染、切换筛选带来的重新挂载、列表无变化的重新加载都不会触发；「减少动态效果」下不播。
const rowEl = ref<HTMLElement | null>(null)
const FLASH_MS = 1400
watch(
  () => [item.value.updated_at, item.value.state, item.value.stage || '', item.value.progress_pct ?? -1].join('|'),
  () => {
    const el = rowEl.value
    if (!el || typeof el.animate !== 'function') return
    if (typeof window.matchMedia === 'function' && window.matchMedia('(prefers-reduced-motion: reduce)').matches) return
    const base = getComputedStyle(el).backgroundColor
    const accent = getComputedStyle(document.documentElement).getPropertyValue('--primary').trim()
    if (!accent) return
    el.animate(
      [{ backgroundColor: `color-mix(in oklab, ${accent} 18%, transparent)` }, { backgroundColor: base }],
      { duration: FLASH_MS, easing: 'ease-out' },
    )
  },
  { flush: 'post' },
)
</script>

<template>
  <article
    ref="rowEl"
    role="button"
    tabindex="0"
    :aria-label="`${workRef(item)} ${item.title}`"
    :data-testid="`work-row-${item.seq}`"
    :data-state="item.state"
    :data-depth="row.depth"
    class="group flex cursor-pointer select-none flex-wrap items-center gap-x-3 gap-y-1.5 border-b border-border/50 py-2 pr-3 transition-colors hover:bg-muted/40 focus-visible:bg-muted/40 focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-inset focus-visible:ring-primary/50"
    :class="[row.depth ? 'bg-muted/10 pl-10 sm:pl-12' : 'pl-3', selected ? 'bg-muted/50' : '']"
    @click="emit('open', item.id)"
    @keydown.enter.self.prevent="emit('open', item.id)"
    @keydown.space.self.prevent="emit('open', item.id)"
  >
    <div class="flex min-w-0 flex-1 basis-[15rem] items-start gap-2">
      <button
        v-if="row.childCount"
        type="button"
        class="-ml-1 mt-px inline-flex size-5 shrink-0 cursor-pointer items-center justify-center rounded-xs text-muted-foreground hover:bg-muted hover:text-foreground"
        :aria-expanded="!row.collapsed"
        :aria-label="row.collapsed ? `展开 ${row.childCount} 个子任务` : '折叠子任务'"
        :title="row.collapsed ? `展开 ${row.childCount} 个子任务` : '折叠子任务'"
        :data-testid="`work-row-toggle-${item.seq}`"
        @click.stop="emit('toggle', row)"
        @keydown.stop
      >
        <ChevronRight class="size-3.5 transition-transform" :class="row.collapsed ? '' : 'rotate-90'" />
      </button>
      <span v-else-if="!row.depth" class="-ml-1 size-5 shrink-0" aria-hidden="true" />
      <span class="mt-[7px] size-2 shrink-0 rounded-full" :class="tone.dot" :title="stateLabel(item.state)" />

      <div class="min-w-0 flex-1">
        <div class="flex min-w-0 items-center gap-1.5">
          <span class="shrink-0 font-mono text-[10px] font-semibold text-foreground/70">{{ workRef(item) }}</span>
          <h3 class="truncate text-[13px] font-medium leading-snug text-foreground" :title="item.title" data-testid="work-row-title">{{ item.title }}</h3>
          <span
            v-if="kind"
            class="hidden shrink-0 items-center rounded-xs border px-1 py-px text-[10px] leading-none sm:inline-flex"
            :class="TONE_CLASSES[kind.tone].badge"
          >{{ kind.label }}</span>
          <span
            v-if="batch"
            class="inline-flex shrink-0 items-center gap-0.5 font-mono text-[10px] text-muted-foreground"
            :title="`批次：${batch.done}/${batch.total} 个子任务已完成`"
          ><Layers class="size-3" />{{ batch.done }}/{{ batch.total }}</span>
        </div>
        <p v-if="subtext" class="mt-0.5 truncate text-xs text-muted-foreground" :title="subtext">{{ subtext }}</p>
        <p v-if="item.parent_seq && !row.depth" class="mt-0.5 truncate text-[10px] text-muted-foreground" :title="item.parent_title">属于 #{{ item.parent_seq }} {{ item.parent_title }}</p>
        <p v-if="assignee || creator" class="mt-1 flex min-w-0 flex-wrap items-center gap-1.5 text-[10px]" data-testid="work-row-collab">
          <span
            v-if="creator"
            class="max-w-[10rem] truncate rounded-xs bg-muted px-1 py-px leading-none text-muted-foreground"
            :title="`创建者：${creator}`"
            data-testid="work-row-creator"
          >来自 {{ creator }}</span>
          <span
            v-if="assignee"
            class="inline-flex max-w-full items-center gap-1 rounded-xs border px-1 py-px leading-none"
            :class="TONE_CLASSES[assignee.tone].badge"
            :title="`承接者：${assignee.label}（${assignee.hint}；派发者：${assignee.byLabel}）`"
            :data-claim="assignee.claim"
            data-testid="work-row-assignee"
          >
            <ArrowRight class="size-2.5 shrink-0" aria-hidden="true" />
            <span class="truncate">{{ assignee.label }}</span>
            <span v-if="assignee.claimLabel" class="shrink-0 font-medium">{{ assignee.claimLabel }}</span>
          </span>
        </p>
      </div>
    </div>

    <div class="flex min-w-0 shrink-0 items-center gap-3 pl-[1.625rem] sm:pl-0">
      <span
        v-if="showModule && item.module"
        class="hidden max-w-[11rem] shrink-0 truncate rounded-xs bg-muted px-1.5 py-px text-[10px] text-muted-foreground md:inline-block"
        :title="item.module"
        data-testid="work-row-module"
      >{{ displayModulePath(item.module) }}</span>
      <!-- 双进度条：阶段（评估 → 方案 → 开发 → 改进）+ 状态（待办 → 进行中 → 验证中 → 已完成）；窄屏上下叠放 -->
      <WorkTrack :item="item" class="sm:w-[11.5rem]" />
      <span class="w-[3.75rem] shrink-0 text-right text-[10px] tabular-nums text-muted-foreground" :title="item.updated_at">{{ relativeTime(item.updated_at, now) }}</span>
      <span class="inline-flex w-[5.25rem] shrink-0 justify-end" @click.stop @keydown.stop>
        <button
          v-if="primary"
          type="button"
          :disabled="busy"
          class="inline-flex h-6 cursor-pointer items-center gap-1 whitespace-nowrap rounded-xs border border-primary/40 bg-primary/5 px-2 text-[11px] font-medium text-primary transition-colors hover:bg-primary/10 disabled:opacity-50"
          :data-testid="`work-action-${item.seq}-${primary.to}`"
          @click="runPrimary"
        >{{ primary.label }}</button>
      </span>
    </div>
  </article>
</template>
