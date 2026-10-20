<script setup lang="ts">
import { useSettingsStore } from '@/stores/settings'
import InboxSettings from './InboxSettings.vue'
import {
  Laptop,
  Clock,
  RotateCcw
} from 'lucide-vue-next'

const settingsStore = useSettingsStore()

function handleInputSave(key: string, val: any) {
  settingsStore.updateSettings({ [key]: val })
}

function resetWaitingSettings() {
  settingsStore.updateSettings({
    defaultWaitCountdownMinutes: 1,
    defaultWaitCountdownSeconds: 60,
    maxNoFeedbackChecks: 6,
    promptWaitMinutes: 2,
    defaultTimeoutSeconds: 120
  })
}

const timeoutOptions = [
  { label: '1 分钟', value: 60, title: '60 秒' },
  { label: '2 分钟', value: 120, title: '120 秒' },
  { label: '5 分钟', value: 300, title: '300 秒' },
  { label: '10 分钟', value: 600, title: '600 秒' }
]

const maxChecksOptions = [
  { label: '3 次', value: 3 },
  { label: '6 次', value: 6 },
  { label: '∞ 无限', value: 0 }
]

// 挂起倒计时按秒存（秒回 / 30 秒 / 1 分钟，#121）
const waitCountdownOptions = [
  { label: '秒回', value: 0, title: '几乎立刻发空回执' },
  { label: '30 秒', value: 30, title: '挂起 30 秒' },
  { label: '1 分钟', value: 60, title: '挂起 1 分钟' }
]

function countdownSeconds(): number {
  const st = settingsStore.settings
  if (typeof st.defaultWaitCountdownSeconds === 'number' && st.defaultWaitCountdownSeconds >= 0) return st.defaultWaitCountdownSeconds
  return (st.defaultWaitCountdownMinutes ?? 1) * 60
}
function countdownLabel(): string {
  const sec = countdownSeconds()
  if (sec <= 0) return '秒回'
  return sec < 60 ? `${sec} 秒` : `${Math.round(sec / 60)} 分钟`
}
function pickCountdown(sec: number) {
  settingsStore.updateSettings({ defaultWaitCountdownSeconds: sec, defaultWaitCountdownMinutes: Math.ceil(sec / 60) })
}

const promptWaitOptions = [
  { label: '1 分钟', value: 1 },
  { label: '2 分钟', value: 2 },
  { label: '5 分钟', value: 5 },
  { label: '10 分钟', value: 10 }
]
</script>

