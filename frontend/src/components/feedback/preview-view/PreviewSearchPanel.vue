<script setup lang="ts">
// 消息搜索面板（#120 阶段 E-c1；#241 起群聊窗口也用它）：浮在列表顶部。输入关键词 → 服务端子串搜索（覆盖整段历史）→ 点结果落到那条消息。
// 只负责「搜」与「列」：落点由宿主（预览列表的 jumpToHit、群聊窗口的 openResult）按命中消息认卡，这里只把命中交出去。
// 不碰任何 store，直接走会话接口：GET /conversations/<wf:工作流 | grp:群>/messages?q=。
// workflowId 就是会话键：工作流会话传工作流编号，群聊传 grp:…（conversationIdOf 对 grp: 原样放行）。
// 群聊没有「汇报 / 提问」这些种类（群消息只有文字与系统行；服务端搜群消息只看文字，不看 kinds 参数），
// 所以群聊宿主传 kinds=[]：不带这个参数，免得把工作流会话的种类词表带进群聊请求（默认只搜有对应卡片的种类）。
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Loader2, Search, X } from 'lucide-vue-next'
import { chatErrorMessage, conversationIdOf, conversationsApi, type ChatMessage } from '../../../api/conversations'
import { SEARCH_KINDS, SEARCH_LIMIT, hitKindLabel, hitSnippet, hitWhen } from '../../../lib/previewSearch'

const props = withDefaults(
  defineProps<{
    workflowId: string
    /** 输入框的占位文字；默认是工作流会话的说法 */
    placeholder?: string
    /** 只搜这些种类；空数组 = 不过滤 */
    kinds?: readonly string[]
  }>(),
  { placeholder: '搜索这个工作流的全部消息（汇报 / 回复 / 追加 / 提问 / 任务进展）', kinds: () => SEARCH_KINDS },
)
const emit = defineEmits<{ close: []; pick: [hit: ChatMessage] }>()

const inputEl = ref<HTMLInputElement | null>(null)
const query = ref('')
/** 当前这批结果是用哪个关键词搜出来的（输入框里的字已经变了、新结果还没回来时，旧结果仍按旧词标注） */
const resultQuery = ref('')
const results = ref<ChatMessage[]>([])
const searching = ref(false)
const truncated = ref(false)
const error = ref('')
const active = ref(0)
/** 点中某条结果后把结果列表收起来（只留输入条），让目标卡露出来；再点输入条、敲方向键或改关键词就重新展开 */
const collapsed = ref(false)
/** 结果行的「今天 / 昨天」按搜索那一刻算 */
const asOf = ref(new Date())

const trimmed = computed(() => query.value.trim())
let timer: ReturnType<typeof setTimeout> | null = null
let seq = 0

watch(trimmed, q => {
  if (timer) clearTimeout(timer)
  const mine = ++seq
  collapsed.value = false
  if (!q) {
    // 清空输入：结果一并清掉
    results.value = []
    resultQuery.value = ''
    truncated.value = false
    error.value = ''
    searching.value = false
    active.value = 0
    return
  }
  searching.value = true
  timer = setTimeout(async () => {
    try {
      const page = await conversationsApi.list(conversationIdOf(props.workflowId), { limit: SEARCH_LIMIT, q, kinds: [...props.kinds] })
      if (mine !== seq) return // 晚到的旧响应丢弃
      // 接口按时间升序给最新的一批，面板里新的在上
      results.value = [...page.items].reverse()
      resultQuery.value = q
      truncated.value = page.has_more_before
      error.value = ''
      active.value = 0
      asOf.value = new Date()
    } catch (e) {
      if (mine === seq) error.value = chatErrorMessage(e, '搜索失败')
    } finally {
      if (mine === seq) searching.value = false
    }
  }, 300)
})

function pick(hit: ChatMessage) {
  collapsed.value = true
  inputEl.value?.blur() // 手机上顺手收起软键盘，别盖住落点
  emit('pick', hit)
}

function move(delta: number) {
  const n = results.value.length
  if (!n) return
  collapsed.value = false
  active.value = (active.value + delta + n) % n
  void nextTick(() => document.querySelector('[data-testid="preview-search-hit"][data-active="true"]')?.scrollIntoView({ block: 'nearest' }))
}

