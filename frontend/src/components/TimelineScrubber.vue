<script setup lang="ts">
import { ref, watch, nextTick } from 'vue'
import dayjs from 'dayjs'
import type { FeedbackSession } from '../api/types'

function formatDateTimeCustom(dateStr?: string): string {
  if (!dateStr) return ''
  const target = dayjs(dateStr)
  if (!target.isValid()) return ''
  const now = dayjs()

  const isSameYear = target.isSame(now, 'year')
  const isSameDay = isSameYear && target.isSame(now, 'day')

  if (isSameDay) {
    return target.format('HH:mm')
  } else if (isSameYear) {
    return target.format('MM-DD·HH:mm')
  } else {
    return target.format('YYYY·MM-DD·HH:mm')
  }
}

const props = defineProps<{
  sessions: FeedbackSession[]
  activeSessionId?: string | null
}>()

const emit = defineEmits<{
  (e: 'jump', sessionId: string): void
}>()

const hoveredSession = ref<FeedbackSession | null>(null)
const tooltipTop = ref(0)
const scrubberContainer = ref<HTMLElement | null>(null)
const railContainer = ref<HTMLElement | null>(null)

watch(() => props.activeSessionId, (newId) => {
  if (!newId) return
  nextTick(() => {
    const tickEl = document.getElementById(`scrubber-tick-${newId}`)
    if (tickEl) {
      tickEl.scrollIntoView({ block: 'nearest', behavior: 'smooth' })
    }
  })
}, { immediate: true })

function handleMouseEnter(session: FeedbackSession, event: MouseEvent) {
  hoveredSession.value = session
  if (scrubberContainer.value) {
    const rect = scrubberContainer.value.getBoundingClientRect()
    tooltipTop.value = Math.max(20, Math.min(rect.height - 80, event.clientY - rect.top))
  }
}

function handleMouseLeave() {
  hoveredSession.value = null
}

function handleClick(sessionId: string) {
  emit('jump', sessionId)
}

function getSessionStatusLabel(status: string) {
  switch (status) {
    case 'completed': return '已完成'
    case 'pending': return '等待确认'
    case 'cancelled': return '已取消'
    case 'timeout': return '已超时'
    default: return status
  }
}
</script>

<template>
  <div
    ref="scrubberContainer"
    class="relative select-none flex flex-col items-end py-2 px-1 justify-center group"
    @mouseleave="handleMouseLeave"
  >
    <!-- Timeline Vertical Rail of Ticks -->
    <div
      ref="railContainer"
      class="flex flex-col items-end gap-1 py-1 w-6 max-h-[50vh] overflow-y-auto no-scrollbar scrollbar-none"
    >
      <template v-for="(session, idx) in sessions" :key="session.session_id">
        <!-- Intermediate subtle tick dashes (仅在总轮次 <= 6 时展示，避免多轮对话高度过高溢出) -->
        <template v-if="sessions.length <= 6">
          <div class="w-1.5 h-[1px] bg-border/60 my-0.5"></div>
          <div class="w-1.5 h-[1px] bg-border/60 my-0.5"></div>
        </template>

        <!-- Major session tick mark -->
        <div
          :id="`scrubber-tick-${session.session_id}`"
          class="relative flex items-center justify-end cursor-pointer py-0.5 group/tick w-full"
          @mouseenter="(e) => handleMouseEnter(session, e)"
          @click="handleClick(session.session_id)"
        >
          <!-- The Tick Line -->
          <div
            class="transition-all duration-200 rounded-xs"
            :class="[
              activeSessionId === session.session_id
                ? 'w-6 h-[2.5px] bg-foreground shadow-2xs'
                : session.status === 'pending'
                  ? 'w-5 h-[2px] bg-primary animate-pulse'
                  : 'w-3 h-[1.5px] bg-border/80 group-hover/tick:w-5 group-hover/tick:bg-foreground/80'
            ]"
          ></div>
        </div>
      </template>

      <!-- Trailing bottom tick lines (仅在轮次较少时展示) -->
      <template v-if="sessions.length <= 6">
        <div class="w-1.5 h-[1px] bg-border/60 my-0.5"></div>
        <div class="w-1.5 h-[1px] bg-border/60 my-0.5"></div>
      </template>
    </div>

    <!-- Floating Preview Tooltip Card on Hover -->
    <Transition
      enter-active-class="transition duration-150 ease-out"
      enter-from-class="opacity-0 translate-x-2"
      enter-to-class="opacity-100 translate-x-0"
      leave-active-class="transition duration-100 ease-in"
      leave-from-class="opacity-100 translate-x-0"
      leave-to-class="opacity-0 translate-x-2"
    >
      <div
        v-if="hoveredSession"
        class="absolute right-8 z-50 w-64 p-3 rounded-md border border-border bg-card shadow-float pointer-events-none text-left space-y-1.5 -translate-y-1/2"
        :style="{ top: `${tooltipTop}px` }"
      >
        <div class="flex items-center justify-between gap-1.5">
          <span class="text-xs font-semibold text-foreground truncate">
            {{ hoveredSession.title || '反馈会话' }}
          </span>
          <span
            class="text-[10px] px-1.5 py-0.2 rounded-xs border font-mono shrink-0"
            :class="hoveredSession.status === 'pending'
              ? 'border-primary/30 text-primary bg-primary/10 font-bold'
              : 'border-border text-muted-foreground bg-muted/40'"
          >
            {{ getSessionStatusLabel(hoveredSession.status) }}
          </span>
        </div>

        <p class="text-[11px] text-muted-foreground line-clamp-3 leading-relaxed">
          {{ hoveredSession.summary ? hoveredSession.summary.replace(/[#*`]/g, '') : '暂无详细摘要' }}
        </p>

        <div class="flex items-center justify-between text-[10px] text-muted-foreground font-mono pt-1 border-t border-border/50">
          <span class="truncate max-w-[120px]">{{ hoveredSession.workflow_id || hoveredSession.session_id }}</span>
          <span>{{ formatDateTimeCustom(hoveredSession.created_at) }}</span>
        </div>
      </div>
    </Transition>
  </div>
</template>
