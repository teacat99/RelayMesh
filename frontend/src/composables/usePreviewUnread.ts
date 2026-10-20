import { onBeforeUnmount, onMounted, ref } from 'vue'
import { conversationIdOf, conversationsApi } from '../api/conversations'
import { NEWEST_PAGE, UNREAD_KINDS, newestAgentMessage, readVerdict } from '../lib/previewUnread'
import { useChatStore } from '../stores/chat'

// 预览视图的未读（#120 阶段 E-c2）：进入会话时冻结一份「已读游标」用来画分隔线，停在底部看过之后把共享已读游标推到最新。
// 已读游标 conversation_reads 是跨设备共享、只增不减的一份（群聊窗口、侧栏、这里读写同一份）；
// 这里不碰群聊窗口的 store 数据（它一次只开一个群），只在推进游标之后把数字同步给侧栏。

/** 停在底部、标签页可见，持续这么久才算「看过了」。 */
export const READ_DWELL_MS = 2000
/** 服务端最新的 Agent 消息还没渲染出来时，隔多久再看一次。 */
export const READ_RETRY_MS = 3000
/** 连续这么多次都对不上（页面一直没渲染出那条消息），就不再等：您已经在底部停了足够久，别让数字一直挂着。 */
export const READ_MAX_WAITS = 4
/** 标签页离开这么久再回来，才当作「刚回到这个会话」重新冻结游标（马上切回来不算）。 */
export const AWAY_MS = 5000

export interface PreviewUnreadOptions {
  /** 现在是不是停在底部（离底部不到约 80 像素）。 */
  atBottom: () => boolean
  /** 已经渲染出来的最晚一条 Agent 消息（毫秒），见 lib/previewUnread。 */
  renderedMs: () => number
}

export function usePreviewUnread(opts: PreviewUnreadOptions) {
  const chatStore = useChatStore()
  /** 进入会话时冻结的已读游标（毫秒）：分隔线画在它之后的第一条 Agent 消息前面；null = 进来时没有未读，不画。 */
  const dividerMs = ref<number | null>(null)

  let seq = 0
  let current = ''
  let ready = false
  let timer: ReturnType<typeof setTimeout> | null = null
  let waits = 0
  let hiddenAt = 0

  function clearTimer() {
    if (timer) {
      clearTimeout(timer)
      timer = null
    }
  }

  function viewing(): boolean {
    return document.visibilityState === 'visible' && opts.atBottom()
  }

  /** 取一次服务端的已读状态：把侧栏的数字对上；有未读就冻结游标（只在还没冻结时）。 */
  async function freeze(wf: string): Promise<void> {
    const mine = seq
    try {
      const page = await conversationsApi.list(conversationIdOf(wf), { limit: 1 })
      if (mine !== seq) return
      chatStore.setUnread(wf, page.unread)
      if (dividerMs.value === null && page.unread > 0) dividerMs.value = page.read_ms
    } catch {
      // 拿不到就不画分隔线，不影响阅读
    }
  }

  /** 打开 / 切换会话：清掉旧状态，冻结一份游标，然后开始「停在底部就标已读」的计时。传空串表示没有会话。 */
  async function open(wf: string): Promise<void> {
    seq++
    clearTimer()
    waits = 0
    ready = false
    dividerMs.value = null
    current = wf
    if (!wf) return
    const mine = seq
    await freeze(wf)
    if (mine !== seq) return
    ready = true
    touch()
  }

  /** 滚动 / 回到前台 / 渲染出新消息之后调用：满足「可见且停在底部」就（重新）计时，不满足就取消。 */
  function touch(): void {
    if (!ready || !current) return
    if (!viewing()) {
      clearTimer()
      waits = 0
      return
    }
    clearTimer()
    timer = setTimeout(() => {
      timer = null
      void check()
    }, READ_DWELL_MS)
  }

  function retryLater(): void {
    clearTimer()
    timer = setTimeout(() => {
      timer = null
      void check()
    }, READ_RETRY_MS)
  }

  async function check(): Promise<void> {
    const wf = current
    if (!wf || !ready || !viewing()) return
    const mine = seq
    try {
      const page = await conversationsApi.list(conversationIdOf(wf), { limit: NEWEST_PAGE, kinds: [...UNREAD_KINDS] })
      if (mine !== seq) return
      const newest = newestAgentMessage(page.items)
      const verdict = readVerdict({
        unread: page.unread,
        newestMs: newest ? Date.parse(newest.sent_at) : null,
        renderedMs: opts.renderedMs(),
      })
      if (verdict === 'none') {
        waits = 0
        chatStore.setUnread(wf, page.unread)
        return
      }
      if (verdict === 'wait' && ++waits < READ_MAX_WAITS) {
        retryLater()
        return
      }
      waits = 0
      // 请求回来的这段时间里您可能已经滚走 / 切走了标签页：那就不标，等下一次停在底部
      if (!viewing()) return
      const cursor = newest?.cursor ?? page.items[page.items.length - 1]?.cursor
      const res = await conversationsApi.markRead(conversationIdOf(wf), cursor)
      if (mine !== seq) return
      chatStore.setUnread(wf, res.unread)
    } catch {
      // 标已读失败不打扰：下一次滚动 / 新消息 / 回到前台时会再试
    }
  }

  function onVisibility(): void {
    if (document.visibilityState === 'hidden') {
      hiddenAt = Date.now()
      clearTimer()
      return
    }
    // 离开了一阵再回来：Agent 这段时间说的话还没读过，重新冻结游标，分隔线才能标出「从哪里开始是新的」
    const away = hiddenAt > 0 && Date.now() - hiddenAt >= AWAY_MS
    hiddenAt = 0
    if (away && ready && current && dividerMs.value === null) {
      const wf = current
      void freeze(wf).then(() => touch())
      return
    }
    touch()
  }

  function dispose(): void {
    seq++
    ready = false
    current = ''
    clearTimer()
    document.removeEventListener('visibilitychange', onVisibility)
  }

  onMounted(() => document.addEventListener('visibilitychange', onVisibility))
  onBeforeUnmount(dispose)

  return { dividerMs, open, touch, dispose }
}
