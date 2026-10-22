<script setup lang="ts">
// 「待您处理」的提问 / 计划确认的统一消息卡（#120 · D-273 阶段 C3，只给预览视图用；经典仍是 ConfirmTimelineCard 里的 ConfirmCard）。
//
// 还要您拍板的提问 / 计划是正文里最要紧的一类话，所以也套同一张卡，元素与顺序同其它卡：
//   头部：智 · 提问（计划）· 标题 · 创建时间 · 风险（L0 / L1 / L2）· 「待您处理」（脉动的小点）· ⋯（复制正文 / 复制编号）
//   正文：关联任务 / 来源文档入口 → Markdown 全文（不限高：经典 ConfirmCard 的 max-h-56 框内滚动在这里取消）
//         → [计划：批准之前智能体只做只读的准备工作] → 作答区（选项 + 其他（自定义）+ 备注 + 提交 / 采用推荐，控件 ConfirmChoice 与经典共用）
//   脚注：编号 · 引用 · 忽略（两段式确认：第一击换成「确认忽略 / 算了」）
// 作答 / 采用推荐 / 忽略的动作、失败提示与本机草稿（composables/useConfirmAnswer），以及被工具栏「待答复」点名时的滚动与高亮
// （composables/useConfirmFocus）都与经典同一份实现，这里只管摆放。作答成功后条目变成「已答复」，列表自动把它换成 32px 的事件行。
// 外层 div 保留经典的定位容器标记（confirm-tl-<编号> + data-status），卡根上保留 confirm-<编号> / data-kind / data-risk，e2e 与经典命名一致。
import { computed, ref } from 'vue'
import { FileText, X } from 'lucide-vue-next'
import type { ConfirmItem } from '../../../api/confirms'
import { useConfirmAnswer } from '../../../composables/useConfirmAnswer'
import { useConfirmFocus } from '../../../composables/useConfirmFocus'
import { RISK_LABEL, RISK_SHORT, riskTone } from '../../../lib/confirms'
import { CARD_CHIP_BASE, CARD_PANEL_CLASS, CARD_TYPES, cardTimeText, type CardMenuItem } from '../../../lib/messageCard'
import { confirmRefToken } from '../../../lib/replyRef'
import { STATUS_WORD } from '../../../lib/statusWords'
import ConfirmChoice from '../../confirms/ConfirmChoice.vue'
import ConfirmLinks from '../../confirms/ConfirmLinks.vue'
import PreviewMarkdown from './PreviewMarkdown.vue'
import EventStatus from '../EventStatus.vue'
import ReplyRefButton from '../ReplyRefButton.vue'
import { copyWithToast } from './cardCopy'
import PreviewCardMenu from './PreviewCardMenu.vue'
import PreviewMessageCard from './PreviewMessageCard.vue'

const props = defineProps<{ item: ConfirmItem; workflowId: string }>()

const { busy, draftScope, confirmDismiss, submit, adopt, dismiss } = useConfirmAnswer(() => props.item, () => props.workflowId)
const rootEl = ref<HTMLElement | null>(null)
const focused = useConfirmFocus(() => props.item.ref, rootEl)

const testid = computed(() => `confirm-${props.item.ref}`)
const statusTone = computed(() => CARD_TYPES[props.item.kind].tone)

// 风险标签：与事件行同一套让位规则——手机上只留高风险（L2），其余让位给标题（完整等级在悬停提示里）
const riskClass = computed(() => {
  const tone = riskTone(props.item.risk)
  if (tone === 'danger') return 'border-destructive/50 text-destructive'
  if (tone === 'warn') return 'border-amber-500/40 text-amber-700 max-sm:hidden dark:text-amber-300'
  return 'border-border text-muted-foreground max-sm:hidden'
})

const menuItems = computed<CardMenuItem[]>(() => [
  { key: 'copy-body', label: '复制正文', onSelect: () => void copyWithToast(props.item.body || props.item.title, '正文') },
  { key: 'copy-ref', label: '复制编号', onSelect: () => void copyWithToast(props.item.ref, '编号') }
])
</script>

