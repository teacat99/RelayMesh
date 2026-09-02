<script setup lang="ts">
import { computed, ref } from 'vue'
import { useTaskStore } from '../../stores/task'
import MarkdownRenderer from '../MarkdownRenderer.vue'
import Badge from '../ui/badge/Badge.vue'
import Card from '../ui/card/Card.vue'
import CardHeader from '../ui/card/CardHeader.vue'
import CardTitle from '../ui/card/CardTitle.vue'
import CardContent from '../ui/card/CardContent.vue'
import {
  Sparkles,
  CheckCircle2,
  Clock,
  AlertCircle,
  PlayCircle,
  FileCode,
  Layers,
  TrendingUp,
  Cpu,
  Workflow,
  CheckCheck,
  ChevronRight,
  GitCommit
} from 'lucide-vue-next'

const taskStore = useTaskStore()
const selectedStageId = ref<string | null>(null)

const stages = computed(() => {
  return taskStore.currentTask?.stages || []
})

const currentStage = computed(() => {
  if (!stages.value.length) return null
  const curId = selectedStageId.value || taskStore.currentTask?.current_stage_id
  if (curId) {
    const found = stages.value.find(s => s.id === curId)
    if (found) return found
  }
  return stages.value.find(s => s.status === 'in_progress') || stages.value[0]
})

const completedCount = computed(() => {
  return stages.value.filter(s => s.status === 'completed').length
})

const progressPercent = computed(() => {
  if (!stages.value.length) return 0
  return Math.round((completedCount.value / stages.value.length) * 100)
})

function getStageStatusInfo(status: string) {
  switch (status) {
    case 'completed':
      return { label: '已完成', color: 'text-emerald-500 bg-emerald-500/10 border-emerald-500/20', icon: CheckCircle2 }
    case 'in_progress':
      return { label: '进行中', color: 'text-sky-500 bg-sky-500/10 border-sky-500/20', icon: PlayCircle }
    case 'blocked':
      return { label: '已阻塞', color: 'text-rose-500 bg-rose-500/10 border-rose-500/20', icon: AlertCircle }
    default:
      return { label: '待开始', color: 'text-muted-foreground bg-muted/40 border-border', icon: Clock }
  }
}

function getReportKindBadge(kind: string) {
  switch (kind) {
    case 'progress': return { label: '进展', variant: 'secondary' }
    case 'stage': return { label: '阶段产物', variant: 'default' }
    case 'evidence': return { label: '验证证据', variant: 'outline' }
    case 'question': return { label: '阻塞提问', variant: 'destructive' }
    case 'completion': return { label: '完工交付', variant: 'default' }
    default: return { label: kind, variant: 'outline' }
  }
}
</script>

