<script setup lang="ts">
import { computed } from 'vue'
import { cn } from '@/lib/utils'
interface Props { class?: string }
const props = defineProps<Props>()
const classes = computed(() =>
  cn('border-b border-border transition-colors hover:bg-muted/40 data-[state=selected]:bg-muted', props.class)
)
</script>
<template><tr :class="classes"><slot /></tr></template>
