<script setup lang="ts">
import { computed } from 'vue'
import { DropdownMenuSeparator, type DropdownMenuSeparatorProps } from 'radix-vue'
import { cn } from '@/lib/utils'

const props = defineProps<DropdownMenuSeparatorProps & { class?: string }>()
const classes = computed(() => cn('-mx-1 my-1 h-px bg-border', props.class))
</script>

<template>
  <DropdownMenuSeparator :class="classes" />
</template>
