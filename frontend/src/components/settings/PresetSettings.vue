<script setup lang="ts">
import { ref, computed } from 'vue'
import { useSettingsStore, type QuickPresetItem } from '@/stores/settings'
import {
  MessageSquareQuote,
  Plus,
  Trash2,
  RotateCcw,
  Sparkles,
  Edit2,
  CheckSquare,
  Tag,
  Square,
  SlidersHorizontal,
  GripVertical,
  ArrowUp,
  ArrowDown
} from 'lucide-vue-next'
import QuickPresetEditDialog from '../feedback/QuickPresetEditDialog.vue'
import PresetVersionArchive from './PresetVersionArchive.vue'
import Badge from '../ui/badge/Badge.vue'

const settingsStore = useSettingsStore()
const STATUS_KEY = 'online' as const

// Dialog states
const isDialogOpen = ref(false)
const editingPreset = ref<QuickPresetItem | null>(null)
const isNewPreset = ref(false)
const dialogTargetType = ref<'status' | 'general'>('status')

const currentStatusPresets = computed(() => {
  return settingsStore.getNormalizedStatusPresets(STATUS_KEY)
})

const currentGeneralPresets = computed(() => {
  return settingsStore.getNormalizedQuickPresets()
})

function openCreateDialog(target: 'status' | 'general') {
  dialogTargetType.value = target
  editingPreset.value = null
  isNewPreset.value = true
  isDialogOpen.value = true
}

function openEditDialog(target: 'status' | 'general', item: QuickPresetItem) {
  dialogTargetType.value = target
  editingPreset.value = { ...item }
  isNewPreset.value = false
  isDialogOpen.value = true
}

function handleSavePreset(item: QuickPresetItem) {
  if (dialogTargetType.value === 'status') {
    settingsStore.saveStatusPresetItem(STATUS_KEY, item)
  } else {
    settingsStore.saveQuickPresetItem(item)
  }
}

function handleDeletePreset(id: string) {
  if (dialogTargetType.value === 'status') {
    settingsStore.removeStatusPresetItem(STATUS_KEY, id)
  } else {
    settingsStore.removeQuickPresetItem(id)
  }
}

function handleRemoveStatusPreset(id: string) {
  settingsStore.removeStatusPresetItem(STATUS_KEY, id)
}

function handleRemoveGeneralPreset(id: string) {
  settingsStore.removeQuickPresetItem(id)
}

function resetCurrentStatusPresets() {
  settingsStore.resetStatusPresets(STATUS_KEY)
}

// 恢复默认：换成出厂内容；会被换掉的自定义内容（改过的、自建的）先存进「版本留存」，不会丢
function resetGeneralPresets() {
  settingsStore.resetGeneralPresets()
}

// Phase Template
const phaseEditingIdx = ref<number | null>(null)
const phaseEditLabel = ref('')
const phaseEditDesc = ref('')
const phaseEditPrompt = ref('')

const currentPhaseTemplate = computed(() => {
  return settingsStore.settings.phaseTemplate || []
})

function addPhaseItem() {
  const items = [...currentPhaseTemplate.value]
  const newId = `phase-${Date.now()}`
  items.push({ id: newId, label: '新阶段', description: '', prompt: '' })
  settingsStore.updateSettings({ phaseTemplate: items })
  phaseEditingIdx.value = items.length - 1
  phaseEditLabel.value = '新阶段'
  phaseEditDesc.value = ''
  phaseEditPrompt.value = ''
}

function removePhaseItem(idx: number) {
  const items = [...currentPhaseTemplate.value]
  items.splice(idx, 1)
  settingsStore.updateSettings({ phaseTemplate: items })
  if (phaseEditingIdx.value === idx) phaseEditingIdx.value = null
}

function startEditPhase(idx: number) {
  const item = currentPhaseTemplate.value[idx]
  if (!item) return
  phaseEditingIdx.value = idx
  phaseEditLabel.value = item.label
  phaseEditDesc.value = item.description || ''
  phaseEditPrompt.value = item.prompt || ''
}

