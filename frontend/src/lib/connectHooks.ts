// 实时流（首次或重新）连上之后要「补取一次」的仓库登记处（#243）。
//
// 实时流不补发事件：页面载入到流真正连上之间、以及断线期间，服务端发出的事件没有任何订阅者听见。
// 所以每次连上都要把「可能已经过期的快照」重取一次，首次与重连一样。
//   - 每个页面都有的仓库（设置 / 托管自驾任务 / 聊天……）：session.ts 的 onopen 直接调用它们的补取函数。
//   - 只有部分页面用到的仓库（任务清单 / 确认项）与惰性加载的仓库（项目文档 / 常驻提示词 / 异步档位）：
//     由 session.ts 直接 use…Store() 会把设置页这类用不到它们的页面也实例化出来、白发请求，惰性的三个还会在启动时就拉下代码块。
//     所以它们在自己的 setup 里按名字登记；连上时只对「已经被用过」（= 已登记）的仓库补取。
//     没登记 = 还没被用过 = 没有旧数据要校准，用到的时候本来就会取最新的。
//
// 同名重复登记以后一次为准：热更新或测试里仓库被重建，也不会让同一个仓库补取两次。

export type ConnectHook = () => void | Promise<unknown>

const hooks = new Map<string, ConnectHook>()

export function registerConnectHook(name: string, hook: ConnectHook): void {
  hooks.set(name, hook)
}

export function unregisterConnectHook(name: string): void {
  hooks.delete(name)
}

/** 当前登记的仓库名（按登记顺序；测试 / 诊断用）。 */
export function connectHookNames(): string[] {
  return [...hooks.keys()]
}

function reportToConsole(name: string, err: unknown): void {
  console.warn(`[SSE] 连上之后补取「${name}」失败:`, err)
}

/**
 * 逐个执行已登记的补取。某个仓库的补取抛错（同步异常或异步拒绝）不能拖累其余仓库：只报告、继续。
 * 执行的是调用瞬间登记表的快照：补取期间新登记的仓库不在这一轮里（它刚被用到，数据本来就是新的）。
 * report 默认写 console.warn；测试里传入自己的记录函数。
 */
export function runConnectHooks(report: (name: string, err: unknown) => void = reportToConsole): void {
  for (const [name, hook] of [...hooks]) {
    try {
      const pending = hook() as Promise<unknown> | undefined
      if (pending && typeof pending.then === 'function') {
        pending.then(undefined, (err: unknown) => report(name, err))
      }
    } catch (err) {
      report(name, err)
    }
  }
}
