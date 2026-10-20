import { defineStore } from 'pinia'
import { ref } from 'vue'
import { toast } from 'vue-sonner'
import { NOTICE_DURATION_MS, type RelayRequest } from '../lib/crossNotice'
import {
  TAKEOVER_GRACE_MS,
  TOAST_ACTION,
  badgedTitle,
  createAggregator,
  createSeenIds,
  notificationTag,
  parseNotifyPayload,
  planPresentation,
  presentNotice,
  safeDeepLink,
  stripBadge,
  type NotifyPayload,
} from '../lib/notifyBus'
import { browserEnv, createCoord, type Coord } from '../lib/notifyCoord'
import { workflowConversation } from '../lib/notifyPrefs'
import { useNotifyStore } from './notify'

/** 新反馈请求在本地兜底路径上登记时用的字段（FeedbackSession 的子集）。 */
export interface LocalRequest {
  session_id: string
  workflow_id?: string
  title?: string
  summary?: string
  created_at?: string
}

/**
 * 浏览器通知总线（#115 M1.4）：消费服务端统一通知闸推来的 SSE notify，在这台设备上呈现——
 * 幂等去重、多标签选主（只有一个标签页响铃与弹系统通知）、前台抑制、同会话合并、标题角标、页内提示回退、点击深链。
 * 纯逻辑在 lib/notifyBus.ts 与 lib/notifyCoord.ts（有单元测试），这里只把它们接到浏览器与 Pinia。
 */
