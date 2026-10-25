<script setup lang="ts">
// 进度通知的统一消息卡（#120 · D-273 阶段 B，只给预览视图用）。
//
// 智能体用 wait:false 发的一条话本质就是正文，所以和汇报、提问一样套同一张卡：标题是这条通知的标题，
// 正文是 Markdown 全文（不折叠、不限高——阶段 1 的「折成一行点开」被取消）。
// 通知没有「已读 / 未读」数据，所以头部不画状态标签；脚注放「引用」按钮（点一下在输入框插入「【回复 哈希】」）。
// 根节点保留 data-testid="notice-bubble" 与 data-session-id，e2e 与对等测试按它找通知。
import { computed } from 'vue'
import type { TimelineEntry } from '../../../lib/timeline'
import { cardTimeText, type CardMenuItem } from '../../../lib/messageCard'
import { replyHash, replyRefToken } from '../../../lib/replyRef'
import PreviewBodyGate from './PreviewBodyGate.vue'
import PreviewMarkdown from './PreviewMarkdown.vue'
import ReplyRefButton from '../ReplyRefButton.vue'
import { copyWithToast } from './cardCopy'
import PreviewCardMenu from './PreviewCardMenu.vue'
import PreviewMessageCard from './PreviewMessageCard.vue'

const props = defineProps<{ entry: Extract<TimelineEntry, { kind: 'notice' }> }>()

const session = computed(() => props.entry.session)

// 轻量行（正文还没取回，见 PreviewBodyGate）手里只有首行预览，不能复制「正文」：取回之前先不给这一项
const menuItems = computed<CardMenuItem[]>(() => [
  ...(session.value.body_omitted ? [] : [{ key: 'copy-body', label: '复制正文', onSelect: () => void copyWithToast(session.value.summary, '正文') }]),
  { key: 'copy-id', label: '复制会话号', onSelect: () => void copyWithToast(session.value.session_id, '会话号') }
])
</script>

<template>
  <PreviewMessageCard
    type="notice"
    :title="session.title"
    :time="cardTimeText(entry.at)"
    :time-title="entry.at"
    data-testid="notice-bubble"
    :data-session-id="session.session_id"
  >
    <template #menu>
      <PreviewCardMenu :items="menuItems" />
    </template>
    <PreviewBodyGate :session="session">
      <PreviewMarkdown :content="session.summary" :workflow-id="session.workflow_id" />
    </PreviewBodyGate>
    <template #footer>
      <!-- 短哈希 + 引用：点一下在输入框插入「【回复 哈希】」（#106） -->
      <ReplyRefButton :token="replyRefToken(session.session_id)" :label="replyHash(session.session_id)" />
    </template>
  </PreviewMessageCard>
</template>
