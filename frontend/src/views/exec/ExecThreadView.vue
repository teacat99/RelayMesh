<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { toast } from 'vue-sonner'
import { ArrowDown, Bot, Loader2, Lock, Pause, Play, Search, ShieldAlert, SquareTerminal } from 'lucide-vue-next'
import Button from '../../components/ui/button/Button.vue'
import ExecPageHeader from '../../components/exec/ExecPageHeader.vue'
import RunCard from '../../components/exec/RunCard.vue'
import ConfirmDialog from '../../components/terminal/ConfirmDialog.vue'
import { useExecRunsStore } from '../../stores/execRuns'
import { settleDialogHistory } from '../../composables/useDialogBackGesture'
import { useTerminalStore } from '../../stores/terminal'
import type { RunView } from '../../api/terminal'
import { EXEC_HOME, EXEC_HOSTS_PATH, execSessionPath } from '../../lib/mode'
import { hostTitle, relativeTime, shortCode } from '../../lib/terminal/format'
import { canHumanTerminal } from '../../lib/terminal/hosts'
import { RUN_FILTERS, distinctCallers, filterRuns, isActiveRun, runTitle, sortRuns, threadTitle, type RunFilter } from '../../lib/terminal/execRuns'

// 模型线程页（参考 Cursor 的终端调用历史）：一个线程 = 一个调用方 × 一台主机（× 可选的线程标签），
// 或 混合令牌的 工作流 × 主机。历史在上、实时在下；进行中的卡片自动展开并跟随输出，结束后折叠。
// 人的动作：取消、批准 / 拒绝（需批准模式）、接管（取消并在同一目录打开人工终端）、暂停此主机的模型。

const route = useRoute()
const router = useRouter()
const terminal = useTerminalStore()
const execRuns = useExecRunsStore()

const key = computed(() => (typeof route.params.key === 'string' ? route.params.key : ''))
const thread = computed(() => execRuns.threadOf(key.value))
const runs = computed(() => execRuns.runsOf(key.value))
const targetCode = computed(() => thread.value?.target ?? runs.value[0]?.target ?? '')
const host = computed(() => (targetCode.value ? terminal.hostByCode(targetCode.value) : undefined))
const canTakeover = computed(() => canHumanTerminal(host.value))
const hostName = computed(() => (host.value ? hostTitle(host.value) : targetCode.value ? shortCode(targetCode.value) : ''))
const title = computed(() => {
  if (thread.value) return threadTitle(thread.value, hostName.value)
  const r = runs.value[0]
  return r ? threadTitle({ credential_name: r.credential_name, workflow_id: r.workflow_id }, hostName.value) : '模型线程'
})

// ---------- 视图设置 ----------
const PREVIEW_KEY = 'relaymesh_exec_preview_lines'
function readPreview(): number {
  try {
    const n = Number(localStorage.getItem(PREVIEW_KEY))
    return Number.isFinite(n) && n >= 1 && n <= 20 ? Math.floor(n) : 4
  } catch {
    return 4
  }
}
const previewN = ref(readPreview())
function setPreview(n: number) {
  previewN.value = Math.min(20, Math.max(1, Math.floor(n) || 4))
  try {
    localStorage.setItem(PREVIEW_KEY, String(previewN.value))
  } catch {
    /* 隐私模式等：放弃持久化 */
  }
}

const order = ref<'asc' | 'desc'>('asc')
const filter = ref<RunFilter>('all')
const query = ref('')

// ---------- 时钟（驱动运行中的计时与待批准倒计时） ----------
const now = ref(Date.now())
let clock: ReturnType<typeof setInterval> | null = null
const activeRuns = computed(() => runs.value.filter(r => isActiveRun(r.state)))

// ---------- 展开状态 ----------
// 进行中的卡片默认展开、结束后默认折叠；用户手动点过的以用户为准（结束后保持展开 / 折叠）。
const manual = reactive<Record<string, boolean>>({})
function isExpanded(r: RunView): boolean {
  return manual[r.run_id] ?? isActiveRun(r.state)
}
function toggle(r: RunView) {
  manual[r.run_id] = !isExpanded(r)
}

// ---------- 列表 ----------
const shown = computed(() => sortRuns(filterRuns(runs.value, filter.value, query.value), order.value))
const history = computed(() => shown.value.filter(r => !isActiveRun(r.state)))
const liveRuns = computed(() => shown.value.filter(r => isActiveRun(r.state)))
const callers = computed(() => distinctCallers(runs.value))
const firstLoad = computed(() => execRuns.loading[key.value] && runs.value.length === 0)
const notFound = computed(() => execRuns.threadsLoaded && !thread.value && runs.value.length === 0 && !execRuns.loading[key.value] && !execRuns.loadError[key.value])

