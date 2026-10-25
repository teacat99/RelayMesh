<script setup lang="ts">
// 唤醒词预览（#232 D3，D-376）：「预览最终发出去的文字」折叠块。
// 文字由服务端用真正发送时的同一套渲染器渲染（分层选用、变量现算、清洗与压成一行都一样），这里只负责展示。
// 带 workflowId：变量按这个会话此刻的真实现状渲染；不带：用示例值。
// 草稿为空时不带模板：显示「留空时实际发出的」那一份，并标明它来自哪一层。
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useDebounceFn } from '@vueuse/core'
import { Eye, EyeOff } from 'lucide-vue-next'
import { wakeErrorMessage, wakePromptsApi, type WakeKind, type WakePreview } from '@/api/wakePrompts'
import { useWakePromptsStore } from '@/stores/wakePrompts'
import { wakeFlattenNote, wakeLayerLabel } from '@/lib/wakePrompts'

const props = withDefaults(
  defineProps<{
    kind: WakeKind
    /** 草稿文字；空 = 渲染「留空时实际发出的」那一份。 */
    template: string
    /** template 是启动器预设（或会话页运行配置）里自己写的正文，不是某一层的模板草稿。 */
    preset?: boolean
    /** 带上：按这个会话此刻的真实现状渲染；不带：用示例值。 */
    workflowId?: string
    /** data-testid 前缀：{testid}-preview-toggle / -preview-box / -preview-error / -preview-layer / -preview。 */
    testid: string
    openByDefault?: boolean
    /** 追加的一段说明（例如启动器：内置默认之后运行时还会补一句最长运行时间）。 */
    note?: string
  }>(),
  { openByDefault: true, preset: false, workflowId: '', note: '' },
)

const store = useWakePromptsStore()

const open = ref(props.openByDefault)
const preview = ref<WakePreview | null>(null)
const error = ref('')
let seq = 0

const empty = computed(() => props.template.trim() === '')

async function fetchPreview() {
  const mine = ++seq
  try {
    const res = await wakePromptsApi.preview({
      kind: props.kind,
      workflow_id: props.workflowId.trim() || undefined,
      // 草稿为空：不带模板，让服务端渲染「留空时实际发出的」那一份
      template: empty.value ? undefined : props.template,
      preset: !empty.value && props.preset ? true : undefined,
    })
    if (mine !== seq) return
    preview.value = res
    error.value = ''
  } catch (err) {
    if (mine !== seq) return
    preview.value = null
    error.value = wakeErrorMessage(err, '无法生成预览')
  }
}
const refresh = useDebounceFn(() => void fetchPreview(), 300)

watch(
  () => [props.template, props.kind, props.workflowId, props.preset] as const,
  () => {
    if (open.value) void refresh()
  },
)
watch(
  open,
  on => {
    if (on) void fetchPreview()
  },
  { immediate: true },
)
// 变量表 / 内置默认晚到（首次打开页面）：空草稿时预览显示的就是内置默认，到了再刷新一次
watch(
  () => store.defaults,
  () => {
    if (open.value && empty.value) void fetchPreview()
  },
)
onBeforeUnmount(() => {
  seq++
})

const flattenNote = computed(() => (props.preset ? '' : wakeFlattenNote(props.kind)))
</script>

<template>
  <div class="rounded-xs border border-border/60">
    <button
      type="button"
      :data-testid="`${testid}-preview-toggle`"
      class="flex w-full items-center gap-1.5 px-2.5 py-1.5 text-left text-[11px] text-muted-foreground hover:text-foreground cursor-pointer"
      :aria-expanded="open"
      @click="open = !open"
    >
      <EyeOff v-if="open" class="w-3 h-3" />
      <Eye v-else class="w-3 h-3" />
      <span>{{ open ? '收起预览' : '预览最终发出去的文字' }}</span>
    </button>
    <div v-if="open" class="border-t border-border/50 px-2.5 py-2 space-y-1.5" :data-testid="`${testid}-preview-box`">
      <p v-if="error" class="text-[11px] text-destructive" :data-testid="`${testid}-preview-error`">{{ error }}</p>
      <template v-else-if="preview">
        <div class="flex flex-wrap items-center gap-1.5 text-[10px] text-muted-foreground">
          <span class="px-1.5 py-px rounded-xs border border-border/70" :data-testid="`${testid}-preview-layer`" :data-layer="preview.layer">
            {{ empty ? `留空时发出的是：${wakeLayerLabel(preview.layer)}` : preset ? '下面是这段正文渲染的结果' : '下面是这份草稿渲染的结果' }}
          </span>
          <span class="tabular-nums">{{ preview.length }} 字</span>
          <span v-if="preview.sample">变量用的是示例值，真正发出时按当时的现状换成实际值</span>
          <span v-else>变量按这个会话此刻的现状渲染</span>
        </div>
        <pre
          class="p-2 rounded-xs border border-border/70 bg-muted/40 text-[11px] leading-relaxed whitespace-pre-wrap break-words max-h-56 overflow-y-auto"
          :data-testid="`${testid}-preview`"
        >{{ preview.text }}</pre>
        <p v-if="flattenNote" class="text-[10px] text-muted-foreground leading-relaxed">{{ flattenNote }}</p>
        <p v-if="note" class="text-[10px] text-muted-foreground leading-relaxed">{{ note }}</p>
      </template>
      <p v-else class="text-[11px] text-muted-foreground">生成中…</p>
    </div>
  </div>
</template>
