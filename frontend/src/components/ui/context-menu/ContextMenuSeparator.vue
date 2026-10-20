<script setup lang="ts">
import { computed } from 'vue'
import { ContextMenuSeparator, type ContextMenuSeparatorProps } from 'radix-vue'
import { cn } from '@/lib/utils'

const props = defineProps<ContextMenuSeparatorProps & { class?: string }>()
const classes = computed(() => cn('-mx-1 my-1 h-px bg-border', props.class))
</script>

<template>
  <ContextMenuSeparator :class="classes" />
</template>
