<script setup lang="ts">
import { computed, nextTick, reactive, ref, watch, type Component } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useMediaQuery } from '@vueuse/core'
import { toast } from 'vue-sonner'
import {
  Archive,
  ArchiveRestore,
  Bot,
  Columns2,
  Copy,
  Ellipsis,
  EyeOff,
  History,
  Loader2,
  Lock,
  LockOpen,
  Moon,
  Pencil,
  PanelLeftClose,
  Pin,
  PinOff,
  Plus,
  Radar,
  ScrollText,
  Search,
  Server,
  Settings,
  SquareTerminal,
  Sun,
  X,
} from 'lucide-vue-next'
import Button from '../ui/button/Button.vue'
import { ContextMenu, ContextMenuContent, ContextMenuItem, ContextMenuSeparator, ContextMenuTrigger } from '../ui/context-menu'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuSeparator, DropdownMenuTrigger } from '../ui/dropdown-menu'
import SwipeRow from './SwipeRow.vue'
import ThemeAutoIcon from '../ThemeAutoIcon.vue'
import ProjectSwitcher from '../ProjectSwitcher.vue'
import ConfirmDialog from '../terminal/ConfirmDialog.vue'
import { useExecStore } from '../../stores/exec'
import { useExecRunsStore } from '../../stores/execRuns'
import { useSettingsStore } from '../../stores/settings'
import { useTerminalStore } from '../../stores/terminal'
import { useThemeStore } from '../../stores/theme'
import { UnlockCancelled, type HostView } from '../../api/terminal'
import { EXEC_FILTERS, type ExecItem } from '../../lib/terminal/execList'
import { EXEC_ARCHIVE_PATH, EXEC_HOME, EXEC_HOSTS_PATH, execSessionPath, execThreadPath } from '../../lib/mode'
import { formatCountdown, hostTitle, relativeTime } from '../../lib/terminal/format'
import { canHumanTerminal, canOpenSshTerminal, isSshHost, sshKindBadge, sshTerminalRecorded } from '../../lib/terminal/hosts'
import { idleSummary } from '../../lib/terminal/lifecycle'
import { endReasonLabel, recStateInfo } from '../../lib/terminal/record'
import { isImeKey } from '../../lib/ime'

// ExecMesh 的左侧栏：终端会话是清单里的普通条目（和 RelayMesh 的会话列表一样按活动日期排列），
// 点开后右侧才显示详细内容。顶部品牌区是项目切换器，底部是主机、接入、审计、解锁等工具。

const route = useRoute()
const router = useRouter()
const exec = useExecStore()
const execRuns = useExecRunsStore()
const terminal = useTerminalStore()
const themeStore = useThemeStore()
const settingsStore = useSettingsStore()

const activeSid = computed(() => (typeof route.params.sid === 'string' ? route.params.sid : ''))
const activeThread = computed(() => (typeof route.params.key === 'string' ? route.params.key : ''))

function isActive(it: ExecItem): boolean {
  return it.model ? it.model.threadKey === activeThread.value : it.sid === activeSid.value
}

/**
 * 模型执行没有启用、也没有历史线程时，不显示“模型 / 待批准”两个筛选，界面与以前一致；
 * “已归档”只在确实有归档条目（或正停在这个筛选上）时出现，没用过归档的人看不到多余的按钮。
 */
const filters = computed(() =>
  EXEC_FILTERS.filter(f => {
    if (f.id === 'model' || f.id === 'awaiting') return execRuns.status?.enabled || execRuns.threads.length > 0
    if (f.id === 'archived') return exec.archivedInfo.total > 0 || exec.filter === 'archived'
    return true
  }),
)

function closeMobile() {
  exec.mobileSidebarOpen = false
}

// ---------- 新建 ----------
const onlineHosts = computed(() => terminal.hosts.filter(h => h.status === 'enabled' && h.online && canHumanTerminal(h)).sort((a, b) => hostTitle(a).localeCompare(hostTitle(b), 'zh-CN')))
// 只要有已启用的主机（Agent 或 SSH）就不显示「还没有接入主机」；此刻能不能新建由「新建终端」按钮自己说明。
const hasEnabledHost = computed(() => terminal.hosts.some(h => h.status === 'enabled'))
// 此刻能打开人工终端的 SSH 主机（Hub 直连与经网关一视同仁）：菜单里与 Agent 主机并列供选。
const sshHosts = computed(() => terminal.hosts.filter(h => canOpenSshTerminal(h)).sort((a, b) => hostTitle(a).localeCompare(hostTitle(b), 'zh-CN')))
// 无痕只对有记录的会话有意义：Hub 直连的 SSH 会话本来就不记录，不出现在无痕清单里。
const sshIncognitoHosts = computed(() => sshHosts.value.filter(sshTerminalRecorded))

