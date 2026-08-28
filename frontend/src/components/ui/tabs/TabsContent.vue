<script setup lang="ts">
import { computed } from 'vue'
import { TabsContent, type TabsContentProps, useForwardProps } from 'radix-vue'
import { cn } from '@/lib/utils'

interface Props extends TabsContentProps {
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
    'mt-2 ring-offset-background focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2',
    props.class
  )
)
</script>

<template>
  <TabsContent v-bind="forwarded" :class="classes">
    <slot />
  </TabsContent>
</template>
