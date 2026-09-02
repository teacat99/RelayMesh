<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useSettingsStore } from '../../stores/settings'
import { useNormsStore } from '@/stores/norms'
import type { UserNorm } from '@/api/client'
import OnlineFlowDiagram from '../OnlineFlowDiagram.vue'
import FlowPromptsSettings from '../FlowPromptsSettings.vue'
import {
  Brain,
  BookOpen,
  Plus,
  Pencil,
  Trash2,
  X,
  Check,
  ToggleLeft,
  ToggleRight,
  AlertCircle,
  ChevronDown,
  ChevronRight
} from 'lucide-vue-next'

const settingsStore = useSettingsStore()
const normsStore = useNormsStore()
const isNormsOpen = ref(false)

const isNormEditing = ref(false)
const normEditTarget = ref<string | null>(null)
const normForm = ref({ name: '', summary: '', content: '', is_active: true })
const normFormError = ref('')

onMounted(() => {
  normsStore.fetchNorms()
})

function openNormCreate() {
  normEditTarget.value = null
  normForm.value = { name: '', summary: '', content: '', is_active: true }
  normFormError.value = ''
  isNormEditing.value = true
}

function openNormEdit(norm: UserNorm) {
  normEditTarget.value = norm.name
  normForm.value = { name: norm.name, summary: norm.summary, content: norm.content, is_active: norm.is_active }
  normFormError.value = ''
  isNormEditing.value = true
}

function closeNormEditor() {
  isNormEditing.value = false
  normEditTarget.value = null
  normFormError.value = ''
}

async function handleNormSave() {
  normFormError.value = ''
  const { name, summary, content, is_active } = normForm.value
  if (!name.trim()) { normFormError.value = '名称不能为空'; return }
  if (!summary.trim()) { normFormError.value = '摘要不能为空'; return }
  if (!content.trim()) { normFormError.value = '内容不能为空'; return }
  try {
    if (normEditTarget.value) {
      await normsStore.updateNorm(normEditTarget.value, { summary, content, is_active })
    } else {
      await normsStore.createNorm({ name: name.trim(), summary, content, is_active })
    }
    closeNormEditor()
  } catch (e: any) {
    normFormError.value = e?.response?.data?.error || e.message || '保存失败'
  }
}

async function handleNormDelete(name: string) {
  if (!confirm(`确认删除规范「${name}」？此操作不可撤销。`)) return
  try {
    await normsStore.deleteNorm(name)
  } catch (e: any) {
    alert(e?.response?.data?.error || '删除失败')
  }
}

async function handleNormToggle(norm: UserNorm) {
  await normsStore.toggleActive(norm.name, !norm.is_active)
}
</script>

