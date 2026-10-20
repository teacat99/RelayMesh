<script setup lang="ts">
// 预览视图右侧轨的刻度列表（#158 拆出来的）：PreviewTimelineRail 负责量位置、排布、跟随与可视窗口，这里只负责把排好的刻度画出来。
//
// 为什么单独一个组件：会话列表每滚一帧，轨的可视窗口框（与轨自己的 scrollTop、渐隐）都要更新；
// 刻度和这些写在同一个模板里时，每一帧都会把几百道刻度（含每道的内联样式与分段）整个重新渲染一遍，
// 150 轮的长会话上这一项就占了滚动开销的大头（scripts/e2e-terminal/e2e/bench-preview.mjs 度量）。
// 拆开之后，刻度只在排布真的换了新数组（条目增删、状态翻转、条目尺寸变化）时才重渲染；
// 滚动、悬停、轨自己的滚动都只更新外层那一小块，不牵连这里。
//
// 刻度是一道 16px 高 × 整条轨宽的点击区（点得准），刻度线（2px / 4px 粗）靠右、长度 / 颜色按 lib/timelineRail 的样式表。
// 悬停 / 聚焦 / 点击都只往外发事件（hover / jump），状态在外层：本组件不持有任何会变的状态。
import { CARD_TONE_CLASS } from '../../../lib/messageCard'
import { RAIL_DIM_OPACITY, RAIL_PITCH_PX, railLabel, type RailItem, type RailTick } from '../../../lib/timelineRail'
import { formatStackParts, stackRailSegments } from '../../../lib/stackTypes'

defineProps<{
  /** 排好的刻度（lib/timelineRail 的 layoutRail 产出，按位置从上到下）。整个数组替换才会触发重渲染；不要原地修改。 */
  ticks: readonly RailTick[]
}>()

const emit = defineEmits<{
  /** 点击刻度：交给列表去滚。 */
  jump: [item: RailItem]
  /** 悬停 / 聚焦在某道刻度上（id）；移开 / 失焦时是 null。 */
  hover: [id: string | null]
}>()

function tickLabel(item: RailItem): string {
  return `${railLabel(item.kind, item.state, item.parts)}${item.title ? `：${item.title}` : ''}`
}
</script>

<template>
  <button
    v-for="t in ticks"
    :key="t.item.id"
    type="button"
    class="pointer-events-auto absolute inset-x-0 flex cursor-pointer items-center justify-end rounded-sm pr-1 outline-none transition-colors hover:bg-foreground/[0.1] focus-visible:ring-1 focus-visible:ring-ring"
    :style="{ top: `${t.y - RAIL_PITCH_PX / 2}px`, height: `${RAIL_PITCH_PX}px` }"
    :aria-label="tickLabel(t.item)"
    data-testid="pv-rail-tick"
    :data-kind="t.item.kind"
    :data-state="t.item.state"
    :data-id="t.item.id"
    :data-parts="t.item.parts ? formatStackParts(t.item.parts) : undefined"
    @click="emit('jump', t.item)"
    @mouseenter="emit('hover', t.item.id)"
    @mouseleave="emit('hover', null)"
    @focus="emit('hover', t.item.id)"
    @blur="emit('hover', null)"
  >
    <!-- 摞：刻度按内含类型分段着色（一种类型一段，并排；总长仍是「短」一档，颜色取卡片的类型色调） -->
    <span
      v-if="t.item.parts && t.item.parts.length > 0"
      class="flex shrink-0 gap-px"
      :style="{ width: `${t.len}px`, height: `${t.line}px` }"
      data-testid="pv-rail-tick-line"
    >
      <span
        v-for="seg in stackRailSegments(t.item.parts)"
        :key="seg.type"
        class="min-w-0 flex-1 rounded-full bg-[color:var(--card-tone)]"
        :class="seg.toneClass"
        :data-type="seg.type"
        data-testid="pv-rail-tick-seg"
      />
    </span>
    <!-- 单个条目：颜色一律取这一类卡片的色调（追加 #265：进度通知是蓝的），状态靠长度 / 粗细 / 脉冲 / 变淡表达 -->
    <span
      v-else
      class="block shrink-0 rounded-full bg-[color:var(--card-tone)]"
      :class="[CARD_TONE_CLASS[t.tone], t.pulse ? 'animate-pulse motion-reduce:animate-none' : '']"
      :style="{ width: `${t.len}px`, height: `${t.line}px`, ...(t.dim ? { opacity: RAIL_DIM_OPACITY } : {}) }"
      :data-tone="t.tone"
      :data-dim="t.dim ? '1' : undefined"
      data-testid="pv-rail-tick-line"
    />
  </button>
</template>