// ---------- 滚动跟随 ----------
const scroller = ref<HTMLElement | null>(null)
const atBottom = ref(true)
const unseen = ref(false)

function onScroll() {
  const el = scroller.value
  if (!el) return
  atBottom.value = el.scrollHeight - el.scrollTop - el.clientHeight < 96
  if (atBottom.value) unseen.value = false
}

async function toBottom(smooth = false) {
  await nextTick()
  const el = scroller.value
  if (!el) return
  el.scrollTo({ top: el.scrollHeight, behavior: smooth ? 'smooth' : 'auto' })
  unseen.value = false
}

// 新的运行 / 状态变化 / 实时输出：已经在底部就跟着走，否则提示有新内容。
const followSig = computed(() => {
  const liveSize = activeRuns.value.reduce((n, r) => n + (execRuns.live[r.run_id]?.size ?? 0), 0)
  return `${runs.value.length}:${runs.value.map(r => r.state).join(',')}:${liveSize}`
})
watch(followSig, () => {
  if (order.value !== 'asc') return
  if (atBottom.value) void toBottom()
  else unseen.value = true
})

// ---------- 生命周期 ----------
let watched = ''
async function enter(k: string) {
  if (watched) execRuns.unwatchThread(watched)
  watched = k
  if (!k) return
  execRuns.watchThread(k)
  for (const id of Object.keys(manual)) delete manual[id]
  filter.value = 'all'
  query.value = ''
  await execRuns.loadRuns(k)
  await focusRequested()
  if (order.value === 'asc') await toBottom()
}

/** 链接里带了 ?run=<运行 ID>（例如从通知打开）：展开并滚动到那一张卡片。 */
async function focusRequested() {
  const id = typeof route.query.run === 'string' ? route.query.run : ''
  if (!id) return
  if (!runs.value.some(r => r.run_id === id)) await execRuns.refreshRun(id)
  manual[id] = true
  await nextTick()
  scroller.value?.querySelector(`[data-run-id="${CSS.escape(id)}"]`)?.scrollIntoView({ block: 'center' })
}

onMounted(() => {
  clock = setInterval(() => {
    now.value = Date.now()
  }, 1000)
  void enter(key.value)
})
onBeforeUnmount(() => {
  if (clock) clearInterval(clock)
  if (watched) execRuns.unwatchThread(watched)
})
watch(key, k => void enter(k))

// 线程汇总到了但还没载入过运行（例如刷新页面后直接打开链接）：补载。
watch(
  () => execRuns.threadsLoaded,
  () => {
    if (key.value && !execRuns.runs[key.value] && !execRuns.loading[key.value]) void execRuns.loadRuns(key.value)
  },
)

// ---------- 人的动作 ----------
const busy = reactive<Record<string, boolean>>({})

async function guarded(r: RunView, fn: () => Promise<unknown>) {
  if (busy[r.run_id]) return
  busy[r.run_id] = true
  try {
    await fn()
  } finally {
    busy[r.run_id] = false
  }
}

const approve = (r: RunView) => guarded(r, () => execRuns.approve(r))
const reject = (r: RunView) => guarded(r, () => execRuns.reject(r))
const cancel = (r: RunView) =>
  guarded(r, async () => {
    if (await execRuns.cancel(r)) toast.success('已取消运行')
  })

const takeoverAsk = reactive({ open: false, run: null as RunView | null })

function takeover(r: RunView) {
  if (isActiveRun(r.state)) {
    Object.assign(takeoverAsk, { open: true, run: r })
    return
  }
  void doTakeover(r)
}

async function doTakeover(r: RunView) {
  try {
    if (isActiveRun(r.state) && !(await execRuns.cancel(r))) return
    const s = await terminal.createSession(r.target, `接管 · ${runTitle(r, 24)}`, undefined, { cwd: r.cwd || undefined, open: false })
    void router.push(execSessionPath(s.sid))
  } catch (e) {
    terminal.notifyError(e, '打开人工终端失败')
  }
}

async function confirmTakeover() {
  const r = takeoverAsk.run
  if (!r) return
  // 先关对话框、等它触发的浏览器历史回退走完，再取消并跳转；否则回退会把刚跳转的页面又退回线程页。
  takeoverAsk.open = false
  await nextTick()
  await settleDialogHistory()
  await guarded(r, () => doTakeover(r))
}

