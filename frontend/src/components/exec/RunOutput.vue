<script setup lang="ts">
import { computed, reactive, watch } from 'vue'
import { Loader2, Lock } from 'lucide-vue-next'
import Button from '../ui/button/Button.vue'
import BrailleSpinner from '../ui/spinner/BrailleSpinner.vue'
import { toApiError, type RunView } from '../../api/terminal'
import { useExecRunsStore } from '../../stores/execRuns'
import { useTerminalStore } from '../../stores/terminal'
import { cleanDisplay, formatBytes, isActiveRun, liveText, type LiveOut } from '../../lib/terminal/execRuns'

// 展开的运行卡片里的输出区：
//  - 进行中：实时片段（来自事件流，仅在解锁时下发），按到达顺序混排标准输出与标准错误；
//  - 已结束且已解锁：从主机分页读取完整输出（标准输出与标准错误各一块）；
//  - 已结束但未解锁：只显示服务端保存的脱敏预览，并提示解锁后才能读完整内容。

const props = defineProps<{ run: RunView; live?: LiveOut }>()

const execRuns = useExecRunsStore()
const terminal = useTerminalStore()

type StreamName = 'stdout' | 'stderr'

interface Block {
  text: string
  /** 下一页的起始偏移；null 表示还没读过。 */
  next: number | null
  total: number
  eof: boolean
  loading: boolean
  error: string
  /** 已跳过主机丢弃的中间部分。 */
  gap: boolean
  /** 从末尾开始读的（前面还有内容没读）。 */
  tailMode: boolean
}

const mk = (): Block => ({ text: '', next: null, total: 0, eof: false, loading: false, error: '', gap: false, tailMode: false })
const blocks = reactive<Record<StreamName, Block>>({ stdout: mk(), stderr: mk() })

const PAGE_BYTES = 64 * 1024
/** 浏览器里最多展示的文本（字符数）；超出时提示改用主机上的完整记录。 */
const DISPLAY_CAP = 1_500_000

const active = computed(() => isActiveRun(props.run.state))
const unlocked = computed(() => terminal.isUnlocked)
const expectStderr = computed(() => props.run.stderr_bytes > 0)
const liveShown = computed(() => cleanDisplay(liveText(props.live)))

async function load(stream: StreamName, opts: { tail?: boolean } = {}) {
  const b = blocks[stream]
  if (b.loading) return
  b.loading = true
  b.error = ''
  try {
    const total = stream === 'stdout' ? props.run.stdout_bytes : props.run.stderr_bytes
    let offset = b.next ?? 0
    if (opts.tail) offset = Math.max(0, total - PAGE_BYTES)
    const r = await execRuns.readOutput(props.run.run_id, stream, offset, PAGE_BYTES)
    const text = cleanDisplay(r.text)
    if (opts.tail) {
      b.text = text
      b.tailMode = r.offset > 0
    } else {
      b.text = b.text.length > DISPLAY_CAP ? b.text : b.text + text
    }
    b.next = r.next_offset
    b.total = r.total_bytes
    b.eof = r.eof
    b.gap = b.gap || !!r.gap
  } catch (e) {
    const err = toApiError(e)
    b.error = err.code === 'unlock_required' ? '解锁已过期，请重新解锁后再查看完整输出。' : err.message
  } finally {
    b.loading = false
  }
}

function reset() {
  blocks.stdout = mk()
  blocks.stderr = mk()
}

function loadInitial() {
  if (active.value || !unlocked.value) return
  if (blocks.stdout.next === null && props.run.stdout_bytes > 0) void load('stdout')
  if (blocks.stderr.next === null && expectStderr.value) void load('stderr')
}

watch(
  () => [props.run.run_id, props.run.state, unlocked.value] as const,
  (cur, prev) => {
    if (prev && cur[0] !== prev[0]) reset()
    loadInitial()
  },
  { immediate: true },
)

const hasAny = computed(() => props.run.stdout_bytes + props.run.stderr_bytes > 0)

/** 未解锁（或读不到）时展示的预览：头部，输出更长时再接上尾部。 */
const previewText = computed(() => {
  const head = cleanDisplay(props.run.preview_head ?? '')
  const tail = cleanDisplay(props.run.preview_tail ?? '')
  const total = props.run.stdout_bytes + props.run.stderr_bytes
  const shown = new TextEncoder().encode(props.run.preview_head ?? '').length
  if (total > shown && tail) return `${head}${head.endsWith('\n') || !head ? '' : '\n'}⋯⋯ 中间省略 ⋯⋯\n${tail}`
  return head
})

function unlock() {
  void terminal.requestUnlock('查看完整输出需要再次验证密码')
}

const showStderrBlock = computed(() => expectStderr.value && unlocked.value && !active.value)
</script>

