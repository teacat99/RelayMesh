<script setup lang="ts">
import { ref, watch, computed } from 'vue'
import Dialog from '../ui/dialog/Dialog.vue'
import DialogContent from '../ui/dialog/DialogContent.vue'
import DialogTitle from '../ui/dialog/DialogTitle.vue'
import Button from '../ui/button/Button.vue'
import Badge from '../ui/badge/Badge.vue'
import {
  MessageSquareQuote,
  Sparkles,
  Trash2,
  CheckSquare,
  Square,
  HelpCircle,
  FileCheck,
  Tag,
  SlidersHorizontal
} from 'lucide-vue-next'
import type { QuickPresetItem } from '../../stores/settings'

const props = defineProps<{
  open: boolean
  presetItem: QuickPresetItem | null
  isNew?: boolean
  statusContext?: 'online' | 'away' | 'autopilot'
  availablePresets?: QuickPresetItem[]
}>()

const emit = defineEmits<{
  (e: 'update:open', val: boolean): void
  (e: 'save', item: QuickPresetItem): void
  (e: 'delete', id: string): void
}>()

const form = ref<QuickPresetItem>({
  id: '',
  title: '',
  content: '',
  allowAppend: false,
  appendTitle: '',
  isAppendActive: false,
  isMutex: false,
  mutexTargets: []
})

const selectableMutexPresets = computed(() => {
  return (props.availablePresets || []).filter(p => p.id !== form.value.id)
})

watch(() => props.open, (isOpen) => {
  if (isOpen) {
    if (props.presetItem) {
      form.value = {
        id: props.presetItem.id || `qp-${Date.now()}`,
        title: props.presetItem.title || '',
        content: props.presetItem.content || props.presetItem.title || '',
        allowAppend: !!props.presetItem.allowAppend,
        appendTitle: props.presetItem.appendTitle || props.presetItem.title || '',
        isAppendActive: !!props.presetItem.isAppendActive,
        isMutex: !!props.presetItem.isMutex,
        mutexTargets: Array.isArray(props.presetItem.mutexTargets) ? [...props.presetItem.mutexTargets] : []
      }
    } else {
      form.value = {
        id: `qp-${Date.now()}`,
        title: '',
        content: '',
        allowAppend: false,
        appendTitle: '',
        isAppendActive: false,
        isMutex: false,
        mutexTargets: []
      }
    }
  }
}, { immediate: true })

watch(() => form.value.title, (newTitle) => {
  if (!form.value.appendTitle || form.value.appendTitle.trim() === '') {
    form.value.appendTitle = newTitle
  }
})

const isFormValid = computed(() => {
  return form.value.title.trim() !== '' && form.value.content.trim() !== ''
})

function toggleMutexTarget(targetId: string) {
  if (!form.value.mutexTargets) {
    form.value.mutexTargets = []
  }
  const idx = form.value.mutexTargets.indexOf(targetId)
  if (idx >= 0) {
    form.value.mutexTargets.splice(idx, 1)
  } else {
    form.value.mutexTargets.push(targetId)
  }
}

function selectAllMutexTargets() {
  form.value.mutexTargets = selectableMutexPresets.value.map(p => p.id)
}

function clearAllMutexTargets() {
  form.value.mutexTargets = []
}

function handleSave() {
  if (!isFormValid.value) return
  const finalItem: QuickPresetItem = {
    id: form.value.id || `qp-${Date.now()}`,
    title: form.value.title.trim(),
    content: form.value.content.trim(),
    allowAppend: !!form.value.allowAppend,
    appendTitle: (form.value.appendTitle && form.value.appendTitle.trim()) ? form.value.appendTitle.trim() : form.value.title.trim(),
    isAppendActive: form.value.allowAppend ? !!form.value.isAppendActive : false,
    isMutex: form.value.allowAppend ? !!form.value.isMutex : false,
    mutexTargets: form.value.isMutex && Array.isArray(form.value.mutexTargets) ? [...form.value.mutexTargets] : []
  }
  emit('save', finalItem)
  emit('update:open', false)
}

function handleDelete() {
  if (form.value.id) {
    emit('delete', form.value.id)
    emit('update:open', false)
  }
}
</script>

