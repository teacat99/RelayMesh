<script setup lang="ts">
import { ref, computed, markRaw, shallowRef, onBeforeUnmount, onMounted, watch, type Component, type ShallowRef } from 'vue'
import { useRouter } from 'vue-router'
import { useMediaQuery } from '@vueuse/core'
import { useSessionStore } from '../../stores/session'
import { useSettingsStore } from '../../stores/settings'
import { useWorkItemStore } from '../../stores/workItems'
import { useProjectDocStore } from '../../stores/projectDocs'
import { useTerminalStore } from '../../stores/terminal'
import SessionStatusBadge from './SessionStatusBadge.vue'
import WorkflowSheetDrawer from './WorkflowSheetDrawer.vue'
import ConversationNotifyMenu from './ConversationNotifyMenu.vue'
import SessionHeaderMore from './SessionHeaderMore.vue'
import MobileMenuButton from '../shell/MobileMenuButton.vue'
import { TONE_CLASSES } from '../work/tone'
import {
  Sparkles,
  FolderGit2,
  Workflow,
  Copy,
  PanelLeftOpen,
  FileText,
  ListChecks,
  SquareTerminal
} from 'lucide-vue-next'
import { toast } from 'vue-sonner'
import { legacyDocsLocation, projectKbLocation } from '../../lib/docNav'
import { ownerProjectId, ownerProjectIdWithin, ownerProjectName } from '../../lib/docNavClient'
import { reportChunkFailure } from '../../utils/chunkFailure'
import { PHONE_MEDIA } from '../../lib/pendingSessions'

const props = defineProps<{
  sidebarCollapsed?: boolean
}>()

const emit = defineEmits<{
  (e: 'open-mobile-sidebar'): void
  (e: 'expand-sidebar'): void
  /** 状态徽标的待处理清单里点了「等回复的汇报」：让对话列表滚到那一轮。 */
  (e: 'jump-session', sessionId: string): void
}>()

const sessionStore = useSessionStore()
const settingsStore = useSettingsStore()
const workStore = useWorkItemStore()
const docStore = useProjectDocStore()
const terminalStore = useTerminalStore()
const router = useRouter()

const isSheetDrawerOpen = ref(false)

// 手机宽度：次要动作（提醒、工作表）收进「⋯」菜单，状态徽章挪到第二行，把页头宽度让给标题（#156 批 1）
const isPhone = useMediaQuery(PHONE_MEDIA)

// 「终端」入口：打开终端启动器（用预设的主机 / 目录 / 命令一键开一个终端会话，例如在项目目录里启动 Cursor CLI）。
// 只在服务端终端总闸开启时出现（与 ExecMesh 模式切换器同一个条件）；弹窗在第一次打开时才加载并挂载。
//
// 弹窗含终端窗格（xterm 等），做成懒加载的分块，不拖慢会话页本身。不用 defineAsyncComponent：它会把第一次失败的加载永久缓存，
// 之后每次点击都不再重试，页面开着期间热更新换掉了分块文件名时「终端」就一直点不开，只能刷新网页（#137）。
// 这里自己管：加载成功才缓存组件；失败不缓存、出一条带「刷新」的提示，下一次点击重新尝试。
const launcherOpen = ref(false)
const launcherComponent: ShallowRef<Component | null> = shallowRef(null)
let launcherLoading: Promise<boolean> | null = null
/** 从状态卡点「唤醒」进来时要定位的预设；从「终端」按钮进来时为空。 */
const launcherFocusId = ref('')
function ensureLauncher(): Promise<boolean> {
  if (launcherComponent.value) return Promise.resolve(true)
  launcherLoading ??= import('../terminal/launcher/TerminalLauncherDialog.vue')
    .then(m => {
      launcherComponent.value = markRaw(m.default)
      return true
    })
    .catch((err: unknown) => {
      if (!reportChunkFailure(err)) {
        console.error('[session-header] 终端窗口加载出错', err)
        toast.error('终端窗口加载出错', { description: err instanceof Error ? err.message : String(err) })
      }
      return false
    })
    .finally(() => {
      launcherLoading = null
    })
  return launcherLoading
}
async function openLauncher() {
  if (!(await ensureLauncher())) return
  launcherFocusId.value = ''
  launcherOpen.value = true
}
async function openLauncherFor(presetId: string) {
  if (!(await ensureLauncher())) return
  launcherFocusId.value = presetId
  launcherOpen.value = true
}

