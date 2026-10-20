<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { BellRing, Bot, ExternalLink, Info, Loader2, Pencil, Play, Plus, RefreshCw, RotateCcw, Server, Sparkles, SquareTerminal, Trash2 } from 'lucide-vue-next'
import Button from '../../ui/button/Button.vue'
import BrailleSpinner from '../../ui/spinner/BrailleSpinner.vue'
import LauncherOtherRuns from './LauncherOtherRuns.vue'
import type { GuardItem, GuardView, HostView, LauncherOtherRun, LauncherRunView, SessionLauncherSuggest, TerminalLauncher } from '../../../api/terminal'
import { hostTitle, relativeTime, shortCode } from '../../../lib/terminal/format'
import { GUARD_TONE_DOT, GUARD_TONE_TEXT, guardStateView, headlessButton, lastWakeText, wakeAdvice, wakeButton } from '../../../lib/terminal/guard'
import { CURSOR_CLI_NAME, hostReadiness, isHeadlessPreset, normalizeName, runningSessionsOf, type LauncherLimits } from '../../../lib/terminal/launcher'
import { RUN_TONE_DOT, RUN_TONE_TEXT, autoResumeLine, runFailureLine, runHint, runMeta, runReadiness, runStatus, runSummary } from '../../../lib/terminal/launcherRun'
import { inProject, othersInView, projectName, resolveTab, suggestSummary, tabCounts, type ListTab, type ProjectScope, type TabChoice } from '../../../lib/terminal/sessionLaunchers'

/**
 * 启动器的预设清单：每一条一个「启动」按钮；它已经在运行的会话列在下面，可以「继续」而不是再开一个。
 * 「非交互续聊」预设（#126）没有终端：按钮是「运行」，最近一次运行的状态、用时、最后一条回复列在下面。
 * 主机离线 / 不存在时按钮置灰并写明原因，预设本身仍然可以编辑和删除。
 *
 * 从会话页打开时（带了 scope）清单分「本项目 / 全部」两个标签（#140）：本项目 = 目录是会话的项目目录（或在它下面）的预设，
 * 默认看本项目；本项目里没有对应的续聊预设时，顶上给一张预填好的「为这个会话创建续聊预设」建议卡（点一下确认才创建）。
 */
const props = defineProps<{
  launchers: TerminalLauncher[]
  hosts: HostView[]
  hostsLoaded: boolean
  /** 正在启动的预设编号（启动是异步的，期间不允许再点任何一个「启动」）。 */
  busyId: string
  limits: LauncherLimits
  /** 会话守护（D-214）：服务端判断出的各预设状态；旧版 Hub / 还没读到时是 null。 */
  guard?: GuardView | null
  /** 正在唤醒的预设编号（唤醒要先解锁、再生成计划、再连上会话，期间不允许再点）。 */
  wakeBusyId?: string
  permitBusyId?: string
  /** 从状态卡点「唤醒」进来时要定位的预设：滚到它、加亮；真正的唤醒仍要点它的「唤醒」按钮。 */
  focusId?: string
  /** 「非交互续聊」预设最近一次运行的现状（预设编号 → 现状）；没有运行过的预设没有条目。 */
  runs?: Record<string, LauncherRunView>
  /** 正在提交运行的预设编号（提交要先解锁、再等服务端接收，期间不允许再点任何一个「运行」/「启动」）。 */
  runBusyId?: string
  /** 不属于任何预设的非交互运行（#147：旧入口、非交互唤醒、预设已删除后留下的）；旧版 Hub 没有。 */
  others?: LauncherOtherRun[]
  /** 正在取消的「其他运行」的运行编号。 */
  cancelBusyId?: string
  /** 正在取消自动续跑的出事运行的编号（#151）。 */
  autoBusyId?: string
  /** 「本项目」的范围（会话的项目目录与主机）；没有（不是从会话页打开、或 Hub 太旧）就不分标签。 */
  scope?: ProjectScope | null
  /** 当前标签的选择：auto = 用户还没选过，按默认规则（本项目里有预设或有建议就看本项目，否则看全部）。 */
  tab?: TabChoice
  /** 这个会话没有对应的续聊预设时，服务端给的一份预填建议；建议不出来时 suggestReason 说明原因。 */
  suggest?: SessionLauncherSuggest | null
  suggestReason?: string
  /** 正在创建建议的预设（要先解锁、再等服务端保存）。 */
  suggestBusy?: boolean
}>()

const emit = defineEmits<{
  (e: 'permit', item: GuardItem, permit: 0 | 1): void
  (e: 'wake', item: GuardItem): void
  (e: 'wake-headless', item: GuardItem): void
  (e: 'start', l: TerminalLauncher): void
  (e: 'run', l: TerminalLauncher): void
  (e: 'open-thread', key: string): void
  (e: 'cancel-run', run: LauncherOtherRun): void
  (e: 'cancel-auto', run: LauncherRunView): void
  (e: 'resume', l: TerminalLauncher, sid: string): void
  (e: 'edit', l: TerminalLauncher): void
  (e: 'remove', l: TerminalLauncher): void
  (e: 'create'): void
  (e: 'create-cursor'): void
  (e: 'create-headless'): void
  (e: 'update:tab', t: ListTab): void
  (e: 'create-suggested'): void
  (e: 'customize-suggested'): void
}>()

