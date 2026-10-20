<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { toast } from 'vue-sonner'
import { ArrowUpCircle, CircleCheck, CircleX, Copy, Info, Loader2, LockKeyhole, Plus, Rocket, TriangleAlert } from 'lucide-vue-next'
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription } from '../ui/dialog'
import Button from '../ui/button/Button.vue'
import InstallCommand from './InstallCommand.vue'
import { useTerminalStore } from '../../stores/terminal'
import { toApiError, type WebUpgradeSessions } from '../../api/terminal'
import { copyText } from '../../lib/terminal/clipboard'
import { formatCountdown, hostTitle } from '../../lib/terminal/format'
import {
  UPGRADE_WINDOW_MAX_SECONDS,
  UPGRADE_WINDOW_MINUTES,
  UPGRADE_WINDOW_WARN_SECONDS,
  defaultUpgradeScope,
  upgradeBlock,
  upgradeBlockText,
  upgradeSummary,
  upgradeTargetOS,
  upgradeWindowOpen,
} from '../../lib/terminal/upgrade'
import {
  WEB_UPGRADE_OFF_COMMAND,
  WEB_UPGRADE_ON_COMMAND,
  jobActive,
  jobNotice,
  localSessions,
  sessionWarning,
  sessionsNeedConfirm,
  webUpgradeMode,
} from '../../lib/terminal/webUpgrade'

// 升级一台主机上的 Agent。升级始终是「主机自己去取」，服务端不会把程序推送到主机上，有两种方式：
//   - 一键升级：主机所有者先在本机允许（relaymesh-agent web-upgrade on，只能在主机上改），之后管理员在这里点一下，
//     由 Agent 自己下载、校验、试运行新程序，再交给独立的升级进程替换并重启；新版本连不上服务端时自动还原旧版本。
//     主机上有正在运行的会话时先提醒并要求确认（服务端同样会核对）。
//   - 升级窗口（手动）：开启一个只对这台主机有效的窗口（默认 10 分钟，可延时、可提前结束），窗口期间生成的升级命令能从服务端取到安装文件，
//     在主机上执行即可。服务端检测到这台主机用新版本连上来后，会自动关闭窗口。
const open = defineModel<boolean>('open', { required: true })
const props = defineProps<{ code: string }>()
const store = useTerminalStore()

const host = computed(() => store.hosts.find(h => h.machine_code === props.code))
const win = computed(() => host.value?.upgrade_window)
const windowOpen = computed(() => !!host.value && upgradeWindowOpen(host.value))
const block = computed(() => (host.value ? upgradeBlock(host.value, store.status?.install) : null))
const targetOS = computed(() => (host.value ? upgradeTargetOS(host.value.os) : null))
const scope = computed(() => defaultUpgradeScope(host.value?.privilege ?? ''))

// ---------- 倒计时 ----------
// remaining_seconds 是服务端算好的剩余秒数（不受浏览器与服务器时钟差影响）；每次轮询拿到新值时记下时刻，其间用本地秒表递减。
const tick = ref(Date.now())
let timer: ReturnType<typeof setInterval> | null = null
function stopTimer() {
  if (timer) clearInterval(timer)
  timer = null
}
watch(
  open,
  o => {
    stopTimer()
    if (!o) return
    tick.value = Date.now()
    timer = setInterval(() => (tick.value = Date.now()), 1000)
  },
  { immediate: true },
)
onBeforeUnmount(stopTimer)

const seen = ref({ remaining: 0, at: 0 })
const baseVersion = ref('')
watch(
  win,
  w => {
    if (!w?.open) return
    seen.value = { remaining: w.remaining_seconds, at: Date.now() }
    baseVersion.value = w.base_version || host.value?.agent_version || ''
  },
  { immediate: true },
)
const remaining = computed(() => (windowOpen.value ? Math.max(0, seen.value.remaining - Math.floor((tick.value - seen.value.at) / 1000)) : 0))
const ending = computed(() => windowOpen.value && remaining.value <= UPGRADE_WINDOW_WARN_SECONDS)
const canExtend = computed(() => remaining.value < UPGRADE_WINDOW_MAX_SECONDS - 5)

