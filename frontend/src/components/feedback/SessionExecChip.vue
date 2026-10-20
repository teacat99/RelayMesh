<script setup lang="ts">
// 会话「执行状态」块：把页头原来的三个计时控件合并成一个矩形——
//   [状态图标] 22:28 · 1/6 · 5m
// 图标按状态动起来（挂着等你 / 休眠 / 执行中 / 已结束），右键（或点击、长按）向下展开菜单，在里面快捷调整
// 挂起倒计时、空回执上限、提示词等待时长。
import { computed, onUnmounted, ref, watch, type Component } from 'vue'
import { CircleDot, CircleOff, Moon, Plus, Radio, TriangleAlert } from 'lucide-vue-next'
import { toast } from 'vue-sonner'
import BrailleSpinner from '../ui/spinner/BrailleSpinner.vue'
import { useSessionStore } from '../../stores/session'
import { useSettingsStore } from '../../stores/settings'
import { useAgentStateStore } from '../../stores/agentState'
import { stateView } from '../../lib/agentState'
import { useSessionTimer } from '../../composables/useSessionTimer'
import { usePresence } from '../../composables/usePresence'
import {
  COUNTDOWN_OPTIONS,
  MAX_CHECKS_OPTIONS,
  PROMPT_WAIT_OPTIONS,
  buildExecChip,
  countdownOptionLabel,
  effectiveCountdownSeconds,
  execChipTitle,
  maxChecksOptionLabel,
} from '../../lib/execChip'
import { agentLiveIcon } from '../../lib/agentLive'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuTrigger,
} from '../ui/dropdown-menu'

const sessionStore = useSessionStore()
const settingsStore = useSettingsStore()
const agentStore = useAgentStateStore()

const session = computed(() => sessionStore.selectedSession || sessionStore.currentSession)

const { timerDisplay, cleanup } = useSessionTimer(
  () => session.value as any,
  () => effectiveCountdownSeconds(session.value as any, settingsStore.settings) / 60,
)
onUnmounted(cleanup)

const view = computed(() => buildExecChip(session.value as any, settingsStore.settings, timerDisplay.value))
// Agent 状态机（hook v2）有这个工作流的会话时优先用它，没有再回落到收件箱接口里的三值（#113）
const agentView = computed(() => {
  const wf = (session.value as { workflow_id?: string } | null | undefined)?.workflow_id
  const entry = agentStore.entryFor(wf)
  return entry.primary ? stateView(entry.primary) : null
})
const live = computed(() => agentLiveIcon(sessionStore.agentLive, agentView.value))
const title = computed(() => {
  const base = execChipTitle(view.value, session.value as any, timerDisplay.value)
  return live.value.active ? `${base}\n${live.value.title}` : base
})
const countdown = computed(() => effectiveCountdownSeconds(session.value as any, settingsStore.settings))

// 「执行中」用盲文旋转符（#130）；其余状态仍是各自的图标。
const icon = computed<Component>(() => {
  if (view.value.kind === 'executing' && live.value.active) {
    if (live.value.state === 'idle') return CircleDot
    if (live.value.state === 'quiet') return Moon
    if (live.value.state === 'alert') return TriangleAlert
    return BrailleSpinner
  }
  return ({ countdown: Radio, waiting: Moon, executing: BrailleSpinner, idle: CircleOff })[view.value.kind]
})
const iconClass = computed(() => {
  if (view.value.kind === 'executing' && live.value.active) {
    if (live.value.state === 'idle') return 'text-muted-foreground'
    if (live.value.state === 'quiet') return 'text-amber-500'
    if (live.value.state === 'alert') return 'text-destructive'
    return 'text-primary'
  }
  return ({
    countdown: 'text-emerald-500 motion-safe:animate-pulse',
    waiting: 'text-amber-500 motion-safe:animate-pulse',
    executing: 'text-primary',
    idle: 'text-muted-foreground',
  })[view.value.kind]
})
const timeClass = computed(() => {
  if (view.value.kind === 'idle') return 'text-muted-foreground'
  return timerDisplay.value.isCountdown ? 'text-destructive' : 'text-foreground'
})
const checksClass = computed(() => ({
  ok: 'text-foreground/80',
  warn: 'text-amber-600 dark:text-amber-400',
  full: 'text-destructive',
})[view.value.pressure])

