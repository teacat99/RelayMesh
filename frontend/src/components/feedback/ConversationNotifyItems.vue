<script setup lang="ts">
import { Check } from 'lucide-vue-next'
import { DropdownMenuItem, DropdownMenuLabel, DropdownMenuSeparator } from '../ui/dropdown-menu'
import { MUTE_PRESETS } from '../../lib/notifyPrefs'
import { useConversationNotify } from '../../composables/useConversationNotify'

// 这个会话（工作流）的提醒策略条目：全部提醒，或静音一段时间 / 一直静音。必须放在某个 DropdownMenuContent 里面。
// 静音只拦下智能体发来的文字与待确认卡片；阻塞待答（智能体在等你回复）、@ 你、终端审批、系统告警不受静音影响。
// PC 页头的「提醒」菜单与手机页头「⋯」菜单里的提醒部分共用这一份（#156 批 1）。
const props = defineProps<{ workflowId: string }>()
const { prefs, conversation, muted, note, mutedForever } = useConversationNotify(() => props.workflowId)
</script>

<template>
  <template v-if="conversation">
    <DropdownMenuLabel class="text-[11px] font-normal text-muted-foreground leading-snug">
      这个会话的提醒
      <template v-if="muted && note"><br />{{ note }}</template>
    </DropdownMenuLabel>
    <DropdownMenuItem class="text-xs" data-testid="notify-policy-all" @select="prefs.resetConversation(conversation)">
      <Check v-if="!muted" class="!size-3.5" /><span v-else class="!size-3.5" aria-hidden="true" />全部提醒
    </DropdownMenuItem>
    <DropdownMenuItem
      v-for="p in MUTE_PRESETS"
      :key="p.id"
      class="text-xs"
      :data-testid="`notify-policy-mute-${p.id}`"
      @select="prefs.setConversationPolicy(conversation, 'muted', p.durationMs)"
    >
      <Check v-if="p.durationMs === null && mutedForever" class="!size-3.5" /><span v-else class="!size-3.5" aria-hidden="true" />{{ p.label }}
    </DropdownMenuItem>
    <DropdownMenuSeparator />
    <DropdownMenuLabel class="text-[10px] font-normal text-muted-foreground leading-snug">
      静音只拦下智能体发来的文字与待确认卡片；智能体在等你回复、@ 你、终端审批与系统告警照常提醒。
    </DropdownMenuLabel>
  </template>
</template>
