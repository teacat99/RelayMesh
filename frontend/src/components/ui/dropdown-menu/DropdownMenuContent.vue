<script setup lang="ts">
import { computed } from 'vue'
import {
  DropdownMenuContent,
  type DropdownMenuContentEmits,
  type DropdownMenuContentProps,
  DropdownMenuPortal,
  useForwardPropsEmits
} from 'radix-vue'
import { cn } from '@/lib/utils'
import { LAYER_CLASS } from '@/lib/layers'

// 根节点是 Portal（一个 Teleport 片段），额外的属性（如 data-testid）落不到任何元素上，Vue 会在开发环境报警；
// 手动把它们转给真正渲染出来的菜单内容。
defineOptions({ inheritAttrs: false })
const props = withDefaults(defineProps<DropdownMenuContentProps & { class?: string }>(), {
  sideOffset: 6
})
const emits = defineEmits<DropdownMenuContentEmits>()

const delegated = computed(() => {
  const { class: _c, ...rest } = props
  return rest
})
const forwarded = useForwardPropsEmits(delegated, emits)

const classes = computed(() =>
  cn(
    'min-w-[9rem] overflow-hidden rounded-md border border-border bg-popover p-1 text-popover-foreground shadow-float data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0 data-[state=closed]:zoom-out-95 data-[state=open]:zoom-in-95 data-[side=bottom]:slide-in-from-top-2 data-[side=left]:slide-in-from-right-2 data-[side=right]:slide-in-from-left-2 data-[side=top]:slide-in-from-bottom-2',
    LAYER_CLASS.popper,
    props.class
  )
)
</script>

<template>
  <DropdownMenuPortal>
    <DropdownMenuContent v-bind="{ ...forwarded, ...$attrs }" :class="classes">
      <slot />
    </DropdownMenuContent>
  </DropdownMenuPortal>
</template>