// 会话守护（D-214）：有预设开着监测、并且它的会话意外结束 / 停摆了，「终端」入口上亮一个红点，悬停写明是哪个、怎么了。
// 数据半分钟读一次（服务端一分钟才判断一次）；没有预设开着监测、或 Hub 太旧不认识时不发请求。
const guardNeeds = computed(() => terminalStore.guardNeeds)
// Agent 状态芯片搬到了输入区（#81）；它点「唤醒」时用窗口事件把预设递过来，由这里打开启动器。
function onWakeEvent(e: Event) {
  const id = (e as CustomEvent<{ presetId?: string }>).detail?.presetId
  if (id) openLauncherFor(id)
}
onMounted(() => {
  terminalStore.startGuardPolling()
  window.addEventListener('relaymesh:open-launcher', onWakeEvent)
})
onBeforeUnmount(() => {
  terminalStore.stopGuardPolling()
  window.removeEventListener('relaymesh:open-launcher', onWakeEvent)
})
// 预设是随终端状态异步到的：一旦发现有预设开着监测，马上读一次，不必等下一个半分钟。
watch(
  () => terminalStore.guardWatching,
  watching => {
    if (watching) void terminalStore.refreshGuard()
  },
  { immediate: true },
)
// 页头右侧按钮共用的盒子（#168）：PC 是「图标 + 文字」的小按钮；手机一律是 36×36 的方块、只留 16px 的图标，
// 免得 终端 / 任务 / 项目 / ⋯ 各有各的宽度与图标大小。切换点用 md（768px），与 isPhone / PHONE_MEDIA 同一条线。
// relative：数字角标与红点（任务的数字、终端的守护红点）都贴在按钮右上角。
const HEADER_BTN =
  'relative inline-flex items-center gap-1 px-2.5 py-1 max-md:size-9 max-md:justify-center max-md:gap-0 max-md:p-0 rounded-sm border text-[10px] sm:text-xs font-mono shrink-0 shadow-xs transition-colors cursor-pointer outline-none focus-visible:ring-1 focus-visible:ring-ring bg-muted/40 hover:bg-muted/80 hover:text-primary border-border/80'
const HEADER_ICON = 'size-4 md:size-3.5 text-primary'
const launcherTitle = computed(() => {
  const base = '终端启动器：一键在预设的主机和目录里开终端、运行命令（例如启动 Cursor CLI）'
  return guardNeeds.value.count ? `${base}\n\n需要你处理：\n${guardNeeds.value.title}` : base
})

// 「任务」入口：进入这个会话所属项目（workflow）的任务清单整页。
const sessionWorkflowId = computed(() => sessionStore.selectedSession?.workflow_id || sessionStore.currentSession?.workflow_id || '')

/**
 * 入口上的小数字：有在做的（进行中 + 验证中）就亮蓝色，否则是未结束的总数；一个任务也没有就不显示。
 * 任务不再需要你逐条确认，所以这里只是进度提示，不催你做什么。
 * 数据来自任务模块的项目概况（侧栏加载、实时事件刷新），这里不额外发请求。
 */
const workBadge = computed(() => {
  const stat = workStore.workflows.find(w => w.workflow_id === sessionWorkflowId.value)
  if (!stat) return null
  const review = stat.review ?? 0
  const doing = stat.doing ?? 0
  if (doing + review > 0) {
    return { n: doing + review, tone: TONE_CLASSES.sky.badge, title: review > 0 ? `${doing} 个任务进行中，${review} 个验证中` : `${doing} 个任务进行中` }
  }
  if (stat.active > 0) return { n: stat.active, tone: TONE_CLASSES.slate.badge, title: `${stat.active} 个任务未结束` }
  return null
})

function openWork() {
  if (!sessionWorkflowId.value) return
  void router.push({ name: 'work', params: { workflowId: sessionWorkflowId.value } })
}

