<script setup lang="ts">
import { ref, watch, nextTick } from 'vue'
import Dialog from '../ui/dialog/Dialog.vue'
import DialogContent from '../ui/dialog/DialogContent.vue'
import DialogTitle from '../ui/dialog/DialogTitle.vue'
import Button from '../ui/button/Button.vue'
import { Edit2 } from 'lucide-vue-next'

const props = defineProps<{
  open: boolean
  currentTitle: string
  itemId: string
  isWorkflow?: boolean
}>()

const emit = defineEmits<{
  (e: 'update:open', val: boolean): void
  (e: 'submit', newTitle: string): void
}>()

const titleInput = ref('')
const inputRef = ref<HTMLInputElement | null>(null)

watch(() => props.open, (isOpen) => {
  if (isOpen) {
    titleInput.value = props.currentTitle || ''
    nextTick(() => {
      inputRef.value?.focus()
      inputRef.value?.select()
    })
  }
})

function handleSave() {
  const trimmed = titleInput.value.trim()
  if (!trimmed) return
  emit('submit', trimmed)
  emit('update:open', false)
}

function handleCancel() {
  emit('update:open', false)
}
</script>

<template>
  <Dialog :open="open" @update:open="emit('update:open', $event)">
    <DialogContent class="sm:max-w-md p-4 bg-card border border-border rounded-md shadow-modal font-mono">
      <DialogTitle class="flex items-center gap-2 text-sm font-bold text-foreground mb-3">
        <Edit2 class="w-4 h-4 text-primary" />
        <span>{{ isWorkflow ? '重命名工作流' : '重命名会话' }}</span>
      </DialogTitle>

      <div class="space-y-3">
        <div class="space-y-1.5">
          <label class="text-[11px] font-bold text-muted-foreground uppercase tracking-wider">
            名称 / 标题
          </label>
          <input
            ref="inputRef"
            v-model="titleInput"
            class="w-full px-2.5 py-1.5 rounded-sm border border-border/80 bg-background text-sm font-mono text-foreground focus:outline-none focus:ring-1 focus:ring-primary/50"
            placeholder="请输入新的工作流名称"
            @keydown.enter.prevent="handleSave"
            @keydown.esc.prevent="handleCancel"
          />
          <p class="text-[10px] text-muted-foreground leading-normal">
            重命名后将自动锁定该名称，并在多轮交互与侧边栏中持续保持。
          </p>
        </div>

        <div class="flex items-center justify-end gap-2 pt-2 border-t border-border/50">
          <Button
            type="button"
            variant="ghost"
            size="sm"
            class="text-xs"
            @click="handleCancel"
          >
            取消
          </Button>
          <Button
            type="button"
            size="sm"
            class="text-xs px-3"
            :disabled="!titleInput.trim() || titleInput.trim() === props.currentTitle"
            @click="handleSave"
          >
            保存修改
          </Button>
        </div>
      </div>
    </DialogContent>
  </Dialog>
</template>
