<script setup lang="ts">
import { ref } from 'vue'
import { useSettingsStore } from '@/stores/settings'
import {
  Database,
  Download,
  Trash2,
  Sparkles,
  Info
} from 'lucide-vue-next'
import { toast } from 'vue-sonner'

const settingsStore = useSettingsStore()
const isExporting = ref(false)
const isClearing = ref(false)

function handleExportSettings() {
  try {
    isExporting.value = true
    const jsonStr = JSON.stringify(settingsStore.settings, null, 2)
    const blob = new Blob([jsonStr], { type: 'application/json' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `relaymesh-settings-${new Date().toISOString().slice(0, 10)}.json`
    a.click()
    URL.revokeObjectURL(url)
    toast.success('配置已成功导出为 JSON 文件')
  } catch (err: any) {
    toast.error('导出失败: ' + err.message)
  } finally {
    isExporting.value = false
  }
}

function handleClearLocalCache() {
  if (confirm('确定要清空本地所有会话草稿与临时缓存吗？此操作不影响已入库的历史会话。')) {
    try {
      isClearing.value = true
      localStorage.removeItem('relaymesh_drafts_v2')
      localStorage.removeItem('relaymesh_active_draft_index')
      toast.success('已清空本地草稿与临时缓存')
    } catch (err: any) {
      toast.error('清理失败: ' + err.message)
    } finally {
      isClearing.value = false
    }
  }
}
</script>

<template>
  <div class="space-y-4">
    <!-- Section Header -->
    <div class="flex items-center justify-between pb-1.5 border-b border-border/70">
      <div class="flex items-center gap-1.5">
        <Database class="w-3.5 h-3.5 text-primary" />
        <span class="text-xs font-bold font-mono text-foreground">数据备份与缓存维护</span>
      </div>
    </div>

    <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
      <!-- 导出配置 -->
      <div class="p-3.5 rounded-xs border border-border/70 bg-card/60 space-y-2.5 flex flex-col justify-between">
        <div class="space-y-1">
          <div class="text-xs font-mono font-semibold text-foreground flex items-center gap-1.5">
            <Download class="w-3.5 h-3.5 text-primary" />
            <span>导出当前全部配置</span>
          </div>
          <p class="text-[10px] text-muted-foreground leading-relaxed">
            将当前系统的全部参数、提示词模板及预设标签导出为标准 JSON 备份文件。
          </p>
        </div>
        <button
          type="button"
          class="h-8 px-3 rounded-xs text-xs font-mono font-bold bg-primary text-primary-foreground flex items-center justify-center gap-1.5 cursor-pointer hover:opacity-90 transition-opacity"
          @click="handleExportSettings"
        >
          <Download class="w-3.5 h-3.5" />
          <span>导出配置文件 (.json)</span>
        </button>
      </div>

      <!-- 清理临时缓存 -->
      <div class="p-3.5 rounded-xs border border-border/70 bg-card/60 space-y-2.5 flex flex-col justify-between">
        <div class="space-y-1">
          <div class="text-xs font-mono font-semibold text-foreground flex items-center gap-1.5">
            <Trash2 class="w-3.5 h-3.5 text-destructive" />
            <span>清理本地草稿与缓存</span>
          </div>
          <p class="text-[10px] text-muted-foreground leading-relaxed">
            清空当前浏览器存储的本地输入框草稿与临时缓存。已入库的历史记录不受影响。
          </p>
        </div>
        <button
          type="button"
          class="h-8 px-3 rounded-xs text-xs font-mono font-bold border border-destructive/30 text-destructive bg-destructive/10 hover:bg-destructive hover:text-destructive-foreground flex items-center justify-center gap-1.5 cursor-pointer transition-colors"
          @click="handleClearLocalCache"
        >
          <Trash2 class="w-3.5 h-3.5" />
          <span>立即清空本地缓存</span>
        </button>
      </div>
    </div>
  </div>
</template>
