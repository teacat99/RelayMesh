<script setup lang="ts">
import { computed } from 'vue'
import { useCoarseNow } from '../../composables/useCoarseNow'
import { offeredBadgeTitle, type WorkOffered } from '../../lib/workItems'
import { TONE_CLASSES } from '../work/tone'

// 侧栏会话卡上的「N 项待接取」徽标（#180 B3b）：这个工作流手上有协作任务被派来、还没接取。
// 做成独立的小组件、悬停说明里的「最久已等」自己读 30 秒一跳的共享慢时钟：只有真的带徽标的卡才订阅，
// 其余绝大多数卡不多一个依赖（与 #215 / #219 同一条规矩：不把时间当 prop 灌给整列卡片）。
const props = defineProps<{ offered: WorkOffered }>()
const clock = useCoarseNow()
const title = computed(() => offeredBadgeTitle(props.offered, clock.value))
</script>

<template>
  <span
    class="shrink-0 whitespace-nowrap rounded-2xs border px-1 leading-4"
    :class="TONE_CLASSES.amber.badge"
    :title="title"
    data-testid="sidebar-board-offered"
  >{{ offered.count }} 项待接取</span>
</template>
