import { defineStore } from 'pinia'
import { ref } from 'vue'

// 会话页上各处的「引用」按钮（轮次头、进度气泡、恢复胶囊）与输入框之间的信道（#106）：
// 按钮只负责说「我要引用这个标记」，谁来插、插在哪、要不要聚焦由输入框决定。
// tick 每次递增，同一个标记连点两次也能再次触发（输入框据此把光标挪回标记后面）。
export const useReplyQuoteStore = defineStore('replyQuote', () => {
  const token = ref('')
  const tick = ref(0)

  function quote(next: string) {
    if (!next) return
    token.value = next
    tick.value += 1
  }

  return { token, tick, quote }
})
