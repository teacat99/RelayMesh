<script setup lang="ts">
// 会话页底部状态块旁的「运行」按钮（#140）：这个会话有对应的非交互续聊预设、对话已经停下来时，
// 一点就在预设的主机上接着这个对话运行一次（不开终端）。点了先弹确认窗，把「在哪里 / 接着哪个对话 / 怎么运行 / 会运行的命令」
// 摆清楚；确认后服务端经主机的执行通道提交（要刚刚解锁——它会让主机运行命令）。
//
// 出现的条件全部由服务端算好（GET /terminal/launchers/for-session，口径与提交运行时的检查完全一致）：
//   - 有「对应」的预设：主机 + 目录与这个会话一致，并且预设跟的就是这个会话（工作流或固定的对话编号）；
//   - 对话没有在运行（智能体状态不是工作中 / 等你回答 / 等批准），这个预设上一次运行也已结束；
//   - 其余不能运行的原因（主机离线……）按钮灰掉，原因写在提示里。
// 对应的预设有多条时，在确认窗里选一条。没有对应预设时什么都不显示：到启动器的「本项目」标签里一键为这个会话创建。
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { ChevronDown, ChevronRight, Loader2, Play } from 'lucide-vue-next'
import { Dialog, DialogContent, DialogDescription, DialogTitle } from '@/components/ui/dialog'
import Button from '@/components/ui/button/Button.vue'
import { toast } from '@/components/ui/sonner'
import { stateView } from '@/lib/agentState'
import { quickRun, runFacts, type QuickRun } from '@/lib/terminal/sessionLaunchers'
import { runNotice } from '@/lib/terminal/launcherRun'
import type { SessionLauncherView } from '@/api/terminal'
import { useAgentStateStore } from '@/stores/agentState'
import { useSessionLaunchersStore } from '@/stores/sessionLaunchers'
import { useTerminalStore } from '@/stores/terminal'

const props = defineProps<{ workflowId: string }>()

const terminal = useTerminalStore()
const agent = useAgentStateStore()
const launchers = useSessionLaunchersStore()

// 「卡住」这类状态随时间变化，要有个时钟；与状态块同一个节奏。
const now = ref(Date.now())
let clock: number | undefined

const view = computed(() => launchers.views[props.workflowId] ?? null)
const agentKey = computed(() => stateView(agent.entryFor(props.workflowId).primary, now.value).key)
const enabled = computed(() => terminal.gateOpen && !launchers.unsupported)
const q = computed<QuickRun>(() => (enabled.value ? quickRun(view.value, agentKey.value) : { mode: 'hidden' }))
const submittingId = computed(() => launchers.submitting[props.workflowId] ?? '')

// ---- 数据：按钮在输入区常驻，所以挂着就定时读；预设被改、对话状态变化时马上再读一次 ----
let polled = ''
function syncPolling() {
  const want = enabled.value && props.workflowId ? props.workflowId : ''
  if (want === polled) return
  if (polled) launchers.stopPolling(polled)
  polled = want
  if (polled) launchers.startPolling(polled)
}
watch(() => [props.workflowId, enabled.value] as const, syncPolling, { immediate: true })
watch(agentKey, () => {
  if (enabled.value) void launchers.refresh(props.workflowId)
})
/** 预设里与「对应」有关的字段的指纹：存了 / 删了 / 改了预设，视图要马上跟上。 */
const presetSignature = computed(() =>
  terminal.launchers.map(l => [l.id, l.name, l.host, l.dir ?? '', l.kind ?? '', l.run?.workflow_id ?? '', l.run?.conversation_id ?? ''].join('\u0001')).join('\u0002'),
)
watch(presetSignature, () => {
  if (enabled.value) void launchers.refresh(props.workflowId)
})

onMounted(() => {
  clock = window.setInterval(() => (now.value = Date.now()), 5000)
})
onBeforeUnmount(() => {
  if (clock !== undefined) window.clearInterval(clock)
  if (polled) launchers.stopPolling(polled)
  polled = ''
})