<template>
  <div class="space-y-4">
    <!-- Section Header -->
    <div class="flex items-center justify-between pb-1.5 border-b border-border/70">
      <div class="flex items-center gap-1.5">
        <Clock class="w-3.5 h-3.5 text-primary" />
        <span class="text-xs font-bold font-mono text-foreground">等待与超时参数设定</span>
      </div>
      <button
        type="button"
        class="text-[10px] font-mono text-muted-foreground hover:text-foreground underline underline-offset-2 flex items-center gap-1 cursor-pointer transition-colors"
        @click="resetWaitingSettings"
      >
        <RotateCcw class="w-2.5 h-2.5" />
        <span>恢复默认</span>
      </button>
    </div>

    <!-- 主机名配置 -->
    <div class="p-3 rounded-xs border border-border/70 bg-card/60 space-y-2">
      <div class="flex items-center justify-between text-xs font-mono font-medium text-foreground">
        <span class="flex items-center gap-1.5">
          <Laptop class="w-3.5 h-3.5 text-primary" />
          <span>主机标识名称 (Host Name)</span>
        </span>
        <span v-if="settingsStore.settings.hostName" class="text-[10px] text-primary font-bold">
          {{ settingsStore.settings.hostName }}
        </span>
      </div>
      <input
        type="text"
        :value="settingsStore.settings.hostName || ''"
        placeholder="例如: wsl, macbook, dev-server, prod"
        class="w-full h-8 px-2.5 text-xs font-mono bg-background border border-border/80 rounded-xs focus:outline-none focus:border-primary text-foreground"
        @change="(e) => handleInputSave('hostName', (e.target as HTMLInputElement).value.trim())"
      />
      <p class="text-[10px] text-muted-foreground leading-relaxed">
        工作区路径将显示为 <code class="px-1 py-0.2 rounded-2xs bg-muted font-mono">{{ (settingsStore.settings.hostName || 'host') }}:/path/to/project</code>，便于多端辨识。
      </p>
    </div>

    <!-- 参数选择网格 -->
    <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
      <!-- 提示词指导等待时间 -->
      <div class="p-3 rounded-xs border border-border/70 bg-card/60 space-y-2">
        <label class="text-xs font-mono font-medium text-foreground flex items-center justify-between">
          <span>空回执指令等待时长</span>
          <span class="text-[10px] text-primary font-bold">{{ settingsStore.settings.promptWaitMinutes || 2 }} 分钟</span>
        </label>
        <div class="grid grid-cols-4 gap-1">
          <button
            v-for="opt in promptWaitOptions"
            :key="opt.value"
            type="button"
            class="h-7 text-xs rounded-xs border font-mono transition-all cursor-pointer flex items-center justify-center"
            :class="(settingsStore.settings.promptWaitMinutes || 2) === opt.value
              ? 'border-primary bg-primary text-primary-foreground font-bold shadow-2xs'
              : 'border-border/70 bg-background/70 hover:bg-muted text-muted-foreground'"
            @click="handleInputSave('promptWaitMinutes', opt.value)"
          >
            {{ opt.label }}
          </button>
        </div>
      </div>

      <!-- 倒计时时长 -->
      <div class="p-3 rounded-xs border border-border/70 bg-card/60 space-y-2">
        <label class="text-xs font-mono font-medium text-foreground flex items-center justify-between">
          <span>默认倒计时时长</span>
          <span class="text-[10px] text-primary font-bold">{{ countdownLabel() }}</span>
        </label>
        <div class="grid grid-cols-3 gap-1">
          <button
            v-for="opt in waitCountdownOptions"
            :key="opt.value"
            type="button"
            class="h-7 text-xs rounded-xs border font-mono transition-all cursor-pointer flex items-center justify-center"
            :class="countdownSeconds() === opt.value
              ? 'border-primary bg-primary text-primary-foreground font-bold shadow-2xs'
              : 'border-border/70 bg-background/70 hover:bg-muted text-muted-foreground'"
            @click="pickCountdown(opt.value)"
          >
            {{ opt.label }}
          </button>
        </div>
      </div>

      <!-- 最大探测次数 -->
      <div class="p-3 rounded-xs border border-border/70 bg-card/60 space-y-2">
        <label class="text-xs font-mono font-medium text-foreground flex items-center justify-between">
          <span>最大无反馈检查次数</span>
          <span class="text-[10px] text-primary font-bold">
            {{ settingsStore.settings.maxNoFeedbackChecks === 0 ? '无限' : `${settingsStore.settings.maxNoFeedbackChecks ?? 24} 次` }}
          </span>
        </label>
        <div class="grid grid-cols-3 gap-1">
          <button
            v-for="opt in maxChecksOptions"
            :key="opt.value"
            type="button"
            class="h-7 text-xs rounded-xs border font-mono transition-all cursor-pointer flex items-center justify-center"
            :class="(settingsStore.settings.maxNoFeedbackChecks ?? 24) === opt.value
              ? 'border-primary bg-primary text-primary-foreground font-bold shadow-2xs'
              : 'border-border/70 bg-background/70 hover:bg-muted text-muted-foreground'"
            @click="handleInputSave('maxNoFeedbackChecks', opt.value)"
          >
            {{ opt.label }}
          </button>
        </div>
      </div>

      <!-- 单次会话超时限制 -->
      <div class="p-3 rounded-xs border border-border/70 bg-card/60 space-y-2">
        <label class="text-xs font-mono font-medium text-foreground flex items-center justify-between">
          <span>单次轮询等待上限</span>
          <span class="text-[10px] text-primary font-bold">{{ Math.floor((settingsStore.settings.defaultTimeoutSeconds || 120) / 60) }} 分钟</span>
        </label>
        <div class="grid grid-cols-4 gap-1">
          <button
            v-for="opt in timeoutOptions"
            :key="opt.value"
            type="button"
            class="h-7 text-xs rounded-xs border font-mono transition-all cursor-pointer flex items-center justify-center"
            :class="(settingsStore.settings.defaultTimeoutSeconds || 120) === opt.value
              ? 'border-primary bg-primary text-primary-foreground font-bold shadow-2xs'
              : 'border-border/70 bg-background/70 hover:bg-muted text-muted-foreground'"
            @click="handleInputSave('defaultTimeoutSeconds', opt.value)"
          >
            {{ opt.label }}
          </button>
        </div>
      </div>
    </div>

    <!-- 追加指令收件箱：智能体工作期间（没在等反馈时）你追加的指令如何被它发现 -->
    <InboxSettings />
  </div>
</template>
