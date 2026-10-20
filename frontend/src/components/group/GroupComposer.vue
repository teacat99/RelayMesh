<script setup lang="ts">
// 群聊输入框（#165 第二步，取代旧的聊天输入框）：Enter 发送、Shift+Enter 换行（输入法组字时 Enter 只是上屏）、
// 输入 @ 弹出成员补全。文字草稿按群存在本机，切走再回来不丢；发送失败时输入原样保留。
// 群聊不支持图片、「作为追加」与引用回复（引用只属于已经删除的单会话聊天窗口，群里的消息编号是 m:…，从来没有「引用」入口），
// 这里也就没有这三项。
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { SendHorizontal } from 'lucide-vue-next'
import { isImeKey } from '../../lib/ime'
import { extractMentions, filterCandidates, insertMention, mentionQuery, type MentionCandidate } from '../../lib/group'
import type { SenderIdentity } from '../../lib/groupCard'
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

/** 输入框随内容长高，最多约 8 行。 */
function fit() {
  const el = area.value
  if (!el) return
  el.style.height = 'auto'
  el.style.height = `${Math.min(el.scrollHeight, 168)}px`
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
  <div class="relative border-t border-border bg-background px-3 pb-[max(0.5rem,env(safe-area-inset-bottom))] pt-2" data-testid="chat-composer">
    <!-- @ 补全 -->
    <ul
      v-if="pickerOpen"
      class="absolute inset-x-3 bottom-full z-10 mb-1 max-h-48 overflow-y-auto rounded-md border border-border bg-popover p-1 text-[12px] shadow-md"
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

    <div class="flex items-end gap-2">
      <textarea
        ref="area"
        v-model="text"
        rows="1"
        class="max-h-42 min-h-9 flex-1 resize-none rounded-lg border border-input bg-card px-3 py-2 text-[13px] leading-relaxed outline-none transition-colors placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-1 focus-visible:ring-ring"
        placeholder="发消息到群里，输入 @ 点名成员…"
        :disabled="disabled"
        data-testid="chat-input"
        @keydown="onKeydown"
        @keyup="syncCaret"
        @click="syncCaret"
        @input="syncCaret"
      />

      <button
        type="button"
        class="mb-0.5 flex size-9 shrink-0 cursor-pointer items-center justify-center rounded-full bg-primary text-primary-foreground transition-opacity disabled:cursor-not-allowed disabled:opacity-40"
        :disabled="!canSend"
        title="发送（Enter）"
        data-testid="chat-send"
        @click="submit"
      >
        <SendHorizontal class="size-4" />
      </button>
    </div>
  </div>
</template>
