<script setup lang="ts">
// 时间线上一张「卡片」：按种类选对应的组件。摞里的卡片与单独的卡片都走这里，渲染完全一致。
//
// compact（#120 预览视图，默认关）：没有正文的事件用 32px 的「事件行」版式渲染（阶段 C2，见 lib/eventRow 与 EventRowHead）。
// 经典列表不传这个参数，渲染一像素不变；这里只负责把它原样递给各叶子组件。
import type { TimelineEntry } from '../../lib/timeline'
import AsyncEntryItem from './AsyncTimelineEntry.vue'
import ConfirmTimelineCard from '../confirms/ConfirmTimelineCard.vue'
import DecisionGroupCard from '../confirms/DecisionGroupCard.vue'
import GuardAlertCard from '../confirms/GuardAlertCard.vue'

defineProps<{ entry: TimelineEntry; compact?: boolean }>()
</script>

<template>
  <ConfirmTimelineCard v-if="entry.kind === 'confirm'" :item="entry.item" :compact="compact" />
  <DecisionGroupCard v-else-if="entry.kind === 'decisions'" :group="entry.group" :compact="compact" />
  <GuardAlertCard v-else-if="entry.kind === 'guard'" :alert="entry.alert" :compact="compact" />
  <AsyncEntryItem v-else-if="entry.kind === 'task' || entry.kind === 'notice'" :entry="entry" :compact="compact" />
</template>
