<script setup lang="ts">
import { ref } from 'vue'
import { useSettingsStore } from '@/stores/settings'
import {
  Database,
  Download,
  Trash2,
  Sparkles,
  Info,
  Gauge,
  Activity
} from 'lucide-vue-next'
import { toast } from 'vue-sonner'

const settingsStore = useSettingsStore()
const isExporting = ref(false)
const isClearing = ref(false)

const rateLimitPills = [
  { label: '不限速', value: 0 },
  { label: '128 KB/s (1Mbps推荐)', value: 128 },
  { label: '256 KB/s (2Mbps推荐)', value: 256 },
  { label: '512 KB/s', value: 512 },
  { label: '1024 KB/s', value: 1024 }
]

function setRateLimit(val: number) {
  settingsStore.updateSettings({ imageRateLimitKB: val })
  if (val > 0) {
    toast.success(`已设置图片下载限速为 ${val} KB/s`)
  } else {
    toast.success('已恢复图片全速下载（不限速）')
  }
}

function handleRateLimitInput(e: Event) {
  const target = e.target as HTMLInputElement
  let val = parseInt(target.value, 10)
  if (isNaN(val) || val < 0) val = 0
  settingsStore.updateSettings({ imageRateLimitKB: val })
}

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

    <!-- 网络传输与图片限速控制 (QoS 保护) -->
    <div class="mt-4 pt-4 border-t border-border/70 space-y-3">
      <div class="flex items-center justify-between pb-1">
        <div class="flex items-center gap-1.5">
          <Gauge class="w-3.5 h-3.5 text-primary" />
          <span class="text-xs font-bold font-mono text-foreground">图片传输与带宽保护 (QoS 限速)</span>
        </div>
        <div class="flex items-center gap-1 text-[11px] font-mono">
          <span
            v-if="settingsStore.settings.imageRateLimitKB && settingsStore.settings.imageRateLimitKB > 0"
            class="px-1.5 py-0.5 rounded-xs bg-amber-500/10 border border-amber-500/30 text-amber-500 font-bold flex items-center gap-1"
          >
            <Activity class="w-3 h-3 animate-pulse" />
            已限速 {{ settingsStore.settings.imageRateLimitKB }} KB/s
          </span>
          <span
            v-else
            class="px-1.5 py-0.5 rounded-xs bg-muted text-muted-foreground"
          >
            全速运行 (不限速)
          </span>
        </div>
      </div>

      <div class="p-3.5 rounded-xs border border-border/70 bg-card/60 space-y-3">
        <div class="space-y-1">
          <div class="text-xs font-mono font-semibold text-foreground flex items-center gap-1.5">
            <Activity class="w-3.5 h-3.5 text-primary" />
            <span>单图下载速率限制 (Image Download Rate Limit)</span>
          </div>
          <p class="text-[10px] text-muted-foreground leading-relaxed">
            限制单个图片传输的最大流出速率。在小带宽云服务器（如 1Mbps~3Mbps）上建议配置为 128KB/s 或 256KB/s，为核心 SSE 实时事件流与心跳保活预留足够生存带宽，防止大图瞬间吃满带宽导致连接超时假死。设置为 0 为全速不限速。
          </p>
        </div>

        <!-- 快捷档位 Pills -->
        <div class="flex flex-wrap items-center gap-1.5 pt-1">
          <button
            v-for="pill in rateLimitPills"
            :key="pill.value"
            type="button"
            class="px-2.5 py-1 rounded-xs text-[11px] font-mono border transition-all cursor-pointer select-none"
            :class="[
              (settingsStore.settings.imageRateLimitKB || 0) === pill.value
                ? 'bg-primary text-primary-foreground border-primary font-bold shadow-xs'
                : 'bg-muted/50 hover:bg-muted text-foreground border-border/70'
            ]"
            @click="setRateLimit(pill.value)"
          >
            {{ pill.label }}
          </button>
        </div>

        <!-- 自定义微调输入框 -->
        <div class="flex items-center gap-2 pt-1 border-t border-border/40">
          <span class="text-[11px] font-mono text-muted-foreground shrink-0">自定义速率:</span>
          <div class="relative w-36">
            <input
              type="number"
              min="0"
              step="32"
              class="w-full h-7 pl-2 pr-12 text-xs font-mono rounded-xs border border-border bg-background text-foreground focus:outline-hidden focus:ring-1 focus:ring-primary"
              :value="settingsStore.settings.imageRateLimitKB || 0"
              @change="handleRateLimitInput"
            />
            <span class="absolute right-2 top-1.5 text-[10px] font-mono text-muted-foreground pointer-events-none select-none">
              KB/s
            </span>
          </div>
          <span class="text-[10px] font-mono text-muted-foreground">
            {{ (settingsStore.settings.imageRateLimitKB || 0) > 0 ? `(约占用 ${(settingsStore.settings.imageRateLimitKB * 8 / 1024).toFixed(2)} Mbps 上行带宽)` : '(无限制)' }}
          </span>
        </div>
      </div>
    </div>
  </div>
</template>
