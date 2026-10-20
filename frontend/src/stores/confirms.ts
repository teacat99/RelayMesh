import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import {
  confirmErrorMessage,
  confirmsApi,
  type ConfirmBoard,
  type ConfirmItem,
} from '../api/confirms'
import { whenSecondaryOpen } from '../lib/secondaryGate'

// 「待确认」区与时间线确认卡的数据（交流页）：当前工作流的待处理确认项、守护告警，
// 外加最近处理过的几条（时间线里要看到「已答复 / 已忽略」的结局）。
// 数据以服务端为准：任何写操作之后都靠 confirm_updated 事件（或这里的主动刷新）收敛；
// 同一时刻只关心一个工作流，切换时旧数据立刻清空，晚到的旧响应按请求序号丢弃。

const HISTORY_LIMIT = 60

function emptyBoard(): ConfirmBoard {
  return {
    items: [],
    guard_alerts: [],
    counts: { pending: 0, items: 0, guard_alerts: 0, by_risk: {}, by_kind: {} },
  }
}

export const useConfirmStore = defineStore('confirms', () => {
  const workflowId = ref('')
  const board = ref<ConfirmBoard>(emptyBoard())
  /** 已处理过的确认项（新的在前），只用来在时间线里留痕。 */
  const history = ref<ConfirmItem[]>([])
  const loaded = ref(false)
  const loading = ref(false)
  const error = ref('')
  /** 正在提交的编号（防重复点击）；批量时是 '*'。 */
  const busy = ref<Set<string>>(new Set())
  /** 时间线上点「去作答」后要在待确认区里聚焦的编号。 */
  const focusRef = ref('')
  /** 每收到一次 confirm_updated 加一：不在交流页的组件（如任务详情里的关联确认项）靠它知道该重取了。 */
  const eventTick = ref(0)

  let refreshTimer: ReturnType<typeof setTimeout> | null = null
  let requestSeq = 0
  /** watchWorkflow 的令牌：被放行延后的首次加载只有令牌仍是最新的才真的发。 */
  let watchSeq = 0
  /** 深链 /?wf=…&q=Q3 要点名的编号：确认项可能还没加载出来，先记着，load 成功后再点名；换工作流（reset）不清它，15 秒内有效。 */
  let pendingFocus: { workflowId: string; ref: string; at: number } | null = null

  const pending = computed(() => board.value.items)
  const pendingCount = computed(() => board.value.counts.pending)
  /** 时间线用：待处理的 + 已处理的，按序号升序。 */
  const timelineItems = computed(() => {
    const seen = new Set<number>()
    const all: ConfirmItem[] = []
    for (const it of [...board.value.items, ...history.value]) {
      if (seen.has(it.seq)) continue
      seen.add(it.seq)
      all.push(it)
    }
    return all.sort((a, b) => a.seq - b.seq)
  })

  function reset() {
    if (refreshTimer) {
      clearTimeout(refreshTimer)
      refreshTimer = null
    }
    requestSeq++
    board.value = emptyBoard()
    history.value = []
    loaded.value = false
    loading.value = false
    error.value = ''
    busy.value = new Set()
    focusRef.value = ''
  }

  /** 点名一个确认项（深链用）：已经加载好就立刻点名，否则等这个工作流的确认项加载完再点名。 */
  function focusWhenLoaded(id: string, ref: string) {
    if (!id || !ref) return
    if (workflowId.value === id && loaded.value) {
      pendingFocus = null
      focusRef.value = ref
      return
    }
    pendingFocus = { workflowId: id, ref, at: Date.now() }
  }

  /** 切换要关注的工作流（空串 = 不关注）。同一个工作流重复调用不重新拉取。 */
  function watchWorkflow(id: string) {
    if (id === workflowId.value) return
    workflowId.value = id
    reset()
    if (id) {
      // 次要请求让路（#169 第三期，lib/secondaryGate.ts）：整表在途时先等一等，放行后再取；
      // 等的这段时间里又换了工作流（或离开会话页）就不发了——令牌对不上。栅栏没立着时 whenSecondaryOpen 同步执行，与以前一样。
      const token = ++watchSeq
      whenSecondaryOpen(() => {
        if (token === watchSeq && workflowId.value === id) void load(id)
      })
    } else {
      watchSeq++
    }
  }

  async function load(id: string = workflowId.value, silent = false) {
    if (!id) return
    const mine = ++requestSeq
    if (!silent) loading.value = true
    try {
      const [b, done] = await Promise.all([
        confirmsApi.board(id),
        confirmsApi.list(id, { status: ['answered', 'dismissed', 'withdrawn', 'expired'], limit: HISTORY_LIMIT }),
      ])
      if (mine !== requestSeq || id !== workflowId.value) return
      board.value = b
      history.value = done
      error.value = ''
      loaded.value = true
      if (pendingFocus) {
        const want = pendingFocus
        if (want.workflowId === id) {
          pendingFocus = null
          if (Date.now() - want.at < 15_000) focusRef.value = want.ref
        } else if (Date.now() - want.at >= 15_000) {
          pendingFocus = null
        }
      }
    } catch (err) {
      if (mine !== requestSeq) return
      error.value = confirmErrorMessage(err, '无法加载待确认区')
    } finally {
      if (mine === requestSeq) loading.value = false
    }
  }

  /** confirm_updated / SSE 重连 / 文档保存等之后的刷新；短时间内的多次事件合并成一次请求。 */
  function onServerEvent(payload?: { workflow_id?: string }) {
    eventTick.value++
    if (!workflowId.value) return
    if (payload?.workflow_id && payload.workflow_id !== workflowId.value) return
    if (refreshTimer) clearTimeout(refreshTimer)
    refreshTimer = setTimeout(() => {
      refreshTimer = null
      void load(workflowId.value, true)
    }, 200)
  }

  async function withBusy<T>(key: string, fn: () => Promise<T>): Promise<T> {
    if (busy.value.has(key)) throw new Error('正在提交，请稍候')
    busy.value = new Set(busy.value).add(key)
    try {
      return await fn()
    } finally {
      const next = new Set(busy.value)
      next.delete(key)
      busy.value = next
    }
  }

  /** 提交成功后立刻把这条从待处理挪进历史，界面不必等下一次拉取；随后仍会拉一次校准。 */
  function applyAnswered(items: ConfirmItem[]) {
    if (!items.length) return
    const seqs = new Set(items.map(i => i.seq))
    const remaining = board.value.items.filter(i => !seqs.has(i.seq))
    const removed = board.value.items.length - remaining.length
    const counts = { ...board.value.counts }
    counts.items = remaining.length
    counts.pending = Math.max(0, counts.pending - removed)
    board.value = { ...board.value, items: remaining, counts }
    history.value = [...items, ...history.value.filter(i => !seqs.has(i.seq))]
  }

  async function answer(ref: string, options: string[], note?: string, custom?: string): Promise<ConfirmItem> {
    const id = workflowId.value
    return withBusy(ref, async () => {
      const item = await confirmsApi.answer(id, ref, options, note, custom)
      if (id === workflowId.value) applyAnswered([item])
      onServerEvent({ workflow_id: id })
      return item
    })
  }

  /** 把给定编号全部「采用推荐」；整批原子，任何一项不合格就一项都不保存（错误里会逐条说明）。 */
  async function adoptRecommended(refs: string[]): Promise<ConfirmItem[]> {
    const id = workflowId.value
    return withBusy('*', async () => {
      const res = await confirmsApi.answerBatch(id, refs.map(ref => ({ ref })), true)
      if (id === workflowId.value) applyAnswered(res.items)
      onServerEvent({ workflow_id: id })
      return res.items
    })
  }

  async function dismiss(ref: string, reason?: string): Promise<ConfirmItem> {
    const id = workflowId.value
    return withBusy(ref, async () => {
      const item = await confirmsApi.dismiss(id, ref, reason)
      if (id === workflowId.value) applyAnswered([item])
      onServerEvent({ workflow_id: id })
      return item
    })
  }

  return {
    workflowId,
    board,
    history,
    loaded,
    loading,
    error,
    busy,
    focusRef,
    focusWhenLoaded,
    eventTick,
    pending,
    pendingCount,
    timelineItems,
    watchWorkflow,
    load,
    onServerEvent,
    answer,
    adoptRecommended,
    dismiss,
  }
})