// 「+1 分钟」与输入暂停（#121 收尾宽限）：输入期间倒计时冻结在开始输入的那一刻，停手后接着走；
// 服务端同步暂停这次挂起（见 usePresence），所以界面与真实期限一致。
const { typing, plus } = usePresence()
const frozenTime = ref<string | null>(null)
watch(
  () => typing.value && view.value.kind === 'countdown',
  (freeze) => {
    frozenTime.value = freeze ? view.value.time : null
  },
  { immediate: true },
)
const shownTime = computed(() => frozenTime.value ?? view.value.time)
const canPlus = computed(() => view.value.kind === 'countdown')
async function onPlus() {
  try {
    const ok = await plus()
    // 成功不弹提示：倒计时本身会变长，提示条还会盖住这个按钮，影响连点
    if (!ok) toast.info('智能体现在没有在等你回复', { description: '它不在挂着，没有可续的时间' })
  } catch (e: any) {
    toast.error('续时失败', { description: e?.message || String(e) })
  }
}

const menuOpen = ref(false)

async function apply(what: string, fn: () => Promise<unknown>) {
  try {
    await fn()
  } catch (e: any) {
    toast.error(`${what}失败`, { description: e?.message || String(e) })
  }
}

function pickCountdown(v: number) {
  const id = session.value?.session_id
  settingsStore.updateSettings({ defaultWaitCountdownSeconds: v, defaultWaitCountdownMinutes: Math.ceil(v / 60) })
  if (id) void apply('设置挂起倒计时', () => sessionStore.updateWaitCountdownSeconds(id, v))
}

function pickMaxChecks(v: number) {
  const id = session.value?.session_id
  settingsStore.updateSettings({ maxNoFeedbackChecks: v })
  if (id) void apply('设置空回执上限', () => sessionStore.updateMaxChecks(id, v))
}

function pickPromptWait(v: number) {
  const id = session.value?.session_id
  settingsStore.updateSettings({ promptWaitMinutes: v })
  if (id) void apply('设置提示词等待时长', () => sessionStore.updatePromptWait(id, v))
}

// 菜单里的一排「分段按钮」：每段是一个菜单项（键盘可达），当前值用主色填充
const SEG = 'flex-1 justify-center cursor-pointer rounded-xs px-2 py-1 text-xs font-mono tabular-nums'
function seg(active: boolean, extra = ''): string {
  const tone = active
    ? 'bg-primary text-primary-foreground font-bold focus:bg-primary focus:text-primary-foreground'
    : 'bg-muted/50 hover:bg-muted'
  return `${SEG} ${extra} ${tone}`.replace(/\s+/g, ' ').trim()
}
</script>

