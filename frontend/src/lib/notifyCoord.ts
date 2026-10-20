// 多标签页协调（#115 M1.4）：同一个浏览器里开了好几个 RelayMesh 标签页时，每个标签页都会收到同一条 SSE notify，
// 但提示音和系统通知只能响一次、弹一次——这里负责三件事：
//   1. 选主（leader）：负责响铃与弹系统通知的那一个标签页。优先用 Web Locks（持有锁的标签页就是 leader，
//      标签页关闭 / 崩溃时锁自动释放，下一个排队的接任）；没有 Web Locks 时回退到 BroadcastChannel 心跳选举。
//   2. 前台状态汇总：每个标签页把自己「是否聚焦、在看哪个会话」广播出去，大家据此做前台抑制
//      （聚焦的标签页正在看这个会话时，其他标签页的 leader 不再重复打扰）。
//   3. 「已呈现」声明：leader 呈现一条通知后广播它的 id；非 leader 等一小会儿没等到声明，就说明 leader
//      不在（被冻结 / 崩溃 / 锁还没拿到），由自己接手呈现——选主出岔子时也不会漏掉通知。
//
// 浏览器接口通过 CoordEnv 注入，单元测试里用假的锁、假的频道与假的时钟；真实环境由 browserEnv() 提供。
// 没有 BroadcastChannel 也没有 Web Locks 时退化为单标签模式（自己就是 leader）。

import { TAB_STATE_TTL_MS, type TabState } from './notifyBus'

export interface ChannelLike {
  postMessage(data: unknown): void
  onmessage: ((ev: { data: unknown }) => void) | null
  close(): void
}

export interface LockManagerLike {
  request(
    name: string,
    options: { mode?: 'exclusive'; signal?: AbortSignal },
    callback: () => Promise<unknown> | unknown,
  ): Promise<unknown>
}

export interface CoordEnv {
  now(): number
  newId(): string
  openChannel(name: string): ChannelLike | null
  locks: LockManagerLike | null
  setInterval(fn: () => void, ms: number): unknown
  clearInterval(handle: unknown): void
  setTimeout(fn: () => void, ms: number): unknown
  clearTimeout(handle: unknown): void
}

export const CHANNEL_NAME = 'relaymesh-notify'
export const LOCK_NAME = 'relaymesh-notify-leader'
/** 状态心跳的间隔；对端超过 TAB_STATE_TTL_MS 没有更新就视为不在了。 */
export const HEARTBEAT_MS = 3_000

export type CoordMode = 'locks' | 'heartbeat' | 'solo'

export interface SelfState {
  focused: boolean
  visible: boolean
  conversation: string
}

export interface Coord {
  readonly tabId: string
  readonly mode: CoordMode
  isLeader(): boolean
  /** 订阅 leader 身份的变化；返回取消订阅函数。 */
  onLeaderChange(cb: (leader: boolean) => void): () => void
  setSelf(state: SelfState): void
  /** 所有仍然新鲜的标签页状态（含自己）。 */
  states(): TabState[]
  /** 声明「这条通知我已经呈现过了」。 */
  announceShown(id: string): void
  hasShown(id: string): boolean
  /** 等别的标签页声明已呈现：等到返回 true；宽限内没等到返回 false（该由自己接手）。 */
  waitShown(id: string, graceMs: number): Promise<boolean>
  stop(): void
}

interface Peer extends TabState {
  startedAt: number
}

type Msg =
  | { k: 'state'; tabId: string; startedAt: number; focused: boolean; visible: boolean; conversation: string }
  | { k: 'hello'; tabId: string }
  | { k: 'bye'; tabId: string }
  | { k: 'shown'; tabId: string; id: string }

const SHOWN_CAP = 512

