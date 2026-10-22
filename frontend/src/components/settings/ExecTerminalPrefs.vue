<script setup lang="ts">
import { onMounted } from 'vue'
import { Minus, Plus, RotateCcw, TerminalSquare } from 'lucide-vue-next'
import { useExecStore } from '@/stores/exec'
import { useTerminalStore } from '@/stores/terminal'
import { useTerminalFont } from '@/composables/useTerminalFont'
import { useTerminalPasteConfirm } from '@/composables/useTerminalPasteConfirm'
import { FONT_DEFAULT, FONT_MAX, FONT_MIN } from '@/lib/terminal/fontSize'
import { LIVE_CAP_DEFAULT, LIVE_CAP_MAX, LIVE_CAP_MIN } from '@/lib/terminal/liveSet'
import { Switch } from '../ui/switch'
import ExecFixedWidthPref from './ExecFixedWidthPref.vue'
import AgentAutoUpgradePref from './AgentAutoUpgradePref.vue'

// ExecMesh 的「终端偏好」：字号、粘贴确认、同时保持连接的会话数只影响这个浏览器，保存在本地；
// 固定终端宽度保存在服务端、所有设备共用（各区块标题旁标明了生效范围）。
const exec = useExecStore()
const terminal = useTerminalStore()
onMounted(() => {
  void terminal.loadStatus()
})
const font = useTerminalFont()
const pasteConfirm = useTerminalPasteConfirm()
</script>

