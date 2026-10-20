<script setup lang="ts">
// 日期分隔（#120 阶段 E-b）：跨天时画在新一天的第一个条目前面。
// 只是一条分隔，没有交互；不带 data-rail-*，右侧轨不为它画刻度。
defineProps<{ label: string }>()
</script>

<template>
  <div class="flex select-none items-center gap-3" role="separator" :aria-label="label" data-testid="date-separator" :data-date-label="label">
    <div class="h-px flex-1 bg-border/50"></div>
    <span class="rounded-full bg-muted/70 px-2.5 py-0.5 text-[10px] font-medium text-muted-foreground sm:text-[11px]">{{ label }}</span>
    <div class="h-px flex-1 bg-border/50"></div>
  </div>
</template>
