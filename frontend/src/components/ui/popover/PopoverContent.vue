<script setup lang="ts">
import { computed } from 'vue'
import {
  PopoverContent,
  type PopoverContentEmits,
  type PopoverContentProps,
  PopoverPortal,
  useForwardPropsEmits
} from 'radix-vue'
import { cn } from '@/lib/utils'

const props = withDefaults(defineProps<PopoverContentProps & { class?: string }>(), {
  align: 'center',
  sideOffset: 6
})
const emits = defineEmits<PopoverContentEmits>()

const delegated = computed(() => {
  const { class: _c, ...rest } = props
  return rest
})
const forwarded = useForwardPropsEmits(delegated, emits)

const classes = computed(() =>
  cn(
    'z-50 w-72 rounded-md border border-border bg-popover p-4 text-popover-foreground shadow-float outline-none data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0 data-[state=closed]:zoom-out-95 data-[state=open]:zoom-in-95',
    props.class
  )
)
</script>

<template>
  <PopoverPortal>
    <PopoverContent v-bind="forwarded" :class="classes">
      <slot />
    </PopoverContent>
  </PopoverPortal>
</template>
