<script setup lang="ts">
import { computed, reactive, watch } from 'vue'
import { toast } from 'vue-sonner'
import { History } from 'lucide-vue-next'
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter } from '../ui/dialog'
import Button from '../ui/button/Button.vue'
import { useSettingsStore } from '@/stores/settings'
import { useAppMode } from '@/composables/useAppMode'
import { SCOPE_NAMES, type Decision } from '@/lib/presetVersions'

// 快捷标签预设的出厂文案更新了、而你改过它们：逐条选择怎么处理。
// 全局挂载（App.vue）：不管启动后落在哪个页面，只要人在 RelayMesh 里就会弹；在 ExecMesh 里不打扰，回到 RelayMesh 时再弹。
// 关闭 / 稍后都不改任何内容，设置里「快捷标签预设」顶部有入口可以再打开。

const settingsStore = useSettingsStore()
const mode = useAppMode()

const OPTIONS: Array<{ value: Decision; label: string; hint: string }> = [
  { value: 'both', label: '均保留', hint: '你的版本不动，最新版另存为一条新的（标题带「新版」），两条都能用' },
  { value: 'latest', label: '使用最新', hint: '换成最新版；你原来的版本存进「版本留存」（旧版，已失效），随时可以重新启用' },
  { value: 'mine', label: '使用旧版', hint: '继续用你的版本；最新版存进「版本留存」（新版，未启用），想用时再启用' },
]

// 默认选「均保留」：什么都不丢、什么都不藏
const decisions = reactive<Record<string, Decision>>({})
watch(
  () => settingsStore.presetConflicts,
  list => {
    const keys = new Set(list.map(c => c.key))
    for (const k of Object.keys(decisions)) if (!keys.has(k)) delete decisions[k]
    for (const c of list) if (!decisions[c.key]) decisions[c.key] = 'both'
  },
  { immediate: true },
)

const open = computed(() => settingsStore.presetConflicts.length > 0 && !settingsStore.presetConflictsPostponed && mode.value === 'relay')

function setAll(v: Decision) {
  for (const c of settingsStore.presetConflicts) decisions[c.key] = v
}

function hintOf(v: Decision | undefined) {
  return OPTIONS.find(o => o.value === v)?.hint ?? ''
}

function apply() {
  const n = settingsStore.resolvePresetConflicts({ ...decisions })
  if (n > 0) toast.success(`已处理 ${n} 条快捷预设`, { description: '换下来的内容在「设置 → 快捷标签预设 → 版本留存」' })
}

function onOpenChange(v: boolean) {
  if (!v) settingsStore.postponePresetConflicts()
}

// 用户没改过的预设在后台自动换成了新版：告诉一声，旧版留在「版本留存」里
watch(
  () => settingsStore.presetUpgrade,
  n => {
    if (!n || mode.value !== 'relay') return
    toast.info(`${n.count} 条快捷预设已更新为新版`, { description: '你没改过它们；旧版在「设置 → 快捷标签预设 → 版本留存」' })
  },
)
</script>

<template>
  <Dialog :open="open" @update:open="onOpenChange">
    <DialogContent class="max-w-[640px] rounded-sm p-5 gap-3" data-testid="preset-update-dialog" :show-close="false">
      <DialogHeader class="space-y-1.5 text-left">
        <DialogTitle class="text-sm font-bold flex items-center gap-1.5">
          <History class="w-3.5 h-3.5 text-primary" />
          快捷标签预设有更新
        </DialogTitle>
        <DialogDescription class="text-xs leading-relaxed text-muted-foreground">
          新版本更新了 {{ settingsStore.presetConflicts.length }} 条出厂预设，而你之前改过它们。逐条选一下怎么处理——任何选择都不会丢内容，没选的下次还会问。
        </DialogDescription>
      </DialogHeader>

      <div class="flex items-center gap-2 text-[11px] font-mono text-muted-foreground">
        <span>全部：</span>
        <button v-for="o in OPTIONS" :key="o.value" type="button" class="underline underline-offset-2 hover:text-foreground cursor-pointer" :data-testid="`preset-update-all-${o.value}`" @click="setAll(o.value)">
          {{ o.label }}
        </button>
      </div>

      <div class="max-h-[50vh] overflow-y-auto space-y-2.5 pr-0.5">
        <div v-for="c in settingsStore.presetConflicts" :key="c.key" class="rounded-xs border border-border/70 bg-card/60 p-2.5 space-y-2" :data-testid="`preset-conflict-${c.key}`">
          <div class="flex items-center gap-1.5 text-xs font-mono">
            <span class="px-1 py-0 rounded-2xs border border-primary/30 text-primary text-[9px]">{{ SCOPE_NAMES[c.scope] }}</span>
            <span class="font-bold truncate">{{ c.label }}</span>
          </div>

          <div class="grid grid-cols-1 sm:grid-cols-2 gap-2">
            <div class="min-w-0 space-y-1">
              <div class="text-[10px] font-mono text-muted-foreground">我的（你改过的）</div>
              <div class="text-[11px] leading-relaxed whitespace-pre-wrap break-words rounded-xs bg-background border border-border/60 p-1.5 max-h-24 overflow-y-auto">{{ c.mineText || '（空）' }}</div>
            </div>
            <div class="min-w-0 space-y-1">
              <div class="text-[10px] font-mono text-muted-foreground">最新出厂</div>
              <div class="text-[11px] leading-relaxed whitespace-pre-wrap break-words rounded-xs bg-background border border-border/60 p-1.5 max-h-24 overflow-y-auto">{{ c.latestText || '（空）' }}</div>
            </div>
          </div>

          <div class="flex items-center gap-1" role="radiogroup" :aria-label="`${c.label} 的处理方式`">
            <button
              v-for="o in OPTIONS"
              :key="o.value"
              type="button"
              role="radio"
              :aria-checked="decisions[c.key] === o.value"
              class="px-2.5 py-1 rounded-xs text-xs font-mono border transition-all cursor-pointer"
              :class="decisions[c.key] === o.value ? 'border-primary/50 bg-primary/10 text-foreground font-bold' : 'border-border/70 text-muted-foreground hover:bg-muted'"
              :data-testid="`preset-decision-${c.key}-${o.value}`"
              @click="decisions[c.key] = o.value"
            >
              {{ o.label }}
            </button>
          </div>
          <div class="text-[10px] text-muted-foreground leading-relaxed">{{ hintOf(decisions[c.key]) }}</div>
        </div>
      </div>

      <DialogFooter class="flex-row justify-end gap-2 sm:space-x-0">
        <Button variant="outline" size="sm" class="rounded-sm text-xs" data-testid="preset-update-postpone" @click="settingsStore.postponePresetConflicts()">稍后</Button>
        <Button size="sm" class="rounded-sm text-xs" data-testid="preset-update-apply" @click="apply">应用选择</Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
