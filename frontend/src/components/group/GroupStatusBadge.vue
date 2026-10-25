<script setup lang="ts">
// 群聊页页头的群级状态徽标（#241 第三步）：怎么把各位成员的状态汇成一句话见 lib/groupStatus.ts，这里只管摆放。
// 是一个按钮：点一下开 / 关成员面板（面板里逐位列着每位成员的状态），与旁边的头像堆是同一个入口；
// 悬停（读屏同样）说明各状态各有哪几位。群里没有 Agent 成员时不画。
// 窄屏（<sm）只留短写法（「等你 2」），群名不被挤没；宽屏写全（「2 位在等你」）。
import { computed } from 'vue'
import { deriveGroupStatus, type GroupStatusTone, type LiveGroupMember } from '../../lib/groupStatus'

/** members 应当是已经盖上实时状态的成员（见 GroupChatPane 的 liveMembers；硬卡住 stalled 只在这份里有）。 */
const props = defineProps<{ members: readonly LiveGroupMember[]; expanded?: boolean }>()
const emit = defineEmits<{ (e: 'toggle'): void }>()

const status = computed(() => deriveGroupStatus(props.members))

// 色调与成员面板里的状态点是同一套（工作中绿、等你 / 等授权琥珀、疑似卡住与出错红、其余灰）；完整类名写成字面量，Tailwind 才扫得到。
const BOX: Record<GroupStatusTone, string> = {
  attention: 'border-amber-500/30 bg-amber-500/10 text-amber-700 hover:bg-amber-500/20 dark:text-amber-300',
  active: 'border-emerald-500/25 bg-emerald-500/10 text-emerald-700 hover:bg-emerald-500/20 dark:text-emerald-300',
  danger: 'border-red-500/30 bg-red-500/10 text-red-700 hover:bg-red-500/20 dark:text-red-300',
  muted: 'border-border text-muted-foreground hover:bg-accent hover:text-accent-foreground',
}
const DOT: Record<GroupStatusTone, string> = {
  attention: 'bg-amber-500',
  active: 'bg-emerald-500',
  danger: 'bg-red-500',
  muted: 'bg-muted-foreground/40',
}
</script>

<template>
  <button
    v-if="status"
    type="button"
    class="inline-flex h-5 shrink-0 cursor-pointer select-none items-center gap-1 rounded-xs border px-1.5 text-[11px] font-normal leading-none outline-none focus-visible:ring-1 focus-visible:ring-ring"
    :class="BOX[status.tone]"
    :title="status.title"
    :aria-label="`群成员状态：${status.title.replace(/\n/g, '；')}`"
    :aria-expanded="expanded ? 'true' : 'false'"
    data-testid="group-status-badge"
    :data-status-key="status.key"
    :data-count="status.count"
    @click="emit('toggle')"
  >
    <i
      class="size-1.5 shrink-0 rounded-full"
      :class="[DOT[status.tone], status.pulse ? 'animate-pulse motion-reduce:animate-none' : '']"
      aria-hidden="true"
    />
    <span class="sm:hidden">{{ status.shortLabel }}</span>
    <span class="hidden sm:inline">{{ status.label }}</span>
  </button>
</template>
