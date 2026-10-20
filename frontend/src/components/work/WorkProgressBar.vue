<script setup lang="ts">
import { computed } from 'vue'
import { progressView, type ProgressView } from '../../lib/workItems'
import { TONE_CLASSES } from './tone'

const props = withDefaults(
  defineProps<{
    /** 直接给一个任务（取它的状态与后端算好的进度）。 */
    item?: Parameters<typeof progressView>[0]
    /** 或者给一个现成的进度值（模块汇总用）：只有百分比，没有来源。 */
    percent?: number
    /** 只在 percent 方式下用：条的颜色。 */
    tone?: ProgressView['tone']
    /** 在条旁边显示数字（由状态推导出来的值不显示数字）。 */
    showLabel?: boolean
    /** 条的粗细。 */
    thin?: boolean
  }>(),
  { showLabel: false, thin: false }
)

const view = computed<ProgressView>(() => {
  if (props.item) return progressView(props.item)
  const pct = Math.min(100, Math.max(0, Math.round(props.percent ?? 0)))
  return { pct, label: `${pct}%`, from: 'explicit', derived: false, tone: props.tone || 'sky' }
})

const barClass = computed(() => TONE_CLASSES[view.value.tone].dot)

const hint = computed(() => {
  switch (view.value.from) {
    case 'explicit':
      return `完成度 ${view.value.pct}%（手动填写）`
    case 'children':
      return `子任务完成比例 ${view.value.pct}%`
    default:
      return props.item ? '没有填写具体完成度：进度条由状态推导' : `${view.value.pct}%`
  }
})
</script>

<template>
  <div class="flex items-center gap-1.5" :title="hint">
    <div
      class="flex-1 overflow-hidden rounded-full bg-muted"
      :class="thin ? 'h-1' : 'h-1.5'"
      role="progressbar"
      aria-valuemin="0"
      aria-valuemax="100"
      :aria-valuenow="view.pct"
      :data-pct="view.pct"
      :data-from="view.from"
    >
      <div class="h-full rounded-full transition-[width] duration-500 ease-out motion-reduce:transition-none" :class="[barClass, view.derived ? 'opacity-45' : '']" :style="{ width: view.pct + '%' }" />
    </div>
    <span v-if="showLabel" class="w-8 shrink-0 text-right font-mono text-[10px] tabular-nums text-muted-foreground">{{ view.label }}</span>
  </div>
</template>
