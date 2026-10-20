<script setup lang="ts">
// 工具栏里的「收件箱状态块」（D-212 WP-H）。
// 原先这些信息（Agent 最近活动、提醒方式、hook 状态与安装命令）挤在对话最底下的一条状态带里，
// 既占地方、又只在有追加时才出现；现在按钮上只给一眼能看懂的结论
// （编辑器 hook 最近一次来问收件箱的时间 + 排队 / 待确认条数），点开才是完整信息。
// 它同时负责让这些数据保持新鲜：有未确认条目时每 15 秒、否则每 60 秒重新拉取一次
// （页面在后台时不拉），相对时间靠同一个低频时钟推动。
// 浮层里还放着这个工作流的「异步对话」开关与授权档位选择（它依赖 hook，所以就放在 hook 的状态旁边）。
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Inbox } from 'lucide-vue-next'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { Switch } from '@/components/ui/switch'
import { toast } from '@/components/ui/sonner'
import { copyText } from '@/lib/terminal/clipboard'
import { useSessionStore } from '@/stores/session'
import { useAsyncModeStore } from '@/stores/asyncMode'
import { asyncModeErrorMessage } from '@/api/asyncMode'
import { agentActivity, hasLiveItems, hookInstallCommands, hookStatus, inboxChipTextIsTimed, inboxChipView, layersSummary } from '@/lib/inbox'
import { profileOptionLabel, summarizeAsyncMode } from '@/lib/asyncMode'

const props = defineProps<{ workflowId: string }>()

const sessionStore = useSessionStore()
const asyncStore = useAsyncModeStore()
const open = ref(false)
const showInstall = ref(false)
const showWindows = ref(false)

const now = ref(Date.now())
let clock: number | undefined
let lastPoll = Date.now()

const items = computed(() => sessionStore.queuedFeedbacks ?? [])
const view = computed(() =>
  inboxChipView({
    cfg: sessionStore.inboxConfig,
    hookSeenAt: sessionStore.hookLastSeenAt,
    activityAt: sessionStore.agentLastActivityAt,
    items: items.value,
    nowMs: now.value,
  }),
)
const activity = computed(() => agentActivity(sessionStore.agentLastActivityAt, now.value))
const layers = computed(() => layersSummary(sessionStore.inboxConfig))
const hook = computed(() => hookStatus(sessionStore.hookLastSeenAt, sessionStore.inboxConfig, now.value))
const installCmds = computed(() =>
  hookInstallCommands(typeof window !== 'undefined' ? window.location.origin : '', props.workflowId),
)

// ---- 异步对话 ----
const asyncView = computed(() => sessionStore.asyncMode)
const asyncSummary = computed(() => summarizeAsyncMode(asyncView.value))
/** 工作流选用的档位已被删掉时，下拉里显示「跟随默认」（服务端同样回落到默认档位）。 */
const selectedProfileId = computed(() => {
  const id = asyncView.value?.profile_id ?? 0
  return asyncStore.profiles.some(p => p.id === id) ? id : 0
})
const defaultProfile = computed(() => asyncStore.profiles.find(p => p.is_default) ?? null)

async function toggleAsync(next: boolean) {
  try {
    const view = await asyncStore.setEnabled(props.workflowId, next)
    if (next) {
      toast.success(view.effective ? '已开启异步对话' : '已开启异步对话，暂未生效', {
        description: view.effective ? '智能体下一次调用 RelayMesh 时会收到通知' : view.reason_text,
        duration: 2400,
      })
    } else {
      toast.success('已关闭异步对话', { description: '已回到默认的逐条确认模式', duration: 1800 })
    }
  } catch (err) {
    toast.error(asyncModeErrorMessage(err, '无法切换异步对话'))
  }
}

async function pickProfile(e: Event) {
  const id = Number((e.target as HTMLSelectElement).value)
  try {
    await asyncStore.selectProfile(props.workflowId, id)
  } catch (err) {
    toast.error(asyncModeErrorMessage(err, '无法切换授权档位'))
  }
}

function dotClass(level: string): string {
  if (level === 'active') return 'bg-emerald-500 animate-pulse'
  if (level === 'recent') return 'bg-amber-500'
  if (level === 'none') return 'bg-destructive/70'
  return 'bg-muted-foreground/50'
}

function tick() {
  const t = Date.now()
  now.value = t
  if (typeof document !== 'undefined' && document.visibilityState !== 'visible') return
  const every = hasLiveItems(items.value) ? 15_000 : 60_000
  if (t - lastPoll >= every) {
    lastPoll = t
    void sessionStore.fetchQueuedFeedbacks(props.workflowId)
  }
}

