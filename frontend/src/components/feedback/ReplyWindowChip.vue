<script setup lang="ts">
// 异步对话场景的执行状态块（#123）：只显示「剩余的等待回复时间」。
//   [图标] 03:20   [+1m]
// 智能体每发一条新消息，窗口累加一段；你一回复，窗口清零；走完还没有消息，智能体才收尾。
// 1:1 场景那套（挂起倒计时 / 空回执 / 提示词等待）在这里没有意义，所以不出现。
// 输入时倒计时暂停（服务端同步暂停）；右键 / 点击展开菜单，调整每条新消息加多久、智能体单次最长等多久。
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { Hourglass, Moon, Pause, Plus, TimerOff } from 'lucide-vue-next'
import { toast } from 'vue-sonner'
import { useSessionStore } from '../../stores/session'
import { useSettingsStore } from '../../stores/settings'
import { usePresence } from '../../composables/usePresence'
import {
  DEFAULT_WAIT_SLICE,
  DEFAULT_WINDOW_ADD,
  WAIT_SLICE_OPTIONS,
  WINDOW_ADD_OPTIONS,
  windowTitle,
  windowView,
} from '../../lib/replyWindow'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuTrigger,
} from '../ui/dropdown-menu'

const sessionStore = useSessionStore()
const settingsStore = useSettingsStore()
const { typing, plus } = usePresence()

const now = ref(Date.now())
let timer: number | null = null
onMounted(() => {
  timer = window.setInterval(() => (now.value = Date.now()), 1000)
})
onUnmounted(() => {
  if (timer !== null) window.clearInterval(timer)
})

const mode = computed(() => sessionStore.asyncMode)
const addMin = computed(() => mode.value?.window_add_minutes || settingsStore.settings?.asyncWindowAddMinutes || DEFAULT_WINDOW_ADD)
const sliceMin = computed(() => mode.value?.wait_slice_minutes || settingsStore.settings?.asyncWaitSliceMinutes || DEFAULT_WAIT_SLICE)

const live = computed(() => windowView({
  until: mode.value?.reply_window_until,
  paused: mode.value?.reply_window_paused,
  bankSec: mode.value?.reply_window_bank_sec,
  activeTasks: mode.value?.active_tasks,
}, now.value))
// 输入期间显示冻结在开始输入的那一刻，停手后接着走（服务端同步暂停）
const frozen = ref<string | null>(null)
watch(
  () => typing.value && live.value.kind === 'running',
  (freeze) => {
    frozen.value = freeze ? live.value.text : null
  },
  { immediate: true },
)
const shownText = computed(() => frozen.value ?? live.value.text)
const title = computed(() => windowTitle(live.value, addMin.value, sliceMin.value))
const icon = computed(() => ({ none: TimerOff, running: Hourglass, paused: Pause, expired: Moon })[live.value.kind])
const iconClass = computed(() => ({
  none: 'text-muted-foreground',
  running: 'text-emerald-500 motion-safe:animate-pulse',
  paused: 'text-muted-foreground',
  expired: 'text-amber-500',
})[live.value.kind])
const timeClass = computed(() => {
  if (live.value.kind === 'none' || live.value.kind === 'paused') return 'text-muted-foreground'
  if (live.value.kind === 'expired') return 'text-amber-600 dark:text-amber-400'
  return 'text-foreground'
})

const menuOpen = ref(false)
async function onPlus() {
  try {
    const ok = await plus()
    // 成功不弹提示：倒计时本身会变长，提示条还会盖住这个按钮，影响连点
    if (!ok) toast.info('现在没有在等你回复', { description: '智能体发新消息后才会有回复窗口' })
  } catch (e: any) {
    toast.error('续时失败', { description: e?.message || String(e) })
  }
}
function pickAdd(v: number) {
  if (mode.value) mode.value.window_add_minutes = v
  settingsStore.updateSettings({ asyncWindowAddMinutes: v })
}
function pickSlice(v: number) {
  if (mode.value) mode.value.wait_slice_minutes = v
  settingsStore.updateSettings({ asyncWaitSliceMinutes: v })
}

