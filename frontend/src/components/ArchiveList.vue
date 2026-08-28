<script setup lang="ts">
import { ref, computed } from 'vue'
import dayjs from 'dayjs'
import { useSessionStore } from '../stores/session'
import Button from './ui/button/Button.vue'
import Badge from './ui/badge/Badge.vue'
import {
  Search,
  FolderGit2,
  Workflow,
  Clock,
  RotateCcw,
  ExternalLink,
  Inbox,
  Sparkles,
  Layers,
  FileText
} from 'lucide-vue-next'
import type { FeedbackSession } from '../api/types'

const emit = defineEmits<{
  (e: 'selectSession', session: FeedbackSession): void
}>()

const sessionStore = useSessionStore()
const searchQuery = ref('')

export interface ArchivedGroup {
  key: string
  latest: FeedbackSession
  rounds: number
  sessions: FeedbackSession[]
}

const archivedGroups = computed<ArchivedGroup[]>(() => {
  const groups = new Map<string, FeedbackSession[]>()
  for (const s of sessionStore.sessions) {
    if (s.status !== 'archived') continue
    const key = s.workflow_id || s.session_id
    if (!groups.has(key)) {
      groups.set(key, [])
    }
    groups.get(key)!.push(s)
  }

  const list: ArchivedGroup[] = []
  for (const [key, group] of groups.entries()) {
    const sorted = [...group].sort((a, b) => new Date(b.created_at).getTime() - new Date(a.created_at).getTime())
    list.push({
      key,
      latest: sorted[0],
      rounds: group.length,
      sessions: sorted
    })
  }
  return list.sort((a, b) => new Date(b.latest.created_at).getTime() - new Date(a.latest.created_at).getTime())
})

function formatDateTimeCustom(dateStr?: string): string {
  if (!dateStr) return ''
  const target = dayjs(dateStr)
  if (!target.isValid()) return ''
  const now = dayjs()

  const isSameYear = target.isSame(now, 'year')
  const isSameDay = isSameYear && target.isSame(now, 'day')

  if (isSameDay) {
    return target.format('HH:mm')
  } else if (isSameYear) {
    return target.format('MM-DD·HH:mm')
  } else {
    return target.format('YYYY·MM-DD·HH:mm')
  }
}

const filteredArchivedGroups = computed(() => {
  if (!searchQuery.value.trim()) return archivedGroups.value
  const q = searchQuery.value.toLowerCase()
  return archivedGroups.value.filter(g => {
    const s = g.latest
    const title = (s.title || '').toLowerCase()
    const summary = (s.summary || '').toLowerCase()
    const wf = (s.workflow_id || '').toLowerCase()
    const dir = (s.project_directory || '').toLowerCase()
    const id = s.session_id.toLowerCase()
    return title.includes(q) || summary.includes(q) || wf.includes(q) || dir.includes(q) || id.includes(q) || g.key.toLowerCase().includes(q)
  })
})

async function handleUnarchive(group: ArchivedGroup) {
  await sessionStore.unarchiveSession(group.key)
}

function handleView(group: ArchivedGroup) {
  emit('selectSession', group.latest)
}
</script>

