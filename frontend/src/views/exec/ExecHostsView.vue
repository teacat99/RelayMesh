<script setup lang="ts">
import { computed, nextTick, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { toast } from 'vue-sonner'
import { ArrowUpCircle, Bot, ChevronDown, Copy, Crown, EyeOff, Hourglass, Infinity as InfinityIcon, KeyRound, Laptop, Loader2, Pencil, Plug, Plus, Power, Radar, RotateCcw, ScrollText, Server, ShieldAlert, SquareTerminal, Trash2, X } from 'lucide-vue-next'
import Button from '../../components/ui/button/Button.vue'
import { Switch } from '../../components/ui/switch'
import ExecPageHeader from '../../components/exec/ExecPageHeader.vue'
import LifecycleMenu from '../../components/exec/LifecycleMenu.vue'
import SshHostDialog from '../../components/exec/SshHostDialog.vue'
import AgentUpgradeDialog from '../../components/terminal/AgentUpgradeDialog.vue'
import ConfirmDialog from '../../components/terminal/ConfirmDialog.vue'
import { useExecStore } from '../../stores/exec'
import { useExecRunsStore } from '../../stores/execRuns'
import { useTerminalStore } from '../../stores/terminal'
import { UnlockCancelled, type HostView, type LostSession, type SshTestResult } from '../../api/terminal'
import { isImeKey } from '../../lib/ime'
import { execSessionPath } from '../../lib/mode'
import { holderBadge, holderStaleBadge, holderVersionNote, hostTitle, lostReasonLabel, privilegeLabel, relativeTime, shortCode } from '../../lib/terminal/format'
import { DUPLICATE_NAME_HINT, canOpenSshTerminal, hostSummary, isGatewayHost, isSshHost, shortFingerprint, sshBadge, sshEndpoint, sshKindBadge, sshTerminalRecorded, summaryText, supportsModel, type SshDialogMode, type SummaryTone } from '../../lib/terminal/hosts'
import { idleSummary, type IdleChoice } from '../../lib/terminal/lifecycle'
import { deleteAgentNote, uninstallOutcomeNotice } from '../../lib/terminal/uninstallHint'
import { agentVersionNote, upgradeBadge } from '../../lib/terminal/upgrade'
import { autoUpgradeChip, autoUpgradeNote } from '../../lib/terminal/autoUpgrade'

// 主机管理页：每台主机一张卡片——状态、版本、重启不中断标注、默认生命周期、终端记录开关、模型访问、新建会话，以及启用 / 停用 / 删除。
// 卡片默认收起，只留名字、徽标和一行摘要（hostSummary）；点右侧的箭头（或摘要）展开完整设置。

const router = useRouter()
const store = useTerminalStore()
const exec = useExecStore()
const execRuns = useExecRunsStore()

const enabled = computed<HostView[]>(() =>
  store.hosts.filter(h => h.status === 'enabled').sort((a, b) => Number(b.online) - Number(a.online) || hostTitle(a).localeCompare(hostTitle(b), 'zh-CN')),
)
const disabled = computed<HostView[]>(() => store.hosts.filter(h => h.status === 'disabled'))
const onlineCount = computed(() => enabled.value.filter(h => h.online).length)

const busy = reactive<Record<string, boolean>>({})

// ---------- Agent 升级 ----------
// 提醒来自服务端（这台主机的 Agent 版本与服务端自带的不同）；升级本身是在主机上执行一条带票据的命令，对话框负责开关升级窗口与生成命令。
const upgradeDlg = reactive({ open: false, code: '' })
function openUpgrade(h: HostView) {
  upgradeDlg.code = h.machine_code
  upgradeDlg.open = true
}
/** 能升级的主机：已启用、装了 Agent 的持久主机（临时主机每次都是新身份，SSH 主机没有 Agent）。 */
function canUpgrade(h: HostView): boolean {
  return !isSshHost(h) && !h.temporary && h.status === 'enabled'
}

function osShort(h: HostView): string {
  return [h.os, h.arch].filter(Boolean).join(' · ')
}

// ---------- 模型访问（G2 / 执行模式 / 暂停） ----------
// 模型能不能用一台主机由几道闸共同决定：服务端总开关、令牌授权与主机白名单、这里的「允许模型访问」、
// 暂停开关、主机本机的一票否决。这里只管主机一侧的三件事：允许与否、执行模式、暂停。
const modelEnabled = computed(() => execRuns.status?.enabled === true)

const EXEC_MODE_CHOICES: { id: string; label: string; hint: string }[] = [
  { id: '', label: '自动', hint: '令牌授权加上这台主机的授权，命令就直接执行。' },
  { id: 'approve', label: '需批准', hint: '每条命令都先停下来，等你在模型线程里批准才会执行。' },
  { id: 'readonly', label: '只读', hint: '模型能看到这台主机，但不能在上面执行任何命令。' },
]

function modeHint(h: HostView): string {
  return EXEC_MODE_CHOICES.find(m => m.id === (h.exec_mode === 'auto' ? '' : (h.exec_mode ?? '')))?.hint ?? ''
}

async function setAllowModel(h: HostView, on: boolean) {
  const key = `model:${h.machine_code}`
  if (busy[key]) return
  busy[key] = true
  try {
    if (await execRuns.setHostModel(h.machine_code, { allow_model: on })) {
      toast.success(on ? '已允许模型使用这台主机' : '已不再允许模型使用这台主机', {
        description: on ? '模型还需要持有带终端权限、且授权了这台主机的 MCP 令牌。' : '这台主机上进行中的模型命令已被取消。',
      })
    }
  } finally {
    busy[key] = false
  }
}

async function setExecMode(h: HostView, mode: string) {
  const key = `model:${h.machine_code}`
  if (busy[key] || (h.exec_mode ?? '') === mode) return
  busy[key] = true
  try {
    await execRuns.setHostModel(h.machine_code, { exec_mode: mode })
  } finally {
    busy[key] = false
  }
}

async function setHostPaused(h: HostView, paused: boolean) {
  const key = `model:${h.machine_code}`
  if (busy[key]) return
  busy[key] = true
  try {
    await execRuns.setPaused('host', h.machine_code, paused)
  } finally {
    busy[key] = false
  }
}

const pausingAll = ref(false)
async function toggleAllPaused() {
  if (pausingAll.value) return
  pausingAll.value = true
  try {
    await execRuns.setPaused('all', undefined, !execRuns.paused)
  } finally {
    pausingAll.value = false
  }
}

// ---------- 折叠与摘要 ----------
// 卡片默认收起：只剩名字、徽标和一行摘要，主机多了也不会铺满整页。展开后才是完整设置。
// 展开状态只在本页内记忆（离开再回来重新收起），不写入本地存储：「默认收起」是承诺，不是偏好。
const expanded = reactive<Record<string, boolean>>({})

function isExpanded(h: HostView): boolean {
  return expanded[h.machine_code] === true
}

function toggleExpanded(h: HostView) {
  expanded[h.machine_code] = !isExpanded(h)
}

// 点摘要也能展开，但正在选中文字（比如想复制地址）时不要抢走这次点击。
function onSummaryClick(h: HostView) {
  if (window.getSelection()?.toString()) return
  toggleExpanded(h)
}

function detailId(h: HostView): string {
  return `host-detail-${h.machine_code}`
}

const SUMMARY_TONE_CLASS: Record<SummaryTone, string> = {
  muted: 'text-muted-foreground',
  ok: 'text-emerald-700 dark:text-emerald-400',
  warn: 'text-amber-700 dark:text-amber-300',
  bad: 'text-destructive',
}

function summaryOf(h: HostView) {
  return hostSummary(h, { modelEnabled: modelEnabled.value, gatewayTitle })
}

/** 网关主机的显示名（机器码找不到对应主机时退回机器码本身）。 */
function gatewayTitle(code: string): string {
  const g = store.hosts.find(x => x.machine_code === code)
  return g ? hostTitle(g) : shortCode(code)
}

/** SSH 主机不能打开终端时，说明原因（用于按钮的悬停提示）；可以打开则返回空串。 */
function sshTerminalBlocked(h: HostView): string {
  if (canOpenSshTerminal(h)) return ''
  const v = h.ssh
  if (!v) return '这台主机没有 SSH 连接信息'
  if (h.status !== 'enabled') return '这台主机已停用，启用后才能打开终端'
  if (v.key_state === 'mismatch') return '主机密钥与已固定的不一致，核对并重新确认之前不会连接'
  if (v.gateway) {
    // 经网关的主机凭据在网关上、不依赖 Hub 的 SSH 主密钥，所以这一支没有「后端未启用」。
    if (v.cred_missing) return '网关上已经没有这台主机的登录凭据，需要在「修改连接」里重新填写'
    if (v.gateway_ready !== true) return '网关当前不可用（不在线、关闭了 SSH 网关或会话守护进程不可用）'
  } else {
    if (!v.backend_enabled) return '服务端没有配置有效的 RELAYMESH_SSH_SECRET_KEY，Hub 直连暂不可用'
    if (!v.has_secret) return 'Hub 上已经没有这台主机的登录凭据，需要在「修改连接」里重新填写'
  }
  return '这台主机当前不可用'
}

// ---------- 备注 ----------
const editing = ref('')
const draft = ref('')
const remarkInput = ref<HTMLInputElement[] | HTMLInputElement | null>(null)

async function startRename(h: HostView) {
  editing.value = h.machine_code
  draft.value = h.remark
  await nextTick()
  const el = Array.isArray(remarkInput.value) ? remarkInput.value[0] : remarkInput.value
  el?.focus()
  el?.select()
}

// 输入法组合期间的回车是在确认候选词、Esc 是在取消组合：不能当成「提交 / 放弃备注」，否则中文备注打不完。
function onRemarkEnter(ev: KeyboardEvent, h: HostView) {
  if (isImeKey(ev)) return
  ev.preventDefault()
  void commitRename(h)
}

function onRemarkEsc(ev: KeyboardEvent) {
  if (isImeKey(ev)) return
  ev.preventDefault()
  editing.value = ''
}

async function commitRename(h: HostView) {
  if (editing.value !== h.machine_code) return
  editing.value = ''
  const remark = draft.value.trim()
  if (remark === h.remark) return
  try {
    await store.setRemark(h.machine_code, remark)
  } catch (e) {
    store.notifyError(e, '修改备注失败')
  }
}

async function copyCode(h: HostView) {
  try {
    await navigator.clipboard.writeText(h.machine_code)
    toast.success('已复制机器码')
  } catch {
    toast.error('复制失败，请手动选择复制', { description: h.machine_code })
  }
}

// ---------- 生命周期与记录 ----------
async function changeLifecycle(h: HostView, v: IdleChoice) {
  if (v === 'inherit') return
  try {
    await store.setHostLifecycle(h.machine_code, v)
    toast.success('已修改这台主机的默认生命周期', {
      description: v > 0 ? `之后没有单独设置的会话，连续 ${idleSummary(v).replace('空闲 ', '').replace('后回收', '')}没有人使用就会被回收。` : '没有单独设置的会话不会再因为没人使用而被回收。',
    })
  } catch (e) {
    store.notifyError(e, '修改默认生命周期失败')
  }
}

async function toggleRecording(h: HostView, on: boolean) {
  const off = !on
  try {
    await store.setRecording(h.machine_code, off)
    toast.success(off ? '已关闭这台主机的终端记录' : '已开启这台主机的终端记录', {
      description: off ? '之后的新输出不再记录；已有的记录仍然保留，到期后自动清理。' : '之后新产生的终端内容会记录在主机上。',
    })
  } catch (e) {
    store.notifyError(e, off ? '关闭记录失败' : '开启记录失败')
  }
}

// ---------- 新建与重建 ----------
async function create(h: HostView, opts: { incognito?: boolean; cwd?: string; name?: string } = {}) {
  const key = `new:${h.machine_code}`
  if (busy[key]) return
  busy[key] = true
  try {
    const s = await store.createSession(h.machine_code, opts.name, undefined, { incognito: opts.incognito, cwd: opts.cwd, open: false })
    if (opts.incognito) toast.success('已新建无痕会话', { description: '这个会话的内容不会被记录。' })
    void router.push(execSessionPath(s.sid))
  } catch (e) {
    store.notifyError(e, opts.incognito ? '新建无痕会话失败' : '新建会话失败')
  } finally {
    busy[key] = false
  }
}

/** 为一台 SSH 主机打开人工终端（Hub 直连：会话由 Hub 持有；经网关：运行在网关主机上），打开后直接跳到它。 */
async function openSshTerminal(h: HostView, opts: { incognito?: boolean } = {}) {
  const key = `ssh-term:${h.machine_code}`
  if (busy[key]) return
  busy[key] = true
  try {
    const s = await store.openSshTerminal(h, { open: false, incognito: opts.incognito })
    if (opts.incognito) toast.success('已打开无痕终端', { description: '这个会话的内容不会被记录。' })
    void router.push(execSessionPath(s.sid))
  } catch (e) {
    store.notifyError(e, opts.incognito ? '打开无痕 SSH 终端失败' : '打开 SSH 终端失败')
  } finally {
    busy[key] = false
  }
}

function rebuild(h: HostView, l: LostSession) {
  void create(h, { cwd: l.cwd, name: l.name })
}

function lostTime(l: LostSession): string {
  return l.at ? relativeTime(new Date(l.at * 1000).toISOString()) : ''
}

/**
 * 忽略「有会话已不存在」的提示：传一条就忽略这一条，不传就是这台主机当前的全部。
 * 只是不再提醒——会话的记录与审计都还在，主机不用在线，守护进程之后再报同样的丢失也不会回来。
 */
async function dismissLost(h: HostView, l?: LostSession) {
  const key = `lost:${h.machine_code}`
  if (busy[key]) return
  busy[key] = true
  try {
    await store.dismissLost(h.machine_code, l ? [l.sid] : 'all')
  } catch (e) {
    store.notifyError(e, '忽略提示失败')
  } finally {
    busy[key] = false
  }
}

// ---------- SSH 主机 ----------
// SSH 主机没有 Agent：Hub 直连与经网关接入两条路径的管理方式和权限完全一致——都有人工终端，模型授权后也都能执行命令，
// 都没有文件传输。这里管它的三件事：接入 / 修改连接信息、测试连接、密钥不一致后重新确认。
const sshDlg = reactive({ open: false, mode: 'add' as SshDialogMode, host: null as HostView | null })
const sshOff = computed(() => store.sshStatus !== null && !store.sshStatus.enabled)

function openSsh(mode: SshDialogMode, host: HostView | null = null) {
  Object.assign(sshDlg, { open: true, mode, host })
}

const sshTesting = reactive<Record<string, boolean>>({})
const sshResults = reactive<Record<string, SshTestResult | undefined>>({})

function sshFacts(r: SshTestResult): string {
  return [r.user && r.hostname ? `${r.user}@${r.hostname}` : '', [r.os, r.arch].filter(Boolean).join(' '), r.latency_ms ? `${r.latency_ms} ms` : ''].filter(Boolean).join(' · ')
}

async function testSsh(h: HostView) {
  const code = h.machine_code
  if (sshTesting[code]) return
  sshTesting[code] = true
  try {
    const r = await store.sshTest(code)
    sshResults[code] = r
    if (r.ok) toast.success('连接正常', { description: sshFacts(r) || undefined })
    else toast.error(r.message || '连接测试失败')
  } catch (e) {
    store.notifyError(e, '测试连接失败')
  } finally {
    sshTesting[code] = false
  }
}

// ---------- 启用 / 停用 / 删除 ----------
async function enableHost(h: HostView) {
  busy[`en:${h.machine_code}`] = true
  try {
    await store.enableHost(h.machine_code)
    // 被停用的 Agent 只会按固定间隔重试，不会被服务端即时唤醒：提前说明，免得管理员以为没有生效。
    if (!h.online && !isSshHost(h)) toast.success('已启用', { description: '主机上的 Agent 会在一分钟内自动重新连接。' })
  } catch (e) {
    store.notifyError(e, '启用失败')
  } finally {
    busy[`en:${h.machine_code}`] = false
  }
}

const confirm = reactive({
  open: false,
  busy: false,
  kind: '' as '' | 'disable' | 'delete',
  code: '',
  title: '',
  description: '',
  button: '',
  // 删除 Agent 主机时关于设备端的说明（SSH 主机没有设备端 Agent，为 null）：能不能同时卸载、不能的原因、手动命令
  os: '',
  agentNote: null as ReturnType<typeof deleteAgentNote> | null,
  uninstall: false, // 「同时卸载设备端」：每次打开对话框都重置为不勾选
})

/** 有多少台 SSH 主机经这台主机（作为网关）接入。 */
function gatewayDependents(h: HostView): HostView[] {
  return isSshHost(h) ? [] : store.hosts.filter(x => x.ssh?.gateway === h.machine_code)
}

function dependentsText(deps: HostView[]): string {
  const names = deps.slice(0, 3).map(x => `「${hostTitle(x)}」`).join('、')
  return `${names}${deps.length > 3 ? ` 等 ${deps.length} 台` : ''}`
}

function askDisable(h: HostView) {
  const deps = gatewayDependents(h)
  Object.assign(confirm, {
    open: true,
    kind: 'disable',
    code: h.machine_code,
    agentNote: null,
    uninstall: false,
    title: `停用「${hostTitle(h)}」？`,
    description:
      (isSshHost(h)
        ? isGatewayHost(h)
          ? '模型在这台主机上进行中的命令会被取消（并通知远端终止），已打开的 SSH 终端会话会随之结束，之后不能再经网关连接它。连接信息仍保留，登录凭据仍保存在网关上，可以随时重新启用。'
          : '模型在这台主机上进行中的命令会被取消（并通知远端终止），已打开的 SSH 终端会话（由 Hub 持有）会随之结束，之后不再连接它。连接信息与凭据仍保留，可以随时重新启用。'
        : '会立即断开这台主机，其上所有会话都会被终止。之后可以随时重新启用。') +
      (deps.length ? `经它接入的 SSH 主机（${dependentsText(deps)}）在它重新启用之前无法打开终端；它们的连接信息仍然保留。` : ''),
    button: '停用',
  })
}

function askDelete(h: HostView) {
  // 网关还有 SSH 主机在用：服务端会拒绝（gateway_in_use），这里提前说清楚，别让人点完确认才发现。
  const deps = gatewayDependents(h)
  if (deps.length) {
    toast.error(`「${hostTitle(h)}」还不能删除`, {
      description: `有 ${deps.length} 台 SSH 主机经它接入（${dependentsText(deps)}）。请先把它们改接到别的网关或 Hub 直连，或者先删除它们。`,
    })
    return
  }
  Object.assign(confirm, {
    open: true,
    kind: 'delete',
    code: h.machine_code,
    os: h.os,
    agentNote: isSshHost(h) ? null : deleteAgentNote(h),
    uninstall: false,
    title: `删除「${hostTitle(h)}」？`,
    description: isSshHost(h)
      ? isGatewayHost(h)
        ? '会取消模型在这台主机上进行中的命令、终止它在网关上已打开的终端会话，并删除保存的连接信息与固定的主机密钥，同时通知网关删除它保存的登录凭据（等进行中的命令收尾之后）；历史运行记录仍然保留。再次使用需要重新接入并核对指纹。'
        : '会取消模型在这台主机上进行中的命令、结束 Hub 为它持有的终端会话，并删除保存的连接信息、加密凭据与固定的主机密钥；历史运行记录仍然保留。再次使用需要重新接入并核对指纹。'
      : '会断开这台主机并终止其上所有会话，同时忘记它的机器码；它再次连接时需要重新走发现与启用流程。',
    button: '删除',
  })
}

async function runConfirm() {
  confirm.busy = true
  try {
    if (confirm.kind === 'disable') await store.disableHost(confirm.code)
    else if (confirm.kind === 'delete') {
      const wantUninstall = confirm.uninstall && !!confirm.agentNote?.canUninstall
      const outcome = await store.deleteHost(confirm.code, { uninstall: wantUninstall })
      // 设备上到底发生了什么要如实告诉管理员：「已删除」不等于设备端清理干净了
      const notice = uninstallOutcomeNotice(outcome, confirm.os)
      if (notice) toast[notice.level](notice.title, { description: notice.description })
    }
    confirm.open = false
  } catch (e) {
    if (e instanceof UnlockCancelled) return // 保持对话框打开，用户可以重试或取消
    store.notifyError(e, confirm.button + '失败')
    confirm.open = false
  } finally {
    confirm.busy = false
  }
}
</script>

<template>
  <div class="flex-1 min-h-0 flex flex-col">
    <ExecPageHeader>
      <Server class="w-4 h-4 text-primary shrink-0" aria-hidden="true" />
      <div class="min-w-0">
        <h1 class="truncate text-sm font-medium text-foreground leading-tight">主机</h1>
        <p class="truncate text-[11px] text-muted-foreground leading-tight">
          <template v-if="store.hostsLoaded">已启用 {{ enabled.length }} 台 · 在线 {{ onlineCount }} 台</template>
          <template v-else>正在读取…</template>
        </p>
      </div>
      <template #actions>
        <Button variant="ghost" size="sm" class="h-7 px-2 gap-1.5 text-[11px] rounded-sm" @click="exec.discoveryOpen = true">
          <Radar class="w-3.5 h-3.5" /><span class="hidden sm:inline">接入主机</span>
        </Button>
        <Button
          variant="ghost"
          size="sm"
          class="h-7 px-2 gap-1.5 text-[11px] rounded-sm"
          :title="sshOff ? '服务端没有配置 RELAYMESH_SSH_SECRET_KEY：Hub 直连暂不可用；经网关接入（凭据只保存在网关上）不受影响' : '通过 SSH 接入没有 Agent 的主机：由 Hub 直连，或经一台装了 Agent 的网关主机连接；两种方式都有人工终端，模型获得授权后也能执行命令'"
          @click="openSsh('add')"
        >
          <KeyRound class="w-3.5 h-3.5" /><span class="hidden sm:inline">SSH 主机</span>
        </Button>
        <Button variant="ghost" size="sm" class="h-7 px-2 gap-1.5 text-[11px] rounded-sm" aria-label="审计日志" title="审计日志" @click="exec.auditOpen = true">
          <ScrollText class="w-3.5 h-3.5" /><span class="hidden sm:inline">审计</span>
        </Button>
      </template>
    </ExecPageHeader>

    <div class="flex-1 min-h-0 overflow-y-auto">
      <div class="max-w-3xl mx-auto px-3 sm:px-5 py-4 space-y-4">
        <button
          v-if="store.pending.length"
          type="button"
          class="w-full flex items-center gap-2 rounded-sm border border-amber-500/30 bg-amber-500/10 px-3 py-2.5 text-left text-xs text-amber-700 dark:text-amber-300 hover:bg-amber-500/15 transition-colors cursor-pointer"
          @click="exec.discoveryOpen = true"
        >
          <Radar class="w-4 h-4 shrink-0" />
          <span class="flex-1">{{ store.pending.length }} 台主机等待启用，点这里核对机器码</span>
        </button>

        <section v-if="enabled.length" class="rounded-sm border border-border/70 bg-card/60 p-3.5 space-y-1.5" aria-label="模型执行">
          <div class="flex items-center gap-2">
            <Bot class="w-4 h-4 text-muted-foreground shrink-0" />
            <h2 class="text-xs font-semibold flex-1">模型执行</h2>
            <span
              class="text-[10px] px-1.5 py-0.5 rounded-sm"
              :class="!modelEnabled ? 'bg-muted text-muted-foreground' : execRuns.paused ? 'bg-amber-500/15 text-amber-700 dark:text-amber-300' : 'bg-emerald-500/10 text-emerald-700 dark:text-emerald-300'"
            >{{ !modelEnabled ? '未启用' : execRuns.paused ? '已全部暂停' : '已启用' }}</span>
            <Button v-if="modelEnabled" :variant="execRuns.paused ? 'default' : 'outline'" size="sm" class="h-7 px-2.5 rounded-sm text-xs" :disabled="pausingAll" @click="toggleAllPaused">
              {{ execRuns.paused ? '恢复全部模型' : '暂停全部模型' }}
            </Button>
          </div>
          <p v-if="!modelEnabled" class="text-[11px] leading-relaxed text-muted-foreground">
            默认关闭。需要让模型在这些主机上执行命令时：在服务端设置环境变量 <code class="font-mono">RELAYMESH_TERMINAL_EXEC=on</code> 并重启，再给 MCP 令牌授权终端权限与主机白名单，最后在下面逐台打开「允许模型访问」。
          </p>
          <p v-else class="text-[11px] leading-relaxed text-muted-foreground">
            模型默认碰不到任何主机：需要令牌授权、这里逐台允许、并且主机本机没有一票否决。暂停会立即取消进行中的命令并拒绝新的命令；随时可以恢复。
          </p>
        </section>

        <div v-if="!store.hostsLoaded && !store.hostsError" class="space-y-3" aria-busy="true" aria-label="正在读取主机">
          <div v-for="i in 2" :key="i" class="h-32 rounded-sm bg-muted/60 animate-pulse" />
        </div>
        <div v-else-if="!store.hostsLoaded && store.hostsError" class="text-xs text-destructive leading-relaxed" role="alert">
          {{ store.hostsError }}
          <Button variant="outline" size="sm" class="ml-2 h-7 rounded-sm text-xs" @click="store.refreshHosts()">重试</Button>
        </div>
        <div v-else-if="!enabled.length && !disabled.length" class="py-10 text-center space-y-3">
          <p class="text-xs text-muted-foreground leading-relaxed">还没有接入任何主机。在目标主机上运行 Agent，这里就能远程打开它的终端。</p>
          <div class="flex flex-wrap items-center justify-center gap-2">
            <Button size="sm" class="h-8 rounded-sm text-xs gap-1.5" @click="exec.discoveryOpen = true"><Plus class="w-3.5 h-3.5" />接入第一台主机</Button>
            <Button variant="outline" size="sm" class="h-8 rounded-sm text-xs gap-1.5" @click="openSsh('add')"><KeyRound class="w-3.5 h-3.5" />通过 SSH 接入主机</Button>
          </div>
        </div>

        <!-- 已启用 -->
        <article
          v-for="h in enabled"
          :key="h.machine_code"
          class="rounded-sm border border-border/70 bg-card/60 p-3.5 sm:p-4"
          :aria-label="`主机 ${hostTitle(h)}`"
        >
          <div class="flex items-start gap-2.5">
            <span
              class="w-2 h-2 mt-1.5 rounded-full shrink-0"
              :class="h.online ? 'bg-emerald-500' : 'bg-muted-foreground/40'"
              role="img"
              :aria-label="h.online ? '在线' : '离线'"
            />
            <div class="min-w-0 flex-1 space-y-1">
              <div class="flex flex-wrap items-center gap-x-2 gap-y-1">
                <input
                  v-if="editing === h.machine_code"
                  ref="remarkInput"
                  v-model="draft"
                  maxlength="64"
                  class="h-7 px-1.5 rounded-sm bg-background border border-primary text-sm focus:outline-none"
                  aria-label="主机备注"
                  @keydown.enter="onRemarkEnter($event, h)"
                  @keydown.esc="onRemarkEsc($event)"
                  @blur="commitRename(h)"
                />
                <template v-else>
                  <h2 class="text-sm font-semibold text-foreground truncate">{{ hostTitle(h) }}</h2>
                  <button type="button" class="p-1 rounded-sm text-muted-foreground hover:text-foreground hover:bg-muted cursor-pointer" aria-label="编辑备注" title="编辑备注" @click="startRename(h)">
                    <Pencil class="w-3 h-3" />
                  </button>
                </template>
                <template v-if="isSshHost(h)">
                  <span class="text-[10px] px-1.5 py-0.5 rounded-sm bg-violet-500/10 text-violet-600 dark:text-violet-400" :title="sshKindBadge(h).title">{{ sshKindBadge(h).text }}</span>
                  <span
                    class="text-[10px] px-1.5 py-0.5 rounded-sm"
                    :class="{
                      'bg-emerald-500/10 text-emerald-600 dark:text-emerald-400': sshBadge(h).tone === 'ok',
                      'bg-amber-500/10 text-amber-600 dark:text-amber-400': sshBadge(h).tone === 'warn',
                      'bg-destructive/10 text-destructive': sshBadge(h).tone === 'bad',
                      'bg-muted text-muted-foreground': sshBadge(h).tone === 'muted',
                    }"
                    :title="sshBadge(h).title"
                  >{{ sshBadge(h).text }}</span>
                </template>
                <template v-else>
                  <Crown v-if="privilegeLabel(h.privilege).elevated" class="w-3.5 h-3.5 shrink-0 text-amber-500" :title="privilegeLabel(h.privilege).text" />
                  <span v-if="h.temporary" class="text-[10px] px-1.5 py-0.5 rounded-sm bg-sky-500/10 text-sky-600 dark:text-sky-400">临时</span>
                  <span
                    v-if="holderBadge(h)"
                    class="text-[10px] px-1.5 py-0.5 rounded-sm"
                    :class="{
                      'bg-emerald-500/10 text-emerald-600 dark:text-emerald-400': holderBadge(h)!.tone === 'ok',
                      'bg-amber-500/10 text-amber-600 dark:text-amber-400': holderBadge(h)!.tone === 'warn',
                      'bg-destructive/10 text-destructive': holderBadge(h)!.tone === 'bad',
                    }"
                    :title="holderBadge(h)!.title"
                  >{{ holderBadge(h)!.text }}</span>
                  <!-- 守护进程是在某项修复之前启动的：升级只替换 Agent，要 holder restart 才带上（悬停有原因与做法） -->
                  <span
                    v-if="holderStaleBadge(h)"
                    class="text-[10px] px-1.5 py-0.5 rounded-sm bg-amber-500/10 text-amber-600 dark:text-amber-400 cursor-help"
                    :title="holderStaleBadge(h)!.title"
                    data-testid="holder-stale-badge"
                  >{{ holderStaleBadge(h)!.text }}</span>
                  <!-- Agent 升级提醒 / 升级窗口状态：点开升级对话框 -->
                  <button
                    v-if="canUpgrade(h) && upgradeBadge(h)"
                    type="button"
                    class="text-[10px] px-1.5 py-0.5 rounded-sm cursor-pointer transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                    :class="
                      upgradeBadge(h)!.tone === 'info'
                        ? 'bg-sky-500/10 text-sky-600 dark:text-sky-400 hover:bg-sky-500/20'
                        : 'bg-amber-500/10 text-amber-600 dark:text-amber-400 hover:bg-amber-500/20'
                    "
                    :title="upgradeBadge(h)!.title"
                    :aria-label="`${hostTitle(h)}：${upgradeBadge(h)!.title}`"
                    data-testid="upgrade-badge"
                    @click="openUpgrade(h)"
                  >{{ upgradeBadge(h)!.text }}</button>
                  <!-- 主机所有者在本机开启了「自动升级」：悬停说明它做什么（提示模式只提醒，不下载不升级）与最近一次检查的结果 -->
                  <span
                    v-if="autoUpgradeChip(h, store.agentAutoUpgrade)"
                    class="text-[10px] px-1.5 py-0.5 rounded-sm cursor-help"
                    :class="{
                      'bg-sky-500/10 text-sky-600 dark:text-sky-400': autoUpgradeChip(h, store.agentAutoUpgrade)!.tone === 'info',
                      'bg-amber-500/10 text-amber-600 dark:text-amber-400': autoUpgradeChip(h, store.agentAutoUpgrade)!.tone === 'warn',
                      'bg-muted text-muted-foreground': autoUpgradeChip(h, store.agentAutoUpgrade)!.tone === 'muted',
                    }"
                    :title="autoUpgradeChip(h, store.agentAutoUpgrade)!.title"
                    data-testid="auto-upgrade-chip"
                  >{{ autoUpgradeChip(h, store.agentAutoUpgrade)!.text }}</span>
                </template>
                <span v-if="h.duplicate_name" class="text-[10px] px-1.5 py-0.5 rounded-sm bg-amber-500/10 text-amber-600 dark:text-amber-400" :title="DUPLICATE_NAME_HINT">同名</span>
              </div>
              <!-- 展开：完整的版本与机器码；收起：一行摘要（点它也能展开） -->
              <template v-if="isExpanded(h)">
                <p v-if="isSshHost(h)" class="flex flex-wrap items-center gap-x-1.5 text-[11px] text-muted-foreground font-mono leading-snug">
                  <span>{{ sshEndpoint(h.ssh) }}</span>
                  <span v-if="h.ssh?.gateway" :title="`网关主机 ${h.ssh.gateway}`">· 经网关 {{ gatewayTitle(h.ssh.gateway) }}</span>
                  <span v-else title="由 Hub 出站连接这台主机">· Hub 直连</span>
                  <span v-if="osShort(h)">· {{ osShort(h) }}</span>
                  <span>· {{ h.ssh?.auth === 'key' ? '私钥认证' : '口令认证' }}</span>
                  <span v-if="h.ssh?.fingerprint" :title="`${h.ssh.key_type} ${h.ssh.fingerprint}`">· {{ shortFingerprint(h.ssh.fingerprint) }}</span>
                </p>
                <p v-else class="flex flex-wrap items-center gap-x-1.5 text-[11px] text-muted-foreground font-mono leading-snug">
                  <span v-if="osShort(h)">{{ osShort(h) }}</span>
                  <span v-if="h.agent_version">· Agent {{ h.agent_version }}</span>
                  <!-- 版本号后的说明：有可升级的更新（琥珀色）；或两边 Agent 代码一致、版本号不同只是构建号不同（不需要升级） -->
                  <span
                    v-if="agentVersionNote(h)"
                    :class="agentVersionNote(h)!.tone === 'warn' ? 'text-amber-600 dark:text-amber-400' : 'text-emerald-600/80 dark:text-emerald-400/80'"
                    :title="agentVersionNote(h)!.title"
                    data-testid="agent-version-note"
                  >{{ agentVersionNote(h)!.text }}</span>
                  <!-- 会话守护进程只在手动 holder restart 时才换，版本常与 Agent 不同：看代码指纹说明是否真的较旧 -->
                  <span
                    v-if="holderVersionNote(h)"
                    :class="holderVersionNote(h)!.tone === 'warn' ? 'text-amber-600 dark:text-amber-400' : ''"
                    :title="holderVersionNote(h)!.title"
                    data-testid="holder-version-note"
                  >· {{ holderVersionNote(h)!.text }}</span>
                  <span>· {{ privilegeLabel(h.privilege).text }}</span>
                  <!-- 支持自动升级、但主机所有者没有开启：悬停说明在主机上怎么开（服务端无法替主机开启） -->
                  <span v-if="autoUpgradeNote(h)" class="cursor-help" :title="autoUpgradeNote(h)!.title" data-testid="auto-upgrade-note">· {{ autoUpgradeNote(h)!.text }}</span>
                  <span v-if="!h.online && h.last_seen_at">· 最后在线 {{ relativeTime(h.last_seen_at) }}</span>
                </p>
                <p class="flex items-center gap-1 text-[11px] text-muted-foreground font-mono">
                  <span :title="h.machine_code">{{ shortCode(h.machine_code) }}</span>
                  <button type="button" class="p-1 rounded-sm hover:text-foreground hover:bg-muted cursor-pointer" aria-label="复制机器码" title="复制机器码" @click="copyCode(h)">
                    <Copy class="w-3 h-3" />
                  </button>
                </p>
              </template>
              <p
                v-else
                class="flex flex-wrap items-center gap-x-1.5 gap-y-0.5 text-[11px] leading-snug cursor-pointer"
                :title="summaryText(summaryOf(h))"
                @click="onSummaryClick(h)"
              >
                <template v-for="(part, i) in summaryOf(h)" :key="i">
                  <span v-if="i > 0" class="text-muted-foreground/50" aria-hidden="true">·</span>
                  <span :class="[SUMMARY_TONE_CLASS[part.tone], part.mono && 'font-mono']" :title="part.title">{{ part.text }}</span>
                </template>
              </p>
            </div>

            <div class="flex items-center gap-1 shrink-0">
              <Button
                v-if="!isExpanded(h) && isSshHost(h) && canOpenSshTerminal(h)"
                variant="outline"
                size="sm"
                class="h-7 px-2 rounded-sm text-xs gap-1.5"
                :disabled="busy[`ssh-term:${h.machine_code}`]"
                :aria-label="`打开 ${hostTitle(h)} 的终端`"
                @click="openSshTerminal(h)"
              >
                <Loader2 v-if="busy[`ssh-term:${h.machine_code}`]" class="w-3.5 h-3.5 animate-spin" />
                <SquareTerminal v-else class="w-3.5 h-3.5" /><span class="hidden sm:inline">打开终端</span>
              </Button>
              <Button
                v-if="!isExpanded(h) && !isSshHost(h) && h.online"
                variant="outline"
                size="sm"
                class="h-7 px-2 rounded-sm text-xs gap-1.5"
                :disabled="busy[`new:${h.machine_code}`]"
                :aria-label="`在 ${hostTitle(h)} 上新建会话`"
                @click="create(h)"
              >
                <Loader2 v-if="busy[`new:${h.machine_code}`]" class="w-3.5 h-3.5 animate-spin" />
                <Plus v-else class="w-3.5 h-3.5" /><span class="hidden sm:inline">新建会话</span>
              </Button>
              <button
                type="button"
                class="w-7 h-7 rounded-sm flex items-center justify-center text-muted-foreground hover:text-foreground hover:bg-muted cursor-pointer transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                :aria-expanded="isExpanded(h)"
                :aria-controls="detailId(h)"
                :aria-label="`${isExpanded(h) ? '收起' : '展开'} ${hostTitle(h)} 的详情与设置`"
                :title="isExpanded(h) ? '收起' : '展开设置'"
                @click="toggleExpanded(h)"
              >
                <ChevronDown class="w-4 h-4 transition-transform motion-reduce:transition-none" :class="isExpanded(h) && 'rotate-180'" />
              </button>
            </div>
          </div>

          <!-- 完整设置：只在展开时渲染 -->
          <div v-if="isExpanded(h)" :id="detailId(h)" class="mt-3 space-y-3">
            <div v-if="isSshHost(h)" class="pt-3 border-t border-border/60 space-y-2" :aria-label="`${hostTitle(h)} 的 SSH 连接`">
              <div class="flex flex-wrap items-center gap-x-2 gap-y-1">
                <p class="text-[11px] font-medium text-foreground">SSH 连接</p>
                <span v-if="h.ssh?.ok_at" class="text-[11px] text-muted-foreground">最近连通 {{ relativeTime(h.ssh.ok_at) }}</span>
                <span v-if="isGatewayHost(h) ? h.ssh?.cred_missing : h.ssh && !h.ssh.has_secret" class="text-[10px] px-1.5 py-0.5 rounded-sm bg-amber-500/10 text-amber-600 dark:text-amber-400">{{ isGatewayHost(h) ? '网关上没有凭据，需重新填写' : '凭据不可用，需重新填写' }}</span>
                <span v-else-if="isGatewayHost(h)" class="text-[10px] px-1.5 py-0.5 rounded-sm bg-muted text-muted-foreground" title="登录凭据加密保存在网关主机自己的存储里，Hub 没有副本">凭据在网关上</span>
              </div>
              <p v-if="h.ssh?.key_state === 'mismatch'" class="flex items-start gap-1.5 rounded-sm border border-destructive/30 bg-destructive/10 px-2.5 py-2 text-[11px] leading-relaxed text-destructive" role="alert">
                <ShieldAlert class="w-3.5 h-3.5 shrink-0 mt-0.5" />
                这台主机出示的密钥与之前固定的不一致，已停止连接，没有向它发送过任何凭据。可能是主机重装 / 换了密钥，也可能是有人在冒充：请通过可信渠道核对新指纹后再重新确认。
              </p>
              <p v-else-if="!h.online && h.ssh?.error" class="text-[11px] leading-relaxed text-amber-700 dark:text-amber-300">最近一次连接失败：{{ h.ssh.error }}</p>
              <p
                v-if="sshResults[h.machine_code]"
                class="text-[11px] leading-relaxed"
                :class="sshResults[h.machine_code]!.ok ? 'text-emerald-700 dark:text-emerald-300' : 'text-destructive'"
                role="status"
              >
                测试结果：{{ sshResults[h.machine_code]!.ok ? `连接正常 · ${sshFacts(sshResults[h.machine_code]!)}` : sshResults[h.machine_code]!.message || '失败' }}
              </p>
              <template v-if="isGatewayHost(h)">
                <p class="text-[11px] leading-snug text-muted-foreground">
                  经网关接入：由网关主机「{{ gatewayTitle(h.ssh!.gateway!) }}」出站连接这台主机。登录凭据只加密保存在网关主机自己的存储里，Hub 没有副本，也不需要 SSH 主密钥；
                  网关用 <code class="font-mono">--ssh-allow</code> 限定能连到哪里。有人工终端，会话运行在网关主机上（Hub 重启后仍在），内容记录与其他会话一样保存在那里，需要不留痕迹时请打开无痕终端；没有文件传输。
                </p>
                <p v-if="h.ssh?.term_only" class="text-[11px] leading-snug text-amber-700 dark:text-amber-300">
                  登记时没能探测出 POSIX 环境（需要 sh 与 base64），这台主机只能当人工终端用，不支持模型执行。目标环境变了之后点「测试连接」会重新判断。
                </p>
                <p v-else class="text-[11px] leading-snug text-muted-foreground">
                  模型获得授权后也可以经网关在这台主机上执行命令：命令文本不会出现在 SSH 命令行里，输出只存放在远端的运行目录（保留 7 天），Hub 与网关都不落盘。网关主机用 <code class="font-mono">--deny-model-exec</code> 启动时可以在本机一票否决，服务端无法更改。
                </p>
              </template>
              <template v-else>
                <p class="text-[11px] leading-snug text-muted-foreground">
                  Hub 直连：由 Hub 出站连接这台主机，登录凭据加密保存在 Hub。有人工终端，会话由 Hub 持有、不做记录，Hub 重启（以及停用、删除、修改连接）时会结束；
                  需要会话在 Hub 重启后仍然保留、并留存终端记录时，请改经网关接入。没有文件传输。
                </p>
                <p class="text-[11px] leading-snug text-muted-foreground">
                  模型获得授权后也可以执行命令：命令文本不会出现在 SSH 命令行里，输出只存放在远端的运行目录（保留 7 天），Hub 不落盘；没有主机本机的一票否决。
                </p>
              </template>
            </div>

            <!-- 经网关的 SSH 主机，会话由网关持有、生命周期归网关主机自己管，所以这里不出现；Hub 直连的会话在 Hub 里，默认生命周期就是这台主机的 -->
            <div v-if="!isGatewayHost(h)" class="grid gap-3 sm:grid-cols-2 pt-3 border-t border-border/60">
              <div class="space-y-1.5">
                <p class="text-[11px] font-medium text-foreground">{{ isSshHost(h) ? '终端会话的默认生命周期' : '默认生命周期' }}</p>
                <LifecycleMenu :model-value="h.idle_sec ?? 0" title="没有单独设置的会话，没人使用时怎么处理" @update:model-value="v => changeLifecycle(h, v)">
                  <template #trigger>
                    <button
                      type="button"
                      class="h-8 px-2.5 rounded-sm border border-border/80 bg-background text-xs flex items-center gap-1.5 hover:bg-muted cursor-pointer transition-colors"
                      :aria-label="`默认生命周期：${idleSummary(h.idle_sec ?? 0)}，点击修改`"
                    >
                      <InfinityIcon v-if="!(h.idle_sec && h.idle_sec > 0)" class="w-3.5 h-3.5 text-muted-foreground" />
                      <Hourglass v-else class="w-3.5 h-3.5 text-sky-500" />
                      {{ idleSummary(h.idle_sec ?? 0) }}
                    </button>
                  </template>
                </LifecycleMenu>
                <p class="text-[11px] leading-snug text-muted-foreground">
                  对没有单独设置的会话生效（包括正在运行的）。「空闲」指没人连接、也没有输入；程序自己的输出不算。<template v-if="isSshHost(h)">无论怎么设置，Hub 重启时这些会话都会结束。</template>
                </p>
              </div>

              <div v-if="!isSshHost(h)" class="space-y-1.5">
                <p class="text-[11px] font-medium text-foreground">终端记录</p>
                <label class="flex items-center gap-2 text-xs">
                  <Switch :checked="!h.rec_off && !h.rec_server_off" :disabled="h.rec_server_off" aria-label="记录这台主机上的终端内容" @update:checked="(v: boolean) => toggleRecording(h, v)" />
                  <span>{{ h.rec_server_off ? '已被服务端关闭' : h.rec_off ? '已关闭' : '记录终端上显示的内容' }}</span>
                </label>
                <p class="text-[11px] leading-snug text-muted-foreground">记录保存在主机本地，可在会话里回放；也可以单独新建无痕会话。</p>
              </div>
            </div>

            <div v-if="modelEnabled && supportsModel(h)" class="pt-3 border-t border-border/60 space-y-2" :aria-label="`${hostTitle(h)} 的模型访问`">
              <div class="flex flex-wrap items-center gap-x-3 gap-y-1.5">
                <p class="text-[11px] font-medium text-foreground">模型访问</p>
                <label class="flex items-center gap-2 text-xs">
                  <Switch :checked="!!h.allow_model" :disabled="busy[`model:${h.machine_code}`] || h.temporary" aria-label="允许模型使用这台主机" @update:checked="(v: boolean) => setAllowModel(h, v)" />
                  <span>{{ h.allow_model ? '允许模型使用' : '不允许模型使用' }}</span>
                </label>
                <span
                  v-if="h.holder?.deny_exec"
                  class="text-[10px] px-1.5 py-0.5 rounded-sm bg-destructive/10 text-destructive"
                  title="这台主机用 --deny-model-exec 启动：本机一票否决，服务端无法更改。"
                >本机已一票否决</span>
                <span
                  v-else-if="h.ssh?.gateway_denied"
                  class="text-[10px] px-1.5 py-0.5 rounded-sm bg-destructive/10 text-destructive"
                  title="承载这台主机的网关用 --deny-model-exec 启动：网关本机一票否决，服务端无法更改。人工终端不受影响。"
                >网关已一票否决</span>
                <span v-else-if="h.online && h.holder && !h.holder.exec" class="text-[10px] px-1.5 py-0.5 rounded-sm bg-amber-500/10 text-amber-700 dark:text-amber-300" title="这台主机的 Agent 版本不支持模型执行，请升级。">Agent 不支持</span>
                <span v-if="(h.model_running ?? 0) > 0" class="text-[10px] px-1.5 py-0.5 rounded-sm bg-emerald-500/10 text-emerald-700 dark:text-emerald-300 tabular-nums">{{ h.model_running }} 条进行中</span>
              </div>
              <p v-if="h.temporary" class="text-[11px] leading-snug text-muted-foreground">临时启用的主机重启即消失，不支持模型访问。</p>
              <template v-else-if="h.allow_model">
                <div class="flex flex-wrap items-center gap-2">
                  <span class="text-[11px] text-muted-foreground">执行模式</span>
                  <div class="inline-flex rounded-sm border border-border/80 p-0.5 gap-0.5" role="group" aria-label="执行模式">
                    <button
                      v-for="m in EXEC_MODE_CHOICES"
                      :key="m.id || 'auto'"
                      type="button"
                      class="h-6 px-2 rounded-xs text-[11px] cursor-pointer transition-colors disabled:opacity-50"
                      :class="(h.exec_mode === 'auto' ? '' : (h.exec_mode ?? '')) === m.id ? 'bg-primary text-primary-foreground' : 'text-muted-foreground hover:bg-muted'"
                      :aria-pressed="(h.exec_mode === 'auto' ? '' : (h.exec_mode ?? '')) === m.id"
                      :disabled="busy[`model:${h.machine_code}`]"
                      @click="setExecMode(h, m.id)"
                    >{{ m.label }}</button>
                  </div>
                  <label class="flex items-center gap-2 text-xs ml-auto">
                    <Switch :checked="!!h.model_paused" :disabled="busy[`model:${h.machine_code}`]" aria-label="暂停这台主机的模型" @update:checked="(v: boolean) => setHostPaused(h, v)" />
                    <span>{{ h.model_paused ? '已暂停' : '暂停此主机的模型' }}</span>
                  </label>
                </div>
                <p class="text-[11px] leading-snug text-muted-foreground">{{ modeHint(h) }}令牌自己的模式上限只会让它更严，不会放宽这里的设置。</p>
              </template>
              <p v-else class="text-[11px] leading-snug text-muted-foreground">关闭时，模型看不到这台主机，也不能在上面执行命令；人工终端不受影响。</p>
              <p v-if="isGatewayHost(h)" class="text-[11px] leading-snug text-muted-foreground">
                经网关接入的主机，一票否决在网关主机本机（<code class="font-mono">--deny-model-exec</code>）；除此之外能不能执行由这里、令牌授权和服务端开关决定。建议使用权限受限的专用账号，并优先选「需批准」。
              </p>
              <p v-else-if="isSshHost(h)" class="text-[11px] leading-snug text-muted-foreground">
                Hub 直连的 SSH 主机没有本机的一票否决，能不能执行完全由这里、令牌授权和服务端开关决定。建议使用权限受限的专用账号，并优先选「需批准」。
              </p>
            </div>

            <div v-if="h.holder?.lost?.length" class="pt-3 border-t border-border/60 space-y-1.5" data-testid="lost-sessions">
              <div class="flex items-center gap-2">
                <p class="text-[11px] font-medium text-amber-600 dark:text-amber-400">有会话已不存在</p>
                <span class="flex-1" />
                <button
                  v-if="h.holder.lost.length > 1"
                  type="button"
                  class="h-6 px-2 rounded-sm text-[11px] text-muted-foreground hover:text-foreground hover:bg-muted cursor-pointer transition-colors disabled:opacity-50"
                  title="忽略这台主机上的全部提示（不会删除任何记录）"
                  data-testid="lost-dismiss-all"
                  :disabled="busy[`lost:${h.machine_code}`]"
                  @click="dismissLost(h)"
                >全部清除</button>
              </div>
              <ul class="space-y-1">
                <li v-for="l in h.holder.lost" :key="l.sid" class="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs" :data-testid="`lost-item-${l.sid}`">
                  <span class="font-medium truncate max-w-[12rem]">{{ l.name || `会话 ${l.sid.slice(0, 6)}` }}</span>
                  <span class="text-muted-foreground">因{{ lostReasonLabel(l.reason) }}结束<template v-if="lostTime(l)"> · {{ lostTime(l) }}</template></span>
                  <span v-if="l.cwd" class="text-muted-foreground font-mono truncate max-w-[16rem]" :title="l.cwd">{{ l.cwd }}</span>
                  <Button variant="outline" size="sm" class="h-6 px-2 text-[11px] rounded-sm gap-1" :disabled="!h.online || busy[`new:${h.machine_code}`]" @click="rebuild(h, l)">
                    <RotateCcw class="w-3 h-3" />在原目录重建
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    class="h-6 px-2 text-[11px] rounded-sm gap-1 text-muted-foreground hover:text-foreground"
                    title="忽略这条提示（只是不再提醒，不会删除任何记录）"
                    :aria-label="`忽略提示：${l.name || `会话 ${l.sid.slice(0, 6)}`}`"
                    :data-testid="`lost-dismiss-${l.sid}`"
                    :disabled="busy[`lost:${h.machine_code}`]"
                    @click="dismissLost(h, l)"
                  >
                    <X class="w-3 h-3" />忽略
                  </Button>
                </li>
              </ul>
            </div>

            <div class="flex flex-wrap items-center gap-2 pt-3 border-t border-border/60">
              <template v-if="!isSshHost(h)">
                <Button size="sm" class="h-8 rounded-sm text-xs gap-1.5" :disabled="!h.online || busy[`new:${h.machine_code}`]" @click="create(h)">
                  <Loader2 v-if="busy[`new:${h.machine_code}`]" class="w-3.5 h-3.5 animate-spin" />
                  <Plus v-else class="w-3.5 h-3.5" />新建会话
                </Button>
                <Button variant="outline" size="sm" class="h-8 rounded-sm text-xs gap-1.5" :disabled="!h.online || busy[`new:${h.machine_code}`]" @click="create(h, { incognito: true })">
                  <EyeOff class="w-3.5 h-3.5" />新建无痕会话
                </Button>
                <Button v-if="canUpgrade(h)" variant="outline" size="sm" class="h-8 rounded-sm text-xs gap-1.5" data-testid="upgrade-open-btn" @click="openUpgrade(h)">
                  <ArrowUpCircle class="w-3.5 h-3.5" />升级 Agent…
                </Button>
              </template>
              <template v-else>
                <Button
                  size="sm"
                  class="h-8 rounded-sm text-xs gap-1.5"
                  :disabled="!canOpenSshTerminal(h) || busy[`ssh-term:${h.machine_code}`]"
                  :title="sshTerminalBlocked(h) || undefined"
                  @click="openSshTerminal(h)"
                >
                  <Loader2 v-if="busy[`ssh-term:${h.machine_code}`]" class="w-3.5 h-3.5 animate-spin" />
                  <SquareTerminal v-else class="w-3.5 h-3.5" />打开终端
                </Button>
                <!-- 无痕只对有记录的会话有意义：Hub 直连的 SSH 终端本来就不记录 -->
                <Button
                  v-if="sshTerminalRecorded(h)"
                  variant="outline"
                  size="sm"
                  class="h-8 rounded-sm text-xs gap-1.5"
                  :disabled="!canOpenSshTerminal(h) || busy[`ssh-term:${h.machine_code}`]"
                  :title="sshTerminalBlocked(h) || undefined"
                  @click="openSshTerminal(h, { incognito: true })"
                >
                  <EyeOff class="w-3.5 h-3.5" />打开无痕终端
                </Button>
                <Button variant="outline" size="sm" class="h-8 rounded-sm text-xs gap-1.5" :disabled="sshTesting[h.machine_code] || (!isGatewayHost(h) && !h.ssh?.backend_enabled)" @click="testSsh(h)">
                  <Loader2 v-if="sshTesting[h.machine_code]" class="w-3.5 h-3.5 animate-spin" />
                  <Plug v-else class="w-3.5 h-3.5" />测试连接
                </Button>
                <Button variant="outline" size="sm" class="h-8 rounded-sm text-xs gap-1.5" @click="openSsh('edit', h)">
                  <Pencil class="w-3.5 h-3.5" />修改连接
                </Button>
                <Button v-if="h.ssh?.key_state === 'mismatch'" variant="outline" size="sm" class="h-8 rounded-sm text-xs gap-1.5 border-destructive/40 text-destructive hover:text-destructive" @click="openSsh('rekey', h)">
                  <ShieldAlert class="w-3.5 h-3.5" />核对并重新确认密钥
                </Button>
              </template>
              <span class="flex-1" />
              <Button variant="ghost" size="sm" class="h-8 rounded-sm text-xs gap-1.5" @click="askDisable(h)"><Power class="w-3.5 h-3.5" />停用</Button>
              <Button variant="ghost" size="sm" class="h-8 rounded-sm text-xs gap-1.5 text-destructive hover:text-destructive" @click="askDelete(h)"><Trash2 class="w-3.5 h-3.5" />删除</Button>
            </div>
          </div>
        </article>

        <!-- 已停用 -->
        <section v-if="disabled.length" class="space-y-2" aria-label="已停用的主机">
          <h2 class="text-[11px] font-medium text-muted-foreground px-1">已停用</h2>
          <ul class="space-y-1.5">
            <li v-for="h in disabled" :key="h.machine_code" class="flex items-center gap-2.5 rounded-sm border border-border/60 px-3 h-12">
              <component :is="isSshHost(h) ? KeyRound : h.os.toLowerCase().includes('windows') ? Laptop : Server" class="w-4 h-4 shrink-0 text-muted-foreground" />
              <div class="min-w-0 flex-1">
                <p class="truncate text-xs text-muted-foreground">{{ hostTitle(h) }}</p>
                <p class="truncate text-[10px] text-muted-foreground/80 font-mono leading-tight">{{ shortCode(h.machine_code) }}</p>
              </div>
              <Button variant="outline" size="sm" class="h-7 px-2.5 text-xs rounded-sm" :disabled="busy[`en:${h.machine_code}`]" @click="enableHost(h)">
                <Loader2 v-if="busy[`en:${h.machine_code}`]" class="w-3 h-3 animate-spin" />启用
              </Button>
              <button type="button" class="p-1.5 rounded-sm text-muted-foreground hover:text-destructive hover:bg-muted cursor-pointer" :aria-label="`删除 ${hostTitle(h)}`" @click="askDelete(h)">
                <Trash2 class="w-3.5 h-3.5" />
              </button>
            </li>
          </ul>
        </section>
      </div>
    </div>

    <SshHostDialog v-model:open="sshDlg.open" :mode="sshDlg.mode" :host="sshDlg.host" />
    <AgentUpgradeDialog v-model:open="upgradeDlg.open" :code="upgradeDlg.code" />

    <ConfirmDialog
      v-model:open="confirm.open"
      :title="confirm.title"
      :description="confirm.description"
      :confirm-text="confirm.button"
      :busy="confirm.busy"
      destructive
      @confirm="runConfirm"
    >
      <!-- 设备端怎么办：能同时卸载就给勾选项（默认不勾）；不能就说明原因，并给出在设备上手动卸载的命令 -->
      <div v-if="confirm.kind === 'delete' && confirm.agentNote" class="space-y-2 text-xs" data-testid="delete-device-note">
        <label v-if="confirm.agentNote.canUninstall" class="flex items-start gap-2 cursor-pointer select-none">
          <input v-model="confirm.uninstall" type="checkbox" class="mt-0.5 accent-primary" :disabled="confirm.busy" />
          <span class="min-w-0">
            <span class="font-medium text-foreground">同时卸载设备端</span>
            <span class="block text-muted-foreground leading-relaxed">
              通知设备停止并移除这台主机上的 Agent 服务与身份文件；终端录像与命令输出仍保留在设备的数据目录里。
            </span>
          </span>
        </label>
        <div
          v-if="!confirm.agentNote.canUninstall || !confirm.uninstall"
          class="rounded-sm border border-border/70 bg-muted/40 px-2.5 py-2 text-muted-foreground leading-relaxed"
        >
          <p v-if="confirm.agentNote.reason">{{ confirm.agentNote.reason }}</p>
          <template v-if="confirm.agentNote.command">
            <p>
              {{
                confirm.agentNote.canUninstall
                  ? '不勾选的话，设备上的 Agent 仍在运行，会继续尝试连接（被服务端拒绝）。需要时在设备上手动执行：'
                  : '删除后设备上的 Agent 仍在运行，会继续尝试连接。需要卸载时，在设备上执行：'
              }}
            </p>
            <code class="mt-1 block font-mono text-[11px] text-foreground select-all break-all">{{ confirm.agentNote.command }}</code>
            <p class="mt-1 text-[11px]">{{ confirm.agentNote.note }}</p>
          </template>
        </div>
      </div>
    </ConfirmDialog>
  </div>
</template>
