<script setup lang="ts">
import { ref, computed } from 'vue'
import { useSettingsStore, type QuickPresetItem, type PhaseTemplateItem, normalizePresetItem } from '@/stores/settings'
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
  settingsStore.updateSettings({
    phaseTemplate: [
      { id: 'assess', label: '评估', description: '需求接入与理解确认', prompt: '当前处于需求评估阶段。通过 feedback 收集用户描述，逐条记录到会话文档，保留用户原话。对每条需求复述自己的理解：真实场景、根因推测、期望行为、验收标准。等待用户确认后再进入方案阶段。不急于敲定方案选型，先听完并理解真实需求，并引导用户完善需求，汇报不同方案的利弊，对每个需求列出推荐方案、风险、改动范围与备选。方向敲定后可调整到方案阶段。⚠️ 本阶段禁止修改代码。可以读取代码验证可行性，但不得创建、修改或删除任何源代码文件。如确需修改代码，必须先通过 feedback 获得用户二次确认并切换到开发阶段。' },
      { id: 'plan', label: '方案', description: '方案设计与评审拍板', prompt: '当前处于方案设计阶段。决策确认→问题拆解→数据流→不变量→边界→影响面→备选→验收。阅读代码和文档，注意核对方案可行性，确保实施阶段的逻辑闭环；通过 feedback 与用户逐项确认，将每条决策写入会话文档「关键决策」。决策全部锁定后等待用户确认再进入开发。⚠️ 本阶段禁止修改代码。可以读取代码验证可行性，但不得创建、修改或删除任何源代码文件。如确需修改代码，必须先通过 feedback 获得用户二次确认并切换到开发阶段。' },
      { id: 'dev', label: '开发', description: '编码实施与增量验证', prompt: '当前处于开发执行阶段。改前先读相关代码与文档，沿用项目惯用模式。每完成一个逻辑单元立即增量验证（lint/type-check→build），不等全部完成再统一修。改 import/接口/类型时检查所有引用方。每 200-500 行改动即 commit。如发现方案和代码冲突，先记录并尝试解决，解决不了则向用户汇报，并回退到方案阶段。开发完成后进入验证阶段。' },
      { id: 'verify', label: '验证', description: '三件套通过与功能验证', prompt: '当前处于部署验证阶段。执行三件套：lint/type-check→build→功能验证。部署、DB迁移、push main 等不可逆操作必须二次确认。完成标准 = 功能 + 类型 + 编译 + 校验 + 文档同步 + 配置同步 + 开发记录，每条须有可验证证据。验证失败回退到开发阶段修复，验证成功进入完成阶段，使用 feedback 汇报。' },
      { id: 'done', label: '完成', description: '汇报完成与等待下一步', prompt: '当前阶段的开发任务完成。通过 feedback 提交最终汇报：修改内容、原因、影响范围、验证结果与后续建议。盘点后台进程和未提交变更，归档会话文档。等待用户确认下一步需求或结束会话。' },
    ]
  })
  phaseEditingIdx.value = null
}
</script>

<template>
  <div class="space-y-4">
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
