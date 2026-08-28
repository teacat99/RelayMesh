<script setup lang="ts">
import { computed } from 'vue'
import { DropdownMenuItem, type DropdownMenuItemProps, useForwardProps } from 'radix-vue'
import { cn } from '@/lib/utils'

const props = defineProps<DropdownMenuItemProps & { class?: string, inset?: boolean }>()

const delegated = computed(() => {
  const { class: _c, inset: _i, ...rest } = props
  return rest
})
const forwarded = useForwardProps(delegated)

const classes = computed(() =>
  cn(
    'relative flex cursor-default select-none items-center gap-2 rounded-sm px-2 py-1.5 text-sm outline-none transition-colors focus:bg-accent focus:text-accent-foreground data-[disabled]:pointer-events-none data-[disabled]:opacity-50 [&_svg]:size-4 [&_svg]:shrink-0',
    props.inset && 'pl-8',
    props.class
  )
)
</script>

<template>
  <DropdownMenuItem v-bind="forwarded" :class="classes">
    <slot />
  </DropdownMenuItem>
</template>
