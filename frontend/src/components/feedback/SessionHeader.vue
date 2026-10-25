<script setup lang="ts">
import { ref, computed, markRaw, shallowRef, onBeforeUnmount, watch, type Component, type ShallowRef } from 'vue'
import { useRouter } from 'vue-router'
import { useMediaQuery } from '@vueuse/core'
import { useSessionStore } from '../../stores/session'
import { useSettingsStore } from '../../stores/settings'
import { useWorkItemStore } from '../../stores/workItems'
import { useProjectDocStore } from '../../stores/projectDocs'
import { useTerminalStore } from '../../stores/terminal'
import { useBranchStore } from '../../stores/branches'
import SessionStatusBadge from './SessionStatusBadge.vue'
import WorkflowSheetDrawer from './WorkflowSheetDrawer.vue'
import ConversationNotifyMenu from './ConversationNotifyMenu.vue'
import SessionHeaderMore from './SessionHeaderMore.vue'
import BranchHeaderChip from '../branch/BranchHeaderChip.vue'
import MobileMenuButton from '../shell/MobileMenuButton.vue'
import { useLazyComponent } from '../../composables/useLazyComponent'
import { TONE_CLASSES } from '../work/tone'
import {
  Sparkles,
  FolderGit2,
  Workflow,
  Copy,
  PanelLeftOpen,
  FileText,
  GitBranch,
  ListChecks,
  SquareTerminal
} from 'lucide-vue-next'
import { toast } from 'vue-sonner'
import { legacyDocsLocation, projectKbLocation, resolveOwnerPatiently, retryWithBackoff } from '../../lib/docNav'
import { ownerProjects, ownerProjectIdWithin, ownerProjectName } from '../../lib/docNavClient'
import { reportChunkFailure } from '../../utils/chunkFailure'
import { PHONE_MEDIA } from '../../lib/pendingSessions'
import { branchChipView } from '../../lib/branch'
import type { FeedbackSession } from '../../api/types'

const props = defineProps<{
  sidebarCollapsed?: boolean
}>()

const emit = defineEmits<{
  (e: 'open-mobile-sidebar'): void
  (e: 'expand-sidebar'): void
  /** 状态徽标的待处理清单里点了「等回复的汇报」：让对话列表滚到那一轮。 */
  (e: 'jump-session', sessionId: string): void
  /** 分支会话（#236）：从分支芯片 / 分支面板跳到另一个工作流的会话页（主会话或某个分支）。 */
  (e: 'open-workflow', workflowId: string): void
  /** 分支会话（#236）：新建分支之后进入它（按会话编号定位）。 */
  (e: 'open-session', sessionId: string): void
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
  launcherOpen.value = true
}

// 页头右侧按钮共用的盒子（#168）：PC 是「图标 + 文字」的小按钮；手机一律是 36×36 的方块、只留 16px 的图标，
// 免得 终端 / 任务 / 项目 / ⋯ 各有各的宽度与图标大小。切换点用 md（768px），与 isPhone / PHONE_MEDIA 同一条线。
// relative：数字角标（任务的数字）贴在按钮右上角。
const HEADER_BTN =
  'relative inline-flex items-center gap-1 px-2.5 py-1 max-md:size-9 max-md:justify-center max-md:gap-0 max-md:p-0 rounded-sm border text-[10px] sm:text-xs font-mono shrink-0 shadow-xs transition-colors cursor-pointer outline-none focus-visible:ring-1 focus-visible:ring-ring bg-muted/40 hover:bg-muted/80 hover:text-primary border-border/80'
const HEADER_ICON = 'size-4 md:size-3.5 text-primary'
const launcherTitle = '续聊预设：点「运行」由 Hub 在预设的主机和目录里接着原对话运行一次（不开终端）；也能在这里看到其他非交互运行'

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

// 「分支」入口与标签（#236）：把一块工作拆成独立的分支会话。入口按钮（手机上在「⋯」里）始终在；
// 标签只在这个会话有分支关系时出现（分支页上写「分支 · 主会话「X」」，主会话上写「N 个分支」），没用到分支的会话页头上不多任何标签。
// 关系表由分支仓库统一取（切到会话时 viewWorkflow 会确保取过一次），实时流校准；没有关系的会话切过去不发额外的消息流请求。
const branchStore = useBranchStore()
const branchPanelOpen = ref(false)
const newBranchOpen = ref(false)
// 分支面板与新建分支窗口是点开才用的弹窗：做成懒加载的分块，第一次点的时候才取（加载与失败重试见 useLazyComponent）
const { component: BranchPanelDialog, ensure: ensureBranchPanel } = useLazyComponent(() => import('../branch/BranchPanel.vue'), '分支面板')
const { component: NewBranchWindow, ensure: ensureNewBranch } = useLazyComponent(() => import('../branch/NewBranchDialog.vue'), '新建分支窗口')
async function openBranchPanel() {
  if (await ensureBranchPanel()) branchPanelOpen.value = true
}
async function openNewBranch() {
  if (await ensureNewBranch()) newBranchOpen.value = true
}
const sessionTitle = computed(() => sessionStore.selectedSession?.title || sessionStore.currentSession?.title || '')
const branchOf = computed(() => (sessionWorkflowId.value ? branchStore.asBranch(sessionWorkflowId.value) : undefined))
const activeBranchCount = computed(() =>
  sessionWorkflowId.value ? branchStore.asMain(sessionWorkflowId.value).filter(b => b.status === 'active').length : 0,
)
const branchChip = computed(() => branchChipView(branchOf.value, activeBranchCount.value))
const branchEntryTitle = computed(() =>
  activeBranchCount.value > 0
    ? `分支会话：这个会话有 ${activeBranchCount.value} 个进行中的分支`
    : branchOf.value
      ? '分支会话：这个会话是一个分支，可以改它向主会话开放多少、结束它'
      : '分支会话：把这个会话里的一块工作拆出去，交给另一个智能体单独做',
)
watch(sessionWorkflowId, wf => void branchStore.viewWorkflow(wf), { immediate: true })

