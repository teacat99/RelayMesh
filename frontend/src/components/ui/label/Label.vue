<script setup lang="ts">
import { computed } from 'vue'
import { Label as LabelPrimitive } from 'radix-vue'
import { cn } from '@/lib/utils'

defineOptions({ inheritAttrs: true })

interface Props {
  class?: string
}

const props = defineProps<Props>()

const classes = computed(() =>
  cn(
    'text-sm font-medium leading-none text-foreground/90 peer-disabled:cursor-not-allowed peer-disabled:opacity-70',
    props.class
  )
)
</script>

<template>
  <LabelPrimitive :class="classes">
    <slot />
  </LabelPrimitive>
</template>
