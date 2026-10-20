<script setup lang="ts">
// 会话页底部状态块旁的「启用 Agent」按钮（#176 第三期）：网页上新建的会话在智能体接入之前是「待接入」，
// 以前只能把启动提示词复制给某个智能体；现在可以点这个按钮，直接在某台主机的某个目录里无人值守地新开一个 Cursor CLI 对话，
// 它带着这个工作流编号接入，之后这个会话就和别的智能体会话一样了。
//
// 只在同时满足时出现：终端子系统开着、Hub 认识这个功能（旧版没有）、这个会话现在是「待接入」（与页头状态徽标同一口径）。
// 一旦有智能体接入，按钮自己消失，换成「运行」按钮接着它。点开是弹窗（EnableAgentDialog），选项、预览与确认都在弹窗里。
import { computed, ref } from 'vue'
import { Rocket } from 'lucide-vue-next'
import EnableAgentDialog from '@/components/terminal/EnableAgentDialog.vue'
import { isAwaitingAgent } from '@/lib/newSession'
import { enableSupported } from '@/lib/terminal/sessionEnable'
import { useSessionStore } from '@/stores/session'
import { useTerminalStore } from '@/stores/terminal'

const props = defineProps<{ workflowId: string }>()

const sessionStore = useSessionStore()
const terminal = useTerminalStore()

const open = ref(false)

const awaiting = computed(() => {
  const cur = sessionStore.selectedSession
  if (!cur || cur.workflow_id !== props.workflowId) return false
  const pool = sessionStore.currentWorkflowSessions.length > 0 ? sessionStore.currentWorkflowSessions : sessionStore.sessions
  return isAwaitingAgent(cur, pool)
})
const supported = computed(() => enableSupported(terminal.prefs?.limits))
const visible = computed(() => !!props.workflowId && terminal.gateOpen && supported.value && awaiting.value)
</script>

<template>
  <button
    v-if="visible"
    type="button"
    class="inline-flex shrink-0 cursor-pointer items-center gap-1 rounded-sm border border-primary/40 bg-primary/5 px-2 py-1 font-mono text-[11px] text-primary outline-none transition-colors hover:bg-primary/10 focus-visible:ring-1 focus-visible:ring-ring sm:text-xs"
    title="在主机上无人值守地新开一个 Cursor CLI 对话，让它接入这个会话（不用再把启动提示词复制给智能体）"
    aria-label="启用 Agent：在主机上新开一个 Cursor CLI 对话接入这个会话"
    data-testid="session-enable-agent"
    @click="open = true"
  >
    <Rocket class="size-3.5" aria-hidden="true" />
    <span>启用 Agent</span>
  </button>
  <!-- 弹窗开着时即使会话刚好被别的智能体接入、按钮消失，也不把弹窗收掉（服务端会在预览 / 提交时说明「已经接入过了」） -->
  <EnableAgentDialog v-if="visible || open" v-model:open="open" :workflow-id="workflowId" />
</template>
