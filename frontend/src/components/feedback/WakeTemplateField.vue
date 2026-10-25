<script setup lang="ts">
// 一类唤醒词模板的编辑器（#232 D3，D-376）：文本框 + 变量按钮 + 「以内置默认为底稿」 + 实时预览最终文字。
// 会话页「常驻」弹窗的「唤醒词」页签（续聊 / 新开对话）与设置页（三类全局覆盖）共用。
// 预览由服务端渲染（与真正发出去的是同一套渲染器，见 WakePreview）：带 workflowId 时用这个会话此刻的真实现状，不带用示例值。
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { wakePromptsApi, type WakeKind } from '@/api/wakePrompts'
import { useWakePromptsStore } from '@/stores/wakePrompts'
import { insertVarAt, wakeKindLabel, wakeLayerHint, wakeLayerLabel, wakeLength, wakeTemplateProblem } from '@/lib/wakePrompts'
import WakePreview from './WakePreview.vue'

const props = defineProps<{
  kind: WakeKind
  modelValue: string
  /** 带上：预览用这个会话此刻的真实现状；不带：用示例值。 */
  workflowId?: string
  /**
   * 「已保存的状态下，这一类实际用的是哪一层」：设置页由本地算（全局覆盖 / 内置默认）；
   * 不传则向服务端问一次（会话页：会话模板 / 全局覆盖 / 内置默认）。
   */
  savedLayer?: string
  /** 父组件在保存成功后加一，让「已保存状态用哪一层」重新问一次。 */
  refreshKey?: number
  /** 给 data-testid 用的前缀，如 wake-resume。 */
  testid: string
  /** 留空时的说明（出现在文本框占位符里）。 */
  emptyHint: string
}>()
const emit = defineEmits<{ (e: 'update:modelValue', v: string): void }>()

const store = useWakePromptsStore()
void store.ensure()

const info = computed(() => store.info(props.kind))
const vars = computed(() => store.varsFor(props.kind))
const max = computed(() => store.max)

const area = ref<HTMLTextAreaElement | null>(null)

const count = computed(() => wakeLength(props.modelValue))
const problem = computed(() => wakeTemplateProblem(props.kind, props.modelValue, max.value))
const empty = computed(() => props.modelValue.trim() === '')

// 已保存状态下实际用的来源
const askedLayer = ref('')
let layerSeq = 0
async function askSavedLayer() {
  if (props.savedLayer !== undefined) return
  const mine = ++layerSeq
  try {
    const res = await wakePromptsApi.preview({ kind: props.kind, workflow_id: props.workflowId || undefined })
    if (mine === layerSeq) askedLayer.value = res.layer
  } catch {
    if (mine === layerSeq) askedLayer.value = ''
  }
}
const effectiveLayer = computed(() => props.savedLayer ?? askedLayer.value)
watch(() => [props.kind, props.workflowId, props.refreshKey], () => void askSavedLayer(), { immediate: true })
onBeforeUnmount(() => {
  layerSeq++
})

function onInput(e: Event) {
  emit('update:modelValue', (e.target as HTMLTextAreaElement).value)
}

async function insertVar(name: string) {
  const el = area.value
  const cur = props.modelValue
  const a = el ? el.selectionStart : cur.length
  const b = el ? el.selectionEnd : cur.length
  const res = insertVarAt(cur, a, b, name)
  emit('update:modelValue', res.text)
  await nextTick()
  if (area.value) {
    area.value.focus()
    area.value.setSelectionRange(res.caret, res.caret)
  }
}

function useBuiltin() {
  const t = info.value?.template ?? ''
  if (t) emit('update:modelValue', t)
}

function clearDraft() {
  emit('update:modelValue', '')
}

const layerTitle = computed(() => wakeLayerHint(effectiveLayer.value))
</script>

<template>
  <div class="space-y-2 font-mono" :data-testid="testid" :data-kind="kind">
    <div class="flex flex-wrap items-center gap-2">
      <span class="text-xs font-semibold text-foreground">{{ info?.label ?? wakeKindLabel(kind) }}</span>
      <span
        v-if="effectiveLayer"
        class="text-[10px] px-1.5 py-px rounded-xs border"
        :class="effectiveLayer === 'session' || effectiveLayer === 'global' ? 'border-primary/40 bg-primary/10 text-primary' : 'border-border/70 text-muted-foreground'"
        :title="layerTitle"
        :data-testid="`${testid}-layer`"
        :data-layer="effectiveLayer"
      >当前生效：{{ wakeLayerLabel(effectiveLayer) }}</span>
    </div>
    <p v-if="info?.desc" class="text-[11px] text-muted-foreground leading-relaxed">{{ info.desc }}</p>
    <p v-else-if="store.error" class="text-[11px] text-destructive" :data-testid="`${testid}-defaults-error`">无法读取内置默认与变量表：{{ store.error }}</p>

    <textarea
      ref="area"
      :value="modelValue"
      :data-testid="`${testid}-text`"
      :aria-label="`${info?.label ?? wakeKindLabel(kind)}的唤醒词模板`"
      rows="7"
      spellcheck="false"
      :placeholder="emptyHint"
      class="w-full px-2.5 py-2 text-xs bg-background border border-border/80 rounded-xs focus:outline-none focus:border-primary text-foreground resize-y leading-relaxed min-h-[7rem] max-h-[40vh]"
      :class="problem ? 'border-destructive focus:border-destructive' : ''"
      :aria-invalid="!!problem"
      @input="onInput"
    />
    <div class="flex flex-wrap items-center gap-2 text-[11px]">
      <span
        class="tabular-nums"
        :class="count > max ? 'text-destructive font-semibold' : 'text-muted-foreground'"
        :data-testid="`${testid}-count`"
      >{{ count }} / {{ max }}</span>
      <span v-if="problem" class="text-destructive" :data-testid="`${testid}-problem`">{{ problem }}</span>
      <span class="flex-1" />
      <button
        type="button"
        :data-testid="`${testid}-use-builtin`"
        :disabled="!info?.template"
        class="px-2 h-6 rounded-xs border border-border/80 text-[11px] text-muted-foreground hover:text-foreground hover:bg-muted cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed"
        title="把内置默认文字放进文本框，在它的基础上改（保存才生效）"
        @click="useBuiltin"
      >以内置默认为底稿</button>
      <button
        type="button"
        :data-testid="`${testid}-clear`"
        :disabled="empty"
        class="px-2 h-6 rounded-xs border border-border/80 text-[11px] text-muted-foreground hover:text-destructive hover:bg-muted cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed"
        title="清空文本框；保存后这一类回到下一层（全局覆盖或内置默认）"
        @click="clearDraft"
      >清空</button>
    </div>

    <div v-if="vars.length" class="flex flex-wrap items-center gap-1">
      <span class="text-[11px] text-muted-foreground mr-1">点一下在光标处插入变量（带色的是发出那一刻现算的现状）</span>
      <button
        v-for="v in vars"
        :key="v.name"
        type="button"
        :data-testid="`${testid}-var-${v.name}`"
        :title="v.desc + (v.live ? '（发出那一刻由 Hub 现算）' : '')"
        class="px-1.5 h-5 rounded-xs border text-[10px] cursor-pointer transition-colors"
        :class="v.live ? 'border-primary/30 text-primary/90 hover:bg-primary/10' : 'border-border/80 text-muted-foreground hover:text-foreground hover:bg-muted'"
        @mousedown.prevent
        @click="insertVar(v.name)"
      >%{{ v.name }}</button>
    </div>

    <WakePreview :kind="kind" :template="modelValue" :workflow-id="workflowId" :testid="testid" />
  </div>
</template>
