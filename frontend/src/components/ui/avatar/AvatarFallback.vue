<script setup lang="ts">
import { computed } from 'vue'
import { AvatarFallback, type AvatarFallbackProps } from 'radix-vue'
import { cn } from '@/lib/utils'

const props = defineProps<AvatarFallbackProps & { class?: string }>()
const classes = computed(() =>
  cn('flex size-full items-center justify-center rounded-full bg-primary/10 text-primary font-medium', props.class)
)
</script>

<template>
  <AvatarFallback :class="classes" :delay-ms="delayMs">
    <slot />
  </AvatarFallback>
</template>
