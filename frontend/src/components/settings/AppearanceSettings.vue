<script setup lang="ts">
import { useThemeStore } from '@/stores/theme'
import { useNotifyStore } from '@/stores/notify'
import ThemeAutoIcon from '../ThemeAutoIcon.vue'
import {
  Sun,
  Moon,
  Laptop,
  Volume2,
  VolumeX,
  Bell,
  BellOff,
  RotateCcw,
  Sparkles
} from 'lucide-vue-next'

const themeStore = useThemeStore()
const notifyStore = useNotifyStore()

function resetAppearanceSettings() {
  themeStore.setMode('auto')
  if (!notifyStore.soundEnabled) notifyStore.toggleSound()
  if (!notifyStore.desktopEnabled) notifyStore.toggleDesktop()
}

const themeOptions = [
  { id: 'light', label: '浅色模式 (Light)', icon: Sun },
  { id: 'dark', label: '深色模式 (Dark)', icon: Moon },
  { id: 'auto', label: '跟随系统 (Auto)', icon: ThemeAutoIcon }
]
</script>

<template>
  <div class="space-y-4">
    <!-- Section Header -->
    <div class="flex items-center justify-between pb-1.5 border-b border-border/70">
      <div class="flex items-center gap-1.5">
        <Sparkles class="w-3.5 h-3.5 text-primary" />
        <span class="text-xs font-bold font-mono text-foreground">外观主题与声音通知</span>
      </div>
      <button
        type="button"
        class="text-[10px] font-mono text-muted-foreground hover:text-foreground underline underline-offset-2 flex items-center gap-1 cursor-pointer transition-colors"
        @click="resetAppearanceSettings"
      >
        <RotateCcw class="w-2.5 h-2.5" />
        <span>恢复默认</span>
      </button>
    </div>

    <!-- 主题切换 -->
    <div class="p-3.5 rounded-xs border border-border/70 bg-card/60 space-y-2.5">
      <div class="text-xs font-mono font-medium text-foreground">主题显示模式</div>
      <div class="grid grid-cols-1 sm:grid-cols-3 gap-2">
        <button
          v-for="opt in themeOptions"
          :key="opt.id"
          type="button"
          class="p-2.5 rounded-xs border text-xs font-mono flex items-center gap-2 cursor-pointer transition-all"
          :class="themeStore.mode === opt.id
            ? 'border-primary bg-primary text-primary-foreground font-bold shadow-2xs'
            : 'border-border/70 bg-background/70 hover:bg-muted text-muted-foreground'"
          @click="themeStore.setMode(opt.id as any)"
        >
          <component :is="opt.icon" class="w-4 h-4 shrink-0" />
          <span>{{ opt.label }}</span>
        </button>
      </div>
    </div>

    <!-- 通知与提示音 -->
    <div class="grid grid-cols-1 sm:grid-cols-2 gap-2.5">
      <!-- 提示音开关 -->
      <button
        type="button"
        class="p-3 rounded-xs border text-left transition-all font-mono cursor-pointer flex items-center justify-between"
        :class="notifyStore.soundEnabled
          ? 'border-primary/40 bg-primary/10 text-foreground'
          : 'border-border/70 bg-card/60 text-muted-foreground'"
        @click="notifyStore.toggleSound"
      >
        <div class="space-y-0.5">
          <div class="text-xs font-semibold flex items-center gap-1.5">
            <component :is="notifyStore.soundEnabled ? Volume2 : VolumeX" class="w-3.5 h-3.5 text-primary" />
            <span>新消息蜂鸣音效</span>
          </div>
          <div class="text-[10px] text-muted-foreground">收到新反馈请求时播放轻柔蜂鸣</div>
        </div>
        <div
          class="w-8 h-4.5 rounded-full transition-colors flex items-center p-0.5"
          :class="notifyStore.soundEnabled ? 'bg-primary' : 'bg-muted-foreground/30'"
        >
          <div
            class="w-3.5 h-3.5 rounded-full bg-white transition-transform"
            :class="notifyStore.soundEnabled ? 'translate-x-3.5' : 'translate-x-0'"
          ></div>
        </div>
      </button>

      <!-- 桌面通知开关 -->
      <button
        type="button"
        class="p-3 rounded-xs border text-left transition-all font-mono cursor-pointer flex items-center justify-between"
        :class="notifyStore.desktopEnabled
          ? 'border-primary/40 bg-primary/10 text-foreground'
          : 'border-border/70 bg-card/60 text-muted-foreground'"
        @click="notifyStore.toggleDesktop"
      >
        <div class="space-y-0.5">
          <div class="text-xs font-semibold flex items-center gap-1.5">
            <component :is="notifyStore.desktopEnabled ? Bell : BellOff" class="w-3.5 h-3.5 text-primary" />
            <span>浏览器系统级通知</span>
          </div>
          <div class="text-[10px] text-muted-foreground">切到后台时通过系统弹窗提醒</div>
        </div>
        <div
          class="w-8 h-4.5 rounded-full transition-colors flex items-center p-0.5"
          :class="notifyStore.desktopEnabled ? 'bg-primary' : 'bg-muted-foreground/30'"
        >
          <div
            class="w-3.5 h-3.5 rounded-full bg-white transition-transform"
            :class="notifyStore.desktopEnabled ? 'translate-x-3.5' : 'translate-x-0'"
          ></div>
        </div>
      </button>
    </div>
  </div>
</template>
