// 分块加载失败的接线（识别与文案见 lib/chunkError.ts，#137）：
//   - Vite 的 vite:preloadError：任何动态 import（路由页、懒加载组件、xterm 之类）失败都会触发；这里只提示，不 preventDefault，
//     让原来的调用方照常收到错误、收回自己的「加载中」状态；
//   - 路由导航时懒加载的路由页失败：vue-router 的 onError；提示里的「刷新」会整页前往目标页；
//   - 需要自己处理加载的组件（如会话页头的「终端」窗口）直接调用 reportChunkFailure。
import type { Router } from 'vue-router'
import { toast } from 'vue-sonner'
import { createChunkFailureReporter } from '../lib/chunkError'

export const reportChunkFailure = createChunkFailureReporter({
  notify: n =>
    toast.error(n.title, {
      id: n.id,
      description: n.description,
      duration: 20000,
      closeButton: true,
      action: { label: n.actionLabel, onClick: n.onAction },
    }),
  reload: target => {
    if (target) window.location.assign(target)
    else window.location.reload()
  },
})

export function installChunkFailureHandling(router: Router) {
  window.addEventListener('vite:preloadError', ev => {
    reportChunkFailure((ev as Event & { payload?: unknown }).payload)
  })
  router.onError((err, to) => {
    reportChunkFailure(err, { kind: 'route', target: to.fullPath })
  })
}