// 点开的那一刻就拉一次，浮层里的「最近一次」不会停在上次轮询的结果上
watch(open, isOpen => {
  if (!isOpen) return
  now.value = Date.now()
  lastPoll = now.value
  void sessionStore.fetchQueuedFeedbacks(props.workflowId)
  void asyncStore.loadProfiles()
})

onMounted(() => {
  clock = window.setInterval(tick, 5000)
})
onBeforeUnmount(() => {
  if (clock !== undefined) {
    window.clearInterval(clock)
    clock = undefined
  }
})

async function copyInstall(text: string) {
  const ok = await copyText(text)
  if (ok) toast.success('已复制 安装命令', { description: text, duration: 1800 })
  else toast.error('复制失败，请手动选中命令复制')
}
</script>

<template>
  <Popover v-if="view" v-model:open="open">
    <PopoverTrigger as-child>
      <button
        type="button"
        data-testid="inbox-chip"
        :data-level="view.level"
        :aria-label="`收件箱状态 ${view.text}`"
        :title="view.title"
        class="text-[11px] sm:text-xs px-2 sm:px-2.5 py-1 rounded-sm border transition-all shadow-2xs flex items-center gap-1.5 cursor-pointer shrink-0 bg-card/95 hover:bg-muted border-border/80 text-foreground backdrop-blur-xs select-none font-mono outline-none focus-visible:ring-1 focus-visible:ring-ring"
      >
        <span class="relative inline-flex shrink-0">
          <Inbox class="w-3.5 h-3.5 text-muted-foreground" />
          <span
            data-testid="inbox-chip-dot"
            class="absolute -top-0.5 -right-0.5 w-1.5 h-1.5 rounded-full ring-1 ring-card"
            :class="dotClass(view.level)"
          ></span>
        </span>
        <!-- 「hook N 秒前」这类随时间变的文字固定占 8em，免得后面的芯片被推着走（#197）；其余几种文字只在状态变化时才换 -->
        <span data-testid="inbox-chip-text" class="tabular-nums" :class="inboxChipTextIsTimed(view.level) ? 'min-w-[8em]' : ''">{{ view.text }}</span>
        <span
          v-if="view.queued > 0"
          data-testid="inbox-chip-queued"
          class="tabular-nums text-amber-600 dark:text-amber-400"
          title="排队中：Agent 还没读到"
        >待提取 {{ view.queued }}</span>
        <span
          v-if="view.delivered > 0"
          data-testid="inbox-chip-delivered"
          class="tabular-nums text-sky-600 dark:text-sky-400"
          title="已被 Agent 读取，等待它确认"
        >待确认 {{ view.delivered }}</span>
      </button>
    </PopoverTrigger>

    <!-- data-testid 放在里面的容器上：PopoverContent 的根是 Teleport，挂在它上面的属性不会落到 DOM 里 -->
    <PopoverContent
      align="start"
      class="w-[min(26rem,calc(100vw-1.5rem))] p-3 font-mono text-[11px] sm:text-xs"
    >
      <div data-testid="inbox-chip-popover" class="space-y-2.5">
        <div class="font-semibold text-foreground">
          收件箱<span class="font-normal text-muted-foreground/70"> · 您追加的指令怎么送到 Agent</span>
        </div>

        <!-- Agent 最近一次与 RelayMesh 交互 -->
        <div class="flex items-start gap-2 text-muted-foreground">
          <span class="mt-1 inline-block w-1.5 h-1.5 rounded-full shrink-0" :class="dotClass(activity.level)"></span>
          <span>
            Agent 最近一次与 RelayMesh 交互：
            <span class="text-foreground font-semibold" data-testid="inbox-agent-activity" :data-level="activity.level">{{ activity.text }}</span>
          </span>
        </div>

        <!-- 当前生效的提醒方式 -->
        <div v-if="layers.text" class="text-muted-foreground" data-testid="inbox-layers" :data-mode="layers.mode">{{ layers.text }}</div>

        <!-- 编辑器 hook 有没有装好、在不在跑 -->
        <div
          v-if="hook.level !== 'hidden'"
          class="flex items-start gap-2 text-muted-foreground"
          data-testid="inbox-hook-status"
          :data-level="hook.level"
        >
          <span class="mt-1 inline-block w-1.5 h-1.5 rounded-full shrink-0" :class="dotClass(hook.level)"></span>
          <span>
            {{ hook.text }}
            <button
              v-if="hook.showInstall"
              type="button"
              class="ml-1 underline underline-offset-2 hover:text-foreground cursor-pointer select-none"
              data-testid="inbox-hook-install-toggle"
              @click="showInstall = !showInstall"
            >{{ showInstall ? '收起' : '怎么装？' }}</button>
          </span>
        </div>

        <div
          v-if="showInstall && hook.showInstall"
          class="p-2.5 rounded-md border border-border/80 bg-muted/30 space-y-2 text-muted-foreground"
          data-testid="inbox-hook-install"
        >
          <p class="text-foreground font-semibold">它做什么</p>
          <p>在你工作时把 Web 上的追加指令实时送进对话，并在顶栏显示智能体真实运行状态（异步对话也依赖它）。</p>
          <p class="text-foreground font-semibold">怎么装</p>
          <p>在项目根目录执行，一条命令装好：</p>
          <pre class="whitespace-pre-wrap break-all text-foreground bg-background/60 border border-border/70 rounded-xs p-2 select-all" data-testid="inbox-hook-install-command">{{ installCmds.primary }}</pre>
          <div class="flex flex-wrap items-center gap-x-3 gap-y-1">
            <button type="button" class="underline underline-offset-2 hover:text-foreground cursor-pointer" data-testid="inbox-hook-install-copy" @click="copyInstall(installCmds.primary)">复制命令</button>
            <button type="button" class="underline underline-offset-2 hover:text-foreground cursor-pointer" @click="showWindows = !showWindows">{{ showWindows ? '收起 Windows' : 'Windows 命令' }}</button>
          </div>
          <pre v-if="showWindows && installCmds.windows" class="whitespace-pre-wrap break-all text-foreground bg-background/60 border border-border/70 rounded-xs p-2 select-all">{{ installCmds.windows }}</pre>
          <p class="text-foreground font-semibold">怎么确认</p>
          <p>装好后运行 <code class="text-foreground">relaymesh-hook status</code>；本页芯片会显示「hook 刚刚」，顶栏执行图标会反映 hook 汇报的运行状态。令牌不会写进任何项目文件。</p>
        </div>

        <!-- 异步对话：开关 + 授权档位 -->
        <div
          v-if="asyncView"
          class="pt-2 border-t border-border/60 space-y-1.5"
          data-testid="async-mode-section"
          :data-state="asyncSummary.state"
        >
          <div class="flex items-center justify-between gap-2">
            <span class="font-semibold text-foreground">
              异步对话<span class="font-normal text-muted-foreground/70"> · 发完进度就继续工作</span>
            </span>
            <Switch
              data-testid="async-mode-switch"
              aria-label="异步对话"
              :checked="asyncView.enabled"
              :disabled="!asyncSummary.canToggle || asyncStore.busy"
              :title="asyncSummary.blockedReason || undefined"
              @update:checked="(v: boolean) => toggleAsync(v)"
            />
          </div>
          <p class="text-muted-foreground" data-testid="async-mode-status">
            <b class="text-foreground">{{ asyncSummary.label }}</b>
            <span v-if="asyncSummary.detail"> · {{ asyncSummary.detail }}</span>
          </p>
          <p v-if="asyncSummary.blockedReason" class="text-amber-600 dark:text-amber-400" data-testid="async-mode-blocked">
            {{ asyncSummary.blockedReason }}
          </p>
          <label v-if="asyncStore.profiles.length && (asyncView.enabled || asyncView.can_enable)" class="flex items-center gap-2 text-muted-foreground">
            <span class="shrink-0">授权档位</span>
            <select
              data-testid="async-mode-profile"
              class="min-w-0 flex-1 rounded-xs border border-border bg-background text-foreground text-[11px] py-0.5 px-1 cursor-pointer"
              :value="selectedProfileId"
              :disabled="asyncStore.busy"
              @change="pickProfile"
            >
              <option :value="0">跟随默认{{ defaultProfile ? `（${defaultProfile.name}）` : '' }}</option>
              <option v-for="p in asyncStore.profiles" :key="p.id" :value="p.id">{{ profileOptionLabel(p) }}</option>
            </select>
          </label>
          <p v-if="asyncView.profile && (asyncView.enabled || asyncView.can_enable)" class="text-muted-foreground/80" data-testid="async-mode-profile-rules">
            {{ asyncView.profile.rules }}
          </p>
        </div>

        <div class="pt-2 border-t border-border/60 text-muted-foreground" data-testid="inbox-counts">
          排队中 <b class="text-foreground tabular-nums">{{ view.queued }}</b> 条 · 已读取待确认 <b class="text-foreground tabular-nums">{{ view.delivered }}</b> 条
          <span class="block mt-0.5 text-muted-foreground/70">每条追加的状态与撤回按钮，在对话里它所在的位置。</span>
        </div>
      </div>
    </PopoverContent>
  </Popover>
</template>
