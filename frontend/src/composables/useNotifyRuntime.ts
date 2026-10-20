import { onBeforeUnmount, onMounted, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { deepLinkTarget, isDeepLinkPending, resolveDeepLinkTarget, viewedConversation } from '../lib/notifyBus'
import { useAuthStore } from '../stores/auth'
import { useConfirmStore } from '../stores/confirms'
import { useNotifyBusStore } from '../stores/notifyBus'
import { useSessionStore } from '../stores/session'

/**
 * 通知总线的运行时接线（#115 M1.4）：
 * - 启动通知总线（多标签协调、聚焦 / 可见性监听），点击通知或页内提示时用路由跳转；
 * - 告诉总线「这个标签页正在看哪个会话」，用于前台抑制（正看着的会话不再重复打扰）；
 * - 处理站内深链 /?wf=<工作流>&s=<会话>（点击系统通知、点击 ntfy 推送都会落到这里）：选中对应会话，再把地址里的参数去掉；
 *   /?wf=<工作流>&q=Q23（正文里的确认项引用在新标签页打开时的地址）在此基础上等确认项加载完再点名那张卡。
 */
export function useNotifyRuntime() {
  const route = useRoute()
  const router = useRouter()
  const bus = useNotifyBusStore()
  const sessions = useSessionStore()
  const confirms = useConfirmStore()
  const auth = useAuthStore()

  onMounted(() =>
    bus.start({
      navigate: path => {
        void router.push(path)
      },
    }),
  )
  onBeforeUnmount(() => bus.stop())

  // 与会话页头显示的是同一个会话：选中的优先，没选时看当前待答的那个
  watch(
    () => [route.name, (sessions.selectedSession || sessions.currentSession)?.workflow_id, route.params.groupId] as const,
    ([name, workflowId, groupId]) => bus.setConversation(viewedConversation(name, workflowId, groupId)),
    { immediate: true },
  )

  let opening = false
  async function openDeepLink() {
    // 与会话页判断「深链还没处理完」的是同一个条件（isDeepLinkPending）：它为真期间会话页不为占位会话取数据
    if (!isDeepLinkPending(route.name, route.query)) return
    const { wf, sid, q } = deepLinkTarget(route.query)
    // 还没登录：登录完成后 isAuthenticated 变化会再次触发，这里先不动地址里的参数
    if (!auth.isAuthenticated || opening) return
    opening = true
    try {
      // 简略列表只有最近的会话：目标工作流较旧时才按工作流取完整列表；这几步都走 store 的单飞，
      // 与页面挂载时的取数合并，不再各发一次整表请求（#177）
      const target = await resolveDeepLinkTarget(
        {
          list: () => sessions.sessions,
          refreshList: () => sessions.fetchSessions(undefined, { fresh: false }),
          loadWorkflow: id => sessions.loadWorkflowSessionsList(id),
        },
        wf,
        sid,
      )
      if (target) {
        sessions.selectSession(target)
        // 点名确认项：确认 store 会随选中的会话切到这个工作流并加载，加载完（或本来就加载好了）再点名
        if (q) confirms.focusWhenLoaded(target.workflow_id || target.session_id, q)
      }
    } catch (err) {
      console.warn('[notify] 打开通知深链失败', err)
    } finally {
      opening = false
      // 无论找没找到都去掉地址里的参数，免得刷新 / 后退时反复触发
      void router.replace({ name: 'home' })
    }
  }

  watch(
    () => [route.name, route.query.wf, route.query.s, route.query.q, auth.isAuthenticated] as const,
    () => void openDeepLink(),
    { immediate: true },
  )
}
