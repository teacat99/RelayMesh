<script setup lang="ts">
import { computed } from 'vue'
import { DialogDescription, type DialogDescriptionProps } from 'radix-vue'
import { cn } from '@/lib/utils'

const props = defineProps<DialogDescriptionProps & { class?: string }>()
const classes = computed(() => cn('text-sm text-muted-foreground', props.class))
</script>
<template>
  <DialogDescription :as-child="asChild" :class="classes">
    <slot />
  </DialogDescription>
</template>