export const useNotifyBusStore = defineStore('notifyBus', () => {
  const settings = useNotifyStore()
  /** 本标签页未读的通知条数（标题角标）；回到本标签页时清零。 */
  const unread = ref(0)

  const seen = createSeenIds()
  const aggregator = createAggregator()
  let coord: Coord | null = null
  let navigate: ((path: string) => void) | null = null
  /** 本标签页此刻正在看的会话键（不在会话界面时为空）。 */
  let conversation = ''
  let baseTitle = ''
  let started = false
  const cleanups: Array<() => void> = []

  const hasDocument = typeof document !== 'undefined'

  function isVisible(): boolean {
    return hasDocument && document.visibilityState === 'visible'
  }

  function isFocused(): boolean {
    return hasDocument && isVisible() && document.hasFocus()
  }

  function publishSelf() {
    coord?.setSelf({ focused: isFocused(), visible: isVisible(), conversation })
  }

  function applyTitle() {
    if (hasDocument) document.title = badgedTitle(baseTitle || document.title, unread.value)
  }

  function bumpUnread() {
    unread.value++
    applyTitle()
  }

  function clearUnread() {
    aggregator.reset()
    if (unread.value === 0) return
    unread.value = 0
    applyTitle()
  }

  function openLink(p: NotifyPayload) {
    const link = safeDeepLink(p.deep_link)
    if (link) navigate?.(link)
  }

  function showToast(p: NotifyPayload) {
    const { title, body } = presentNotice(p)
    toast.info(title, {
      id: `notify:${notificationTag(p)}`, // 同一会话的页内提示互相替换，不叠出一摞
      description: body,
      duration: NOTICE_DURATION_MS,
      action: safeDeepLink(p.deep_link) ? { label: TOAST_ACTION, onClick: () => openLink(p) } : undefined,
    })
  }

  /** leader 的职责：响提示音、弹系统通知，并向其他标签页声明「已呈现」。 */
  function present(p: NotifyPayload) {
    if (coord?.hasShown(p.id)) return // 别的标签页（接手方）已经呈现过了
    if (p.sound && settings.soundEnabled) settings.playAlert()
    if (settings.desktopReady) {
      const tag = notificationTag(p)
      // 只有非穿透类按会话合并；穿透类每条独立
      const count = tag.startsWith('conv:') ? aggregator.bump(tag, Date.now()) : 1
      const { title, body } = presentNotice(p, count)
      settings.showSystem({ title, body, tag, onClick: () => openLink(p) })
    }
    coord?.announceShown(p.id)
  }

  function requestFromPayload(p: NotifyPayload): RelayRequest {
    return {
      sessionId: p.session_id,
      workflowId: p.workflow_id || p.session_id,
      title: p.preview,
      createdAt: p.created_at || undefined,
    }
  }

  function handle(p: NotifyPayload, request?: RelayRequest) {
    const now = Date.now()
    if (!seen.claim(p.id, now)) return
    const plan = planPresentation({
      payload: p,
      isLeader: coord?.isLeader() ?? true,
      selfFocused: isFocused(),
      states: coord?.states() ?? [],
      now,
      soundOn: settings.soundEnabled,
      desktopReady: settings.desktopReady,
    })
    if (plan.suppressed) return
    if (plan.badge) bumpUnread()
    if (plan.request) settings.emitRequest(request ?? requestFromPayload(p))
    if (plan.toast) showToast(p)
    if (!plan.sound && !plan.system) {
      // 本标签页不是 leader（或提示音与系统通知都不适用）：leader 不在时由接手方兜底
      if (coord && !coord.isLeader()) {
        void coord.waitShown(p.id, TAKEOVER_GRACE_MS).then(ok => {
          if (!ok) present(p)
        })
      }
      return
    }
    present(p)
  }

  /** SSE notify 事件：原始载荷进来，整理后呈现。无效载荷静默丢弃。 */
  function ingest(raw: unknown) {
    const p = parseNotifyPayload(raw)
    if (p) handle(p)
  }

  /**
   * 新反馈请求的本地兜底路径：服务端的 notify 事件正常会先到；
   * 但页面刚连上 / 断线重连期间错过的请求，只能从会话列表里发现——这里用同一个 id 登记，
   * 已经由 SSE 呈现过的不会重复，没呈现过的补上（与升级前「发现新请求就提醒」的行为一致）。
   */
  function announceRequest(s: LocalRequest) {
    if (!s.session_id) return
    const wf = s.workflow_id ?? ''
    const q = new URLSearchParams()
    if (wf) q.set('wf', wf)
    q.set('s', s.session_id)
    handle(
      {
        id: `blk:${s.session_id}`,
        kind: 'blocking_feedback',
        conversation: workflowConversation(wf),
        workflow_id: wf,
        session_id: s.session_id,
        sender: '',
        title: '',
        preview: s.title ?? '',
        deep_link: `/?${q.toString()}`,
        mention: false,
        created_at: s.created_at ?? '',
        penetrate: true,
        sound: true,
        badge: true,
      },
      {
        sessionId: s.session_id,
        workflowId: wf || s.session_id,
        title: s.title,
        summary: s.summary,
        createdAt: s.created_at,
      },
    )
  }

  /** 本标签页正在看的会话变了（由界面层根据路由与选中的工作流算出）。 */
  function setConversation(next: string) {
    if (conversation === next) return
    conversation = next
    publishSelf()
  }

  function setupCoord() {
    if (coord) return
    coord = createCoord(browserEnv())
    publishSelf()
  }

  function teardownCoord() {
    coord?.stop()
    coord = null
  }

  /** 启动：建立多标签协调、监听聚焦 / 可见性变化。重复调用无副作用。navigate 用来在点击通知 / 提示时跳转。 */
  function start(opts: { navigate: (path: string) => void }) {
    navigate = opts.navigate
    if (started || typeof window === 'undefined') return
    started = true
    baseTitle = stripBadge(hasDocument ? document.title : '')
    setupCoord()

    const onFocusChange = () => {
      publishSelf()
      if (isFocused()) {
        clearUnread()
        settings.refreshPermission() // 用户可能刚在浏览器设置里改了通知权限
      }
    }
    const onPageHide = () => teardownCoord()
    const onPageShow = () => {
      setupCoord()
      onFocusChange()
    }
    window.addEventListener('focus', onFocusChange)
    window.addEventListener('blur', onFocusChange)
    document.addEventListener('visibilitychange', onFocusChange)
    window.addEventListener('pagehide', onPageHide)
    window.addEventListener('pageshow', onPageShow)
    cleanups.push(
      () => window.removeEventListener('focus', onFocusChange),
      () => window.removeEventListener('blur', onFocusChange),
      () => document.removeEventListener('visibilitychange', onFocusChange),
      () => window.removeEventListener('pagehide', onPageHide),
      () => window.removeEventListener('pageshow', onPageShow),
    )
  }

  function stop() {
    for (const fn of cleanups.splice(0)) fn()
    teardownCoord()
    started = false
  }

  return { unread, start, stop, ingest, announceRequest, setConversation }
})
