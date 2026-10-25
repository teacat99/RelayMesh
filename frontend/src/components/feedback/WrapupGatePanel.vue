<script setup lang="ts">
// 严格收尾门（#232 D2）：回复窗口弹层里的「收尾门」一节。
//   [锁] 收尾门 · 14 个未完成                      [开关]
//   #232 进行中 会话级常驻提示词…               [暂缓]
//   …另有 8 个，去任务页看
//   [本次放行]
// 开着时：本会话名下还有未完成（待办 / 进行中 / 验证中）任务，回复窗口就不走、智能体也不能收尾——「做完所有非搁置任务
// 再结束会话」；放下任务的正规出口是「暂缓」（等你拍板的，智能体要先发确认卡）。这里让你看清是哪些任务在挡着，
// 并能：本次放行（窗口照常倒计时、任务不动、你下次回复自动复位）/ 关掉收尾门（这个会话回到老口径）/ 替它把某个任务置暂缓。
// 老服务端没有这套字段时整节不出现。
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import { Lock, LockOpen } from 'lucide-vue-next'
import { toast } from 'vue-sonner'
import { useSessionStore } from '../../stores/session'
import { useAsyncModeStore } from '../../stores/asyncMode'
import { asyncModeErrorMessage } from '../../api/asyncMode'
import type { GateTask } from '../../api/types'
import {
  gateBreakdown,
  gateHeavy,
  gateKnown,
  gateOn,
  gateReleaseState,
  gateShown,
  gateStateLabel,
  gateSwitchTitle,
  gateTaskTarget,
  gateTotal,
} from '../../lib/wrapupGate'
import { Switch } from '../ui/switch'

const props = defineProps<{ workflowId: string }>()
const emit = defineEmits<{ (e: 'navigate'): void }>()

const router = useRouter()
const session = useSessionStore()
const store = useAsyncModeStore()

const mode = computed(() => session.asyncMode)
const known = computed(() => gateKnown(mode.value))
const on = computed(() => gateOn(mode.value))
const total = computed(() => gateTotal(mode.value))
const released = computed(() => !!mode.value?.gate_released)
const list = computed(() => gateShown(mode.value?.gate_tasks, total.value))
const breakdown = computed(() => gateBreakdown(mode.value?.gate_tasks, total.value))
const release = computed(() => gateReleaseState(mode.value))
const heavy = computed(() => gateHeavy(total.value))

const headline = computed(() => {
  if (!on.value) return '已关闭'
  if (total.value === 0) return '没有未完成任务'
  return released.value ? `${total.value} 个未完成 · 已放行` : `${total.value} 个未完成`
})

async function toggle(v: boolean) {
  try {
    await store.setStrictWrapup(props.workflowId, v)
  } catch (e) {
    toast.error('改收尾门开关失败', { description: asyncModeErrorMessage(e) })
  }
}

async function onRelease() {
  try {
    await store.releaseGate(props.workflowId)
    toast.success('已本次放行', { description: '回复窗口照常倒计时，任务没有动；你下次回复后自动复位。' })
  } catch (e) {
    toast.error('放行失败', { description: asyncModeErrorMessage(e) })
  }
}

async function shelve(t: GateTask) {
  try {
    await store.shelveGateTask(props.workflowId, t.id)
  } catch (e) {
    toast.error(`#${t.seq} 置暂缓失败`, { description: asyncModeErrorMessage(e) })
  }
}

/** 点一行任务：去任务页并打开它；不带任务（「另有 N 个」）就只进任务页。地址认的是数据库编号（t.id），不是 #N（t.seq，#242）。 */
function openTasks(t?: GateTask) {
  emit('navigate')
  void router.push(gateTaskTarget(props.workflowId, t))
}

const STATE_TONE: Record<string, string> = {
  todo: 'text-muted-foreground',
  doing: 'text-sky-600 dark:text-sky-400',
  review: 'text-violet-600 dark:text-violet-400',
}
</script>

