<script setup lang="ts">
// 群聊里的一条消息（#120 阶段 E-d1；#165 第二步起是群聊窗口唯一的消息画法）：统一消息卡的「具名」版本。
//
// 谁说的话用 头像字 + 名称 + 成员色（lib/groupCard 稳定分配，同一个人在任何地方都是同一个颜色）一眼分清；
// 内容：Markdown / 文档引用 / 附件、已读 N/M、「@你」、各被点名成员的投递状态——
// 文案取 lib/group 的函数，data-testid 沿用 chat-*（e2e 命名一致）。群里的消息编号是 m:…，没有「引用回复」入口
// （引用只属于已经删除的单会话聊天窗口），所以这里没有引用条与「引用」按钮。
// 系统行、确认卡、决策卡、任务进展不走这里（lib/groupCard.groupRenderKind 判定），由 GroupEventRow 画。
// 根节点 = PreviewMessageCard 的 <article>，data-testid="chat-message" / data-message-id（跳转与搜索定位用）落在它上面。
import dayjs from 'dayjs'
import { computed, watch } from 'vue'
import type { ChatMessage } from '../../api/conversations'
import { useImageLoader } from '../../composables/useImageLoader'
import { extractArtifactRefs } from '../../lib/artifactRef'
import { readsLabel, readsTitle } from '../../lib/group'
import { senderIdentity } from '../../lib/groupCard'
import { cardTimeText, type CardType } from '../../lib/messageCard'
import { usePreviewStore } from '../../stores/preview'
import ArtifactRefCard from '../artifact/ArtifactRefCard.vue'
import PreviewMarkdown from '../feedback/preview-view/PreviewMarkdown.vue'
import PreviewMessageCard from '../feedback/preview-view/PreviewMessageCard.vue'

const props = defineProps<{
  message: ChatMessage
  /** 群的会话键（grp:…）：发送者没有自己的工作流编号时，正文里的引用链接按它装饰。 */
  workflowId: string
  /** 成员色相表（assignMemberHues 的结果）：由调用方算一次传下来，每张卡不重算。 */
  hues: Readonly<Record<string, number>>
}>()

const imageLoader = useImageLoader()
const previewStore = usePreviewStore()

const m = computed(() => props.message)
const mine = computed(() => m.value.sender.kind === 'human')
const ident = computed(() => senderIdentity(m.value.sender, props.hues))
const isNotice = computed(() => m.value.kind === 'progress' && !m.value.task)
// 群里的话没有「汇报 / 回复」之分，只借类型决定靠左还是靠右；智能体的进度通知保留「进度」标签。
const type = computed<CardType>(() => (mine.value ? 'self' : isNotice.value ? 'notice' : 'member'))
const hideType = computed(() => !isNotice.value)

const rawBody = computed(() => m.value.text ?? '')
// 您自己的话在这里先抽掉 [[artifact:N]] 记号；智能体的由 PreviewMarkdown 抽取并画引用卡
const mineExtracted = computed(() => (mine.value ? extractArtifactRefs(rawBody.value) : { text: '', ids: [] as number[] }))

const reads = computed(() => readsLabel(m.value.reads))
const readsHint = computed(() => readsTitle(m.value.reads))
const mentionsYou = computed(() => !mine.value && !!m.value.mentions?.some(x => x.ref === 'human'))
const DELIVERY_TEXT: Record<string, string> = { delivered: '已送达', read: '已读', handled: '已处理', retracted: '已撤回', expired: '已过期' }
const deliveries = computed(() => (mine.value ? (m.value.deliveries ?? []).map(d => `${d.name} ${DELIVERY_TEXT[d.status] ?? d.status}`).join(' · ') : ''))
const hasFooter = computed(() => !!(reads.value || mentionsYou.value || deliveries.value))

const time = computed(() => cardTimeText(m.value.sent_at))
const timeTitle = computed(() => {
  const t = dayjs(m.value.sent_at)
  return t.isValid() ? t.format('YYYY-MM-DD HH:mm:ss') : undefined
})

watch(
  () => m.value.attachments,
  list => {
    for (const a of list ?? []) if (a.url && !a.url.startsWith('data:')) imageLoader.enqueue(a.url)
  },
  { immediate: true },
)

function openImage(url: string, name: string | undefined, width?: number, height?: number) {
  imageLoader.prioritize(url)
  previewStore.openImagePreview({ src: url, alt: name || '图片', width, height })
}
</script>

<template>
  <PreviewMessageCard
    :type="type"
    :title="mine ? undefined : m.title"
    :time="time"
    :time-title="timeTitle"
    :sender-name="ident.name"
    :sender-avatar="ident.avatar"
    :hue="ident.hue"
    :hide-type="hideType"
    :class="mentionsYou ? 'ring-1 ring-amber-500/50' : ''"
    data-testid="chat-message"
    :data-message-id="m.id"
    :data-sender="m.sender.kind"
    :data-status="m.status"
    :data-mentions-you="mentionsYou ? 'true' : undefined"
  >
    <template v-if="mine">
      <p v-if="mineExtracted.text" class="whitespace-pre-wrap break-words">{{ mineExtracted.text }}</p>
      <ArtifactRefCard v-for="id in mineExtracted.ids" :key="id" :doc-id="id" />
    </template>
    <PreviewMarkdown v-else-if="rawBody" :content="rawBody" :workflow-id="m.sender.workflow_id || workflowId" />

    <div v-if="m.attachments?.length" class="mt-1.5 flex flex-wrap gap-1.5" data-testid="chat-attachments">
      <button
        v-for="(a, i) in m.attachments"
        :key="`${a.url}-${i}`"
        type="button"
        class="size-20 cursor-zoom-in overflow-hidden rounded-md border border-border/50 bg-muted"
        @click="openImage(a.url, a.name, a.width, a.height)"
      >
        <img :src="imageLoader.getDisplaySrc(a.url)" :alt="a.name || '图片'" class="size-full object-cover" loading="lazy" />
      </button>
    </div>

    <template v-if="hasFooter" #footer>
      <span v-if="reads" class="cursor-default text-[10px] text-muted-foreground" :title="readsHint" data-testid="chat-reads">{{ reads }}</span>
      <span v-if="mentionsYou" class="text-[10px] font-medium text-amber-700 dark:text-amber-400" data-testid="chat-mentions-you">@你</span>
      <span v-if="deliveries" class="basis-full text-[10px] text-muted-foreground/80" data-testid="chat-deliveries">{{ deliveries }}</span>
    </template>
  </PreviewMessageCard>
</template>
