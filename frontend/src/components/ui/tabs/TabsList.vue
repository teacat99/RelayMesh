<script setup lang="ts">
import { computed } from 'vue'
import { TabsList, type TabsListProps } from 'radix-vue'
import { cn } from '@/lib/utils'

interface Props extends TabsListProps {
  class?: string
}

const props = defineProps<Props>()

const classes = computed(() =>
  cn(
    'inline-flex h-10 items-center justify-start rounded-md bg-muted p-1 text-muted-foreground',
    props.class
  )
)
</script>

<template>
  <TabsList :loop="loop" :class="classes">
    <slot />
  </TabsList>
</template>
