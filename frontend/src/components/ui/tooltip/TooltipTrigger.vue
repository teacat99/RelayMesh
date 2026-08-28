<script setup lang="ts">
import { TooltipTrigger, type TooltipTriggerProps } from 'radix-vue'
const props = defineProps<TooltipTriggerProps & { class?: string }>()
</script>
<template>
  <TooltipTrigger v-bind="props"><slot /></TooltipTrigger>
</template>