// ---------- 窗口结束的说明 ----------
// 对话框开着时窗口消失了：版本变了就是升级完成（服务端自动关的窗），否则是到期或被结束。
const ended = ref<'' | 'upgraded' | 'closed'>('')
// 一键升级会替这次升级开一个窗口、结束时自己关掉：那不是管理员手动开的，窗口消失时不必再解释一遍（请求自己的进展 / 结果提示已经说明了）。
const windowOfWebUpgrade = ref(false)
watch(windowOpen, (now, was) => {
  if (now) {
    ended.value = ''
    return
  }
  const ownedByWebUpgrade = windowOfWebUpgrade.value
  windowOfWebUpgrade.value = false
  if (was && open.value && !ownedByWebUpgrade) {
    const version = host.value?.agent_version ?? ''
    ended.value = baseVersion.value && version && version !== baseVersion.value ? 'upgraded' : 'closed'
  }
})
watch([open, () => props.code], () => {
  ended.value = ''
  windowOfWebUpgrade.value = false
})

// ---------- 一键升级 ----------
const mode = computed(() => (host.value ? webUpgradeMode(host.value) : 'current'))
const job = computed(() => host.value?.upgrade_job ?? null)
watch(
  [windowOpen, () => jobActive(job.value)],
  ([isOpen, active]) => {
    if (isOpen && active) windowOfWebUpgrade.value = true
  },
  { immediate: true },
)
const notice = computed(() => (job.value ? jobNotice(job.value) : null))
const lastNote = computed(() => {
  const n = host.value?.last_upgrade
  // 请求记录还在时以请求为准；只有请求记录没了（服务端重启、过了保留期）而主机还记着一次没成功的升级时，才补一句。
  if (!n || n.outcome === 'ok' || job.value) return ''
  const when = n.at ? new Date(n.at * 1000).toLocaleString() : ''
  const what = n.outcome === 'rolled_back' ? '新版本没有连上服务端，已自动还原为旧版本' : '升级进程没能完成替换或重启'
  return `主机上记录的上一次升级${when ? `（${when}）` : ''}没有成功：${what}${n.detail ? `。${n.detail}` : ''}`
})

/** 服务端在 409 sessions_active 里回报的会话摘要（比界面自己算的更准：它刚刚核对过）。 */
const serverSessions = ref<WebUpgradeSessions | null>(null)
const sessions = computed<WebUpgradeSessions | null>(() => serverSessions.value ?? (host.value ? localSessions(host.value) : null))
const warning = computed(() => (sessions.value && sessionsNeedConfirm(sessions.value) ? sessionWarning(sessions.value) : null))
const confirming = ref(false)
const starting = ref(false)

watch([open, () => props.code], () => {
  confirming.value = false
  serverSessions.value = null
})
// 活动会话都结束了：不需要再确认。
watch(warning, w => {
  if (!w) confirming.value = false
})

const modeHint = computed(() => {
  const latest = host.value?.latest_agent_version || '服务端自带的版本'
  switch (mode.value) {
    case 'running':
      return '升级进行中。这期间这台主机上的 Agent 会重启一次，终端页面会短暂断开后自动重连。'
    case 'ready':
      return `这台主机的所有者已允许从 Web 升级：Agent 会自己从这台服务端下载 ${latest} 并校验，试运行新程序确认它能在这台主机上运行，再交给独立的升级进程替换并重启；新版本连不上服务端时会自动还原旧版本。`
    case 'owner-off':
      return '这台主机上的 Agent 支持一键升级，但主机所有者没有允许。是否允许由主机上的人决定，服务端无法替他打开。'
    case 'unsupported':
      return '这台主机上的 Agent 不支持一键升级（版本较旧、以前台方式运行，或所在系统暂不受支持）。用下面的升级窗口升级一次后，主机所有者就可以在本机开启一键升级。'
    case 'offline':
      return '这台主机当前离线：一键升级需要 Agent 在线。可以先用下面的升级窗口生成升级命令，等主机上线后在主机上执行。'
    default:
      return ''
  }
})

async function startWebUpgrade() {
  if (!host.value || starting.value) return
  // 有活动会话：第一次点击只展开提醒，由管理员确认后才真正发请求。
  if (warning.value && !confirming.value) {
    confirming.value = true
    return
  }
  starting.value = true
  try {
    await store.webUpgrade(props.code, confirming.value)
    confirming.value = false
    serverSessions.value = null
  } catch (e) {
    const err = toApiError(e)
    if (err.code === 'sessions_active' && err.data.sessions) {
      // 点击之前界面没有看到的会话（刚刚新开的）：服务端要求确认，展示它核对过的摘要。
      serverSessions.value = err.data.sessions as WebUpgradeSessions
      confirming.value = true
    } else {
      store.notifyError(e, '发起一键升级失败')
    }
  } finally {
    starting.value = false
  }
}

