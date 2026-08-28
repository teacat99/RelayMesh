<script setup lang="ts">
import { computed } from 'vue'
import { cn } from '@/lib/utils'
interface Props { class?: string, containerClass?: string }
const props = defineProps<Props>()
const classes = computed(() => cn('w-full caption-bottom text-sm', props.class))
const containerClasses = computed(() =>
  cn('relative w-full overflow-x-auto rounded-md border border-border', props.containerClass)
)
</script>

<template>
  <div :class="containerClasses">
    <table :class="classes"><slot /></table>
  </div>
</template>
