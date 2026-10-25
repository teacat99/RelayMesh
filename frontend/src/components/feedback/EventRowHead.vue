<script setup lang="ts">
// 事件行的那一行（#120 · D-273 阶段 C2）：卡片摞、恢复胶囊、任务进展，以及 compact 版式的已处理提问 / 决策，都用它。
//
// 没有正文的事件——任务进展、已处理的提问、文档决策汇总、恢复胶囊、「N 张卡片」汇总——共用这一行：
//   类型标签 · [编号] · 标题 · 一行预览 · 时间 · [状态] · [展开箭头]    行末可再放按钮（trail 槽）
// 外面的盒子（左色条、边框、展开体）由各叶子用 lib/eventRow 的 EVENT_BOX 自己画——展开后的内容各不相同；
// 这里只管那一行：根节点固定 30px，放进盒子（上下边框各 1px）正好 32px，所以这一行里不能有会把它撑高的东西。
//   interactive：主体是一个整行可点的按钮（带悬停与焦点环）；否则是普通块（行末的按钮自己可点）。
//   expanded：传了就带 aria-expanded 与箭头（收起 → 向右，展开 → 向下）；不传时由 chevron 决定要不要画向右的箭头。
//   槽：lead（换掉类型标签，如「摞」用图标）、preview（换掉预览文字）、status（时间后面的状态标签，可放多个）、trail（行末按钮）。
//   预览只在宽屏（≥ sm）出现，手机上让位给标题（完整内容在悬停提示 / 展开后 / 详情页里）。
import { computed } from 'vue'
import { ChevronDown, ChevronRight } from 'lucide-vue-next'
import { CARD_CHIP_CLASS, CARD_TYPES, type CardType } from '../../lib/messageCard'
import { EVENT_LINE_PX } from '../../lib/eventRow'

const props = defineProps<{
  /** 类型：决定标签的字与色（色条的色由外层盒子的 CARD_TONE_CLASS 给）；用了 lead 槽可以不传 */
  type?: CardType
  title: string
  /** 标题前的编号（#12 / Q3 / r4…），等宽字 */
  lead?: string
  preview?: string
  time?: string
  /** 时间的悬停提示（完整时间） */
  timeTitle?: string
  /** 手机上不画时间：这一行里别的东西（摞头的类型标签）加上一段跨天的时间范围（「10-17·20:28 – 10-17·20:31」）
   *  比 390px 宽的一行宽，shrink-0 的箭头会被挤出卡片（#167）；完整的时间在展开后 / 详情页里仍看得到 */
  phoneHideTime?: boolean
  interactive?: boolean
  expanded?: boolean
  /** 没传 expanded 时，是否在行末画一个向右的箭头（任务行：点一下进详情） */
  chevron?: boolean
  /** 主体按钮的悬停提示 */
  openTitle?: string
  /** 各处 testid：叶子按各自既有的 testid 传进来（e2e 按这些名字找各类事件行） */
  mainTestid?: string
  titleTestid?: string
  previewTestid?: string
}>()
const emit = defineEmits<{ (e: 'click'): void }>()

const label = computed(() => (props.type ? CARD_TYPES[props.type].label : ''))
const arrow = computed(() => {
  if (props.expanded !== undefined) return props.expanded ? ChevronDown : ChevronRight
  return props.chevron ? ChevronRight : null
})
const lineStyle = { height: `${EVENT_LINE_PX}px` }
</script>

<template>
  <div class="flex items-center" :style="lineStyle" data-testid="event-row">
    <!-- 右边的 12px 内边距：没有行末按钮时留在主体里（整行都可点），有行末按钮时留给行末那一组 -->
    <component
      :is="interactive ? 'button' : 'div'"
      :type="interactive ? 'button' : undefined"
      class="flex h-full min-w-0 flex-1 items-center gap-2 pl-3 text-left"
      :class="[
        $slots.trail ? '' : 'pr-3',
        interactive ? 'cursor-pointer transition-colors hover:bg-muted/40 focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-inset focus-visible:ring-primary/50' : '',
        interactive ? ($slots.trail ? 'rounded-l-[7px]' : 'rounded-[7px]') : '',
      ]"
      :aria-expanded="interactive && expanded !== undefined ? expanded : undefined"
      :title="interactive ? openTitle : undefined"
      :data-testid="mainTestid || 'event-main'"
      @click="interactive && emit('click')"
    >
      <slot name="lead">
        <span v-if="label" :class="CARD_CHIP_CLASS" data-testid="event-type">{{ label }}</span>
      </slot>
      <span v-if="lead" class="shrink-0 font-mono text-[11px] font-semibold text-foreground/70" data-testid="event-lead">{{ lead }}</span>
      <!-- 标题可以被截断（shrink），预览吃剩下的位置：放不下时先让预览消失，再截标题 -->
      <span class="min-w-0 truncate text-[13px] font-medium text-foreground" :title="title" :data-testid="titleTestid || 'event-title'">{{ title }}</span>
      <span class="min-w-0 flex-1" data-testid="event-fill">
        <slot name="preview">
          <span v-if="preview" class="hidden truncate text-[11px] text-muted-foreground sm:block" :title="preview" :data-testid="previewTestid || 'event-preview'">{{ preview }}</span>
        </slot>
      </span>
      <!-- 时间：行末有按钮的行（带「引用」/「去…处理」）、传了 phoneHideTime 的行，手机上放不下，让位给标题（详情 / 展开后里仍有完整时间） -->
      <time
        v-if="time"
        class="shrink-0 font-mono text-[10px] text-muted-foreground/70"
        :class="$slots.trail || phoneHideTime ? 'max-sm:hidden' : ''"
        :title="timeTitle"
        data-testid="event-time"
      >{{ time }}</time>
      <slot name="status" />
      <component :is="arrow" v-if="arrow" class="size-3.5 shrink-0 text-muted-foreground/60" aria-hidden="true" />
    </component>
    <div v-if="$slots.trail" class="ml-1.5 flex shrink-0 items-center gap-1.5 pr-3" data-testid="event-trail">
      <slot name="trail" />
    </div>
  </div>
</template>
