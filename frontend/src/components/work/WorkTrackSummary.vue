<script setup lang="ts">
// 一组任务的「双进度条」分布版：第一条按阶段、第二条按状态，条上每一段的宽度与任务数成正比，旁边写出各自的数量，
// 再加一句整体完成度。单个任务画位置、一组任务画分布，两者用同一套颜色。
// 紧凑版只用 span（要放进按钮里），完整版带图例。紧凑版的两条短条宽 64px 但可以收窄（最窄 16px），屏幕窄到 340px 以下
// （320px 的小屏手机）干脆不画：放它的那一行装不下时先让短条让位，数字（「100% · 12/12」）一直完整显示——
// 360px 宽的手机上，一行 [图标 + 名称 + 数量] 再加 64 + 64 + 数字约 380px，原来短条不收，右边被切掉一截（#167）。
import { computed } from 'vue'
import { WORK_STAGE_META, WORK_STATE_META, summarizeTracks, type WorkItem } from '../../lib/workItems'
import { STAGE_SHADE, TONE_CLASSES } from './tone'

const props = withDefaults(
  defineProps<{
    items: WorkItem[]
    /** 紧凑：一行放下两条短条与一句整体完成度，不写图例（会话页每一轮的折叠条用）。 */
    compact?: boolean
    /** 范围说明（悬浮提示的前缀），例如「已完成 / 已搁置的任务只统计最近 30 天内关闭的」。 */
    scopeNote?: string
  }>(),
  { compact: false, scopeNote: '' }
)

const sum = computed(() => summarizeTracks(props.items))
const stageSegs = computed(() => sum.value.stages.filter(s => s.count > 0))
const stateSegs = computed(() => sum.value.states.filter(s => s.count > 0))

const stageTitle = computed(() => {
  const parts = stageSegs.value.map(s => `${WORK_STAGE_META[s.stage].label} ${s.count}`)
  const derived = sum.value.derivedStages ? `（其中 ${sum.value.derivedStages} 个没有标注阶段，按类型推导）` : ''
  return `阶段分布：${parts.join(' · ')}${derived}`
})
const stateTitle = computed(() => `状态分布：${stateSegs.value.map(s => `${WORK_STATE_META[s.state].label} ${s.count}`).join(' · ')}`)
const overall = computed(() => `整体 ${sum.value.percent}% · 已完成 ${sum.value.done}/${sum.value.total}`)
const overallTitle = computed(() => [props.scopeNote, '整体完成度 = 没有子任务的任务的平均进度（已搁置的不计入）'].filter(Boolean).join('。'))

const stateSegClass = (state: keyof typeof WORK_STATE_META) => [
  TONE_CLASSES[WORK_STATE_META[state].tone].dot,
  state === 'shelved' ? 'opacity-50' : '',
]
</script>

<template>
  <!-- 紧凑版：全是 span，可以放在按钮里 -->
  <span
    v-if="sum.total && compact"
    class="flex min-w-0 items-center gap-2"
    data-testid="work-track-summary"
    data-compact="1"
    :data-total="sum.total"
    :data-done="sum.done"
    :data-percent="sum.percent"
  >
    <span class="flex h-1.5 w-16 min-w-4 shrink gap-px overflow-hidden rounded-full bg-muted max-[340px]:hidden" role="img" :title="stageTitle" :aria-label="stageTitle" data-testid="work-track-summary-stage">
      <span
        v-for="s in stageSegs"
        :key="s.stage"
        class="block min-w-1 transition-[flex-grow] duration-300 motion-reduce:transition-none"
        :class="STAGE_SHADE[s.stage]"
        :style="{ flexGrow: s.count }"
      />
    </span>
    <span class="flex h-1.5 w-16 min-w-4 shrink gap-px overflow-hidden rounded-full bg-muted max-[340px]:hidden" role="img" :title="stateTitle" :aria-label="stateTitle" data-testid="work-track-summary-state">
      <span
        v-for="s in stateSegs"
        :key="s.state"
        class="block min-w-1 transition-[flex-grow] duration-300 motion-reduce:transition-none"
        :class="stateSegClass(s.state)"
        :style="{ flexGrow: s.count }"
      />
    </span>
    <span class="whitespace-nowrap font-mono text-[10px] tabular-nums text-muted-foreground" :title="overallTitle" data-testid="work-track-summary-overall">{{ sum.percent }}% · {{ sum.done }}/{{ sum.total }}</span>
  </span>

  <!-- 完整版：两条分布条 + 图例 + 整体一句话 -->
  <div
    v-else-if="sum.total"
    class="grid gap-x-8 gap-y-2 md:grid-cols-2"
    data-testid="work-track-summary"
    :data-total="sum.total"
    :data-done="sum.done"
    :data-percent="sum.percent"
  >
    <div class="space-y-1" :title="stageTitle" data-testid="work-track-summary-stage">
      <div class="flex items-center gap-2">
        <span class="w-7 shrink-0 text-[10px] text-muted-foreground">阶段</span>
        <div class="flex h-1.5 min-w-10 flex-1 gap-px overflow-hidden rounded-full bg-muted" role="img" :aria-label="stageTitle">
          <span
            v-for="s in stageSegs"
            :key="s.stage"
            class="block min-w-1 transition-[flex-grow] duration-300 motion-reduce:transition-none"
            :class="STAGE_SHADE[s.stage]"
            :style="{ flexGrow: s.count }"
          />
        </div>
      </div>
      <ul class="flex flex-wrap gap-x-2.5 gap-y-0.5 pl-9 text-[10px] text-foreground/80">
        <li v-for="s in stageSegs" :key="s.stage" class="inline-flex items-center gap-1" :data-testid="`work-track-summary-stage-${s.stage}`">
          <i class="block size-1.5 rounded-full" :class="STAGE_SHADE[s.stage]" />{{ WORK_STAGE_META[s.stage].label }}
          <span class="font-mono tabular-nums text-muted-foreground">{{ s.count }}</span>
        </li>
      </ul>
    </div>

    <div class="space-y-1" :title="stateTitle" data-testid="work-track-summary-state">
      <div class="flex items-center gap-2">
        <span class="w-7 shrink-0 text-[10px] text-muted-foreground">状态</span>
        <div class="flex h-1.5 min-w-10 flex-1 gap-px overflow-hidden rounded-full bg-muted" role="img" :aria-label="stateTitle">
          <span
            v-for="s in stateSegs"
            :key="s.state"
            class="block min-w-1 transition-[flex-grow] duration-300 motion-reduce:transition-none"
            :class="stateSegClass(s.state)"
            :style="{ flexGrow: s.count }"
          />
        </div>
      </div>
      <ul class="flex flex-wrap gap-x-2.5 gap-y-0.5 pl-9 text-[10px] text-foreground/80">
        <li v-for="s in stateSegs" :key="s.state" class="inline-flex items-center gap-1" :data-testid="`work-track-summary-state-${s.state}`">
          <i class="block size-1.5 rounded-full" :class="stateSegClass(s.state)" />{{ WORK_STATE_META[s.state].label }}
          <span class="font-mono tabular-nums text-muted-foreground">{{ s.count }}</span>
        </li>
      </ul>
    </div>

    <p class="text-[11px] text-muted-foreground md:col-span-2" :title="overallTitle" data-testid="work-track-summary-overall">{{ overall }}</p>
  </div>
</template>
