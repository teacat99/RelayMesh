<script setup lang="ts">
// 选项 + 其他（自定义）+ 备注 + 提交：待确认区的确认卡与文档里的决策卡共用的作答控件。
// 它只管「选什么、能不能提交」，不碰网络；提交 / 采用推荐都交给上层去调接口，
// 这样两处入口的规则（单选多选、自定义、备注、L2 不给一键采用）只写这一份。
//
// 「其他（自定义）」（#183）：每张确认卡的选项列表末尾常驻这一行，预设选项都不对时用户自己写答复，不用事先靠智能体多加一个「其他」选项。
// 单选时它与预设选项二选一（点它清掉预设选项，点预设选项就收起它）；多选时可与预设选项并存。「备注」保持原样：只在这一项允许备注时出现，
// 是对所选项的补充说明，与「自定义」是两回事。
import { computed, nextTick, ref, watch } from 'vue'
import { Check, Sparkles } from 'lucide-vue-next'
import Button from '../ui/button/Button.vue'
import DraftRestoredNote from '../DraftRestoredNote.vue'
import type { ConfirmOption } from '../../api/confirms'
import {
  CONFIRM_CUSTOM_MAX,
  canAdoptRecommended,
  checkSelection,
  selectionAfterCustomToggle,
  toggleSelection,
} from '../../lib/confirms'
import { useLocalDraft } from '../../composables/useLocalDraft'

const props = defineProps<{
  options: ConfirmOption[]
  multi: boolean
  allowNote: boolean
  risk: string
  /** 智能体等待期间先按它推进的选项（只是工作假设，不是答复）。 */
  defaultOption?: string
  /** 这是一张计划确认：自定义答复不等于批准，选中「其他（自定义）」时提示用户。 */
  plan?: boolean
  busy?: boolean
  /** data-testid 前缀，如 confirm-Q12。 */
  testid: string
  /**
   * 本机草稿的作用域（draftScopes.confirm）：给了就把「选了什么 + 自定义内容 + 备注」存在这个浏览器里，
   * 切走再回来、刷新页面都还在。提交成功后由上层按同一个作用域清掉（clearLocalDraft）。不给 = 不存。
   */
  draftScope?: string
}>()

const emit = defineEmits<{
  /** custom：选中了「其他（自定义）」时用户写的答复（已去掉首尾空白），没选中为空串。 */
  (e: 'submit', options: string[], note: string, custom: string): void
  (e: 'adopt'): void
}>()

const selected = ref<string[]>([])
const note = ref('')
// 「其他（自定义）」：customOn = 这一行被选中；customText 在取消选中后仍保留，再点回来文字还在（误点一下不丢已写的内容）。
const customOn = ref(false)
const customText = ref('')
const customEl = ref<HTMLTextAreaElement | null>(null)

// 选项变了（文档决策卡被改、或服务端刷新了这一项）、或刚从草稿回填：丢掉已不存在的选中，避免提交一个失效的选项；
// 单选只留一个，且与「自定义」二选一（同时有时保留用户亲手写的自定义、丢掉预设选项）；这一项不接受备注就不带备注。
// 放在 watch 里而不是回填时直接过滤：上层换卡时 props 是一个个更新的，回填那一刻 options 可能还是旧的。
watch(
  [() => props.options.map(o => o.id).join('\u0000'), () => props.multi, () => props.allowNote, selected, customOn],
  () => {
    const known = new Set(props.options.map(o => o.id))
    let kept = selected.value.filter(id => known.has(id))
    if (!props.multi && kept.length > 1) kept = kept.slice(0, 1)
    if (!props.multi && customOn.value && kept.length > 0) kept = []
    if (kept.length !== selected.value.length) selected.value = kept
    if (!props.allowNote && note.value) note.value = ''
  },
)

interface ConfirmDraft {
  selected: string[]
  note: string
  /** 自定义答复（#183）；老版本存的草稿没有这两个字段，按「没选中、没写」处理。 */
  customOn?: boolean
  customText?: string
}
function isConfirmDraft(d: unknown): d is ConfirmDraft {
  const o = d as Partial<ConfirmDraft> | null
  return (
    !!o &&
    Array.isArray(o.selected) &&
    o.selected.every(x => typeof x === 'string') &&
    typeof o.note === 'string' &&
    (o.customOn === undefined || typeof o.customOn === 'boolean') &&
    (o.customText === undefined || typeof o.customText === 'string')
  )
}

