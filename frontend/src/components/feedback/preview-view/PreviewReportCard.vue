<script setup lang="ts">
// 汇报卡（#120 · D-273 阶段 C，只给预览视图用；经典仍是列表里「轮次分隔条 + AI 方案汇报」两段）。
//
// 一轮智能体的方案汇报就是一张统一消息卡：左色条（深灰）· 头像「智」· 「汇报」标签 · 标题 · 时间 · 状态（待您处理 / 已处理 / 已取消 / 已超时，取自预览状态词汇表 lib/statusWords；经典仍是等待确认 / 已完成）· ⋯ ，
// 正文是 Markdown 全文（不折叠、不限高），脚注放交付锚点（提交哈希 + 主题）、会话号（点一下复制）、「引用」、工作流号与工作区路径。
// 经典里头部右边那个「删除此轮」按钮收进了 ⋯ 菜单，仍是两段式确认：第一击菜单不关、这一项换成「再点一次确认删除」，3.5 秒内再点才真删。
// 删除本身（调接口、可撤销快照）由列表执行，这里只发 delete 事件，并通过 confirmingDelete 知道确认进行到哪一步。
import { computed } from 'vue'
import { Copy, FolderGit2 } from 'lucide-vue-next'
import type { FeedbackSession } from '../../../api/types'
import { cardTimeText, sessionStatusBadge, type CardMenuItem } from '../../../lib/messageCard'
import { replyRefToken } from '../../../lib/replyRef'
import PreviewBodyGate from './PreviewBodyGate.vue'
import PreviewMarkdown from './PreviewMarkdown.vue'
import Badge from '../../ui/badge/Badge.vue'
import AnchorChip from '../AnchorChip.vue'
import ReplyRefButton from '../ReplyRefButton.vue'
import { copyWithToast } from './cardCopy'
import PreviewCardMenu from './PreviewCardMenu.vue'
import PreviewMessageCard from './PreviewMessageCard.vue'

const props = defineProps<{
  session: FeedbackSession
  /** 工作区路径（已带主机名前缀）；没有则空串。 */
  projectDir: string
  /** 「删除此轮」的两段式确认进行中（第一击之后的 3.5 秒内）。 */
  confirmingDelete: boolean
}>()

const emit = defineEmits<{ (e: 'delete'): void }>()

const badge = computed(() => sessionStatusBadge(props.session.status))

// 轻量行（正文还没取回，见 PreviewBodyGate）手里只有首行预览，不能复制「正文」：取回之前先不给这一项
const menuItems = computed<CardMenuItem[]>(() => [
  ...(props.session.body_omitted ? [] : [{ key: 'copy-body', label: '复制正文', onSelect: () => void copyWithToast(props.session.summary, '正文') }]),
  ...(props.projectDir
    ? [{ key: 'copy-path', label: '复制工作区路径', onSelect: () => void copyWithToast(props.projectDir, '工作区路径') }]
    : []),
  {
    key: 'delete',
    label: props.confirmingDelete ? '再点一次确认删除（3.5 秒内）' : '删除此轮',
    destructive: true,
    keepOpen: !props.confirmingDelete,
    onSelect: () => emit('delete')
  }
])
</script>

<template>
  <PreviewMessageCard
    type="report"
    :title="session.title || '方案汇报'"
    :time="cardTimeText(session.created_at)"
    :time-title="session.created_at"
    data-testid="report-card"
    :data-session-id="session.session_id"
    :data-status="session.status"
  >
    <template #status>
      <Badge
        :variant="badge.variant"
        class="shrink-0 whitespace-nowrap rounded-xs px-1.5 py-0 text-[10px] font-normal"
        data-testid="card-status"
      >{{ badge.label }}</Badge>
    </template>
    <template #menu>
      <PreviewCardMenu :items="menuItems" />
    </template>

    <PreviewBodyGate :session="session" :lines="4">
      <PreviewMarkdown :content="session.summary" :workflow-id="session.workflow_id" />
    </PreviewBodyGate>

    <template #footer>
      <!-- 交付锚点：模型随本次交付申报的仓库最新提交（没有锚点的轮次不出现这一行） -->
      <AnchorChip
        v-if="session.git_commit"
        class="basis-full"
        :commit="session.git_commit"
        :subject="session.git_subject"
        @copy="copyWithToast"
      />
      <button
        type="button"
        class="group inline-flex shrink-0 cursor-pointer items-center gap-1 whitespace-nowrap rounded bg-muted px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground"
        :title="`点击复制会话 ID: ${session.session_id}`"
        data-testid="report-session-id"
        @click="copyWithToast(session.session_id, '会话 ID')"
      >
        <Copy class="size-2.5 text-muted-foreground transition-colors group-hover:text-primary" />
        <!-- 手机宽度下只留复制图标：编号就是引用标记里的哈希，点「引用」后输入框里看得到 -->
        <span class="hidden max-w-[8rem] truncate sm:inline">{{ session.session_id }}</span>
      </button>
      <!-- 引用这一轮：在输入框插入「【回复 哈希】」（#106） -->
      <ReplyRefButton :token="replyRefToken(session.session_id)" />
      <span
        v-if="session.workflow_id"
        class="max-w-[9rem] truncate font-mono text-[10px] text-muted-foreground"
        :title="session.workflow_id"
      >({{ session.workflow_id }})</span>
      <button
        v-if="projectDir"
        type="button"
        class="group hidden max-w-xs cursor-pointer items-center gap-1 truncate rounded px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground transition-colors hover:bg-muted/70 hover:text-foreground sm:flex md:max-w-md lg:max-w-lg"
        :title="`点击复制工作区路径: ${projectDir}`"
        @click="copyWithToast(projectDir, '工作区路径')"
      >
        <FolderGit2 class="size-3 shrink-0 transition-colors group-hover:text-primary" />
        <span class="truncate">{{ projectDir }}</span>
      </button>
    </template>
  </PreviewMessageCard>
</template>
