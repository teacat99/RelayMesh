<script setup lang="ts">
import { Bell, BellOff } from 'lucide-vue-next'
import { DropdownMenu, DropdownMenuContent, DropdownMenuTrigger } from '../ui/dropdown-menu'
import { useConversationNotify } from '../../composables/useConversationNotify'
import ConversationNotifyItems from './ConversationNotifyItems.vue'

// 这个会话（工作流）的提醒策略：全部提醒，或静音一段时间 / 一直静音。
// 静音只拦下智能体发来的文字与待确认卡片；阻塞待答（智能体在等你回复）、@ 你、终端审批、守护告警不受静音影响。
// 偏好存在服务端，浏览器与手机推送一起生效。
// 条目本身在 ConversationNotifyItems（手机页头的「⋯」菜单里复用同一份）；这里只管 PC 页头的触发按钮与菜单外壳。
const props = defineProps<{ workflowId: string }>()

const { conversation, muted, title } = useConversationNotify(() => props.workflowId)
</script>

<template>
  <DropdownMenu v-if="conversation">
    <DropdownMenuTrigger as-child>
      <button
        type="button"
        class="inline-flex items-center gap-1 px-2.5 py-1 max-md:min-w-10 max-md:justify-center max-md:py-1.5 rounded-sm border text-[10px] sm:text-xs font-mono shrink-0 shadow-xs transition-colors cursor-pointer outline-none focus-visible:ring-1 focus-visible:ring-ring bg-muted/40 hover:bg-muted/80 hover:text-primary border-border/80"
        :class="muted ? 'text-amber-600 dark:text-amber-400' : ''"
        :title="title"
        :aria-label="title"
        :data-muted="muted ? 'true' : 'false'"
        data-testid="session-notify-menu"
      >
        <component :is="muted ? BellOff : Bell" class="w-3 sm:w-3.5 h-3 sm:h-3.5" :class="muted ? 'text-amber-500' : 'text-primary'" />
        <span class="hidden sm:inline font-medium">{{ muted ? '已静音' : '提醒' }}</span>
      </button>
    </DropdownMenuTrigger>
    <DropdownMenuContent align="end" class="text-xs min-w-[15rem] max-w-[min(20rem,calc(100vw-1rem))]" data-testid="session-notify-menu-content">
      <ConversationNotifyItems :workflow-id="props.workflowId" />
    </DropdownMenuContent>
  </DropdownMenu>
</template>
