<script setup lang="ts">
import { ref, computed } from 'vue'
import { useSettingsStore } from '../stores/settings'
import {
  Clock,
  CheckCircle2,
  Split,
  CircleDot,
  ChevronDown,
  ChevronRight
} from 'lucide-vue-next'

const settingsStore = useSettingsStore()

const isOpen = ref(false)

const promptWaitMinutes = computed(() => settingsStore.settings.promptWaitMinutes || 2)
const countdownMinutes = computed(() => settingsStore.settings.defaultWaitCountdownMinutes ?? 2)
const maxChecks = computed(() => settingsStore.settings.maxNoFeedbackChecks ?? 24)
const totalHours = computed(() => {
  if (maxChecks.value === 0) return '∞'
  return ((maxChecks.value * promptWaitMinutes.value) / 60).toFixed(1)
})
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
      <CircleDot class="w-3.5 h-3.5 text-foreground shrink-0" />
      <div class="flex-1 min-w-0">
        <span class="font-bold text-xs sm:text-sm text-foreground tracking-tight">状态流转与生命周期矩阵</span>
        <span class="text-[10px] text-muted-foreground font-normal ml-1.5">/ Lifecycle Matrix</span>
        <p class="text-[10px] text-muted-foreground font-sans mt-0.5">
          SSE 实时事件 + MCP v1.0 · 在线值守的等待与超时流转
        </p>
      </div>
    </button>

    <div v-show="isOpen" class="space-y-4 px-4 pb-4 border-t border-border/70 pt-3">
    <!-- ==================== 在线值守的等待与超时流转 纯线框矩阵 ==================== -->
    <div class="space-y-3.5 pt-0.5">
      <!-- 极简四列参数指示微格 (去色纯正线框) -->
      <div class="grid grid-cols-2 sm:grid-cols-4 gap-2 text-[11px] font-mono">
        <div class="p-2 rounded-xs border border-border/70 bg-background flex flex-col justify-between">
          <span class="text-[10px] text-muted-foreground">空回执休眠</span>
          <span class="font-bold text-foreground text-xs">{{ promptWaitMinutes }} 分钟 / 轮</span>
        </div>
        <div class="p-2 rounded-xs border border-border/70 bg-background flex flex-col justify-between">
          <span class="text-[10px] text-muted-foreground">倒计时时效</span>
          <span class="font-bold text-foreground text-xs">{{ countdownMinutes }} 分钟 (超时转空回执)</span>
        </div>
        <div class="p-2 rounded-xs border border-border/70 bg-background flex flex-col justify-between">
          <span class="text-[10px] text-muted-foreground">最大探测轮次</span>
          <span class="font-bold text-foreground text-xs">{{ maxChecks === 0 ? '∞ 无限循环' : `${maxChecks} 次` }}</span>
        </div>
        <div class="p-2 rounded-xs border border-border/70 bg-background flex flex-col justify-between">
          <span class="text-[10px] text-muted-foreground">最长守护上限</span>
          <span class="font-bold text-foreground text-xs">{{ totalHours === '∞' ? '∞' : `约 ${totalHours} 小时` }}</span>
        </div>
      </div>

      <!-- 节点流转线框列表 -->
      <div class="space-y-2.5">
        <!-- 阶段 01: 请求发起与阻塞挂起 -->
        <div class="p-3 rounded-xs border border-border/80 bg-background space-y-1.5">
          <div class="flex items-center justify-between">
            <div class="flex items-center gap-2">
              <span class="px-1.5 py-0.2 rounded-2xs bg-primary text-primary-foreground text-[10px] font-mono font-bold">
                01
              </span>
              <span class="font-bold text-foreground text-xs">
                AI 发起交互请求 ➔ 状态进入 pending 阻塞挂起
              </span>
            </div>
            <span class="text-[9px] font-mono px-1.5 py-0.2 rounded-2xs bg-muted border border-border/70 text-muted-foreground">
              interactive_feedback
            </span>
          </div>
          <p class="text-[11px] text-muted-foreground font-sans leading-relaxed">
            AI 客户端发起方案汇报并进入长轮询阻塞挂起（服务端上限 40s）；SSE 实时广播唤醒 Web 界面，倒计时（{{ countdownMinutes }}m）同步开始。
          </p>
        </div>

        <!-- 阶段 02: 决策分流枢纽 (黑白线框对比) -->
        <div class="space-y-1.5">
          <div class="text-[10px] font-mono text-muted-foreground px-0.5 flex items-center gap-1.5">
            <Split class="w-3 h-3 text-muted-foreground" />
            <span>STAGE 02 / 用户响应分流分歧</span>
          </div>

          <div class="grid grid-cols-1 md:grid-cols-2 gap-2.5">
            <!-- 主线 A: 人工即时反馈 -->
            <div class="p-3 rounded-xs border border-border/90 bg-background space-y-1.5 flex flex-col justify-between">
              <div class="space-y-1">
                <div class="flex items-center justify-between">
                  <span class="font-bold text-foreground text-xs flex items-center gap-1.5">
                    <CheckCircle2 class="w-3.5 h-3.5 text-foreground" />
                    <span>主线 A: 用户即时反馈 (权威指令)</span>
                  </span>
                  <span class="text-[9px] font-mono px-1.5 py-0.2 rounded-2xs bg-muted border border-border/80 text-foreground font-bold">
                    ➔ completed
                  </span>
                </div>
                <p class="text-[11px] text-muted-foreground font-sans leading-relaxed">
                  用户在 Web 端提交意见或点击快捷预设 ➔ 状态即刻原子转为 <strong class="font-mono text-foreground">completed</strong> ➔ 0 延迟原样直返用户权威输入给 AI ➔ AI 立即唤醒继续编码。
                </p>
              </div>
              <div class="pt-1.5 border-t border-border/60 flex items-center justify-between text-[10px] text-muted-foreground font-sans">
                <span>时效: 0ms 秒回响应</span>
                <span class="font-mono text-foreground font-bold">零延迟直返</span>
              </div>
            </div>

            <!-- 分支 B: 倒计时超时 -->
            <div class="p-3 rounded-xs border border-border/70 bg-background space-y-1.5 flex flex-col justify-between">
              <div class="space-y-1">
                <div class="flex items-center justify-between">
                  <span class="font-bold text-foreground text-xs flex items-center gap-1.5">
                    <Clock class="w-3.5 h-3.5 text-muted-foreground" />
                    <span>分支 B: 倒计时 {{ countdownMinutes }}m 届满未反馈</span>
                  </span>
                  <span class="text-[9px] font-mono px-1.5 py-0.2 rounded-2xs bg-muted border border-border/80 text-muted-foreground">
                    ➔ 探测轮询
                  </span>
                </div>
                <p class="text-[11px] text-muted-foreground font-sans leading-relaxed">
                  倒计时结束后，单次 MCP 挂起达 40s 上限主动返回结构化「=== 等待回执 ===」模板，指令 AI 本地休眠 <strong class="font-mono text-foreground">{{ promptWaitMinutes }} 分钟</strong>。
                </p>
              </div>
              <div class="pt-1.5 border-t border-border/60 flex items-center justify-between text-[10px] text-muted-foreground font-sans">
                <span>时效: 挂起 40s 防超时</span>
                <span class="font-mono text-muted-foreground">结构化等待回执</span>
              </div>
            </div>
          </div>
        </div>

        <!-- 阶段 03: 周期探测与秒回直取 -->
        <div class="p-3 rounded-xs border border-border/80 bg-background space-y-2">
          <div class="flex items-center justify-between">
            <div class="flex items-center gap-2">
              <span class="px-1.5 py-0.2 rounded-2xs bg-primary text-primary-foreground text-[10px] font-mono font-bold">
                03
              </span>
              <span class="font-bold text-foreground text-xs">
                AI 休眠结束 ➔ 调用 continue_feedback_session 周期探测
              </span>
            </div>
            <span class="text-[9px] font-mono px-1.5 py-0.2 rounded-2xs bg-muted border border-border/70 text-muted-foreground">
              continue_feedback_session
            </span>
          </div>

          <div class="grid grid-cols-1 sm:grid-cols-2 gap-2 pt-0.5 font-sans text-[11px]">
            <div class="p-2 rounded-xs border border-border/60 bg-card/60 space-y-0.5">
              <strong class="text-foreground block font-mono text-xs">休眠期间用户已提交</strong>
              <span class="text-muted-foreground">服务端 0ms 秒回直取已持久化的意见，AI 立即唤醒推进。</span>
            </div>
            <div class="p-2 rounded-xs border border-border/60 bg-card/60 space-y-0.5">
              <strong class="text-foreground block font-mono text-xs">仍无用户输入</strong>
              <span class="text-muted-foreground">继续返回等待回执，探测计数器 +1，进入下一轮休眠。</span>
            </div>
          </div>
        </div>

        <!-- 阶段 04: 终态闭环 -->
        <div class="p-3 rounded-xs border border-border/80 bg-background space-y-2">
          <div class="flex items-center justify-between">
            <div class="flex items-center gap-2">
              <span class="px-1.5 py-0.2 rounded-2xs bg-primary text-primary-foreground text-[10px] font-mono font-bold">
                04
              </span>
              <span class="font-bold text-foreground text-xs">
                生命周期终态双向闭环
              </span>
            </div>
            <span class="text-[10px] font-mono text-muted-foreground">
              最长守护: {{ totalHours === '∞' ? '∞' : `${totalHours} 小时` }}
            </span>
          </div>

          <div class="grid grid-cols-1 sm:grid-cols-2 gap-2 pt-0.5 font-sans text-[11px]">
            <div class="p-2 rounded-xs border border-border/60 bg-card/60 space-y-0.5">
              <strong class="text-foreground block font-mono text-xs">正常完成 (Completed)</strong>
              <span class="text-muted-foreground">用户随时提交反馈并闭环，状态转为 completed，会话流程圆满归档。</span>
            </div>
            <div class="p-2 rounded-xs border border-border/60 bg-card/60 space-y-0.5">
              <strong class="text-foreground block font-mono text-xs">达探测上限 (Timeout / Suspend)</strong>
              <span class="text-muted-foreground">达到最大探测次数（{{ maxChecks }} 次），返回「=== 反馈超时 ===」，指令 AI 安全挂起。</span>
            </div>
          </div>
        </div>
      </div>
    </div>
    </div>
  </div>
</template>
