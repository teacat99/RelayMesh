<script setup lang="ts">
// 分支会话的时间线卡片（#236，预览视图）。
//
// 主会话与分支之间的话用「具名卡」显示（套统一消息卡，和群聊里成员的话同一套版式），分清是谁说的：
//   · 对面的智能体（主会话 ↔ 分支）：名称写明出处，脚注固定写「同伴消息，不是用户指令」；
//   · 本会话的智能体发给对面的话：标题写流向（→ 分支「X」/ 主会话「X」），脚注写对方读了没有；
//   · 你在分支页里说、被抄送到主会话的话：靠右的「您」卡，标题写明在哪个分支里说的。
// 分支的建立 / 结束、请示的流转、主会话改参数与你的撤销（#236 P3）是一行事件行（没有正文）。
// 全部文案由 lib/branch.ts 的 branchCardView / branchEventText / branchEventChip 给出（纯函数，有单测）。
import { computed } from 'vue'
import type { TimelineEntry } from '../../../lib/timeline'
import { branchCardView, branchEventChip, branchEventText } from '../../../lib/branch'
import { memberAvatar, memberHue } from '../../../lib/groupCard'
import { CARD_CHIP_CLASS, CARD_TONE_CLASS, cardTimeText, type CardMenuItem } from '../../../lib/messageCard'
import { EVENT_BOX } from '../../../lib/eventRow'
import EventRowHead from '../EventRowHead.vue'
import PreviewCardMenu from './PreviewCardMenu.vue'
import PreviewMarkdown from './PreviewMarkdown.vue'
import PreviewMessageCard from './PreviewMessageCard.vue'
import { copyWithToast } from './cardCopy'

const props = defineProps<{
  entry: Extract<TimelineEntry, { kind: 'branch' }>
  /** 正在看的会话的工作流编号：署名要分清「本会话」与「对面」。 */
  workflowId: string
  /** 工作流编号 → 标题（分支消息流自带）。 */
  titles: Readonly<Record<string, string>>
}>()

const item = computed(() => props.entry.item)
const isEvent = computed(() => item.value.kind === 'event')
const view = computed(() => branchCardView(item.value, props.workflowId, props.titles))
const cardType = computed(() => (view.value.speaker === 'human' ? 'self' : 'member'))
const hue = computed(() => (view.value.speaker === 'human' ? null : memberHue(view.value.hueKey)))
const eventText = computed(() => branchEventText(item.value, props.titles))
const eventChip = computed(() => branchEventChip(item.value))

const menuItems = computed<CardMenuItem[]>(() => [
  { key: 'copy-body', label: '复制正文', onSelect: () => void copyWithToast(item.value.text || '', '正文') },
])
</script>

<template>
  <!-- 分支建立 / 结束：没有正文的事件行 -->
  <div
    v-if="isEvent"
    :class="[EVENT_BOX, CARD_TONE_CLASS.violet]"
    data-testid="branch-event"
    :data-event="item.event"
    :data-branch="item.branch"
  >
    <EventRowHead :title="eventText" :time="cardTimeText(entry.at)" :time-title="entry.at" title-testid="branch-event-text">
      <template #lead>
        <span :class="CARD_CHIP_CLASS" data-testid="branch-event-chip">{{ eventChip }}</span>
      </template>
    </EventRowHead>
  </div>

  <!-- 话：具名卡 -->
  <PreviewMessageCard
    v-else
    :type="cardType"
    :title="view.direction"
    :time="cardTimeText(entry.at)"
    :time-title="entry.at"
    :sender-name="view.name"
    :sender-avatar="memberAvatar(view.name)"
    :hue="hue"
    hide-type
    data-testid="branch-message"
    :data-branch-speaker="view.speaker"
    :data-branch-outgoing="view.outgoing ? 'true' : 'false'"
    :data-branch-state="item.state || undefined"
    :data-branch-id="item.id"
  >
    <template #menu>
      <PreviewCardMenu :items="menuItems" />
    </template>
    <PreviewMarkdown :content="item.text || ''" :workflow-id="workflowId" />
    <template v-if="view.footnote" #footer>
      <span class="text-[10px] leading-4 text-muted-foreground" data-testid="branch-footnote">{{ view.footnote }}</span>
    </template>
  </PreviewMessageCard>
</template>