/** 默认在当前会话所在的主机上新建；那台不能新建就用第一台在线的 Agent 主机，再不行用第一台可连的 SSH 主机。 */
const defaultHost = computed<HostView | undefined>(() => {
  const cur = activeSid.value
    ? exec.items.find(i => !i.model && i.sid === activeSid.value)
    : activeThread.value
      ? exec.items.find(i => i.model?.threadKey === activeThread.value)
      : undefined
  const same = cur ? (onlineHosts.value.find(h => h.machine_code === cur.code) ?? sshHosts.value.find(h => h.machine_code === cur.code)) : undefined
  return same ?? onlineHosts.value[0] ?? sshHosts.value[0]
})

/** 「新建终端」按钮：目标是 SSH 主机时走 SSH 打开流程（要现场连接，比本机 shell 慢一些）。 */
function createDefault() {
  const h = defaultHost.value
  if (!h) return
  if (isSshHost(h)) void createSsh(h)
  else void createOn(h.machine_code)
}

const creating = ref(false)
async function createOn(code: string, incognito = false) {
  if (creating.value) return
  creating.value = true
  try {
    const s = await terminal.createSession(code, undefined, undefined, { incognito })
    if (incognito) toast.success('已新建无痕会话', { description: '这个会话的内容不会被记录。' })
    void router.push(execSessionPath(s.sid))
    closeMobile()
  } catch (e) {
    terminal.notifyError(e, incognito ? '新建无痕会话失败' : '新建会话失败')
  } finally {
    creating.value = false
  }
}

/** 为一台 SSH 主机打开终端（Hub 或网关要现场拨号、认证，所以比本机 shell 慢一些）。 */
async function createSsh(h: HostView, incognito = false) {
  if (creating.value) return
  creating.value = true
  try {
    const s = await terminal.openSshTerminal(h, { incognito })
    if (incognito) toast.success('已打开无痕终端', { description: '这个会话的内容不会被记录。' })
    void router.push(execSessionPath(s.sid))
    closeMobile()
  } catch (e) {
    terminal.notifyError(e, incognito ? '打开无痕 SSH 终端失败' : '打开 SSH 终端失败')
  } finally {
    creating.value = false
  }
}

// ---------- 条目 ----------
function select(it: ExecItem) {
  void router.push(it.model ? execThreadPath(it.model.threadKey) : execSessionPath(it.sid))
  closeMobile()
}

/** 这个会话此刻是否附着在本浏览器里（在 live 集合内）。 */
function isOpenHere(it: ExecItem): boolean {
  return terminal.tabs.some(t => !t.replay && t.key === it.key) && exec.liveOrder.includes(it.key)
}

// 并排需要宽屏；窄屏不提供。
const isNarrow = useMediaQuery('(max-width: 767px)')
const canSplit = computed(() => !isNarrow.value)

/** 菜单「在右侧并排打开」：把会话放进右窗格，当前会话留在左边。 */
function openOnRight(it: ExecItem) {
  const key = terminal.openTabBackground(it.code, it.sid)
  exec.splitRequest = key
  if (route.name !== 'exec' && route.name !== 'exec-session') void router.push(EXEC_HOME)
}

function dotClass(it: ExecItem): string {
  if (!it.running) return 'bg-muted-foreground/40'
  return isOpenHere(it) ? 'bg-emerald-500' : 'border border-emerald-500'
}

function dotLabel(it: ExecItem): string {
  if (!it.running) return '已结束'
  return isOpenHere(it) ? '已连接' : '运行中（未连接）'
}

function recBadge(it: ExecItem) {
  const st = recStateInfo(it.rec)
  return st && st.tone !== 'ok' ? st : null
}

/** 第二行：主机，加上状态（空闲回收的设置、结束原因、主机离线、其他设备的连接）。模型线程：线程标签、进行中数量、最近的命令。 */
function subtitle(it: ExecItem): string {
  if (it.model) {
    const parts = [it.model.summary || `共 ${it.model.total} 次调用`]
    if (!it.hostOnline) parts.push('主机离线')
    return parts.join(' · ')
  }
  const parts = [it.hostName]
  if (it.running) {
    if (it.idle > 0) parts.push(idleSummary(it.idle))
    const others = it.attached - (isOpenHere(it) ? 1 : 0)
    if (others > 0) parts.push(`${others} 个其他连接`)
    if (!it.hostOnline) parts.push('主机离线')
  } else {
    parts.push(it.live ? '已退出' : endReasonLabel(it.endedReason))
  }
  return parts.join(' · ')
}

function timeText(it: ExecItem): string {
  return it.lastActive ? relativeTime(new Date(it.lastActive).toISOString(), exec.now) : ''
}

function itemTitle(it: ExecItem): string {
  const when = it.lastActive ? new Date(it.lastActive).toLocaleString('zh-CN', { hour12: false }) : ''
  return [it.title, subtitle(it), when && `最近活动 ${when}`, '右键：置顶 / 重命名 / 归档 / 复制 ID'].filter(Boolean).join('\n')
}

