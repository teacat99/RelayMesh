<script setup lang="ts">
// 异步对话模式下，时间线上的任务进展卡（#42）：开启异步之后智能体每更新一次任务（新建 / 进行中 / 进入验证 / 只写了进度…）就是一张，
// 按提交时间落在对话里（#105），连续的几张会叠成一摞（见 CardStack）。
// 渲染成 32px 的事件行（CompactTaskRow，#120 · D-273 阶段 C2）；这里只负责按提交时间与任务状态算好时间文字和状态标签的颜色。
// 进度通知（智能体 wait:false 发出的话）不走这里：在预览里它是一张统一消息卡。
import { computed } from 'vue'
import dayjs from 'dayjs'
import type { TimelineEntry } from '../../lib/timeline'
import { WORK_STATE_META, isWorkState } from '../../lib/workItems'
import { TONE_CLASSES } from '../work/tone'
import CompactTaskRow from './CompactTaskRow.vue'

const props = defineProps<{ entry: Extract<TimelineEntry, { kind: 'task' }> }>()

const timeText = computed(() => {
  const target = dayjs(props.entry.at)
  if (!target.isValid()) return ''
  const now = dayjs()
  if (target.isSame(now, 'day')) return target.format('HH:mm')
  if (target.isSame(now, 'year')) return target.format('MM-DD·HH:mm')
  return target.format('YYYY·MM-DD·HH:mm')
})

const verbClass = computed(() => {
  const state = props.entry.card.state
  const tone = isWorkState(state) ? WORK_STATE_META[state].tone : 'slate'
  return TONE_CLASSES[tone].badge
})
</script>

<template>
  <CompactTaskRow :entry="entry" :time-text="timeText" :verb-class="verbClass" />
</template>
