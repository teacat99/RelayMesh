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
import { dialogLayerClass, type DialogLayer } from '@/lib/layers'

// 根节点是 Portal（一个 Teleport 片段），额外的属性（如 data-testid）落不到任何元素上，Vue 会在开发环境报警；
// 手动把它们转给真正渲染出来的内容元素。
defineOptions({ inheritAttrs: false })
// layer：普通弹窗用默认的 base；会叠在别的弹窗之上的验证 / 确认框传 top（层级表见 lib/layers.ts）。
const props = defineProps<DialogContentProps & { class?: string, showClose?: boolean, layer?: DialogLayer }>()
const emits = defineEmits<DialogContentEmits>()

const delegated = computed(() => {
  const { class: _c, showClose: _s, layer: _l, ...rest } = props
  return rest
})
const forwarded = useForwardPropsEmits(delegated, emits)

const layerClass = computed(() => dialogLayerClass(props.layer))

const classes = computed(() =>
  cn(
    'fixed left-1/2 top-1/2 grid w-full max-w-lg -translate-x-1/2 -translate-y-1/2 gap-4 rounded-lg border border-border bg-card p-6 shadow-modal duration-200 data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0 data-[state=closed]:zoom-out-95 data-[state=open]:zoom-in-95 max-h-[90vh] overflow-y-auto',
    layerClass.value,
    props.class
  )
)

const overlayClasses = computed(() =>
  cn(
    'fixed inset-0 bg-black/60 backdrop-blur-xs data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0',
    layerClass.value
  )
)
</script>

<template>
  <DialogPortal>
    <DialogOverlay :class="overlayClasses" />
    <DialogContent v-bind="{ ...forwarded, ...$attrs }" :class="classes">
      <slot />
      <DialogClose
        v-if="showClose !== false"
        class="absolute right-4 top-4 rounded-sm opacity-70 ring-offset-background transition-opacity hover:opacity-100 focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-2 disabled:pointer-events-none"
      >
        <X class="size-4" />
        <span class="sr-only">Close</span>
      </DialogClose>
    </DialogContent>
  </DialogPortal>
</template>