// ---------- 重命名 ----------
const editingKey = ref('')
const draft = ref('')
const renameInput = ref<HTMLInputElement[] | HTMLInputElement | null>(null)

async function startRename(it: ExecItem) {
  editingKey.value = it.key
  draft.value = it.name
  await nextTick()
  const el = Array.isArray(renameInput.value) ? renameInput.value[0] : renameInput.value
  el?.focus()
  el?.select()
}

// 输入法组合期间的回车是在确认候选词、Esc 是在取消组合：都不能当成「提交 / 放弃重命名」，否则中文名字打不完。
function onRenameEnter(ev: KeyboardEvent, it: ExecItem) {
  if (isImeKey(ev)) return
  ev.preventDefault()
  void commitRename(it)
}

function onRenameEsc(ev: KeyboardEvent) {
  if (isImeKey(ev)) return
  ev.preventDefault()
  editingKey.value = ''
}

async function commitRename(it: ExecItem) {
  if (editingKey.value !== it.key) return
  editingKey.value = ''
  const name = draft.value.trim()
  if (name === it.name) return
  if (it.model) {
    exec.renameThread(it.model.threadKey, name)
    return
  }
  try {
    await terminal.renameSession(it.code, it.sid, name)
  } catch (e) {
    terminal.notifyError(e, '重命名失败')
  }
}

// ---------- 归档 ----------
function archive(it: ExecItem) {
  void exec.archiveItems([{ key: it.key, title: it.title }])
}

function unarchive(it: ExecItem) {
  void exec.unarchiveItems([it.key])
}

// ---------- 右滑操作条（触摸设备；桌面用右键菜单） ----------
interface TrayAction {
  id: string
  label: string
  icon: Component
  /** 主操作（归档）用强调色。 */
  primary?: boolean
  run: () => void
}

/** 操作条每个按钮的宽度（px）；整条宽度 = 按钮数 × 这个值。 */
const TRAY_BUTTON_PX = 60

const canRename = (it: ExecItem) => !!it.model || (it.live && it.running)

/** 右滑露出的操作：置顶、重命名（能改名时）、归档 / 取消归档、复制 ID；其余低频操作留在长按菜单里。 */
function trayActions(it: ExecItem): TrayAction[] {
  const list: TrayAction[] = [{ id: 'pin', label: it.pinned ? '取消置顶' : '置顶', icon: it.pinned ? PinOff : Pin, run: () => exec.togglePin(it.key) }]
  if (canRename(it)) list.push({ id: 'rename', label: '重命名', icon: Pencil, run: () => void startRename(it) })
  list.push(
    it.archived
      ? { id: 'archive', label: '取消归档', icon: ArchiveRestore, run: () => unarchive(it) }
      : { id: 'archive', label: '归档', icon: Archive, primary: true, run: () => archive(it) },
  )
  list.push({ id: 'copy', label: '复制 ID', icon: Copy, run: () => void copyId(it) })
  return list
}

const trayWidth = (it: ExecItem) => trayActions(it).length * TRAY_BUTTON_PX

/** 当前展开操作条的那一行（同一时间只展开一行）。 */
const swipeKey = ref('')

function setSwipe(key: string, open: boolean) {
  if (open) swipeKey.value = key
  else if (swipeKey.value === key) swipeKey.value = ''
}

function runTray(it: ExecItem, a: TrayAction) {
  swipeKey.value = ''
  a.run()
}

/** 点到别的行或列表空白处，展开的操作条自动收起。 */
function onListPointerDown(e: PointerEvent) {
  if (!swipeKey.value) return
  const row = (e.target as HTMLElement | null)?.closest<HTMLElement>('[data-swipe-key]')
  if (row?.dataset.swipeKey !== swipeKey.value) swipeKey.value = ''
}

// 切换筛选 / 搜索、收起移动端侧栏时也收起操作条，免得下次打开还停在上次的样子。
watch(
  () => [exec.filter, exec.query, exec.mobileSidebarOpen],
  () => {
    swipeKey.value = ''
  },
)

// ---------- 其他操作 ----------
async function copyId(it: ExecItem) {
  const id = it.model ? it.model.threadKey : it.sid
  try {
    await navigator.clipboard.writeText(id)
    toast.success(it.model ? '已复制线程 ID' : '已复制会话 ID')
  } catch {
    toast.error('复制失败，请手动选择复制', { description: id })
  }
}

function viewRecord(it: ExecItem) {
  void router.push(execSessionPath(it.sid, true))
  closeMobile()
}

const canRecord = (it: ExecItem) => (it.live ? it.rec === 'on' || it.rec === 'paused' || it.rec === 'error' : it.replayable)

const confirm = reactive({ open: false, busy: false, code: '', sid: '', title: '', description: '', button: '' })

