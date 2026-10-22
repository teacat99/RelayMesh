<script setup lang="ts">
import { computed } from 'vue'
import { toast } from 'vue-sonner'
import { Check, ChevronRight, Copy, Hand, SquareTerminal, X } from 'lucide-vue-next'
import Button from '../ui/button/Button.vue'
import BrailleSpinner from '../ui/spinner/BrailleSpinner.vue'
import RunOutput from './RunOutput.vue'
import type { RunView } from '../../api/terminal'
import {
  approvalRemaining,
  collapsedPreview,
  formatBytes,
  formatDuration,
  isActiveRun,
  modeLabel,
  runElapsed,
  runStateInfo,
  runTitle,
  toolList,
  type LiveOut,
} from '../../lib/terminal/execRuns'
import { formatCountdown } from '../../lib/terminal/format'
import { useSharedNow } from '../../composables/useSessionTimer'

// 模型线程里的一张命令卡片（参考 Cursor 的终端调用历史）：
// 折叠时只有标题行和输出的前几行；展开后是完整命令、调用信息和输出；待批准时给出批准 / 拒绝。

const props = defineProps<{
  run: RunView
  hostName: string
  expanded: boolean
  /** 折叠态预览的行数。 */
  previewN: number
  /** 一个线程里出现多个调用方时，在卡片上标出是谁发起的。 */
  showCaller?: boolean
  live?: LiveOut
  busy?: boolean
  /** 目标主机有人工终端时才能接管 / 在此目录打开终端（SSH 主机没有）；缺省为可以。 */
  canTakeover?: boolean
}>()

const emit = defineEmits<{
  (e: 'toggle'): void
  (e: 'approve'): void
  (e: 'reject'): void
  (e: 'cancel'): void
  (e: 'takeover'): void
}>()

const info = computed(() => runStateInfo(props.run))
const title = computed(() => runTitle(props.run))
const tools = computed(() => toolList(props.run))
const active = computed(() => isActiveRun(props.run.state))
const awaiting = computed(() => props.run.state === 'awaiting_approval')
// 每秒走字的时钟（运行中的计时、待批准的倒计时）由卡片自己读共享时钟，而不是由页面当 prop 灌给每一张卡（#219）：
// prop 一变，卡片不管用没用到都会整张重画——一个线程一页 50 张卡，就是每秒 50 次无谓的重画。
// 只有「运行中 / 待批准」的卡片才读时钟；已结束的卡片（历史里绝大多数）的计算属性里没有读它，所以不会被每秒的时钟唤醒。
const sharedNow = useSharedNow()
const clockMs = () => (active.value ? sharedNow.value : 0)
const elapsed = computed(() => runElapsed(props.run, clockMs()))
const remaining = computed(() => approvalRemaining(props.run, clockMs()))
const preview = computed(() => collapsedPreview(props.run, props.previewN))

const toneClass = computed(() => {
  switch (info.value.tone) {
    case 'ok':
      return 'text-emerald-700 dark:text-emerald-300'
    case 'bad':
      return 'text-red-700 dark:text-red-300'
    case 'warn':
      return 'text-amber-700 dark:text-amber-300'
    case 'run':
      return 'text-emerald-700 dark:text-emerald-300'
    default:
      return 'text-muted-foreground'
  }
})

const dotClass = computed(() => {
  switch (info.value.tone) {
    case 'ok':
      return 'bg-emerald-500'
    case 'bad':
      return 'bg-red-500'
    case 'warn':
      return 'bg-amber-500'
    case 'run':
      return 'bg-emerald-500 animate-pulse'
    default:
      return 'bg-muted-foreground/50'
  }
})

const startedText = computed(() => {
  const t = Date.parse(props.run.started_at || props.run.created_at)
  return Number.isFinite(t) ? new Date(t).toLocaleString('zh-CN', { hour12: false }) : ''
})
const clock = computed(() => {
  const t = Date.parse(props.run.created_at)
  return Number.isFinite(t) ? new Date(t).toLocaleTimeString('zh-CN', { hour12: false }) : ''
})