<template>
  <div ref="rootEl" :data-testid="`confirm-tl-${item.ref}`" data-status="pending">
    <PreviewMessageCard
      :type="item.kind"
      :title="item.title"
      :title-testid="`${testid}-title`"
      :time="cardTimeText(item.created_at)"
      :time-title="item.created_at"
      :class="focused ? 'ring-2 ring-primary/50' : ''"
      :data-testid="testid"
      :data-kind="item.kind"
      :data-risk="item.risk"
    >
      <template #status>
        <span :class="[CARD_CHIP_BASE, riskClass]" :title="RISK_LABEL[item.risk]" :data-testid="`${testid}-risk`">{{ RISK_SHORT[item.risk] || item.risk }}</span>
        <EventStatus :tone="statusTone" pulse>{{ STATUS_WORD.pending }}</EventStatus>
      </template>
      <template #menu>
        <PreviewCardMenu :items="menuItems" />
      </template>

      <div class="space-y-2.5">
        <ConfirmLinks :item="item" :workflow-id="workflowId" :testid="testid" />

        <!-- 正文全文：不限高、不折叠（经典里这里是 max-h-56 的框内滚动） -->
        <div v-if="item.body" :data-testid="`${testid}-body`">
          <PreviewMarkdown :content="item.body" :workflow-id="workflowId" />
        </div>
        <p v-if="item.kind === 'plan'" class="flex items-center gap-1 text-[11px] text-muted-foreground">
          <FileText class="size-3" />批准之前，智能体只会做只读的准备工作。
        </p>

        <!-- 作答区：要您动手的这一块从正文里浮出来（色调取自卡的色条） -->
        <div :class="[CARD_PANEL_CLASS, 'p-2.5']" :data-testid="`${testid}-answer`">
          <ConfirmChoice
            :options="item.options"
            :multi="item.multi"
            :allow-note="item.allow_note"
            :risk="item.risk"
            :default-option="item.default_option"
            :plan="item.kind === 'plan'"
            :busy="busy"
            :testid="testid"
            :draft-scope="draftScope"
            @submit="submit"
            @adopt="adopt"
          />
        </div>
      </div>

      <template #footer>
        <span class="font-mono text-[11px] font-semibold text-foreground/70" :data-testid="`${testid}-ref`">{{ item.ref }}</span>
        <!-- 引用这张卡：在输入框插入「【确认 Q5】」，智能体读到时会附上标题、选项与正文开头（#132） -->
        <ReplyRefButton :token="confirmRefToken(item.ref)" title="引用这张确认卡：在输入框插入标记，智能体读到时会附上标题、选项与正文开头" />
        <!-- 忽略：两段式，第一击只换成「确认忽略 / 算了」；手机上按钮撑到 44px 高，好点 -->
        <span class="ml-auto flex flex-wrap items-center justify-end gap-x-1">
          <span v-if="confirmDismiss" class="text-[11px] text-muted-foreground">忽略后会告诉智能体不用再等它。</span>
          <button
            v-if="confirmDismiss"
            type="button"
            class="-my-1.5 cursor-pointer px-1 py-1.5 text-[11px] text-muted-foreground hover:text-foreground max-md:min-h-11 max-md:px-2"
            :data-testid="`${testid}-dismiss-cancel`"
            @click="confirmDismiss = false"
          >
            算了
          </button>
          <button
            type="button"
            class="-my-1.5 inline-flex cursor-pointer items-center gap-1 px-1 py-1.5 text-[11px] text-muted-foreground hover:text-destructive disabled:cursor-not-allowed disabled:opacity-60 max-md:min-h-11 max-md:px-2"
            :disabled="busy"
            :data-testid="`${testid}-dismiss`"
            @click="dismiss"
          >
            <X class="size-3" />{{ confirmDismiss ? '确认忽略' : '忽略' }}
          </button>
        </span>
      </template>
    </PreviewMessageCard>
  </div>
</template>
