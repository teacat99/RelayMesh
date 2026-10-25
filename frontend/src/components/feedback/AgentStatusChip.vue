<script setup lang="ts">
// 会话页头里的「Agent 状态身份卡」（#113，设计稿 04 §3，需求 W-1 / I-2 / S-1）。
//
// 头部徽标（#140）：状态圆点 + 图标 + 状态文字（「工作中 · 思考」）+ 运行时间与 token。
// 状态来源（运行输出 / 编辑器 hook / 终端画面）不放在徽标上（#159：常驻显示是噪音），点开身份卡才看得到。
// 手机上留圆点、图标和状态词，子活动收进悬停提示；名称只给读屏。点开是身份卡：
//   - 名称（可改，空 = 用目录名；同一批身份里重名的会自动显示成「主机:完整路径」）、完整路径、主机、客户端与版本；
//   - 状态与已持续多久，疑似卡住 / 疑似等批准这类软标记带着理由；
//   - 基础状态四项（S-1）：最后交互 / 最后查询 / 推送已收到 / 已读——只用 Hub 已有记录，没有 hook 也能显示；
//   - 最近工具、本轮用时、会话运行时长。
// 这个工作流从来没收到过 hook 汇报或 MCP 调用时整块不出现（收件箱芯片已经在提示「hook 未装」）。
// 数据与刷新节奏在 stores/agentState.ts；文字与颜色的翻译在 lib/agentState.ts。
//
// 占位（#246）：芯片要等实时流连上、取到 Agent 状态概览之后才有，生产上比第一屏晚到 0.2 ～ 1.5 秒，晚到的那一刻状态条里其余芯片
// 整体被推右一个芯片的宽度（实测 142 ～ 158px）。上次这个工作流有芯片时，概览还在路上就先占一个同样宽的位置
// （lib/shellHints.ts 记着上次量到的宽度），到了原地换成真芯片；概览取到而这个工作流没有会话，占位收起并忘掉旧宽度。
// 芯片宽度跟着状态文字走、不为子活动文字预留空白（Q74 选 b），所以占位只是个估计：状态没变时一动不动，变了只挪两者的差。
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch, type Component } from 'vue'
import { useMediaQuery, useResizeObserver } from '@vueuse/core'
import { Check, CircleDot, CircleOff, Hourglass, LoaderCircle, Moon, Pencil, ShieldQuestion, TriangleAlert, X } from 'lucide-vue-next'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import BrailleSpinner from '@/components/ui/spinner/BrailleSpinner.vue'
import { toast } from '@/components/ui/sonner'
import { useSessionStore } from '@/stores/session'
import { useAgentStateStore } from '@/stores/agentState'
import { isImeKey } from '@/lib/ime'
import { IDENTITY_NAME_MAX } from '@/lib/identity'
import { chipReservation, patchShellHint, readShellHint } from '@/lib/shellHints'
import {
  basicFacts,
  chipSpanText,
  clientText,
  dotToneClass,
  factText,
  screenFacts,
  sessionSpanText,
  shortConversationId,
  sourceNote,
  sourceText,
  stateView,
  textToneClass,
  tokensText,
  turnSpanText,
} from '@/lib/agentState'

const props = defineProps<{ workflowId: string }>()

const store = useAgentStateStore()
const sessionStore = useSessionStore()

const open = ref(false)
const now = ref(Date.now())
let clock: number | undefined