const { restored, discard: discardDraft } = useLocalDraft<ConfirmDraft>({
  scope: () => props.draftScope || null,
  read: () => ({ selected: [...selected.value], note: note.value, customOn: customOn.value, customText: customText.value }),
  apply: saved => {
    selected.value = saved ? [...saved.selected] : []
    note.value = saved ? saved.note : ''
    customOn.value = !!saved?.customOn
    customText.value = saved?.customText ?? ''
  },
  isEmpty: d => d.selected.length === 0 && d.note.trim() === '' && !d.customOn && (d.customText ?? '').trim() === '',
  validate: isConfirmDraft,
})

const check = computed(() =>
  checkSelection(
    { options: props.options, multi: props.multi, allow_note: props.allowNote },
    selected.value,
    note.value,
    { on: customOn.value, text: customText.value },
  ),
)
// 还什么都没填（含刚点开「其他（自定义）」、文字还空着）：提示只是引导，不当作错误标红
const hintOnly = computed(() => selected.value.length === 0 && (!customOn.value || customText.value.trim() === ''))
const adoptable = computed(() =>
  canAdoptRecommended({ options: props.options, multi: props.multi, risk: props.risk as 'L0' | 'L1' | 'L2' }),
)

function pick(id: string) {
  if (props.busy) return
  // 单选：选预设选项就等于放弃「自定义」（文字留着，再点回「其他（自定义）」还在）
  if (!props.multi) customOn.value = false
  selected.value = toggleSelection(props.options, props.multi, selected.value, id)
}

function toggleCustom() {
  if (props.busy) return
  customOn.value = !customOn.value
  selected.value = selectionAfterCustomToggle(props.multi, selected.value, customOn.value)
  if (customOn.value) void nextTick(() => customEl.value?.focus())
}

function submit() {
  if (props.busy || !check.value.ok) return
  emit('submit', [...selected.value], note.value.trim(), customOn.value ? customText.value.trim() : '')
}
</script>

