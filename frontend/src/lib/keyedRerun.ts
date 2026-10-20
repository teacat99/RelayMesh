// 按钥匙合并的取数：同一把钥匙上，进行中又来了新请求时「不能丢，结束后补一次」。
//
// 为什么不能直接丢：进行中的这一次可能是在新变化发生之前发出的（服务端按发出那一刻的数据回答），它回来的数据是旧的；
// 此时又有事件说「变了」，如果这个请求被直接丢掉，界面就停在旧数据上，要等下一次定时校准才会纠正。
// 页面主线程忙的时候（后台标签、低端设备、软件渲染），一次请求「进行中」可以拖得比事件合并的间隔还长，这条路径就会真的走到。
//
// 规则：没有进行中的 → 立刻执行；有进行中的 → 记一笔「要再来一次」并返回（多次请求只补一次），当前这次结束后再执行一次；
// 补的那一次执行时同样适用这条规则。不同钥匙互不影响。run 里自己负责处理错误（抛出也不会让这个机制卡住：finally 里照常补取）。

export function createKeyedRerun<K>(run: (key: K) => Promise<void>): (key: K) => Promise<void> {
  const inflight = new Set<K>()
  const rerun = new Set<K>()

  async function request(key: K): Promise<void> {
    if (inflight.has(key)) {
      rerun.add(key)
      return
    }
    inflight.add(key)
    try {
      await run(key)
    } finally {
      inflight.delete(key)
      if (rerun.delete(key)) void request(key).catch(() => {})
    }
  }

  return request
}