<template>
  <div v-if="known" data-testid="wrapup-gate" :data-on="on" :data-total="total" :data-released="released" class="px-1 pb-1">
    <div class="flex items-center justify-between gap-2 px-1 pt-1 pb-1">
      <span class="flex min-w-0 items-center gap-1.5 text-[11px]" :title="gateSwitchTitle(on)">
        <component :is="on ? Lock : LockOpen" class="size-3 shrink-0" :class="on ? 'text-primary' : 'text-muted-foreground'" />
        <span class="shrink-0">收尾门</span>
        <span data-testid="wrapup-gate-headline" class="truncate text-muted-foreground/70">· {{ headline }}</span>
      </span>
      <Switch
        data-testid="wrapup-gate-switch"
        :checked="on"
        :disabled="store.busy"
        :title="gateSwitchTitle(on)"
        aria-label="严格收尾门"
        @update:checked="toggle"
      />
    </div>

    <template v-if="on && total > 0">
      <p
        v-if="heavy"
        data-testid="wrapup-gate-heavy"
        class="mx-1 mb-1 rounded-xs border border-amber-500/40 bg-amber-500/10 px-2 py-1 text-[11px] leading-snug text-amber-700 dark:text-amber-300"
      >
        积压 {{ total }} 个：智能体要把它们都做完，或把做不下去的置「暂缓」（等你拍板的先发确认卡），回复窗口才会走。不想等就点「本次放行」，或关掉收尾门。
      </p>
      <p v-if="breakdown" data-testid="wrapup-gate-breakdown" class="px-2 pb-0.5 text-[10px] text-muted-foreground">{{ breakdown }}</p>
      <ul data-testid="wrapup-gate-list" class="max-h-44 space-y-0.5 overflow-y-auto px-1">
        <li
          v-for="t in list.shown"
          :key="t.id"
          :data-testid="`wrapup-gate-task-${t.seq}`"
          class="flex items-center gap-1.5 rounded-xs px-1 py-0.5 hover:bg-muted/60"
        >
          <button
            type="button"
            class="flex min-w-0 flex-1 cursor-pointer items-center gap-1.5 text-left"
            :title="`打开任务 #${t.seq}：${t.title}`"
            @click="openTasks(t)"
          >
            <span class="shrink-0 tabular-nums text-muted-foreground">#{{ t.seq }}</span>
            <span class="shrink-0 text-[10px]" :class="STATE_TONE[t.state] ?? 'text-muted-foreground'">{{ gateStateLabel(t.state) }}</span>
            <span class="truncate">{{ t.title }}</span>
          </button>
          <button
            type="button"
            data-testid="wrapup-gate-shelve"
            :disabled="store.busy"
            :title="`你替智能体把 #${t.seq} 置「暂缓」：它不再挡收尾；进展里会写明是你放下的、等你另行安排`"
            class="shrink-0 cursor-pointer rounded-xs border border-border/70 px-1.5 py-0.5 text-[10px] hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50"
            @click="shelve(t)"
          >暂缓</button>
        </li>
      </ul>
      <button
        v-if="list.more > 0"
        type="button"
        data-testid="wrapup-gate-more"
        class="mx-1 mt-0.5 cursor-pointer px-1 text-left text-[10px] text-muted-foreground underline-offset-2 hover:underline"
        @click="openTasks()"
      >…另有 {{ list.more }} 个，去任务页查看</button>
    </template>

    <div v-if="on" class="flex items-center gap-1 px-1 pt-1">
      <button
        type="button"
        data-testid="wrapup-gate-release"
        :disabled="!release.can || store.busy"
        :title="release.why"
        class="flex-1 cursor-pointer rounded-xs border border-border/80 bg-muted/50 px-2 py-1 text-xs hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50"
        @click="onRelease"
      >本次放行</button>
    </div>
  </div>
</template>