<template>
  <div class="flex flex-col h-full space-y-4">
    <!-- Top Search and Filter Bar -->
    <div class="flex flex-col sm:flex-row items-stretch sm:items-center justify-between gap-3 pb-3 border-b border-border/70">
      <div class="relative flex-1">
        <Search class="w-3.5 h-3.5 absolute left-3 top-1/2 -translate-y-1/2 text-muted-foreground" />
        <input
          v-model="searchQuery"
          type="text"
          placeholder="搜索归档记录（标题 / 方案摘要 / 项目目录 / Workflow ID）..."
          class="w-full pl-9 pr-3 py-1.5 text-xs bg-background border border-border/80 rounded-sm focus:outline-hidden focus:border-primary placeholder:text-muted-foreground font-mono"
        />
      </div>

      <div class="flex items-center gap-2 text-xs font-mono text-muted-foreground shrink-0 justify-between sm:justify-start">
        <span class="px-2 py-1 rounded-xs bg-muted/60 border border-border/60">
          共 {{ archivedGroups.length }} 个归档 Workflow
        </span>
        <span v-if="searchQuery" class="text-primary font-medium">
          匹配 {{ filteredArchivedGroups.length }} 条
        </span>
      </div>
    </div>

    <!-- Scrollable Archive Cards Grid/List -->
    <div class="flex-1 overflow-y-auto space-y-3 pr-1 min-h-[320px]">
      <div
        v-if="filteredArchivedGroups.length === 0"
        class="flex flex-col items-center justify-center py-20 text-center space-y-3 text-muted-foreground"
      >
        <div class="w-12 h-12 rounded-sm bg-muted/40 border border-border/60 flex items-center justify-center">
          <Inbox class="w-6 h-6 stroke-[1.2]" />
        </div>
        <div class="space-y-1">
          <p class="text-xs font-semibold text-foreground">暂无匹配的已归档 Workflow</p>
          <p class="text-[11px] text-muted-foreground">在侧边栏中点击右键菜单“归档 Workflow”，可将已完成交互移入归档库</p>
        </div>
      </div>

      <div
        v-for="g in filteredArchivedGroups"
        :key="g.key"
        class="p-4 rounded-sm border border-border/80 bg-card/60 hover:bg-muted/30 transition-all space-y-2.5 shadow-2xs group"
      >
        <!-- Top Row: Title + Tags + Row Actions (无全局底栏按钮，操作内聚在每行) -->
        <div class="flex items-center justify-between gap-3">
          <div class="flex items-center gap-2 min-w-0">
            <Workflow class="w-3.5 h-3.5 text-primary shrink-0" />
            <h3 class="text-xs sm:text-sm font-semibold text-foreground truncate tracking-tight">
              {{ g.latest.title || '工作流方案交互' }}
            </h3>
            <Badge variant="outline" class="text-[10px] px-1.5 py-0 font-normal font-mono rounded-xs shrink-0">
              已归档
            </Badge>
            <Badge v-if="g.rounds > 1" variant="secondary" class="text-[10px] px-1.5 py-0 font-normal font-mono rounded-xs shrink-0">
              共 {{ g.rounds }} 轮交互
            </Badge>
          </div>

          <!-- Row Inline Action Buttons -->
          <div class="flex items-center gap-1.5 shrink-0">
            <Button
              variant="ghost"
              size="sm"
              class="h-6.5 px-2.5 text-xs font-mono rounded-xs text-muted-foreground hover:text-foreground hover:bg-muted cursor-pointer"
              @click="handleUnarchive(g)"
              title="将该 Workflow 恢复至主活动会话流"
            >
              <RotateCcw class="w-3 h-3 mr-1" />
              <span>恢复 Workflow</span>
            </Button>
            <Button
              variant="outline"
              size="sm"
              class="h-6.5 px-2.5 text-xs font-mono rounded-xs border-border/80 bg-card hover:bg-muted text-foreground cursor-pointer"
              @click="handleView(g)"
              title="在主工作区中打开并查看完整记录"
            >
              <ExternalLink class="w-3 h-3 mr-1" />
              <span>查看</span>
            </Button>
          </div>
        </div>

        <!-- Summary Preview -->
        <div class="text-xs text-muted-foreground line-clamp-2 leading-relaxed bg-background/50 p-2 rounded-xs border border-border/40 font-mono">
          {{ g.latest.summary ? g.latest.summary.replace(/[#*`]/g, '') : '暂无详细方案摘要' }}
        </div>

        <!-- Metadata Row -->
        <div class="flex flex-wrap items-center justify-between gap-2 pt-1.5 border-t border-border/40 text-[10px] text-muted-foreground font-mono">
          <div class="flex items-center gap-3">
            <span v-if="g.latest.project_directory" class="flex items-center gap-1 truncate max-w-[240px]">
              <FolderGit2 class="w-3 h-3 text-muted-foreground" />
              {{ g.latest.project_directory }}
            </span>
            <span v-if="g.latest.workflow_id" class="flex items-center gap-1 truncate max-w-[140px]">
              <Workflow class="w-3 h-3 text-muted-foreground" />
              {{ g.latest.workflow_id }}
            </span>
            <span v-if="g.latest.no_feedback_checks" class="flex items-center gap-1">
              <RotateCcw class="w-3 h-3 text-muted-foreground" />
              空回执: {{ g.latest.no_feedback_checks }}次
            </span>
          </div>

          <div class="flex items-center gap-1">
            <Clock class="w-3 h-3 text-muted-foreground" />
            <span>{{ formatDateTimeCustom(g.latest.created_at) }}</span>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>