const entry = computed(() => store.entryFor(props.workflowId))
const session = computed(() => entry.value.primary)
const identity = computed(() => entry.value.identity)
const view = computed(() => stateView(session.value, now.value))
const label = computed(() => identity.value?.label || session.value?.identity_label || '')
const facts = computed(() =>
  basicFacts({ session: session.value, identity: identity.value, items: sessionStore.queuedFeedbacks }),
)
const client = computed(() =>
  clientText(identity.value?.client_kind || session.value?.client_kind, identity.value?.cursor_version || session.value?.cursor_version),
)
const pathText = computed(() => {
  const dir = identity.value?.project_directory || session.value?.project_directory || ''
  const host = identity.value?.host_name || session.value?.host_name || ''
  if (!dir) return host || '路径未知'
  return host ? `${host}:${dir}` : dir
})
const screen = computed(() => screenFacts(session.value))
/** 状态来源（#129）：运行输出 / 编辑器 hook / 终端画面；只在身份卡里显示（#159），旧数据没有来源就不显示这一行。 */
const stateSourceKey = computed(() => session.value?.state_source ?? '')
const stateSourceLabel = computed(() => sourceText(stateSourceKey.value))
const stateSourceNote = computed(() => sourceNote(stateSourceKey.value))
const turnSpan = computed(() => turnSpanText(session.value, now.value))
const sessionSpan = computed(() => sessionSpanText(session.value, now.value))
/** 徽标上只在「工作中」时带子活动，其余状态的补充（卡了多久、离线多久）放在悬停与卡片里，免得徽标太长。 */
const chipDetail = computed(() => (view.value.key === 'working' ? view.value.detail : ''))

// 状态用图标表示（#81）：工作中 / 等你 / 等批准 / 空闲 / 结束 / 异常 / 离线各一个；图标之后紧跟状态词（#140），子活动宽屏才露出。
// 「工作中」用盲文旋转符（#130，与命令行里的一致）；思考还是调工具宽屏直接写在状态词后面，窄屏看悬停提示与身份卡里的子状态。
const stateIcon = computed<{ icon: Component; cls: string }>(() => {
  switch (view.value.key) {
    case 'working':
      return { icon: BrailleSpinner, cls: 'text-primary' }
    case 'waiting_you':
      return { icon: Hourglass, cls: 'text-emerald-500 motion-safe:animate-pulse' }
    case 'waiting_approval':
      return { icon: ShieldQuestion, cls: 'text-amber-500 motion-safe:animate-pulse' }
    case 'idle':
      return { icon: CircleDot, cls: 'text-muted-foreground' }
    case 'ended':
    case 'aborted':
      return { icon: CircleOff, cls: 'text-muted-foreground' }
    case 'error':
    case 'stalled':
      return { icon: TriangleAlert, cls: 'text-destructive' }
    case 'offline':
      return { icon: Moon, cls: 'text-amber-500' }
    default:
      return { icon: LoaderCircle, cls: 'text-muted-foreground' }
  }
})
/**
 * 芯片上的运行时间与 token：有就显示、没有就不占位（token 只在能拿到时常驻，不依赖会话流程）。
 * 追加 #535（Q63 选 c）+ #582（更正为「仅对 hook 启用时生效」）+ #601（每个会话的 hook 状态各自独立）：这个会话的状态来源是
 * 编辑器 hook（启用了 hook）时，芯片不再放运行时间——会话时长只有 hook 报过会话开始的会话才有，有的芯片带一段时长、有的没有，
 * 看起来样式不一致；本轮用时与会话时长都在点开的身份卡里（agent-card-turn / agent-card-session-span）。
 * 其他来源的会话维持原规则（见 chipSpanText）。
 */
const chipSpan = computed(() => chipSpanText(session.value, now.value))
const chipTokens = computed(() => (typeof session.value?.tokens_live === 'number' ? tokensText(session.value.tokens_live) : ''))

// ---- 占位与宽度记忆（#246）----
const wide = useMediaQuery('(min-width: 640px)')
const chipEl = ref<HTMLElement | null>(null)
// 只在芯片还没出现、概览还没取到时才需要读记忆
const hint = computed(() => (session.value || store.loaded ? null : readShellHint(props.workflowId)))
const reservedWidth = computed(() =>
  chipReservation({
    hint: hint.value,
    wide: wide.value,
    hasSession: !!session.value,
    overviewLoaded: store.loaded,
    overviewFailed: !!store.error,
    supported: store.supported,
  }),
)
let rememberTimer: ReturnType<typeof setTimeout> | undefined
/** 量一次芯片现在的宽度并记下；状态文字变化会让宽度跟着变，等 600ms 稳定下来再写，免得每次变化都落盘。 */
function scheduleRemember() {
  clearTimeout(rememberTimer)
  rememberTimer = setTimeout(() => {
    const w = chipEl.value?.getBoundingClientRect().width ?? 0
    if (w > 0 && props.workflowId) patchShellHint(props.workflowId, { chip: { w, wide: wide.value } })
  }, 600)
}
useResizeObserver(chipEl, scheduleRemember)
watch(
  () => [props.workflowId, store.loaded, !!session.value] as const,
  ([workflowId, loaded, has]) => {
    if (!workflowId) return
    if (has) scheduleRemember()
    else if (loaded) patchShellHint(workflowId, { chip: null })
  },
  { immediate: true },
)

