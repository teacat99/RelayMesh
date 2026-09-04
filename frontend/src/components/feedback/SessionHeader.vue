<script setup lang="ts">
import { computed } from 'vue'
import { useSessionStore } from '../../stores/session'
import { useSettingsStore } from '../../stores/settings'
import Badge from '../ui/badge/Badge.vue'
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem
} from '../ui/dropdown-menu'
import {
  Sparkles,
  FolderGit2,
  Workflow,
  Clock,
  Check,
  Menu,
  Copy,
  PanelLeftOpen
} from 'lucide-vue-next'
import { toast } from 'vue-sonner'

const props = defineProps<{
  timerDisplayInfo: {
    text: string
    prefix: string
    isCountdown: boolean
  }
  sidebarCollapsed?: boolean
}>()

const emit = defineEmits<{
  (e: 'open-mobile-sidebar'): void
  (e: 'expand-sidebar'): void
}>()

const sessionStore = useSessionStore()
const settingsStore = useSettingsStore()

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

const displayProjectDirectory = computed(() => {
  const sess = sessionStore.selectedSession || sessionStore.currentSession
  if (!sess?.project_directory) return ''
  const host = sess.host_name || settingsStore.settings.hostName
  if (host && host.trim()) {
    return `${host.trim()}:${sess.project_directory}`
  }
  return sess.project_directory
})

function formatMaxChecks(val?: number): string {
  if (val === undefined || val === null) return '24'
  if (val === 0 || val < 0) return '∞'
  return String(val)
}

const activeSession = computed(() => sessionStore.selectedSession || sessionStore.currentSession)

const currentCountdownMinutes = computed(() => {
  const sess = activeSession.value
  if (sess?.wait_countdown_minutes !== undefined && sess?.wait_countdown_minutes !== null) {
    return sess.wait_countdown_minutes
  }
  return settingsStore.settings.defaultWaitCountdownMinutes ?? 2
})

async function handleWaitCountdownChange(val: number) {
  settingsStore.updateSettings({ defaultWaitCountdownMinutes: val })
  const targetId = activeSession.value?.session_id
  if (targetId) {
    await sessionStore.updateWaitCountdown(targetId, val)
  }
}

async function handleMaxChecksChange(val: number) {
  const targetId = activeSession.value?.session_id
  settingsStore.updateSettings({ maxNoFeedbackChecks: val })
  if (targetId) {
    await sessionStore.updateMaxChecks(targetId, val)
  }
}

async function handlePromptWaitChange(val: number) {
  const targetId = activeSession.value?.session_id
  settingsStore.updateSettings({ promptWaitMinutes: val })
  if (targetId) {
    await sessionStore.updatePromptWait(targetId, val)
  }
}

function getStatusBadge(status: string) {
  switch (status) {
    case 'completed': return { label: '已完成', variant: 'default' }
    case 'pending': return { label: '等待确认', variant: 'secondary' }
    case 'cancelled': return { label: '已取消', variant: 'outline' }
    case 'timeout': return { label: '已超时', variant: 'destructive' }
    case 'archived': return { label: '已归档', variant: 'outline' }
    default: return { label: status, variant: 'outline' }
  }
}
</script>