function savePhaseEdit() {
  if (phaseEditingIdx.value === null) return
  const items = [...currentPhaseTemplate.value]
  const idx = phaseEditingIdx.value
  if (items[idx]) {
    items[idx] = { ...items[idx], label: phaseEditLabel.value.trim() || items[idx].label, description: phaseEditDesc.value.trim(), prompt: phaseEditPrompt.value.trim() }
    settingsStore.updateSettings({ phaseTemplate: items })
  }
  phaseEditingIdx.value = null
}

function cancelPhaseEdit() {
  phaseEditingIdx.value = null
}

function movePhaseItem(idx: number, dir: -1 | 1) {
  const items = [...currentPhaseTemplate.value]
  const targetIdx = idx + dir
  if (targetIdx < 0 || targetIdx >= items.length) return
  ;[items[idx], items[targetIdx]] = [items[targetIdx], items[idx]]
  settingsStore.updateSettings({ phaseTemplate: items })
  if (phaseEditingIdx.value === idx) phaseEditingIdx.value = targetIdx
}

function resetPhaseTemplate() {
  settingsStore.resetPhaseTemplate()
  phaseEditingIdx.value = null
}
</script>

<template>
  <div class="space-y-4">
    <!-- 出厂预设更新了、而你改过它们：待选择（稍后之后的入口） -->
    <div
      v-if="settingsStore.presetConflicts.length > 0"
      class="p-2.5 rounded-xs border border-amber-500/40 bg-amber-500/10 flex items-center justify-between gap-3"
      data-testid="preset-conflict-banner"
    >
      <span class="text-xs leading-relaxed">有 {{ settingsStore.presetConflicts.length }} 条出厂预设更新了，而你改过它们，等你选择怎么处理（均保留 / 使用最新 / 使用旧版）。</span>
      <button
        type="button"
        class="shrink-0 text-xs font-mono px-2.5 py-1 rounded-xs border border-amber-500/50 hover:bg-amber-500/15 cursor-pointer transition-colors"
        data-testid="preset-conflict-open"
        @click="settingsStore.reopenPresetConflicts()"
      >
        逐条处理
      </button>
    </div>

    <!-- 0. 阶段滑块模板 -->
    <div class="space-y-3">
      <div class="flex items-center justify-between pb-1.5 border-b border-border/70">
        <div class="flex items-center gap-1.5">
          <SlidersHorizontal class="w-3.5 h-3.5 text-primary" />
          <span class="text-xs font-bold font-mono text-foreground">工作流阶段滑块模板</span>
        </div>
        <div class="flex items-center gap-2">
          <button
            type="button"
            class="text-[10px] font-mono text-primary hover:underline flex items-center gap-1 cursor-pointer transition-colors"
            @click="addPhaseItem"
          >
            <Plus class="w-3 h-3" />
            <span>新增阶段</span>
          </button>
          <button
            type="button"
            class="text-[10px] font-mono text-muted-foreground hover:text-foreground underline underline-offset-2 flex items-center gap-1 cursor-pointer transition-colors"
            @click="resetPhaseTemplate"
          >
            <RotateCcw class="w-2.5 h-2.5" />
            <span>恢复默认</span>
          </button>
        </div>
      </div>

      <div class="p-3 rounded-xs border border-border/70 bg-card/60 space-y-2.5">
        <div class="flex flex-wrap gap-2">
          <div
            v-for="(item, idx) in currentPhaseTemplate"
            :key="item.id"
            class="flex items-center gap-1 px-2.5 py-1 rounded-xs border text-xs font-mono group transition-all"
            :class="phaseEditingIdx === idx
              ? 'bg-primary/10 border-primary/40 text-foreground'
              : 'bg-background border-border/80 text-foreground hover:border-border'"
          >
            <!-- Inline editing mode -->
            <template v-if="phaseEditingIdx === idx">
              <input
                v-model="phaseEditLabel"
                class="w-14 bg-transparent border-b border-primary/50 text-xs font-mono font-bold outline-none px-0.5 py-0"
                placeholder="标签"
                @keydown.enter="savePhaseEdit"
                @keydown.escape="cancelPhaseEdit"
              />
              <input
                v-model="phaseEditDesc"
                class="w-28 bg-transparent border-b border-border/50 text-[10px] font-mono text-muted-foreground outline-none px-0.5 py-0"
                placeholder="说明（可选）"
                @keydown.enter="savePhaseEdit"
                @keydown.escape="cancelPhaseEdit"
              />
              <button
                type="button"
                class="text-primary hover:text-primary/80 cursor-pointer p-0.5"
                title="保存"
                @click="savePhaseEdit"
              >
                <CheckSquare class="w-3 h-3" />
              </button>
            </template>
            <!-- Display mode -->
            <template v-else>
              <span class="font-bold">{{ item.label }}</span>
              <span v-if="item.description" class="text-[10px] text-muted-foreground hidden sm:inline">{{ item.description }}</span>
              <span v-if="item.prompt" class="text-[9px] px-1 py-0 rounded-2xs border border-primary/30 text-primary font-normal">提示词</span>
              <div class="flex items-center gap-0.5 ml-0.5 opacity-50 group-hover:opacity-100 transition-opacity">
                <button v-if="idx > 0" type="button" class="text-muted-foreground hover:text-foreground cursor-pointer p-0.5" title="上移" @click="movePhaseItem(idx, -1)">
                  <ArrowUp class="w-2.5 h-2.5" />
                </button>
                <button v-if="idx < currentPhaseTemplate.length - 1" type="button" class="text-muted-foreground hover:text-foreground cursor-pointer p-0.5" title="下移" @click="movePhaseItem(idx, 1)">
                  <ArrowDown class="w-2.5 h-2.5" />
                </button>
                <button type="button" class="text-muted-foreground hover:text-primary cursor-pointer p-0.5" title="编辑" @click="startEditPhase(idx)">
                  <Edit2 class="w-3 h-3" />
                </button>
                <button type="button" class="text-muted-foreground hover:text-destructive cursor-pointer p-0.5" title="删除" @click="removePhaseItem(idx)">
                  <Trash2 class="w-3 h-3" />
                </button>
              </div>
            </template>
          </div>
        </div>

        <div class="text-[11px] text-muted-foreground pt-1">
          新建工作流将使用此模板作为默认阶段；每个工作流可独立自定义阶段列表。
        </div>
      </div>
    </div>

    <!-- 1. 快捷回复预设（输入区上方的标签） -->
    <div class="space-y-3">
      <div class="flex items-center justify-between pb-1.5 border-b border-border/70">
        <div class="flex items-center gap-1.5">
          <MessageSquareQuote class="w-3.5 h-3.5 text-primary" />
          <span class="text-xs font-bold font-mono text-foreground">快捷回复预设（输入区上方的标签）</span>
        </div>
        <div class="flex items-center gap-2">
          <button
            type="button"
            class="text-[10px] font-mono text-primary hover:underline flex items-center gap-1 cursor-pointer transition-colors"
            @click="openCreateDialog('status')"
          >
            <Plus class="w-3 h-3" />
            <span>新建快捷回复</span>
          </button>
          <button
            type="button"
            class="text-[10px] font-mono text-muted-foreground hover:text-foreground underline underline-offset-2 flex items-center gap-1 cursor-pointer transition-colors"
            @click="resetCurrentStatusPresets"
          >
            <RotateCcw class="w-2.5 h-2.5" />
            <span>恢复默认</span>
          </button>
        </div>
      </div>

      <!-- 快捷回复标签列表 -->
      <div class="p-3 rounded-xs border border-border/70 bg-card/60 space-y-2.5">
        <div class="flex flex-wrap gap-2">
          <div
            v-for="item in currentStatusPresets"
            :key="item.id"
            class="flex items-center gap-1.5 px-2.5 py-1 rounded-xs bg-background border border-border/80 text-xs font-mono text-foreground group transition-all hover:border-border"
          >
            <Tag class="w-3 h-3 text-muted-foreground group-hover:text-primary transition-colors" />
            <span class="font-medium">{{ item.title }}</span>

                   <!-- Allow append badge -->
                   <Badge v-if="item.allowAppend" variant="outline" class="text-[9px] px-1 py-0 rounded-2xs text-primary border-primary/30 font-normal">
                     可追加
                   </Badge>

                   <Badge v-if="item.allowAppend && item.isMutex" variant="outline" class="text-[9px] px-1 py-0 rounded-2xs text-amber-600 dark:text-amber-400 border-amber-500/30 font-normal">
                     互斥
                   </Badge>

            <div class="flex items-center gap-1 ml-1 opacity-60 group-hover:opacity-100 transition-opacity">
              <button
                type="button"
                class="text-muted-foreground hover:text-primary transition-colors cursor-pointer p-0.5"
                title="编辑此预设规则"
                @click="openEditDialog('status', item)"
              >
                <Edit2 class="w-3 h-3" />
              </button>
              <button
                type="button"
                class="text-muted-foreground hover:text-destructive transition-colors cursor-pointer p-0.5"
                title="删除此预设"
                @click="handleRemoveStatusPreset(item.id)"
              >
                <Trash2 class="w-3 h-3" />
              </button>
            </div>
          </div>
        </div>

        <div class="text-[11px] text-muted-foreground pt-1 flex items-center justify-between">
          <span>💡 提示：点击编辑图标可修改实际回复内容并配置「允许自动追加」规则。</span>
        </div>
      </div>
    </div>

    <!-- 2. 全局通用快捷标签 -->
    <div class="space-y-3 pt-2">
      <div class="flex items-center justify-between pb-1.5 border-b border-border/70">
        <div class="flex items-center gap-1.5">
          <Sparkles class="w-3.5 h-3.5 text-primary" />
          <span class="text-xs font-bold font-mono text-foreground">全局通用快捷回复预设</span>
        </div>
        <div class="flex items-center gap-2">
          <button
            type="button"
            class="text-[10px] font-mono text-primary hover:underline flex items-center gap-1 cursor-pointer transition-colors"
            @click="openCreateDialog('general')"
          >
            <Plus class="w-3 h-3" />
            <span>新建通用预设</span>
          </button>
          <button
            type="button"
            class="text-[10px] font-mono text-muted-foreground hover:text-foreground underline underline-offset-2 flex items-center gap-1 cursor-pointer transition-colors"
            @click="resetGeneralPresets"
          >
            <RotateCcw class="w-2.5 h-2.5" />
            <span>恢复默认</span>
          </button>
        </div>
      </div>

      <div class="p-3 rounded-xs border border-border/70 bg-card/60 space-y-2.5">
        <div class="flex flex-wrap gap-2">
          <div
            v-for="item in currentGeneralPresets"
            :key="item.id"
            class="flex items-center gap-1.5 px-2.5 py-1 rounded-xs bg-background border border-border/80 text-xs font-mono text-foreground group transition-all hover:border-border"
          >
            <Tag class="w-3 h-3 text-muted-foreground group-hover:text-primary transition-colors" />
            <span class="font-medium">{{ item.title }}</span>

            <Badge v-if="item.allowAppend" variant="outline" class="text-[9px] px-1 py-0 rounded-2xs text-primary border-primary/30 font-normal">
              可追加
            </Badge>

            <Badge v-if="item.allowAppend && item.isMutex" variant="outline" class="text-[9px] px-1 py-0 rounded-2xs text-amber-600 dark:text-amber-400 border-amber-500/30 font-normal">
              互斥
            </Badge>

            <div class="flex items-center gap-1 ml-1 opacity-60 group-hover:opacity-100 transition-opacity">
              <button
                type="button"
                class="text-muted-foreground hover:text-primary transition-colors cursor-pointer p-0.5"
                title="编辑此预设规则"
                @click="openEditDialog('general', item)"
              >
                <Edit2 class="w-3 h-3" />
              </button>
              <button
                type="button"
                class="text-muted-foreground hover:text-destructive transition-colors cursor-pointer p-0.5"
                title="删除此预设"
                @click="handleRemoveGeneralPreset(item.id)"
              >
                <Trash2 class="w-3 h-3" />
              </button>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- 3. 版本留存：出厂预设更新 / 恢复默认时被换下来的旧版、没启用的新版 -->
    <PresetVersionArchive />

    <!-- Quick Preset Edit Dialog -->
    <QuickPresetEditDialog
      v-model:open="isDialogOpen"
      :preset-item="editingPreset"
      :is-new="isNewPreset"
      :status-context="STATUS_KEY"
      :available-presets="dialogTargetType === 'status' ? currentStatusPresets : currentGeneralPresets"
      @save="handleSavePreset"
      @delete="handleDeletePreset"
    />
  </div>
</template>
