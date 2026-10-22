// 页面生命周期闸门：页面离开（进入前进 / 后退缓存，或被卸载）时把长连接还给浏览器，回来时再接上。
//
// 为什么需要：事件流（SSE）与流式请求会连同连接一起被冻结在浏览器的前进 / 后退缓存（bfcache）里。
// 明文 HTTP/1.1 下同一个站点最多 6 条连接，连续整页跳转几次之后连接名额被一堆冻结的旧页面占满，
// 新页面的请求要等十几秒甚至更久才拿得到连接（D-206 第 5 项实测：从第 4 次整页跳转起经常卡 10~57 秒）。
// 做法是浏览器官方建议的那种：pagehide 时主动关闭，pageshow（来自 bfcache）时立即重连。
//
// 这里只放与具体连接无关的部分：何时该关、何时该开。关什么、怎么开由调用方（session.ts / execRuns.ts）决定。

export interface PageLifecycleGate {
  /** 页面当前处在「已离开」状态：连接已经交还，不应自己重连。 */
  readonly suspended: boolean
  /** pagehide：返回 true 表示这一次需要真正去关闭连接；重复的 pagehide 返回 false。 */
  hide(): boolean
  /** pageshow / 重新可见：返回 true 表示需要恢复连接；只有之前确实离开过才会返回 true，首次加载的 pageshow 不会。 */
  show(): boolean
}

export function createPageLifecycleGate(): PageLifecycleGate {
  let suspended = false
  return {
    get suspended() {
      return suspended
    },
    hide() {
      if (suspended) return false
      suspended = true
      return true
    },
    show() {
      if (!suspended) return false
      suspended = false
      return true
    },
  }
}

export interface PageLifecycleHandlers {
  /** 页面离开：关闭长连接、停掉重连与看门狗定时器。不要把它当成「用户手动断开」，页面回来后要接着连。 */
  suspend: () => void
  /** 页面回来：立即重连并校准状态。persisted = 页面是从前进 / 后退缓存里恢复的。 */
  resume: (persisted: boolean) => void
}

type Listenable = Pick<EventTarget, 'addEventListener' | 'removeEventListener'>

/**
 * 监听 pagehide / pageshow，并以「重新可见」作兜底（个别浏览器在恢复时不触发 pageshow）。
 * 返回取消监听的函数。没有 window（测试 / SSR）时什么也不做。
 */
export function watchPageLifecycle(
  handlers: PageLifecycleHandlers,
  win: Listenable | undefined = typeof window !== 'undefined' ? window : undefined,
  doc: (Listenable & { visibilityState?: string }) | undefined = typeof document !== 'undefined' ? document : undefined,
): () => void {
  if (!win) return () => {}
  const gate = createPageLifecycleGate()
  const onHide = () => {
    if (gate.hide()) handlers.suspend()
  }
  const onShow = (e: Event) => {
    if (gate.show()) handlers.resume(Boolean((e as { persisted?: boolean }).persisted))
  }
  const onVisible = () => {
    if (doc?.visibilityState === 'visible' && gate.show()) handlers.resume(false)
  }
  win.addEventListener('pagehide', onHide)
  win.addEventListener('pageshow', onShow)
  doc?.addEventListener('visibilitychange', onVisible)
  return () => {
    win.removeEventListener('pagehide', onHide)
    win.removeEventListener('pageshow', onShow)
    doc?.removeEventListener('visibilitychange', onVisible)
  }
}
