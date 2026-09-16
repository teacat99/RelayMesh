import { toast as sonnerToast } from 'vue-sonner'
import { h } from 'vue'
import { Trash2 } from 'lucide-vue-next'

export { default as Toaster } from './Sonner.vue'

/**
 * 专属可撤销删除通知 (Destructive Undo Toast)
 * 采用克制深红微边框、红色垃圾桶图元与红色一体化 Action 撤销手柄
 */
export function toastDestructive(message: string, onUndo: () => void, duration = 7000) {
  return sonnerToast(message, {
    duration,
    icon: h(Trash2, { class: 'w-3.5 h-3.5 text-destructive shrink-0' }),
    classes: {
      toast: '!border-destructive/40 !bg-card/95',
      actionButton: '!text-destructive hover:!text-destructive-foreground hover:!bg-destructive/80 !border-destructive/30',
      closeButton: 'hover:!text-destructive !border-destructive/30'
    },
    action: {
      label: '↺',
      onClick: onUndo
    }
  })
}

export const toast = sonnerToast
