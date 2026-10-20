<script setup lang="ts">
import { computed } from 'vue'
import {
  ContextMenuContent,
  type ContextMenuContentEmits,
  type ContextMenuContentProps,
  ContextMenuPortal,
  useForwardPropsEmits,
} from 'radix-vue'
import { cn } from '@/lib/utils'
import { LAYER_CLASS } from '@/lib/layers'

const props = defineProps<ContextMenuContentProps & { class?: string }>()
const emits = defineEmits<ContextMenuContentEmits>()

const delegated = computed(() => {
  const { class: _c, ...rest } = props
  return rest
})
const forwarded = useForwardPropsEmits(delegated, emits)

// 样式与下拉菜单一致，右键菜单和三点菜单看起来是同一套东西；层级用气泡层，保证在弹窗和侧栏之上。
const classes = computed(() =>
  cn(
    'min-w-[10rem] overflow-hidden rounded-md border border-border bg-popover p-1 text-popover-foreground shadow-float data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0 data-[state=closed]:zoom-out-95 data-[state=open]:zoom-in-95',
    LAYER_CLASS.popper,
    props.class,
  ),
)
</script>

<template>
  <ContextMenuPortal>
    <ContextMenuContent v-bind="forwarded" :class="classes">
      <slot />
    </ContextMenuContent>
  </ContextMenuPortal>
</template>