// ---- 改名 ----
const editing = ref(false)
const draft = ref('')
const saving = ref(false)
const inputRef = ref<HTMLInputElement | null>(null)
const draftLength = computed(() => [...draft.value].length)
const tooLong = computed(() => draftLength.value > IDENTITY_NAME_MAX)

function startEdit() {
  if (!identity.value) return
  draft.value = identity.value.name || ''
  editing.value = true
  void nextTick(() => inputRef.value?.focus())
}

function cancelEdit() {
  editing.value = false
}

async function saveEdit() {
  const id = identity.value?.id
  if (!id || saving.value || tooLong.value) return
  saving.value = true
  try {
    const saved = await store.rename(id, draft.value)
    // 改名在路上用户可能已经切到别的会话、在那边的名字框里写着：只收起自己这一个身份的编辑框
    if (identity.value?.id === id) editing.value = false
    toast.success(saved.name ? `已改名为「${saved.name}」` : '已恢复用目录名', {
      description: saved.label_degraded ? `有重名的身份，显示为 ${saved.label}` : undefined,
      duration: 2000,
    })
  } catch (err) {
    toast.error(err instanceof Error ? err.message : '改名失败')
  } finally {
    saving.value = false
  }
}

function onNameKey(e: KeyboardEvent) {
  if (isImeKey(e)) return // 输入法组合期间的回车是在确认候选词、Esc 是在取消组合，不是提交 / 放弃
  if (e.key === 'Enter') {
    e.preventDefault()
    void saveEdit()
  } else if (e.key === 'Escape') {
    e.preventDefault()
    e.stopPropagation() // 先取消改名，不要连浮层一起关掉
    cancelEdit()
  }
}

/** 改名过程中按 Esc 只放弃改名，不要连浮层一起关掉（放弃动作由输入框自己的 keydown 处理）。 */
function onEscapeKeyDown(e: KeyboardEvent) {
  if (editing.value) e.preventDefault()
}

// 点开的那一刻把卡片里的数据刷成最新：状态、最近交互 / 查询时间（身份）、推送与已读（收件箱）
watch(open, isOpen => {
  if (isOpen) {
    now.value = Date.now()
    void store.refreshWorkflow(props.workflowId)
    void store.refreshIdentities()
    void sessionStore.fetchQueuedFeedbacks(props.workflowId)
  } else {
    editing.value = false
  }
})

onMounted(() => {
  store.ensureFresh(15_000)
  clock = window.setInterval(() => {
    now.value = Date.now()
    store.ensureFresh()
  }, 5000)
})
onBeforeUnmount(() => {
  clearTimeout(rememberTimer)
  if (clock !== undefined) {
    window.clearInterval(clock)
    clock = undefined
  }
})
</script>

