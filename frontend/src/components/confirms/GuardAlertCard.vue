<script setup lang="ts">
// 守护告警在对话里的卡片（#105）：终端会话意外结束 / 智能体停摆，等您决定要不要唤醒。
// 以前挤在输入区上方的长条里；现在是时间线上的一张卡，告警存在期间一直摊开（不叠进摞里）。
//
// compact（#120 预览视图，默认关）：换成 32px 的事件行（阶段 C2：[告警] 名称 …… 原因 · 主机 · 状态 · 去终端页处理），
// 完整原因在悬停提示里。经典列表不传它，经典的标记一个字没动。
import { useRouter } from 'vue-router'
import { ShieldAlert } from 'lucide-vue-next'
import type { ConfirmBoardGuard } from '../../api/confirms'
import { EVENT_BOX } from '../../lib/eventRow'
import { CARD_TONE_CLASS, CARD_TYPES } from '../../lib/messageCard'
import EventRowHead from '../feedback/EventRowHead.vue'
import EventStatus from '../feedback/EventStatus.vue'

defineProps<{ alert: ConfirmBoardGuard; compact?: boolean }>()
const router = useRouter()

const GUARD_STATE_LABEL: Record<string, string> = { crashed: '会话意外结束', stalled: '智能体停摆' }
const TONE = CARD_TONE_CLASS[CARD_TYPES.guard.tone]

function openTerminal() {
  void router.push({ name: 'exec' })
}
</script>

<template>
  <!-- 事件行（预览视图，compact）：告警没有「已处理」——存在期间一直是琥珀色，状态标签写它的原因分类 -->
  <div v-if="compact" :class="[EVENT_BOX, TONE]" data-testid="guard-alert-card" :data-preset="alert.preset_id">
    <EventRowHead type="guard" :title="alert.name" :preview="alert.verdict.reason" preview-testid="guard-alert-reason">
      <template #status>
        <span v-if="alert.host" class="hidden shrink-0 text-[11px] text-muted-foreground sm:inline">{{ alert.host }}</span>
        <EventStatus tone="amber">{{ GUARD_STATE_LABEL[alert.verdict.state] || alert.verdict.state }}</EventStatus>
      </template>
      <template #trail>
        <button
          type="button"
          class="shrink-0 cursor-pointer rounded-xs border border-amber-500/50 px-2 py-0.5 text-[11px] text-amber-700 hover:bg-amber-500/10 dark:text-amber-300"
          data-testid="guard-alert-open"
          @click="openTerminal"
        >
          去终端页处理
        </button>
      </template>
    </EventRowHead>
  </div>

  <div
    v-else
    class="flex items-start gap-2 rounded-sm border border-amber-500/40 bg-amber-500/5 px-3 py-2 text-xs"
    data-testid="guard-alert-card"
    :data-preset="alert.preset_id"
  >
    <ShieldAlert class="mt-0.5 size-3.5 shrink-0 text-amber-600 dark:text-amber-400" />
    <div class="min-w-0 flex-1 space-y-0.5">
      <div class="flex flex-wrap items-center gap-x-2 gap-y-0.5">
        <span class="text-[10px] text-muted-foreground">守护告警</span>
        <span class="font-medium text-foreground">{{ alert.name }}</span>
        <span v-if="alert.host" class="text-[11px] text-muted-foreground">{{ alert.host }}</span>
        <span class="rounded-xs border border-amber-500/40 px-1 text-[10px] text-amber-700 dark:text-amber-300">{{ GUARD_STATE_LABEL[alert.verdict.state] || alert.verdict.state }}</span>
      </div>
      <p v-if="alert.verdict.reason" class="text-[11px] text-muted-foreground">{{ alert.verdict.reason }}</p>
    </div>
    <button
      type="button"
      class="shrink-0 cursor-pointer rounded-xs border border-amber-500/50 px-2 py-0.5 text-[11px] text-amber-700 hover:bg-amber-500/10 dark:text-amber-300"
      data-testid="guard-alert-open"
      @click="openTerminal"
    >
      去终端页处理
    </button>
  </div>
</template>
