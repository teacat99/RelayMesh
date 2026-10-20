<script setup lang="ts">
// 群成员头像（预览视图阶段 E-d2）：一个色点加一个字，与统一消息卡上的发送者头像是同一份取色（lib/groupCard 的 memberIdentity）——
// 同一位成员在消息卡、成员面板、页头头像堆、@ 选择器里永远是同一个颜色与同一个字。
// 不用图片，亮 / 暗主题下都清晰；dotClass 给成员面板用：在头像右下角叠一个状态点（在线 / 等你 / 异常 / 离线），描一圈背景色让它从头像上分得开。
import { computed } from 'vue'
import { CARD_HUE_TONE_CLASS, CARD_TONE_CLASS } from '../../lib/messageCard'
import type { SenderIdentity } from '../../lib/groupCard'

const props = withDefaults(defineProps<{ identity: SenderIdentity; size?: 'sm' | 'md'; dotClass?: string }>(), { size: 'sm', dotClass: '' })

const hued = computed(() => typeof props.identity.hue === 'number' && Number.isFinite(props.identity.hue))
// 成员按色相取色；您自己固定是绿色（与您的 self 卡一致），系统 / 所有人是石板色。类名都是字面量，Tailwind 才扫得到。
const toneClass = computed(() => (hued.value ? CARD_HUE_TONE_CLASS : props.identity.kind === 'user' ? CARD_TONE_CLASS.green : CARD_TONE_CLASS.slate))
const hueStyle = computed(() => (hued.value ? { '--card-hue': String(props.identity.hue) } : undefined))
const sizeClass = computed(() => (props.size === 'md' ? 'size-5 text-[10px]' : 'size-4 text-[9px]'))
</script>

<template>
  <span
    class="relative inline-grid shrink-0 place-items-center rounded-full bg-[color:var(--card-tone)] font-semibold leading-none text-card"
    :class="[toneClass, sizeClass]"
    :style="hueStyle"
    :title="identity.name"
    aria-hidden="true"
    data-testid="member-avatar"
    :data-hue="hued ? identity.hue : undefined"
  >{{ identity.avatar }}<span
    v-if="dotClass"
    class="absolute -bottom-0.5 -right-0.5 size-2 rounded-full ring-2 ring-background"
    :class="dotClass"
    data-testid="member-status-dot"
  /></span>
</template>
