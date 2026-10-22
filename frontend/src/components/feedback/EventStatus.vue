<script setup lang="ts">
// 事件行里的状态小标签（#120 · D-273 阶段 C2，预览视图专用）：
// tone 取色调（描边、底、字都取该色调，元素自己带 CARD_TONE_CLASS，所以放在别的色调的行里也行），plain 是中性的「已处理」。
// pulse = 前面加一个脉动的小点，给「还要您处理」的状态用。
import { computed } from 'vue'
import { CARD_CHIP_CLASS, CARD_CHIP_PLAIN_CLASS, CARD_TONE_CLASS, type CardTone } from '../../lib/messageCard'

const props = defineProps<{ tone: CardTone | 'plain'; pulse?: boolean }>()
const cls = computed(() => (props.tone === 'plain' ? CARD_CHIP_PLAIN_CLASS : [CARD_CHIP_CLASS, CARD_TONE_CLASS[props.tone]]))
</script>

<template>
  <span :class="cls" class="gap-1" data-testid="event-status">
    <i v-if="pulse" class="size-1.5 shrink-0 animate-pulse rounded-full bg-[color:var(--card-tone)]" aria-hidden="true" />
    <slot />
  </span>
</template>
