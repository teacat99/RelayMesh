<script setup lang="ts">
import { computed } from 'vue'
import { cn } from '@/lib/utils'
interface Props { class?: string }
const props = defineProps<Props>()
const classes = computed(() =>
  cn('p-3 align-middle [&:has([role=checkbox])]:pr-0', props.class)
)
</script>
<template><td :class="classes"><slot /></td></template>