function onKey(e: KeyboardEvent) {
  // 输入法组字过程中的回车 / 方向键是选字用的，不是「选中结果」
  if (e.isComposing || e.keyCode === 229) return
  if (e.key === 'ArrowDown') {
    e.preventDefault()
    move(1)
  } else if (e.key === 'ArrowUp') {
    e.preventDefault()
    move(-1)
  } else if (e.key === 'Enter') {
    e.preventDefault()
    const hit = results.value[active.value]
    if (hit && !collapsed.value) pick(hit)
  }
}

function onWindowKey(e: KeyboardEvent) {
  if (e.key === 'Escape') emit('close')
}

onMounted(() => {
  window.addEventListener('keydown', onWindowKey)
  void nextTick(() => inputEl.value?.focus())
})
onBeforeUnmount(() => {
  window.removeEventListener('keydown', onWindowKey)
  if (timer) clearTimeout(timer)
  seq++ // 面板关了，在路上的响应不再落地
})
</script>

<template>
  <div
    class="absolute inset-x-2 top-2 z-30 mx-auto max-w-2xl rounded-lg border border-border bg-card/95 shadow-float backdrop-blur-sm sm:inset-x-8"
    role="dialog"
    aria-label="搜索消息"
    data-testid="preview-search"
  >
    <div class="flex items-center gap-2 px-3 py-2">
      <Search class="size-4 shrink-0 text-muted-foreground" />
      <input
        ref="inputEl"
        v-model="query"
        type="text"
        enterkeyhint="search"
        autocomplete="off"
        spellcheck="false"
        :disabled="!props.workflowId"
        :placeholder="props.workflowId ? props.placeholder : '这个会话没有工作流编号，无法搜索'"
        class="min-w-0 flex-1 bg-transparent text-[16px] outline-none placeholder:text-muted-foreground/70 sm:text-[13px]"
        data-testid="preview-search-input"
        @keydown="onKey"
        @click="collapsed = false"
      />
      <Loader2 v-if="searching" class="size-3.5 shrink-0 animate-spin text-muted-foreground" data-testid="preview-search-loading" />
      <button
        type="button"
        class="cursor-pointer text-muted-foreground hover:text-foreground"
        aria-label="关闭搜索"
        data-testid="preview-search-close"
        @click="emit('close')"
      >
        <X class="size-4" />
      </button>
    </div>
    <ul
      v-if="trimmed && !collapsed"
      class="max-h-[min(60vh,420px)] overflow-y-auto border-t border-border/60 py-1"
      data-testid="preview-search-results"
    >
      <li v-if="error" class="px-3 py-2 text-[12px] text-destructive" data-testid="preview-search-error">{{ error }}</li>
      <li v-else-if="!searching && !results.length" class="px-3 py-2 text-[12px] text-muted-foreground" data-testid="preview-search-empty">
        没有匹配的消息
      </li>
      <li v-for="(m, i) in results" :key="m.id">
        <button
          type="button"
          class="flex w-full cursor-pointer items-start gap-2 px-3 py-1.5 text-left text-[12px] hover:bg-accent"
          :class="i === active ? 'bg-accent' : ''"
          data-testid="preview-search-hit"
          :data-hit-id="m.id"
          :data-hit-source="m.source?.type"
          :data-active="i === active"
          @mouseenter="active = i"
          @click="pick(m)"
        >
          <span class="mt-px max-w-[8rem] shrink-0 truncate rounded-full bg-muted px-1.5 py-0.5 text-[10px] font-medium text-muted-foreground" data-testid="preview-search-hit-label">{{ hitKindLabel(m) }}</span>
          <span class="min-w-0 flex-1 break-words">
            <template v-for="(part, k) in hitSnippet(m, resultQuery)" :key="k"><mark v-if="part.hit" class="rounded-sm bg-primary/20 px-px text-foreground">{{ part.text }}</mark><template v-else>{{ part.text }}</template></template>
          </span>
          <span class="shrink-0 whitespace-nowrap text-[10px] text-muted-foreground">{{ hitWhen(m, asOf) }}</span>
        </button>
      </li>
      <li v-if="truncated" class="px-3 py-1.5 text-[11px] text-muted-foreground" data-testid="preview-search-more">
        只显示最新的 {{ SEARCH_LIMIT }} 条匹配，换个更具体的关键词可以缩小范围
      </li>
    </ul>
  </div>
</template>
