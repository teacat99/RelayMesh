<script setup lang="ts">
import { ref } from 'vue'
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter } from '../ui/dialog'
import Button from '../ui/button/Button.vue'

// 通用确认对话框：用于终止会话、删除主机等不可撤销的操作。
const props = defineProps<{
  open: boolean
  title: string
  description?: string
  confirmText?: string
  destructive?: boolean
  busy?: boolean
  /** 打开时的初始焦点：默认交给对话框（第一个可聚焦的元素）；传 confirm 则落在确认按钮上（回车即确认，例如多行粘贴确认）。 */
  initialFocus?: 'confirm'
}>()

const emit = defineEmits<{
  (e: 'update:open', v: boolean): void
  (e: 'confirm'): void
}>()

const confirmBtn = ref<{ $el?: HTMLElement } | null>(null)

function onOpenAutoFocus(ev: Event) {
  if (props.initialFocus !== 'confirm') return
  ev.preventDefault()
  confirmBtn.value?.$el?.focus()
}
</script>

<template>
  <Dialog :open="open" @update:open="(v: boolean) => emit('update:open', v)">
    <!-- layer="top"：确认框常从别的弹窗里（删除主机、结束会话……）打开，必须叠在它们之上 -->
    <DialogContent layer="top" class="max-w-[400px] rounded-sm p-5 gap-4" :show-close="false" @open-auto-focus="onOpenAutoFocus">
      <DialogHeader class="space-y-1.5 text-left">
        <DialogTitle class="text-sm font-bold">{{ title }}</DialogTitle>
        <DialogDescription v-if="description" class="text-xs leading-relaxed text-muted-foreground">{{ description }}</DialogDescription>
      </DialogHeader>
      <!-- 需要附加选项 / 提示的确认（例如删除主机时的「同时卸载设备端」）可以放进默认插槽 -->
      <slot />
      <DialogFooter class="flex-row justify-end gap-2 sm:space-x-0">
        <Button variant="outline" size="sm" class="rounded-sm text-xs" :disabled="busy" @click="emit('update:open', false)">取消</Button>
        <Button
          ref="confirmBtn"
          :variant="destructive ? 'destructive' : 'default'"
          size="sm"
          class="rounded-sm text-xs"
          :disabled="busy"
          @click="emit('confirm')"
        >
          {{ confirmText || '确认' }}
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