const rows = computed(() =>
  props.launchers.map(l => {
    const host = props.hosts.find(h => h.machine_code === l.host)
    const ready = hostReadiness(host)
    const isRun = isHeadlessPreset(l)
    const last = isRun ? props.runs?.[l.id] : undefined
    const watched = !isRun && !!l.guard?.enabled
    const item = watched ? props.guard?.items.find(i => i.preset_id === l.id) : undefined
    return {
      l,
      host,
      hostLabel: host ? hostTitle(host) : shortCode(l.host),
      ready,
      // 非交互续聊预设：没有终端会话，只有「最近一次运行」。
      isRun,
      last,
      runReady: isRun ? runReadiness(host, last) : null,
      runStat: last ? runStatus(last) : null,
      // 异常退出的失败说明与自动续跑的安排（#151）；倒计时随每次轮询刷新。
      runFailure: last ? runFailureLine(last) : null,
      runAuto: last ? autoResumeLine(last) : null,
      running: ready.ok && !isRun ? runningSessionsOf(l, host) : [],
      watched,
      item,
      view: item ? guardStateView(item.verdict.state) : null,
      wake: item ? wakeButton(item) : null,
      headless: item ? headlessButton(item) : null,
    }
  }),
)

// ---------- 本项目 / 全部（#140） ----------
const counts = computed(() => tabCounts(props.launchers, props.scope ?? null))
/** 有范围、并且有东西可分（有预设，或有建议可给）时才显示标签。 */
const tabsShown = computed(() => !!props.scope && (props.launchers.length > 0 || !!props.suggest))
/** 要定位的预设（从状态卡点「唤醒」进来）不在本项目里时，直接看全部，免得它藏在另一个标签里。 */
const choice = computed<TabChoice>(() => {
  const c = props.tab ?? 'auto'
  if (c !== 'auto' || !props.focusId) return c
  const f = props.launchers.find(l => l.id === props.focusId)
  return f && !inProject(f, props.scope ?? null) ? 'all' : 'auto'
})
const activeTab = computed<ListTab>(() => resolveTab(choice.value, counts.value, !!props.suggest))
const visibleRows = computed(() =>
  tabsShown.value && activeTab.value === 'project' ? rows.value.filter(r => inProject(r.l, props.scope ?? null)) : rows.value,
)
/** 不属于任何预设的运行：在「本项目」标签下只看本项目的，其余（全部 / 不分标签）全部都看。 */
const visibleOthers = computed(() => othersInView(props.others, props.scope ?? null, tabsShown.value && activeTab.value === 'project'))
const showSuggest = computed(() => !!props.suggest && (!tabsShown.value || activeTab.value === 'project'))
/** 给不出建议时写明为什么（主机清单里没有这个会话所在的主机、不知道目录、预设到上限了……），免得人对着「没有运行按钮」猜。 */
const showReason = computed(() => !props.suggest && !!props.suggestReason && (!tabsShown.value || activeTab.value === 'project'))
const projectLabel = computed(() => projectName(props.scope ?? null))

// 定位：预设是随主机状态异步到的，所以清单有了这一条再滚；一个定位只滚一次。
let focused = ''
watch(
  () => [props.focusId ?? '', props.launchers.length, activeTab.value] as const,
  async ([id]) => {
    if (!id) {
      focused = ''
      return
    }
    if (focused === id || !props.launchers.some(l => l.id === id)) return
    focused = id
    await nextTick()
    document.querySelector(`[data-testid="launcher-item-${CSS.escape(id)}"]`)?.scrollIntoView({ block: 'center', behavior: 'smooth' })
  },
  { immediate: true },
)

const wakeMax = computed(() => props.guard?.wake_max ?? 3)
const progressMinutes = computed(() => props.guard?.progress_minutes ?? 10)

/** 状态行后半段：多久了、最近一次唤醒的结果、24 小时内唤醒了几次。 */
function guardMeta(item: GuardItem): string {
  const parts: string[] = []
  if (item.verdict.since) parts.push(relativeTime(item.verdict.since))
  const w = lastWakeText(item.last_wake, progressMinutes.value)
  if (w) parts.push(w)
  if (item.wakes_24h > 0) parts.push(`24 小时内已唤醒 ${item.wakes_24h}/${wakeMax.value} 次`)
  return parts.join(' · ')
}

