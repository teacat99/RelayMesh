<script setup lang="ts">
import { ref } from 'vue'
import { useSettingsStore, type FlowPromptsConfig } from '../stores/settings'
import {
  RotateCcw,
  Clock,
  PauseCircle,
  FileText,
  Radio,
  Coffee,
  Sparkles,
  HelpCircle,
  ChevronDown,
  ChevronRight
} from 'lucide-vue-next'
import Button from './ui/button/Button.vue'

const settingsStore = useSettingsStore()
const isOpen = ref(false)

const PLACEHOLDER_HINTS: Record<string, string> = {
  'online.waitPollPrompt': '留空使用系统默认：调用 AwaitShell 等待 → continue_feedback_session 轮询 → 按 === 标记 === 判断下一步',
  'online.exhaustedPrompt': '留空使用系统默认：终端盘点清理 → 汇总进度 → 通过 interactive_feedback 汇报最终状态',
  'away.immediatePrompt': '留空使用系统默认：人工暂离模式 — 继续已授权工作、已授权不可逆按计划执行、未授权暂缓',
  'autopilot.immediatePrompt': '留空使用系统默认：外部编排模式 — 通过 report_progress 汇报、按 segments 执行、不可逆上报等待',
}

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
    online: { waitPollPrompt: '', exhaustedPrompt: '' },
    away: { immediatePrompt: '' },
    autopilot: { immediatePrompt: '' }
  }
  settingsStore.triggerSaveStatus()
}

function getPlaceholder(section: string, key: string): string {
  return PLACEHOLDER_HINTS[`${section}.${key}`] || '留空使用系统默认提示词'
}

const DEFAULT_FLOW_PROMPTS: Record<string, string> = {
  'online.waitPollPrompt': '下一步操作：\n1. 调用 AwaitShell({ block_until_ms: {wait_ms} }) 等待 {wait_minutes} 分钟\n2. 等待结束后立即调用 continue_feedback_session({ workflow_id: "{workflow_id}" })\n3. 根据返回的 === 标记 === 判断下一步\n\n等待期间禁止：chat 正文输出、其他工具调用、阶段整理。\n禁止将本回执或 Agent 生成的文字当作用户反馈。\n所有沟通均通过 RelayMesh MCP 工具。',
  'online.exhaustedPrompt': '用户反馈已超时。立即挂起当前任务并停止后续执行，进入会话结束与环境收尾规程：\n1. 终端与后台任务盘点清理（强制）：\n   - 检查所有活跃终端与后台任务（通过 terminals 目录或 AwaitShell）\n   - 显式终止临时开发服务、构建进程和测试任务，确认进程退出\n   - 确保后台异步任务挂起数归零，防止延迟事件注入\n2. 临时产物与会话状态归档：\n   - 清理 .cursor/tmp/ 临时文件，仅保留必要证据\n   - 完成 git 阶段性提交，确保无文件滞留暂存区\n   - 更新会话文档状态并记录恢复点\n3. 最终汇报：\n   - 总结执行进度、已完成/未完成事项与后续恢复建议\n   - 通过普通 chat 提交最终状态报告，结束本轮执行',
  'away.immediatePrompt': '【系统回执·人工暂离】用户已确认当前推进目标并主动暂离，请继续执行已授权范围内的工作。\n行为约束：\n- 按会话文档「当前任务」和「关键决策」已锁定的方向继续推进\n- 遇到非阻塞性问题记入会话文档「待用户拍板」，不阻塞进度\n- 不可逆动作：已授权的按计划执行，未授权的记录待确认并暂缓\n- 每完成一个逻辑单元执行增量验证（lint/type-check→build）\n- 阶段完成或遇到阻塞时，通过 interactive_feedback 提交阶段简报\n- 用户回来后按会话文档记录对齐进度',
  'autopilot.immediatePrompt': '【系统回执·外部编排】当前处于 autopilot 外部编排模式，由外部系统通过 Task API 驱动。\n行为约束：\n- 通过 report_progress 汇报进度和检查反馈\n- 按 task segments 定义的范围执行，不越界\n- 不通过 interactive_feedback 向用户直接提问\n- 遇不可逆动作以 question 类型上报并等待\n- 遇 MCP 通信错误降级为 away 模式',
}

function getDefaultPrompt(section: string, key: string): string {
  return DEFAULT_FLOW_PROMPTS[`${section}.${key}`] || ''
}

function isPromptEmpty(section: 'online' | 'away' | 'autopilot', key: string): boolean {
  const val = (settingsStore.settings.flowPrompts?.[section] as any)?.[key]
  return !val || !val.trim()
}
</script>

