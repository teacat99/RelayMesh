<script setup lang="ts">
import { ref, watch } from 'vue'
import { useTaskStore } from '../../stores/task'
import MarkdownRenderer from '../MarkdownRenderer.vue'
import Button from '../ui/button/Button.vue'
import Badge from '../ui/badge/Badge.vue'
import Card from '../ui/card/Card.vue'
import CardHeader from '../ui/card/CardHeader.vue'
import CardTitle from '../ui/card/CardTitle.vue'
import CardContent from '../ui/card/CardContent.vue'
import {
  Bot,
  CheckCircle2,
  RefreshCw,
  Info,
  FileCode,
  TrendingUp,
  Send,
  Menu,
  PanelLeftOpen
} from 'lucide-vue-next'

const props = defineProps<{
  activeItemId: string | null
  sidebarCollapsed?: boolean
}>()

const emit = defineEmits<{
  (e: 'open-mobile-sidebar'): void
  (e: 'expand-sidebar'): void
}>()

const taskStore = useTaskStore()
const activeSegmentTab = ref<string>('')
const taskFeedbackBody = ref('')

watch(() => taskStore.currentTask?.segments, (segments) => {
  if (segments && segments.length > 0 && !activeSegmentTab.value) {
    activeSegmentTab.value = segments[0].name
  }
}, { immediate: true })

async function handleAckTask(seq: number) {
  if (!taskStore.currentTask) return
  await taskStore.ackReports(taskStore.currentTask.task_id, seq)
}

async function handleSendTaskFeedback() {
  if (!taskStore.currentTask || !taskFeedbackBody.value.trim()) return
  await taskStore.sendFeedback(taskStore.currentTask.task_id, taskFeedbackBody.value)
  taskFeedbackBody.value = ''
}

function getReportKindBadge(kind: string) {
  switch (kind) {
    case 'progress': return { label: '进展', variant: 'secondary' }
    case 'stage': return { label: '阶段产物', variant: 'default' }
    case 'evidence': return { label: '证据', variant: 'outline' }
    case 'question': return { label: '外部问题', variant: 'destructive' }
    case 'completion': return { label: '完工汇报', variant: 'default' }
    default: return { label: kind, variant: 'outline' }
  }
}
</script>

