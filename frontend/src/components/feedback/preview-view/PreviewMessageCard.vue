<script setup lang="ts">
// 统一消息卡的骨架（#120 · D-273 阶段 B，只给预览视图用）。
//
// 正文里每一种话——进度通知、汇报、提问、计划、您的回复——都套同一张卡，元素与顺序固定：
//   左色条（按类型的色调）· 发送方头像与名称 · 类型标签 · 标题 · 时间 · [状态] · 更多菜单 ⋯
//   正文（Markdown 全文，不折叠、不限高） · 脚注（引用、提交等）
// 类型 → 色调 / 标签 / 发送方的对照在 lib/messageCard.ts；这里只负责摆放，内容由槽位填：
//   默认槽 = 正文；status = 头部里的状态标签；menu = 「⋯」菜单；footer = 脚注。
// 根节点是一个 <article>，调用方传的 data-testid / data-* 会落在它上面。
import { computed } from 'vue'
import { CARD_CHIP_CLASS, CARD_HUE_TONE_CLASS, CARD_SENDERS, CARD_TONE_CLASS, CARD_TYPES, type CardType } from '../../../lib/messageCard'

const props = defineProps<{
  type: CardType
  /** 标题（可无）：这条话的名字；过长时单行截断，完整内容放在悬停提示里 */
  title?: string
  /** 头部的时间文案（已格式化）与完整时间（悬停提示） */
  time?: string
  timeTitle?: string
  /** 标题位的 data-testid，默认 card-title。待处理的提问 / 计划卡沿用经典的 confirm-<编号>-title，两种视图的 e2e 命名一致。 */
  titleTestid?: string
  /**
   * 具名发送者（群聊，阶段 E-d）：名称与头像字，覆盖按类型取的「智能体 / 您」。
   * 有名称时手机上也显示（群里要分清是谁说的；单会话里名称恒为「智能体」，才让位给标题）。
   */
  senderName?: string
  senderAvatar?: string
  /** 群成员的色相（0–360）：有值时色条 / 头像 / 标签取成员色，不再按类型取色调（成员色由 lib/groupCard 稳定分配）。 */
  hue?: number | null
  /** 不画类型标签：群里的话没有「汇报 / 回复」之分。 */
  hideType?: boolean
}>()

const meta = computed(() => CARD_TYPES[props.type])
const sender = computed(() => {
  const base = CARD_SENDERS[meta.value.sender]
  return { avatar: props.senderAvatar || base.avatar, label: props.senderName || base.label }
})
const hued = computed(() => typeof props.hue === 'number' && Number.isFinite(props.hue))
const toneClass = computed(() => (hued.value ? CARD_HUE_TONE_CLASS : CARD_TONE_CLASS[meta.value.tone]))
const hueStyle = computed(() => (hued.value ? { '--card-hue': String(props.hue) } : undefined))
const nameClass = computed(() =>
  props.senderName ? 'min-w-0 max-w-[8.5rem] truncate font-semibold text-foreground' : 'hidden shrink-0 font-semibold text-foreground sm:inline',
)

// 您的话靠右、窄一档（左边让出 64px，手机 24px），底色是色调的 5%，色条在右边（像聊天里自己发的气泡，追加 #224）；
// 智能体 / 系统的话通栏靠左、底色是卡片色，色条在左边。
// 类名必须是字面量（Tailwind 才扫得到）；--color-card 是 bg-card 背后的变量，暗色主题自动跟着换。
const USER_CLASS = 'ml-6 bg-[color:color-mix(in_srgb,var(--card-tone)_5%,var(--color-card))] sm:ml-16 border-r-[3px] border-r-[color:var(--card-tone)]'
const AGENT_CLASS = 'bg-card border-l-[3px] border-l-[color:var(--card-tone)]'
const alignClass = computed(() => (meta.value.sender === 'user' ? USER_CLASS : AGENT_CLASS))
</script>

<template>
  <article
    class="relative rounded-lg border border-border text-xs text-card-foreground"
    :class="[toneClass, alignClass]"
    :style="hueStyle"
    :data-card-type="type"
    :data-card-sender="meta.sender"
  >
    <header class="flex min-h-8 items-center gap-2 px-3 pt-[5px] text-xs text-muted-foreground" data-testid="card-head">
      <!-- 头像：16px 色点 + 一个字，不用图片，亮 / 暗主题下都清晰；手机上名称让位给标题，只留头像 -->
      <span
        class="grid size-4 shrink-0 place-items-center rounded-full bg-[color:var(--card-tone)] text-[9px] font-semibold leading-none text-card"
        aria-hidden="true"
        data-testid="card-avatar"
      >{{ sender.avatar }}</span>
      <span :class="nameClass" :title="senderName || undefined" data-testid="card-sender">{{ sender.label }}</span>
      <!-- 类型标签：描边与字色取自色调（CARD_CHIP_CLASS，事件行共用同一份）；带边框的小标签要 leading-4，否则会把头部撑高 -->
      <span v-if="!hideType" :class="CARD_CHIP_CLASS" data-testid="card-type">{{ meta.label }}</span>
      <span v-if="title" class="min-w-0 flex-1 truncate font-medium text-foreground" :title="title" :data-testid="titleTestid ?? 'card-title'">{{ title }}</span>
      <span v-else class="flex-1" aria-hidden="true" />
      <time v-if="time" class="shrink-0 font-mono text-[10px]" :title="timeTitle" data-testid="card-time">{{ time }}</time>
      <slot name="status" />
      <slot name="menu" />
    </header>
    <div class="px-3.5 pb-2.5 pt-[3px] leading-relaxed sm:text-[13px]" data-testid="card-body">
      <slot />
    </div>
    <footer v-if="$slots.footer" class="flex flex-wrap items-center gap-1.5 px-3.5 pb-2.5" data-testid="card-footer">
      <slot name="footer" />
    </footer>
  </article>
</template>
