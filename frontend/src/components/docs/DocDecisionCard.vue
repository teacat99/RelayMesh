<script setup lang="ts">
// 文档里的决策卡（```decision 围栏）。
// 有对应确认项（服务端保存文档时按 doc_key 同步）且待处理时，卡片可以直接作答；答复会作为确认项答复送达智能体。
// 其余状态只读：已作答（显示答复）、选项变更后过期、围栏被移除后撤回、围栏有误、确认项尚未生成、历史修订。
import { computed, ref } from 'vue'
import { toast } from 'vue-sonner'
import ConfirmChoice from '../confirms/ConfirmChoice.vue'
import { confirmErrorMessage } from '../../api/confirms'
import type { DecisionCard } from '@/lib/docDecisions'
import { decisionAnswerable, decisionAnswerText, optionLabel, recommendedIds, type DocDecisionView } from '@/lib/confirms'
import { clearLocalDraft, draftScopes } from '@/lib/localDraft'
import { useProjectDocStore } from '@/stores/projectDocs'

const props = defineProps<{
  card: DecisionCard
  errors: string[]
  /** 服务端对齐后的视图；undefined = 没配上（编辑预览、或服务端还没同步）。 */
  view?: DocDecisionView
  workflowId?: string
  historic?: boolean
}>()

const store = useProjectDocStore()
const busy = ref(false)

const answerable = computed(() => !!props.workflowId && decisionAnswerable(props.view, props.historic))
const allErrors = computed(() => (props.view?.errors?.length ? props.view.errors : props.errors))
const testid = computed(() => `doc-decision-${props.card.id || 'x'}`)
// 与「待确认」区里同一张确认卡共用一份本机草稿（作用域按确认项 ref），两处入口填的内容互通
const draftScope = computed(() =>
  props.workflowId && props.view?.confirm_ref ? draftScopes.confirm(props.workflowId, props.view.confirm_ref) : undefined,
)

type Chip = { text: string; cls: string }
const chip = computed<Chip>(() => {
  const muted = 'border-border text-muted-foreground'
  if (props.historic) return { text: '历史版本', cls: muted }
  if (allErrors.value.length) return { text: '围栏有误', cls: 'border-destructive/50 text-destructive' }
  const v = props.view
  if (!v) return { text: '仅预览', cls: muted }
  switch (v.status) {
    case 'pending':
      return v.confirm_ref
        ? { text: `待作答 · ${v.confirm_ref}`, cls: 'border-amber-500/60 text-amber-700 dark:text-amber-300' }
        : { text: '待同步', cls: muted }
    case 'answered':
      return { text: `已作答 · ${v.confirm_ref}`, cls: 'border-emerald-500/50 text-emerald-700 dark:text-emerald-300' }
    case 'expired':
      return { text: '已过期', cls: muted }
    case 'withdrawn':
      return { text: '已撤回', cls: muted }
    case 'dismissed':
      return { text: '已忽略', cls: muted }
    default:
      return { text: v.status, cls: muted }
  }
})

const answeredText = computed(() => (props.view && props.view.answer ? decisionAnswerText(props.view) : ''))
const answeredIds = computed(() => new Set(props.view?.answer?.options ?? []))
const risk = computed(() => props.card.risk || 'L1')

// custom：「其他（自定义）」里写的答复（#183），没选中为空串
async function submit(options: string[], note: string, custom = '') {
  const ref = props.view?.confirm_ref
  if (!ref || !props.workflowId) return
  busy.value = true
  const scope = draftScope.value
  try {
    await store.answerDecision(props.workflowId, ref, options, note || undefined, custom || undefined)
    if (scope) clearLocalDraft(scope)
    toast.success(`已作答 ${ref}`, { duration: 2500 })
  } catch (err) {
    toast.error(confirmErrorMessage(err))
  } finally {
    busy.value = false
  }
}

async function adopt() {
  const ref = props.view?.confirm_ref
  if (!ref || !props.workflowId) return
  const ids = recommendedIds(props.card.options, !!props.card.multi)
  if (!ids.length) return
  busy.value = true
  const scope = draftScope.value
  try {
    await store.answerDecision(props.workflowId, ref, ids)
    if (scope) clearLocalDraft(scope)
    toast.success(`已采用推荐 ${ref}`, { description: ids.map(id => optionLabel(props.card.options, id)).join('、'), duration: 2500 })
  } catch (err) {
    toast.error(confirmErrorMessage(err))
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div
    class="my-4 rounded-xs border border-amber-500/40 bg-amber-500/5 p-4 text-sm"
    data-testid="doc-decision-card"
    :data-card="card.id"
    :data-status="historic ? 'historic' : view?.status || 'preview'"
  >
    <div class="flex items-start justify-between gap-2">
      <h4 class="font-semibold text-foreground">{{ card.title }}</h4>
      <span
        class="shrink-0 rounded-xs border px-1.5 text-[10px]"
        :class="chip.cls"
        :title="view?.status_note"
        :data-testid="`${testid}-chip`"
      >{{ chip.text }}</span>
    </div>
    <p v-if="card.body" class="mt-2 whitespace-pre-wrap text-muted-foreground">{{ card.body }}</p>

    <div v-if="answerable && view?.confirm_ref" class="mt-3">
      <ConfirmChoice
        :options="card.options"
        :multi="!!card.multi"
        :allow-note="!!card.note"
        :risk="risk"
        :default-option="card.default"
        :busy="busy"
        :testid="testid"
        :draft-scope="draftScope"
        @submit="submit"
        @adopt="adopt"
      />
    </div>
    <ul v-else class="mt-3 space-y-2">
      <li
        v-for="opt in card.options"
        :key="opt.id"
        class="flex items-start gap-2 rounded-xs border px-2 py-1.5"
        :class="answeredIds.has(opt.id) ? 'border-emerald-500/60 bg-emerald-500/10' : 'border-border/70'"
        :data-chosen="answeredIds.has(opt.id) ? '1' : '0'"
      >
        <span
          class="mt-0.5 size-3 shrink-0 rounded-full border"
          :class="answeredIds.has(opt.id) ? 'border-emerald-500 bg-emerald-500' : 'border-border'"
        />
        <div>
          <span class="font-medium">{{ opt.label }}</span>
          <span v-if="opt.recommended" class="ml-2 text-[10px] text-primary">推荐</span>
          <span v-if="card.default === opt.id" class="ml-2 text-[10px] text-muted-foreground">默认</span>
          <p v-if="opt.detail" class="mt-0.5 text-xs text-muted-foreground">{{ opt.detail }}</p>
        </div>
      </li>
    </ul>

    <p v-if="answeredText" class="mt-2 whitespace-pre-wrap break-words text-xs text-foreground/80" :data-testid="`${testid}-answer`">答复：{{ answeredText }}</p>
    <p v-if="view?.status === 'expired'" class="mt-2 text-[11px] text-muted-foreground">选项在作答后被改动，上面的答复已作废；保存文档后会生成新的待作答项。</p>
    <p v-else-if="view && view.status === 'pending' && !view.confirm_ref && !historic" class="mt-2 text-[11px] text-muted-foreground">{{ view.status_note }}</p>
    <p v-if="card.risk" class="mt-2 text-[10px] text-muted-foreground">风险：{{ card.risk }}</p>
    <ul v-if="allErrors.length" class="mt-2 list-disc pl-4 text-xs text-destructive">
      <li v-for="(e, i) in allErrors" :key="i">{{ e }}</li>
    </ul>
  </div>
</template>
