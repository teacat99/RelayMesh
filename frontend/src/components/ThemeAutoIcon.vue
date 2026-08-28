<script setup lang="ts">
import { SunMoon } from 'lucide-vue-next'

const props = withDefaults(
  defineProps<{
    size?: string
    class?: string
  }>(),
  {
    size: 'w-3.5 h-3.5',
    class: ''
  }
)
</script>

<template>
  <div class="inline-flex items-center justify-center shrink-0" :class="[props.size, props.class]">
    <!-- 明 + 暗 纯净矢量图元 (SunMoon: 半日半月，完全继承父容器 currentColor) -->
    <SunMoon class="w-full h-full text-current stroke-[1.75]" />
  </div>
</template>
