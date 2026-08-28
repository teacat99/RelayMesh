<script setup lang="ts">
import { computed } from 'vue'
import { Separator, type SeparatorProps } from 'radix-vue'
import { cn } from '@/lib/utils'

interface Props extends SeparatorProps {
  class?: string
}

const props = withDefaults(defineProps<Props>(), {
  orientation: 'horizontal',
  decorative: true
})

const classes = computed(() =>
  cn(
    'shrink-0 bg-border',
    props.orientation === 'horizontal' ? 'h-px w-full' : 'h-full w-px',
    props.class
  )
)
</script>

<template>
  <Separator
    :orientation="orientation"
    :decorative="decorative"
    :class="classes"
  />
</template>