async function copyCommand(command: string) {
  if (await copyText(command)) toast.success('已复制命令')
  else toast.error('复制失败，请手动选中命令复制')
}

const noticeClass: Record<string, string> = {
  progress: 'border-border/80 bg-muted/40 text-foreground',
  success: 'border-emerald-500/40 bg-emerald-500/10 text-emerald-800 dark:text-emerald-200',
  error: 'border-red-500/40 bg-red-500/10 text-red-800 dark:text-red-200',
  warn: 'border-amber-500/40 bg-amber-500/10 text-amber-800 dark:text-amber-200',
}

// ---------- 升级窗口操作 ----------
const busy = ref(false)
async function run(fn: () => Promise<unknown>, failure: string) {
  busy.value = true
  try {
    await fn()
  } catch (e) {
    store.notifyError(e, failure)
  } finally {
    busy.value = false
  }
}
const openWindow = () => run(() => store.openUpgradeWindow(props.code, UPGRADE_WINDOW_MINUTES), '开启升级窗口失败')
/** 延时：在剩余时间上累加，已经生成的升级命令继续有效。 */
const extendWindow = () => run(() => store.openUpgradeWindow(props.code, UPGRADE_WINDOW_MINUTES, true), '延时升级窗口失败')
const closeWindow = () => run(() => store.closeUpgradeWindow(props.code), '结束升级窗口失败')
</script>

