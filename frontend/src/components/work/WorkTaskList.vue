<script setup lang="ts">
import { computed } from 'vue'
import WorkProgressBar from './WorkProgressBar.vue'
import WorkTaskRow from './WorkTaskRow.vue'
import type { TaskGroup, TaskRow, TaskSort, WorkItem, WorkModuleStats, WorkState } from '../../lib/workItems'

const props = defineProps<{
  groups: TaskGroup[]
  sort: TaskSort
  /** 未归类任务的汇总（「未归类」分组头上显示）。 */
  unassigned?: WorkModuleStats | null
  selectedId?: number | null
  busyId?: number | null
  /** 工作流的展示名来源（会话标题），行上的承接者 / 创建者标签用。 */
  known?: { workflow_id: string; title?: string }[]
  /** 正在看的工作流：共享板上任务不是它创建的才标创建者。 */
  viewer?: string
  /** 这张板被多个工作流共用。 */
  sharedBoard?: boolean
}>()

const emit = defineEmits<{
  (e: 'open', id: number): void
  (e: 'action', item: WorkItem, to: WorkState): void
  (e: 'toggle', row: TaskRow): void
}>()

function statsOf(g: TaskGroup, unassigned?: WorkModuleStats | null): WorkModuleStats | null {
  return g.key === 'none' ? unassigned || null : g.stats
}

/** 行上是否要标出所属模块：平铺（没有分组头）时总要；分组里只有当任务和分组头的模块不一致时才要（批次里的子任务）。 */
function showModule(sort: TaskSort, g: TaskGroup, row: TaskRow): boolean {
  if (sort === 'recent') return true
  return (row.item.module_id ?? null) !== g.moduleId
}

/**
 * 列表的过渡动画（只动 transform / opacity，不触发布局，也没有无限循环；样式见 assets/globals.css 的「任务页过渡动画」）：
 *  - 新出现的行 / 分组淡入并从上方落下一点（页面刚加载时不播，由 TransitionGroup 默认的「首次渲染不动」保证）；
 *  - 状态变化引起的重排、批次折叠 / 展开，其余的行平滑让位（FLIP）；离开的行直接消失，让位动画照常进行；
 *  - 「减少动态效果」下全部关闭；
 *  - 行数很多（整页换内容、一次加载几百行）时不逐行做动画，免得一下子起几百个合成层：换成一个没有任何样式的名字即可。
 */
const ANIMATED_ROW_LIMIT = 150
const animated = computed(() => props.groups.reduce((n, g) => n + g.rows.length, 0) <= ANIMATED_ROW_LIMIT)
const groupFx = computed(() => (animated.value ? 'work-group' : 'work-off'))
const rowFx = computed(() => (animated.value ? 'work-row' : 'work-off'))
</script>

<template>
  <TransitionGroup tag="div" data-testid="work-list" class="relative" :name="groupFx">
    <section v-for="g in groups" :key="g.key" :data-testid="`work-group-${g.key}`" :data-path="g.path">
      <header
        v-if="sort === 'plan'"
        class="sticky top-0 z-10 flex items-center gap-2 border-b border-border/60 bg-muted/60 px-3 py-1.5 backdrop-blur-xs"
        :class="g.depth ? 'pl-7' : ''"
      >
        <h3 class="min-w-0 truncate text-xs font-semibold text-foreground" :title="g.path" data-testid="work-group-title">
          <span v-if="g.parentName" class="font-normal text-muted-foreground">{{ g.parentName }} / </span>{{ g.name }}
        </h3>
        <span class="font-mono text-[10px] tabular-nums text-muted-foreground">{{ g.rows.filter(r => r.depth === 0).length }}</span>
        <template v-if="statsOf(g, unassigned)?.total">
          <span class="ml-auto hidden font-mono text-[10px] tabular-nums text-muted-foreground sm:inline" :title="`共 ${statsOf(g, unassigned)!.total} 项，已完成 ${statsOf(g, unassigned)!.done}`">
            {{ statsOf(g, unassigned)!.done }}/{{ statsOf(g, unassigned)!.total }}
          </span>
          <WorkProgressBar class="ml-auto w-20 sm:ml-0 sm:w-28" :percent="statsOf(g, unassigned)!.percent" tone="emerald" thin show-label />
        </template>
      </header>
      <TransitionGroup tag="div" class="relative" :name="rowFx">
        <WorkTaskRow
          v-for="r in g.rows"
          :key="r.item.id"
          :row="r"
          :show-module="showModule(sort, g, r)"
          :selected="selectedId === r.item.id"
          :busy="busyId === r.item.id"
          :known="known"
          :viewer="viewer"
          :shared-board="sharedBoard"
          @open="id => emit('open', id)"
          @action="(item, to) => emit('action', item, to)"
          @toggle="row => emit('toggle', row)"
        />
      </TransitionGroup>
    </section>
  </TransitionGroup>
</template>