<template>
  <!-- Top Workspace Header (高度 h-14 与左侧侧边栏保持严格一致) -->
  <header class="h-14 px-3 sm:px-6 border-b border-border/60 flex items-center justify-between gap-2 sm:gap-3 bg-background/80 backdrop-blur-xs shrink-0 z-20">
    <div class="flex items-center gap-2 min-w-0">
      <!-- PC Collapsed Sidebar Expand Button (PC 端侧边栏展开按钮) -->
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
          <h2 class="text-xs sm:text-sm font-semibold text-foreground tracking-tight truncate max-w-[140px] sm:max-w-xs md:max-w-md">
            {{ sessionStore.selectedSession?.title || sessionStore.currentSession?.title || '人机交互方案会话流' }}
          </h2>
          <Badge
            v-if="sessionStore.selectedSession"
            :variant="getStatusBadge(sessionStore.selectedSession.status).variant as any"
            class="text-[10px] sm:text-xs font-normal rounded-xs shrink-0"
          >
            {{ getStatusBadge(sessionStore.selectedSession.status).label }}
          </Badge>
        </div>
        <div class="flex items-center gap-1.5 sm:gap-2 text-[10px] sm:text-xs text-muted-foreground min-w-0">
          <!-- 1. 工作区路径（有空间时自适应展开，支持主机名前缀与点击一键复制） -->
          <button
            v-if="displayProjectDirectory"
            type="button"
            class="hidden sm:inline-flex items-center gap-1 font-mono text-[10px] sm:text-[11px] text-muted-foreground hover:text-foreground hover:bg-muted/70 px-1.5 py-0.5 rounded cursor-pointer transition-colors max-w-xs md:max-w-md lg:max-w-lg truncate group"
            :title="`点击复制工作区路径: ${displayProjectDirectory}`"
            @click="copyText(displayProjectDirectory, '工作区路径')"
          >
            <FolderGit2 class="w-3 h-3 text-muted-foreground shrink-0 group-hover:text-primary transition-colors" />
            <span class="truncate">{{ displayProjectDirectory }}</span>
          </button>

          <!-- 2. Workflow ID（点击直接复制） -->
          <button
            v-if="sessionStore.selectedSession?.workflow_id"
            type="button"
            class="hidden md:inline-flex items-center gap-1 font-mono text-[10px] sm:text-[11px] text-muted-foreground hover:text-foreground hover:bg-muted/70 px-1.5 py-0.5 rounded cursor-pointer transition-colors shrink-0 group"
            :title="`点击复制 Workflow ID: ${sessionStore.selectedSession.workflow_id}`"
            @click="copyText(sessionStore.selectedSession.workflow_id, 'Workflow ID')"
          >
            <Workflow class="w-3 h-3 text-muted-foreground shrink-0 group-hover:text-primary transition-colors" />
            <span>{{ sessionStore.selectedSession.workflow_id }}</span>
          </button>

          <!-- 3. Session ID（点击直接复制） -->
          <button
            v-if="sessionStore.selectedSession?.session_id"
            type="button"
            class="inline-flex items-center gap-1 font-mono text-[10px] sm:text-[11px] bg-muted/80 hover:bg-accent hover:text-accent-foreground px-1.5 py-0.5 rounded cursor-pointer transition-colors shrink-0 group"
            :title="`点击复制会话 ID: ${sessionStore.selectedSession.session_id}`"
            @click="copyText(sessionStore.selectedSession.session_id, '会话 ID')"
          >
            <Copy class="w-2.5 h-2.5 text-muted-foreground group-hover:text-primary transition-colors" />
            <span>{{ sessionStore.selectedSession.session_id }}</span>
          </button>
        </div>
      </div>
    </div>

    <!-- Session Status Controls & Time Display (执行/等待/剩余时间 · 空回执次数 · 提示词等待时长) -->
    <div class="flex items-center gap-1.5 sm:gap-2 shrink-0">
      <!-- 统一计时与倒计时时长选择控件 (无论 pending 还是 completed/执行中状态，均保持同一可交互 DropdownMenu 组件) -->
      <DropdownMenu v-if="activeSession">
        <DropdownMenuTrigger as-child>
          <button
            type="button"
            class="inline-flex items-center gap-1 px-2.5 py-1 rounded-sm border text-[10px] sm:text-xs font-mono shrink-0 shadow-xs transition-colors cursor-pointer outline-none focus-visible:ring-1 focus-visible:ring-ring"
            :class="sessionStore.currentSession && sessionStore.currentSession.status === 'pending'
              ? 'bg-muted/60 hover:bg-muted/90 border-border'
              : 'bg-muted/40 hover:bg-muted/70 border-border/80'"
            title="点击切换默认倒计时时长 (0m / 1m / 2m)"
          >
            <Clock class="w-3 sm:w-3.5 h-3 sm:h-3.5 text-muted-foreground" />
            <span class="text-muted-foreground hidden sm:inline">{{ props.timerDisplayInfo.prefix }}:</span>
            <span
              class="font-medium"
              :class="props.timerDisplayInfo.isCountdown ? 'text-destructive' : 'text-foreground'"
            >
              {{ props.timerDisplayInfo.text }}
            </span>
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start" class="w-24 p-1 font-mono text-xs shadow-modal border border-border bg-popover">
          <DropdownMenuItem
            v-for="val in [0, 1, 2]"
            :key="val"
            class="flex items-center justify-between cursor-pointer rounded-xs px-2 py-1.5 text-xs font-mono"
            :class="currentCountdownMinutes === val ? 'bg-accent font-bold text-accent-foreground' : ''"
            @click="handleWaitCountdownChange(val)"
          >
            <span>{{ val }}m</span>
            <Check v-if="currentCountdownMinutes === val" class="w-3.5 h-3.5 text-primary shrink-0" />
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>

      <!-- 独立控件 2: 空回执次数下拉选择 (6, 12, 24, 36, ∞) -->
      <DropdownMenu v-if="activeSession">
        <DropdownMenuTrigger as-child>
          <button
            type="button"
            class="inline-flex items-center gap-1 px-2.5 py-1 rounded-sm border text-[10px] sm:text-xs font-mono shrink-0 shadow-xs transition-colors cursor-pointer outline-none focus-visible:ring-1 focus-visible:ring-ring"
            :class="sessionStore.currentSession && sessionStore.currentSession.status === 'pending'
              ? 'bg-muted/60 hover:bg-muted/90 border-border'
              : 'bg-muted/40 hover:bg-muted/60 border-border'"
          >
            <span class="text-muted-foreground hidden sm:inline">空回执:</span>
            <span class="font-medium text-foreground">
              {{ activeSession.no_feedback_checks || 0 }}/{{ formatMaxChecks(activeSession.max_no_feedback_checks ?? settingsStore.settings.maxNoFeedbackChecks) }}
            </span>
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start" class="w-28 p-1 font-mono text-xs shadow-modal border border-border bg-popover">
          <DropdownMenuItem
            v-for="opt in [
              { label: '6 次', value: 6 },
              { label: '12 次', value: 12 },
              { label: '24 次', value: 24 },
              { label: '36 次', value: 36 },
              { label: '∞ 无限', value: 0 }
            ]"
            :key="opt.value"
            class="flex items-center justify-between cursor-pointer rounded-xs px-2 py-1.5 text-xs font-mono"
            :class="(activeSession.max_no_feedback_checks ?? settingsStore.settings.maxNoFeedbackChecks ?? 24) === opt.value ? 'bg-accent font-bold text-accent-foreground' : ''"
            @click="handleMaxChecksChange(opt.value)"
          >
            <span>{{ opt.label }}</span>
            <Check v-if="(activeSession.max_no_feedback_checks ?? settingsStore.settings.maxNoFeedbackChecks ?? 24) === opt.value" class="w-3.5 h-3.5 text-primary shrink-0" />
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>

      <!-- 独立控件 3: 提示词等待时长下拉选择 (1m, 2m, 5m, 10m) -->
      <DropdownMenu v-if="activeSession">
        <DropdownMenuTrigger as-child>
          <button
            type="button"
            class="inline-flex items-center gap-1 px-2.5 py-1 rounded-sm border text-[10px] sm:text-xs font-mono shrink-0 shadow-xs transition-colors cursor-pointer outline-none focus-visible:ring-1 focus-visible:ring-ring"
            :class="sessionStore.currentSession && sessionStore.currentSession.status === 'pending'
              ? 'bg-muted/60 hover:bg-muted/90 border-border'
              : 'bg-muted/40 hover:bg-muted/60 border-border'"
          >
            <span class="text-muted-foreground">等待:</span>
            <span class="font-medium text-foreground">{{ activeSession.prompt_wait_minutes || settingsStore.settings.promptWaitMinutes || 5 }}m</span>
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start" class="w-24 p-1 font-mono text-xs shadow-modal border border-border bg-popover">
          <DropdownMenuItem
            v-for="m in [1, 2, 5, 10]"
            :key="m"
            class="flex items-center justify-between cursor-pointer rounded-xs px-2 py-1.5 text-xs font-mono"
            :class="(activeSession.prompt_wait_minutes || settingsStore.settings.promptWaitMinutes || 5) === m ? 'bg-accent font-bold text-accent-foreground' : ''"
            @click="handlePromptWaitChange(m)"
          >
            <span>{{ m }}m</span>
            <Check v-if="(activeSession.prompt_wait_minutes || settingsStore.settings.promptWaitMinutes || 5) === m" class="w-3.5 h-3.5 text-primary shrink-0" />
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  </header>
</template>