// 唤醒：会话看起来没出问题、超过一天的参考次数、或最近连着没有效果时，先在行内问一句（说明为什么），确认了才做。
const confirmHeadlessId = ref('')
const confirmWakeId = ref('')
const confirmReasons = ref<string[]>([])
function askWake(item: GuardItem) {
  const advice = wakeAdvice(item, wakeMax.value)
  if (!advice.confirm) {
    confirmWakeId.value = ''
    emit('wake', item)
    return
  }
  confirmWakeId.value = item.preset_id
  confirmReasons.value = advice.reasons
}
function goHeadless(item: GuardItem) {
  confirmHeadlessId.value = ''
  emit('wake-headless', item)
}
function confirmWake(item: GuardItem) {
  confirmWakeId.value = ''
  emit('wake', item)
}

const atLimit = computed(() => props.launchers.length >= props.limits.max)
const hasCursor = computed(() => props.launchers.some(l => normalizeName(l.name).toLowerCase() === CURSOR_CLI_NAME.toLowerCase()))

function openedAgo(created: number | undefined): string {
  if (!created) return ''
  // 创建时间是主机时钟的 Unix 秒，只用来展示；时钟有偏差时 relativeTime 会给出「刚刚」。
  return `开于 ${relativeTime(new Date(created * 1000).toISOString())}`
}

// 删除要点两次：第一次变成「确认删除」，几秒内没有再点就恢复，免得手滑。
const confirmId = ref('')
let confirmTimer: ReturnType<typeof setTimeout> | undefined
function askRemove(l: TerminalLauncher) {
  if (confirmId.value === l.id) {
    clearTimeout(confirmTimer)
    confirmId.value = ''
    emit('remove', l)
    return
  }
  confirmId.value = l.id
  clearTimeout(confirmTimer)
  confirmTimer = setTimeout(() => (confirmId.value = ''), 4000)
}
onBeforeUnmount(() => clearTimeout(confirmTimer))
</script>

