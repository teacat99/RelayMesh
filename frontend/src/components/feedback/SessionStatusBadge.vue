<script setup lang="ts">
// 会话页头的「会话级状态」徽标（#120 · D-273 阶段 D）：经典视图与预览视图共用（它们共用同一个 SessionHeader）。
//
// 状态怎么推出来见 lib/sessionStatus.ts：等待您 · N 项 → 智能体进行中 → 已取消 / 已超时 → 空闲 → 已完成（已归档原样）。
// 这里只负责取数与呈现：
//   - 「等待您」是一个按钮，点开列出等您处理的东西（等回复的汇报、待答复的提问 / 计划、文档里的决策），点一条定位到对话里的位置；
//     列表与跳转沿用输入区「待答复」芯片的做法（同样靠 confirm store 的 focusRef 定位；汇报用页头抛出的 jump-session 事件）。
//   - 其余状态是只读的徽标，悬停说明写明依据。
// 每半分钟重算一次：智能体十分钟内的动作过期后，状态会自己从「进行中」落回「空闲 / 已完成」，不必等任何事件。
// 取数的几路（待确认项、任务计数、智能体最近的动作、Agent 状态）没到齐之前不显示，免得刷新页面时先闪一个错的状态（见 sourcesReady）。
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useNow } from '@vueuse/core'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { cn } from '@/lib/utils'
import Badge from '../ui/badge/Badge.vue'
import { badgeVariants } from '../ui/badge/variants'
import { TONE_CLASSES } from '../work/tone'
import { useSessionStore } from '../../stores/session'
import { useConfirmStore } from '../../stores/confirms'
import { useWorkItemStore } from '../../stores/workItems'
import { useAgentStateStore } from '../../stores/agentState'
import { stateView } from '../../lib/agentState'
import { deriveSessionStatus } from '../../lib/sessionStatus'
import { laterIso } from '../../lib/sidebarStatus'
import { latestWorkflowRow, workflowRoundStatus } from '../../lib/newSession'
import { KIND_LABEL, RISK_SHORT, ageText, riskTone } from '../../lib/confirms'
import { groupDecisions, needsAnswer } from '../../lib/timeline'
import { openDoc as navigateToDoc } from '../../lib/docNavClient'
import { BookOpen } from 'lucide-vue-next'

const props = defineProps<{
  workflowId: string
  /** 调用方按场地（页头标题行 / 手机第二行）给的字号等外观类。 */
  class?: string
}>()

const emit = defineEmits<{
  (e: 'jump-session', sessionId: string): void
}>()

const sessionStore = useSessionStore()
const confirmStore = useConfirmStore()
const workStore = useWorkItemStore()
const agentStore = useAgentStateStore()
const router = useRouter()

const now = useNow({ interval: 30_000 })

const session = computed(() => sessionStore.selectedSession)
// 这个工作流里创建时间最新的一行（「待接入」只在最新一行还是占位会话时成立：智能体接入后，页头停在占位会话那一行也不该再说待接入）
const latestRow = computed(() =>
  latestWorkflowRow(
    session.value,
    sessionStore.currentWorkflowSessions.length > 0 ? sessionStore.currentWorkflowSessions : sessionStore.sessions,
  ),
)

// 确认项只在 store 正在关注这个工作流、且已加载完时才算数（切换工作流的一瞬间别拿上一个的数据）
const confirmsReady = computed(() => confirmStore.loaded && confirmStore.workflowId === props.workflowId)
const items = computed(() =>
  confirmsReady.value ? confirmStore.board.items.filter(needsAnswer).sort((a, b) => a.seq - b.seq) : [],
)
// 提问 / 计划：对话里各有一张卡；文档里的决策按文档汇总，去文档页作答
const asks = computed(() => items.value.filter(i => i.kind !== 'decision'))
const docGroups = computed(() => groupDecisions(items.value))

const agentKey = computed(() => {
  const entry = agentStore.entryFor(props.workflowId)
  return entry.primary ? stateView(entry.primary, now.value.getTime()).key : null
})
const openTasks = computed(() => workStore.workflows.find(w => w.workflow_id === props.workflowId)?.active ?? 0)
// 智能体最近的动作：收件箱轮询与会话列表聚合各知道一份，取较晚的——与侧栏会话卡同一口径（#159，见 lib/sidebarStatus.ts），两处不会各说各话
const agentActivityAt = computed(() =>
  laterIso(sessionStore.agentLastActivityAt, sessionStore.workflowAggregates[props.workflowId]?.agentActivityAt),
)