<template>
  <Dialog v-model:open="open">
    <!-- 标题栏右侧给关闭按钮留了位置（pr-12）；DialogContent 的 showClose 是布尔属性，不写就是 false，必须显式打开。 -->
    <DialogContent class="max-w-2xl rounded-sm p-0 gap-0 max-h-[90vh] overflow-y-auto" :show-close="true" data-testid="upgrade-dialog">
      <DialogHeader class="px-5 pt-5 pb-3 text-left space-y-1 pr-12">
        <DialogTitle class="text-sm font-bold flex items-center gap-2">
          <ArrowUpCircle class="w-4 h-4 text-muted-foreground" />
          <span class="truncate">升级 Agent<template v-if="host"> · {{ hostTitle(host) }}</template></span>
        </DialogTitle>
        <DialogDescription class="text-xs leading-relaxed text-muted-foreground" data-testid="upgrade-summary">
          {{ host ? upgradeSummary(host) : '这台主机已经不存在了。' }}
        </DialogDescription>
      </DialogHeader>

      <div v-if="host" class="px-5 pb-5 space-y-4">
        <p class="text-xs leading-relaxed text-muted-foreground flex gap-1.5">
          <Info class="w-3.5 h-3.5 shrink-0 mt-px" />
          <span>
            升级始终是主机自己去取新版本，服务端不会把程序推送到主机上：下载后先校验，替换程序后只重启 Agent；服务设置与机器码不变，
            会话守护进程不会被重启，正在运行的终端会话不受影响。可以用下面的「一键升级」（需要主机所有者先在本机允许），也可以在主机上执行升级命令。
          </span>
        </p>

        <p
          v-if="block"
          class="rounded-sm border border-amber-500/40 bg-amber-500/10 px-3 py-2.5 text-xs leading-relaxed text-amber-800 dark:text-amber-200 flex gap-1.5"
          role="alert"
          data-testid="upgrade-blocked"
        >
          <TriangleAlert class="w-3.5 h-3.5 shrink-0 mt-px" />
          <span>{{ upgradeBlockText(block) }}</span>
        </p>

        <template v-else>
          <!-- ========== 一键升级 ========== -->
          <section
            v-if="mode !== 'current' || job"
            aria-labelledby="web-upgrade-title"
            class="rounded-sm border border-border/80 px-3 py-2.5 space-y-2.5"
            data-testid="webupgrade"
            :data-mode="mode"
          >
            <div class="flex flex-wrap items-start justify-between gap-2">
              <div class="min-w-0 text-xs">
                <h3 id="web-upgrade-title" class="font-semibold text-foreground flex items-center gap-1.5">
                  <Rocket class="w-3.5 h-3.5 text-muted-foreground" />
                  一键升级
                </h3>
                <p v-if="modeHint" class="text-muted-foreground mt-0.5 leading-relaxed" data-testid="webupgrade-hint">{{ modeHint }}</p>
              </div>
              <Button
                v-if="mode === 'ready' && !confirming"
                size="sm"
                class="h-7 rounded-sm text-xs shrink-0"
                :disabled="starting"
                :title="warning ? '这台主机上还有正在运行的会话，点击后会先让你确认' : `让这台主机的 Agent 自己升级到 ${host.latest_agent_version || '服务端自带的版本'}`"
                data-testid="webupgrade-start"
                @click="startWebUpgrade"
              >
                <Loader2 v-if="starting" class="w-3.5 h-3.5 animate-spin" />
                <Rocket v-else class="w-3.5 h-3.5" />
                升级到 {{ host.latest_agent_version || '自带版本' }}
              </Button>
            </div>

            <!-- 请求的进展 / 结果 -->
            <div
              v-if="notice"
              class="rounded-sm border px-3 py-2 text-xs leading-relaxed flex gap-1.5"
              :class="noticeClass[notice.tone]"
              :role="notice.tone === 'error' || notice.tone === 'warn' ? 'alert' : 'status'"
              data-testid="webupgrade-job"
              :data-state="job?.state"
              :data-tone="notice.tone"
            >
              <Loader2 v-if="notice.tone === 'progress'" class="w-3.5 h-3.5 shrink-0 mt-px animate-spin" />
              <CircleCheck v-else-if="notice.tone === 'success'" class="w-3.5 h-3.5 shrink-0 mt-px" />
              <CircleX v-else-if="notice.tone === 'error'" class="w-3.5 h-3.5 shrink-0 mt-px" />
              <TriangleAlert v-else class="w-3.5 h-3.5 shrink-0 mt-px" />
              <div class="min-w-0 space-y-0.5">
                <p class="font-medium" data-testid="webupgrade-job-title">{{ notice.title }}</p>
                <p v-if="notice.detail" class="break-words" data-testid="webupgrade-job-detail">{{ notice.detail }}</p>
              </div>
            </div>

            <!-- 活动会话提醒：点击之前就看得见；会话会被结束时用更醒目的颜色。 -->
            <div
              v-if="mode === 'ready' && warning"
              class="rounded-sm border px-3 py-2 text-xs leading-relaxed flex gap-1.5"
              :class="warning.severe ? noticeClass.error : noticeClass.warn"
              role="alert"
              data-testid="webupgrade-warning"
              :data-severe="warning.severe"
            >
              <TriangleAlert class="w-3.5 h-3.5 shrink-0 mt-px" />
              <div class="min-w-0 space-y-1">
                <p class="font-medium">{{ warning.headline }}</p>
                <p>{{ warning.detail }}</p>
                <div v-if="confirming" class="flex flex-wrap items-center gap-2 pt-1">
                  <Button
                    size="sm"
                    :variant="warning.severe ? 'destructive' : 'default'"
                    class="h-7 rounded-sm text-xs"
                    :disabled="starting"
                    data-testid="webupgrade-confirm"
                    @click="startWebUpgrade"
                  >
                    <Loader2 v-if="starting" class="w-3.5 h-3.5 animate-spin" />
                    {{ warning.severe ? '仍要升级（会结束这些会话）' : '确认升级' }}
                  </Button>
                  <Button variant="outline" size="sm" class="h-7 rounded-sm text-xs" :disabled="starting" data-testid="webupgrade-cancel" @click="confirming = false">
                    先不升级
                  </Button>
                </div>
              </div>
            </div>

            <!-- 主人没有允许：告诉他去主机上怎么开（服务端无法替他开）。 -->
            <div v-if="mode === 'owner-off'" class="rounded-sm border border-border/80 bg-muted/40 px-3 py-2 text-xs leading-relaxed space-y-1.5" data-testid="webupgrade-owner-off">
              <p class="flex gap-1.5 text-muted-foreground">
                <LockKeyhole class="w-3.5 h-3.5 shrink-0 mt-px" />
                <span>在这台主机上以运行 Agent 的身份执行下面的命令即可允许（随时可以用 off 关闭，用 status 查看）：</span>
              </p>
              <div class="flex items-center gap-2">
                <code class="flex-1 min-w-0 truncate rounded-sm bg-background border border-border px-2 py-1 font-mono select-all" data-testid="webupgrade-on-command">{{ WEB_UPGRADE_ON_COMMAND }}</code>
                <Button variant="outline" size="sm" class="h-7 rounded-sm text-xs shrink-0" aria-label="复制允许命令" data-testid="webupgrade-copy" @click="copyCommand(WEB_UPGRADE_ON_COMMAND)">
                  <Copy class="w-3.5 h-3.5" />
                  复制
                </Button>
              </div>
              <p class="text-muted-foreground">
                允许后 Agent 会自动重新连接并告知服务端，几秒内这里就会出现升级按钮；关闭用 <code class="font-mono">{{ WEB_UPGRADE_OFF_COMMAND }}</code>。
              </p>
            </div>

            <p v-if="lastNote" class="text-xs leading-relaxed text-muted-foreground break-words" data-testid="webupgrade-last">{{ lastNote }}</p>
          </section>

          <!-- ========== 升级窗口（手动升级）========== -->
          <!-- 一键升级进行中时，升级窗口是这次升级自己开的：不展示它的命令，免得有人拿着它在主机上并行再升一次。 -->
          <template v-if="mode !== 'running'">
            <section aria-labelledby="upgrade-window" class="flex flex-wrap items-center justify-between gap-2 rounded-sm border border-border/80 px-3 py-2.5">
              <div class="min-w-0 text-xs">
                <h3 id="upgrade-window" class="font-semibold text-foreground">升级窗口（手动升级）</h3>
                <p
                  v-if="windowOpen"
                  class="mt-0.5 tabular-nums"
                  :class="ending ? 'text-amber-600 dark:text-amber-400' : 'text-emerald-600 dark:text-emerald-400'"
                  data-testid="upgrade-countdown"
                >
                  开启中 · 剩余 {{ formatCountdown(remaining) }}<template v-if="ending">（即将关闭，需要的话先延时）</template>
                </p>
                <p v-else class="text-muted-foreground mt-0.5 leading-relaxed">
                  未开启。开启后才会生成升级命令，默认 {{ UPGRADE_WINDOW_MINUTES }} 分钟；检测到这台主机用新版本连上来，窗口会自动关闭。
                </p>
              </div>
              <div class="flex items-center gap-2">
                <!-- 延时放在「提前结束」左边：已经生成的命令继续有效；每次累加，窗口最长保持 60 分钟。 -->
                <Button
                  v-if="windowOpen"
                  variant="outline"
                  size="sm"
                  class="h-7 rounded-sm text-xs"
                  :disabled="busy || !canExtend"
                  :title="canExtend ? `在剩余时间上再加 ${UPGRADE_WINDOW_MINUTES} 分钟（已生成的升级命令继续有效）` : '升级窗口已经达到 60 分钟的上限，不能再延时'"
                  :aria-label="`延时 ${UPGRADE_WINDOW_MINUTES} 分钟`"
                  data-testid="upgrade-extend"
                  @click="extendWindow"
                >
                  <Loader2 v-if="busy" class="w-3.5 h-3.5 animate-spin" />
                  <Plus v-else class="w-3.5 h-3.5" />
                  延时 {{ UPGRADE_WINDOW_MINUTES }} 分钟
                </Button>
                <Button v-if="windowOpen" variant="outline" size="sm" class="h-7 rounded-sm text-xs" :disabled="busy" data-testid="upgrade-close" @click="closeWindow">
                  <Loader2 v-if="busy" class="w-3.5 h-3.5 animate-spin" />
                  提前结束
                </Button>
                <Button v-else size="sm" class="h-7 rounded-sm text-xs" :disabled="busy" data-testid="upgrade-open" @click="openWindow">
                  <Loader2 v-if="busy" class="w-3.5 h-3.5 animate-spin" />
                  开启 {{ UPGRADE_WINDOW_MINUTES }} 分钟
                </Button>
              </div>
            </section>

            <p
              v-if="ended === 'upgraded' && job?.state !== 'done'"
              class="rounded-sm border border-emerald-500/40 bg-emerald-500/10 px-3 py-2.5 text-xs leading-relaxed text-emerald-800 dark:text-emerald-200 flex gap-1.5"
              role="status"
              data-testid="upgrade-done"
            >
              <CircleCheck class="w-3.5 h-3.5 shrink-0 mt-px" />
              <span>已检测到 {{ host.agent_version }} 连接上来，升级完成，升级窗口已自动关闭。</span>
            </p>
            <p v-else-if="ended === 'closed'" class="text-xs leading-relaxed text-muted-foreground" role="status" data-testid="upgrade-closed">
              升级窗口已结束，之前生成的升级命令不再有效。需要的话重新开启。
            </p>

            <InstallCommand
              v-if="windowOpen && targetOS"
              action="upgrade"
              :locked-os="targetOS"
              :upgrade-ticket="win?.ticket"
              :default-scope="scope"
            />
          </template>
        </template>
      </div>
    </DialogContent>
  </Dialog>
</template>
