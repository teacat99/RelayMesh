<script setup lang="ts">
// 会话时间线里的确认卡（智能体的提问 / 授权 / 计划确认，#105）：
// - 还没答复的：就地作答——选项、备注、忽略都在卡上，不用再跳到别处（输入区上方的长条已经去掉）；
// - 处理过的：留下一行结局（答复内容 / 已忽略 / 已撤回 / 已过期），翻时间线就能回看当时的决定；
//   点一下展开（#132）：看到当时的方案正文、全部选项（用户选的标出来）、备注与时间，右上角「引用」把这张卡引用到输入框。
import { computed, ref, watch } from 'vue'
import { BookCheck, ChevronRight, Circle, CheckCircle2, CircleSlash } from 'lucide-vue-next'
import ConfirmCard from './ConfirmCard.vue'
import ConfirmLinks from './ConfirmLinks.vue'
import MarkdownRenderer from '../MarkdownRenderer.vue'
import ReplyRefButton from '../feedback/ReplyRefButton.vue'
import EventRowHead from '../feedback/EventRowHead.vue'
import EventStatus from '../feedback/EventStatus.vue'
import type { ConfirmItem } from '../../api/confirms'
import { useConfirmFocus } from '../../composables/useConfirmFocus'
import { useConfirmStore } from '../../stores/confirms'
import { KIND_LABEL, RISK_LABEL, RISK_SHORT, STATUS_LABEL, ageText, answerText, customAnswerOf, recallOptions, recallTimes, riskTone } from '../../lib/confirms'
import { confirmRefToken } from '../../lib/replyRef'
import { EVENT_BOX } from '../../lib/eventRow'
import { CARD_CHIP_BASE, CARD_TONE_CLASS, CARD_TYPES, cardTimeText } from '../../lib/messageCard'
import { STATUS_WORD } from '../../lib/statusWords'

// compact（#120 预览视图，默认关）：已处理的卡折叠时是 32px 的事件行（阶段 C2：类型标签 · 编号 · 标题 · 结局 · 时间 · 状态，
// 点一下原地展开）；展开后详情原地全部展开、高度不限（经典展开体里正文限高 max-h-72、框内滚动）。
// 经典列表不传它，经典的标记一个字没动；还没答复的提问在经典里仍是完整的 ConfirmCard（预览列表不会把待答复的卡交给这里：
// 阶段 C3 起它们是预览视图自己的统一消息卡，作答动作与聚焦高亮和这里共用 composables）。
const props = defineProps<{ item: ConfirmItem; compact?: boolean }>()

const store = useConfirmStore()
const tone = computed(() => riskTone(props.item.risk))
// 折叠行上的结局：自定义答复压成一行摘要（全文在展开后的「自定义答复」里，#183）
const result = computed(() => (props.item.status === 'answered' ? answerText(props.item, 60) : ''))
const pending = computed(() => props.item.status === 'pending')

// 回看：默认收起，只留一行结局；展开状态属于这张卡自己（时间线重新渲染不会丢）
const expanded = ref(false)
const options = computed(() => recallOptions(props.item))
const times = computed(() => recallTimes(props.item))
const note = computed(() => (props.item.status === 'answered' ? props.item.answer?.note?.trim() ?? '' : ''))
// 用户在「其他（自定义）」里写的答复全文（#183）；折叠行只给摘要
const custom = computed(() => (props.item.status === 'answered' ? customAnswerOf(props.item) : ''))

// 事件行（compact）：色调随提问的种类（提问 琥珀 / 计划 红 / 决策 紫）；结局放进预览（宽屏才显示，只有备注时显示备注），
// 状态标签是「已处理」或「已忽略 / 已撤回 / 已过期」；时间是答复（没有则创建）的时刻，悬停看完整时间
const eventTone = computed(() => CARD_TONE_CLASS[CARD_TYPES[props.item.kind].tone])
const eventPreview = computed(() => {
  if (props.item.status !== 'answered') return ''
  const text = result.value || note.value
  return text ? `已答复：${text}` : '已答复'
})
const eventStatus = computed(() => (props.item.status === 'answered' ? STATUS_WORD.done : STATUS_LABEL[props.item.status]))
const eventAt = computed(() => props.item.answered_at || props.item.created_at)