// 数据是分几路晚到的（会话、待确认项、任务计数、智能体最近的动作、Agent 状态）：没到齐之前不报结论，否则刷新页面时会先闪一下
// 「空闲 / 已完成」再变成「等待您」。任何一路出错、一直到不了时，3 秒后用手上有的数据照常显示（宁可晚一点出现，也不先报一个错的状态）。
const SETTLE_MS = 3000
const settled = ref(false)
let settleTimer: ReturnType<typeof setTimeout> | null = null
watch(
  () => props.workflowId,
  () => {
    settled.value = false
    if (settleTimer) clearTimeout(settleTimer)
    settleTimer = setTimeout(() => {
      settled.value = true
    }, SETTLE_MS)
  },
  { immediate: true },
)
onBeforeUnmount(() => {
  if (settleTimer) clearTimeout(settleTimer)
})
// 页面里没有别处在拉的时候自己拉一次（比如侧栏没挂载）；已经取到过就不重复
onMounted(() => {
  agentStore.ensureFresh()
  if (!workStore.countsLoaded) void workStore.refreshCounts()
})
const sourcesReady = computed(
  () =>
    confirmsReady.value &&
    workStore.countsLoaded &&
    sessionStore.inboxLoadedFor === props.workflowId &&
    (agentStore.loaded || !agentStore.supported),
)

const status = computed(() =>
  sourcesReady.value || settled.value
    ? deriveSessionStatus({
        // 网页新建、智能体还没接入的会话（工作流里最新的一行是占位会话，#174）：轮次状态翻成「待接入」
        roundStatus: workflowRoundStatus(latestRow.value, session.value?.status ?? ''),
        pendingItems: items.value.length,
        agentActivityAt: agentActivityAt.value,
        agentState: agentKey.value,
        openTasks: openTasks.value,
        nowMs: now.value.getTime(),
      })
    : null,
)

const reportPending = computed(() => session.value?.status === 'pending')

const open = ref(false)
// 不再「等待您」时收起面板；否则 v-model 还停在 true，下次又有待处理时会自己弹开
watch(
  () => status.value?.key,
  key => {
    if (key !== 'waiting') open.value = false
  },
)

// 外观：waiting / working 带色调，空闲偏灰，其余走 Badge 变体（沿用原来页头徽标的样子）
const toneClass = computed(() => {
  const tone = status.value?.tone
  if (tone === 'amber') return TONE_CLASSES.amber.badge
  if (tone === 'sky') return TONE_CLASSES.sky.badge
  if (tone === 'muted') return 'text-muted-foreground'
  return ''
})
const baseClass = 'font-normal rounded-xs shrink-0'

/** 定位到对话里的那张卡：先清空再赋值，同一张卡连点两次也会再次触发滚动与高亮（列表会先把窗口撑到它所在的位置）。 */
async function jumpAsk(ref: string) {
  open.value = false
  confirmStore.focusRef = ''
  await nextTick()
  confirmStore.focusRef = ref
}

function jumpReport() {
  const id = session.value?.session_id
  if (!id) return
  open.value = false
  emit('jump-session', id)
}

function openDoc(docPath: string) {
  if (!docPath) return
  open.value = false
  void navigateToDoc(router, { workflowId: props.workflowId, path: docPath })
}
</script>

