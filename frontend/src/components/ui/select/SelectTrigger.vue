<script setup lang="ts">
import { computed } from 'vue'
import { ChevronDown } from 'lucide-vue-next'
import { SelectIcon, SelectTrigger, type SelectTriggerProps, useForwardProps } from 'radix-vue'
import { cn } from '@/lib/utils'

const props = defineProps<SelectTriggerProps & { class?: string }>()

const delegated = computed(() => {
  const { class: _c, ...rest } = props
  return rest
})
const forwarded = useForwardProps(delegated)

const classes = computed(() =>
  cn(
    'flex h-9 w-full items-center justify-between whitespace-nowrap rounded-md border border-input bg-transparent px-3 py-2 text-sm shadow-sm ring-offset-background placeholder:text-muted-foreground focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-0 disabled:cursor-not-allowed disabled:opacity-50 [&>span]:line-clamp-1',
    props.class
  )
)
</script>

<template>
  <SelectTrigger v-bind="forwarded" :class="classes">
    <slot />
    <SelectIcon as-child>
      <ChevronDown class="size-4 opacity-50 shrink-0" />
    </SelectIcon>
  </SelectTrigger>
</template>
