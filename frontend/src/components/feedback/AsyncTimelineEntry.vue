<script setup lang="ts">
// 异步对话模式下，时间线上「不是一轮」的两类进展条目（#42）：
// - 进度通知：智能体 wait:false 发出的一条话，不阻塞、没有回复框，也不算「第 N 轮」；
// - 任务进展卡：开启异步之后智能体每更新一次任务（新建 / 进行中 / 进入验证 / 只写了进度…）就是一张，
//   按提交时间落在对话里（#105），连续的几张会叠成一摞（见 CardStack）。
import { computed } from 'vue'
import dayjs from 'dayjs'
import { ListChecks, Radio } from 'lucide-vue-next'
import type { TimelineEntry } from '../../lib/timeline'
import { WORK_STATE_META, isWorkState } from '../../lib/workItems'
import { TONE_CLASSES } from '../work/tone'
import { useWorkItemStore } from '../../stores/workItems'
import { useTaskRefs } from '../../composables/useTaskRefs'
import MarkdownRenderer from '../MarkdownRenderer.vue'
import ReplyRefButton from './ReplyRefButton.vue'
import CompactTaskRow from './CompactTaskRow.vue'
import { replyHash, replyRefToken } from '../../lib/replyRef'

// compact（#120 预览视图，默认关）：任务进展换成「事件行」版式（CompactTaskRow，阶段 C2 起固定 32px 单行）。
// 进度通知在预览里是统一消息卡（阶段 B），不会走到这里，所以 compact 只管任务进展。
// 经典列表不传它，下面的紧凑分支不会渲染，经典的两块标记一个字没动。
const props = defineProps<{ entry: Extract<TimelineEntry, { kind: 'notice' | 'task' }>; compact?: boolean }>()

const workStore = useWorkItemStore()
const taskRefs = useTaskRefs(() => workStore.feed.workflowId)

const timeText = computed(() => {
  const target = dayjs(props.entry.at)
  if (!target.isValid()) return ''
  const now = dayjs()
  if (target.isSame(now, 'day')) return target.format('HH:mm')
  if (target.isSame(now, 'year')) return target.format('MM-DD·HH:mm')
  return target.format('YYYY·MM-DD·HH:mm')
})

const verbClass = computed(() => {
  if (props.entry.kind !== 'task') return ''
  const state = props.entry.card.state
  const tone = isWorkState(state) ? WORK_STATE_META[state].tone : 'slate'
  return TONE_CLASSES[tone].badge
})
</script>

<template>
  <!-- 事件行（预览视图） -->
  <CompactTaskRow v-if="compact && entry.kind === 'task'" :entry="entry" :time-text="timeText" :verb-class="verbClass" />

  <!-- 进度通知 -->
  <div
    v-else-if="entry.kind === 'notice'"
    class="rounded-sm border border-sky-500/30 bg-sky-500/5 px-3 py-2"
    data-testid="notice-bubble"
    :data-session-id="entry.session.session_id"
  >
    <div class="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-[11px] text-muted-foreground">
      <Radio class="size-3.5 shrink-0 text-sky-600 dark:text-sky-400" />
      <span class="font-medium text-foreground/80">智能体进度</span>
      <!-- 短哈希 + 引用：点一下在输入框插入「【回复 哈希】」（#106） -->
      <ReplyRefButton :token="replyRefToken(entry.session.session_id)" :label="replyHash(entry.session.session_id)" />
      <span class="ml-auto font-mono text-[10px] opacity-70">{{ timeText }}</span>
    </div>
    <div class="mt-1.5 text-xs leading-relaxed sm:text-[13px]">
      <MarkdownRenderer :content="entry.session.summary" :workflow-id="entry.session.workflow_id" />
    </div>
  </div>

  <!-- 任务更新卡 -->
  <div
    v-else
    class="flex items-start gap-2 rounded-sm border border-border/60 bg-muted/20 px-3 py-2 text-xs"
    data-testid="task-update-card"
    :data-seq="entry.card.seq"
    :data-state="entry.card.state"
  >
    <ListChecks class="mt-0.5 size-3.5 shrink-0 text-muted-foreground" />
    <div class="min-w-0 flex-1 space-y-0.5">
      <div class="flex flex-wrap items-center gap-x-2 gap-y-0.5">
        <span class="font-mono text-[11px] font-semibold text-foreground/70">#{{ entry.card.seq }}</span>
        <span class="min-w-0 break-words text-[13px] font-medium leading-snug text-foreground">{{ entry.card.title }}</span>
        <span class="rounded-xs border px-1 text-[10px]" :class="verbClass" data-testid="task-update-verb">{{ entry.card.verb }}</span>
      </div>
      <p v-if="entry.card.text" class="break-words text-[11px] text-muted-foreground" data-testid="task-update-text">{{ entry.card.text }}</p>
    </div>
    <span class="shrink-0 font-mono text-[10px] text-muted-foreground/70">{{ timeText }}</span>
    <button
      type="button"
      class="shrink-0 cursor-pointer rounded-xs border border-border px-2 py-0.5 text-[11px] text-muted-foreground hover:bg-muted"
      data-testid="task-update-open"
      @click="taskRefs.open(entry.card.seq)"
    >
      查看
    </button>
  </div>
</template>