<template>
  <!-- 等待您：可点的按钮，点开是待处理清单 -->
  <Popover v-if="status && status.key === 'waiting'" v-model:open="open">
    <PopoverTrigger as-child>
      <button
        type="button"
        :class="cn(badgeVariants({ variant: status.variant }), toneClass, baseClass, 'cursor-pointer select-none outline-none hover:bg-amber-500/20 focus-visible:ring-1 focus-visible:ring-ring', props.class)"
        :title="status.title"
        :aria-label="status.title"
        data-testid="session-status-badge"
        :data-status-key="status.key"
        :data-count="status.count"
      >
        <i class="size-1.5 shrink-0 animate-pulse rounded-full bg-amber-500 motion-reduce:animate-none" aria-hidden="true" />
        {{ status.label }}
      </button>
    </PopoverTrigger>

    <PopoverContent align="start" class="w-[min(26rem,calc(100vw-1.5rem))] p-3 font-mono text-[11px] sm:text-xs">
      <div data-testid="session-status-popover" class="space-y-2.5">
        <div class="font-semibold text-foreground">
          等您处理 · {{ status.count }} 项<span class="font-normal text-muted-foreground/70"> · 点一条定位到对话里的位置</span>
        </div>

        <ul class="space-y-1">
          <li v-if="reportPending && session">
            <button
              type="button"
              data-testid="session-status-report"
              class="w-full cursor-pointer rounded-sm border border-border/70 px-2 py-1.5 text-left transition-colors hover:bg-muted"
              @click="jumpReport"
            >
              <span class="flex flex-wrap items-center gap-x-1.5 gap-y-0.5">
                <span class="font-semibold text-foreground/80">汇报</span>
                <span class="text-[10px] text-muted-foreground">等回复</span>
                <span class="ml-auto text-[10px] text-muted-foreground">{{ ageText(session.created_at) }}</span>
              </span>
              <span class="mt-0.5 block break-words font-sans text-[12px] leading-snug text-foreground">{{ session.title || '智能体的方案汇报' }}</span>
            </button>
          </li>

          <li v-for="it in asks" :key="it.seq">
            <button
              type="button"
              :data-testid="`session-status-item-${it.ref}`"
              class="w-full cursor-pointer rounded-sm border border-border/70 px-2 py-1.5 text-left transition-colors hover:bg-muted"
              @click="jumpAsk(it.ref)"
            >
              <span class="flex flex-wrap items-center gap-x-1.5 gap-y-0.5">
                <span class="font-semibold text-foreground/80">{{ it.ref }}</span>
                <span class="text-[10px] text-muted-foreground">{{ KIND_LABEL[it.kind] }}</span>
                <span
                  v-if="riskTone(it.risk) !== 'muted'"
                  class="rounded-xs border px-1 text-[10px]"
                  :class="riskTone(it.risk) === 'danger' ? 'border-destructive/50 text-destructive' : 'border-amber-500/40 text-amber-700 dark:text-amber-300'"
                >{{ RISK_SHORT[it.risk] }}</span>
                <span class="ml-auto text-[10px] text-muted-foreground">{{ ageText(it.created_at) }}</span>
              </span>
              <span class="mt-0.5 block break-words font-sans text-[12px] leading-snug text-foreground">{{ it.title }}</span>
            </button>
          </li>

          <li v-for="g in docGroups" :key="g.key">
            <button
              type="button"
              :data-testid="`session-status-doc-${g.docPath || g.key}`"
              class="w-full cursor-pointer rounded-sm border border-border/70 px-2 py-1.5 text-left transition-colors hover:bg-muted"
              @click="openDoc(g.docPath)"
            >
              <span class="flex flex-wrap items-center gap-x-1.5 gap-y-0.5">
                <BookOpen class="size-3 shrink-0 text-muted-foreground" />
                <span class="text-[10px] text-muted-foreground">文档决策</span>
                <span class="ml-auto text-[10px] text-muted-foreground">{{ g.pending }} 项待决策 · 去文档页作答</span>
              </span>
              <span class="mt-0.5 block break-words font-sans text-[12px] leading-snug text-foreground">《{{ g.docTitle }}》</span>
            </button>
          </li>
        </ul>
      </div>
    </PopoverContent>
  </Popover>

  <!-- 其余状态：只读徽标，悬停写明依据 -->
  <Badge
    v-else-if="status"
    :variant="status.variant"
    :class="cn(baseClass, toneClass, props.class)"
    :title="status.title"
    data-testid="session-status-badge"
    :data-status-key="status.key"
    :data-count="status.count"
  >
    <i
      v-if="status.pulse"
      class="size-1.5 shrink-0 animate-pulse rounded-full bg-sky-500 motion-reduce:animate-none"
      aria-hidden="true"
    />
    {{ status.label }}
  </Badge>
</template>
