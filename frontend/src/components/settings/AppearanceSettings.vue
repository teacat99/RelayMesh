<script setup lang="ts">
import { useThemeStore } from '@/stores/theme'
import { useNotifyStore } from '@/stores/notify'
import { useNotifyPrefsStore } from '@/stores/notifyPrefs'
import { useFxStore } from '@/stores/fx'
import ThemeAutoIcon from '../ThemeAutoIcon.vue'
import NtfySettings from './NtfySettings.vue'
import { boardStaleChoices, boardStaleSelected } from '@/lib/notifyPrefs'
import { computed } from 'vue'
import {
  Sun,
  Moon,
  Laptop,
  Volume2,
  VolumeX,
  Bell,
  BellMinus,
  BellOff,
  Hourglass,
  ArrowLeftRight,
  RotateCcw,
  Sparkles
} from 'lucide-vue-next'

const themeStore = useThemeStore()
const notifyStore = useNotifyStore()
const notifyPrefs = useNotifyPrefsStore()
const fxStore = useFxStore()

function resetAppearanceSettings() {
  themeStore.setMode('auto')
  if (!notifyStore.soundEnabled) notifyStore.toggleSound()
  if (!notifyStore.desktopEnabled) notifyStore.toggleDesktop()
  fxStore.setModeSwitch(true)
}