function askKill(it: ExecItem) {
  Object.assign(confirm, {
    open: true,
    code: it.code,
    sid: it.sid,
    title: it.running ? `终止会话「${it.title}」？` : `清除会话「${it.title}」？`,
    description: it.running ? '会话里正在运行的程序会被结束，未保存的内容会丢失。' : '把这个已退出的会话从主机的会话列表里清除；它的记录仍然保留，可以在「已结束」里回放。',
    button: it.running ? '终止会话' : '清除',
  })
}

async function runConfirm() {
  confirm.busy = true
  try {
    await terminal.killSession(confirm.code, confirm.sid)
    confirm.open = false
    if (activeSid.value === confirm.sid && !terminal.tabs.some(t => t.sid === confirm.sid && t.replay)) void router.replace('/exec')
  } catch (e) {
    if (e instanceof UnlockCancelled) return
    terminal.notifyError(e, confirm.button + '失败')
    confirm.open = false
  } finally {
    confirm.busy = false
  }
}

// ---------- 底部工具 ----------
function openSettings() {
  closeMobile()
  if (window.innerWidth < 768) void router.push({ path: '/settings', query: { module: 'exec' } })
  else settingsStore.openSettings()
}

function openDiscovery() {
  closeMobile()
  exec.discoveryOpen = true
}

function openAudit() {
  closeMobile()
  exec.auditOpen = true
}

function openHosts() {
  void router.push(EXEC_HOSTS_PATH)
  closeMobile()
}

/** 归档页：常驻入口。侧栏里的「已归档」筛选只在有归档时才出现，以前没法找到它。 */
function openArchive() {
  void router.push(EXEC_ARCHIVE_PATH)
  closeMobile()
}

const onHostsPage = computed(() => route.path === EXEC_HOSTS_PATH)
const onArchivePage = computed(() => route.path === EXEC_ARCHIVE_PATH)
</script>