// ---------- 暂停 ----------
const hostPaused = computed(() => host.value?.model_paused === true)
const pausing = ref(false)
async function toggleHostPause() {
  if (!targetCode.value || pausing.value) return
  pausing.value = true
  try {
    await execRuns.setPaused('host', targetCode.value, !hostPaused.value)
  } finally {
    pausing.value = false
  }
}
async function resumeAll() {
  await execRuns.setPaused('all', undefined, false)
}

// ---------- 提示条 ----------
const unavailableText = computed(() => {
  const st = execRuns.status
  if (!st || st.available) return ''
  if (st.reason === 'exec_off') return '服务端没有开启模型执行（环境变量 RELAYMESH_TERMINAL_EXEC=on）。已有的历史记录仍可查看，但不会再有新的模型命令。'
  return '模型执行当前不可用（终端总闸没有开启）。'
})
const hostBlocked = computed(() => {
  const h = host.value
  if (!h) return ''
  if (!h.online) return '这台主机当前不在线，新的模型命令会被拒绝；已经在运行的命令仍在主机上继续。'
  if (!h.allow_model) return '这台主机没有开放给模型：在「主机」页打开「允许模型访问」后，模型才能在这里执行命令。'
  if (h.holder?.deny_exec) return '这台主机用 --deny-model-exec 拒绝了模型执行（本机一票否决，服务端无法更改）。'
  return ''
})

const lockedNote = computed(() => !terminal.isUnlocked)
function unlock() {
  void terminal.requestUnlock('查看完整输出与批准命令需要再次验证密码')
}

const headerSub = computed(() => {
  const t = thread.value
  const parts: string[] = []
  if (callers.value.length) parts.push(`调用方 ${callers.value.join('、')}`)
  if (t) {
    parts.push(`共 ${t.total} 次调用`)
    if (t.first_at) parts.push(`${relativeTime(t.first_at, now.value)}起`)
  }
  return parts.join(' · ')
})
</script>

