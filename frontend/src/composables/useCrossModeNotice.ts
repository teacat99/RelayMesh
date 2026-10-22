import { watch } from 'vue'
import { useRouter } from 'vue-router'
import { toast } from 'vue-sonner'
import { relayNoticeId, relayRequestNotice } from '../lib/crossNotice'
import { useModeStore } from '../stores/mode'
import { useNotifyStore } from '../stores/notify'
import { useSessionStore } from '../stores/session'
import { useAppMode } from './useAppMode'

/**
 * 人待在 ExecMesh 里时，RelayMesh 来了新的反馈请求：除了声音与桌面通知（它们对两个项目一视同仁，
 * 由通知仓库就地处理），再弹一条带「查看」入口的消息——ExecMesh 的界面上没有 RelayMesh 的会话列表，
 * 不弹的话只剩切换器上的一个小圆点，很容易错过。
 * 点「查看」回到 RelayMesh，并选中这条请求所在的会话。
 */
export function useCrossModeNotice() {
  const router = useRouter()
  const mode = useAppMode()
  const modeStore = useModeStore()
  const notify = useNotifyStore()
  const sessions = useSessionStore()

  function openRequest(sessionId: string) {
    const target = sessions.sessions.find(s => s.session_id === sessionId)
      ?? (sessions.currentSession?.session_id === sessionId ? sessions.currentSession : null)
    if (target) sessions.selectSession(target)
    void router.push(modeStore.targetOf('relay'))
  }

  watch(
    () => notify.lastRequest,
    req => {
      if (!req) return
      const notice = relayRequestNotice(mode.value, req, Date.now())
      if (!notice) return
      toast.info(notice.title, {
        id: relayNoticeId(req.sessionId),
        description: notice.description,
        duration: notice.durationMs,
        action: { label: notice.action, onClick: () => openRequest(req.sessionId) },
      })
    },
  )
}