<template>
  <div class="space-y-4">
    <div class="flex items-center justify-between pb-1.5 border-b border-border/70">
      <div class="flex items-center gap-1.5">
        <TerminalSquare class="w-3.5 h-3.5 text-primary" />
        <span class="text-xs font-bold font-mono text-foreground">终端偏好</span>
      </div>
      <span class="text-[10px] font-mono text-muted-foreground">各项标注了生效范围</span>
    </div>

    <!-- 字号 -->
    <section class="p-3.5 rounded-xs border border-border/70 bg-card/60 space-y-2.5" aria-label="终端字号" data-testid="pref-font">
      <div class="flex items-center justify-between gap-2">
        <div class="flex items-center gap-2 min-w-0">
          <div class="text-xs font-mono font-medium text-foreground">终端字号</div>
          <span class="px-1.5 py-0.5 rounded-xs text-[10px] font-mono bg-muted/60 border border-border/70 text-muted-foreground">仅此浏览器</span>
        </div>
        <button
          type="button"
          class="text-[10px] font-mono text-muted-foreground hover:text-foreground underline underline-offset-2 flex items-center gap-1 cursor-pointer transition-colors disabled:opacity-40 disabled:no-underline disabled:cursor-default"
          :disabled="font.fontSize.value === FONT_DEFAULT"
          data-testid="pref-font-reset"
          @click="font.reset()"
        >
          <RotateCcw class="w-2.5 h-2.5" />
          <span>恢复默认（{{ FONT_DEFAULT }}）</span>
        </button>
      </div>
      <div class="flex items-center gap-2">
        <button
          type="button"
          class="w-7 h-7 rounded-sm border border-border/80 flex items-center justify-center hover:bg-muted cursor-pointer disabled:opacity-40 disabled:cursor-not-allowed"
          :disabled="font.fontSize.value <= FONT_MIN"
          aria-label="减小字号"
          data-testid="pref-font-dec"
          @click="font.bump(-1)"
        ><Minus class="w-3 h-3" /></button>
        <span class="w-14 text-center text-sm tabular-nums font-medium" aria-live="polite" data-testid="pref-font-value">{{ font.fontSize.value }} px</span>
        <button
          type="button"
          class="w-7 h-7 rounded-sm border border-border/80 flex items-center justify-center hover:bg-muted cursor-pointer disabled:opacity-40 disabled:cursor-not-allowed"
          :disabled="font.fontSize.value >= FONT_MAX"
          aria-label="增大字号"
          data-testid="pref-font-inc"
          @click="font.bump(1)"
        ><Plus class="w-3 h-3" /></button>
      </div>
      <p class="text-[10px] leading-relaxed text-muted-foreground">
        可选 {{ FONT_MIN }}–{{ FONT_MAX }} px，默认 {{ FONT_DEFAULT }} px。在终端区域里也可以按住 Ctrl（Mac 为 ⌘）滚动滚轮缩放，按 Ctrl+0 恢复默认。
      </p>
    </section>

    <!-- 固定终端宽度（服务端保存，所有设备共用） -->
    <ExecFixedWidthPref />

    <!-- Agent 自动升级总闸（服务端保存，默认关；主机上的开关在主机本机） -->
    <AgentAutoUpgradePref />

    <!-- 多行粘贴前确认 -->
    <section class="p-3.5 rounded-xs border border-border/70 bg-card/60 space-y-2.5" aria-label="多行粘贴前确认" data-testid="pref-paste-confirm">
      <label class="flex items-center justify-between gap-3 cursor-pointer">
        <span class="flex items-center gap-2 min-w-0">
          <span class="text-xs font-mono font-medium text-foreground">多行粘贴前确认</span>
          <span class="px-1.5 py-0.5 rounded-xs text-[10px] font-mono bg-muted/60 border border-border/70 text-muted-foreground">仅此浏览器</span>
        </span>
        <Switch
          :checked="pasteConfirm.enabled.value"
          aria-label="多行粘贴前确认"
          data-testid="pref-paste-confirm-switch"
          @update:checked="(v: boolean) => pasteConfirm.set(v)"
        />
      </label>
      <p class="text-[10px] leading-relaxed text-muted-foreground">
        终端没有开启括号粘贴模式时，粘贴含换行的内容会被逐行立刻执行；开启后会先弹出预览让你确认（支持括号粘贴的程序，例如 vim、较新的 shell，不受影响）。
        在终端里右键可以「复制」（有选区时）和「粘贴」。Ctrl+V 保持原样，把 ^V 交给程序；粘贴请用右键菜单或 Ctrl+Shift+V（Mac 为 ⌘V）。
      </p>
    </section>

    <!-- 同时保持连接的会话数 -->
    <section class="p-3.5 rounded-xs border border-border/70 bg-card/60 space-y-2.5" aria-label="同时保持连接的会话数" data-testid="pref-live-cap">
      <div class="flex items-center gap-2">
        <div class="text-xs font-mono font-medium text-foreground">同时保持连接的会话数</div>
        <span class="px-1.5 py-0.5 rounded-xs text-[10px] font-mono bg-muted/60 border border-border/70 text-muted-foreground">仅此浏览器</span>
      </div>
      <div class="flex items-center gap-2">
        <button
          type="button"
          class="w-7 h-7 rounded-sm border border-border/80 flex items-center justify-center hover:bg-muted cursor-pointer disabled:opacity-40 disabled:cursor-not-allowed"
          :disabled="exec.liveCap <= LIVE_CAP_MIN"
          aria-label="减少"
          data-testid="pref-live-dec"
          @click="exec.setLiveCap(exec.liveCap - 1)"
        ><Minus class="w-3 h-3" /></button>
        <span class="w-14 text-center text-sm tabular-nums font-medium" aria-live="polite" data-testid="pref-live-value">{{ exec.liveCap }}</span>
        <button
          type="button"
          class="w-7 h-7 rounded-sm border border-border/80 flex items-center justify-center hover:bg-muted cursor-pointer disabled:opacity-40 disabled:cursor-not-allowed"
          :disabled="exec.liveCap >= LIVE_CAP_MAX"
          aria-label="增加"
          data-testid="pref-live-inc"
          @click="exec.setLiveCap(exec.liveCap + 1)"
        ><Plus class="w-3 h-3" /></button>
      </div>
      <p class="text-[10px] leading-relaxed text-muted-foreground">
        只让最近用过的几个会话保持连接，其余的先断开，节省浏览器内存与连接数；会话仍在主机上运行，再点开时接着回放。可选 {{ LIVE_CAP_MIN }}–{{ LIVE_CAP_MAX }}，默认 {{ LIVE_CAP_DEFAULT }}。
      </p>
    </section>
  </div>
</template>
