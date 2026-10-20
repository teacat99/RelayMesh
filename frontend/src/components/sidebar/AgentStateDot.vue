<script setup lang="ts">
// 工作流列表里的「Agent 状态小圆点」（#113，设计稿 04 §3）：一眼看出哪个工作流的 Agent 正在干活 / 在等你 / 出了问题。
// 没有收到过这个工作流的 hook 汇报时不显示（避免给每一行都挂一个灰点）。
import { computed } from 'vue'
import { agentDotShown, dotToneClass, stateView } from '@/lib/agentState'
import { useAgentStateStore } from '@/stores/agentState'

const props = defineProps<{ workflowId: string }>()

const store = useAgentStateStore()
const entry = computed(() => store.entryFor(props.workflowId))
const view = computed(() => stateView(entry.value.primary))
// 画不画与侧栏的「工作中」芯片共用同一条判断（agentDotShown）：画了它，芯片就不再自带绿点
const shown = computed(() => agentDotShown(entry.value.primary))
const label = computed(() => entry.value.identity?.label || entry.value.primary?.identity_label || '')
const title = computed(() => {
  const who = label.value ? `${label.value} · ` : ''
  const extra = entry.value.others > 0 ? `\n另有 ${entry.value.others} 个会话在活动` : ''
  return `${who}${view.value.title}${extra}`
})
</script>

<template>
  <span
    v-if="shown"
    data-testid="sidebar-agent-dot"
    :data-key="view.key"
    :data-tone="view.tone"
    role="img"
    :aria-label="`Agent ${view.text}`"
    :title="title"
    class="inline-block w-1.5 h-1.5 rounded-full shrink-0"
    :class="dotToneClass(view.tone, view.pulse)"
  ></span>
</template>
