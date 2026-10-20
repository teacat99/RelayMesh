// 「别处有几个会话在等我答复」的响应式版本：手机页头的顶条与汉堡角标共用（#156 批 1）。
// 只在会话页（路由 home）才有「正在看的会话」；在任务清单 / 群聊 / 项目页不排除任何一个。
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import { useSessionStore } from '../stores/session'
import { pickPending } from '../lib/pendingSessions'

export function usePendingSessions() {
  const route = useRoute()
  const sessionStore = useSessionStore()
  return computed(() => {
    const viewing = route.name === 'home' ? sessionStore.selectedSession || sessionStore.currentSession : null
    return pickPending(
      sessionStore.sessions,
      sessionStore.currentSession,
      viewing ? { workflowId: viewing.workflow_id, sessionId: viewing.session_id } : null,
    )
  })
}