<template>
  <div class="flex-1 min-h-0 flex flex-col">
    <ExecPageHeader>
      <Bot class="w-4 h-4 text-muted-foreground shrink-0" aria-hidden="true" />
      <div class="min-w-0">
        <h1 class="text-sm font-bold truncate leading-tight">{{ title }}</h1>
        <p class="text-[11px] text-muted-foreground truncate leading-tight">
          <span v-if="thread?.thread" class="font-mono mr-1.5">#{{ thread.thread }}</span>{{ headerSub }}
        </p>
      </div>
      <span
        v-if="activeRuns.length"
        class="ml-1 text-[11px] px-1.5 py-0.5 rounded-sm bg-emerald-500/10 text-emerald-700 dark:text-emerald-300 shrink-0 tabular-nums"
      >{{ activeRuns.length }} 个进行中</span>
      <template #actions>
        <Button
          v-if="host"
          :variant="hostPaused ? 'default' : 'outline'"
          size="sm"
          class="h-7 px-2 rounded-sm text-xs gap-1"
          :disabled="pausing"
          :title="hostPaused ? '恢复这台主机的模型执行（需要解锁）' : '暂停这台主机的模型执行：进行中的命令会被取消，新的命令会被拒绝'"
          @click="toggleHostPause"
        >
          <Loader2 v-if="pausing" class="w-3.5 h-3.5 animate-spin" /><Play v-else-if="hostPaused" class="w-3.5 h-3.5" /><Pause v-else class="w-3.5 h-3.5" />
          <span class="hidden sm:inline">{{ hostPaused ? '恢复此主机的模型' : '暂停此主机的模型' }}</span>
        </Button>
      </template>
    </ExecPageHeader>

    <!-- 提示条 -->
    <div class="shrink-0 space-y-1.5 px-3 sm:px-4 pt-2 empty:hidden">
      <div v-if="execRuns.paused" class="flex items-center gap-2 text-xs rounded-sm border border-amber-500/40 bg-amber-500/10 text-amber-800 dark:text-amber-200 px-3 py-1.5" role="status">
        <ShieldAlert class="w-3.5 h-3.5 shrink-0" /><span class="flex-1 min-w-0">全部模型执行已被暂停：进行中的命令已取消，新的命令会被拒绝。</span>
        <Button variant="outline" size="sm" class="h-6 px-2 rounded-sm text-[11px]" @click="resumeAll">恢复</Button>
      </div>
      <div v-else-if="hostPaused" class="flex items-center gap-2 text-xs rounded-sm border border-amber-500/40 bg-amber-500/10 text-amber-800 dark:text-amber-200 px-3 py-1.5" role="status">
        <ShieldAlert class="w-3.5 h-3.5 shrink-0" /><span class="flex-1 min-w-0">这台主机的模型执行已被暂停。</span>
      </div>
      <div v-if="unavailableText" class="text-xs rounded-sm border border-border/70 bg-muted/50 text-muted-foreground px-3 py-1.5" role="status">{{ unavailableText }}</div>
      <div v-else-if="hostBlocked" class="flex items-center gap-2 text-xs rounded-sm border border-border/70 bg-muted/50 text-muted-foreground px-3 py-1.5" role="status">
        <span class="flex-1 min-w-0">{{ hostBlocked }}</span>
        <Button variant="outline" size="sm" class="h-6 px-2 rounded-sm text-[11px] shrink-0" @click="router.push(EXEC_HOSTS_PATH)">主机设置</Button>
      </div>
      <div v-if="lockedNote && runs.length" class="flex items-center gap-2 text-xs rounded-sm border border-border/70 bg-muted/50 text-muted-foreground px-3 py-1.5" role="status">
        <Lock class="w-3.5 h-3.5 shrink-0" /><span class="flex-1 min-w-0">已锁定：可以看到每条命令和预览；查看完整输出、实时输出与批准命令需要先解锁。</span>
        <Button variant="outline" size="sm" class="h-6 px-2 rounded-sm text-[11px] shrink-0" @click="unlock">解锁</Button>
      </div>
      <div v-if="execRuns.stream === 'retry'" class="text-[11px] text-amber-700 dark:text-amber-300 px-1" role="status">实时连接中断，正在重连…</div>
    </div>

    <!-- 筛选条 -->
    <div v-if="runs.length" class="shrink-0 px-3 sm:px-4 pt-2 pb-1.5 flex flex-wrap items-center gap-x-3 gap-y-1.5 border-b border-border/50">
      <div class="flex flex-wrap gap-1" role="group" aria-label="按状态筛选">
        <button
          v-for="f in RUN_FILTERS"
          :key="f.id"
          type="button"
          class="text-[10px] px-2 py-0.5 rounded-xs transition-all font-medium cursor-pointer"
          :class="filter === f.id ? 'bg-primary text-primary-foreground shadow-2xs' : 'text-muted-foreground hover:bg-muted/70'"
          :aria-pressed="filter === f.id"
          @click="filter = f.id"
        >{{ f.label }}</button>
      </div>
      <div class="relative flex-1 min-w-[8rem] max-w-xs">
        <Search class="w-3.5 h-3.5 absolute left-2 top-1.5 text-muted-foreground" />
        <input
          v-model="query"
          type="search"
          placeholder="搜索命令 / 意图..."
          aria-label="搜索命令"
          class="w-full pl-7 pr-2 py-0.5 text-xs bg-background border border-border/70 rounded-sm focus:outline-none focus:ring-1 focus:ring-primary/30 placeholder:text-muted-foreground"
        />
      </div>
      <div class="flex items-center gap-2 ml-auto text-[11px] text-muted-foreground">
        <label class="flex items-center gap-1">
          预览
          <select
            :value="previewN"
            class="bg-background border border-border/70 rounded-sm text-[11px] px-1 py-0.5 focus:outline-none cursor-pointer"
            aria-label="折叠时显示的输出行数"
            @change="setPreview(Number(($event.target as HTMLSelectElement).value))"
          >
            <option v-for="n in [1, 2, 4, 8, 12, 20]" :key="n" :value="n">{{ n }} 行</option>
          </select>
        </label>
        <button
          type="button"
          class="px-1.5 py-0.5 rounded-sm hover:bg-muted/70 hover:text-foreground cursor-pointer"
          :aria-label="order === 'asc' ? '当前最新在底，点击改为最新在前' : '当前最新在前，点击改为最新在底'"
          @click="order = order === 'asc' ? 'desc' : 'asc'"
        >{{ order === 'asc' ? '最新在底' : '最新在前' }}</button>
      </div>
    </div>

    <!-- 卡片 -->
    <div class="flex-1 min-h-0 relative">
    <div ref="scroller" class="absolute inset-0 overflow-y-auto px-3 sm:px-4 py-3" @scroll.passive="onScroll">
      <div v-if="firstLoad" class="space-y-2" aria-busy="true" aria-label="正在读取运行记录">
        <div v-for="i in 3" :key="i" class="h-16 rounded-sm bg-muted/60 animate-pulse" />
      </div>

      <div v-else-if="execRuns.loadError[key] && !runs.length" class="text-xs text-destructive space-y-2" role="alert">
        <p>{{ execRuns.loadError[key] }}</p>
        <Button variant="outline" size="sm" class="h-7 rounded-sm text-xs" @click="execRuns.loadRuns(key)">重试</Button>
      </div>

      <div v-else-if="notFound" class="max-w-md mx-auto py-16 text-center space-y-3">
        <Bot class="w-8 h-8 mx-auto text-muted-foreground/60" />
        <p class="text-sm font-medium">找不到这个线程</p>
        <p class="text-xs text-muted-foreground leading-relaxed">它可能已经被清理（模型运行记录默认保留 {{ execRuns.status?.retention_days ?? 30 }} 天），或者所在主机已被删除。</p>
        <Button variant="outline" size="sm" class="h-7 rounded-sm text-xs gap-1" @click="router.push(EXEC_HOME)"><SquareTerminal class="w-3.5 h-3.5" />回到终端首页</Button>
      </div>

      <div v-else class="max-w-4xl mx-auto space-y-4">
        <div v-if="execRuns.hasMore[key] && order === 'asc'" class="flex justify-center">
          <Button variant="outline" size="sm" class="h-7 rounded-sm text-xs gap-1" :disabled="execRuns.loading[key]" @click="execRuns.loadRuns(key, true)">
            <Loader2 v-if="execRuns.loading[key]" class="w-3.5 h-3.5 animate-spin" />加载更早的记录
          </Button>
        </div>

        <p v-if="runs.length && !shown.length" class="text-xs text-muted-foreground text-center py-8">没有符合筛选条件的命令。</p>

        <template v-for="section in order === 'asc' ? ['history', 'live'] : ['live', 'history']" :key="section">
          <section v-if="section === 'history' ? history.length : liveRuns.length" :aria-label="section === 'history' ? '历史' : '实时'">
            <h2 class="flex items-center gap-2 mb-2 text-[11px] font-medium text-muted-foreground">
              <span>{{ section === 'history' ? '历史' : '实时' }}</span>
              <span class="flex-1 h-px bg-border/60" />
              <span v-if="section === 'live'" class="inline-flex items-center gap-1 text-emerald-700 dark:text-emerald-300"><span class="w-1.5 h-1.5 rounded-full bg-emerald-500 animate-pulse" />{{ liveRuns.length }}</span>
            </h2>
            <div class="space-y-2">
              <RunCard
                v-for="r in section === 'history' ? history : liveRuns"
                :key="r.run_id"
                :run="r"
                :host-name="hostName"
                :expanded="isExpanded(r)"
                :preview-n="previewN"
                :now="now"
                :show-caller="callers.length > 1"
                :live="execRuns.live[r.run_id]"
                :busy="busy[r.run_id]"
                :can-takeover="canTakeover"
                @toggle="toggle(r)"
                @approve="approve(r)"
                @reject="reject(r)"
                @cancel="cancel(r)"
                @takeover="takeover(r)"
              />
            </div>
          </section>
        </template>

        <div v-if="execRuns.hasMore[key] && order === 'desc'" class="flex justify-center">
          <Button variant="outline" size="sm" class="h-7 rounded-sm text-xs gap-1" :disabled="execRuns.loading[key]" @click="execRuns.loadRuns(key, true)">
            <Loader2 v-if="execRuns.loading[key]" class="w-3.5 h-3.5 animate-spin" />加载更早的记录
          </Button>
        </div>
      </div>

    </div>
    <button
      v-if="unseen && order === 'asc'"
      type="button"
      class="absolute bottom-3 right-4 flex items-center gap-1 px-2.5 py-1 rounded-full bg-primary text-primary-foreground text-[11px] shadow-float cursor-pointer"
      @click="toBottom(true)"
    >
      <ArrowDown class="w-3 h-3" />有新内容
    </button>
    </div>

    <ConfirmDialog
      v-model:open="takeoverAsk.open"
      title="接管这条正在运行的命令？"
      description="接管会先取消这条命令（结束它的整个进程组），然后在同一目录打开一个人工终端。已经产生的输出仍保留在记录里。"
      confirm-text="取消并接管"
      destructive
      @confirm="confirmTakeover"
    />
  </div>
</template>
