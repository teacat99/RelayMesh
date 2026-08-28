<script setup lang="ts">
import { ref, computed } from 'vue'
import { useSettingsStore, type QuickPresetItem, normalizePresetItem } from '@/stores/settings'
import {
  MessageSquareQuote,
  Plus,
  Trash2,
  RotateCcw,
  Sparkles,
  Edit2,
  CheckSquare,
  Tag,
  Square
} from 'lucide-vue-next'
import QuickPresetEditDialog from '../feedback/QuickPresetEditDialog.vue'
import Badge from '../ui/badge/Badge.vue'

const settingsStore = useSettingsStore()
const activeStatusTab = ref<'online' | 'away' | 'autopilot'>('online')

// Dialog states
const isDialogOpen = ref(false)
const editingPreset = ref<QuickPresetItem | null>(null)
const isNewPreset = ref(false)
const dialogTargetType = ref<'status' | 'general'>('status')

const currentStatusPresets = computed(() => {
  return settingsStore.getNormalizedStatusPresets(activeStatusTab.value)
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
    settingsStore.saveStatusPresetItem(activeStatusTab.value, item)
  } else {
    settingsStore.saveQuickPresetItem(item)
  }
}

function handleDeletePreset(id: string) {
  if (dialogTargetType.value === 'status') {
    settingsStore.removeStatusPresetItem(activeStatusTab.value, id)
  } else {
    settingsStore.removeQuickPresetItem(id)
  }
}

function handleRemoveStatusPreset(id: string) {
  settingsStore.removeStatusPresetItem(activeStatusTab.value, id)
}

function handleRemoveGeneralPreset(id: string) {
  settingsStore.removeQuickPresetItem(id)
}

function resetCurrentStatusPresets() {
  settingsStore.resetStatusPresets(activeStatusTab.value)
}

function resetGeneralPresets() {
  settingsStore.updateSettings({
    quickPresets: [
      { id: 'qp-1', title: '按计划推进', content: '按计划推进', allowAppend: false, appendTitle: '按计划推进', isAppendActive: false },
      { id: 'qp-2', title: '同意方案，请继续', content: '同意方案，请继续', allowAppend: false, appendTitle: '同意方案', isAppendActive: false },
      { id: 'qp-3', title: '实施规范', content: '应先阐述对当前问题的理解，不修改代码，汇报后等待二次确认。', allowAppend: true, appendTitle: '实施规范', isAppendActive: false },
      { id: 'qp-4', title: '已核对无误', content: '已核对无误', allowAppend: false, appendTitle: '核对确认', isAppendActive: false },
      { id: 'qp-5', title: '需要调整方案', content: '需要调整方案，请暂停后续改动，先提供备选设计。', allowAppend: false, appendTitle: '方案调整', isAppendActive: false }
    ]
  })
}
</script>

<template>
  <div class="space-y-4">
    <!-- 1. 状态专属预设 -->
    <div class="space-y-3">
      <div class="flex items-center justify-between pb-1.5 border-b border-border/70">
        <div class="flex items-center gap-1.5">
          <MessageSquareQuote class="w-3.5 h-3.5 text-primary" />
          <span class="text-xs font-bold font-mono text-foreground">状态专属预设指令与快捷回复</span>
        </div>
        <div class="flex items-center gap-2">
          <button
            type="button"
            class="text-[10px] font-mono text-primary hover:underline flex items-center gap-1 cursor-pointer transition-colors"
            @click="openCreateDialog('status')"
          >
            <Plus class="w-3 h-3" />
            <span>新建状态预设</span>
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

      <!-- 状态小Tab -->
      <div class="flex items-center gap-1 border-b border-border/60 pb-1">
        <button
          type="button"
          class="px-2.5 py-1 rounded-xs text-xs font-mono transition-all cursor-pointer border"
          :class="activeStatusTab === 'online'
            ? 'border-emerald-500/40 bg-emerald-500/10 text-foreground font-bold shadow-2xs'
            : 'border-transparent text-muted-foreground hover:bg-muted'"
          @click="activeStatusTab = 'online'"
        >
          🟢 在线模式预设
        </button>
        <button
          type="button"
          class="px-2.5 py-1 rounded-xs text-xs font-mono transition-all cursor-pointer border"
          :class="activeStatusTab === 'away'
            ? 'border-amber-500/40 bg-amber-500/10 text-foreground font-bold shadow-2xs'
            : 'border-transparent text-muted-foreground hover:bg-muted'"
          @click="activeStatusTab = 'away'"
        >
          🟡 暂离模式预设
        </button>
        <button
          type="button"
          class="px-2.5 py-1 rounded-xs text-xs font-mono transition-all cursor-pointer border"
          :class="activeStatusTab === 'autopilot'
            ? 'border-indigo-500/40 bg-indigo-500/10 text-foreground font-bold shadow-2xs'
            : 'border-transparent text-muted-foreground hover:bg-muted'"
          @click="activeStatusTab = 'autopilot'"
        >
          🟣 托管模式预设
        </button>
      </div>

      <!-- 状态标签列表 -->
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

    <!-- Quick Preset Edit Dialog -->
    <QuickPresetEditDialog
      v-model:open="isDialogOpen"
      :preset-item="editingPreset"
      :is-new="isNewPreset"
      :status-context="activeStatusTab"
      :available-presets="dialogTargetType === 'status' ? currentStatusPresets : currentGeneralPresets"
      @save="handleSavePreset"
      @delete="handleDeletePreset"
    />
  </div>
</template>
