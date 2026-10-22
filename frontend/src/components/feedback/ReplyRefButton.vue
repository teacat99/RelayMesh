<script setup lang="ts">
// 「引用」按钮（#106）：点一下把这条回复 / 恢复胶囊的引用标记插进输入框，智能体读到时会附上被引用内容的开头。
import { Quote } from 'lucide-vue-next'
import { useReplyQuoteStore } from '../../stores/replyQuote'

const props = withDefaults(defineProps<{
  /** 要插入的标记，例如「【回复 9f02517c】」；为空（会话编号没有可用哈希）时不渲染。 */
  token: string
  /** 按钮上的文字；默认「引用」。 */
  label?: string
  /** 悬停说明；默认按标记生成。 */
  title?: string
}>(), { label: '引用', title: '' })

const quoteStore = useReplyQuoteStore()
</script>

<template>
  <button
    v-if="props.token"
    type="button"
    class="inline-flex shrink-0 cursor-pointer items-center gap-1 whitespace-nowrap rounded border border-border/60 px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground"
    data-testid="reply-ref-btn"
    :data-token="props.token"
    :title="props.title || `引用：在输入框插入 ${props.token}，智能体读到时会附上被引用内容的开头`"
    @click.stop="quoteStore.quote(props.token)"
  >
    <Quote class="size-2.5 shrink-0" />
    <span>{{ props.label }}</span>
  </button>
</template>