// 工具栏的「待答复」徽标点一下，会把要看的编号放进 store.focusRef：滚到这张卡并亮一下边框
// （逻辑在 composables/useConfirmFocus，预览视图的统一卡共用同一份）
const rootEl = ref<HTMLElement | null>(null)
const focused = useConfirmFocus(() => props.item.ref, rootEl)
// 被点名的是已处理的卡（徽标 / 正文里的 Q23 引用）：展开它回看当时的方案与答复，并亮一下边框；待答复的卡由 ConfirmCard 自己亮
// （immediate：卡可能是在 focusRef 已经设好之后才挂载的——比如它所在的摞刚被展开——useConfirmFocus 在 setup 里就已经把 focused 置真了）
watch(
  focused,
  on => {
    if (on && !pending.value) expanded.value = true
  },
  { immediate: true }
)
</script>

<template>
  <div
    ref="rootEl"
    :data-testid="`confirm-tl-${item.ref}`"
    :data-status="item.status"
    :class="focused && !pending ? 'rounded-sm ring-2 ring-primary/60' : ''"
  >
    <ConfirmCard v-if="pending" :item="item" :workflow-id="store.workflowId" :focused="focused" />

    <div
      v-else
      :class="compact ? [EVENT_BOX, eventTone] : 'rounded-sm border border-border/60 bg-muted/20 text-xs'"
      :data-expanded="expanded ? 'true' : 'false'"
    >
      <!-- 事件行（预览视图，compact）：32px 单行，点一下展开；「引用」在行末 -->
      <EventRowHead
        v-if="compact"
        :type="item.kind"
        :lead="item.ref"
        :title="item.title"
        :preview="eventPreview"
        :time="cardTimeText(eventAt)"
        :time-title="eventAt"
        interactive
        :expanded="expanded"
        :open-title="expanded ? '收起' : '展开回看当时的方案、选项与答复'"
        :main-testid="`confirm-tl-${item.ref}-toggle`"
        :preview-testid="`confirm-tl-${item.ref}-state`"
        @click="expanded = !expanded"
      >
        <template #status>
          <!-- 风险标签：手机上只留高风险（danger），中风险让位给标题（完整风险等级在展开后的详情里） -->
          <span
            v-if="tone !== 'muted'"
            :class="[CARD_CHIP_BASE, tone === 'danger' ? 'border-destructive/50 text-destructive' : 'border-amber-500/40 text-amber-700 max-sm:hidden dark:text-amber-300']"
            :title="RISK_LABEL[item.risk]"
          >{{ RISK_SHORT[item.risk] }}</span>
          <EventStatus tone="plain">{{ eventStatus }}</EventStatus>
        </template>
        <template #trail>
          <ReplyRefButton :token="confirmRefToken(item.ref)" title="引用这张确认卡：在输入框插入标记，智能体读到时会附上标题、选项、你的答复与正文开头" />
        </template>
      </EventRowHead>

      <div v-else class="flex items-start gap-1.5 px-3 py-2">
        <button
          type="button"
          class="flex min-w-0 flex-1 cursor-pointer items-start gap-2 text-left"
          :aria-expanded="expanded"
          :title="expanded ? '收起' : '展开回看当时的方案、选项与答复'"
          :data-testid="`confirm-tl-${item.ref}-toggle`"
          @click="expanded = !expanded"
        >
          <component
            :is="item.status === 'answered' ? CheckCircle2 : CircleSlash"
            class="mt-0.5 size-3.5 shrink-0"
            :class="item.status === 'answered' ? 'text-emerald-600 dark:text-emerald-400' : 'text-muted-foreground'"
          />
          <span class="block min-w-0 flex-1 space-y-0.5">
            <span class="flex flex-wrap items-center gap-x-2 gap-y-0.5">
              <BookCheck class="size-3 shrink-0 text-muted-foreground" />
              <span class="font-mono text-[11px] font-semibold text-foreground/70">{{ item.ref }}</span>
              <span class="text-[10px] text-muted-foreground">{{ KIND_LABEL[item.kind] }}</span>
              <span
                v-if="tone !== 'muted'"
                class="rounded-xs border px-1 text-[10px]"
                :class="tone === 'danger' ? 'border-destructive/50 text-destructive' : 'border-amber-500/40 text-amber-700 dark:text-amber-300'"
              >{{ RISK_SHORT[item.risk] }}</span>
              <span class="min-w-0 break-words text-[13px] font-medium leading-snug text-foreground">{{ item.title }}</span>
            </span>
            <span class="block text-[11px] text-muted-foreground" :data-testid="`confirm-tl-${item.ref}-state`">
              <template v-if="item.status === 'answered'">已答复：<span class="text-foreground/80">{{ result }}</span></template>
              <template v-else>{{ STATUS_LABEL[item.status] }}</template>
              <span class="ml-1.5 opacity-70">· {{ ageText(item.answered_at || item.created_at) }}</span>
            </span>
          </span>
          <ChevronRight class="mt-0.5 size-3.5 shrink-0 text-muted-foreground transition-transform" :class="expanded ? 'rotate-90' : ''" />
        </button>
        <!-- 引用这张卡：已处理的也能引用，智能体读到时会附上标题、选项、用户的答复与正文开头（#132） -->
        <ReplyRefButton :token="confirmRefToken(item.ref)" title="引用这张确认卡：在输入框插入标记，智能体读到时会附上标题、选项、你的答复与正文开头" />
      </div>

      <div
        v-if="expanded"
        class="space-y-2 border-t border-border/50 px-3 py-2"
        :data-testid="`confirm-tl-${item.ref}-detail`"
      >
        <p v-if="times" class="text-[11px] text-muted-foreground" :data-testid="`confirm-tl-${item.ref}-times`">{{ times }}</p>

        <ConfirmLinks :item="item" :workflow-id="store.workflowId" :testid="`confirm-tl-${item.ref}`" />

        <div
          v-if="item.body"
          class="rounded-xs border border-border/50 bg-background/60 px-2.5 py-2"
          :class="compact ? '' : 'max-h-72 overflow-y-auto'"
          :data-testid="`confirm-tl-${item.ref}-body`"
        >
          <MarkdownRenderer :content="item.body" :workflow-id="store.workflowId" />
        </div>

        <ul v-if="options.length" class="space-y-1" :data-testid="`confirm-tl-${item.ref}-options`">
          <li
            v-for="o in options"
            :key="o.id"
            class="flex items-start gap-1.5 rounded-xs px-1.5 py-1"
            :class="o.chosen ? 'bg-emerald-500/10' : ''"
            :data-testid="`confirm-tl-${item.ref}-opt-${o.id}`"
            :data-chosen="o.chosen ? 'true' : 'false'"
          >
            <component
              :is="o.chosen ? CheckCircle2 : Circle"
              class="mt-0.5 size-3.5 shrink-0"
              :class="o.chosen ? 'text-emerald-600 dark:text-emerald-400' : 'text-muted-foreground/50'"
            />
            <span class="block min-w-0 flex-1">
              <span class="break-words" :class="o.chosen ? 'font-medium text-foreground' : 'text-foreground/80'">{{ o.label }}</span>
              <span v-if="o.chosen" class="ml-1.5 text-[10px] text-emerald-700 dark:text-emerald-300">你选了这个</span>
              <span v-if="o.recommended" class="ml-1.5 rounded-xs border border-border/60 px-1 text-[10px] text-muted-foreground">推荐</span>
              <span v-if="o.missing" class="ml-1.5 text-[10px] text-muted-foreground">已不在选项里</span>
              <span v-if="o.detail" class="mt-0.5 block whitespace-pre-wrap break-words text-[11px] text-muted-foreground">{{ o.detail }}</span>
            </span>
          </li>
        </ul>

        <p
          v-if="note"
          class="whitespace-pre-wrap break-words rounded-xs border border-border/50 bg-background/60 px-2.5 py-1.5 text-[11px] text-foreground/80"
          :data-testid="`confirm-tl-${item.ref}-note`"
        >备注：{{ note }}</p>
        <p
          v-if="custom"
          class="whitespace-pre-wrap break-words rounded-xs border border-primary/30 bg-primary/5 px-2.5 py-1.5 text-[11px] text-foreground/90"
          :data-testid="`confirm-tl-${item.ref}-custom`"
        >自定义答复：{{ custom }}</p>
      </div>
    </div>
  </div>
</template>
