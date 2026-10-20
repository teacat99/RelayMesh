<script setup lang="ts">
// 单个任务的「双进度条」：第一条是阶段（评估 → 方案 → 开发 → 改进），第二条是状态（待办 → 进行中 → 验证中 → 已完成）。
// 画的是「位置」：走到第几格就填到第几格；一组任务的「分布」见 WorkTrackSummary。
// 只用背景色与一次性的颜色过渡，不用 backdrop-filter、不做常驻动画（见 GPU 评估）。
import { computed } from 'vue'
import {
  STATE_TRACK,
  WORK_STAGES,
  stageView,
  stateTrack,
  trackFill,
  trackPercentLabel,
  type WorkItem,
} from '../../lib/workItems'
import { STAGE_SHADE, TONE_CLASSES } from './tone'

const props = withDefaults(
  defineProps<{
    item: Pick<WorkItem, 'kind' | 'state'> & Partial<Pick<WorkItem, 'stage' | 'progress_pct' | 'progress_from'>>
    /** 在条旁边写出阶段与状态的名字。 */
    labels?: boolean
    /** 在状态条后面写完成度数字（只有手动填写或按子任务比例算出来的才写）。 */
    percent?: boolean
    /** auto：窄屏上下叠放、宽屏并排；row：始终并排；stack：始终叠放。 */
    layout?: 'auto' | 'row' | 'stack'
  }>(),
  { labels: true, percent: true, layout: 'auto' }
)

const stage = computed(() => stageView(props.item))
const state = computed(() => stateTrack(props.item))
const stageFill = computed(() => trackFill(WORK_STAGES.length, stage.value.index))
const stateFill = computed(() => trackFill(STATE_TRACK.length, state.value.index))
const pct = computed(() => (props.percent ? trackPercentLabel(props.item) : null))

const layoutClass = computed(() => {
  if (props.layout === 'row') return 'flex-row items-center'
  if (props.layout === 'stack') return 'flex-col'
  return 'flex-col sm:flex-row sm:items-center'
})

const stageTitle = computed(() => {
  const s = stage.value
  const base = `阶段：${s.label}（评估 → 方案 → 开发 → 改进）· ${s.hint}`
  return s.derived ? `${base}。还没有标注阶段，先按类型推导；你或智能体标注后以标注为准` : base
})

const stateTitle = computed(() => {
  const note = pct.value ? `；完成度 ${pct.value}` : ''
  return `状态：${state.value.hint}${note}`
})

/** 阶段条每一格：走过的与当前的填上它自己的深浅，没走到的留空；推导出来的整体淡一些。 */
function stageSeg(i: number): string[] {
  if (!stageFill.value[i]) return ['bg-muted']
  const s = WORK_STAGES[i]
  return [STAGE_SHADE[s], stage.value.derived ? 'opacity-40' : i === stage.value.index ? '' : 'opacity-70']
}

/** 状态条每一格：按当前状态着色；已搁置不在主线上，整条留空并描虚线边。 */
function stateSeg(i: number): string[] {
  if (state.value.shelved) return ['bg-muted/60', 'ring-1', 'ring-inset', 'ring-zinc-400/50']
  if (!stateFill.value[i]) return ['bg-muted']
  return [TONE_CLASSES[state.value.tone].dot, i === state.value.index ? '' : 'opacity-60']
}
</script>

<template>
  <div
    class="inline-flex shrink-0 gap-x-3 gap-y-1"
    :class="layoutClass"
    data-testid="work-track"
    :data-stage="stage.stage"
    :data-stage-derived="stage.derived ? '1' : '0'"
    :data-state="state.state"
  >
    <div class="flex items-center gap-1.5" :title="stageTitle" data-testid="work-track-stage">
      <span class="flex gap-px" role="img" :aria-label="stageTitle">
        <i
          v-for="(_, i) in stageFill"
          :key="WORK_STAGES[i]"
          class="block h-1.5 w-2.5 rounded-[1px] transition-colors duration-300 motion-reduce:transition-none"
          :class="stageSeg(i)"
        />
      </span>
      <span
        v-if="labels"
        class="min-w-6 whitespace-nowrap text-[10px] leading-none"
        :class="stage.derived ? 'italic text-muted-foreground' : 'text-foreground/80'"
        data-testid="work-track-stage-label"
      >{{ stage.label }}</span>
    </div>

    <div class="flex items-center gap-1.5" :title="stateTitle" data-testid="work-track-state">
      <span class="flex gap-px" role="img" :aria-label="stateTitle">
        <i
          v-for="(_, i) in stateFill"
          :key="STATE_TRACK[i]"
          class="block h-1.5 w-2.5 rounded-[1px] transition-colors duration-300 motion-reduce:transition-none"
          :class="stateSeg(i)"
        />
      </span>
      <span
        v-if="labels"
        class="min-w-9 whitespace-nowrap text-[10px] leading-none"
        :class="state.shelved ? 'text-muted-foreground' : TONE_CLASSES[state.tone].text"
        data-testid="work-track-state-label"
      >{{ state.label }}</span>
      <span v-if="pct" class="w-7 shrink-0 font-mono text-[10px] tabular-nums leading-none text-muted-foreground" data-testid="work-track-pct">{{ pct }}</span>
    </div>
  </div>
</template>
