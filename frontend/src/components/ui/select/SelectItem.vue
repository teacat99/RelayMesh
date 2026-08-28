<script setup lang="ts">
import { computed } from 'vue'
import { Check } from 'lucide-vue-next'
import {
  SelectItem,
  SelectItemIndicator,
  type SelectItemProps,
  SelectItemText,
  useForwardProps
} from 'radix-vue'
import { cn } from '@/lib/utils'

const props = defineProps<SelectItemProps & { class?: string }>()

const delegated = computed(() => {
  const { class: _c, ...rest } = props
  return rest
})
const forwarded = useForwardProps(delegated)

const classes = computed(() =>
  cn(
    'relative flex w-full cursor-default select-none items-center rounded-sm py-1.5 pl-2 pr-8 text-sm outline-none focus:bg-accent focus:text-accent-foreground data-[disabled]:pointer-events-none data-[disabled]:opacity-50',
    props.class
  )
)
</script>

<template>
  <SelectItem v-bind="forwarded" :class="classes">
    <span class="absolute right-2 flex size-3.5 items-center justify-center">
      <SelectItemIndicator>
        <Check class="size-4" />
      </SelectItemIndicator>
    </span>
    <SelectItemText>
      <slot />
    </SelectItemText>
  </SelectItem>
</template>
