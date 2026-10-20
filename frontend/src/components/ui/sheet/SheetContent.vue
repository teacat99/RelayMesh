<script setup lang="ts">
import { computed } from 'vue'
import { X } from 'lucide-vue-next'
import {
  DialogClose,
  DialogContent,
  type DialogContentEmits,
  type DialogContentProps,
  DialogOverlay,
  DialogPortal,
  useForwardPropsEmits
} from 'radix-vue'
import { cn } from '@/lib/utils'
import { LAYER_CLASS } from '@/lib/layers'

// 根节点是 Portal（一个 Teleport 片段），额外的属性（如 data-testid）落不到任何元素上，Vue 会在开发环境报警；
// 手动把它们转给真正渲染出来的内容元素。
defineOptions({ inheritAttrs: false })

interface Props extends DialogContentProps {
  class?: string
  side?: 'top' | 'bottom' | 'left' | 'right'
  showClose?: boolean
}

const props = withDefaults(defineProps<Props>(), { side: 'right' })
const emits = defineEmits<DialogContentEmits>()

const delegated = computed(() => {
  const { class: _c, side: _s, showClose: _sc, ...rest } = props
  return rest
})
const forwarded = useForwardPropsEmits(delegated, emits)

const sideClass = computed(() => {
  switch (props.side) {
    case 'top':
      return 'inset-x-0 top-0 border-b data-[state=closed]:slide-out-to-top data-[state=open]:slide-in-from-top'
    case 'bottom':
      return 'inset-x-0 bottom-0 border-t data-[state=closed]:slide-out-to-bottom data-[state=open]:slide-in-from-bottom'
    case 'left':
      return 'inset-y-0 left-0 h-full w-3/4 border-r sm:max-w-sm data-[state=closed]:slide-out-to-left data-[state=open]:slide-in-from-left'
    case 'right':
    default:
      return 'inset-y-0 right-0 h-full w-full max-w-xl border-l data-[state=closed]:slide-out-to-right data-[state=open]:slide-in-from-right'
  }
})

const classes = computed(() =>
  cn(
    'fixed gap-4 bg-card p-6 shadow-modal transition ease-in-out data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=closed]:duration-200 data-[state=open]:duration-300 flex flex-col',
    LAYER_CLASS.sheet,
    sideClass.value,
    props.class
  )
)
const overlayClasses = cn(
  'fixed inset-0 bg-black/50 data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0',
  LAYER_CLASS.sheet
)
</script>

<template>
  <DialogPortal>
    <DialogOverlay :class="overlayClasses" />
    <DialogContent v-bind="{ ...forwarded, ...$attrs }" :class="classes">
      <slot />
      <DialogClose
        v-if="showClose !== false"
        class="absolute right-4 top-4 rounded-sm opacity-70 ring-offset-background transition-opacity hover:opacity-100 focus:outline-none focus:ring-2 focus:ring-ring"
      >
        <X class="size-4" />
        <span class="sr-only">Close</span>
      </DialogClose>
    </DialogContent>
  </DialogPortal>
</template>