// 指派久无人接（#180 B3d）：选项与默认值都来自服务端给出的偏好，界面不抄默认值；
// 选「默认」那一项等于恢复默认（存 0），以后默认值调整时没改过的人跟着走。
const staleChoices = computed(() => boardStaleChoices(notifyPrefs.prefs))
const staleSelected = computed(() => boardStaleSelected(notifyPrefs.prefs))
function onStaleChange(e: Event) {
  void notifyPrefs.setBoardStaleMinutes(Number((e.target as HTMLSelectElement).value))
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
        <span class="text-xs font-bold font-mono text-foreground">外观主题、通知提醒与界面动效</span>
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

    <!-- 通知、提示音与界面动效 -->
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

      <!-- 免打扰：服务端开关，浏览器与手机推送一起拦；阻塞待答、@ 你、终端审批、系统告警不受影响 -->
      <button
        type="button"
        data-testid="settings-dnd"
        :aria-pressed="notifyPrefs.dnd"
        class="p-3 rounded-xs border text-left transition-all font-mono cursor-pointer flex items-center justify-between gap-3"
        :class="notifyPrefs.dnd
          ? 'border-amber-500/50 bg-amber-500/10 text-foreground'
          : 'border-border/70 bg-card/60 text-muted-foreground'"
        @click="notifyPrefs.toggleDnd"
      >
        <div class="space-y-0.5">
          <div class="text-xs font-semibold flex items-center gap-1.5">
            <BellMinus class="w-3.5 h-3.5" :class="notifyPrefs.dnd ? 'text-amber-500' : 'text-primary'" />
            <span>免打扰</span>
          </div>
          <div class="text-[10px] text-muted-foreground">
            开启后，浏览器与手机推送都只保留紧急的：阻塞待答、@ 你、终端审批、系统告警；其余消息只在网页里看
          </div>
        </div>
        <div
          class="w-8 h-4.5 rounded-full transition-colors flex items-center p-0.5 shrink-0"
          :class="notifyPrefs.dnd ? 'bg-amber-500' : 'bg-muted-foreground/30'"
        >
          <div
            class="w-3.5 h-3.5 rounded-full bg-white transition-transform"
            :class="notifyPrefs.dnd ? 'translate-x-3.5' : 'translate-x-0'"
          ></div>
        </div>
      </button>

      <!-- 项目切换动画开关 -->
      <button
        type="button"
        data-testid="fx-mode-switch"
        :aria-pressed="fxStore.modeSwitch"
        class="p-3 rounded-xs border text-left transition-all font-mono cursor-pointer flex items-center justify-between gap-3"
        :class="fxStore.modeSwitch
          ? 'border-primary/40 bg-primary/10 text-foreground'
          : 'border-border/70 bg-card/60 text-muted-foreground'"
        @click="fxStore.setModeSwitch(!fxStore.modeSwitch)"
      >
        <div class="space-y-0.5">
          <div class="text-xs font-semibold flex items-center gap-1.5">
            <ArrowLeftRight class="w-3.5 h-3.5 text-primary" />
            <span>项目切换动画</span>
          </div>
          <div class="text-[10px] text-muted-foreground">
            RelayMesh 与 ExecMesh 互相切换时，旧画面化作粒子消散（约 0.6 秒）；系统开启「减少动态效果」时自动跳过
          </div>
        </div>
        <div
          class="w-8 h-4.5 rounded-full transition-colors flex items-center p-0.5 shrink-0"
          :class="fxStore.modeSwitch ? 'bg-primary' : 'bg-muted-foreground/30'"
        >
          <div
            class="w-3.5 h-3.5 rounded-full bg-white transition-transform"
            :class="fxStore.modeSwitch ? 'translate-x-3.5' : 'translate-x-0'"
          ></div>
        </div>
      </button>
    </div>

    <!-- 系统通知的权限引导：只在被屏蔽或不支持时出现；权限只在点开关时才会去请求 -->
    <p
      v-if="notifyStore.permission === 'denied'"
      class="text-[11px] font-mono text-amber-600 dark:text-amber-400 leading-relaxed"
      data-testid="notify-permission-hint"
    >
      浏览器已屏蔽本站的通知：点地址栏左侧的站点设置，把「通知」改为允许，再回来打开上面的「浏览器系统级通知」。
    </p>
    <p
      v-else-if="notifyStore.permission === 'unsupported'"
      class="text-[11px] font-mono text-muted-foreground leading-relaxed"
      data-testid="notify-permission-hint"
    >
      这个浏览器不支持系统通知：提醒会以页面内的提示和提示音呈现。
    </p>

    <!-- 指派久无人接（#180 B3d）：共享任务板上有指派派出后一直没人接，提醒你一次。时长另外决定任务行上「已等」什么时候标红、
         以及智能体回执里 board 行的口径；开关只管提醒。 -->
    <div
      data-testid="settings-board-stale"
      class="p-3 rounded-xs border font-mono flex flex-col gap-2.5 sm:flex-row sm:items-center sm:justify-between sm:gap-4 transition-colors"
      :class="notifyPrefs.boardStaleOff
        ? 'border-border/70 bg-card/60 text-muted-foreground'
        : 'border-primary/40 bg-primary/10 text-foreground'"
    >
      <button
        type="button"
        data-testid="settings-board-stale-toggle"
        :aria-pressed="!notifyPrefs.boardStaleOff"
        class="min-w-0 flex-1 text-left cursor-pointer flex items-center justify-between gap-3"
        @click="notifyPrefs.setBoardStaleOff(!notifyPrefs.boardStaleOff)"
      >
        <div class="space-y-0.5">
          <div class="text-xs font-semibold flex items-center gap-1.5">
            <Hourglass class="w-3.5 h-3.5 text-primary" />
            <span>指派久无人接的提醒</span>
          </div>
          <div class="text-[10px] text-muted-foreground leading-relaxed">
            共享任务板上有指派派出后，超过设定的时长还没人接，就提醒你一次（同一个承接者的合成一条）。这个时长同时决定任务页里「已等」什么时候标红、智能体回执里的 board 行怎么算「久无人接」，开关只管提醒。手机推送默认不发，要的话在下面「各类消息的优先级」里打开
          </div>
        </div>
        <div
          class="w-8 h-4.5 rounded-full transition-colors flex items-center p-0.5 shrink-0"
          :class="notifyPrefs.boardStaleOff ? 'bg-muted-foreground/30' : 'bg-primary'"
        >
          <div
            class="w-3.5 h-3.5 rounded-full bg-white transition-transform"
            :class="notifyPrefs.boardStaleOff ? 'translate-x-0' : 'translate-x-3.5'"
          ></div>
        </div>
      </button>
      <label class="flex shrink-0 flex-col gap-1 text-[10px] text-muted-foreground sm:w-44">
        <span>超过多久算「久无人接」</span>
        <select
          data-testid="settings-board-stale-after"
          :value="staleSelected"
          :disabled="notifyPrefs.saving"
          class="w-full h-8 px-2.5 text-xs font-mono bg-background border border-border/80 rounded-xs focus:outline-none focus:border-primary text-foreground disabled:opacity-60 cursor-pointer"
          @change="onStaleChange"
        >
          <option v-for="c in staleChoices" :key="c.value" :value="c.value">{{ c.label }}</option>
        </select>
      </label>
    </div>

    <!-- 服务端推送（ntfy）：不依赖网页是否打开 -->
    <NtfySettings />
  </div>
</template>