<template>
  <div class="space-y-2" :data-testid="`${testid}-choice`">
    <ul class="space-y-1.5" :role="multi ? 'group' : 'radiogroup'">
      <li v-for="opt in options" :key="opt.id">
        <button
          type="button"
          :role="multi ? 'checkbox' : 'radio'"
          :aria-checked="selected.includes(opt.id)"
          :disabled="busy"
          class="flex w-full cursor-pointer items-start gap-2 rounded-xs border px-2 py-1.5 text-left transition-colors focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-primary/60 disabled:cursor-not-allowed disabled:opacity-60 max-md:min-h-11"
          :class="selected.includes(opt.id) ? 'border-primary/70 bg-primary/10' : 'border-border/70 hover:bg-muted/40'"
          :data-testid="`${testid}-opt-${opt.id}`"
          :data-selected="selected.includes(opt.id) ? '1' : '0'"
          @click="pick(opt.id)"
        >
          <span
            class="mt-0.5 flex size-3.5 shrink-0 items-center justify-center border"
            :class="[multi ? 'rounded-xs' : 'rounded-full', selected.includes(opt.id) ? 'border-primary bg-primary text-primary-foreground' : 'border-border']"
            aria-hidden="true"
          >
            <Check v-if="selected.includes(opt.id)" class="size-2.5" />
          </span>
          <span class="min-w-0 flex-1">
            <span class="break-words text-[13px] font-medium leading-snug text-foreground">{{ opt.label }}</span>
            <span v-if="opt.recommended" class="ml-1.5 rounded-xs bg-primary/15 px-1 text-[10px] text-primary" :data-testid="`${testid}-rec-${opt.id}`">推荐</span>
            <span
              v-if="defaultOption === opt.id"
              class="ml-1.5 rounded-xs bg-muted px-1 text-[10px] text-muted-foreground"
              title="智能体在等你答复期间，先按这个选项推进可回滚的部分"
            >智能体暂按此推进</span>
            <span v-if="opt.detail" class="mt-0.5 block whitespace-pre-wrap break-words text-xs leading-relaxed text-muted-foreground">{{ opt.detail }}</span>
          </span>
        </button>
      </li>

      <!-- 其他（自定义）：常驻在选项末尾，预设选项都不对时自己写（#183） -->
      <li :data-testid="`${testid}-custom-row`">
        <button
          type="button"
          :role="multi ? 'checkbox' : 'radio'"
          :aria-checked="customOn"
          :disabled="busy"
          class="flex w-full cursor-pointer items-start gap-2 rounded-xs border border-dashed px-2 py-1.5 text-left transition-colors focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-primary/60 disabled:cursor-not-allowed disabled:opacity-60 max-md:min-h-11"
          :class="customOn ? 'border-primary/70 bg-primary/10' : 'border-border/70 hover:bg-muted/40'"
          :data-testid="`${testid}-custom-toggle`"
          :data-selected="customOn ? '1' : '0'"
          @click="toggleCustom"
        >
          <span
            class="mt-0.5 flex size-3.5 shrink-0 items-center justify-center border"
            :class="[multi ? 'rounded-xs' : 'rounded-full', customOn ? 'border-primary bg-primary text-primary-foreground' : 'border-border']"
            aria-hidden="true"
          >
            <Check v-if="customOn" class="size-2.5" />
          </span>
          <span class="min-w-0 flex-1">
            <span class="break-words text-[13px] font-medium leading-snug text-foreground">其他（自定义）</span>
            <span v-if="!customOn" class="mt-0.5 block text-xs leading-relaxed text-muted-foreground">以上都不合适？直接写下你要的答复</span>
          </span>
        </button>
        <template v-if="customOn">
          <textarea
            ref="customEl"
            v-model="customText"
            rows="3"
            :maxlength="CONFIRM_CUSTOM_MAX"
            :disabled="busy"
            aria-label="自定义答复"
            placeholder="写下你的答复：预设选项都不合适时，直接说你要什么"
            class="mt-1.5 w-full resize-y rounded-xs border border-border bg-background px-2 py-1.5 text-xs leading-relaxed outline-none focus-visible:ring-1 focus-visible:ring-primary/60 disabled:opacity-60"
            :data-testid="`${testid}-custom`"
          />
          <p v-if="plan" class="mt-1 text-[11px] leading-snug text-muted-foreground" :data-testid="`${testid}-custom-plan-hint`">
            这是计划确认：自定义答复不等于批准，智能体会当作修改意见；批准之前它仍只做只读的准备工作。要批准请选对应的选项。
          </p>
        </template>
      </li>
    </ul>

    <textarea
      v-if="allowNote"
      v-model="note"
      rows="2"
      maxlength="2000"
      :disabled="busy"
      placeholder="备注（可选）：补充你的想法或条件"
      class="w-full resize-y rounded-xs border border-border bg-background px-2 py-1.5 text-xs leading-relaxed outline-none focus-visible:ring-1 focus-visible:ring-primary/60 disabled:opacity-60"
      :data-testid="`${testid}-note`"
    />

    <DraftRestoredNote v-if="restored" what="选择与填写的内容" :testid="`${testid}-draft-note`" @discard="discardDraft" />

    <div class="flex flex-wrap items-center gap-2">
      <Button
        type="button"
        size="sm"
        class="h-7 gap-1 text-xs"
        :disabled="busy || !check.ok"
        :data-testid="`${testid}-submit`"
        @click="submit"
      >
        <Check class="size-3.5" />{{ busy ? '提交中…' : '提交答复' }}
      </Button>
      <Button
        v-if="adoptable"
        type="button"
        size="sm"
        variant="outline"
        class="h-7 gap-1 text-xs"
        :disabled="busy"
        title="直接采用带「推荐」标记的选项并提交"
        :data-testid="`${testid}-adopt`"
        @click="emit('adopt')"
      >
        <Sparkles class="size-3.5" />采用推荐
      </Button>
      <span v-if="!check.ok && hintOnly" class="text-[11px] text-muted-foreground">{{ check.reason }}</span>
      <span v-else-if="!check.ok" class="text-[11px] text-destructive" role="alert">{{ check.reason }}</span>
    </div>
  </div>
</template>
