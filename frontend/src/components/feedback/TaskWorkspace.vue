<script setup lang="ts">
import { ref, watch, computed, nextTick } from 'vue'
import { useTaskStore } from '../../stores/task'
import { useSettingsStore } from '../../stores/settings'
import AutopilotDashboard from './AutopilotDashboard.vue'
import FeedbackMessageList from './FeedbackMessageList.vue'
import FeedbackInputDock from './FeedbackInputDock.vue'
import type { FeedbackSession, SessionImage } from '../../api/types'
import Badge from '../ui/badge/Badge.vue'
import {
  Sparkles,
  LayoutDashboard,
  MessageSquareCode,
  Menu,
  PanelLeftOpen,
  FolderGit2,
  Workflow,
  Copy,
  Clock,
  Check,
  CheckCircle2,
  RefreshCw
} from 'lucide-vue-next'
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem
} from '../ui/dropdown-menu'
import { toast } from 'vue-sonner'

const props = defineProps<{
  activeItemId: string | null
  sidebarCollapsed?: boolean
}>()

const emit = defineEmits<{
  (e: 'open-mobile-sidebar'): void
  (e: 'expand-sidebar'): void
}>()

const taskStore = useTaskStore()
const settingsStore = useSettingsStore()
const activeTab = ref<'dashboard' | 'conversation'>('dashboard')
const isChatScrolledUp = ref(false)

const messageListRef = ref<InstanceType<typeof FeedbackMessageList> | null>(null)
const inputDockRef = ref<InstanceType<typeof FeedbackInputDock> | null>(null)

async function copyText(text: string, label: string) {
  if (!text) return
  try {
    await navigator.clipboard.writeText(text)
    toast.success(`已复制 ${label}`, {
      description: text,
      duration: 1800
    })
  } catch (_) {
    const input = document.createElement('textarea')
    input.value = text
    document.body.appendChild(input)
    input.select()
    document.execCommand('copy')
    document.body.removeChild(input)
    toast.success(`已复制 ${label}`, {
      description: text,
      duration: 1800
    })
  }
}

// Map task segments, reports, and feedbacks to synthetic FeedbackSession objects
const syntheticConversationRounds = computed<FeedbackSession[]>(() => {
  const t = taskStore.currentTask
  if (!t) return []

  const list: FeedbackSession[] = []

  // 1. Initial Commander Task Planning round
  const segmentsBody = (t.segments && t.segments.length > 0)
    ? t.segments.map(s => `### 📌 ${s.name}\n\n${s.content}`).join('\n\n---\n\n')
    : '暂无规划分段'

  const stagesSummary = (t.stages && t.stages.length > 0)
    ? `\n\n#### 🎯 规划执行阶段\n` + t.stages.map((st, i) => `${i + 1}. **${st.name}** (${st.status})${st.summary ? ' - ' + st.summary : ''}`).join('\n')
    : ''

  list.push({
    session_id: `${t.task_id}-init`,
    workflow_id: t.task_id,
    title: t.title || `工单规划 (Rev ${t.revision})`,
    summary: `【指挥端 Commander】初始化任务规划与执行阶段`,
    status: 'completed',
    user_presence: 'autopilot',
    response_text: `🎖️ 【指挥端规划基准与目标拆解】\n\n${segmentsBody}${stagesSummary}`,
    user_messages: ['指挥端规划'],
    consumed_by_ai: true,
    created_at: t.created_at || t.updated_at || new Date().toISOString(),
    updated_at: t.updated_at || new Date().toISOString()
  })

  // 2. Map reports from Executor
  for (const rep of taskStore.reports) {
    let kindLabel = '阶段进展'
    switch (rep.kind) {
      case 'stage': kindLabel = '阶段里程碑产物'; break
      case 'evidence': kindLabel = '验证与测试证据'; break
      case 'question': kindLabel = '外部阻塞提问'; break
      case 'completion': kindLabel = '完工汇报'; break
      default: kindLabel = '执行进展'; break
    }

    const refText = (rep.references && rep.references.length > 0)
      ? '\n\n**📄 关联代码与文件：**\n' + rep.references.map(r => `- \`${r.path}${r.line ? ':' + r.line : ''}\`${r.description ? ' (' + r.description + ')' : ''}`).join('\n')
      : ''

    list.push({
      session_id: `${t.task_id}-rep-${rep.sequence}`,
      workflow_id: t.task_id,
      title: `执行上报 #${rep.sequence} · ${kindLabel}`,
      summary: `⚙️ 【执行端 Executor】Seq #${rep.sequence} (${kindLabel})`,
      status: 'completed',
      user_presence: 'autopilot',
      response_text: `${rep.body}${refText}`,
      user_messages: [kindLabel, `Seq #${rep.sequence}`],
      consumed_by_ai: rep.sequence <= t.acknowledged_report_sequence,
      created_at: rep.created_at,
      updated_at: rep.created_at
    })
  }

  // 3. Map feedbacks from Human / Commander
  for (const fb of taskStore.feedbacks) {
    const isHuman = fb.source === 'human'
    const authorTag = isHuman ? '👤 【人工最高指令】' : '🎖️ 【指挥端调度反馈】'

    list.push({
      session_id: `${t.task_id}-fb-${fb.sequence}`,
      workflow_id: t.task_id,
      title: `${isHuman ? '人工指令' : '指挥端指令'} #${fb.sequence}`,
      summary: `${authorTag} (Rev ${fb.task_revision})`,
      status: 'completed',
      user_presence: isHuman ? 'online' : 'autopilot',
      response_text: fb.body,
      user_messages: [isHuman ? '人工输入' : '指挥端同步', `Fb #${fb.sequence}`],
      consumed_by_ai: true,
      created_at: fb.created_at,
      updated_at: fb.created_at
    })
  }

  return list.sort((a, b) => new Date(a.created_at).getTime() - new Date(b.created_at).getTime())
})

