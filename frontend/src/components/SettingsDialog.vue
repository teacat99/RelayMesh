<script setup lang="ts">
import { useSettingsStore } from '../stores/settings'
import SettingsForm from './SettingsForm.vue'
import Dialog from './ui/dialog/Dialog.vue'
import DialogContent from './ui/dialog/DialogContent.vue'
import DialogHeader from './ui/dialog/DialogHeader.vue'
import DialogTitle from './ui/dialog/DialogTitle.vue'
import { Settings, CheckCircle2, Loader2 } from 'lucide-vue-next'

const settingsStore = useSettingsStore()
</script>

<template>
  <Dialog :open="settingsStore.isSettingsOpen" @update:open="(v) => (v ? settingsStore.openSettings() : settingsStore.closeSettings())">
    <DialogContent class="sm:max-w-4xl lg:max-w-5xl w-[94vw] h-[86vh] max-h-[88vh] flex flex-col p-0 gap-0 overflow-hidden shadow-2xl rounded-md border-border/80 bg-card">
      <div class="px-5 h-11 border-b border-border/70 flex flex-row items-center justify-between shrink-0 bg-card/90 backdrop-blur-xs pr-12">
        <DialogTitle class="flex items-center gap-2 text-xs sm:text-sm font-bold font-mono tracking-tight text-foreground text-left">
          <div class="p-1 rounded-xs bg-primary/10 text-primary border border-primary/20 shrink-0">
            <Settings class="w-3.5 h-3.5" />
          </div>
          <span>系统偏好与参数设置</span>
          <span class="text-xs text-muted-foreground font-normal font-sans ml-1 hidden sm:inline">Preferences & Settings</span>
        </DialogTitle>

        <!-- 自动保存动态加载与完成提示（1秒后自动消失） -->
        <div
          v-if="settingsStore.saveStatus !== 'idle'"
          class="flex items-center gap-1.5 text-[11px] font-mono text-primary transition-all animate-in fade-in zoom-in-95 duration-150"
        >
          <Loader2 v-if="settingsStore.saveStatus === 'saving'" class="w-3.5 h-3.5 animate-spin text-primary shrink-0" />
          <CheckCircle2 v-else class="w-3.5 h-3.5 text-primary shrink-0" />
          <span>{{ settingsStore.saveStatus === 'saving' ? '正在保存...' : '已自动保存' }}</span>
        </div>
      </div>

      <div class="flex-1 overflow-y-auto px-5 py-4">
        <SettingsForm />
      </div>
    </DialogContent>
  </Dialog>
</template>