// 「项目」入口（#131，取代原来的「文档」）：进入这个会话所属项目的知识库——文档、设计与决策都汇在那里。
// 角标沿用这个会话工作流的文档篇数。工作流还没归入项目时入口不消失：点它仍打开老的文档页，页内提示可以去项目页归入。
const docsBadge = computed(() => docStore.docCount(sessionWorkflowId.value))
const ownerId = ref(0)
const ownerName = ref('')

watch(sessionWorkflowId, async id => {
  ownerId.value = 0
  ownerName.value = ''
  if (!id) return
  void docStore.loadList(id)
  const pid = await ownerProjectId(id)
  if (sessionWorkflowId.value !== id) return // 等的过程中切走了
  ownerId.value = pid
  ownerName.value = pid > 0 ? ownerProjectName(id) : ''
}, { immediate: true })

const projectEntryTitle = computed(() =>
  ownerId.value > 0
    ? `打开项目${ownerName.value ? `「${ownerName.value}」` : ''}的知识库：这个会话的文档、设计与决策都汇在那里`
    : '这个会话还没归入项目：打开它的文档页（把目录归入项目后，文档会汇入项目知识库）',
)

async function openProject() {
  const wf = sessionWorkflowId.value
  if (!wf) return
  // 点的时候再确认一次归属（半分钟内走缓存；查不到就当没有项目——入口永远能打开点什么）
  const pid = await ownerProjectIdWithin(wf)
  void router.push(pid > 0 ? projectKbLocation(pid) : legacyDocsLocation(wf))
}

async function copyText(text: string, label: string) {
  if (!text) return
  try {
    await navigator.clipboard.writeText(text)
    toast.success(`已复制 ${label}`, {
      description: text,
      duration: 1800
    })
  } catch (_) {
    const input = document.createElement('textarea')
    input.value = text
    document.body.appendChild(input)
    input.select()
    document.execCommand('copy')
    document.body.removeChild(input)
    toast.success(`已复制 ${label}`, {
      description: text,
      duration: 1800
    })
  }
}

const displayProjectDirectory = computed(() => {
  const sess = sessionStore.selectedSession || sessionStore.currentSession
  if (!sess?.project_directory) return ''
  const host = sess.host_name || settingsStore.settings.hostName
  if (host && host.trim()) {
    return `${host.trim()}:${sess.project_directory}`
  }
  return sess.project_directory
})

</script>

