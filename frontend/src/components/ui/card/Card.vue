<script setup lang="ts">
import { computed } from 'vue'
import { cn } from '@/lib/utils'

interface Props { class?: string }
const props = defineProps<Props>()
const classes = computed(() =>
  cn('rounded-lg border border-border bg-card text-card-foreground shadow-soft', props.class)
)
</script>

<template>
  <div :class="classes"><slot /></div>
</template>