<template>
  <div class="space-y-3" data-testid="launcher-list" :data-tab="tabsShown ? activeTab : undefined">
    <!-- 本项目 / 全部（#140）：从会话页打开时才有；本项目 = 目录是这个会话的项目目录（或在它下面）的预设 -->
    <div v-if="tabsShown" class="flex items-center gap-1" role="tablist" aria-label="预设范围" data-testid="launcher-tabs">
      <button
        type="button"
        role="tab"
        :aria-selected="activeTab === 'project'"
        class="inline-flex cursor-pointer items-center gap-1 rounded-sm border px-2.5 py-1 text-xs transition-colors"
        :class="activeTab === 'project' ? 'border-primary/50 bg-primary/10 font-medium text-primary' : 'border-border/70 text-muted-foreground hover:bg-muted/50 hover:text-foreground'"
        :title="scope?.dir ? `目录在 ${scope.dir} 下的预设` : '这个会话没有上报项目目录，划分不出本项目'"
        data-testid="launcher-tab-project"
        @click="emit('update:tab', 'project')"
      >
        本项目<span v-if="projectLabel" class="max-w-[8rem] truncate font-mono text-[10px] opacity-70">{{ projectLabel }}</span>
        <span class="tabular-nums opacity-80" data-testid="launcher-tab-project-count">{{ counts.project }}</span>
      </button>
      <button
        type="button"
        role="tab"
        :aria-selected="activeTab === 'all'"
        class="inline-flex cursor-pointer items-center gap-1 rounded-sm border px-2.5 py-1 text-xs transition-colors"
        :class="activeTab === 'all' ? 'border-primary/50 bg-primary/10 font-medium text-primary' : 'border-border/70 text-muted-foreground hover:bg-muted/50 hover:text-foreground'"
        title="所有预设"
        data-testid="launcher-tab-all"
        @click="emit('update:tab', 'all')"
      >
        全部<span class="tabular-nums opacity-80" data-testid="launcher-tab-all-count">{{ counts.all }}</span>
      </button>
    </div>

    <!-- 不属于任何预设的非交互运行（#147）：有运行在进行时展开，让「明明在运行却看不到」不再发生；进行中的可以取消 -->
    <LauncherOtherRuns
      v-if="visibleOthers.length"
      :runs="visibleOthers"
      :hosts="hosts"
      :cancel-busy-id="cancelBusyId"
      @open-thread="(k: string) => emit('open-thread', k)"
      @cancel="(r: LauncherOtherRun) => emit('cancel-run', r)"
    />

    <!-- 这个会话还没有对应的续聊预设：一张预填好的建议，点一下确认才创建 -->
    <div
      v-if="showSuggest && suggest"
      class="space-y-2 rounded-md border border-violet-500/40 bg-violet-500/5 px-3 py-2.5"
      data-testid="launcher-suggest"
      :data-needs-host="suggest.needs_host ? 'true' : undefined"
    >
      <div class="flex items-start gap-2">
        <Bot class="mt-0.5 size-4 shrink-0 text-violet-600 dark:text-violet-400" aria-hidden="true" />
        <div class="min-w-0 flex-1 space-y-1">
          <p class="text-[13px] font-medium text-foreground">为这个会话创建续聊预设</p>
          <p class="break-all font-mono text-[11px] leading-relaxed text-muted-foreground" data-testid="launcher-suggest-summary">{{ suggestSummary(suggest) }}</p>
          <p class="text-[11px] leading-relaxed text-muted-foreground">
            创建后，这个会话停下来时输入区底部会出现「运行」按钮：一点就在这台主机上接着这个对话运行一次（不开终端），运行前会先请你确认。
          </p>
          <p v-if="suggest.needs_host" class="text-[11px] leading-relaxed text-amber-700 dark:text-amber-400" data-testid="launcher-suggest-needs-host">
            这台主机没法从会话信息里唯一确定（会话没上报主机名，或有多台同名主机）：请在「选主机并创建」里选一台。
          </p>
        </div>
      </div>
      <div class="flex flex-wrap items-center justify-end gap-2">
        <Button type="button" variant="ghost" size="sm" class="h-8 text-xs" :disabled="suggestBusy || !!busyId" data-testid="launcher-suggest-customize" @click="emit('customize-suggested')">
          {{ suggest.needs_host ? '选主机并创建' : '调整后创建' }}
        </Button>
        <Button
          v-if="!suggest.needs_host"
          type="button"
          size="sm"
          class="h-8 gap-1 text-xs"
          :disabled="suggestBusy || !!busyId || atLimit"
          :title="atLimit ? `最多 ${limits.max} 个预设` : '按上面的内容创建这条预设（需要再次验证密码）'"
          data-testid="launcher-suggest-create"
          @click="emit('create-suggested')"
        >
          <Loader2 v-if="suggestBusy" class="size-3.5 animate-spin" />
          <Plus v-else class="size-3.5" />
          {{ suggestBusy ? '创建中…' : '创建预设' }}
        </Button>
      </div>
    </div>

    <!-- 给不出建议：说明为什么（本项目标签里、没有对应预设时才有） -->
    <p
      v-else-if="showReason"
      class="flex items-start gap-1.5 rounded-md border border-border/70 bg-muted/20 px-3 py-2 text-xs leading-relaxed text-muted-foreground"
      data-testid="launcher-suggest-reason"
    >
      <Info class="mt-0.5 size-3.5 shrink-0" aria-hidden="true" />
      <span class="min-w-0 break-words">没法为这个会话创建续聊预设：{{ suggestReason }}</span>
    </p>

    <!-- 本项目里没有预设（也没有建议卡可看）：指向「全部」 -->
    <div
      v-if="tabsShown && activeTab === 'project' && !visibleRows.length && !showSuggest"
      class="space-y-1 rounded-md border border-dashed border-border/80 bg-muted/10 px-3 py-4 text-center"
      data-testid="launcher-project-empty"
    >
      <p class="text-[13px] font-medium text-foreground">{{ projectLabel ? `「${projectLabel}」下` : '这个项目下' }}还没有预设</p>
      <button type="button" class="cursor-pointer text-xs text-primary underline-offset-2 hover:underline" data-testid="launcher-project-empty-all" @click="emit('update:tab', 'all')">
        查看全部 {{ counts.all }} 个预设
      </button>
    </div>

    <div
      v-if="!launchers.length"
      class="flex flex-col items-center gap-3 rounded-md border border-dashed border-border/80 bg-muted/10 px-4 py-8 text-center"
      data-testid="launcher-empty"
    >
      <SquareTerminal class="size-6 text-muted-foreground" aria-hidden="true" />
      <div class="space-y-1">
        <p class="text-[13px] font-medium text-foreground">还没有启动预设</p>
        <p class="text-xs leading-relaxed text-muted-foreground">
          预设记住「哪台主机、哪个目录、启动后自动输入什么命令」。点一下就在那台主机上开一个终端会话并运行命令——比如在项目目录里启动 Cursor CLI。
        </p>
      </div>
      <div class="flex flex-wrap items-center justify-center gap-2">
        <Button type="button" size="sm" class="h-8 gap-1 text-xs" data-testid="launcher-create-cursor" @click="emit('create-cursor')">
          <Sparkles class="size-3.5" />添加「Cursor CLI」预设
        </Button>
        <Button type="button" variant="outline" size="sm" class="h-8 gap-1 text-xs" data-testid="launcher-create" @click="emit('create')">
          <Plus class="size-3.5" />新建预设
        </Button>
        <Button v-if="limits.runSupported" type="button" variant="ghost" size="sm" class="h-8 gap-1 text-xs" data-testid="launcher-create-headless" @click="emit('create-headless')">
          <Bot class="size-3.5" />新建非交互续聊
        </Button>
      </div>
    </div>

    <ul v-else-if="visibleRows.length" class="divide-y divide-border/60 rounded-md border border-border/70 bg-muted/10" data-testid="launcher-items">
      <li
        v-for="r in visibleRows"
        :key="r.l.id"
        class="space-y-2 px-3 py-2.5"
        :class="focusId === r.l.id ? 'bg-primary/5 ring-1 ring-inset ring-primary/50' : ''"
        :data-focused="focusId === r.l.id ? 'true' : undefined"
        :data-testid="`launcher-item-${r.l.id}`"
        :data-host-state="r.ready.ok ? 'ready' : r.ready.reason"
      >
        <div class="flex flex-col gap-2 sm:flex-row sm:items-start sm:gap-3">
          <div class="min-w-0 flex-1 space-y-1">
            <div class="flex min-w-0 items-center gap-2">
              <span class="truncate text-[13px] font-medium text-foreground" :title="r.l.name">{{ r.l.name }}</span>
              <span
                class="inline-flex shrink-0 items-center gap-1 text-[10px]"
                :class="r.ready.ok ? 'text-emerald-600 dark:text-emerald-400' : 'text-amber-600 dark:text-amber-400'"
              >
                <span class="size-1.5 rounded-full" :class="r.ready.ok ? 'bg-emerald-500' : 'bg-amber-500'" aria-hidden="true" />
                {{ r.ready.ok ? '在线' : r.ready.text }}
              </span>
              <span
                v-if="r.isRun"
                class="inline-flex shrink-0 items-center gap-0.5 rounded-xs border border-violet-500/40 bg-violet-500/10 px-1 py-px text-[9px] text-violet-700 dark:text-violet-400"
                title="非交互续聊：点「运行」由 Hub 在主机上接着原对话运行一次，不打开终端"
                data-testid="launcher-run-badge"
              ><Bot class="size-2.5" aria-hidden="true" />非交互续聊</span>
              <span
                v-if="r.isRun && r.l.run?.origin === 'enable_agent'"
                class="inline-flex shrink-0 items-center rounded-xs border border-primary/40 bg-primary/10 px-1 py-px text-[9px] text-primary"
                title="由会话页的「启用 Agent」创建：第一次运行时新开对话，之后接着它"
                data-testid="launcher-enable-badge"
              >由「启用 Agent」创建</span>
              <span
                v-if="r.l.guard?.exit_ends_session"
                class="inline-flex shrink-0 rounded-xs border border-sky-500/40 bg-sky-500/10 px-1 py-px font-mono text-[9px] text-sky-700 dark:text-sky-400"
                title="启动时用 exec 包装：命令退出即会话结束"
                data-testid="launcher-exec-badge"
              >exec</span>
            </div>
            <p class="flex min-w-0 items-center gap-1 font-mono text-[11px] text-muted-foreground" :title="`${r.hostLabel}${r.l.dir ? ' · ' + r.l.dir : ''}`">
              <Server class="size-3 shrink-0" aria-hidden="true" />
              <span class="truncate">{{ r.hostLabel }}<template v-if="r.l.dir"> · {{ r.l.dir }}</template></span>
            </p>
            <p v-if="r.isRun" class="flex min-w-0 gap-1 text-[11px] text-foreground/80" :title="runSummary(r.l)" data-testid="launcher-run-summary">
              <Bot class="mt-px size-3 shrink-0 text-violet-600 dark:text-violet-400" aria-hidden="true" /><span class="truncate">{{ runSummary(r.l) }}</span>
            </p>
            <p v-else-if="r.l.command" class="flex min-w-0 gap-1 font-mono text-[11px] text-foreground/80" :title="r.l.command">
              <span class="shrink-0 text-primary">$</span><span class="truncate">{{ r.l.command }}</span>
            </p>
            <p v-else class="text-[11px] italic text-muted-foreground">只打开一个终端，不自动输入命令</p>

            <!-- 非交互续聊：最近一次运行的现状（状态、多久以前、用时、最后一条回复） -->
            <div
              v-if="r.isRun"
              class="space-y-1 rounded-sm border border-border/60 bg-background/60 px-2 py-1.5"
              :data-testid="`run-status-${r.l.id}`"
              :data-run-state="r.last?.state ?? 'none'"
            >
              <template v-if="r.last && r.runStat">
                <p class="flex min-w-0 flex-wrap items-center gap-x-1.5 gap-y-0.5 text-[11px]">
                  <span class="inline-flex shrink-0 items-center gap-1 font-medium" :class="RUN_TONE_TEXT[r.runStat.tone]">
                    <BrailleSpinner v-if="r.runStat.busy" size="sm" />
                    <span v-else class="size-1.5 rounded-full" :class="RUN_TONE_DOT[r.runStat.tone]" aria-hidden="true" />
                    {{ r.runStat.label }}
                  </span>
                  <span class="text-muted-foreground">{{ runMeta(r.last) }}</span>
                  <button
                    v-if="r.last.thread_key"
                    type="button"
                    class="inline-flex cursor-pointer items-center gap-0.5 text-primary underline-offset-2 hover:underline"
                    title="在 ExecMesh 里查看这次运行的完整输出"
                    :data-testid="`run-open-${r.l.id}`"
                    @click="emit('open-thread', r.last.thread_key)"
                  >
                    <ExternalLink class="size-3" aria-hidden="true" />运行记录
                  </button>
                </p>
                <p
                  v-if="r.last.reply"
                  class="line-clamp-3 break-words text-[11px] leading-relaxed text-foreground/80"
                  :title="r.last.reply"
                  :data-testid="`run-reply-${r.l.id}`"
                >{{ r.last.reply }}</p>
                <p v-if="runHint(r.last)" class="text-[11px] leading-relaxed" :class="RUN_TONE_TEXT[r.runStat.tone === 'bad' ? 'bad' : 'warn']" :data-testid="`run-hint-${r.l.id}`">{{ runHint(r.last) }}</p>
                <!-- 失败原因：Cursor CLI 自己报的错误类别（可重试 / 要你处理 / 不可重试），以及你可以怎么办（#151） -->
                <div
                  v-if="r.runFailure"
                  class="space-y-0.5 text-[11px] leading-relaxed"
                  :title="r.last.failure?.raw || ''"
                  :data-testid="`run-failure-${r.l.id}`"
                  :data-failure-class="r.last.failure?.class"
                >
                  <p class="break-words text-destructive">{{ r.runFailure.text }}</p>
                  <p v-if="r.runFailure.hint" class="break-words text-muted-foreground">{{ r.runFailure.hint }}</p>
                </div>
                <!-- 自动续跑：排期中（可取消）/ 已续跑 / 你取消了 / 作废 / 放弃（#151）。图标与文字是一个整体：文字换行时图标留在第一行行首，取消按钮跟在文字后面。 -->
                <p
                  v-if="r.runAuto"
                  class="flex min-w-0 items-start gap-1 text-[11px] leading-relaxed"
                  :class="RUN_TONE_TEXT[r.runAuto.tone]"
                  :data-testid="`run-auto-${r.l.id}`"
                  :data-auto-state="r.last.auto?.state ?? ''"
                >
                  <RefreshCw class="mt-0.5 size-3 shrink-0" aria-hidden="true" />
                  <span class="min-w-0 break-words">
                    {{ r.runAuto.text }}
                    <button
                      v-if="r.runAuto.cancellable"
                      type="button"
                      class="ml-1.5 inline-flex cursor-pointer items-center gap-0.5 align-baseline text-primary underline-offset-2 hover:underline disabled:cursor-not-allowed disabled:opacity-60"
                      :disabled="autoBusyId === r.last.run_id"
                      title="取消这次计划中的自动续跑；你也可以直接点「运行」，那会取代这次计划"
                      :data-testid="`run-auto-cancel-${r.l.id}`"
                      @click="emit('cancel-auto', r.last)"
                    >
                      <Loader2 v-if="autoBusyId === r.last.run_id" class="size-3 animate-spin" aria-hidden="true" />
                      {{ autoBusyId === r.last.run_id ? '取消中…' : '取消自动续跑' }}
                    </button>
                  </span>
                </p>
              </template>
              <p v-else class="text-[11px] italic text-muted-foreground">还没运行过。点「运行」后，结果会显示在这里。</p>
            </div>

            <!-- 会话守护：服务端判断出的状态（意外结束 / 停摆的才需要处理，其余只是告诉你它现在怎么样） -->
            <p
              v-if="r.watched"
              class="flex min-w-0 flex-wrap items-center gap-x-1.5 gap-y-0.5 text-[11px]"
              :title="r.item?.verdict.detail?.join('\n') || ''"
              :data-testid="`guard-status-${r.l.id}`"
              :data-guard-state="r.item?.verdict.state ?? 'pending'"
            >
              <template v-if="r.item && r.view">
                <span class="inline-flex shrink-0 items-center gap-1 font-medium" :class="GUARD_TONE_TEXT[r.view.tone]">
                  <BrailleSpinner v-if="r.view.busy" size="sm" />
                  <span v-else class="size-1.5 rounded-full" :class="GUARD_TONE_DOT[r.view.tone]" aria-hidden="true" />
                  {{ r.view.label }}
                </span>
                <span class="min-w-0 text-muted-foreground">{{ r.item.verdict.reason }}</span>
                <span v-if="guardMeta(r.item)" class="text-muted-foreground/80">· {{ guardMeta(r.item) }}</span>
                <span v-if="r.item.permit_marked" class="text-sky-600/90 dark:text-sky-400/90" title="触发一次告警条件后会自动恢复守护">· 已标记完成</span>
              </template>
              <span v-else class="inline-flex items-center gap-1 text-muted-foreground"><Loader2 class="size-3 animate-spin" aria-hidden="true" />正在判断会话状态…</span>
            </p>

            <div v-if="r.running.length" class="flex flex-wrap items-center gap-1.5 pt-0.5" :data-testid="`launcher-running-${r.l.id}`">
              <span class="text-[11px] text-muted-foreground">已在运行 {{ r.running.length }} 个：</span>
              <button
                v-for="(s, i) in r.running"
                :key="s.sid"
                type="button"
                class="inline-flex cursor-pointer items-center gap-1 rounded-xs border border-primary/40 bg-primary/5 px-1.5 py-0.5 text-[11px] text-primary transition-colors hover:bg-primary/10"
                :data-testid="`launcher-resume-${r.l.id}-${i}`"
                :title="`连回这个会话（会话 ${s.sid.slice(0, 8)}）`"
                @click="emit('resume', r.l, s.sid)"
              >
                继续<span v-if="openedAgo(s.created)" class="text-muted-foreground">· {{ openedAgo(s.created) }}</span>
              </button>
            </div>
          </div>

          <div class="flex shrink-0 items-center gap-1.5">
            <Button
              v-if="r.watched && r.item"
              type="button"
              variant="ghost"
              size="sm"
              class="h-8 px-2 text-[11px]"
              :disabled="!!busyId || !!wakeBusyId || !!permitBusyId"
              :title="r.item.permit_marked ? '取消「任务全部完成」标记，恢复按约定告警' : '标记任务全部完成：本会话结束或停摆时不告警（触发一次后自动恢复）'"
              :data-testid="`guard-permit-${r.l.id}`"
              @click="emit('permit', r.item, r.item.permit_marked ? 1 : 0)"
            >
              <Loader2 v-if="permitBusyId === r.l.id" class="size-3.5 animate-spin" />
              {{ permitBusyId === r.l.id ? '…' : r.item.permit_marked ? '取消标记' : '标记完成' }}
            </Button>
            <Button
              v-if="r.item && r.wake"
              type="button"
              :variant="r.view?.tone === 'bad' ? 'default' : 'outline'"
              size="sm"
              class="h-8 gap-1 text-xs"
              :disabled="r.wake.disabled || !!busyId || !!wakeBusyId"
              :title="r.wake.title"
              :data-testid="`guard-wake-${r.l.id}`"
              @click="askWake(r.item)"
            >
              <Loader2 v-if="wakeBusyId === r.l.id" class="size-3.5 animate-spin" />
              <RotateCcw v-else-if="r.item.wake_mode === 'relaunch'" class="size-3.5" />
              <BellRing v-else class="size-3.5" />
              {{ wakeBusyId === r.l.id ? '唤醒中…' : r.wake.text }}
            </Button>
            <Button
              v-if="r.item && r.headless?.show"
              type="button"
              variant="outline"
              size="sm"
              class="h-8 gap-1 text-xs"
              :disabled="r.headless.disabled || !!busyId || !!wakeBusyId"
              :title="r.headless.title"
              :data-testid="`guard-headless-${r.l.id}`"
              @click="confirmHeadlessId = r.l.id"
            >
              <Bot class="size-3.5" />非交互唤醒
            </Button>
            <Button
              v-if="r.isRun && r.runReady"
              type="button"
              size="sm"
              class="h-8 gap-1 text-xs"
              :disabled="!r.runReady.ok || !!busyId || !!runBusyId"
              :title="r.runReady.ok ? '在主机上接着原对话运行一次（不打开终端）' : r.runReady.text"
              :data-testid="`launcher-run-${r.l.id}`"
              @click="emit('run', r.l)"
            >
              <Loader2 v-if="runBusyId === r.l.id" class="size-3.5 animate-spin" />
              <BrailleSpinner v-else-if="r.last?.active" />
              <Play v-else class="size-3.5" />
              {{ runBusyId === r.l.id ? '提交中…' : r.last?.active ? '运行中…' : '运行' }}
            </Button>
            <Button
              v-else
              type="button"
              size="sm"
              class="h-8 gap-1 text-xs"
              :disabled="!r.ready.ok || !!busyId || !!runBusyId"
              :title="r.ready.ok ? '新开一个会话并运行命令' : r.ready.text"
              :data-testid="`launcher-start-${r.l.id}`"
              @click="emit('start', r.l)"
            >
              <Loader2 v-if="busyId === r.l.id" class="size-3.5 animate-spin" />
              <Play v-else class="size-3.5" />
              {{ busyId === r.l.id ? '启动中…' : '启动' }}
            </Button>
            <Button
              type="button"
              variant="ghost"
              size="icon"
              class="size-8"
              aria-label="编辑预设"
              title="编辑预设"
              :disabled="!!busyId"
              :data-testid="`launcher-edit-${r.l.id}`"
              @click="emit('edit', r.l)"
            >
              <Pencil class="size-3.5" />
            </Button>
            <Button
              type="button"
              variant="ghost"
              :size="confirmId === r.l.id ? 'sm' : 'icon'"
              class="text-muted-foreground hover:text-destructive"
              :class="confirmId === r.l.id ? 'h-8 px-2 text-xs text-destructive' : 'size-8'"
              :aria-label="confirmId === r.l.id ? '确认删除这个预设' : '删除预设'"
              :title="confirmId === r.l.id ? '再点一次确认删除（已经在运行的会话不受影响）' : '删除预设'"
              :disabled="!!busyId"
              :data-testid="`launcher-remove-${r.l.id}`"
              @click="askRemove(r.l)"
            >
              <template v-if="confirmId === r.l.id">确认删除</template>
              <Trash2 v-else class="size-3.5" />
            </Button>
          </div>
        </div>

        <!-- 唤醒前的确认：会话看起来没出问题 / 唤醒太频繁 / 连着没有效果时，先说明为什么再问 -->
        <div
          v-if="r.item && confirmWakeId === r.l.id"
          class="space-y-2 rounded-sm border border-amber-500/40 bg-amber-500/5 px-3 py-2"
          role="alert"
          :data-testid="`guard-wake-confirm-${r.l.id}`"
        >
          <p class="text-[11px] font-medium text-amber-700 dark:text-amber-400">确定要{{ r.wake?.text }}「{{ r.l.name }}」吗？</p>
          <ul class="space-y-1">
            <li v-for="reason in confirmReasons" :key="reason" class="text-[11px] leading-relaxed text-amber-700/90 dark:text-amber-400/90">{{ reason }}</li>
          </ul>
          <div class="flex items-center justify-end gap-2">
            <Button type="button" variant="ghost" size="sm" class="h-7 text-xs" :data-testid="`guard-wake-cancel-${r.l.id}`" @click="confirmWakeId = ''">取消</Button>
            <Button type="button" size="sm" class="h-7 text-xs" :data-testid="`guard-wake-go-${r.l.id}`" @click="confirmWake(r.item)">仍要{{ r.wake?.text }}</Button>
          </div>
        </div>
        <!-- 非交互唤醒前的确认：它会在主机上运行一次模型，权限与用量都和平时的会话无关，所以总是先问 -->
        <div
          v-if="r.item && r.headless?.show && confirmHeadlessId === r.l.id"
          class="space-y-2 rounded-sm border border-amber-500/40 bg-amber-500/5 px-3 py-2"
          role="alert"
          :data-testid="`guard-headless-confirm-${r.l.id}`"
        >
          <p class="text-[11px] font-medium text-amber-700 dark:text-amber-400">确定要对「{{ r.l.name }}」做一次非交互唤醒吗？</p>
          <ul class="space-y-1">
            <li class="text-[11px] leading-relaxed text-amber-700/90 dark:text-amber-400/90">
              会在主机上接着原来的对话运行一次 <code class="font-mono">agent -p</code>，会消耗模型用量；不打开终端，结束后聊天里会有一条记录。
            </li>
            <li v-if="r.item.headless?.posture === 'force'" class="text-[11px] leading-relaxed text-amber-700/90 dark:text-amber-400/90">
              权限姿态是「放开全部命令」（<code class="font-mono">--force</code>）：它可以不经询问地运行任何命令。
            </li>
            <li v-else class="text-[11px] leading-relaxed text-amber-700/90 dark:text-amber-400/90">
              权限姿态是「只放行白名单内的操作」：它只能做主机上 CLI 配置的 <code class="font-mono">permissions.allow</code> 放行的事。
            </li>
            <li class="text-[11px] leading-relaxed text-amber-700/90 dark:text-amber-400/90">
              24 小时内已唤醒 {{ r.item.wakes_24h }}/{{ wakeMax }} 次；到达上限后不能再用这个方式唤醒。
            </li>
          </ul>
          <div class="flex items-center justify-end gap-2">
            <Button type="button" variant="ghost" size="sm" class="h-7 text-xs" :data-testid="`guard-headless-cancel-${r.l.id}`" @click="confirmHeadlessId = ''">取消</Button>
            <Button type="button" size="sm" class="h-7 text-xs" :data-testid="`guard-headless-go-${r.l.id}`" @click="goHeadless(r.item)">仍要非交互唤醒</Button>
          </div>
        </div>
      </li>
    </ul>

    <div v-if="launchers.length" class="flex flex-wrap items-center justify-between gap-2">
      <p v-if="!hostsLoaded" class="flex items-center gap-1 text-[11px] text-muted-foreground"><Loader2 class="size-3 animate-spin" />正在读取主机状态…</p>
      <p v-else class="text-[11px] text-muted-foreground">{{ launchers.length }} / {{ limits.max }} 个预设</p>
      <div class="flex flex-wrap items-center gap-2">
        <Button v-if="!hasCursor" type="button" variant="ghost" size="sm" class="h-8 gap-1 text-xs" data-testid="launcher-create-cursor" @click="emit('create-cursor')">
          <Sparkles class="size-3.5" />添加「Cursor CLI」
        </Button>
        <Button v-if="limits.runSupported" type="button" variant="ghost" size="sm" class="h-8 gap-1 text-xs" :disabled="atLimit" :title="atLimit ? `最多 ${limits.max} 个预设` : '新建一个由 Hub 在主机上接着原对话运行一次的预设（不打开终端）'" data-testid="launcher-create-headless" @click="emit('create-headless')">
          <Bot class="size-3.5" />新建非交互续聊
        </Button>
        <Button type="button" variant="outline" size="sm" class="h-8 gap-1 text-xs" :disabled="atLimit" :title="atLimit ? `最多 ${limits.max} 个预设` : ''" data-testid="launcher-create" @click="emit('create')">
          <Plus class="size-3.5" />新建预设
        </Button>
      </div>
    </div>
  </div>
</template>
