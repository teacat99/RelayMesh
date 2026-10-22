<script setup lang="ts">
// 侧栏会话卡上走字的那一小段计时（#215）。
//
// 自己订阅共享的秒级时钟、自己重画：时钟每走一秒，更新的只有这几个字，整张卡（以及整列卡片）不跟着重画。
// 原来是侧栏把一个每秒变化的 now 当 prop 灌给每一张卡，prop 一变，卡片不管用没用到都整张重画，几十张卡就是几十次。
//   bar   待答复的卡片顶部的计时条：「剩余 / 等待： mm:ss」，倒计时走完变红；
//   chip  已提交的卡片标题行右侧的小标签：「等待 / 执行： mm:ss」。
import { onBeforeUnmount } from 'vue'
import { useSessionTimer, type SessionTimerRaw } from '../../composables/useSessionTimer'
import { stableComputed } from '../../lib/stable'

const props = defineProps<{
  /** 会话的原始数据（侧栏条目的 raw） */
  raw: SessionTimerRaw | null | undefined
  variant: 'bar' | 'chip'
  isActive?: boolean
}>()

const { timerDisplay, cleanup } = useSessionTimer(
  () => props.raw,
  () => props.raw?.wait_countdown_minutes ?? 2,
)
onBeforeUnmount(cleanup)
// 字没变（比如已经停表的）就不重画
const info = stableComputed(() => timerDisplay.value)
</script>

<template>
  <span
    v-if="variant === 'bar'"
    data-testid="sidebar-timer"
    :class="info.isCountdown ? 'text-destructive font-bold' : (isActive ? 'font-bold text-foreground' : 'font-medium text-foreground')"
  >
    <span v-if="info.prefix" class="text-muted-foreground/80 text-[9px] mr-0.5">{{ info.prefix }}:</span>{{ info.text }}
  </span>
  <span
    v-else-if="info.text"
    data-testid="sidebar-timer"
    class="text-[9px] px-1 py-0 rounded-2xs bg-muted/70 text-muted-foreground font-mono"
    :title="info.prefix === '等待' ? '回复已提交，等待智能体来取（从它最近一次空回执算起）' : 'AI 任务执行耗时'"
  >
    {{ info.prefix }}: {{ info.text }}
  </span>
</template>
