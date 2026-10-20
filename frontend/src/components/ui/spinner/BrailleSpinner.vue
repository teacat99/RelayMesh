<script setup lang="ts">
// 「工作中 / 运行中」指示符（#130 / #197）：盲文旋转符 ⠋ ⠙ ⠹ ⠸ ⠼ ⠴ ⠦ ⠧ ⠇ ⠏，80ms 一帧。
// 动画是纯 CSS（见文件末尾的样式块）：十行字形竖排在固定大小的裁剪盒里，transform + steps(10) 每 80ms 上移一行，
// 由浏览器的合成器线程推进——没有 JS 定时器、不写 DOM、不触发样式重算与布局（原因与取舍见 lib/spinner.ts）。
// 页面隐藏时浏览器自己不再出帧；「减少动态效果」时不挂动画，停在第一帧。
// 只用在「正在工作 / 正在运行」的状态指示上；保存、读取这类普通加载转圈仍用 Loader。
// 读屏不读这串符号（aria-hidden）：状态文字由旁边的文案 / aria-label 负责。
// 尺寸只由 size 决定：行高要恰好等于盒子高度，所以调用方额外传来的 w-* / h-* 类不会生效（样式没有放进 Tailwind 的分层里）。
import { alignSpinnerPhase } from '@/lib/spinnerPhase'

withDefaults(defineProps<{ size?: 'sm' | 'md' }>(), { size: 'md' })

// 动画一开始就把起点对齐到文档时间线 0 点：全页所有指示符同一帧（只在开始的那一刻改一次，之后没有脚本参与）。
function onStart(e: AnimationEvent) {
  alignSpinnerPhase(e.target as Element)
}
</script>

<template>
  <span aria-hidden="true" data-spinner :data-size="size" class="braille-spinner font-mono select-none">
    <span data-spinner-strip class="braille-strip" @animationstart="onStart" />
  </span>
</template>

<style>
/*
 * 盲文转圈（#197）。边长 --bs-size；字号与行高都等于它，所以「一行」恰好是裁剪盒的高度，竖条每上移一行就换一个字形。
 * 十行字形写在伪元素的 content 里（\A 是换行；不放进 DOM，所以读屏、复制、textContent 里都没有这串符号）。
 * 这些数字（10 行、800ms = 10 × 80ms、steps(10)、translateY(-100%)、关键帧名 braille-spin）与 lib/spinner.ts
 * 的常量一一对应，lib/spinner.test.ts 逐条守着，改一处要同步改另一处。
 * 动画只动 transform：合成器线程独自推进，主线程没有逐帧的样式重算 / 布局 / 绘制。
 */
.braille-spinner {
  --bs-size: 0.875rem;
  display: inline-flex;
  flex-direction: column;
  flex-shrink: 0;
  width: var(--bs-size);
  height: var(--bs-size);
  overflow: hidden;
  font-size: var(--bs-size);
  line-height: var(--bs-size);
}
.braille-spinner[data-size='sm'] {
  --bs-size: 0.75rem;
}
.braille-strip {
  flex: none;
  width: 100%;
}
.braille-strip::before {
  content: '⠋\A⠙\A⠹\A⠸\A⠼\A⠴\A⠦\A⠧\A⠇\A⠏';
  display: block;
  white-space: pre;
  text-align: center;
}
@media (prefers-reduced-motion: no-preference) {
  .braille-strip {
    animation: braille-spin 800ms steps(10, end) infinite;
  }
}
@keyframes braille-spin {
  to {
    transform: translateY(-100%);
  }
}
</style>