<template>
  <div class="space-y-4">
    <!-- 用户记忆 -->
    <div class="space-y-2 p-4 rounded-xs border border-border/80 bg-card/40 font-mono text-xs">
      <div class="flex items-center gap-2 border-b border-border/70 pb-2">
        <Brain class="w-3.5 h-3.5 text-foreground shrink-0" />
        <div>
          <span class="font-bold text-xs sm:text-sm text-foreground tracking-tight">用户记忆 (User Context)</span>
          <p class="text-[10px] text-muted-foreground font-sans mt-0.5">
            注入每次 MCP 响应的 context 头部，帮助 AI 在上下文压缩后仍保持关键信息
          </p>
        </div>
      </div>
      <textarea
        :value="settingsStore.settings.userMemory"
        class="w-full text-xs font-mono p-2.5 rounded-xs bg-card border border-border/80 focus:border-primary focus:outline-none transition-colors resize-y min-h-[60px] leading-relaxed text-foreground placeholder:text-muted-foreground/50"
        rows="3"
        placeholder="例：沟通语言使用中文，Git 规范 conventional commits，提交信息中文"
        @input="e => { settingsStore.settings.userMemory = (e.target as HTMLTextAreaElement).value; settingsStore.triggerSaveStatus() }"
      ></textarea>
    </div>

    <!-- 流程流转状态图（自管折叠） -->
    <OnlineFlowDiagram />

    <!-- 提示词系统模板配置 -->
    <FlowPromptsSettings />

    <!-- 规范库 (Skills) -->
    <div class="rounded-xs border border-border/80 bg-card/40 font-mono text-xs">
      <!-- Header (collapsible toggle) -->
      <button
        type="button"
        class="w-full flex items-center gap-2 p-4 pb-3 text-left cursor-pointer hover:bg-muted/30 transition-colors select-none"
        @click="isNormsOpen = !isNormsOpen"
      >
        <component :is="isNormsOpen ? ChevronDown : ChevronRight" class="w-3.5 h-3.5 text-muted-foreground shrink-0 transition-transform" />
        <BookOpen class="w-3.5 h-3.5 text-foreground shrink-0" />
        <div class="flex-1 min-w-0">
          <span class="font-bold text-xs sm:text-sm text-foreground tracking-tight">规范库 (Skills)</span>
          <span class="text-[10px] text-muted-foreground font-sans ml-1.5">/ External Skill Directory</span>
          <p class="text-[10px] text-muted-foreground font-sans mt-0.5">
            激活的规范摘要自动注入每次 MCP 响应 context 头部，完整内容由 AI 按需通过 <code class="text-primary/80">manage_skills</code> 工具查询
          </p>
        </div>
        <span
          v-if="!isNormEditing"
          class="shrink-0 flex items-center gap-1.5 px-2.5 py-1 rounded-sm text-xs font-mono bg-primary text-primary-foreground hover:bg-primary/90 transition-colors"
          @click.stop="isNormsOpen = true; openNormCreate()"
        >
          <Plus class="w-3 h-3" />
          <span>新建</span>
        </span>
      </button>

      <div v-show="isNormsOpen" class="space-y-3 px-4 pb-4 border-t border-border/70 pt-3">
        <!-- Editor -->
        <div v-if="isNormEditing" class="p-4 rounded-xs border border-primary/40 bg-card/80 space-y-3">
          <div class="flex items-center justify-between border-b border-border/70 pb-2">
            <span class="font-mono font-bold text-xs text-foreground">{{ normEditTarget ? '编辑规范' : '新建规范' }}</span>
            <button type="button" class="p-1 rounded-sm hover:bg-muted cursor-pointer" @click="closeNormEditor">
              <X class="w-3.5 h-3.5 text-muted-foreground" />
            </button>
          </div>
          <div v-if="normFormError" class="flex items-center gap-1.5 text-xs text-destructive font-mono">
            <AlertCircle class="w-3 h-3 shrink-0" />
            {{ normFormError }}
          </div>
          <div class="grid gap-2.5">
            <div>
              <label class="block text-[10px] text-muted-foreground font-mono mb-1">名称 (kebab-case)</label>
              <input v-model="normForm.name" :disabled="!!normEditTarget" type="text"
                class="w-full text-xs font-mono p-2 rounded-xs bg-card border border-border/80 focus:border-primary focus:outline-none transition-colors text-foreground placeholder:text-muted-foreground/50 disabled:opacity-50"
                placeholder="git-convention" />
            </div>
            <div>
              <label class="block text-[10px] text-muted-foreground font-mono mb-1">摘要 <span class="text-muted-foreground/60">(≤500 字，注入 context 头)</span></label>
              <input v-model="normForm.summary" type="text"
                class="w-full text-xs font-mono p-2 rounded-xs bg-card border border-border/80 focus:border-primary focus:outline-none transition-colors text-foreground placeholder:text-muted-foreground/50"
                placeholder="Git 提交规范：中文 conventional commits，每 200-500 行改动即 commit" maxlength="500" />
            </div>
            <div>
              <label class="block text-[10px] text-muted-foreground font-mono mb-1">完整内容 <span class="text-muted-foreground/60">(Markdown，≤20000 字)</span></label>
              <textarea v-model="normForm.content"
                class="w-full text-xs font-mono p-2.5 rounded-xs bg-card border border-border/80 focus:border-primary focus:outline-none transition-colors resize-y min-h-[120px] leading-relaxed text-foreground placeholder:text-muted-foreground/50"
                rows="6" placeholder="# Git 提交规范&#10;&#10;## 格式&#10;..." maxlength="20000"></textarea>
            </div>
            <div class="flex items-center gap-2">
              <button type="button" class="p-0.5 rounded-sm cursor-pointer" @click="normForm.is_active = !normForm.is_active">
                <component :is="normForm.is_active ? ToggleRight : ToggleLeft" class="w-5 h-5 transition-colors" :class="normForm.is_active ? 'text-primary' : 'text-muted-foreground'" />
              </button>
              <span class="text-xs font-mono" :class="normForm.is_active ? 'text-foreground' : 'text-muted-foreground'">
                {{ normForm.is_active ? '激活（摘要注入 context）' : '未激活' }}
              </span>
            </div>
          </div>
          <div class="flex items-center justify-end gap-2 pt-2 border-t border-border/70">
            <button type="button" class="px-3 py-1.5 rounded-sm text-xs font-mono text-muted-foreground hover:text-foreground hover:bg-muted transition-colors cursor-pointer" @click="closeNormEditor">取消</button>
            <button type="button" class="flex items-center gap-1.5 px-3 py-1.5 rounded-sm text-xs font-mono bg-primary text-primary-foreground hover:bg-primary/90 transition-colors cursor-pointer" @click="handleNormSave">
              <Check class="w-3 h-3" />
              <span>保存</span>
            </button>
          </div>
        </div>

        <!-- List -->
        <div v-if="normsStore.norms.length === 0 && !isNormEditing" class="p-6 text-center text-xs text-muted-foreground font-mono border border-dashed border-border/60 rounded-xs">
          暂无规范，点击「新建」添加第一个
        </div>
        <div v-else class="space-y-2">
          <div v-for="norm in normsStore.norms" :key="norm.name"
            class="flex items-start gap-3 p-3 rounded-xs border border-border/80 bg-card/40 hover:bg-card/70 transition-colors group">
            <button type="button" class="mt-0.5 p-0.5 rounded-sm cursor-pointer shrink-0" :title="norm.is_active ? '点击停用' : '点击激活'" @click="handleNormToggle(norm)">
              <component :is="norm.is_active ? ToggleRight : ToggleLeft" class="w-5 h-5 transition-colors" :class="norm.is_active ? 'text-primary' : 'text-muted-foreground/50'" />
            </button>
            <div class="flex-1 min-w-0">
              <div class="flex items-center gap-2">
                <span class="font-mono font-bold text-xs text-foreground">{{ norm.name }}</span>
                <span v-if="norm.is_active" class="text-[9px] px-1.5 py-0.5 rounded-full bg-primary/15 text-primary font-mono font-medium">active</span>
              </div>
              <p class="text-[11px] text-muted-foreground font-sans mt-0.5 line-clamp-2">{{ norm.summary }}</p>
            </div>
            <div class="flex items-center gap-1 opacity-0 group-hover:opacity-100 transition-opacity shrink-0">
              <button type="button" class="p-1.5 rounded-sm hover:bg-muted cursor-pointer" title="编辑" @click="openNormEdit(norm)">
                <Pencil class="w-3 h-3 text-muted-foreground" />
              </button>
              <button type="button" class="p-1.5 rounded-sm hover:bg-destructive/10 cursor-pointer" title="删除" @click="handleNormDelete(norm.name)">
                <Trash2 class="w-3 h-3 text-muted-foreground hover:text-destructive" />
              </button>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>
