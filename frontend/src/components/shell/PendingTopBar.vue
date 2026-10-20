<script setup lang="ts">
// 手机上「别处有会话在等你答复」的顶条（#156 批 1）：把侧栏里「待处理」那一类提到每个页面的最上面——
// 不用开抽屉、不用在列表里找，点一下直达等得最久的那一个。
// 只在手机宽度出现：PC 的侧栏常驻，待确认的会话本来就在眼前（规格 04 §2.3 的「待确认专属小顶栏」仍在侧栏里）。
// 正在看的那个会话不算「别处」，所以看着它的时候不会出现；没有别的待答复时整条消失，不占一点位置。
import { BellRing, ChevronRight } from 'lucide-vue-next'
import type { FeedbackSession } from '../../api/types'
import { usePendingSessions } from '../../composables/usePendingSessions'

const pending = usePendingSessions()
const emit = defineEmits<{ (e: 'open', session: FeedbackSession): void }>()
</script>

<template>
  <button
    v-if="pending.next"
    type="button"
    class="flex min-h-9 w-full shrink-0 cursor-pointer items-center gap-2 border-b border-amber-500/30 bg-amber-500/10 px-3 py-1.5 text-left text-xs text-amber-800 outline-none focus-visible:ring-1 focus-visible:ring-ring md:hidden dark:text-amber-200"
    :title="`有 ${pending.elsewhere} 个会话在等你答复，点一下去答复等得最久的那个`"
    :data-count="pending.elsewhere"
    data-testid="pending-top-bar"
    @click="emit('open', pending.next)"
  >
    <BellRing class="size-3.5 shrink-0 text-amber-600 dark:text-amber-400" aria-hidden="true" />
    <span class="shrink-0 font-semibold">待答复 {{ pending.elsewhere }}</span>
    <span class="min-w-0 flex-1 truncate text-amber-800/80 dark:text-amber-200/80">{{ pending.next.title || pending.next.workflow_id || pending.next.session_id }}</span>
    <span class="inline-flex shrink-0 items-center font-medium">去答复<ChevronRight class="size-3.5" aria-hidden="true" /></span>
  </button>
</template>