// ---- 确认窗 ----
const confirmOpen = ref(false)
/** 确认窗里选中的预设（打开那一刻的快照；之后视图刷新不会让内容跳掉，只会在不能再运行时提示）。 */
const chosen = ref<SessionLauncherView | null>(null)
const commandOpen = ref(false)

const options = computed<SessionLauncherView[]>(() => (q.value.mode === 'ready' ? q.value.presets : []))
/** 窗开着期间，选中的预设是不是还能运行（对话又开始工作了、主机掉线了……）。 */
const stillReady = computed(() => !!chosen.value && options.value.some(p => p.id === chosen.value?.id))
const facts = computed(() => (chosen.value ? runFacts(chosen.value) : null))
/** 窗里展示的预设取最新的视图（命令、对话编号跟着刷新），没有了才退回打开时的快照。 */
function latest(p: SessionLauncherView): SessionLauncherView {
  return view.value?.presets.find(x => x.id === p.id) ?? p
}
const shown = computed(() => (chosen.value ? latest(chosen.value) : null))

function openConfirm() {
  if (q.value.mode !== 'ready' || submittingId.value) return
  chosen.value = q.value.presets[0]
  commandOpen.value = false
  confirmOpen.value = true
  void launchers.refresh(props.workflowId)
}

function pick(p: SessionLauncherView) {
  chosen.value = p
  commandOpen.value = false
}

async function go() {
  const p = shown.value
  if (!p || !stillReady.value || submittingId.value) return
  try {
    const plan = await launchers.run(props.workflowId, p.id)
    confirmOpen.value = false
    const n = runNotice(plan)
    toast.success(n.title, { description: n.description, duration: plan.warnings?.length ? 15_000 : 8_000 })
  } catch (e) {
    // 取消解锁：什么都没发生，留在确认窗里；其余失败（主机离线、对话还在运行……）把服务端的原因就地提示，并把视图读新。
    terminal.notifyError(e, `运行「${p.name}」失败`)
    void launchers.refresh(props.workflowId)
  }
}

const buttonTitle = computed(() => {
  const m = q.value
  if (m.mode === 'blocked') return `现在不能运行「${m.preset.name}」：${m.reason}`
  if (m.mode === 'ready') {
    return m.presets.length > 1
      ? `在主机上接着这个对话运行一次（非交互续聊）；有 ${m.presets.length} 个对应预设，点开后选择`
      : `运行预设「${m.presets[0].name}」：在主机上接着这个对话运行一次（非交互续聊，不打开终端）`
  }
  return ''
})
</script>