<template>
  <Popover v-if="session" v-model:open="open">
    <PopoverTrigger as-child>
      <button
        ref="chipEl"
        type="button"
        data-testid="agent-status-chip"
        :data-key="view.key"
        :data-tone="view.tone"
        :aria-label="`Agent 状态 ${label} ${view.text}`"
        :title="`${label} · ${view.text}${chipDetail ? ' · ' + chipDetail : ''}\n${view.title}`"
        class="inline-flex items-center gap-1.5 px-2 py-1 rounded-sm border text-[11px] sm:text-xs font-mono shrink-0 max-w-[16rem] sm:max-w-[30rem] bg-card/95 hover:bg-muted border-border/80 text-foreground cursor-pointer select-none outline-none focus-visible:ring-1 focus-visible:ring-ring transition-colors"
      >
        <span
          data-testid="agent-status-dot"
          class="inline-block w-1.5 h-1.5 rounded-full shrink-0"
          :class="dotToneClass(view.tone, view.pulse)"
        ></span>
        <component
          :is="stateIcon.icon"
          data-testid="agent-status-icon"
          class="w-3.5 h-3.5 shrink-0"
          :class="stateIcon.cls"
        />
        <!-- 名称只给读屏（同一页里通常只有一个会话，名字在身份卡里）；状态文字常驻，子活动宽屏才露出，窄屏看悬停提示；状态来源只在点开的身份卡里（#159） -->
        <span data-testid="agent-status-label" class="sr-only">{{ label }}</span>
        <span data-testid="agent-status-text" class="shrink-0 font-medium" :class="textToneClass(view.tone)">{{ view.text }}</span>
        <template v-if="chipDetail">
          <span class="hidden sm:inline text-muted-foreground/50" aria-hidden="true">·</span>
          <!-- 子活动文字（思考 / 回应 / 工具 Shell…）：宽度完全跟着文字走，不预留空白（用户确认项 Q74 选 b，2026-10-21）。
               #197 曾把它放进固定 10em 的盒子，让后面的芯片不被推着走；但那个盒子在 <button> 里继承了居中，两边各空一截（#222 / #833），
               用户宁可后面的芯片随文字挪动，也不要芯片里的空白。写不下才截断（文字本身最长约 17em，芯片自己有最大宽度；全文在悬停提示与身份卡里）。 -->
          <span data-testid="agent-status-detail" class="hidden sm:block truncate text-foreground/80">{{ chipDetail }}</span>
        </template>
        <template v-if="chipSpan">
          <span class="text-muted-foreground/50">·</span>
          <span data-testid="agent-status-span" class="tabular-nums text-foreground/90">{{ chipSpan }}</span>
        </template>
        <template v-if="chipTokens">
          <span class="text-muted-foreground/50">·</span>
          <span data-testid="agent-status-tokens" class="tabular-nums text-muted-foreground">{{ chipTokens }}</span>
        </template>
        <span
          v-if="view.flag === 'stalled_soft'"
          data-testid="agent-status-flag"
          class="shrink-0 text-amber-600 dark:text-amber-400"
          :title="view.flagText"
        >?</span>
      </button>
    </PopoverTrigger>

    <!-- data-testid 放在里面的容器上：PopoverContent 的根是 Teleport，挂在它上面的属性不会落到 DOM 里 -->
    <PopoverContent
      align="start"
      class="w-[min(24rem,calc(100vw-1.5rem))] max-h-[calc(var(--radix-popover-content-available-height)-0.5rem)] overflow-y-auto overscroll-contain p-3 font-mono text-[11px] sm:text-xs"
      @escape-key-down="onEscapeKeyDown"
    >
      <div data-testid="agent-status-card" :data-key="view.key" class="space-y-2.5">
        <!-- 名称（可改） -->
        <div class="min-w-0">
          <div v-if="!editing" class="flex items-center gap-1.5 min-w-0">
            <span data-testid="agent-card-name" class="font-semibold text-foreground truncate" :title="label">{{ label }}</span>
            <button
              v-if="identity"
              type="button"
              data-testid="agent-card-rename"
              class="shrink-0 p-0.5 rounded-xs text-muted-foreground hover:text-foreground hover:bg-muted cursor-pointer outline-none focus-visible:ring-1 focus-visible:ring-ring"
              title="改名（留空 = 用目录名）"
              aria-label="改名"
              @click="startEdit"
            >
              <Pencil class="w-3 h-3" />
            </button>
          </div>
          <p
            v-if="!editing && identity?.label_degraded"
            data-testid="agent-card-degraded"
            class="mt-0.5 text-[10px] text-muted-foreground/80"
          >有重名的身份，所以显示成「主机:完整路径」；给它改个名就能分开。</p>

          <div v-if="editing" class="space-y-1">
            <div class="flex items-center gap-1.5">
              <input
                ref="inputRef"
                v-model="draft"
                data-testid="agent-card-rename-input"
                type="text"
                autocomplete="off"
                spellcheck="false"
                :placeholder="identity?.directory_name || '未知项目'"
                aria-label="Agent 名称"
                class="min-w-0 flex-1 rounded-xs border bg-background px-1.5 py-0.5 text-foreground outline-none focus-visible:ring-1 focus-visible:ring-ring"
                :class="tooLong ? 'border-destructive' : 'border-border'"
                @keydown="onNameKey"
              />
              <button
                type="button"
                data-testid="agent-card-rename-save"
                class="shrink-0 p-1 rounded-xs border border-border hover:bg-muted cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed outline-none focus-visible:ring-1 focus-visible:ring-ring"
                :disabled="saving || tooLong"
                title="保存（Enter）"
                aria-label="保存名称"
                @click="saveEdit"
              >
                <Check class="w-3 h-3" />
              </button>
              <button
                type="button"
                data-testid="agent-card-rename-cancel"
                class="shrink-0 p-1 rounded-xs border border-border hover:bg-muted cursor-pointer outline-none focus-visible:ring-1 focus-visible:ring-ring"
                :disabled="saving"
                title="取消（Esc）"
                aria-label="取消改名"
                @click="cancelEdit"
              >
                <X class="w-3 h-3" />
              </button>
            </div>
            <p class="text-[10px]" :class="tooLong ? 'text-destructive' : 'text-muted-foreground/80'" data-testid="agent-card-rename-hint">
              <template v-if="tooLong">名字最多 {{ IDENTITY_NAME_MAX }} 个字（现在 {{ draftLength }}）</template>
              <template v-else>留空 = 用目录名「{{ identity?.directory_name || '未知项目' }}」</template>
            </p>
          </div>
        </div>

        <!-- 路径与客户端 -->
        <div class="text-muted-foreground space-y-0.5">
          <div data-testid="agent-card-path" class="break-all" :title="pathText">{{ pathText }}</div>
          <div data-testid="agent-card-client">{{ client }}</div>
        </div>

        <!-- 状态 -->
        <div class="pt-2 border-t border-border/60 space-y-1" data-testid="agent-card-state" :data-key="view.key">
          <div class="flex items-center gap-2 min-w-0">
            <BrailleSpinner v-if="view.key === 'working'" size="sm" class="text-primary" data-testid="agent-card-state-spinner" />
            <span v-else class="inline-block w-2 h-2 rounded-full shrink-0" :class="dotToneClass(view.tone, view.pulse)"></span>
            <span class="font-semibold" :class="textToneClass(view.tone)" data-testid="agent-card-state-text">{{ view.text }}</span>
            <span v-if="view.detail" class="text-muted-foreground truncate" data-testid="agent-card-state-detail">· {{ view.detail }}</span>
          </div>
          <p v-if="view.flagText" class="text-amber-600 dark:text-amber-400" data-testid="agent-card-flag">{{ view.flagText }}</p>
          <p
            v-if="stateSourceLabel"
            class="text-[10px] text-muted-foreground"
            :title="stateSourceNote"
            data-testid="agent-card-state-source"
            :data-source="stateSourceKey"
          >
            状态来源：{{ stateSourceLabel }}
          </p>
          <p v-if="entry.others > 0" class="text-muted-foreground" data-testid="agent-card-others">
            这个工作流里还有 {{ entry.others }} 个会话在活动；这里显示的是最近有动静的一个。
          </p>
        </div>

        <!-- 基础状态四项（S-1） -->
        <dl class="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 pt-2 border-t border-border/60" data-testid="agent-card-facts">
          <dt class="text-muted-foreground" title="最近一次任何 hook 事件或 MCP 调用">最后交互</dt>
          <dd class="text-foreground tabular-nums" data-testid="agent-card-fact-interaction">{{ factText(facts.lastInteractionAt, now) }}</dd>
          <dt class="text-muted-foreground" title="最近一次只看不改的调用：收件箱、会话列表、文档读取等">最后查询</dt>
          <dd class="text-foreground tabular-nums" data-testid="agent-card-fact-query">{{ factText(facts.lastQueryAt, now) }}</dd>
          <dt class="text-muted-foreground" title="你追加的指令被 hook 提醒或回执提示行带到 Agent 面前的最近时间">推送已收到</dt>
          <dd class="text-foreground tabular-nums" data-testid="agent-card-fact-push">
            {{ factText(facts.pushReceivedAt, now) }}<span v-if="facts.queued > 0" class="text-amber-600 dark:text-amber-400"> · {{ facts.queued }} 条排队中</span>
          </dd>
          <dt class="text-muted-foreground" title="Agent 取走追加指令全文的最近时间">已读</dt>
          <dd class="text-foreground tabular-nums" data-testid="agent-card-fact-read">{{ factText(facts.readAt, now) }}</dd>
        </dl>

        <!-- 运行细节 -->
        <dl class="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 pt-2 border-t border-border/60" data-testid="agent-card-details">
          <dt class="text-muted-foreground">最近工具</dt>
          <dd class="text-foreground truncate" data-testid="agent-card-tool">{{ session?.last_tool || '—' }}</dd>
          <dt class="text-muted-foreground">本轮用时</dt>
          <dd class="text-foreground tabular-nums" data-testid="agent-card-turn">{{ turnSpan || '—' }}</dd>
          <dt class="text-muted-foreground">会话时长</dt>
          <dd class="text-foreground tabular-nums" data-testid="agent-card-session-span">{{ sessionSpan || '—' }}</dd>
          <dt class="text-muted-foreground">会话</dt>
          <dd class="text-foreground" :title="session?.conversation_id" data-testid="agent-card-conversation">{{ shortConversationId(session?.conversation_id) || '—' }}</dd>
          <!-- 完整的对话编号单独占一行（#164）：续聊命令 agent --resume <编号> 要用它；点一下整段选中，方便复制 -->
          <dd
            v-if="session?.conversation_id"
            class="col-span-2 -mt-0.5 break-all text-foreground/80 select-all cursor-text"
            data-testid="agent-card-conversation-full"
          >{{ session.conversation_id }}</dd>
        </dl>

        <!-- 托管终端的屏幕信号（M3.2）：只有在 RelayMesh 启动的终端里跑 CLI 才有 -->
        <dl
          v-if="screen.tui === 'ok'"
          class="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 pt-2 border-t border-border/60"
          data-testid="agent-card-screen"
        >
          <dt class="text-muted-foreground" title="状态行里的实时计数；只有画面在动时才有">实时 token</dt>
          <dd class="text-foreground tabular-nums" data-testid="agent-card-tokens">{{ screen.tokens || '—' }}</dd>
          <dt class="text-muted-foreground" title="空闲页脚里的上下文占用百分比">上下文占用</dt>
          <dd class="text-foreground tabular-nums" data-testid="agent-card-ctx">{{ screen.ctx || '—' }}</dd>
          <dt v-if="screen.word" class="text-muted-foreground">画面</dt>
          <dd v-if="screen.word" class="text-foreground" data-testid="agent-card-word">{{ screen.word }}</dd>
        </dl>
        <p v-else-if="screen.tui === 'unrecognized'" class="pt-2 border-t border-border/60 text-[10px] text-muted-foreground" data-testid="agent-card-tui-note">
          {{ screen.note }}
        </p>
      </div>
    </PopoverContent>
  </Popover>
  <!-- 占位（#246）：概览还在路上，位置先占住；盒子的边、内边距、字号与真芯片一致（高度由里面不可见的一个字撑出来），不可点、对读屏隐藏 -->
  <span
    v-else-if="reservedWidth > 0"
    data-testid="agent-status-chip-reserved"
    aria-hidden="true"
    class="inline-flex shrink-0 items-center rounded-sm border border-border/50 bg-muted/30 px-2 py-1 font-mono text-[11px] sm:text-xs max-w-[16rem] sm:max-w-[30rem] motion-safe:animate-pulse"
    :style="{ width: `${reservedWidth}px` }"
  ><span class="invisible">0</span></span>
</template>