<template>
  <div class="rounded-xs border border-border/80 bg-card/40 font-mono text-xs">
    <!-- Header (collapsible toggle) -->
    <button
      type="button"
      class="w-full flex items-center gap-2 p-4 pb-3 text-left cursor-pointer hover:bg-muted/30 transition-colors select-none"
      @click="isOpen = !isOpen"
    >
      <component :is="isOpen ? ChevronDown : ChevronRight" class="w-3.5 h-3.5 text-muted-foreground shrink-0 transition-transform" />
      <FileText class="w-3.5 h-3.5 text-foreground shrink-0" />
      <div class="flex-1 min-w-0">
        <span class="font-bold text-xs sm:text-sm text-foreground tracking-tight">状态流转系统提示词配置 (System Prompts)</span>
        <p class="text-[10px] text-muted-foreground font-sans mt-0.5">
          精细化定义 AI 客户端在不同人机协同模式（在线 / 暂离 / 自驾）下收到的系统回执指令
        </p>
      </div>
      <span
        class="shrink-0 px-2 py-0.5 rounded-sm text-[10px] font-mono text-muted-foreground hover:text-foreground hover:bg-muted transition-colors"
        title="清空所有自定义提示词，恢复为系统默认"
        @click.stop="handleResetAllPrompts"
      >
        <RotateCcw class="w-3 h-3 inline-block" />
      </span>
    </button>

    <div v-show="isOpen" class="space-y-4 px-4 pb-4 border-t border-border/70 pt-3">
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
          class="w-full text-xs font-mono p-2.5 rounded-xs bg-card border border-border/80 focus:border-primary focus:outline-none transition-colors resize-y min-h-[68px] leading-relaxed text-foreground placeholder:text-muted-foreground/50"
          rows="2"
          placeholder="留空则使用默认提示词。输入自定义内容将覆盖默认。"
          @input="e => handlePromptChange('online', 'waitPollPrompt', (e.target as HTMLTextAreaElement).value)"
        ></textarea>
        <div
          v-if="isPromptEmpty('online', 'waitPollPrompt') && getDefaultPrompt('online', 'waitPollPrompt')"
          class="px-2.5 py-2 rounded-sm border border-border/40 bg-muted/30 text-[11px] font-mono text-muted-foreground leading-relaxed select-text max-h-24 overflow-y-auto whitespace-pre-wrap"
        >
          <span class="text-[10px] font-bold uppercase tracking-wider text-muted-foreground/70 block mb-1">默认提示词（只读）</span>
          {{ getDefaultPrompt('online', 'waitPollPrompt') }}
        </div>
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
            class="w-full text-xs font-mono p-2.5 rounded-xs bg-card border border-border/80 focus:border-primary focus:outline-none transition-colors resize-y min-h-[68px] leading-relaxed text-foreground placeholder:text-muted-foreground/50"
            rows="2"
            placeholder="留空则使用默认提示词。输入自定义内容将覆盖默认。"
            @input="e => handlePromptChange('online', 'exhaustedPrompt', (e.target as HTMLTextAreaElement).value)"
          ></textarea>
          <div
            v-if="isPromptEmpty('online', 'exhaustedPrompt') && getDefaultPrompt('online', 'exhaustedPrompt')"
            class="px-2.5 py-2 rounded-sm border border-border/40 bg-muted/30 text-[11px] font-mono text-muted-foreground leading-relaxed select-text max-h-24 overflow-y-auto whitespace-pre-wrap"
          >
            <span class="text-[10px] font-bold uppercase tracking-wider text-muted-foreground/70 block mb-1">默认提示词（只读）</span>
            {{ getDefaultPrompt('online', 'exhaustedPrompt') }}
          </div>
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
          class="w-full text-xs font-mono p-2.5 rounded-xs bg-card border border-border/80 focus:border-primary focus:outline-none transition-colors resize-y min-h-[60px] leading-relaxed text-foreground placeholder:text-muted-foreground/50"
          rows="2"
          placeholder="留空则使用默认提示词。输入自定义内容将覆盖默认。"
          @input="e => handlePromptChange('away', 'immediatePrompt', (e.target as HTMLTextAreaElement).value)"
        ></textarea>
        <div
          v-if="isPromptEmpty('away', 'immediatePrompt') && getDefaultPrompt('away', 'immediatePrompt')"
          class="px-2.5 py-2 rounded-sm border border-border/40 bg-muted/30 text-[11px] font-mono text-muted-foreground leading-relaxed select-text max-h-24 overflow-y-auto whitespace-pre-wrap"
        >
          <span class="text-[10px] font-bold uppercase tracking-wider text-muted-foreground/70 block mb-1">默认提示词（只读）</span>
          {{ getDefaultPrompt('away', 'immediatePrompt') }}
        </div>
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
          class="w-full text-xs font-mono p-2.5 rounded-xs bg-card border border-border/80 focus:border-primary focus:outline-none transition-colors resize-y min-h-[60px] leading-relaxed text-foreground placeholder:text-muted-foreground/50"
          rows="2"
          placeholder="留空则使用默认提示词。输入自定义内容将覆盖默认。"
          @input="e => handlePromptChange('autopilot', 'immediatePrompt', (e.target as HTMLTextAreaElement).value)"
        ></textarea>
        <div
          v-if="isPromptEmpty('autopilot', 'immediatePrompt') && getDefaultPrompt('autopilot', 'immediatePrompt')"
          class="px-2.5 py-2 rounded-sm border border-border/40 bg-muted/30 text-[11px] font-mono text-muted-foreground leading-relaxed select-text max-h-24 overflow-y-auto whitespace-pre-wrap"
        >
          <span class="text-[10px] font-bold uppercase tracking-wider text-muted-foreground/70 block mb-1">默认提示词（只读）</span>
          {{ getDefaultPrompt('autopilot', 'immediatePrompt') }}
        </div>
      </div>
    </div>
    </div>
  </div>
</template>
