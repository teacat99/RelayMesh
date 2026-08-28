<script setup lang="ts">
import { computed } from 'vue'
import { cn } from '@/lib/utils'
interface Props { class?: string }
const props = defineProps<Props>()
const classes = computed(() =>
  cn('h-10 px-3 text-left align-middle text-xs font-semibold text-muted-foreground uppercase tracking-wide [&:has([role=checkbox])]:pr-0', props.class)
)
</script>
<template><th :class="classes"><slot /></th></template>
