<script setup lang="ts">
// 时间线上叠成一摞的卡片（#105）：没有「交流」（您的话、智能体的话）把它们隔开时，
// 连续的任务进展 / 已处理的提问默认叠成一摞，点开看全部、再点收起。
// 还没答复的提问不会出现在摞里（见 lib/timeline 的 isStackable）：该您点的卡永远摊开。
import { computed } from 'vue'
import dayjs from 'dayjs'
import { ChevronUp, Layers } from 'lucide-vue-next'
import { entryPreview, stackSummaryText, summarizeStack, type TimelineEntry } from '../../lib/timeline'
import { EVENT_BOX } from '../../lib/eventRow'
import { CARD_CHIP_CLASS, CARD_CHIP_PLAIN_CLASS, CARD_TONE_CLASS, CARD_TYPES } from '../../lib/messageCard'
import { stackChipModel, stackTypeCounts } from '../../lib/stackTypes'
import EventRowHead from './EventRowHead.vue'

// 折叠的汇总行是 32px 的事件行（#120 预览视图，阶段 C2：[摞图标] N 张卡片 …… 概要 · 最新：… · 时间范围 ˅），
// 背板仍留着（两层错开的边，看起来是「一摞」）。
const props = defineProps<{ entries: TimelineEntry[]; expanded: boolean }>()
const emit = defineEmits<{ (e: 'toggle'): void }>()

const summary = computed(() => summarizeStack(props.entries))
const summaryText = computed(() => stackSummaryText(summary.value))
const latest = computed(() => props.entries[props.entries.length - 1])
const preview = computed(() => (latest.value ? entryPreview(latest.value) : ''))
const TONE = CARD_TONE_CLASS[CARD_TYPES.task.tone]
// 摞头给内含的每种类型一个色标签（任务 / 提问 / 计划 / 决策），悬停写出各自的张数（#160，追加 #248 / Q24）
const chipModel = computed(() => stackChipModel(stackTypeCounts(props.entries)))

function clock(at: string): string {
  const t = dayjs(at)
  if (!t.isValid()) return ''
  const now = dayjs()
  if (t.isSame(now, 'day')) return t.format('HH:mm')
  if (t.isSame(now, 'year')) return t.format('MM-DD·HH:mm')
  return t.format('YYYY·MM-DD·HH:mm')
}

/** 时间范围：同一分钟只写一个时间。 */
const range = computed(() => {
  const a = clock(summary.value.firstAt)
  const b = clock(summary.value.lastAt)
  if (!a) return b
  return a === b || !b ? a : `${a} – ${b}`
})
</script>

<template>
  <div class="relative" data-testid="card-stack" :data-count="entries.length" :data-expanded="expanded ? '1' : '0'">
    <template v-if="!expanded">
      <!-- 两层错开的背板，让它看起来是「一摞」而不是一条普通的横幅 -->
      <div class="pointer-events-none absolute inset-x-3 -bottom-1.5 h-3 rounded-b-sm border border-t-0 border-border/40 bg-muted/20" aria-hidden="true"></div>
      <div class="pointer-events-none absolute inset-x-1.5 -bottom-0.5 h-3 rounded-b-sm border border-t-0 border-border/50 bg-muted/30" aria-hidden="true"></div>
      <!-- 事件行：整行是展开按钮；概要、「最新：…」与时间范围只在宽屏出现
           （手机上 [摞图标] + 最多 3 个类型标签 + 跨天的时间范围放不下 390px 的一行，会把行末箭头挤出卡片，#167） -->
      <div :class="[EVENT_BOX, TONE]" class="shadow-2xs">
        <EventRowHead
          :title="`${entries.length} 张卡片`"
          :time="range"
          phone-hide-time
          interactive
          :expanded="false"
          main-testid="card-stack-toggle"
          title-testid="card-stack-count"
          @click="emit('toggle')"
        >
          <template #lead>
            <Layers class="size-3.5 shrink-0 text-muted-foreground" />
            <!-- 类型标签：一种类型一个，与卡片头部的类型标签同字同色；最多 3 个，多出的并成「+N」（悬停写全） -->
            <span v-if="chipModel.chips.length > 0" class="flex shrink-0 items-center gap-1" :title="chipModel.text" data-testid="card-stack-types">
              <span
                v-for="c in chipModel.chips"
                :key="c.type"
                :class="[CARD_CHIP_CLASS, c.toneClass]"
                :data-type="c.type"
                data-testid="card-stack-type"
              >{{ c.label }}</span>
              <span v-if="chipModel.more > 0" :class="CARD_CHIP_PLAIN_CLASS" data-testid="card-stack-type-more">+{{ chipModel.more }}</span>
            </span>
          </template>
          <template #preview>
            <span class="hidden truncate text-[11px] text-muted-foreground sm:block">
              <span data-testid="card-stack-summary">{{ summaryText }}</span>
              <span class="text-muted-foreground/60"> · </span>
              <span data-testid="card-stack-preview">最新：{{ preview }}</span>
            </span>
          </template>
        </EventRowHead>
      </div>
    </template>

    <template v-else>
      <button
        type="button"
        class="flex w-full cursor-pointer items-center gap-2 px-1 pb-1.5 text-left text-[11px] text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-primary/50"
        :aria-expanded="true"
        data-testid="card-stack-toggle"
        @click="emit('toggle')"
      >
        <Layers class="size-3.5 shrink-0" />
        <span>{{ entries.length }} 张卡片</span>
        <span class="font-mono text-[10px] opacity-70">{{ range }}</span>
        <span class="ml-auto inline-flex items-center gap-0.5">收起<ChevronUp class="size-3.5" /></span>
      </button>
      <div class="space-y-2 border-l-2 border-border/50 pl-3" data-testid="card-stack-items">
        <div v-for="e in entries" :key="e.key">
          <slot name="card" :entry="e" />
        </div>
      </div>
    </template>
  </div>
</template>
