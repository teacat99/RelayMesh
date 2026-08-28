<script setup lang="ts">
import { computed } from 'vue'
import { DialogTitle, type DialogTitleProps } from 'radix-vue'
import { cn } from '@/lib/utils'

const props = defineProps<DialogTitleProps & { class?: string }>()
const classes = computed(() => cn('text-base font-semibold text-foreground', props.class))
</script>
<template>
  <DialogTitle :as-child="asChild" :class="classes">
    <slot />
  </DialogTitle>
</template>
