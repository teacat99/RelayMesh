<script setup lang="ts">
import { computed, ref } from 'vue'
import { toast } from 'vue-sonner'
import { Archive, ChevronDown, ChevronRight, RotateCcw, Trash2 } from 'lucide-vue-next'
import { useSettingsStore } from '@/stores/settings'
import { SCOPE_NAMES, archiveReasonText, describeItem, type ArchivedEntry } from '@/lib/presetVersions'
import { relativeTime } from '@/lib/terminal/format'
import ConfirmDialog from '../terminal/ConfirmDialog.vue'

// 「版本留存」：出厂预设更新、或「恢复默认」时被换下来的内容，各自是独立的一条。
//   旧版：被换下来的内容（你的版本，或没改过的旧出厂文案）——已失效，不再出现在快捷标签里；
//   新版：你选了「使用旧版」时没有启用的最新出厂文案。
// 可以重新启用（作为一条新的快捷预设加回去），也可以删除；整个留存区可以清空。

const settingsStore = useSettingsStore()

// 最近的在前
const entries = computed<ArchivedEntry[]>(() => [...settingsStore.presetVersions.archive].reverse())
const expanded = ref<Set<string>>(new Set())
const confirmClear = ref(false)

function toggle(uid: string) {
  const next = new Set(expanded.value)
  if (next.has(uid)) next.delete(uid)
  else next.add(uid)
  expanded.value = next
}

function reenable(e: ArchivedEntry) {
  if (settingsStore.reenablePresetVersion(e.uid)) {
    toast.success(`已重新启用「${e.label}」`, { description: `已加回「${SCOPE_NAMES[e.scope]}」` })
  }
}

function remove(e: ArchivedEntry) {
  settingsStore.deletePresetVersion(e.uid)
}

function clearAll() {
  settingsStore.clearPresetVersions()
  confirmClear.value = false
}

const textOf = (e: ArchivedEntry) => describeItem(e.scope, e.payload).text || '（空）'
</script>

<template>
  <div class="space-y-3 pt-2" data-testid="preset-archive">
    <div class="flex items-center justify-between pb-1.5 border-b border-border/70">
      <div class="flex items-center gap-1.5">
        <Archive class="w-3.5 h-3.5 text-primary" />
        <span class="text-xs font-bold font-mono text-foreground">版本留存（旧版 / 新版）</span>
        <span v-if="entries.length" class="text-[10px] font-mono text-muted-foreground">{{ entries.length }} 条</span>
      </div>
      <button
        v-if="entries.length"
        type="button"
        class="text-[10px] font-mono text-muted-foreground hover:text-destructive underline underline-offset-2 flex items-center gap-1 cursor-pointer transition-colors"
        data-testid="preset-archive-clear"
        @click="confirmClear = true"
      >
        <Trash2 class="w-2.5 h-2.5" />
        <span>清空</span>
      </button>
    </div>

    <div class="p-3 rounded-xs border border-border/70 bg-card/60 space-y-2">
      <div v-if="!entries.length" class="text-[11px] text-muted-foreground leading-relaxed" data-testid="preset-archive-empty">
        还没有留存的版本。出厂预设更新、或你点「恢复默认」时被换下来的内容会存在这里，可以重新启用或删除。
      </div>

      <div
        v-for="e in entries"
        :key="e.uid"
        class="rounded-xs border border-border/70 bg-background"
        :class="e.kind === 'old' ? 'opacity-90' : ''"
        data-testid="preset-archive-item"
        :data-kind="e.kind"
        :data-scope="e.scope"
      >
        <div class="flex items-center gap-2 px-2 py-1.5">
          <button type="button" class="flex items-center gap-1.5 min-w-0 flex-1 text-left cursor-pointer" :aria-expanded="expanded.has(e.uid)" @click="toggle(e.uid)">
            <component :is="expanded.has(e.uid) ? ChevronDown : ChevronRight" class="w-3 h-3 shrink-0 text-muted-foreground" />
            <span
              class="shrink-0 px-1 py-0 rounded-2xs border text-[9px] font-mono"
              :class="e.kind === 'old' ? 'border-amber-500/40 text-amber-600 dark:text-amber-400' : 'border-emerald-500/40 text-emerald-600 dark:text-emerald-400'"
            >{{ e.kind === 'old' ? '旧版 · 已失效' : '新版 · 未启用' }}</span>
            <span class="text-xs font-mono font-medium truncate">{{ e.label }}</span>
            <span class="hidden sm:inline text-[10px] font-mono text-muted-foreground shrink-0">{{ SCOPE_NAMES[e.scope] }}</span>
            <span class="hidden sm:inline text-[10px] font-mono text-muted-foreground shrink-0">· {{ relativeTime(e.archivedAt) }}</span>
          </button>
          <button
            type="button"
            class="shrink-0 text-[10px] font-mono text-primary hover:underline flex items-center gap-1 cursor-pointer"
            data-testid="preset-archive-reenable"
            @click="reenable(e)"
          >
            <RotateCcw class="w-2.5 h-2.5" />
            <span>重新启用</span>
          </button>
          <button
            type="button"
            class="shrink-0 text-muted-foreground hover:text-destructive cursor-pointer p-0.5"
            title="删除这条留存"
            data-testid="preset-archive-delete"
            @click="remove(e)"
          >
            <Trash2 class="w-3 h-3" />
          </button>
        </div>
        <div v-if="expanded.has(e.uid)" class="px-2 pb-2 space-y-1.5">
          <div class="text-[10px] text-muted-foreground leading-relaxed">{{ archiveReasonText(e) }}</div>
          <div class="text-[11px] leading-relaxed whitespace-pre-wrap break-words rounded-xs bg-card border border-border/60 p-1.5 max-h-40 overflow-y-auto">{{ textOf(e) }}</div>
        </div>
      </div>

      <div class="text-[11px] text-muted-foreground pt-0.5">
        只有这里的自定义内容（阶段模板、状态预设、通用预设）会留存版本；系统提示词的更新不在其内。
      </div>
    </div>

    <ConfirmDialog
      v-model:open="confirmClear"
      title="清空版本留存？"
      :description="`将删除全部 ${entries.length} 条留存的旧版 / 新版，不会影响当前正在使用的快捷预设。此操作无法撤销。`"
      confirm-text="清空"
      destructive
      @confirm="clearAll"
    />
  </div>
</template>
