<script setup lang="ts">
// 任务进展卡的事件行（预览视图，#120 · D-273 阶段 C2；经典用 AsyncTimelineEntry 里的整块卡）：
//   [任务] #12 标题 …… 进展的第一句 · 时间 · [动词标签] ›
// 固定 32px 单行；整行就是一个按钮，点一下进任务详情（完整进展在详情里）。
import { CARD_CHIP_BASE, CARD_TONE_CLASS, CARD_TYPES } from '../../lib/messageCard'
import type { TimelineEntry } from '../../lib/timeline'
import { EVENT_BOX } from '../../lib/eventRow'
import { useWorkItemStore } from '../../stores/workItems'
import { useTaskRefs } from '../../composables/useTaskRefs'
import EventRowHead from './EventRowHead.vue'

defineProps<{
  entry: Extract<TimelineEntry, { kind: 'task' }>
  timeText: string
  /** 状态标签的颜色类（由 AsyncTimelineEntry 按任务状态算好，两种版式共用同一份）。 */
  verbClass: string
}>()

const workStore = useWorkItemStore()
const taskRefs = useTaskRefs(() => workStore.feed.workflowId)
const TONE = CARD_TONE_CLASS[CARD_TYPES.task.tone]
</script>

<template>
  <div
    :class="[EVENT_BOX, TONE]"
    data-testid="task-update-card"
    :data-seq="entry.card.seq"
    :data-state="entry.card.state"
  >
    <EventRowHead
      type="task"
      :lead="`#${entry.card.seq}`"
      :title="entry.card.title"
      :preview="entry.card.text"
      :time="timeText"
      interactive
      chevron
      :open-title="`查看任务 #${entry.card.seq} 的详情`"
      main-testid="task-update-open"
      preview-testid="task-update-text"
      @click="taskRefs.open(entry.card.seq)"
    >
      <template #status>
        <!-- 动词标签的颜色按任务状态由外面给（verbClass）；几何与其他小标签同一份（高 18px），不把这一行撑高 -->
        <span :class="[CARD_CHIP_BASE, verbClass]" data-testid="task-update-verb">{{ entry.card.verb }}</span>
      </template>
    </EventRowHead>
  </div>
</template>