function onBranchChip() {
  const parent = branchOf.value?.parent_workflow_id
  if (parent) emit('open-workflow', parent)
  else void openBranchPanel()
}

function onBranchOpened(session: FeedbackSession) {
  emit('open-session', session.session_id)
}

// 「项目」入口（#131，取代原来的「文档」）：进入这个会话所属项目的知识库——文档、设计与决策都汇在那里。
// 角标沿用这个会话工作流的文档篇数。工作流还没归入项目时入口不消失：点它仍打开老的文档页，页内提示可以去项目页归入。
const docsBadge = computed(() => docStore.docCount(sessionWorkflowId.value))
const ownerId = ref(0)
const ownerName = ref('')

// 篇数与所属项目都要靠网络查：浏览器在网络瞬断时会把在途请求一起掐掉，只试一次就会一直缺着（#208）。
// 失败后按退避再试几次（切走工作流或页头卸载就不再试）；点「项目」时本来就会再查一次归属，入口始终能打开。
let headerGone = false
onBeforeUnmount(() => {
  headerGone = true
})
watch(sessionWorkflowId, async id => {
  ownerId.value = 0
  ownerName.value = ''
  if (!id) return
  const stillWanted = () => !headerGone && sessionWorkflowId.value === id // 等的过程中切走了 / 页头没了
  void retryWithBackoff(async () => {
    await docStore.loadList(id)
    return true
  }, { stillWanted })
  const pid = await resolveOwnerPatiently(ownerProjects, id, { stillWanted })
  if (!stillWanted()) return
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
          <!-- 分支标签（#236）：只在有分支关系时出现；宽屏跟在状态徽章后面，手机放到第二行（见下） -->
          <BranchHeaderChip v-if="branchChip && !isPhone" :chip="branchChip" @click="onBranchChip" />
        </div>
        <!-- overflow-hidden：窄屏下第二行放不下时让会话 ID 自己截断，别伸到右边的按钮底下被盖住 -->
        <div class="flex items-center gap-1.5 sm:gap-2 text-[10px] sm:text-xs text-muted-foreground min-w-0 overflow-hidden">
          <SessionStatusBadge
            v-if="sessionWorkflowId && isPhone"
            :workflow-id="sessionWorkflowId"
            class="text-[10px]"
            @jump-session="emit('jump-session', $event)"
          />
          <BranchHeaderChip v-if="branchChip && isPhone" :chip="branchChip" @click="onBranchChip" />
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
        @click="openLauncher"
      >
        <SquareTerminal :class="HEADER_ICON" />
        <span class="hidden md:inline font-medium">终端</span>
      </button>

      <!-- 任务清单入口：点击后整个会话页切换为这个项目的任务清单页 -->
      <button
        v-if="sessionWorkflowId"
        type="button"
        :class="HEADER_BTN"
        title="打开这个项目的任务清单"
        data-testid="session-work-entry"
        data-prefetch="work"
        @click="openWork"
      >
        <ListChecks :class="HEADER_ICON" />
        <span class="hidden md:inline font-medium">任务</span>
        <!-- 手机：按钮只剩图标，数字改成贴在右上角的角标（不占按钮宽度） -->
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
        data-prefetch="projects"
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

      <!-- 分支入口（#236）：PC 是页头上的按钮；手机收进「⋯」菜单（见下） -->
      <button
        v-if="sessionWorkflowId && !isPhone"
        type="button"
        :class="HEADER_BTN"
        :title="branchEntryTitle"
        aria-label="分支"
        data-testid="session-branch-entry"
        @click="openBranchPanel"
      >
        <GitBranch :class="HEADER_ICON" />
        <span class="hidden md:inline font-medium">分支</span>
        <span
          v-if="activeBranchCount"
          class="min-w-4 rounded-xs border px-1 text-center text-[10px] leading-4 tabular-nums border-border text-muted-foreground"
          :title="`${activeBranchCount} 个进行中的分支`"
          data-testid="session-branch-badge"
        >{{ activeBranchCount }}</span>
      </button>

      <!-- 手机：项目、提醒策略与会话工作表收进「⋯」（#156 批 1、#168）；主动作终端 / 任务留在页头 -->
      <SessionHeaderMore
        v-if="isPhone && sessionWorkflowId"
        :workflow-id="sessionWorkflowId"
        :project-id="ownerId"
        :project-title="projectEntryTitle"
        :docs-count="docsBadge"
        :branch-count="activeBranchCount"
        @open-project="openProject"
        @open-sheet="isSheetDrawerOpen = true"
        @open-branch="openBranchPanel"
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
  <component :is="launcherComponent" v-if="launcherComponent" v-model:open="launcherOpen" :workflow-id="sessionWorkflowId" />

  <!-- 分支面板与新建分支（#236）：第一次点开时才加载并挂载（懒加载分块） -->
  <component
    :is="BranchPanelDialog"
    v-if="BranchPanelDialog && sessionWorkflowId"
    v-model:open="branchPanelOpen"
    :workflow-id="sessionWorkflowId"
    @open-workflow="emit('open-workflow', $event)"
    @new-branch="openNewBranch"
  />
  <component
    :is="NewBranchWindow"
    v-if="NewBranchWindow && sessionWorkflowId"
    v-model:open="newBranchOpen"
    :parent-workflow-id="sessionWorkflowId"
    :parent-title="sessionTitle"
    @opened="onBranchOpened"
  />
</template>
