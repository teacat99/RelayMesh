<script setup lang="ts">
import { computed } from 'vue'
import { TabsTrigger, type TabsTriggerProps, useForwardProps } from 'radix-vue'
import { cn } from '@/lib/utils'

interface Props extends TabsTriggerProps {
  class?: string
}

const props = defineProps<Props>()

const delegated = computed(() => {
  const { class: _c, ...rest } = props
  return rest
})
const forwarded = useForwardProps(delegated)

const classes = computed(() =>
  cn(
    'inline-flex items-center justify-center whitespace-nowrap rounded px-3 py-1.5 text-sm font-medium ring-offset-background transition-all focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 disabled:pointer-events-none disabled:opacity-50 data-[state=active]:bg-card data-[state=active]:text-foreground data-[state=active]:shadow',
    props.class
  )
)
</script>

<template>
  <TabsTrigger v-bind="forwarded" :class="classes">
    <slot />
  </TabsTrigger>
</template>