<template>
  <div class="space-y-2">
    <!-- 进行中：实时输出 -->
    <template v-if="active">
      <div
        v-if="liveShown"
        class="rounded-sm bg-zinc-950 text-zinc-100 border border-border/60 p-2.5 max-h-80 overflow-auto"
        role="log"
        aria-live="off"
        aria-label="实时输出"
      >
        <pre class="font-mono text-[11px] leading-relaxed whitespace-pre-wrap break-words">{{ liveShown }}</pre>
      </div>
      <p v-else-if="run.state === 'running'" class="text-[11px] text-muted-foreground flex items-center gap-1.5">
        <BrailleSpinner size="sm" />{{ execRuns.streamUnlocked ? '等待输出…' : '等待输出（未解锁时不显示实时内容）…' }}
      </p>
      <div v-if="run.state === 'running' && !execRuns.streamUnlocked" class="flex items-center gap-2 text-[11px] text-amber-700 dark:text-amber-300">
        <Lock class="w-3 h-3 shrink-0" /><span>实时输出需要解锁；命令照常在主机上运行，结束后可在解锁后查看完整输出。</span>
        <Button variant="outline" size="sm" class="h-6 px-2 rounded-sm text-[11px]" @click="unlock">解锁</Button>
      </div>
      <p v-else-if="live?.partial" class="text-[11px] text-muted-foreground">中间有部分片段没有显示（只缓存最近的内容），命令结束后可查看完整输出。</p>
    </template>

    <!-- 已结束 -->
    <template v-else>
      <p v-if="!hasAny" class="text-[11px] text-muted-foreground">这次运行没有产生输出。</p>

      <template v-else-if="unlocked">
        <div v-for="s in (['stdout', 'stderr'] as const)" v-show="s === 'stdout' || showStderrBlock" :key="s">
          <template v-if="s === 'stdout' ? run.stdout_bytes > 0 : showStderrBlock">
            <div class="flex items-center gap-2 mb-1">
              <span class="text-[10px] font-medium" :class="s === 'stderr' ? 'text-red-600 dark:text-red-400' : 'text-muted-foreground'">
                {{ s === 'stdout' ? '标准输出' : '标准错误' }}
              </span>
              <span class="text-[10px] text-muted-foreground tabular-nums">{{ formatBytes(s === 'stdout' ? run.stdout_bytes : run.stderr_bytes) }}</span>
              <Loader2 v-if="blocks[s].loading" class="w-3 h-3 animate-spin text-muted-foreground" />
            </div>
            <div
              v-if="blocks[s].text"
              class="rounded-sm border p-2.5 max-h-96 overflow-auto"
              :class="s === 'stderr' ? 'bg-red-950/90 text-red-50 border-red-900/50' : 'bg-zinc-950 text-zinc-100 border-border/60'"
              role="log"
              :aria-label="s === 'stdout' ? '标准输出' : '标准错误'"
            >
              <p v-if="blocks[s].tailMode" class="text-[10px] text-zinc-400 mb-1">（前面的内容没有显示，这里是末尾部分）</p>
              <pre class="font-mono text-[11px] leading-relaxed whitespace-pre-wrap break-words">{{ blocks[s].text }}</pre>
            </div>
            <p v-if="blocks[s].gap" class="text-[10px] text-amber-700 dark:text-amber-300 mt-1">输出很长，主机只保留了开头和末尾，中间部分已被丢弃。</p>
            <p v-if="blocks[s].error" class="text-[11px] text-destructive mt-1" role="alert">{{ blocks[s].error }}</p>
            <div v-if="blocks[s].next !== null && !blocks[s].eof" class="flex items-center gap-2 mt-1.5">
              <Button variant="outline" size="sm" class="h-6 px-2 rounded-sm text-[11px]" :disabled="blocks[s].loading" @click="load(s)">加载更多</Button>
              <Button
                v-if="blocks[s].total > PAGE_BYTES * 2"
                variant="ghost"
                size="sm"
                class="h-6 px-2 rounded-sm text-[11px]"
                :disabled="blocks[s].loading"
                @click="load(s, { tail: true })"
              >跳到末尾</Button>
            </div>
          </template>
        </div>
      </template>

      <template v-else>
        <div v-if="previewText" class="rounded-sm bg-zinc-950 text-zinc-100 border border-border/60 p-2.5 max-h-72 overflow-auto" role="log" aria-label="输出预览">
          <pre class="font-mono text-[11px] leading-relaxed whitespace-pre-wrap break-words">{{ previewText }}</pre>
        </div>
        <div class="flex items-center gap-2 text-[11px] text-muted-foreground">
          <Lock class="w-3 h-3 shrink-0" />
          <span>这是服务端保存的脱敏预览；查看完整输出需要解锁。</span>
          <Button variant="outline" size="sm" class="h-6 px-2 rounded-sm text-[11px]" @click="unlock">解锁</Button>
        </div>
      </template>
    </template>
  </div>
</template>
