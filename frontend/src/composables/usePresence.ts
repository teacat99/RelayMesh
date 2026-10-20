// 用户在场信号（#121 收尾宽限）：
//   - 输入区一有输入就 noteTyping()；正在输入期间每 5 秒向服务端发一次心跳，服务端据此暂停这次挂起的倒计时；
//   - 停手 8 秒后视为空闲，倒计时从暂停处接着走；
//   - plus() 是「+1 分钟」按钮，每点一次多挂 60 秒。
// 状态是模块级单例：输入区（产生输入）与执行状态块（显示冻结的倒计时）共用同一份。
import { computed, ref } from 'vue'
import { useSessionStore } from '../stores/session'
import { asyncModeApi } from '../api/asyncMode'

const TYPING_IDLE_MS = 8000
const HEARTBEAT_MS = 5000

const lastInputAt = ref(0)
const clock = ref(Date.now())
let ticker: number | null = null
let lastBeat = 0

function ensureTicker() {
  if (typeof window === 'undefined' || ticker !== null) return
  ticker = window.setInterval(() => {
    clock.value = Date.now()
  }, 1000)
}

export function usePresence() {
  const sessionStore = useSessionStore()
  ensureTicker()

  /** 此刻是否在输入（最近 8 秒内有过输入）。 */
  const typing = computed(() => lastInputAt.value > 0 && clock.value - lastInputAt.value < TYPING_IDLE_MS)

  function sessionId(): string | undefined {
    const s = sessionStore.selectedSession || sessionStore.currentSession
    return s?.session_id
  }

  /** 异步对话生效时返回工作流 id：此时「我还在」作用于回复窗口，而不是某次挂起（#123）。 */
  function asyncWorkflow(): string | null {
    const m = sessionStore.asyncMode
    return m?.effective && m.workflow_id ? m.workflow_id : null
  }

  function applyWindow(wf: string, until: string | null) {
    const m = sessionStore.asyncMode
    if (m && m.workflow_id === wf) m.reply_window_until = until
  }

  /** 输入区调用：有按键 / 文本变化时。节流后发心跳。 */
  function noteTyping() {
    const now = Date.now()
    lastInputAt.value = now
    clock.value = now
    const wf = asyncWorkflow()
    if (wf) {
      if (now - lastBeat >= HEARTBEAT_MS) {
        lastBeat = now
        void asyncModeApi.replyWindow(wf, 'typing').then(r => applyWindow(wf, r.reply_window_until)).catch(() => {})
      }
      return
    }
    const id = sessionId()
    if (!id) return
    if (now - lastBeat >= HEARTBEAT_MS) {
      lastBeat = now
      void sessionStore.sendPresence(id, 'typing').catch(() => {})
    }
  }

  /** 点「+1 分钟」。返回是否真的有智能体在挂着（false 时按钮应提示没有可延的挂起）。 */
  async function plus(): Promise<boolean> {
    const wf = asyncWorkflow()
    if (wf) {
      const r = await asyncModeApi.replyWindow(wf, 'plus')
      applyWindow(wf, r.reply_window_until)
      return r.active
    }
    const id = sessionId()
    if (!id) return false
    const total = await sessionStore.sendPresence(id, 'plus')
    return total !== null
  }

  return { typing, noteTyping, plus }
}
