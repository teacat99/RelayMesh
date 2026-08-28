<script setup lang="ts">
import { computed } from 'vue'
import {
  SelectContent,
  type SelectContentEmits,
  type SelectContentProps,
  SelectPortal,
  SelectViewport,
  useForwardPropsEmits
} from 'radix-vue'
import { cn } from '@/lib/utils'

const props = withDefaults(defineProps<SelectContentProps & { class?: string }>(), {
  position: 'popper'
})
const emits = defineEmits<SelectContentEmits>()

const delegated = computed(() => {
  const { class: _c, ...rest } = props
  return rest
})
const forwarded = useForwardPropsEmits(delegated, emits)

const classes = computed(() =>
  cn(
    'relative z-50 max-h-[--radix-select-content-available-height] min-w-[8rem] overflow-y-auto overflow-x-hidden rounded-md border border-border bg-popover text-popover-foreground shadow-float data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0 data-[state=closed]:zoom-out-95 data-[state=open]:zoom-in-95 data-[side=bottom]:translate-y-1 data-[side=top]:-translate-y-1',
    props.class
  )
)
</script>

<template>
  <SelectPortal>
    <SelectContent v-bind="forwarded" :class="classes">
      <SelectViewport class="p-1">
        <slot />
      </SelectViewport>
    </SelectContent>
  </SelectPortal>
</template>
