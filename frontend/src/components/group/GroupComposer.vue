<script setup lang="ts">
// 群聊输入框（#165 第二步，取代旧的聊天输入框）：Enter 发送、Shift+Enter 换行（输入法组字时 Enter 只是上屏）、
// 输入 @ 弹出成员补全。文字草稿按群存在本机，切走再回来不丢；发送失败时输入原样保留。
// 群聊不支持图片、「作为追加」与引用回复（引用只属于已经删除的单会话聊天窗口，群里的消息编号是 m:…，从来没有「引用」入口），
// 这里也就没有这三项。
// 外观与会话页输入区（FeedbackInputDock）对齐（#241 第四步，只动外观与提示、不动功能）：底面与左右留白同一套，
// 输入框下面是一行操作行——左边是快捷键提示（宽屏才显示，和会话页一样），右边是带字的小号「发送」按钮。
// 会话页独有的草稿箱、上传截图、语音、快捷预设、拖拽调高、引用条都是功能，群聊没有，这里也不加。
// 顺带去掉一处外观毛病：输入框高度算漏了上下边框，一行高的空输入框右边会冒出一根多余的滚动条（fit() 里补回，没到上限不出滚动条）。
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { Send } from 'lucide-vue-next'
import { isImeKey } from '../../lib/ime'
import { extractMentions, filterCandidates, insertMention, mentionQuery, type MentionCandidate } from '../../lib/group'
import type { SenderIdentity } from '../../lib/groupCard'
import Button from '../ui/button/Button.vue'
import MemberAvatar from './MemberAvatar.vue'

const props = defineProps<{
  /** 群的会话键（grp:…）：草稿按它存（键 rm-chat-draft:<grp:…>，用户已有的草稿不丢）。 */
  workflowId: string
  sending: boolean
  /** @ 候选（在职 Agent + 所有人）。 */
  candidates: MentionCandidate[]
  /** 候选的头像身份（键 = 候选 ref）：在选项前画成员头像。 */
  identities: Record<string, SenderIdentity>
  /** 不能发送（群已归档），disabledReason 显示在输入框上方。 */
  disabled?: boolean
  disabledReason?: string
}>()
const emit = defineEmits<{
  (e: 'send', payload: { text: string; mentions: string[] }): void
}>()

const draftKey = computed(() => `rm-chat-draft:${props.workflowId}`)
const text = ref('')
const area = ref<HTMLTextAreaElement | null>(null)

function loadDraft() {
  try {
    text.value = localStorage.getItem(draftKey.value) ?? ''
  } catch {
    text.value = ''
  }
  void nextTick(fit)
}
function saveDraft() {
  try {
    if (text.value) localStorage.setItem(draftKey.value, text.value)
    else localStorage.removeItem(draftKey.value)
  } catch {
    // 存储不可用时草稿只留在内存里
  }
}
watch(() => props.workflowId, loadDraft)
watch(text, () => {
  saveDraft()
  void nextTick(fit)
})
onMounted(loadDraft)

/** 输入框随内容长高，最多约 8 行（与样式里的 max-h-42 同一个数）。 */
const MAX_HEIGHT = 168
function fit() {
  const el = area.value
  if (!el) return
  el.style.height = 'auto'
  // scrollHeight 不含边框，而盒子是 border-box：不把上下两条边框补回去，一行高的空输入框也会差 2px，右边冒出一根滚动条
  const want = el.scrollHeight + (el.offsetHeight - el.clientHeight)
  el.style.height = `${Math.min(want, MAX_HEIGHT)}px`
  // 没长到上限就不出滚动条（零点几像素的取整误差也不该冒出来）；到了上限才让它滚
  el.style.overflowY = want > MAX_HEIGHT ? 'auto' : 'hidden'
}

const canSend = computed(() => !props.disabled && !props.sending && text.value.trim().length > 0)

function submit() {
  if (!canSend.value) return
  const mentions = extractMentions(text.value, props.candidates)
  emit('send', { text: text.value.trim(), mentions })
}

// ---- @ 补全：光标前是「@查询」时弹出候选，上下键选择、Enter / Tab 插入、Esc 关闭 ----
const caret = ref(0)
const pickIndex = ref(0)
const dismissedAt = ref(-1)
const query = computed(() => mentionQuery(text.value, caret.value))
const options = computed(() => {
  const q = query.value
  if (!q || dismissedAt.value === q.start) return []
  return filterCandidates(props.candidates, q.query)
})
const pickerOpen = computed(() => options.value.length > 0)
watch(options, () => {
  pickIndex.value = 0
})
function syncCaret() {
  caret.value = area.value?.selectionStart ?? text.value.length
}
function pick(c: MentionCandidate) {
  const q = query.value
  if (!q) return
  const r = insertMention(text.value, caret.value, q.start, c.name)
  text.value = r.text
  void nextTick(() => {
    area.value?.focus()
    area.value?.setSelectionRange(r.caret, r.caret)
    caret.value = r.caret
  })
}