<template>
  <button
    v-if="q.mode !== 'hidden'"
    type="button"
    data-testid="session-quick-run"
    :data-mode="q.mode"
    :disabled="q.mode === 'blocked' || !!submittingId"
    :title="buttonTitle"
    :aria-label="buttonTitle"
    class="inline-flex shrink-0 cursor-pointer items-center gap-1 rounded-sm border border-primary/40 bg-primary/5 px-2 py-1 font-mono text-[11px] text-primary outline-none transition-colors hover:bg-primary/10 focus-visible:ring-1 focus-visible:ring-ring disabled:cursor-not-allowed disabled:border-border/80 disabled:bg-card/95 disabled:text-muted-foreground sm:text-xs"
    @click="openConfirm"
  >
    <Loader2 v-if="submittingId" class="size-3.5 animate-spin" aria-hidden="true" />
    <Play v-else class="size-3.5" aria-hidden="true" />
    <span>{{ submittingId ? '提交中…' : '运行' }}</span>
  </button>

  <Dialog v-model:open="confirmOpen">
    <DialogContent class="max-w-md gap-3 p-5 [&>*]:min-w-0" data-testid="session-quick-run-dialog">
      <template v-if="shown && facts">
        <div class="space-y-1 pr-6">
          <DialogTitle class="flex items-center gap-2 text-sm font-semibold">
            <Play class="size-4 text-primary" aria-hidden="true" />
            <span class="min-w-0 truncate" data-testid="session-quick-run-title">运行「{{ shown.name }}」？</span>
          </DialogTitle>
          <DialogDescription class="text-xs leading-relaxed text-muted-foreground">
            在主机上接着这个会话的对话运行一次（非交互续聊，不打开终端），会消耗模型用量；结果显示在启动器的预设里，聊天里也会有记录。
          </DialogDescription>
        </div>

        <!-- 对应的预设有多条：选一条 -->
        <div v-if="options.length > 1" class="space-y-1" role="radiogroup" aria-label="选择要运行的预设" data-testid="session-quick-run-options">
          <button
            v-for="p in options"
            :key="p.id"
            type="button"
            role="radio"
            :aria-checked="chosen?.id === p.id"
            class="flex w-full min-w-0 cursor-pointer flex-col rounded-sm border px-2.5 py-1.5 text-left transition-colors"
            :class="chosen?.id === p.id ? 'border-primary/60 bg-primary/5' : 'border-border/70 hover:bg-muted/40'"
            :data-testid="`session-quick-run-option-${p.id}`"
            @click="pick(p)"
          >
            <span class="truncate text-xs font-medium text-foreground">{{ p.name }}</span>
            <span class="truncate font-mono text-[11px] text-muted-foreground">{{ runFacts(p).target }}</span>
          </button>
        </div>

        <dl class="space-y-1.5 rounded-sm border border-border/60 bg-muted/10 px-3 py-2 text-[11px] leading-relaxed">
          <div class="flex gap-2">
            <dt class="w-14 shrink-0 text-muted-foreground">在哪里</dt>
            <dd class="min-w-0 break-all font-mono text-foreground" data-testid="session-quick-run-target">{{ facts.target }}</dd>
          </div>
          <div class="flex gap-2">
            <dt class="w-14 shrink-0 text-muted-foreground">接着对话</dt>
            <dd class="min-w-0 break-all font-mono text-foreground" :title="shown.conversation_id" data-testid="session-quick-run-conversation">{{ facts.conversation }}</dd>
          </div>
          <div class="flex gap-2">
            <dt class="w-14 shrink-0 text-muted-foreground">怎么运行</dt>
            <dd class="min-w-0 break-words text-foreground" data-testid="session-quick-run-options-text">{{ facts.options }}</dd>
          </div>
        </dl>

        <ul v-if="shown.warnings?.length" class="space-y-1" data-testid="session-quick-run-warnings">
          <li v-for="w in shown.warnings" :key="w" class="text-[11px] leading-relaxed text-amber-700 dark:text-amber-400">{{ w }}</li>
        </ul>

        <div v-if="shown.command" class="space-y-1">
          <button
            type="button"
            class="inline-flex cursor-pointer items-center gap-0.5 text-[11px] text-muted-foreground transition-colors hover:text-foreground"
            :aria-expanded="commandOpen"
            data-testid="session-quick-run-command-toggle"
            @click="commandOpen = !commandOpen"
          >
            <component :is="commandOpen ? ChevronDown : ChevronRight" class="size-3" aria-hidden="true" />将运行的命令
          </button>
          <pre
            v-if="commandOpen"
            class="max-h-40 overflow-auto whitespace-pre-wrap break-all rounded-sm border border-border/60 bg-background/60 p-2 font-mono text-[10px] leading-relaxed text-foreground/90"
            data-testid="session-quick-run-command"
          >{{ shown.command }}</pre>
        </div>

        <p v-if="!stillReady" class="text-[11px] leading-relaxed text-destructive" role="alert" data-testid="session-quick-run-stale">
          现在不能运行了（对话又开始工作了、主机掉线了，或预设被改了）；关掉这个窗口，等状态稳定后再试。
        </p>

        <div class="flex items-center justify-end gap-2 pt-1">
          <Button type="button" variant="ghost" size="sm" class="h-8 text-xs" data-testid="session-quick-run-cancel" @click="confirmOpen = false">取消</Button>
          <Button type="button" size="sm" class="h-8 gap-1 text-xs" :disabled="!stillReady || !!submittingId" data-testid="session-quick-run-go" @click="go">
            <Loader2 v-if="submittingId" class="size-3.5 animate-spin" aria-hidden="true" />
            <Play v-else class="size-3.5" aria-hidden="true" />
            {{ submittingId ? '提交中…' : '运行' }}
          </Button>
        </div>
      </template>
    </DialogContent>
  </Dialog>
</template>
