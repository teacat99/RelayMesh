<script setup lang="ts">
import { computed } from 'vue'
import Badge from '../ui/badge/Badge.vue'
import {
  Workflow,
  Bot,
  Sparkles,
  Pin,
  Clock,
  CheckCircle2,
  AlertCircle,
  FolderGit2,
  X,
  Laptop
} from 'lucide-vue-next'
import type { UnifiedItem } from '../AppSidebar.vue'
import dayjs from 'dayjs'
import { computeSessionTimer } from '../../composables/useSessionTimer'

const props = defineProps<{
  item: UnifiedItem
  isActive: boolean
  isPinned: boolean
  now: number
}>()

const emit = defineEmits<{
  (e: 'select', item: UnifiedItem): void
  (e: 'contextmenu', event: MouseEvent, item: UnifiedItem): void
  (e: 'cancel', id: string): void
}>()

function getStatusBadge(item: UnifiedItem) {
  if (item.type === 'task') {
    switch (item.status) {
      case 'idle': return { label: '空闲', variant: 'outline' }
      case 'working': return { label: '执行中', variant: 'secondary' }
      case 'waiting_for_feedback': return { label: '等待反馈', variant: 'secondary' }
      case 'completed': return { label: '已完成', variant: 'default' }
      case 'failed': return { label: '失败', variant: 'destructive' }
      case 'cancelled': return { label: '已取消', variant: 'outline' }
    }
    return { label: item.status === 'completed' ? '已完工' : item.status, variant: 'outline' }
  }

  switch (item.status) {
    case 'completed': return { label: '已完成', variant: 'default' }
    case 'pending': return { label: '等待确认', variant: 'secondary' }
    case 'cancelled': return { label: '已取消', variant: 'outline' }
    case 'timeout': return { label: '已超时', variant: 'destructive' }
    case 'archived': return { label: '已归档', variant: 'outline' }
    default: return { label: item.status, variant: 'outline' }
  }
}

function getItemPresence(item: UnifiedItem): { label: string; dotClass: string } {
  const p = item.user_presence || 'online'
  if (p === 'away') {
    return { label: '暂离', dotClass: 'bg-amber-500 shadow-[0_0_4px_rgba(245,158,11,0.6)]' }
  }
  if (p === 'autopilot') {
    return { label: '托管', dotClass: 'bg-indigo-500 shadow-[0_0_4px_rgba(99,102,241,0.6)]' }
  }
  return { label: '在线', dotClass: 'bg-emerald-500 shadow-[0_0_4px_rgba(16,185,129,0.6)]' }
}

function getItemTimerInfo(item: UnifiedItem) {
  if (item.type !== 'feedback') {
    return { text: '', prefix: '', isCountdown: false }
  }

  const rawSess = item.raw as any
  return computeSessionTimer(rawSess, props.now, rawSess?.wait_countdown_minutes ?? 2)
}

function formatTime(dateStr: string) {
  if (!dateStr) return ''
  const d = dayjs(dateStr)
  if (!d.isValid()) return ''
  const nowDay = dayjs()
  if (d.isSame(nowDay, 'day')) {
    return d.format('HH:mm')
  }
  if (d.isSame(nowDay, 'year')) {
    return d.format('MM-DD HH:mm')
  }
  return d.format('YYYY-MM-DD')
}

function formatItemProjectDirectory(item: UnifiedItem): string {
  const rawSession = item.raw as any
  let dir = item.project_directory || rawSession?.project_directory || ''
  const host = rawSession?.host_name || ''
  if (dir && host) {
    if (!dir.startsWith(`${host}:`)) {
      return `${host}:${dir}`
    }
  }
  return dir
}
</script>

