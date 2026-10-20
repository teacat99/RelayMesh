import { ref } from 'vue'

/**
 * 正文里点「会话号」（sess-xxxxxxxx）要在当前会话页里滚到那一轮：正文组件藏得很深、拿不到消息列表的引用，
 * 所以用这个单例把请求交给会话页（FeedbackView 监听它，再调用消息列表的 scrollToSession）。
 * tick 每次递增，同一轮连点两次也会各触发一次。
 */
export interface SessionJumpRequest {
  workflowId: string
  sessionId: string
  tick: number
}

export const sessionJumpRequest = ref<SessionJumpRequest | null>(null)

let tick = 0

export function requestSessionJump(workflowId: string, sessionId: string): void {
  sessionJumpRequest.value = { workflowId, sessionId, tick: ++tick }
}