<template>
  <!-- Top Workspace Header (高度 h-14 与左侧侧边栏保持严格一致) -->
  <header class="h-14 px-3 sm:px-6 border-b border-border/60 flex items-center justify-between gap-2 sm:gap-3 bg-background/80 backdrop-blur-xs shrink-0 z-20">
    <div class="flex items-center gap-2 min-w-0">
      <!-- PC Collapsed Sidebar Expand Button (PC 端侧边栏展开按钮) -->
      <button
        v-if="props.sidebarCollapsed"
        type="button"
        class="p-1.5 -ml-1.5 rounded-sm text-muted-foreground hover:text-foreground hover:bg-muted/70 hidden md:inline-flex shrink-0 cursor-pointer border border-border/80 mr-1"
        @click="emit('expand-sidebar')"
        title="展开侧边栏"
        data-testid="session-header-expand"
      >
        <PanelLeftOpen class="w-4 h-4" />
      </button>

      <!-- Mobile Sidebar Hamburger Toggle（别处有会话在等答复时带数字角标） -->
      <MobileMenuButton
        class="p-1.5 -ml-1.5 rounded-sm text-muted-foreground hover:text-foreground md:hidden shrink-0 max-md:p-2"
        @click="emit('open-mobile-sidebar')"
      />

      <div class="space-y-0.5 min-w-0">
        <div class="flex items-center gap-2 min-w-0">
          <Sparkles class="w-3.5 h-3.5 text-foreground shrink-0 hidden sm:inline-block" />
          <h2 class="text-xs sm:text-sm font-semibold text-foreground tracking-tight truncate sm:max-w-xs md:max-w-md">
            {{ sessionStore.selectedSession?.title || sessionStore.currentSession?.title || '人机交互方案会话流' }}
          </h2>
          <!-- 会话级状态徽章（D-273 阶段 D）：宽屏跟在标题后面；手机放到第二行（见下），免得挤掉标题 -->
          <SessionStatusBadge
            v-if="sessionWorkflowId && !isPhone"
            :workflow-id="sessionWorkflowId"
            class="text-[10px] sm:text-xs"
            @jump-session="emit('jump-session', $event)"
          />
        </div>
        <!-- overflow-hidden：窄屏下第二行放不下时让会话 ID 自己截断，别伸到右边的按钮底下被盖住 -->
        <div class="flex items-center gap-1.5 sm:gap-2 text-[10px] sm:text-xs text-muted-foreground min-w-0 overflow-hidden">
          <SessionStatusBadge
            v-if="sessionWorkflowId && isPhone"
            :workflow-id="sessionWorkflowId"
            class="text-[10px]"
            @jump-session="emit('jump-session', $event)"
          />
          <!-- 1. 工作区路径（有空间时自适应展开，支持主机名前缀与点击一键复制） -->
          <button
            v-if="displayProjectDirectory"
            type="button"
            class="hidden sm:inline-flex items-center gap-1 font-mono text-[10px] sm:text-[11px] text-muted-foreground hover:text-foreground hover:bg-muted/70 px-1.5 py-0.5 rounded cursor-pointer transition-colors max-w-xs md:max-w-md lg:max-w-lg truncate group"
            :title="`点击复制工作区路径: ${displayProjectDirectory}`"
            @click="copyText(displayProjectDirectory, '工作区路径')"
          >
            <FolderGit2 class="w-3 h-3 text-muted-foreground shrink-0 group-hover:text-primary transition-colors" />
            <span class="truncate">{{ displayProjectDirectory }}</span>
          </button>

          <!-- 2. Workflow ID（点击直接复制） -->
          <button
            v-if="sessionStore.selectedSession?.workflow_id"
            type="button"
            class="hidden md:inline-flex items-center gap-1 font-mono text-[10px] sm:text-[11px] text-muted-foreground hover:text-foreground hover:bg-muted/70 px-1.5 py-0.5 rounded cursor-pointer transition-colors shrink-0 group"
            :title="`点击复制 Workflow ID: ${sessionStore.selectedSession.workflow_id}`"
            @click="copyText(sessionStore.selectedSession.workflow_id, 'Workflow ID')"
          >
            <Workflow class="w-3 h-3 text-muted-foreground shrink-0 group-hover:text-primary transition-colors" />
            <span>{{ sessionStore.selectedSession.workflow_id }}</span>
          </button>

          <!-- 3. Session ID（点击直接复制） -->
          <button
            v-if="sessionStore.selectedSession?.session_id"
            type="button"
            class="inline-flex items-center gap-1 min-w-0 font-mono text-[10px] sm:text-[11px] bg-muted/80 hover:bg-accent hover:text-accent-foreground px-1.5 py-0.5 rounded cursor-pointer transition-colors group"
            :title="`点击复制会话 ID: ${sessionStore.selectedSession.session_id}`"
            @click="copyText(sessionStore.selectedSession.session_id, '会话 ID')"
          >
            <Copy class="w-2.5 h-2.5 shrink-0 text-muted-foreground group-hover:text-primary transition-colors" />
            <span class="truncate">{{ sessionStore.selectedSession.session_id }}</span>
          </button>
        </div>
      </div>
    </div>

    <!-- Session Status Controls & Time Display (执行/等待/剩余时间 · 空回执次数 · 提示词等待时长) -->
    <div class="flex items-center gap-1.5 sm:gap-2 shrink-0">
      <!-- 终端启动器入口：弹窗里选预设一键开终端会话（主机 / 目录 / 命令可配置），会话在 ExecMesh 里同样可见 -->
      <button
        v-if="terminalStore.gateOpen"
        type="button"
        :class="HEADER_BTN"
        :title="launcherTitle"
        data-testid="session-terminal-launcher"
        :data-guard-attention="guardNeeds.count"
        @click="openLauncher"
      >
        <SquareTerminal :class="HEADER_ICON" />
        <span class="hidden md:inline font-medium">终端</span>
        <!-- 有会话需要处理（意外结束 / 停摆）：红点；具体是哪个在悬停说明里，点开启动器能直接唤醒 -->
        <span
          v-if="guardNeeds.count"
          class="absolute -right-1 -top-1 size-2.5 rounded-full bg-destructive ring-2 ring-background"
          aria-hidden="true"
          data-testid="session-terminal-guard-dot"
        />
      </button>

      <!-- 任务清单入口：点击后整个会话页切换为这个项目的任务清单页 -->
      <button
        v-if="sessionWorkflowId"
        type="button"
        :class="HEADER_BTN"
        title="打开这个项目的任务清单"
        data-testid="session-work-entry"
        @click="openWork"
      >
        <ListChecks :class="HEADER_ICON" />
        <span class="hidden md:inline font-medium">任务</span>
        <!-- 手机：按钮只剩图标，数字改成贴在右上角的角标（不占按钮宽度，和终端的红点同一个位置） -->
        <span
          v-if="workBadge"
          class="min-w-4 rounded-xs border px-1 text-center text-[10px] leading-4 tabular-nums max-md:absolute max-md:-right-1.5 max-md:-top-1.5 max-md:bg-background max-md:ring-2 max-md:ring-background"
          :class="workBadge.tone"
          :title="workBadge.title"
          data-testid="session-work-badge"
        >{{ workBadge.n }}</span>
      </button>

      <!-- 项目入口：PC 是页头上的按钮；手机收进「⋯」菜单（见下），页头只留 终端 / 任务 / ⋯ 三个同样大小的按钮（#168） -->
      <button
        v-if="sessionWorkflowId && !isPhone"
        type="button"
        :class="HEADER_BTN"
        :title="projectEntryTitle"
        aria-label="项目"
        :data-project-id="ownerId || undefined"
        data-testid="session-project-entry"
        @click="openProject"
      >
        <FolderGit2 :class="HEADER_ICON" />
        <span class="hidden md:inline font-medium">项目</span>
        <span
          v-if="docsBadge"
          class="min-w-4 rounded-xs border px-1 text-center text-[10px] leading-4 tabular-nums border-border text-muted-foreground"
          :title="`这个工作流里有 ${docsBadge} 篇文档`"
          data-testid="session-project-badge"
        >{{ docsBadge }}</span>
      </button>

      <!-- 手机：项目、提醒策略与会话工作表收进「⋯」（#156 批 1、#168）；主动作终端 / 任务留在页头 -->
      <SessionHeaderMore
        v-if="isPhone && sessionWorkflowId"
        :workflow-id="sessionWorkflowId"
        :project-id="ownerId"
        :project-title="projectEntryTitle"
        :docs-count="docsBadge"
        @open-project="openProject"
        @open-sheet="isSheetDrawerOpen = true"
      />

      <!-- 这个会话的提醒策略（#115）：全部提醒 / 静音一段时间 / 一直静音 -->
      <ConversationNotifyMenu v-if="!isPhone && sessionWorkflowId" :workflow-id="sessionWorkflowId" />

      <!-- 工作表 (Workflow Sheet) 抽屉触发按钮 -->
      <button
        v-if="!isPhone && (sessionStore.selectedSession?.workflow_id || sessionStore.currentSession?.workflow_id)"
        type="button"
        :class="HEADER_BTN"
        title="打开会话工作表 (Workflow Sheet Drawer)"
        data-testid="session-sheet-entry"
        @click="isSheetDrawerOpen = true"
      >
        <FileText :class="HEADER_ICON" />
        <span class="hidden md:inline font-medium">工作表</span>
      </button>

      <!-- 执行状态（计时 · 空回执 · 提示词等待）已合并到输入区第二行的状态块（SessionExecChip），页头不再重复显示 -->
    </div>
  </header>

  <!-- Workflow Sheet Drawer (会话工作表看板) -->
  <WorkflowSheetDrawer
    v-model:open="isSheetDrawerOpen"
    :workflow-id="sessionStore.selectedSession?.workflow_id || sessionStore.currentSession?.workflow_id"
  />

  <!-- 终端启动器：第一次点「终端」时才加载并挂载 -->
  <component :is="launcherComponent" v-if="launcherComponent" v-model:open="launcherOpen" :focus-preset-id="launcherFocusId" :workflow-id="sessionWorkflowId" />
</template>
