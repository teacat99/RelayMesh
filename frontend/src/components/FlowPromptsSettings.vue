<script setup lang="ts">
import { useSettingsStore, type FlowPromptsConfig } from '../stores/settings'
import {
  RotateCcw,
  Clock,
  PauseCircle,
  FileText,
  Radio,
  Coffee,
  Sparkles,
  HelpCircle
} from 'lucide-vue-next'
import Button from './ui/button/Button.vue'

const settingsStore = useSettingsStore()

function handlePromptChange(section: 'online' | 'away' | 'autopilot', key: string, value: string) {
  if (!settingsStore.settings.flowPrompts) {
    settingsStore.settings.flowPrompts = {
      online: { waitPollPrompt: '', exhaustedPrompt: '' },
      away: { immediatePrompt: '' },
      autopilot: { immediatePrompt: '' }
    }
  }
  if (!settingsStore.settings.flowPrompts[section]) {
    (settingsStore.settings.flowPrompts as any)[section] = {}
  }
  (settingsStore.settings.flowPrompts[section] as any)[key] = value
  settingsStore.triggerSaveStatus()
}

function insertVariable(section: 'online' | 'away' | 'autopilot', key: string, variableName: string) {
  const current = (settingsStore.settings.flowPrompts?.[section] as any)?.[key] || ''
  handlePromptChange(section, key, current + `{${variableName}}`)
}

function handleResetAllPrompts() {
    settingsStore.settings.flowPrompts = {
      online: {
        waitPollPrompt: '严格执行：等待 {wait_minutes} 分钟，期间不得输出、调用工具或执行任何其他操作。等待结束后，第一步调用 continue_feedback_session 工具获取反馈；本指令不得作为循环触发或循环判定条件；循环与否仅由 continue_feedback_session 的返回结果决定。',
        exhaustedPrompt: '用户反馈已超时。立即挂起当前任务，停止后续执行，并终止终端中仍在运行的后台进程。随后总结当前会话的执行进度、已完成事项、未完成事项及后续恢复点；总结完成后结束本轮执行，不再进行其他操作。'
      },
      away: {
        immediatePrompt: '【系统回执·用户暂离】用户当前处于暂离状态。请将非阻塞性问题记录暂存，优先推进已明确授权的开发范围，不可逆动作一律暂缓。'
      },
      autopilot: {
        immediatePrompt: '【系统回执·托管自驾】当前处于 M-C 自驾模式，方案已自动接管授权。请严格按照规划目标推进，如遇不可逆高风险操作（DB迁移/部署/破坏性命令）请立即停下。'
      }
    }
  settingsStore.triggerSaveStatus()
}
</script>