<template>
  <!-- 手机：遮罩 -->
  <Transition
    enter-active-class="transition-opacity ease-linear duration-200"
    enter-from-class="opacity-0"
    enter-to-class="opacity-100"
    leave-active-class="transition-opacity ease-linear duration-200"
    leave-from-class="opacity-100"
    leave-to-class="opacity-0"
  >
    <div v-if="exec.mobileSidebarOpen" class="fixed inset-0 bg-black/60 backdrop-blur-xs z-40 md:hidden" @click="closeMobile"></div>
  </Transition>

  <aside
    class="sidebar-container fixed inset-y-0 left-0 z-50 h-[100dvh] flex flex-col bg-card/95 backdrop-blur-md md:static md:z-10 md:bg-muted/15 md:h-full shrink-0 select-none overflow-hidden border-r border-border/70"
    :class="[
      // 抽屉宽度：手机上占屏幕的 85%、最宽 18rem；md 以上由 globals.css 的 .sidebar-container 固定宽度接管（max-w-none 放开上限）。
      'w-[85vw] max-w-72 md:max-w-none',
      exec.mobileSidebarOpen ? 'translate-x-0 shadow-float' : '-translate-x-full md:translate-x-0',
      exec.sidebarCollapsed ? 'is-collapsed' : '',
    ]"
    aria-label="ExecMesh 侧栏"
  >
    <!-- 内层始终撑满侧栏自己的宽度：侧栏宽度恒定（折叠只动 margin-left），所以折叠动画里内容不会重排；也不会再出现「内层比侧栏窄」造成的右侧空白。 -->
    <div class="w-full h-full flex flex-col shrink-0">
      <!-- 品牌栏（高度与主工作区顶栏一致） -->
      <div class="h-14 px-3.5 border-b border-border/60 flex items-center justify-between shrink-0">
        <ProjectSwitcher @switched="closeMobile" />
        <div class="flex items-center gap-1 shrink-0">
          <button
            type="button"
            class="p-1 rounded-xs text-muted-foreground hover:text-foreground hover:bg-muted/80 transition-colors hidden md:flex items-center justify-center cursor-pointer"
            title="折叠侧边栏"
            aria-label="折叠侧边栏"
            @click="exec.toggleSidebar()"
          >
            <PanelLeftClose class="w-4 h-4" />
          </button>
          <button
            type="button"
            class="p-1 rounded-sm text-muted-foreground hover:text-foreground md:hidden"
            aria-label="关闭侧栏"
            @click="closeMobile"
          >
            <X class="w-4 h-4" />
          </button>
        </div>
      </div>

      <!-- 新建终端 -->
      <div class="px-2.5 pt-2.5 shrink-0 flex items-stretch gap-1.5">
        <Button
          size="sm"
          class="flex-1 min-w-0 h-8 rounded-sm text-xs gap-1.5"
          :disabled="!defaultHost || creating"
          :title="defaultHost ? `在 ${hostTitle(defaultHost)} 上新建终端` : '没有可以新建终端的主机（Agent 主机需要在线，SSH 主机需要凭据可用且密钥一致）'"
          @click="createDefault()"
        >
          <Loader2 v-if="creating" class="w-3.5 h-3.5 animate-spin" />
          <Plus v-else class="w-3.5 h-3.5" />
          <span class="truncate">新建终端</span>
          <span v-if="defaultHost && onlineHosts.length + sshHosts.length > 1" class="truncate text-[10px] opacity-80 font-normal">· {{ hostTitle(defaultHost) }}</span>
        </Button>
        <DropdownMenu v-if="onlineHosts.length || sshHosts.length">
          <DropdownMenuTrigger as-child>
            <Button variant="outline" size="sm" class="h-8 w-8 p-0 rounded-sm shrink-0" aria-label="选择主机或新建无痕会话" title="选择主机或新建无痕会话">
              <Ellipsis class="w-3.5 h-3.5" />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" class="text-xs min-w-[12rem]">
            <template v-if="onlineHosts.length">
              <DropdownMenuLabel class="text-[11px] text-muted-foreground">在主机上新建</DropdownMenuLabel>
              <DropdownMenuItem v-for="h in onlineHosts" :key="`n:${h.machine_code}`" class="text-xs" @select="createOn(h.machine_code)">
                <Plus class="!size-3.5" /><span class="truncate">{{ hostTitle(h) }}</span>
              </DropdownMenuItem>
            </template>
            <template v-if="sshHosts.length">
              <DropdownMenuSeparator v-if="onlineHosts.length" />
              <DropdownMenuLabel class="text-[11px] text-muted-foreground">连接 SSH 主机</DropdownMenuLabel>
              <DropdownMenuItem v-for="h in sshHosts" :key="`s:${h.machine_code}`" class="text-xs" :title="sshKindBadge(h).title" @select="createSsh(h)">
                <SquareTerminal class="!size-3.5" /><span class="truncate">{{ hostTitle(h) }}</span><span class="ml-auto pl-2 text-[10px] text-muted-foreground">{{ h.ssh?.gateway ? 'SSH · 网关' : 'SSH' }}</span>
              </DropdownMenuItem>
            </template>
            <template v-if="onlineHosts.length || sshIncognitoHosts.length">
              <DropdownMenuSeparator />
              <DropdownMenuLabel class="text-[11px] text-muted-foreground">无痕会话（不记录内容）</DropdownMenuLabel>
              <DropdownMenuItem v-for="h in onlineHosts" :key="`i:${h.machine_code}`" class="text-xs" @select="createOn(h.machine_code, true)">
                <EyeOff class="!size-3.5" /><span class="truncate">{{ hostTitle(h) }}</span>
              </DropdownMenuItem>
              <DropdownMenuItem v-for="h in sshIncognitoHosts" :key="`si:${h.machine_code}`" class="text-xs" @select="createSsh(h, true)">
                <EyeOff class="!size-3.5" /><span class="truncate">{{ hostTitle(h) }}</span><span class="ml-auto pl-2 text-[10px] text-muted-foreground">SSH · 网关</span>
              </DropdownMenuItem>
            </template>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>

      <!-- 搜索与筛选 -->
      <div class="p-2.5 border-b border-border/60 space-y-2 shrink-0">
        <div class="relative">
          <Search class="w-3.5 h-3.5 absolute left-2.5 top-2 text-muted-foreground" />
          <input
            v-model="exec.query"
            type="search"
            placeholder="搜索会话 / 主机 / 调用方..."
            aria-label="搜索会话"
            class="w-full pl-7 pr-3 py-1 text-xs bg-background border border-border/70 rounded-sm focus:outline-none focus:ring-1 focus:ring-primary/30 placeholder:text-muted-foreground"
          />
        </div>
        <div class="flex flex-wrap gap-1" role="group" aria-label="筛选">
          <button
            v-for="f in filters"
            :key="f.id"
            type="button"
            class="text-[10px] px-2 py-0.5 rounded-xs transition-all font-medium cursor-pointer"
            :class="exec.filter === f.id ? 'bg-primary text-primary-foreground font-medium shadow-2xs' : 'text-muted-foreground hover:bg-muted/70'"
            :aria-pressed="exec.filter === f.id"
            :title="f.id === 'archived' && exec.archivedInfo.running > 0 ? `已归档的会话里有 ${exec.archivedInfo.running} 个仍在运行` : undefined"
            @click="exec.filter = f.id"
          >
            {{ f.label }}
            <template v-if="f.id === 'archived'">
              <span class="tabular-nums opacity-70">{{ exec.archivedInfo.total }}</span>
              <span v-if="exec.archivedInfo.running > 0" class="inline-block w-1.5 h-1.5 rounded-full bg-emerald-500 ml-1 align-middle" role="img" aria-label="其中有会话仍在运行" />
            </template>
          </button>
        </div>
        <p v-if="exec.filter === 'archived'" class="text-[10px] leading-relaxed text-muted-foreground">
          归档只是不在侧栏里占位，会话不会结束。右键（手机上右滑）可以取消归档；
          <button type="button" class="text-primary hover:underline cursor-pointer" data-testid="exec-archive-page-link" @click="openArchive">在整页里管理归档 →</button>
        </p>
      </div>

      <!-- 会话清单 -->
      <div
        class="flex-1 min-h-0 overflow-y-auto p-2 no-scrollbar scrollbar-none"
        role="navigation"
        aria-label="终端会话与模型线程"
        @pointerdown.capture="onListPointerDown"
        @scroll.passive="swipeKey = ''"
      >
        <div v-if="!terminal.hostsLoaded && !terminal.hostsError" class="space-y-1.5 px-1" aria-busy="true" aria-label="正在读取会话">
          <div v-for="i in 4" :key="i" class="h-12 rounded-sm bg-muted/60 animate-pulse" />
        </div>
        <div v-else-if="!terminal.hostsLoaded && terminal.hostsError" class="px-2 text-xs text-destructive leading-relaxed" role="alert">
          {{ terminal.hostsError }}
          <Button variant="outline" size="sm" class="mt-2 h-7 rounded-sm text-xs" @click="terminal.refreshHosts()">重试</Button>
        </div>
        <div v-else-if="!hasEnabledHost" class="px-3 py-8 space-y-3 text-center">
          <p class="text-xs text-muted-foreground leading-relaxed">还没有接入任何主机。在目标主机上运行 Agent，这里就能远程打开它的终端。</p>
          <Button size="sm" class="h-7 rounded-sm text-xs gap-1" @click="openDiscovery"><Plus class="w-3.5 h-3.5" />接入第一台主机</Button>
        </div>
        <div v-else-if="exec.visibleCount === 0" class="p-6 text-center text-xs text-muted-foreground font-mono space-y-2">
          <p>
            {{
              exec.items.length === 0
                ? '还没有会话，点上面的「新建终端」开始'
                : exec.filter === 'archived'
                  ? '已归档里没有匹配的会话'
                  : exec.archivedInfo.total > 0 && exec.filter === 'all' && !exec.query.trim()
                    ? '侧栏里没有其他会话了'
                    : '没有匹配的会话'
            }}
          </p>
          <!-- 搜不到时提示：可能被归档了。 -->
          <button
            v-if="exec.filter !== 'archived' && exec.archivedMatches > 0"
            type="button"
            class="font-sans text-[11px] text-primary hover:underline cursor-pointer"
            @click="exec.filter = 'archived'"
          >
            已归档里有 {{ exec.archivedMatches }} 个匹配项，去看看
          </button>
        </div>

        <section v-for="g in exec.groups" :key="g.id" class="mb-2" :aria-label="g.label">
          <h3 class="px-2 pt-1 pb-1 text-[11px] font-medium text-muted-foreground flex items-center gap-1">
            <Pin v-if="g.id === 'pinned'" class="w-3 h-3" />{{ g.label }}
          </h3>
          <ul class="space-y-0.5">
            <li v-for="it in g.items" :key="it.key" :data-swipe-key="it.key">
              <!-- 桌面：右键打开菜单（键盘用菜单键 / Shift+F10）；触摸设备：长按同样打开菜单，向右滑动露出常用操作条。 -->
              <ContextMenu>
                <ContextMenuTrigger as-child :disabled="editingKey === it.key">
                  <SwipeRow :open="swipeKey === it.key" :width="trayWidth(it)" :disabled="editingKey === it.key" @update:open="setSwipe(it.key, $event)">
                    <div
                      class="group/row relative flex items-center gap-1.5 pl-2.5 pr-1.5 min-h-[3rem] rounded-sm border border-transparent transition-colors"
                      :class="isActive(it) ? 'bg-accent text-accent-foreground border-border/60' : 'hover:bg-muted/60 group-data-[state=open]/swipe:bg-muted/60'"
                    >
                      <Bot
                        v-if="it.model"
                        class="w-3.5 h-3.5 shrink-0"
                        :class="it.model.awaiting > 0 ? 'text-amber-500' : it.running ? 'text-emerald-500' : 'text-muted-foreground/60'"
                        role="img"
                        :aria-label="it.model.awaiting > 0 ? '有命令等待批准' : it.running ? '有命令在运行' : '模型线程'"
                      />
                      <span v-else class="w-1.5 h-1.5 rounded-full shrink-0" :class="dotClass(it)" role="img" :aria-label="dotLabel(it)" />
                      <input
                        v-if="editingKey === it.key"
                        ref="renameInput"
                        v-model="draft"
                        maxlength="64"
                        class="flex-1 min-w-0 h-7 px-1.5 rounded-sm bg-background border border-primary text-xs focus:outline-none"
                        aria-label="会话名称"
                        @keydown.enter="onRenameEnter($event, it)"
                        @keydown.esc="onRenameEsc($event)"
                        @blur="commitRename(it)"
                      />
                      <button
                        v-else
                        type="button"
                        class="flex-1 min-w-0 py-1.5 text-left cursor-pointer"
                        :aria-current="isActive(it) ? 'true' : undefined"
                        :title="itemTitle(it)"
                        @click="select(it)"
                      >
                        <span class="flex items-center gap-1.5 min-w-0">
                          <Pin v-if="it.pinned" class="w-3 h-3 shrink-0 text-muted-foreground" aria-label="已置顶" />
                          <span class="truncate text-xs font-medium" :class="!it.running && 'text-muted-foreground'">{{ it.title }}</span>
                          <span v-if="exec.unread[it.key]" class="w-1.5 h-1.5 rounded-full bg-primary shrink-0" role="img" aria-label="有新输出" />
                          <span
                            v-if="it.model && it.model.awaiting > 0"
                            class="text-[10px] px-1 rounded-sm shrink-0 bg-amber-500/15 text-amber-700 dark:text-amber-300 font-medium tabular-nums"
                            :title="`${it.model.awaiting} 条命令等待你批准`"
                          >待批准 {{ it.model.awaiting }}</span>
                          <span
                            v-else-if="it.model && it.model.active > 0"
                            class="text-[10px] px-1 rounded-sm shrink-0 bg-emerald-500/10 text-emerald-700 dark:text-emerald-300 font-medium tabular-nums"
                            :title="`${it.model.active} 条命令正在运行`"
                          >{{ it.model.active }} 运行中</span>
                          <span
                            v-if="recBadge(it)"
                            class="text-[10px] px-1 rounded-sm shrink-0"
                            :class="recBadge(it)!.tone === 'warn' ? 'bg-amber-500/10 text-amber-600 dark:text-amber-400' : 'bg-muted text-muted-foreground'"
                            :title="recBadge(it)!.title"
                          >{{ recBadge(it)!.text }}</span>
                        </span>
                        <span class="block truncate text-[10px] text-muted-foreground leading-tight mt-0.5">{{ subtitle(it) }}</span>
                      </button>
                      <span v-if="editingKey !== it.key" class="shrink-0 text-[10px] text-muted-foreground tabular-nums">{{ timeText(it) }}</span>
                    </div>

                    <template #actions>
                      <button
                        v-for="a in trayActions(it)"
                        :key="a.id"
                        type="button"
                        class="flex-1 min-w-0 flex flex-col items-center justify-center gap-0.5 text-[10px] font-medium cursor-pointer active:bg-background/60 transition-colors"
                        :class="a.primary ? 'bg-primary/15 text-primary' : 'text-foreground/80'"
                        :aria-label="`${it.title}：${a.label}`"
                        @click="runTray(it, a)"
                      >
                        <component :is="a.icon" class="w-4 h-4" />
                        <span class="truncate max-w-full px-0.5">{{ a.label }}</span>
                      </button>
                    </template>
                  </SwipeRow>
                </ContextMenuTrigger>
                <ContextMenuContent class="text-xs min-w-[10rem]">
                  <ContextMenuItem class="text-xs" @select="exec.togglePin(it.key)">
                    <PinOff v-if="it.pinned" class="!size-3.5" /><Pin v-else class="!size-3.5" />{{ it.pinned ? '取消置顶' : '置顶' }}
                  </ContextMenuItem>
                  <ContextMenuItem v-if="canSplit && !it.model && it.live && it.running && it.sid !== activeSid" class="text-xs" @select="openOnRight(it)">
                    <Columns2 class="!size-3.5" />在右侧并排打开
                  </ContextMenuItem>
                  <ContextMenuItem v-if="canRename(it)" class="text-xs" @select="startRename(it)"><Pencil class="!size-3.5" />重命名</ContextMenuItem>
                  <ContextMenuItem v-if="it.archived" class="text-xs" @select="unarchive(it)"><ArchiveRestore class="!size-3.5" />取消归档</ContextMenuItem>
                  <ContextMenuItem v-else class="text-xs" @select="archive(it)"><Archive class="!size-3.5" />归档</ContextMenuItem>
                  <ContextMenuItem class="text-xs" @select="copyId(it)"><Copy class="!size-3.5" />{{ it.model ? '复制线程 ID' : '复制会话 ID' }}</ContextMenuItem>
                  <ContextMenuItem v-if="!it.model && canRecord(it)" class="text-xs" @select="viewRecord(it)"><History class="!size-3.5" />查看记录</ContextMenuItem>
                  <template v-if="!it.model && it.live">
                    <ContextMenuSeparator />
                    <ContextMenuItem class="text-xs text-destructive focus:text-destructive" @select="askKill(it)">
                      <X class="!size-3.5" />{{ it.running ? '终止会话' : '清除' }}
                    </ContextMenuItem>
                  </template>
                </ContextMenuContent>
              </ContextMenu>
            </li>
          </ul>
        </section>
      </div>

      <!-- 底部工具栏 -->
      <div class="p-2 border-t border-border/60 flex items-center justify-between bg-card/50 shrink-0">
        <Button variant="ghost" size="sm" class="w-7 h-7 p-0 text-muted-foreground hover:text-foreground" title="设置" aria-label="设置" @click="openSettings">
          <Settings class="w-3.5 h-3.5" />
        </Button>
        <div class="flex items-center gap-1">
          <button
            v-if="terminal.isUnlocked"
            type="button"
            class="h-7 px-1.5 rounded-sm border border-emerald-500/30 bg-emerald-500/10 text-emerald-700 dark:text-emerald-300 hover:bg-emerald-500/15 text-[11px] font-mono flex items-center gap-1 cursor-pointer transition-colors"
            title="已解锁，点击立即锁定"
            aria-label="已解锁，点击立即锁定"
            @click="terminal.lock()"
          >
            <LockOpen class="w-3 h-3" /><span class="tabular-nums">{{ formatCountdown(terminal.unlockRemaining) }}</span>
          </button>
          <Button v-else variant="ghost" size="sm" class="h-7 px-1.5 gap-1 text-[11px] text-muted-foreground hover:text-foreground" title="已锁定，点击解锁" aria-label="已锁定，点击解锁" @click="terminal.requestUnlock('')">
            <Lock class="w-3.5 h-3.5" />
          </Button>
          <Button variant="ghost" size="sm" class="w-7 h-7 p-0 text-muted-foreground hover:text-foreground" title="审计日志" aria-label="审计日志" @click="openAudit">
            <ScrollText class="w-3.5 h-3.5" />
          </Button>
          <Button
            variant="ghost"
            size="sm"
            class="w-7 h-7 p-0 text-muted-foreground hover:text-foreground cursor-pointer transition-colors"
            :title="themeStore.mode === 'auto' ? `当前: 跟随系统 (${themeStore.isDark ? '深色' : '浅色'}) · 点击切换浅色` : themeStore.mode === 'light' ? '当前: 浅色模式 · 点击切换深色' : '当前: 深色模式 · 点击切换跟随系统'"
            aria-label="切换明暗主题"
            @click="themeStore.toggleTheme"
          >
            <ThemeAutoIcon v-if="themeStore.mode === 'auto'" size="w-3.5 h-3.5" />
            <Sun v-else-if="themeStore.mode === 'light'" class="w-3.5 h-3.5 text-amber-500 hover:text-amber-400" />
            <Moon v-else class="w-3.5 h-3.5 text-indigo-400 hover:text-indigo-300" />
          </Button>
          <Button
            variant="ghost"
            size="sm"
            class="w-7 h-7 p-0 text-muted-foreground hover:text-foreground relative"
            :class="onArchivePage ? 'bg-muted text-foreground' : ''"
            :title="exec.archivedInfo.total > 0 ? `归档列表（${exec.archivedInfo.total} 个）` : '归档列表'"
            :aria-label="exec.archivedInfo.total > 0 ? `归档列表，${exec.archivedInfo.total} 个` : '归档列表'"
            data-testid="exec-archive-button"
            @click="openArchive"
          >
            <Archive class="w-3.5 h-3.5" />
            <span
              v-if="exec.archivedInfo.total > 0"
              class="absolute -top-1 -right-1 min-w-4 h-4 px-1 rounded-full bg-muted-foreground/80 text-[10px] leading-4 text-background text-center font-medium tabular-nums"
              aria-hidden="true"
            >{{ exec.archivedInfo.total > 99 ? '99+' : exec.archivedInfo.total }}</span>
          </Button>
          <Button
            variant="ghost"
            size="sm"
            class="w-7 h-7 p-0 text-muted-foreground hover:text-foreground"
            :class="onHostsPage ? 'bg-muted text-foreground' : ''"
            title="主机管理"
            aria-label="主机管理"
            @click="openHosts"
          >
            <Server class="w-3.5 h-3.5" />
          </Button>
          <Button variant="ghost" size="sm" class="w-7 h-7 p-0 text-muted-foreground hover:text-foreground relative" title="接入主机" aria-label="接入主机" @click="openDiscovery">
            <Radar class="w-3.5 h-3.5" />
            <span
              v-if="terminal.pending.length"
              class="absolute -top-1 -right-1 min-w-4 h-4 px-1 rounded-full bg-amber-500 text-[10px] leading-4 text-white text-center font-medium"
              :aria-label="`${terminal.pending.length} 台主机等待启用`"
            >{{ terminal.pending.length }}</span>
          </Button>
        </div>
      </div>
    </div>
  </aside>

  <ConfirmDialog
    v-model:open="confirm.open"
    :title="confirm.title"
    :description="confirm.description"
    :confirm-text="confirm.button"
    :busy="confirm.busy"
    destructive
    @confirm="runConfirm"
  />
</template>
