<script setup lang="ts">
import { ref, watch } from 'vue'
import Dialog from '../ui/dialog/Dialog.vue'
import DialogContent from '../ui/dialog/DialogContent.vue'
import DialogTitle from '../ui/dialog/DialogTitle.vue'
import Button from '../ui/button/Button.vue'
import { SlidersHorizontal, Trash2 } from 'lucide-vue-next'
import type { PhaseItem } from '../../api/client'

const props = defineProps<{
  open: boolean
  phase: PhaseItem | null
  phaseIndex: number
  totalPhases: number
  defaultPrompt?: string
}>()

const emit = defineEmits<{
  (e: 'update:open', val: boolean): void
  (e: 'save', phase: PhaseItem): void
  (e: 'delete', id: string): void
}>()

const form = ref<PhaseItem>({
  id: '',
  label: '',
  description: '',
  prompt: '',
})

watch(() => props.open, (isOpen) => {
  if (isOpen && props.phase) {
    form.value = { ...props.phase }
  }
})

function handleSave() {
  if (!form.value.label.trim()) return
  emit('save', { ...form.value, label: form.value.label.trim(), description: (form.value.description || '').trim(), prompt: (form.value.prompt || '').trim() })
  emit('update:open', false)
}

function handleDelete() {
  if (props.phase) {
    emit('delete', props.phase.id)
    emit('update:open', false)
  }
}
</script>

<template>
  <Dialog :open="open" @update:open="emit('update:open', $event)">
    <DialogContent class="sm:max-w-md p-4 bg-card border border-border rounded-md shadow-modal font-mono">
      <DialogTitle class="flex items-center gap-2 text-sm font-bold text-foreground mb-3">
        <SlidersHorizontal class="w-4 h-4 text-primary" />
        编辑阶段 · {{ form.label || '新阶段' }}
      </DialogTitle>

      <div class="space-y-3">
        <!-- Label -->
        <div class="space-y-1">
          <label class="text-[11px] font-bold text-muted-foreground uppercase tracking-wider">阶段标签</label>
          <input
            v-model="form.label"
            class="w-full px-2.5 py-1.5 rounded-sm border border-border/80 bg-background text-sm font-mono text-foreground focus:outline-none focus:ring-1 focus:ring-primary/50"
            placeholder="如：评估、开发"
            @keydown.enter="handleSave"
          />
        </div>

        <!-- Description -->
        <div class="space-y-1">
          <label class="text-[11px] font-bold text-muted-foreground uppercase tracking-wider">阶段说明</label>
          <input
            v-model="form.description"
            class="w-full px-2.5 py-1.5 rounded-sm border border-border/80 bg-background text-sm font-mono text-foreground focus:outline-none focus:ring-1 focus:ring-primary/50"
            placeholder="简要说明该阶段的目标（可选）"
            @keydown.enter="handleSave"
          />
        </div>

        <!-- Prompt -->
        <div class="space-y-1">
          <label class="text-[11px] font-bold text-muted-foreground uppercase tracking-wider">阶段提示词</label>
          <p class="text-[10px] text-muted-foreground">当该阶段激活时，自动注入到每次 MCP 响应头中，指导 AI 的行为。</p>
          <textarea
            v-model="form.prompt"
            rows="3"
            class="w-full px-2.5 py-1.5 rounded-sm border border-border/80 bg-background text-sm font-mono text-foreground focus:outline-none focus:ring-1 focus:ring-primary/50 resize-none leading-relaxed"
            placeholder="留空则使用默认提示词。输入自定义内容将覆盖默认。"
          ></textarea>
          <div
            v-if="!form.prompt?.trim() && props.defaultPrompt"
            class="px-2.5 py-2 rounded-sm border border-border/40 bg-muted/30 text-[11px] font-mono text-muted-foreground leading-relaxed select-text max-h-24 overflow-y-auto"
          >
            <span class="text-[10px] font-bold uppercase tracking-wider text-muted-foreground/70 block mb-1">默认提示词（只读）</span>
            {{ props.defaultPrompt }}
          </div>
        </div>

        <!-- Actions -->
        <div class="flex items-center justify-between pt-2 border-t border-border/50">
          <Button
            v-if="totalPhases > 1"
            variant="ghost"
            size="sm"
            class="text-destructive hover:text-destructive hover:bg-destructive/10 text-xs gap-1"
            @click="handleDelete"
          >
            <Trash2 class="w-3.5 h-3.5" />
            删除此阶段
          </Button>
          <span v-else></span>
          <div class="flex items-center gap-2">
            <Button variant="outline" size="sm" class="text-xs" @click="emit('update:open', false)">取消</Button>
            <Button size="sm" class="text-xs" :disabled="!form.label.trim()" @click="handleSave">保存</Button>
          </div>
        </div>
      </div>
    </DialogContent>
  </Dialog>
</template>