<template>
  <div class="space-y-4 p-4 rounded-xs border border-border/80 bg-card/40 font-mono text-xs">
    <!-- Header -->
    <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-2 border-b border-border/70 pb-3">
      <div class="flex items-center gap-2">
        <FileText class="w-3.5 h-3.5 text-foreground shrink-0" />
        <div>
          <span class="font-bold text-xs sm:text-sm text-foreground tracking-tight">状态流转系统提示词配置 (System Prompts)</span>
          <p class="text-[10px] text-muted-foreground font-sans mt-0.5">
            精细化定义 AI 客户端在不同人机协同模式（在线 / 暂离 / 自驾）下收到的系统回执指令
          </p>
        </div>
      </div>
      <Button
        variant="ghost"
        size="sm"
        class="h-6 text-[10px] font-mono text-muted-foreground hover:text-foreground px-2 flex items-center gap-1 cursor-pointer"
        @click="handleResetAllPrompts"
        title="恢复所有固定流转系统提示词为标准默认值"
      >
        <RotateCcw class="w-3 h-3" />
        <span>恢复标准默认词</span>
      </Button>
    </div>

    <!-- 1. 在线交互模式提示词 (Online Mode) -->
    <div class="space-y-3">
      <div class="flex items-center gap-1.5 text-xs font-bold text-foreground">
        <Radio class="w-3.5 h-3.5 text-emerald-500 shrink-0" />
        <span>01 · 在线模式流转提示词 (Online Prompts)</span>
      </div>

      <!-- 1.1 等待回执提示词 (wait_instruction / 单次探测等待) -->
      <div class="p-3 rounded-xs border border-border/80 bg-background space-y-2">
        <div class="flex flex-wrap items-center justify-between gap-1.5">
          <label class="text-xs font-bold text-foreground flex items-center gap-1.5">
            <Clock class="w-3.5 h-3.5 text-muted-foreground" />
            <span>在线等待轮询提示词 (=== 等待回执 ===)</span>
          </label>
          <div class="flex items-center gap-1">
            <span class="text-[10px] text-muted-foreground">插入变量:</span>
            <button
              type="button"
              class="text-[9px] font-mono px-1.5 py-0.2 rounded-2xs bg-muted text-foreground border border-border/80 hover:bg-muted/80 cursor-pointer"
              @click="insertVariable('online', 'waitPollPrompt', 'wait_minutes')"
              title="点击插入等待分钟数变量"
            >
              + {wait_minutes}
            </button>
          </div>
        </div>

        <textarea
          :value="settingsStore.settings.flowPrompts?.online?.waitPollPrompt"
          class="w-full text-xs font-mono p-2.5 rounded-xs bg-card border border-border/80 focus:border-primary focus:outline-none transition-colors resize-y min-h-[68px] leading-relaxed text-foreground"
          rows="2"
          placeholder="输入在线等待轮询提示词模板..."
          @input="e => handlePromptChange('online', 'waitPollPrompt', (e.target as HTMLTextAreaElement).value)"
        ></textarea>
      </div>

      <!-- 1.2 反馈超时提示词 (exhausted_instruction / 超限挂起) -->
      <div class="p-3 rounded-xs border border-border/80 bg-background space-y-2">
        <div class="flex flex-wrap items-center justify-between gap-1.5">
          <label class="text-xs font-bold text-foreground flex items-center gap-1.5">
            <PauseCircle class="w-3.5 h-3.5 text-muted-foreground" />
            <span>在线超限终态提示词 (=== 反馈超时 ===)</span>
          </label>
        </div>

          <textarea
            :value="settingsStore.settings.flowPrompts?.online?.exhaustedPrompt"
            class="w-full text-xs font-mono p-2.5 rounded-xs bg-card border border-border/80 focus:border-primary focus:outline-none transition-colors resize-y min-h-[68px] leading-relaxed text-foreground"
            rows="2"
            placeholder="输入在线超限终态提示词模板..."
            @input="e => handlePromptChange('online', 'exhaustedPrompt', (e.target as HTMLTextAreaElement).value)"
          ></textarea>
        </div>
      </div>

      <!-- 2. 暂离模式提示词 (Away Mode) -->
    <div class="space-y-3 pt-2 border-t border-border/70">
      <div class="flex items-center gap-1.5 text-xs font-bold text-foreground">
        <Coffee class="w-3.5 h-3.5 text-amber-500 shrink-0" />
        <span>02 · 暂离模式即时分流提示词 (Away Mode Prompt)</span>
      </div>

      <div class="p-3 rounded-xs border border-border/80 bg-background space-y-2">
        <label class="text-xs font-bold text-foreground block">
          暂离即时响应指令（0ms 即时回执，告知 AI 暂存非阻塞问题）
        </label>
        <textarea
          :value="settingsStore.settings.flowPrompts?.away?.immediatePrompt"
          class="w-full text-xs font-mono p-2.5 rounded-xs bg-card border border-border/80 focus:border-primary focus:outline-none transition-colors resize-y min-h-[60px] leading-relaxed text-foreground"
          rows="2"
          placeholder="输入暂离即时分流提示词..."
          @input="e => handlePromptChange('away', 'immediatePrompt', (e.target as HTMLTextAreaElement).value)"
        ></textarea>
      </div>
    </div>

    <!-- 3. 托管自驾模式提示词 (Autopilot Mode) -->
    <div class="space-y-3 pt-2 border-t border-border/70">
      <div class="flex items-center gap-1.5 text-xs font-bold text-foreground">
        <Sparkles class="w-3.5 h-3.5 text-indigo-500 shrink-0" />
        <span>03 · 托管自驾模式接管提示词 (Autopilot Mode Prompt)</span>
      </div>

      <div class="p-3 rounded-xs border border-border/80 bg-background space-y-2">
        <label class="text-xs font-bold text-foreground block">
          自驾接管授权指令（0ms 即时秒回，告知 AI 自动推进且命中硬停点再停下）
        </label>
        <textarea
          :value="settingsStore.settings.flowPrompts?.autopilot?.immediatePrompt"
          class="w-full text-xs font-mono p-2.5 rounded-xs bg-card border border-border/80 focus:border-primary focus:outline-none transition-colors resize-y min-h-[60px] leading-relaxed text-foreground"
          rows="2"
          placeholder="输入托管自驾模式接管提示词..."
          @input="e => handlePromptChange('autopilot', 'immediatePrompt', (e.target as HTMLTextAreaElement).value)"
        ></textarea>
      </div>
    </div>
  </div>
</template>