<template>
  <div class="h-full flex flex-col bg-background overflow-hidden relative">
    <!-- Top Workspace Header (高度 h-14 与左侧侧边栏保持严格一致) -->
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
            <Bot class="w-4 h-4 text-foreground shrink-0 hidden sm:inline-block" />
            <h2 class="text-xs sm:text-sm font-semibold text-foreground tracking-tight font-mono truncate max-w-[140px] sm:max-w-xs md:max-w-md">
              {{ taskStore.currentTask?.task_id || props.activeItemId || '自动化任务工单' }}
            </h2>
            <Badge
              v-if="taskStore.currentTask"
              :variant="taskStore.currentTask.state === 'active' ? 'default' : 'outline'"
              class="text-[10px] sm:text-xs font-normal rounded-xs shrink-0"
            >
              {{ taskStore.currentTask.state }}
            </Badge>
          </div>
          <div v-if="taskStore.currentTask" class="flex flex-wrap items-center gap-2 sm:gap-3 text-muted-foreground font-mono text-[10px] sm:text-[11px]">
            <span>Rev: {{ taskStore.currentTask.revision }}</span>
            <span>·</span>
            <span>Seq: {{ taskStore.currentTask.report_sequence }}</span>
            <span class="hidden sm:inline">·</span>
            <span class="hidden sm:inline">ACK: {{ taskStore.currentTask.acknowledged_report_sequence }}</span>
          </div>
        </div>
      </div>

      <div class="flex items-center gap-1.5 sm:gap-2 shrink-0">
        <Button
          v-if="taskStore.currentTask && taskStore.currentTask.unread_report_count > 0"
          variant="default"
          size="sm"
          class="h-6 sm:h-7 px-2 text-[10px] sm:text-xs rounded-sm gap-1"
          @click="handleAckTask(taskStore.currentTask.report_sequence)"
        >
          <CheckCircle2 class="w-3.5 h-3.5" />
          <span>ACK Seq {{ taskStore.currentTask.report_sequence }}</span>
        </Button>
        <Button
          variant="outline"
          size="sm"
          class="h-6 sm:h-7 px-2 text-[10px] sm:text-xs rounded-sm"
          @click="props.activeItemId && taskStore.fetchTaskDetail(props.activeItemId)"
          :disabled="taskStore.loading"
        >
          <RefreshCw class="w-3.5 h-3.5 sm:mr-1" :class="{ 'animate-spin': taskStore.loading }" />
          <span class="hidden sm:inline">刷新</span>
        </Button>
      </div>
    </header>

    <!-- Main Task Scroll Area -->
    <div class="flex-1 overflow-y-auto px-6 py-6 space-y-6 max-w-5xl mx-auto w-full pb-8">
      <!-- Guide banner -->
      <div class="bg-card border border-border/80 rounded-md p-4 shadow-2xs space-y-3">
        <div class="flex items-center justify-between">
          <div class="flex items-center gap-2 text-xs font-semibold text-foreground">
            <Info class="w-3.5 h-3.5 text-foreground" />
            <span>TWH Lite 协议自动化流转机制（外部 Master 与 Worker 协作）</span>
          </div>
        </div>
        <p class="text-xs text-muted-foreground leading-relaxed">
          外部主控通过 <code class="text-foreground font-mono bg-muted px-1 py-0.5 rounded-sm">configure_task</code> 分段下发工作规范，Worker Agent 通过 <code class="text-foreground font-mono bg-muted px-1 py-0.5 rounded-sm">report_progress</code> 流式上报阶段产物与证据，主控实时对账并下发指导指令。
        </p>
      </div>

      <!-- Segments Card (Master Input) -->
      <Card class="border-border shadow-2xs rounded-md">
        <CardHeader class="pb-3 border-b border-border/50 bg-muted/20">
          <div class="flex items-center justify-between">
            <CardTitle class="text-sm font-semibold flex items-center gap-2">
              <FileCode class="w-4 h-4 text-foreground" />
              【主控下发】分段工单与规范 (Segments)
            </CardTitle>
            <div v-if="taskStore.currentTask?.segments" class="flex gap-1.5">
              <button
                v-for="seg in taskStore.currentTask.segments"
                :key="seg.name"
                type="button"
                class="text-xs px-2.5 py-1 rounded-sm border transition-all"
                :class="activeSegmentTab === seg.name
                  ? 'bg-foreground text-background border-foreground font-medium'
                  : 'bg-muted/40 hover:bg-muted border-border text-muted-foreground'"
                @click="activeSegmentTab = seg.name"
              >
                {{ seg.name }}
              </button>
            </div>
          </div>
        </CardHeader>
        <CardContent class="pt-4 max-h-[260px] overflow-y-auto">
          <div v-if="!taskStore.currentTask?.segments || taskStore.currentTask.segments.length === 0" class="text-xs text-muted-foreground py-4 text-center">
            暂无工单分段。
          </div>
          <div v-for="seg in taskStore.currentTask?.segments" :key="seg.name">
            <MarkdownRenderer v-if="activeSegmentTab === seg.name" :content="seg.content" />
          </div>
        </CardContent>
      </Card>

      <!-- Reports Timeline (Worker Output) -->
      <Card class="border-border shadow-2xs rounded-md">
        <CardHeader class="pb-3 border-b border-border/50 flex flex-row items-center justify-between">
          <div class="space-y-0.5">
            <CardTitle class="text-sm font-semibold flex items-center gap-2">
              <TrendingUp class="w-4 h-4 text-foreground" />
              【执行端上报】阶段报告与证据流 (Reports)
            </CardTitle>
            <div v-if="taskStore.currentTask" class="text-[11px] text-muted-foreground font-mono">
              当前游标：Seq {{ taskStore.currentTask.report_sequence }} / ACK 游标：Seq {{ taskStore.currentTask.acknowledged_report_sequence }}
            </div>
          </div>
        </CardHeader>
        <CardContent class="pt-4 space-y-3 max-h-[380px] overflow-y-auto">
          <div v-if="taskStore.reports.length === 0" class="text-center py-6 text-xs text-muted-foreground">
            暂无阶段报告提交。
          </div>
          <div
            v-for="rep in taskStore.reports"
            :key="rep.sequence"
            class="p-3 rounded-md border border-border bg-muted/20 space-y-2"
          >
            <div class="flex items-center justify-between">
              <div class="flex items-center gap-2">
                <span class="text-xs font-mono font-bold text-muted-foreground">#{{ rep.sequence }}</span>
                <Badge :variant="getReportKindBadge(rep.kind).variant as any" class="text-xs rounded-xs">
                  {{ getReportKindBadge(rep.kind).label }}
                </Badge>
              </div>
              <span class="text-[11px] text-muted-foreground font-mono">
                {{ new Date(rep.created_at).toLocaleTimeString() }}
              </span>
            </div>
            <MarkdownRenderer :content="rep.body" />
            <div v-if="rep.references && rep.references.length > 0" class="pt-2 border-t border-border/40 text-xs text-muted-foreground">
              <span class="font-medium text-foreground">验证路径引用：</span>
              <span v-for="(ref, idx) in rep.references" :key="idx" class="font-mono ml-1 text-foreground">
                {{ ref.path }}{{ ref.line ? `:${ref.line}` : '' }}
              </span>
            </div>
          </div>
        </CardContent>
      </Card>
    </div>

    <!-- Full-Width Bottom-Flush Master Guidance Input for Task (Seamless) -->
    <div class="w-full border-t border-border/70 bg-background/95 backdrop-blur-xs px-6 py-3.5 shrink-0 z-30">
      <div class="space-y-2">
        <div class="text-xs font-medium text-foreground flex items-center gap-1.5">
          <Send class="w-3.5 h-3.5 text-muted-foreground" />
          <span>下发指导指令与纠偏反馈给 Worker Agent</span>
        </div>
        <textarea
          v-model="taskFeedbackBody"
          rows="2"
          class="w-full bg-transparent p-1 text-sm focus:outline-none placeholder:text-muted-foreground resize-none leading-relaxed text-foreground"
          placeholder="在此输入向下游执行端 Agent 下发的指导指令..."
        ></textarea>
        <div class="flex justify-end pt-1.5 border-t border-border/40">
          <Button
            size="sm"
            class="h-7 px-3.5 text-xs rounded-sm bg-primary text-primary-foreground hover:opacity-90 font-medium"
            @click="handleSendTaskFeedback"
            :disabled="!taskFeedbackBody.trim()"
          >
            <Send class="w-3 h-3 mr-1" />
            <span>发送指导指令</span>
          </Button>
        </div>
      </div>
    </div>
  </div>
</template>
