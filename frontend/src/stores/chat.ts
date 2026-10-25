import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import {
  chatErrorMessage,
  conversationIdOf,
  conversationsApi,
  isGroupId,
  type ChatMessage,
  type ChatPage,
  type ChatSendBody,
  type ChatSender,
} from '../api/conversations'
import type { GroupInfo } from '../api/groups'
import { mergeMessages, mergeTail, newestCursor, precedes } from '../lib/chatView'

// 统一消息流的数据：当前打开的一个群聊（grp:）的消息列表（群聊窗口 GroupChatPane 是唯一打开它的地方，#165 第二步），
// 外加侧栏用的各会话未读数（一对一会话的未读数由预览视图读到之后同步进来）。
// 数据以服务端为准：任何变化都靠 conversation_updated（服务端已防抖）触发「取最新一页再合并」；
// 同一时刻只关心一个会话，切换时旧数据立刻清空，晚到的旧响应按请求序号丢弃。

const PAGE_SIZE = 50

export const useChatStore = defineStore('chat', () => {
  const workflowId = ref('')
  const items = ref<ChatMessage[]>([])
  const hasMoreBefore = ref(false)
  /** 正在看历史里的某一段（搜索跳转）：后面还有更新的消息没加载，最新一页的刷新不能往里并。 */
  const hasMoreAfter = ref(false)
  const loaded = ref(false)
  const loading = ref(false)
  const loadingOlder = ref(false)
  const sending = ref(false)
  const error = ref('')
  const unread = ref(0)
  const readMs = ref(0)
  const agent = ref<ChatSender | null>(null)
  /** 群聊才有：群信息与成员（每次取页时跟着刷新）。 */
  const group = ref<GroupInfo | null>(null)
  /** 侧栏用：工作流 → 未读数（只含打开过 / 被关注过的）。 */
  const unreadByWorkflow = ref<Record<string, number>>({})

  let seq = 0
  let refreshing: Promise<void> | null = null
  let refreshAgain = false
  let summaryTimer: ReturnType<typeof setTimeout> | null = null
  let watched: string[] = []

  const conversationId = computed(() => (workflowId.value ? conversationIdOf(workflowId.value) : ''))

  function apply(page: ChatPage, mode: 'replace' | 'tail') {
    items.value = mode === 'replace' ? page.items : mergeTail(items.value, page.items)
    unread.value = page.unread
    readMs.value = page.read_ms
    agent.value = page.agent
    group.value = page.group ?? null
    if (workflowId.value) unreadByWorkflow.value = { ...unreadByWorkflow.value, [workflowId.value]: page.unread }
  }

  function reset() {
    seq++
    items.value = []
    hasMoreBefore.value = false
    hasMoreAfter.value = false
    loaded.value = false
    loading.value = false
    loadingOlder.value = false
    error.value = ''
    unread.value = 0
    agent.value = null
    group.value = null
    refreshing = null
    refreshAgain = false
  }

  /** 打开一个会话（目前只有群聊窗口用）：清空旧数据，取最新一页。传空串表示离开群聊窗口。 */
  async function open(id: string) {
    if (id === workflowId.value && loaded.value) return
    workflowId.value = id
    reset()
    if (!id) return
    const mine = seq
    loading.value = true
    try {
      const page = await conversationsApi.list(conversationIdOf(id), { limit: PAGE_SIZE })
      if (mine !== seq) return
      apply(page, 'replace')
      hasMoreBefore.value = page.has_more_before
      loaded.value = true
    } catch (e) {
      if (mine === seq) error.value = chatErrorMessage(e, '消息加载失败')
    } finally {
      if (mine === seq) loading.value = false
    }
  }

  /** 取最新一页并合并进列表。同时只跑一个；跑的时候又有新事件，结束后再跑一次。 */
  function refresh(): Promise<void> {
    if (!workflowId.value || !loaded.value) return Promise.resolve()
    if (hasMoreAfter.value) {
      // 在看历史：只更新未读数，不把最新一页并进来（会和正在看的那段断开）
      void refreshCounts()
      return Promise.resolve()
    }
    if (refreshing) {
      refreshAgain = true
      return refreshing
    }
    const mine = seq
    const id = workflowId.value
    refreshing = (async () => {
      try {
        const page = await conversationsApi.list(conversationIdOf(id), { limit: PAGE_SIZE })
        if (mine !== seq) return
        // 这一页之前还有更早的、而我们手里最新的比这一页最早的还早：中间有空洞，整体换成这一页。
        const hole =
          page.has_more_before &&
          page.items.length > 0 &&
          items.value.length > 0 &&
          precedes(items.value[items.value.length - 1], page.items[0])
        apply(page, hole ? 'replace' : 'tail')
        if (hole) hasMoreBefore.value = page.has_more_before
        error.value = ''
      } catch (e) {
        if (mine === seq) error.value = chatErrorMessage(e, '消息刷新失败')
      } finally {
        refreshing = null
        if (refreshAgain && mine === seq) {
          refreshAgain = false
          void refresh()
        }
      }
    })()
    return refreshing
  }

  async function refreshCounts() {
    const id = workflowId.value
    if (!id) return
    try {
      // 群聊没有摘要接口：取一条消息，页里带着未读数
      const count = isGroupId(id)
        ? (await conversationsApi.list(id, { limit: 1 })).unread
        : (await conversationsApi.summaries([conversationIdOf(id)]))[0]?.unread
      if (id !== workflowId.value || count === undefined) return
      unread.value = count
      unreadByWorkflow.value = { ...unreadByWorkflow.value, [id]: count }
    } catch {
      // 保留旧值
    }
  }

  /** 跳到某条消息（搜索结果）：列表换成以它为中心的一窗，之后可以向两侧翻页。 */
  async function jumpTo(cursor: string) {
    if (!workflowId.value) return
    const mine = seq
    loading.value = true
    try {
      const page = await conversationsApi.list(conversationId.value, { limit: PAGE_SIZE, around: cursor })
      if (mine !== seq) return
      items.value = page.items
      hasMoreBefore.value = page.has_more_before
      hasMoreAfter.value = page.has_more_after
      unread.value = page.unread
    } catch (e) {
      if (mine === seq) error.value = chatErrorMessage(e, '定位失败')
    } finally {
      if (mine === seq) loading.value = false
    }
  }

  /** 看历史时往后翻一页；翻到头就回到「跟随最新」状态。 */
  async function loadNewer() {
    if (!workflowId.value || !hasMoreAfter.value || items.value.length === 0) return
    const mine = seq
    try {
      const page = await conversationsApi.list(conversationId.value, { limit: PAGE_SIZE, after: items.value[items.value.length - 1].cursor })
      if (mine !== seq) return
      items.value = mergeMessages(items.value, page.items)
      hasMoreAfter.value = page.has_more_after
    } catch (e) {
      if (mine === seq) error.value = chatErrorMessage(e, '消息加载失败')
    }
  }

  /** 回到最新：丢掉当前这一段，重新取最新一页。 */
  async function backToLatest() {
    const id = workflowId.value
    if (!id) return
    workflowId.value = ''
    await open(id)
  }

  /** 往前翻一页历史。 */
  async function loadOlder() {
    if (!workflowId.value || loadingOlder.value || !hasMoreBefore.value || items.value.length === 0) return
    const mine = seq
    loadingOlder.value = true
    try {
      const page = await conversationsApi.list(conversationId.value, { limit: PAGE_SIZE, before: items.value[0].cursor })
      if (mine !== seq) return
      items.value = mergeMessages(items.value, page.items)
      hasMoreBefore.value = page.has_more_before
    } catch (e) {
      if (mine === seq) error.value = chatErrorMessage(e, '历史消息加载失败')
    } finally {
      if (mine === seq) loadingOlder.value = false
    }
  }

  /** 发一条消息；成功后把服务端返回的那条直接放进列表（不用等事件）。失败抛出，由调用方提示并保留输入。 */
  async function send(body: ChatSendBody): Promise<ChatMessage> {
    if (!workflowId.value) throw new Error('没有打开的会话')
    sending.value = true
    const mine = seq
    try {
      const res = await conversationsApi.send(conversationId.value, body)
      if (mine === seq) {
        if (hasMoreAfter.value) {
          // 正在看历史时发消息：回到最新，新消息才和上下文连得上
          void backToLatest()
        } else {
          items.value = mergeMessages(items.value, [res.message])
          unread.value = 0
        }
      }
      void refresh()
      return res.message
    } finally {
      sending.value = false
    }
  }

  /** 把已读游标推到列表里最新一条。多设备以最大值为准，服务端只增不减。 */
  async function markRead() {
    const cursor = newestCursor(items.value)
    if (!workflowId.value || !cursor) return
    const id = workflowId.value
    try {
      const res = await conversationsApi.markRead(conversationIdOf(id), cursor)
      if (id !== workflowId.value) return
      unread.value = res.unread
      readMs.value = res.read_ms
      unreadByWorkflow.value = { ...unreadByWorkflow.value, [id]: res.unread }
    } catch {
      // 已读标记失败不打扰：下次进入或下一条事件时会再推
    }
  }

  /** 侧栏关注的会话：取一次摘要（未读数）。 */
  function watchSummaries(workflowIds: string[]) {
    watched = [...new Set(workflowIds.filter(Boolean))]
    scheduleSummaries(0)
  }

  function scheduleSummaries(delay = 400) {
    if (summaryTimer) clearTimeout(summaryTimer)
    summaryTimer = setTimeout(async () => {
      summaryTimer = null
      if (watched.length === 0) return
      try {
        const list = await conversationsApi.summaries(watched.map(conversationIdOf))
        const next = { ...unreadByWorkflow.value }
        for (const s of list) next[s.workflow_id] = s.unread
        unreadByWorkflow.value = next
      } catch {
        // 摘要拿不到就保留旧的未读数
      }
    }, delay)
  }

  /** SSE conversation_updated / conversation_changed。 */
  function onServerEvent(payload: { workflow_id?: string; group_id?: string } | null) {
    const id = payload?.group_id || payload?.workflow_id
    if (!id) return
    if (id === workflowId.value) void refresh()
    if (watched.includes(id)) scheduleSummaries()
  }

  /**
   * 实时流（重）连上：页面载入到连上之间、断线期间发生的事件没人听见，也不会补发——
   * 打开着的会话补取最新一页（没打开 / 还没载入时 refresh 什么都不做），侧栏关注的会话重取未读数（#204）。
   */
  function onConnected() {
    void refresh()
    if (watched.length) scheduleSummaries(0)
  }

  function unreadOf(id: string | undefined): number {
    return id ? unreadByWorkflow.value[id] ?? 0 : 0
  }

  /** 预览视图读到未读数 / 推进已读游标之后，把这个会话的数字同步给侧栏（只动侧栏用的映射，不碰群聊窗口自己打开的会话）。 */
  function setUnread(id: string, count: number) {
    if (!id) return
    unreadByWorkflow.value = { ...unreadByWorkflow.value, [id]: count }
  }

  return {
    workflowId,
    conversationId,
    items,
    hasMoreBefore,
    hasMoreAfter,
    loaded,
    loading,
    loadingOlder,
    sending,
    error,
    unread,
    readMs,
    agent,
    group,
    unreadByWorkflow,
    open,
    refresh,
    loadOlder,
    loadNewer,
    jumpTo,
    backToLatest,
    send,
    markRead,
    watchSummaries,
    onServerEvent,
    onConnected,
    unreadOf,
    setUnread,
  }
})