async function handleDockSubmit(data: { text: string; presets: string[]; images: any[] }) {
  if (!taskStore.currentTask) return
  let finalMsg = data.text.trim()
  if (finalMsg === '' && data.presets.length > 0) {
    finalMsg = data.presets.join('；')
  }
  if (!finalMsg) return

  try {
    await taskStore.sendFeedback(taskStore.currentTask.task_id, finalMsg, 'human')
    toast.success('人工最高指令已定向投递至【指挥端】')
    inputDockRef.value?.resetForm(taskStore.currentTask.task_id)
    nextTick(() => {
      messageListRef.value?.scrollToBottom()
    })
  } catch (err: any) {
    toast.error('发送失败: ' + (err.message || err))
  }
}

async function handleAckTask(seq: number) {
  if (!taskStore.currentTask) return
  await taskStore.ackReports(taskStore.currentTask.task_id, seq)
  toast.success(`已确认至 Seq #${seq}`)
}
</script>

<template>
  <div class="h-full flex flex-col bg-background overflow-hidden relative">
    <!-- Top Workspace Header (与普通会话 Header 保持严格一致的布局与高度) -->
    <header class="h-14 px-3 sm:px-6 border-b border-border/60 flex items-center justify-between gap-2 sm:gap-3 bg-background/80 backdrop-blur-xs shrink-0 z-20">
      <div class="flex items-center gap-2 min-w-0">
        <!-- PC Collapsed Sidebar Expand Button -->
        <button
          v-if="props.sidebarCollapsed"
          type="button"
          class="p-1.5 -ml-1.5 rounded-sm text-muted-foreground hover:text-foreground hover:bg-muted/70 hidden md:inline-flex shrink-0 cursor-pointer border border-border/80 mr-1"
          @click="emit('expand-sidebar')"
          title="展开侧边栏"
        >
          <PanelLeftOpen class="w-4 h-4" />
        </button>

        <!-- Mobile Sidebar Hamburger Toggle -->
        <button
          type="button"
          class="p-1.5 -ml-1.5 rounded-sm text-muted-foreground hover:text-foreground md:hidden shrink-0"
          @click="emit('open-mobile-sidebar')"
          title="展开会话与工单侧边栏"
        >
          <Menu class="w-4 h-4" />
        </button>

        <div class="space-y-0.5 min-w-0">
          <div class="flex items-center gap-2 min-w-0">
            <Sparkles class="w-3.5 h-3.5 text-foreground shrink-0 hidden sm:inline-block" />
            <h2 class="text-xs sm:text-sm font-semibold text-foreground tracking-tight font-mono truncate max-w-[140px] sm:max-w-xs md:max-w-md">
              {{ taskStore.currentTask?.title || taskStore.currentTask?.task_id || props.activeItemId || '托管自驾工单' }}
            </h2>
            <Badge
              v-if="taskStore.currentTask"
              :variant="taskStore.currentTask.state === 'active' ? 'default' : 'outline'"
              class="text-[10px] sm:text-xs font-normal rounded-xs shrink-0 font-mono"
            >
              {{ taskStore.currentTask.state }}
            </Badge>
          </div>

          <div class="flex items-center gap-1.5 sm:gap-2 text-[10px] sm:text-xs text-muted-foreground min-w-0">
            <!-- Task ID (点击复制) -->
            <button
              v-if="taskStore.currentTask?.task_id"
              type="button"
              class="inline-flex items-center gap-1 font-mono text-[10px] sm:text-[11px] bg-muted/80 hover:bg-accent hover:text-accent-foreground px-1.5 py-0.5 rounded cursor-pointer transition-colors shrink-0 group"
              :title="`点击复制工单 ID: ${taskStore.currentTask.task_id}`"
              @click="copyText(taskStore.currentTask.task_id, '工单 ID')"
            >
              <Copy class="w-2.5 h-2.5 text-muted-foreground group-hover:text-primary transition-colors" />
              <span>{{ taskStore.currentTask.task_id }}</span>
            </button>

            <span class="font-mono text-[10px] sm:text-[11px] text-muted-foreground">
              Rev {{ taskStore.currentTask?.revision || 1 }} · Seq {{ taskStore.currentTask?.report_sequence || 0 }} (ACK {{ taskStore.currentTask?.acknowledged_report_sequence || 0 }})
            </span>
          </div>
        </div>
      </div>

      <!-- Right Controls: Tab Switcher & Action buttons -->
      <div class="flex items-center gap-1.5 sm:gap-2 shrink-0">
        <!-- Dual Tab Switcher Pill (进度大盘 / 详细会话) -->
        <div class="inline-flex p-0.5 rounded-md bg-muted/60 border border-border/80 text-xs font-mono">
          <button
            type="button"
            class="px-2.5 py-1 rounded-sm text-[11px] font-medium transition-all flex items-center gap-1.5 cursor-pointer"
            :class="activeTab === 'dashboard'
              ? 'bg-background text-foreground shadow-xs font-semibold'
              : 'text-muted-foreground hover:text-foreground'"
            @click="activeTab = 'dashboard'"
          >
            <LayoutDashboard class="w-3.5 h-3.5" />
            <span>进度大盘</span>
          </button>
          <button
            type="button"
            class="px-2.5 py-1 rounded-sm text-[11px] font-medium transition-all flex items-center gap-1.5 cursor-pointer"
            :class="activeTab === 'conversation'
              ? 'bg-background text-foreground shadow-xs font-semibold'
              : 'text-muted-foreground hover:text-foreground'"
            @click="activeTab = 'conversation'"
          >
            <MessageSquareCode class="w-3.5 h-3.5" />
            <span>详细会话</span>
          </button>
        </div>

        <button
          v-if="taskStore.currentTask && taskStore.currentTask.unread_report_count > 0"
          type="button"
          class="inline-flex items-center gap-1 px-2 sm:px-2.5 py-1 rounded-sm bg-primary text-primary-foreground text-[10px] sm:text-xs font-mono shadow-xs hover:opacity-90 cursor-pointer"
          @click="handleAckTask(taskStore.currentTask.report_sequence)"
        >
          <CheckCircle2 class="w-3.5 h-3.5" />
          <span>ACK Seq {{ taskStore.currentTask.report_sequence }}</span>
        </button>

        <button
          type="button"
          class="inline-flex items-center gap-1 px-2 py-1 rounded-sm border border-border bg-muted/40 hover:bg-muted/70 text-[10px] sm:text-xs font-mono cursor-pointer"
          @click="props.activeItemId && taskStore.fetchTaskDetail(props.activeItemId)"
          :disabled="taskStore.loading"
        >
          <RefreshCw class="w-3.5 h-3.5" :class="{ 'animate-spin': taskStore.loading }" />
          <span class="hidden sm:inline">刷新</span>
        </button>
      </div>
    </header>

    <!-- Main Content Body -->
    <div class="flex-1 flex flex-col min-h-0 relative overflow-hidden">
      <!-- TAB 1: PROGRESS DASHBOARD VIEW -->
      <div v-show="activeTab === 'dashboard'" class="flex-1 overflow-y-auto px-3 sm:px-6 py-4 sm:py-6 no-scrollbar">
        <AutopilotDashboard />
      </div>

      <!-- TAB 2: DETAILED CONVERSATION VIEW (复用普通会话标准消息流) -->
      <div v-show="activeTab === 'conversation'" class="flex-1 flex flex-col min-h-0 relative">
        <FeedbackMessageList
          ref="messageListRef"
          :conversation-rounds="syntheticConversationRounds"
          :has-draft-images="false"
          @scroll-state-change="(scrolled) => isChatScrolledUp = scrolled"
        />
      </div>
    </div>

    <!-- Bottom Submission Dock (常驻输入区，向指挥端定向发送人工最高指令) -->
    <FeedbackInputDock
      ref="inputDockRef"
      :is-scrolled-up="isChatScrolledUp"
      :is-submitting="taskStore.loading"
      :placeholder="'向指挥端投递人工最高指令 · 自动提升最高优先级 (Ctrl+Enter 发送)...'"
      :button-text="'发送最高指令'"
      @submit="handleDockSubmit"
      @scroll-to-bottom="messageListRef?.scrollToBottom()"
    />
  </div>
</template>

