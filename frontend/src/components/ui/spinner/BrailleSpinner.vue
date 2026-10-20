<script setup lang="ts">
// 「工作中 / 运行中」指示符（#130）：盲文旋转符 ⠋ ⠙ ⠹ ⠸ ⠼ ⠴ ⠦ ⠧ ⠇ ⠏，80ms 一帧。
// 全页共用一个计时器、页面隐藏时暂停、「减少动态效果」时静止（见 lib/spinner.ts）。
// 只用在「正在工作 / 正在运行」的状态指示上；保存、读取这类普通加载转圈仍用 Loader。
// 读屏不读这串符号（aria-hidden）：状态文字由旁边的文案 / aria-label 负责。
import { computed } from 'vue'
import { frameAt } from '@/lib/spinner'
import { useSpinnerFrame } from '@/lib/useSpinner'

const props = withDefaults(defineProps<{ size?: 'sm' | 'md' }>(), { size: 'md' })

const frame = useSpinnerFrame()
const glyph = computed(() => frameAt(frame.value))
// 固定的盒子：不同字体里盲文字形宽度不一样，盒子不变才不会让旁边的文字抖动。
const box = computed(() => (props.size === 'sm' ? 'w-3 h-3 text-[12px]' : 'w-3.5 h-3.5 text-[14px]'))
</script>

<template>
  <span
    aria-hidden="true"
    data-spinner
    :data-frame="frame"
    class="inline-flex items-center justify-center shrink-0 overflow-hidden font-mono leading-none select-none"
    :class="box"
  >{{ glyph }}</span>
</template>