<template>
  <div class="space-y-6 pb-28 max-w-6xl mx-auto w-full px-2 sm:px-4">
    <!-- 1. Top Executive Overview Banner -->
    <div class="bg-card/70 border border-border/80 rounded-lg p-4 sm:p-5 shadow-2xs backdrop-blur-xs space-y-4">
      <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-3">
        <div class="space-y-1">
          <div class="flex items-center gap-2">
            <span class="inline-flex p-1.5 rounded-sm bg-primary/10 text-primary">
              <Sparkles class="w-4 h-4" />
            </span>
            <h3 class="text-sm sm:text-base font-bold text-foreground tracking-tight font-mono">
              {{ taskStore.currentTask?.title || taskStore.currentTask?.task_id || '托管自驾大盘' }}
            </h3>
            <Badge variant="outline" class="text-[10px] font-mono uppercase">
              {{ taskStore.currentTask?.state || 'active' }}
            </Badge>
          </div>
          <p class="text-xs text-muted-foreground leading-relaxed">
            由【指挥端 Commander】通过 MCP 自主规划拆解，【执行端 Executor】自动编码验证，支持人工随时下达最高指令。
          </p>
        </div>

        <!-- Metric badges -->
        <div class="flex items-center gap-3 shrink-0">
          <div class="px-3 py-2 rounded-md bg-muted/30 border border-border/60 text-center min-w-[80px]">
            <div class="text-[10px] text-muted-foreground font-mono">阶段进度</div>
            <div class="text-sm sm:text-base font-bold font-mono text-foreground">{{ completedCount }}/{{ stages.length }}</div>
          </div>
          <div class="px-3 py-2 rounded-md bg-primary/10 border border-primary/20 text-center min-w-[80px]">
            <div class="text-[10px] text-primary/80 font-mono">完成率</div>
            <div class="text-sm sm:text-base font-bold font-mono text-primary">{{ progressPercent }}%</div>
          </div>
        </div>
      </div>

      <!-- Linear Progress Bar -->
      <div class="space-y-1.5 pt-1">
        <div class="h-2 w-full bg-muted/60 rounded-full overflow-hidden">
          <div
            class="h-full bg-primary transition-all duration-500 rounded-full"
            :style="{ width: `${progressPercent}%` }"
          />
        </div>
      </div>
    </div>

    <!-- 2. Interactive Flow Roadmap (Step-by-Step Stage Flow) -->
    <Card class="border-border shadow-2xs rounded-md">
      <CardHeader class="pb-3 border-b border-border/50 bg-muted/10 flex flex-row items-center justify-between">
        <CardTitle class="text-xs sm:text-sm font-semibold flex items-center gap-2">
          <Workflow class="w-4 h-4 text-primary" />
          <span>任务流全局推进链路 (Stage Flow Pipeline)</span>
        </CardTitle>
        <span class="text-[10px] font-mono text-muted-foreground">
          Rev: {{ taskStore.currentTask?.revision || 1 }}
        </span>
      </CardHeader>
      <CardContent class="pt-4 space-y-4">
        <!-- Step Flow Horizontal Scroller -->
        <div v-if="stages.length" class="flex items-center gap-2 overflow-x-auto pb-2 no-scrollbar">
          <template v-for="(st, idx) in stages" :key="st.id || idx">
            <button
              type="button"
              class="flex items-center gap-2 px-3 py-2 rounded-md border text-left cursor-pointer transition-all shrink-0 min-w-[150px] max-w-[220px]"
              :class="st.id === currentStage?.id
                ? 'bg-primary/10 border-primary shadow-xs ring-1 ring-primary/30'
                : 'bg-card border-border/80 hover:border-border text-muted-foreground'"
              @click="selectedStageId = st.id"
            >
              <div
                class="p-1 rounded-sm border shrink-0"
                :class="getStageStatusInfo(st.status).color"
              >
                <component :is="getStageStatusInfo(st.status).icon" class="w-3.5 h-3.5" />
              </div>
              <div class="min-w-0">
                <div class="text-xs font-mono font-bold text-foreground truncate">
                  {{ idx + 1 }}. {{ st.name }}
                </div>
                <div class="text-[10px] font-mono text-muted-foreground capitalize">
                  {{ getStageStatusInfo(st.status).label }}
                </div>
              </div>
            </button>
            <ChevronRight v-if="idx < stages.length - 1" class="w-4 h-4 text-muted-foreground/50 shrink-0" />
          </template>
        </div>

        <div v-else class="text-center py-6 text-xs text-muted-foreground font-mono">
          指挥端尚未下发结构化阶段流程。
        </div>

        <!-- Selected Stage Detail Card -->
        <div v-if="currentStage" class="p-4 rounded-md border border-border bg-muted/20 space-y-2">
          <div class="flex items-center justify-between">
            <div class="flex items-center gap-2">
              <span class="text-xs font-bold font-mono text-foreground">🎯 当前聚焦阶段：{{ currentStage.name }}</span>
              <Badge variant="outline" class="text-[10px] font-mono capitalize">
                {{ getStageStatusInfo(currentStage.status).label }}
              </Badge>
            </div>
            <span v-if="currentStage.updated_at" class="text-[10px] font-mono text-muted-foreground">
              更新于 {{ new Date(currentStage.updated_at).toLocaleTimeString() }}
            </span>
          </div>

          <p v-if="currentStage.summary" class="text-xs text-foreground/90 leading-relaxed font-sans">
            {{ currentStage.summary }}
          </p>

          <div v-if="currentStage.evidence" class="pt-2 border-t border-border/50 text-xs font-mono text-emerald-600 dark:text-emerald-400 flex items-center gap-1.5">
            <CheckCheck class="w-4 h-4 shrink-0" />
            <span>交付与验证证据: {{ currentStage.evidence }}</span>
          </div>
        </div>
      </CardContent>
    </Card>

    <!-- 3. Dual-Column Core Metrics & Architecture -->
    <div class="grid grid-cols-1 lg:grid-cols-12 gap-6">
      <!-- Left Column: Commander Blueprint Segments (7 cols) -->
      <div class="lg:col-span-7 space-y-4">
        <Card class="border-border shadow-2xs rounded-md">
          <CardHeader class="pb-3 border-b border-border/50 bg-muted/10 flex flex-row items-center justify-between">
            <CardTitle class="text-xs sm:text-sm font-semibold flex items-center gap-2">
              <FileCode class="w-4 h-4 text-sky-500" />
              <span>指挥端分段规划与规范 (Segments)</span>
            </CardTitle>
            <span class="text-[10px] font-mono text-muted-foreground">
              共 {{ taskStore.currentTask?.segments?.length || 0 }} 个分段
            </span>
          </CardHeader>
          <CardContent class="pt-4 max-h-[380px] overflow-y-auto font-mono text-xs no-scrollbar space-y-4">
            <div v-if="!taskStore.currentTask?.segments?.length" class="text-center py-6 text-xs text-muted-foreground">
              暂无规划分段。
            </div>
            <div
              v-for="seg in taskStore.currentTask?.segments"
              :key="seg.name"
              class="p-3.5 rounded-md border border-border/70 bg-card/60 space-y-2"
            >
              <div class="font-bold text-foreground border-b border-border/50 pb-1.5 flex items-center justify-between">
                <span class="text-xs">📌 {{ seg.name }}</span>
                <span class="text-[10px] text-muted-foreground font-normal">Pos: {{ seg.position }}</span>
              </div>
              <div class="text-xs text-muted-foreground font-sans leading-relaxed">
                <MarkdownRenderer :content="seg.content" />
              </div>
            </div>
          </CardContent>
        </Card>
      </div>

      <!-- Right Column: Live Topology & Reports Stream (5 cols) -->
      <div class="lg:col-span-5 space-y-4">
        <!-- Dual Agent Topology Card -->
        <Card class="border-border shadow-2xs rounded-md">
          <CardHeader class="pb-3 border-b border-border/50 bg-muted/10">
            <CardTitle class="text-xs sm:text-sm font-semibold flex items-center gap-2">
              <Cpu class="w-4 h-4 text-emerald-500" />
              <span>双 MCP 终端拓扑 (Agent Nodes)</span>
            </CardTitle>
          </CardHeader>
          <CardContent class="pt-4 space-y-3">
            <!-- Commander Node -->
            <div class="p-3 rounded-md bg-muted/20 border border-border/70 flex items-center justify-between">
              <div class="flex items-center gap-2.5">
                <span class="text-base">🎖️</span>
                <div>
                  <div class="text-xs font-bold font-mono text-foreground">指挥端 (Commander)</div>
                  <div class="text-[10px] text-muted-foreground font-mono">负责目标规划与大盘更新</div>
                </div>
              </div>
              <Badge variant="default" class="text-[10px] bg-emerald-500/10 text-emerald-600 dark:text-emerald-400 border-emerald-500/20 font-mono">
                🟢 Rev: {{ taskStore.currentTask?.revision || 1 }}
              </Badge>
            </div>

            <!-- Executor Node -->
            <div class="p-3 rounded-md bg-muted/20 border border-border/70 flex items-center justify-between">
              <div class="flex items-center gap-2.5">
                <span class="text-base">⚙️</span>
                <div>
                  <div class="text-xs font-bold font-mono text-foreground">执行端 (Executor)</div>
                  <div class="text-[10px] text-muted-foreground font-mono">负责编码、编译与运行测试</div>
                </div>
              </div>
              <Badge variant="default" class="text-[10px] bg-sky-500/10 text-sky-600 dark:text-sky-400 border-sky-500/20 font-mono">
                🟢 Seq: {{ taskStore.currentTask?.report_sequence || 0 }}
              </Badge>
            </div>
          </CardContent>
        </Card>

        <!-- Recent Execution Evidence Stream -->
        <Card class="border-border shadow-2xs rounded-md">
          <CardHeader class="pb-3 border-b border-border/50 bg-muted/10 flex flex-row items-center justify-between">
            <CardTitle class="text-xs sm:text-sm font-semibold flex items-center gap-2">
              <TrendingUp class="w-4 h-4 text-sky-500" />
              <span>最新执行产物与证据 (Reports)</span>
            </CardTitle>
            <span class="text-[10px] font-mono text-muted-foreground">
              共 {{ taskStore.reports.length }} 条
            </span>
          </CardHeader>
          <CardContent class="pt-4 space-y-3 max-h-[300px] overflow-y-auto no-scrollbar">
            <div v-if="!taskStore.reports.length" class="text-center py-8 text-xs text-muted-foreground font-mono">
              暂无上报产物。
            </div>
            <div
              v-for="rep in taskStore.reports.slice(-5).reverse()"
              :key="rep.sequence"
              class="p-2.5 rounded-md border border-border/70 bg-card/60 space-y-1.5"
            >
              <div class="flex items-center justify-between">
                <div class="flex items-center gap-1.5">
                  <span class="text-[10px] font-mono font-bold text-muted-foreground">#{{ rep.sequence }}</span>
                  <Badge :variant="getReportKindBadge(rep.kind).variant as any" class="text-[9px] px-1 py-0 rounded-xs">
                    {{ getReportKindBadge(rep.kind).label }}
                  </Badge>
                </div>
                <span class="text-[10px] font-mono text-muted-foreground">
                  {{ new Date(rep.created_at).toLocaleTimeString() }}
                </span>
              </div>
              <p class="text-xs text-foreground/90 font-mono line-clamp-3 leading-relaxed">
                {{ rep.body }}
              </p>
              <div v-if="rep.references && rep.references.length > 0" class="flex flex-wrap gap-1 pt-1">
                <span
                  v-for="ref in rep.references"
                  :key="ref.path"
                  class="text-[9px] px-1 py-0.2 bg-muted rounded-xs border text-muted-foreground font-mono"
                >
                  📄 {{ ref.path }}
                </span>
              </div>
            </div>
          </CardContent>
        </Card>
      </div>
    </div>
  </div>
</template>