<template>
  <div v-if="session" class="flex items-center gap-1 shrink-0">
  <DropdownMenu v-model:open="menuOpen">
    <DropdownMenuTrigger as-child>
      <button
        type="button"
        data-testid="exec-chip"
        :data-kind="view.kind"
        :aria-label="`执行状态 ${view.text}`"
        :title="title"
        class="text-[11px] sm:text-xs px-2 sm:px-2.5 py-1 rounded-sm border transition-all shadow-2xs flex items-center gap-1.5 cursor-pointer shrink-0 bg-card/95 hover:bg-muted border-border/80 text-foreground backdrop-blur-xs select-none font-mono outline-none focus-visible:ring-1 focus-visible:ring-ring"
        @contextmenu.prevent="menuOpen = true"
      >
        <component
          :is="icon"
          data-testid="exec-chip-icon"
          :data-live="view.kind === 'executing' && live.active ? live.state : 'none'"
          class="w-3.5 h-3.5 shrink-0"
          :class="iconClass"
        />
        <span data-testid="exec-chip-time" class="font-medium tabular-nums" :class="timeClass">{{ shownTime }}</span>
        <span class="text-muted-foreground/50">·</span>
        <span data-testid="exec-chip-checks" class="tabular-nums" :class="checksClass">{{ view.used }}/{{ view.maxText }}</span>
        <span class="text-muted-foreground/50">·</span>
        <span data-testid="exec-chip-wait" class="tabular-nums text-muted-foreground">{{ view.wait }}m</span>
      </button>
    </DropdownMenuTrigger>

    <DropdownMenuContent
      align="start"
      data-testid="exec-chip-menu"
      class="w-60 p-1 font-mono text-xs shadow-modal border border-border bg-popover"
    >
      <DropdownMenuLabel class="px-2 pt-1 pb-1 text-[11px]" title="智能体每次挂起等你回复的时长；走完发出一个空回执。秒回 = 几乎立刻发空回执">
        挂起倒计时<span class="text-muted-foreground/70"> · 挂着等你多久</span>
      </DropdownMenuLabel>
      <div class="flex gap-1 px-1 pb-1.5" role="group" aria-label="挂起倒计时">
        <DropdownMenuItem
          v-for="v in COUNTDOWN_OPTIONS"
          :key="v"
          :data-testid="`exec-chip-countdown-${v}`"
          :data-active="countdown === v"
          :class="seg(countdown === v)"
          @select.prevent="pickCountdown(v)"
        >{{ countdownOptionLabel(v) }}</DropdownMenuItem>
      </div>

      <DropdownMenuLabel class="px-2 pt-1 pb-1 text-[11px]" title="收尾宽限：用户最后一次发言后，智能体要连续落空这么多次，才可以收尾；∞ 表示不限">
        空回执上限<span class="text-muted-foreground/70"> · 收尾宽限轮数</span>
      </DropdownMenuLabel>
      <div class="flex gap-1 px-1 pb-1.5" role="group" aria-label="空回执上限">
        <DropdownMenuItem
          v-for="v in MAX_CHECKS_OPTIONS"
          :key="v"
          :data-testid="`exec-chip-max-${v}`"
          :data-active="view.max === v"
          :title="maxChecksOptionLabel(v)"
          :class="seg(view.max === v)"
          @select.prevent="pickMaxChecks(v)"
        >{{ v <= 0 ? '∞' : v }}</DropdownMenuItem>
      </div>

      <DropdownMenuLabel class="px-2 pt-1 pb-1 text-[11px]" title="空回执里让智能体休眠的分钟数，休眠结束后它再回来取你的回复">
        提示词等待<span class="text-muted-foreground/70"> · 让它休眠多久</span>
      </DropdownMenuLabel>
      <div class="flex gap-1 px-1 pb-1" role="group" aria-label="提示词等待">
        <DropdownMenuItem
          v-for="v in PROMPT_WAIT_OPTIONS"
          :key="v"
          :data-testid="`exec-chip-wait-${v}`"
          :data-active="view.wait === v"
          :class="seg(view.wait === v)"
          @select.prevent="pickPromptWait(v)"
        >{{ v }}m</DropdownMenuItem>
      </div>
    </DropdownMenuContent>
  </DropdownMenu>
  <button
    v-if="canPlus"
    type="button"
    data-testid="exec-chip-plus"
    title="我还在：让智能体再多等 1 分钟（可连点）。输入时倒计时会自动暂停。"
    class="text-[11px] sm:text-xs px-1.5 sm:px-2 py-1 rounded-sm border shadow-2xs flex items-center gap-0.5 cursor-pointer shrink-0 bg-card/95 hover:bg-muted border-border/80 text-foreground tabular-nums"
    @click="onPlus"
  >
    <Plus class="w-3 h-3" />1m
  </button>
  </div>
</template>