async function copyCommand() {
  try {
    await navigator.clipboard.writeText(props.run.command)
    toast.success('已复制命令')
  } catch {
    toast.error('复制失败，请手动选择复制')
  }
}

const borderClass = computed(() => {
  if (awaiting.value) return 'border-amber-500/60 bg-amber-500/[0.04]'
  if (props.run.state === 'running') return 'border-emerald-500/40'
  if (info.value.tone === 'bad') return 'border-red-500/30'
  return 'border-border/70'
})
</script>

<template>
  <article class="rounded-sm border bg-card/60 transition-colors" :class="borderClass" :data-run-id="run.run_id" :aria-label="`命令：${title}`">
    <!-- 标题行 -->
    <button
      type="button"
      class="w-full flex items-center gap-2 px-3 py-2 text-left cursor-pointer focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-primary/50 rounded-sm"
      :aria-expanded="expanded"
      @click="emit('toggle')"
    >
      <span class="font-mono text-[11px] font-bold text-muted-foreground shrink-0" aria-hidden="true">&gt;_</span>
      <span class="flex-1 min-w-0 text-xs font-medium truncate" :title="run.description || run.command">{{ title }}</span>
      <span v-if="showCaller && run.credential_name" class="hidden sm:inline text-[10px] px-1 rounded-sm bg-muted text-muted-foreground shrink-0 max-w-[8rem] truncate" :title="`调用方 ${run.credential_name}`">{{ run.credential_name }}</span>
      <span v-if="tools.length" class="hidden md:inline font-mono text-[10px] text-muted-foreground shrink-0 max-w-[10rem] truncate" :title="tools.join(', ')">{{ tools.slice(0, 4).join(', ') }}</span>
      <span class="flex items-center gap-1 shrink-0 text-[11px]" :class="toneClass">
        <BrailleSpinner v-if="run.state === 'running'" size="sm" data-testid="run-card-spinner" />
        <span v-else class="w-1.5 h-1.5 rounded-full" :class="dotClass" aria-hidden="true" />
        <span class="font-medium">{{ info.label }}</span>
        <span v-if="info.detail" class="hidden sm:inline text-muted-foreground">· {{ info.detail }}</span>
        <span v-if="elapsed !== null" class="text-muted-foreground tabular-nums">· {{ formatDuration(elapsed) }}</span>
        <span v-else-if="remaining !== null" class="text-amber-700 dark:text-amber-300 tabular-nums" title="超时未处理会自动拒绝">· 还剩 {{ formatCountdown(Math.ceil(remaining / 1000)) }}</span>
      </span>
      <ChevronRight class="w-3.5 h-3.5 text-muted-foreground shrink-0 transition-transform" :class="expanded ? 'rotate-90' : ''" aria-hidden="true" />
    </button>

    <!-- 折叠态：待批准显示命令本身（批准前必须看得到要运行什么），其余显示输出的前几行 -->
    <div v-if="!expanded" class="px-3 pb-2">
      <pre v-if="awaiting" class="font-mono text-[11px] leading-relaxed whitespace-pre-wrap break-all rounded-sm bg-muted/60 px-2 py-1.5 max-h-28 overflow-auto">{{ run.command }}</pre>
      <template v-else-if="preview.lines.length">
        <div class="border-t border-border/60 pt-1.5">
          <pre class="font-mono text-[11px] leading-relaxed text-muted-foreground whitespace-pre-wrap break-all">{{ preview.lines.join('\n') }}</pre>
          <button
            v-if="preview.more"
            type="button"
            class="mt-0.5 text-[11px] text-muted-foreground hover:text-foreground cursor-pointer"
            @click="emit('toggle')"
          >… {{ preview.exact ? `共 ${preview.total} 行` : '还有更多' }} · 展开</button>
        </div>
      </template>
    </div>

    <!-- 展开态 -->
    <div v-else class="px-3 pb-3 space-y-2.5 border-t border-border/60 pt-2.5">
      <div class="relative">
        <pre class="font-mono text-[11px] leading-relaxed whitespace-pre-wrap break-all rounded-sm bg-muted/60 px-2 py-1.5 pr-8 max-h-48 overflow-auto">{{ run.command }}</pre>
        <button
          type="button"
          class="absolute top-1 right-1 p-1 rounded-sm text-muted-foreground hover:text-foreground hover:bg-background cursor-pointer"
          aria-label="复制命令"
          title="复制命令"
          @click="copyCommand"
        >
          <Copy class="w-3 h-3" />
        </button>
      </div>

      <dl class="grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5 text-[11px]">
        <dt class="text-muted-foreground">调用方</dt>
        <dd class="min-w-0 truncate">{{ run.credential_name || '—' }}<span v-if="run.client_name" class="text-muted-foreground"> · {{ run.client_name }}</span></dd>
        <dt class="text-muted-foreground">主机</dt>
        <dd class="min-w-0 truncate">{{ hostName }}</dd>
        <template v-if="run.cwd">
          <dt class="text-muted-foreground">目录</dt>
          <dd class="min-w-0 font-mono break-all">{{ run.cwd }}</dd>
        </template>
        <dt class="text-muted-foreground">模式</dt>
        <dd>{{ modeLabel(run.policy_mode) }}<span v-if="run.approved_by" class="text-muted-foreground"> · 由 {{ run.approved_by }} 批准</span></dd>
        <dt class="text-muted-foreground">时间</dt>
        <dd class="tabular-nums">{{ startedText }}<span v-if="run.timeout_sec" class="text-muted-foreground"> · 超时 {{ run.timeout_sec }} 秒</span></dd>
        <template v-if="run.workflow_id || run.thread">
          <dt class="text-muted-foreground">归属</dt>
          <dd class="min-w-0 truncate">{{ run.workflow_id || '' }}<span v-if="run.thread">{{ run.workflow_id ? ' · ' : '' }}#{{ run.thread }}</span></dd>
        </template>
      </dl>

      <p v-if="info.detail && !active" class="text-[11px]" :class="toneClass">{{ info.label }} · {{ info.detail }}</p>
      <p v-if="run.truncated" class="text-[11px] text-amber-700 dark:text-amber-300">输出很长，中间约 {{ formatBytes(run.middle_dropped_bytes) }} 已被主机丢弃，只保留了开头和末尾。</p>

      <p v-if="awaiting" class="text-[11px] text-amber-700 dark:text-amber-300 leading-relaxed">
        这条命令正在等你批准——批准之前不会在主机上执行。批准需要解锁；{{ remaining !== null ? `还剩 ${formatCountdown(Math.ceil(remaining / 1000))}，` : '' }}超时未处理会自动拒绝。
      </p>
      <RunOutput v-else :run="run" :live="live" />

      <div class="flex flex-wrap items-center gap-1.5 pt-0.5">
        <template v-if="awaiting">
          <Button size="sm" class="h-7 px-2.5 rounded-sm text-xs gap-1" :disabled="busy" @click="emit('approve')"><Check class="w-3.5 h-3.5" />批准并运行</Button>
          <Button variant="outline" size="sm" class="h-7 px-2.5 rounded-sm text-xs gap-1" :disabled="busy" @click="emit('reject')"><X class="w-3.5 h-3.5" />拒绝</Button>
        </template>
        <Button v-else-if="active" variant="outline" size="sm" class="h-7 px-2.5 rounded-sm text-xs gap-1 text-destructive" :disabled="busy" @click="emit('cancel')"><X class="w-3.5 h-3.5" />取消运行</Button>
        <Button v-if="!awaiting && canTakeover !== false" variant="ghost" size="sm" class="h-7 px-2.5 rounded-sm text-xs gap-1" :disabled="busy" @click="emit('takeover')">
          <Hand v-if="active" class="w-3.5 h-3.5" /><SquareTerminal v-else class="w-3.5 h-3.5" />{{ active ? '在人工终端中接管' : '在此目录打开终端' }}
        </Button>
        <span class="ml-auto text-[10px] text-muted-foreground tabular-nums" :title="`运行 ID ${run.run_id}`">{{ clock }}</span>
      </div>
    </div>
  </article>
</template>
