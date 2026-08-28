<script setup lang="ts">
import { computed } from 'vue'
import {
  TooltipContent,
  type TooltipContentEmits,
  type TooltipContentProps,
  TooltipPortal,
  useForwardPropsEmits
} from 'radix-vue'
import { cn } from '@/lib/utils'

const props = withDefaults(defineProps<TooltipContentProps & { class?: string }>(), {
  sideOffset: 4
})
const emits = defineEmits<TooltipContentEmits>()

const delegated = computed(() => {
  const { class: _c, ...rest } = props
  return rest
})
const forwarded = useForwardPropsEmits(delegated, emits)

const classes = computed(() =>
  cn(
    'z-50 overflow-hidden rounded-md bg-foreground px-3 py-1.5 text-xs font-medium text-background shadow-md animate-in fade-in-0 zoom-in-95 data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=closed]:zoom-out-95',
    props.class
  )
)
</script>

<template>
  <TooltipPortal>
    <TooltipContent v-bind="forwarded" :class="classes">
      <slot />
    </TooltipContent>
  </TooltipPortal>
</template>