function parseMsg(data: unknown): Msg | null {
  if (!data || typeof data !== 'object') return null
  const m = data as Record<string, unknown>
  if (typeof m.tabId !== 'string' || !m.tabId) return null
  switch (m.k) {
    case 'state':
      if (typeof m.startedAt !== 'number') return null
      return {
        k: 'state',
        tabId: m.tabId,
        startedAt: m.startedAt,
        focused: m.focused === true,
        visible: m.visible === true,
        conversation: typeof m.conversation === 'string' ? m.conversation : '',
      }
    case 'hello':
      return { k: 'hello', tabId: m.tabId }
    case 'bye':
      return { k: 'bye', tabId: m.tabId }
    case 'shown':
      return typeof m.id === 'string' && m.id ? { k: 'shown', tabId: m.tabId, id: m.id } : null
    default:
      return null
  }
}

export function createCoord(env: CoordEnv): Coord {
  const tabId = env.newId()
  const startedAt = env.now()
  const channel = env.openChannel(CHANNEL_NAME)
  const mode: CoordMode = env.locks ? 'locks' : channel ? 'heartbeat' : 'solo'

  let self: SelfState = { focused: false, visible: false, conversation: '' }
  const peers = new Map<string, Peer>()
  const shown = new Set<string>()
  const waiters = new Map<string, Array<(ok: boolean) => void>>()
  const listeners = new Set<(leader: boolean) => void>()
  let leader = mode === 'solo'
  let stopped = false
  let releaseLock: (() => void) | null = null
  const abort = typeof AbortController === 'function' ? new AbortController() : null

  function setLeader(next: boolean) {
    if (leader === next) return
    leader = next
    for (const cb of [...listeners]) {
      try {
        cb(next)
      } catch {
        // 订阅者的错误不能影响协调
      }
    }
  }

  function freshPeers(): Peer[] {
    const now = env.now()
    return [...peers.values()].filter(p => now - p.at <= TAB_STATE_TTL_MS)
  }

  // 心跳模式下的选举：所有仍然新鲜的标签页（含自己）里，启动最早的（并列时按 id）当 leader。
  function recomputeHeartbeatLeader() {
    if (mode !== 'heartbeat') return
    let best = { startedAt, tabId }
    for (const p of freshPeers()) {
      if (p.startedAt < best.startedAt || (p.startedAt === best.startedAt && p.tabId < best.tabId)) {
        best = { startedAt: p.startedAt, tabId: p.tabId }
      }
    }
    setLeader(best.tabId === tabId)
  }

  function post(msg: Msg) {
    if (!channel || stopped) return
    try {
      channel.postMessage(msg)
    } catch {
      // 频道已关闭等：忽略，协调是尽力而为
    }
  }

  function publishState() {
    post({ k: 'state', tabId, startedAt, focused: self.focused, visible: self.visible, conversation: self.conversation })
  }

  function resolveWaiters(id: string, ok: boolean) {
    const list = waiters.get(id)
    if (!list) return
    waiters.delete(id)
    for (const fn of list) fn(ok)
  }

  function remember(id: string) {
    shown.delete(id)
    shown.add(id)
    while (shown.size > SHOWN_CAP) {
      const oldest = shown.values().next().value
      if (oldest === undefined) break
      shown.delete(oldest)
    }
  }

  if (channel) {
    channel.onmessage = ev => {
      if (stopped) return
      const msg = parseMsg(ev.data)
      if (!msg || msg.tabId === tabId) return
      switch (msg.k) {
        case 'state':
          peers.set(msg.tabId, {
            tabId: msg.tabId,
            startedAt: msg.startedAt,
            focused: msg.focused,
            visible: msg.visible,
            conversation: msg.conversation,
            at: env.now(),
          })
          recomputeHeartbeatLeader()
          break
        case 'hello':
          publishState()
          break
        case 'bye':
          peers.delete(msg.tabId)
          recomputeHeartbeatLeader()
          break
        case 'shown':
          remember(msg.id)
          resolveWaiters(msg.id, true)
          break
      }
    }
  }

  if (mode === 'locks' && env.locks) {
    // 持有锁的标签页就是 leader。回调返回一个永不自行结束的 Promise：锁一直握到标签页关闭（或调用 stop）。
    env
      .locks.request(LOCK_NAME, { mode: 'exclusive', signal: abort?.signal }, () => {
        if (stopped) return undefined
        setLeader(true)
        return new Promise<void>(resolve => {
          releaseLock = () => resolve()
        })
      })
      .catch(() => {
        // 主动中止（stop）或浏览器拒绝：退回「不是 leader」，由「已呈现」声明机制兜底
      })
  }

  const timer = channel
    ? env.setInterval(() => {
        if (stopped) return
        for (const [id, p] of peers) {
          if (env.now() - p.at > TAB_STATE_TTL_MS) peers.delete(id)
        }
        publishState()
        recomputeHeartbeatLeader()
      }, HEARTBEAT_MS)
    : null

  post({ k: 'hello', tabId })
  publishState()

  return {
    tabId,
    mode,
    isLeader: () => leader,
    onLeaderChange(cb) {
      listeners.add(cb)
      return () => listeners.delete(cb)
    },
    setSelf(next) {
      const changed = next.focused !== self.focused || next.visible !== self.visible || next.conversation !== self.conversation
      self = { ...next }
      if (changed) publishState()
    },
    states() {
      const mine: TabState = { tabId, focused: self.focused, visible: self.visible, conversation: self.conversation, at: env.now() }
      const others: TabState[] = freshPeers().map(p => ({
        tabId: p.tabId,
        focused: p.focused,
        visible: p.visible,
        conversation: p.conversation,
        at: p.at,
      }))
      return [mine, ...others]
    },
    announceShown(id) {
      remember(id)
      post({ k: 'shown', tabId, id })
      resolveWaiters(id, true)
    },
    hasShown: id => shown.has(id),
    waitShown(id, graceMs) {
      if (shown.has(id)) return Promise.resolve(true)
      return new Promise<boolean>(resolve => {
        const handle = env.setTimeout(() => {
          const list = waiters.get(id)
          if (list) {
            const rest = list.filter(fn => fn !== done)
            if (rest.length) waiters.set(id, rest)
            else waiters.delete(id)
          }
          resolve(false)
        }, graceMs)
        const done = (ok: boolean) => {
          env.clearTimeout(handle)
          resolve(ok)
        }
        const list = waiters.get(id) ?? []
        list.push(done)
        waiters.set(id, list)
      })
    },
    stop() {
      if (stopped) return
      post({ k: 'bye', tabId })
      stopped = true
      if (timer !== null) env.clearInterval(timer)
      if (channel) {
        channel.onmessage = null
        try {
          channel.close()
        } catch {
          // 忽略
        }
      }
      abort?.abort()
      releaseLock?.()
      for (const id of [...waiters.keys()]) resolveWaiters(id, false)
      listeners.clear()
    },
  }
}

/** 真实浏览器环境。 */
export function browserEnv(): CoordEnv {
  const nav = typeof navigator !== 'undefined' ? (navigator as Navigator & { locks?: LockManagerLike }) : null
  return {
    now: () => Date.now(),
    newId: () =>
      typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function'
        ? crypto.randomUUID()
        : `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`,
    openChannel: name => {
      if (typeof BroadcastChannel !== 'function') return null
      try {
        return new BroadcastChannel(name) as unknown as ChannelLike
      } catch {
        return null
      }
    },
    locks: nav?.locks && typeof nav.locks.request === 'function' ? nav.locks : null,
    setInterval: (fn, ms) => setInterval(fn, ms),
    clearInterval: h => clearInterval(h as ReturnType<typeof setInterval>),
    setTimeout: (fn, ms) => setTimeout(fn, ms),
    clearTimeout: h => clearTimeout(h as ReturnType<typeof setTimeout>),
  }
}