const SEG = 'flex-1 justify-center cursor-pointer rounded-xs px-2 py-1 text-xs font-mono tabular-nums'
function seg(active: boolean): string {
  const tone = active
    ? 'bg-primary text-primary-foreground font-bold focus:bg-primary focus:text-primary-foreground'
    : 'bg-muted/50 hover:bg-muted'
  return `${SEG} ${tone}`
}
</script>

<template>
  <div class="flex items-center gap-1 shrink-0">
    <DropdownMenu v-model:open="menuOpen">
      <DropdownMenuTrigger as-child>
        <button
          type="button"
          data-testid="reply-window-chip"
          :data-kind="live.kind"
          :aria-label="`剩余等待回复时间 ${shownText}`"
          :title="title"
          class="text-[11px] sm:text-xs px-2 sm:px-2.5 py-1 rounded-sm border transition-all shadow-2xs flex items-center gap-1.5 cursor-pointer shrink-0 bg-card/95 hover:bg-muted border-border/80 text-foreground backdrop-blur-xs select-none font-mono outline-none focus-visible:ring-1 focus-visible:ring-ring"
          @contextmenu.prevent="menuOpen = true"
        >
          <component :is="icon" class="w-3.5 h-3.5 shrink-0" :class="iconClass" />
          <span data-testid="reply-window-time" class="font-medium tabular-nums" :class="timeClass">{{ shownText }}</span>
        </button>
      </DropdownMenuTrigger>

      <DropdownMenuContent
        align="start"
        data-testid="reply-window-menu"
        class="w-60 p-1 font-mono text-xs shadow-modal border border-border bg-popover"
      >
        <DropdownMenuLabel class="px-2 pt-1 pb-1 text-[11px]" title="智能体每发一条新消息，给你的回复窗口增加的时长（累加，最多 30 分钟）">
          回复窗口<span class="text-muted-foreground/70"> · 每条新消息加多久</span>
        </DropdownMenuLabel>
        <div class="flex gap-1 px-1 pb-1.5" role="group" aria-label="回复窗口增量">
          <DropdownMenuItem
            v-for="v in WINDOW_ADD_OPTIONS"
            :key="v"
            :data-testid="`reply-window-add-${v}`"
            :data-active="addMin === v"
            :class="seg(addMin === v)"
            @select.prevent="pickAdd(v)"
          >{{ v }}m</DropdownMenuItem>
        </div>

        <DropdownMenuLabel class="px-2 pt-1 pb-1 text-[11px]" title="智能体手头没事时用计时器连续等待的最长时间，到点后回头读收件箱；窗口没走完会再等一次">
          单次等待<span class="text-muted-foreground/70"> · 计时器最长等多久</span>
        </DropdownMenuLabel>
        <div class="flex gap-1 px-1 pb-1" role="group" aria-label="单次等待上限">
          <DropdownMenuItem
            v-for="v in WAIT_SLICE_OPTIONS"
            :key="v"
            :data-testid="`reply-window-slice-${v}`"
            :data-active="sliceMin === v"
            :class="seg(sliceMin === v)"
            @select.prevent="pickSlice(v)"
          >{{ v }}m</DropdownMenuItem>
        </div>
      </DropdownMenuContent>
    </DropdownMenu>
    <button
      type="button"
      data-testid="reply-window-plus"
      title="我还在：回复窗口再多 1 分钟（可连点）。输入时倒计时会自动暂停。"
      class="text-[11px] sm:text-xs px-1.5 sm:px-2 py-1 rounded-sm border shadow-2xs flex items-center gap-0.5 cursor-pointer shrink-0 bg-card/95 hover:bg-muted border-border/80 text-foreground tabular-nums"
      @click="onPlus"
    >
      <Plus class="w-3 h-3" />1m
    </button>
  </div>
</template>
