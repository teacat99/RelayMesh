<script setup lang="ts">
import { computed } from 'vue'
import { AvatarRoot, type AvatarRootProps } from 'radix-vue'
import { cn } from '@/lib/utils'

const props = defineProps<AvatarRootProps & { class?: string, size?: 'sm' | 'md' | 'lg' }>()

const sizeClass = computed(() => {
  switch (props.size) {
    case 'sm': return 'size-7 text-xs'
    case 'lg': return 'size-12 text-base'
    case 'md':
    default: return 'size-9 text-sm'
  }
})

const classes = computed(() =>
  cn(
    'relative flex shrink-0 overflow-hidden rounded-full',
    sizeClass.value,
    props.class
  )
)
</script>

<template>
  <AvatarRoot :class="classes">
    <slot />
  </AvatarRoot>
</template>