<template>
  <div
    class="p-2.5 rounded-xs border transition-all cursor-pointer relative group"
    :class="isActive
      ? 'border-border/90 bg-muted/85 dark:bg-muted/65 shadow-inner ring-1 ring-primary/30 border-l-[3.5px] border-l-primary'
      : (item.type === 'feedback' && item.status === 'pending'
          ? 'border-border/70 bg-muted/20 hover:border-border hover:bg-muted/35'
          : 'border-transparent hover:border-border/60 hover:bg-muted/30')"
    @click="emit('select', item)"
    @contextmenu="emit('contextmenu', $event, item)"
  >
    <!-- 待确认专属小顶栏 -->
    <div
      v-if="item.type === 'feedback' && item.status === 'pending'"
      class="flex items-center justify-between pb-1.5 mb-1.5 border-b border-border/50 text-[10px] font-mono"
    >
      <div class="flex items-center gap-1.5 shrink-0">
        <span class="w-1.5 h-1.5 rounded-full shrink-0" :class="getItemPresence(item).dotClass"></span>
        <span class="text-[10px] text-muted-foreground">{{ getItemPresence(item).label }}</span>
      </div>

      <div class="flex-1 flex items-center justify-center text-center px-1">
        <span :class="getItemTimerInfo(item).isCountdown ? 'text-destructive font-bold' : (isActive ? 'font-bold text-foreground' : 'font-medium text-foreground')">
          <span v-if="getItemTimerInfo(item).prefix" class="text-muted-foreground/80 text-[9px] mr-0.5">{{ getItemTimerInfo(item).prefix }}:</span>{{ getItemTimerInfo(item).text }}
        </span>
      </div>

      <div class="shrink-0 flex items-center">
        <button
          type="button"
          class="px-1.5 py-0.5 rounded-2xs text-[9px] font-mono border border-destructive/30 text-destructive bg-destructive/10 hover:bg-destructive hover:text-destructive-foreground transition-colors cursor-pointer flex items-center gap-0.5"
          :title="`取消工作流: ${item.title}`"
          @click.stop="emit('cancel', item.id)"
        >
          <X class="w-2.5 h-2.5" />
          <span>取消</span>
        </button>
      </div>
    </div>

    <!-- Title Row -->
    <div class="flex items-center justify-between gap-1.5 mb-1">
      <div class="flex items-center gap-1.5 min-w-0">
        <Pin v-if="isPinned" class="w-3 h-3 text-primary shrink-0 -rotate-45 fill-primary/30" />
        <Workflow v-else-if="item.type === 'feedback'" class="w-3.5 h-3.5 shrink-0" :class="isActive ? 'text-primary' : 'text-foreground'" />
        <Sparkles v-else class="w-3.5 h-3.5 text-primary shrink-0" />
        <span class="text-xs truncate font-mono" :class="isActive ? 'font-bold text-foreground' : 'font-medium text-foreground/85 group-hover:text-foreground'">
          {{ item.title }}
        </span>
      </div>
      <div class="flex items-center gap-1 shrink-0">
        <span
          v-if="item.type === 'feedback' && item.status === 'completed' && getItemTimerInfo(item).text"
          class="text-[9px] px-1 py-0 rounded-2xs bg-muted/70 text-muted-foreground font-mono"
          :title="`AI 任务执行耗时`"
        >
          执行: {{ getItemTimerInfo(item).text }}
        </span>
        <span
          v-if="item.type === 'feedback' && item.rounds_count && item.rounds_count > 1"
          class="text-[9px] px-1 py-0 rounded-2xs bg-muted/80 text-muted-foreground font-mono"
          :title="`该工作流包含 ${item.rounds_count} 轮交互历史`"
        >
          {{ item.rounds_count }}轮
        </span>
        <Badge :variant="getStatusBadge(item).variant as any" class="text-[9px] px-1 py-0 shrink-0 font-normal rounded-2xs">
          {{ getStatusBadge(item).label }}
        </Badge>
      </div>
    </div>

    <!-- Summary / Subtitle Preview -->
    <p v-if="item.type === 'feedback'" class="text-[11px] text-muted-foreground line-clamp-1 leading-snug mb-1">
      {{ item.summary.replace(/[#*`]/g, '') || '暂无内容' }}
    </p>
    <p v-else class="text-[11px] text-muted-foreground line-clamp-1 font-mono mb-1">
      {{ item.subtitle }}
    </p>

    <!-- Footer Metadata Row -->
    <div class="flex items-center justify-between text-[10px] text-muted-foreground font-mono pt-1 border-t border-border/30">
      <span class="truncate max-w-[130px] flex items-center gap-1" :title="formatItemProjectDirectory(item)">
        <FolderGit2 class="w-2.5 h-2.5 shrink-0" />
        <span class="truncate">{{ formatItemProjectDirectory(item) ? formatItemProjectDirectory(item).split('/').pop() : '工作区' }}</span>
      </span>
      <span>{{ formatTime(item.created_at) }}</span>
    </div>
  </div>
</template>