/** 父组件在发送成功后调用：清掉输入。 */
function reset() {
  text.value = ''
  saveDraft()
  void nextTick(fit)
}
defineExpose({ reset })

function onKeydown(e: KeyboardEvent) {
  if (pickerOpen.value && !isImeKey(e)) {
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault()
      const n = options.value.length
      pickIndex.value = (pickIndex.value + (e.key === 'ArrowDown' ? 1 : n - 1)) % n
      return
    }
    if (e.key === 'Enter' || e.key === 'Tab') {
      e.preventDefault()
      pick(options.value[pickIndex.value])
      return
    }
    if (e.key === 'Escape') {
      e.preventDefault()
      dismissedAt.value = query.value?.start ?? -1
      return
    }
  }
  if (e.key !== 'Enter' || e.shiftKey || e.altKey) return
  if (isImeKey(e)) return
  e.preventDefault()
  submit()
}
</script>

<template>
  <!-- 底面与左右留白同会话页输入区：半透明底 + 模糊、分隔线 border-border/70、手机 px-3 / sm 起 px-6 -->
  <div
    class="relative border-t border-border/70 bg-background/95 px-3 pb-[max(0.5rem,env(safe-area-inset-bottom))] pt-2 backdrop-blur-xs sm:px-6"
    data-testid="chat-composer"
  >
    <!-- @ 补全（左右边与输入框对齐，所以跟着留白走） -->
    <ul
      v-if="pickerOpen"
      class="absolute inset-x-3 bottom-full z-10 mb-1 max-h-48 overflow-y-auto rounded-md border border-border bg-popover p-1 text-[12px] shadow-md sm:inset-x-6"
      role="listbox"
      data-testid="mention-picker"
    >
      <li v-for="(c, i) in options" :key="c.ref">
        <button
          type="button"
          class="flex w-full cursor-pointer items-center gap-2 rounded px-2 py-1 text-left"
          :class="i === pickIndex ? 'bg-accent text-accent-foreground' : 'hover:bg-accent/60'"
          role="option"
          :aria-selected="i === pickIndex"
          data-testid="mention-option"
          :data-ref="c.ref"
          @mousedown.prevent="pick(c)"
        >
          <MemberAvatar v-if="identities[c.ref]" :identity="identities[c.ref]" />
          <span class="font-medium">@{{ c.name }}</span>
          <span v-if="c.ref !== 'all'" class="truncate font-mono text-[10px] text-muted-foreground">{{ c.ref }}</span>
          <span v-else class="text-[10px] text-muted-foreground">群里所有成员</span>
        </button>
      </li>
    </ul>

    <div v-if="disabled && disabledReason" class="mb-1.5 rounded-md bg-muted/60 px-2 py-1 text-[11px] text-muted-foreground" data-testid="chat-disabled">{{ disabledReason }}</div>

    <textarea
      ref="area"
      v-model="text"
      rows="1"
      class="block max-h-42 min-h-9 w-full resize-none rounded-lg border border-input bg-card px-3 py-2 text-[13px] leading-relaxed outline-none transition-colors placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-1 focus-visible:ring-ring"
      placeholder="发消息到群里，输入 @ 点名成员…"
      :disabled="disabled"
      data-testid="chat-input"
      @keydown="onKeydown"
      @keyup="syncCaret"
      @click="syncCaret"
      @input="syncCaret"
    />

    <!-- 操作行（与会话页输入区同一种摆法）：左边快捷键提示（宽屏才显示，手机没有这些键），右边「发送」 -->
    <div class="mt-1.5 flex items-center justify-between gap-2 text-xs text-muted-foreground">
      <span class="hidden text-[11px] text-muted-foreground/80 lg:inline" data-testid="chat-hint">
        按 <kbd class="rounded-xs border border-border bg-muted px-1.5 py-0.5 font-mono text-[10px]">Enter</kbd> 发送，
        <kbd class="rounded-xs border border-border bg-muted px-1.5 py-0.5 font-mono text-[10px]">Shift + Enter</kbd> 换行
      </span>
      <Button
        class="ml-auto h-7 shrink-0 cursor-pointer gap-1 rounded-sm px-3 text-xs font-medium sm:px-3.5"
        :disabled="!canSend"
        title="发送（Enter）"
        data-testid="chat-send"
        @click="submit"
      >
        <Send class="size-3" />
        <span>{{ sending ? '发送中…' : '发送' }}</span>
      </Button>
    </div>
  </div>
</template>