<template>
  <Dialog :open="props.open" @update:open="(v) => emit('update:open', v)">
    <DialogContent class="sm:max-w-lg w-[94vw] flex flex-col p-0 gap-0 overflow-hidden shadow-2xl rounded-md border-border/80 bg-card select-none">
      <!-- Header -->
      <div class="px-5 h-11 border-b border-border/70 flex flex-row items-center justify-between shrink-0 bg-card/90 backdrop-blur-xs pr-12">
        <DialogTitle class="flex items-center gap-2 text-xs sm:text-sm font-bold font-mono tracking-tight text-foreground text-left">
          <div class="p-1 rounded-xs bg-primary/10 text-primary border border-primary/20 shrink-0">
            <MessageSquareQuote class="w-3.5 h-3.5" />
          </div>
          <span>{{ props.isNew ? '新建快捷回复预设' : '编辑快捷回复预设' }}</span>
        </DialogTitle>
      </div>

      <!-- Form Body -->
      <div class="p-5 space-y-4 text-xs font-mono max-h-[75vh] overflow-y-auto">
        <!-- 1. Title Input -->
        <div class="space-y-1.5">
          <label class="font-bold text-foreground flex items-center justify-between">
            <span class="flex items-center gap-1">
              <Tag class="w-3.5 h-3.5 text-primary" />
              <span>展示标题 (Label)</span>
            </span>
            <span class="text-[10px] text-muted-foreground font-normal">悬浮栏/预设按钮名称</span>
          </label>
          <input
            v-model="form.title"
            type="text"
            placeholder="例如：实施规范 / 同意方案 / 核对无误"
            class="w-full px-3 py-1.5 bg-background border border-border/80 rounded-xs text-xs text-foreground focus:outline-none focus:ring-1 focus:ring-primary/40 placeholder:text-muted-foreground/60"
          />
        </div>

        <!-- 2. Content Input -->
        <div class="space-y-1.5">
          <label class="font-bold text-foreground flex items-center justify-between">
            <span class="flex items-center gap-1">
              <FileCheck class="w-3.5 h-3.5 text-primary" />
              <span>实际内容 (Content)</span>
            </span>
            <span class="text-[10px] text-muted-foreground font-normal">快速填充或自动追加文本</span>
          </label>
          <textarea
            v-model="form.content"
            rows="3"
            placeholder="例如：应先阐述对当前问题的理解，不修改代码，汇报后等待二次确认。"
            class="w-full px-3 py-2 bg-background border border-border/80 rounded-xs text-xs text-foreground focus:outline-none focus:ring-1 focus:ring-primary/40 placeholder:text-muted-foreground/60 leading-relaxed resize-y min-h-[60px]"
          ></textarea>
        </div>

        <!-- 分割线 -->
        <div class="h-px bg-border/70 my-3"></div>

        <!-- 3. Allow Append Option (与上半部分样式完全统一) -->
        <div class="space-y-3.5">
          <!-- Toggle Checkbox Line -->
          <div
            class="flex items-center justify-between cursor-pointer select-none py-1 group"
            @click="form.allowAppend = !form.allowAppend"
          >
            <div class="flex items-center gap-2">
              <Sparkles class="w-3.5 h-3.5 text-primary" />
              <span class="font-bold text-foreground">允许作为回复末尾追加规则 (Auto Append Rule)</span>
            </div>
            <div class="flex items-center gap-2">
              <span class="text-[10px] text-muted-foreground hidden sm:inline">在快捷按钮上显示追加勾选框</span>
              <CheckSquare v-if="form.allowAppend" class="w-4 h-4 text-primary fill-primary/20" />
              <Square v-else class="w-4 h-4 text-muted-foreground group-hover:text-foreground" />
            </div>
          </div>

          <!-- Append Configuration Fields (When allowAppend is true) -->
          <div v-if="form.allowAppend" class="space-y-3.5 animate-in fade-in-50 duration-200">
            <!-- Append Title Input -->
            <div class="space-y-1.5">
              <label class="font-bold text-foreground flex items-center justify-between">
                <span class="flex items-center gap-1">
                  <Tag class="w-3.5 h-3.5 text-primary" />
                  <span>追加段落标题 (Append Section Title)</span>
                </span>
                <span class="text-[10px] text-muted-foreground font-normal">附加在文本末尾时的段落标题</span>
              </label>
              <input
                v-model="form.appendTitle"
                type="text"
                placeholder="例如：实施规范"
                class="w-full px-3 py-1.5 bg-background border border-border/80 rounded-xs text-xs text-foreground focus:outline-none focus:ring-1 focus:ring-primary/40 placeholder:text-muted-foreground/60"
              />
            </div>

            <!-- Preview box -->
            <div class="space-y-1.5">
              <label class="font-bold text-foreground flex items-center justify-between">
                <span class="flex items-center gap-1">
                  <FileCheck class="w-3.5 h-3.5 text-primary" />
                  <span>回复自动附加预览</span>
                </span>
                <span class="text-[10px] text-muted-foreground font-normal">格式为「【标题】\n内容」</span>
              </label>
              <div class="p-2.5 rounded-xs bg-muted/40 border border-border/80 text-foreground whitespace-pre-wrap leading-relaxed text-xs">
                <span class="text-muted-foreground italic">&lt;用户输入的正常回复...&gt;</span>

