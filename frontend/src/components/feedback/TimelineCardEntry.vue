<script setup lang="ts">
// 时间线上一张「卡片」：按种类选对应的组件。摞里的卡片与单独的卡片都走这里，渲染完全一致。
//
// 没有正文的事件（任务进展 / 已处理的提问与决策）都用 32px 的「事件行」版式渲染（#120 预览视图，阶段 C2，见 lib/eventRow 与 EventRowHead）。
// 进度通知、还要您处理的提问都不走这里（预览里它们各自是一张统一消息卡）。
import type { TimelineEntry } from '../../lib/timeline'
import AsyncEntryItem from './AsyncTimelineEntry.vue'
import ConfirmTimelineCard from '../confirms/ConfirmTimelineCard.vue'
import DecisionGroupCard from '../confirms/DecisionGroupCard.vue'

defineProps<{ entry: TimelineEntry }>()
</script>

<template>
  <ConfirmTimelineCard v-if="entry.kind === 'confirm'" :item="entry.item" compact />
  <DecisionGroupCard v-else-if="entry.kind === 'decisions'" :group="entry.group" compact />
  <AsyncEntryItem v-else-if="entry.kind === 'task'" :entry="entry" />
</template>
