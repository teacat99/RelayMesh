// 接口请求的在途计数实例（#186）：api/client.ts 的拦截器里 begin / end，utils/lazyChunks 的空闲预取用它判断「首屏请求安静下来了」。
// 单独成文件是为了让 api/client.ts 不必引入块登记表。判断逻辑在 lib/netIdle.ts（可单测），这里只绑定浏览器的定时器。
import { createNetIdle } from '../lib/netIdle'

export const netIdle = createNetIdle({
  setTimeout: (cb, ms) => window.setTimeout(cb, ms),
  clearTimeout: h => window.clearTimeout(h as number),
})