【{{ form.appendTitle || form.title || '追加标题' }}】
{{ form.content || '追加内容...' }}
              </div>
            </div>

            <!-- 互斥追加配置 (支持勾选开启互斥并多选互斥目标项) -->
            <div class="space-y-2.5 pt-2 border-t border-border/50">
              <div
                class="flex items-center justify-between cursor-pointer select-none py-1 group"
                @click="form.isMutex = !form.isMutex"
              >
                <div class="flex items-center gap-2">
                  <SlidersHorizontal class="w-3.5 h-3.5 text-primary" />
                  <span class="font-bold text-foreground">开启互斥追加 (Mutual Exclusion)</span>
                </div>
                <div class="flex items-center gap-2">
                  <span class="text-[10px] text-muted-foreground hidden sm:inline">激活此项时自动关闭互斥预设</span>
                  <CheckSquare v-if="form.isMutex" class="w-4 h-4 text-primary fill-primary/20" />
                  <Square v-else class="w-4 h-4 text-muted-foreground group-hover:text-foreground" />
                </div>
              </div>

              <!-- 当开启互斥时，显示多选列表 -->
              <div v-if="form.isMutex" class="space-y-2 pl-0.5 animate-in fade-in-50 duration-200">
                <div class="flex items-center justify-between text-[10px] text-muted-foreground">
                  <span>勾选与此预设互斥的选项（激活当前项时自动取消勾选下列项）：</span>
                  <div class="flex items-center gap-2" v-if="selectableMutexPresets.length > 0">
                    <button
                      type="button"
                      class="text-primary hover:underline cursor-pointer"
                      @click.stop="selectAllMutexTargets"
                    >
                      全选
                    </button>
                    <span>·</span>
                    <button
                      type="button"
                      class="text-muted-foreground hover:text-foreground hover:underline cursor-pointer"
                      @click.stop="clearAllMutexTargets"
                    >
                      清空
                    </button>
                  </div>
                </div>

                <div v-if="selectableMutexPresets.length > 0" class="grid grid-cols-1 sm:grid-cols-2 gap-1.5 pt-0.5">
                  <div
                    v-for="target in selectableMutexPresets"
                    :key="target.id"
                    class="flex items-center gap-2 p-2 rounded-xs border text-xs cursor-pointer select-none transition-colors"
                    :class="form.mutexTargets?.includes(target.id)
                      ? 'border-primary/50 bg-primary/10 text-primary font-medium shadow-2xs'
                      : 'border-border/70 bg-card/60 hover:bg-muted text-foreground'"
                    @click.stop="toggleMutexTarget(target.id)"
                  >
                    <CheckSquare v-if="form.mutexTargets?.includes(target.id)" class="w-3.5 h-3.5 text-primary fill-primary/20 shrink-0" />
                    <Square v-else class="w-3.5 h-3.5 text-muted-foreground shrink-0" />
                    <span class="truncate">{{ target.title }}</span>
                    <span v-if="target.allowAppend" class="text-[9px] text-muted-foreground ml-auto shrink-0">可追加</span>
                  </div>
                </div>
                <div v-else class="p-2 rounded-xs bg-muted/20 border border-border/50 text-[10px] text-muted-foreground italic text-center">
                  当前状态分组下暂无其他快捷预设
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>

      <!-- Footer Actions -->
      <div class="px-5 py-3 border-t border-border/70 flex items-center justify-between bg-card/90">
        <div>
          <button
            v-if="!props.isNew"
            type="button"
            class="px-2.5 py-1.5 rounded-xs text-xs font-mono text-destructive hover:bg-destructive/10 border border-destructive/30 transition-colors flex items-center gap-1 cursor-pointer"
            @click="handleDelete"
          >
            <Trash2 class="w-3.5 h-3.5" />
            <span>删除预设</span>
          </button>
        </div>

        <div class="flex items-center gap-2">
          <Button
            variant="ghost"
            size="sm"
            class="text-xs font-mono"
            @click="emit('update:open', false)"
          >
            取消
          </Button>
          <Button
            variant="default"
            size="sm"
            class="text-xs font-mono"
            :disabled="!isFormValid"
            @click="handleSave"
          >
            保存预设
          </Button>
        </div>
      </div>
    </DialogContent>
  </Dialog>
</template>
