<script setup lang="ts">
// 「待确认」区里的一张确认卡：智能体的提问 / 计划确认 / 文档决策。
// 作答走 store（单条接口，服务端同事务写入收件箱）；失败时卡片保持原样并提示原因，不会出现「界面已答复、智能体收不到」。
// 作答动作（提交 / 采用推荐 / 两段式忽略）、「提交中」与本机草稿作用域在 composables/useConfirmAnswer：预览视图的统一卡共用同一份。
import { computed } from 'vue'
import { FileText, X } from 'lucide-vue-next'
import MarkdownRenderer from '../MarkdownRenderer.vue'
import ReplyRefButton from '../feedback/ReplyRefButton.vue'
import ConfirmChoice from './ConfirmChoice.vue'
import ConfirmLinks from './ConfirmLinks.vue'
import type { ConfirmItem } from '../../api/confirms'
import { useConfirmAnswer } from '../../composables/useConfirmAnswer'
import { KIND_LABEL, RISK_SHORT, riskTone } from '../../lib/confirms'
import { confirmRefToken } from '../../lib/replyRef'

const props = defineProps<{ item: ConfirmItem; workflowId: string; focused?: boolean }>()

// 选择、备注与自定义答复存本机草稿（draftScope）：切走再回来、刷新都还在；答复 / 采用推荐 / 忽略成功后清掉
const { busy, draftScope, confirmDismiss, submit, adopt, dismiss } = useConfirmAnswer(() => props.item, () => props.workflowId)
const testid = computed(() => `confirm-${props.item.ref}`)
const tone = computed(() => riskTone(props.item.risk))

const kindClass = computed(() => {
  if (props.item.kind === 'plan') return 'border-primary/40 text-primary'
  if (props.item.kind === 'decision') return 'border-amber-500/50 text-amber-700 dark:text-amber-300'
  return 'border-border text-muted-foreground'
})
const riskClass = computed(() => {
  if (tone.value === 'danger') return 'border-destructive/50 bg-destructive/10 text-destructive'
  if (tone.value === 'warn') return 'border-amber-500/40 bg-amber-500/10 text-amber-700 dark:text-amber-300'
  return 'border-border bg-muted/40 text-muted-foreground'
})

</script>

<template>
  <article
    class="rounded-sm border bg-card/80 p-3 text-sm shadow-2xs"
    :class="[focused ? 'border-primary ring-1 ring-primary/40' : 'border-border/70']"
    :data-testid="testid"
    :data-kind="item.kind"
    :data-risk="item.risk"
  >
    <header class="flex flex-wrap items-start gap-x-2 gap-y-1">
      <span class="shrink-0 font-mono text-[11px] font-semibold text-foreground/70" :data-testid="`${testid}-ref`">{{ item.ref }}</span>
      <span class="shrink-0 rounded-xs border px-1.5 text-[10px]" :class="kindClass">{{ KIND_LABEL[item.kind] }}</span>
      <span class="shrink-0 rounded-xs border px-1.5 text-[10px]" :class="riskClass" :title="item.risk === 'L2' ? '不可逆 / 高风险：需要逐条有意识地选择' : undefined">{{ RISK_SHORT[item.risk] || item.risk }}</span>
      <h4 class="min-w-0 flex-1 basis-40 break-words text-[13px] font-semibold leading-snug text-foreground" :data-testid="`${testid}-title`">{{ item.title }}</h4>
      <!-- 引用这张卡：在输入框插入「【确认 Q5】」，智能体读到时会附上标题、选项与正文开头（#132） -->
      <ReplyRefButton :token="confirmRefToken(item.ref)" title="引用这张确认卡：在输入框插入标记，智能体读到时会附上标题、选项与正文开头" />
    </header>

    <ConfirmLinks class="mt-1.5" :item="item" :workflow-id="workflowId" :testid="testid" />

    <div
      v-if="item.body"
      class="mt-2 max-h-56 overflow-y-auto rounded-xs border border-border/50 bg-muted/20 px-2.5 py-2 text-xs"
      :data-testid="`${testid}-body`"
    >
      <MarkdownRenderer :content="item.body" :workflow-id="workflowId" />
    </div>
    <p v-if="item.kind === 'plan'" class="mt-1.5 flex items-center gap-1 text-[11px] text-muted-foreground">
      <FileText class="size-3" />批准之前，智能体只会做只读的准备工作。
    </p>

    <div class="mt-2.5">
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

    <footer class="mt-2 flex items-center justify-end gap-2 border-t border-border/50 pt-1.5">
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
    </footer>
  </article>
</template>
