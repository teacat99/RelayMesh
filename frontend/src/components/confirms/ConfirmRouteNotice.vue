<script setup lang="ts">
// 请示的「决策路由」提示条（#236 P2）：分支会话里的智能体发的请示，默认先送主会话的智能体定夺——
//   · 还在等主会话：这件事暂时不用您管，主会话答复 / 转交 / 一直没人答复时才轮到您；您也可以现在直接答复（先到先得）；
//   · 被转交 / 超时升级 / 分支结束后改由您定夺：写明为什么轮到您，让您看懂上下文。
// 经典的 ConfirmCard 与预览的 PreviewConfirmCard 共用这一条，判断与文案都在 lib/confirms.ts（纯函数，有单测）。
import { computed } from 'vue'
import { CornerUpRight, Users } from 'lucide-vue-next'
import type { ConfirmItem } from '../../api/confirms'
import { escalationText, routedToMain } from '../../lib/confirms'

const props = defineProps<{ item: ConfirmItem; testid: string }>()

const routed = computed(() => routedToMain(props.item))
const escalated = computed(() => (routed.value ? '' : escalationText(props.item)))
</script>

<template>
  <p
    v-if="routed"
    class="flex items-start gap-1.5 rounded-xs border border-violet-500/30 bg-violet-500/5 px-2 py-1.5 text-[11px] leading-relaxed text-foreground/80"
    :data-testid="`${testid}-routed`"
  >
    <Users class="mt-0.5 size-3 shrink-0 text-violet-600 dark:text-violet-300" />
    <span>这是分支会话里的智能体发的请示，先交给主会话的智能体定夺；主会话答复、转交，或一直没人答复时会自动转给您。您也可以现在直接答复，先到先得。</span>
  </p>
  <p
    v-else-if="escalated"
    class="flex items-start gap-1.5 rounded-xs border border-amber-500/30 bg-amber-500/5 px-2 py-1.5 text-[11px] leading-relaxed text-foreground/80"
    :data-testid="`${testid}-escalated`"
  >
    <CornerUpRight class="mt-0.5 size-3 shrink-0 text-amber-700 dark:text-amber-300" />
    <span class="min-w-0 whitespace-pre-wrap break-words">{{ escalated }}</span>
  </p>
</template>
