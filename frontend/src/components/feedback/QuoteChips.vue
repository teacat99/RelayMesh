<script setup lang="ts">
// 输入框上方的引用条（#118）：把文字里的「【回复 …】」「【恢复 …】」「【确认 …】」标记显示成现代的引用条目，
// 带被引用内容的标题 / 开头，点 × 移除（同时从文字里去掉标记）。标记本身保持不变，Hub 照旧按它解析。
import { computed } from 'vue'
import { CornerUpLeft, Archive, BookCheck, X } from 'lucide-vue-next'
import { useSessionStore } from '../../stores/session'
import { useConfirmStore } from '../../stores/confirms'
import { confirmExcerptFor, parseQuoteChips, quoteExcerptFor, removeQuoteToken } from '../../lib/quoteChips'

const props = defineProps<{ text: string }>()
const emit = defineEmits<{ (e: 'update:text', v: string): void }>()

const sessionStore = useSessionStore()
const confirmStore = useConfirmStore()
const chips = computed(() => parseQuoteChips(props.text))
type Chip = ReturnType<typeof parseQuoteChips>[number]
function label(chip: Chip): string {
  if (chip.kind === 'capsule') return `恢复胶囊 ${chip.ref}`
  if (chip.kind === 'confirm') {
    const title = confirmExcerptFor(chip, confirmStore.timelineItems)
    return title ? `确认 ${chip.ref}：${title}` : `确认 ${chip.ref}`
  }
  return quoteExcerptFor(chip, sessionStore.sessions as any) || `回复 ${chip.ref}`
}
const ICONS = { reply: CornerUpLeft, capsule: Archive, confirm: BookCheck } as const
</script>

<template>
  <div v-if="chips.length" class="flex flex-wrap gap-1.5 px-1 pb-1" data-testid="quote-chips">
    <div
      v-for="chip in chips"
      :key="chip.token"
      class="flex max-w-full items-center gap-1.5 rounded-md border-l-2 border-primary/60 bg-muted/50 px-2 py-1 text-[11px]"
      data-testid="quote-chip"
      :data-token="chip.token"
      :data-kind="chip.kind"
    >
      <component :is="ICONS[chip.kind]" class="size-3 shrink-0 text-primary" />
      <span class="min-w-0 truncate text-muted-foreground">引用：{{ label(chip) }}</span>
      <button
        type="button"
        class="shrink-0 cursor-pointer text-muted-foreground hover:text-foreground"
        title="移除这条引用"
        data-testid="quote-chip-remove"
        @click="emit('update:text', removeQuoteToken(text, chip.token))"
      >
        <X class="size-3" />
      </button>
    </div>
  </div>
</template>
