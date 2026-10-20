// 分块（动态 import）加载失败：识别、提示文案与「只提示一次」的上报器（#137）。
//
// 为什么会失败：页面开着期间热更新（部署）会换掉带哈希的分块文件名，旧页面再去加载旧文件名就拿不到（404，或被回成 HTML）；
// 网络抖动也会让一次加载失败。这类失败以前没有任何出口——懒加载的弹窗点了没反应、路由页点了没反应，只能自己想到刷新网页。
// 现在统一：失败要让人看见，并给出出路（刷新）；多处同时失败只提示一条。

/** 失败发生在哪：弹窗之类的组件（刷新当前页）、路由页（刷新后直接前往目标页）。 */
export type ChunkFailureContext = { kind: 'component' } | { kind: 'route'; target: string }

/** 提示的固定编号：同一时刻只会有一条这样的提示，后来的（例如路由的）会就地替换前面的。 */
export const CHUNK_TOAST_ID = 'chunk-load-failed'

// 各浏览器与 Vite 对「分块没加载下来」的措辞。
const CHUNK_ERROR_PATTERNS: RegExp[] = [
  /failed to fetch dynamically imported module/i, // Chrome / Edge
  /error loading dynamically imported module/i, // Firefox
  /importing a module script failed/i, // Safari
  /failed to load module script/i, // 服务器回了 HTML（MIME 类型不对），多半是旧文件名被回退成首页
  /unable to preload css/i, // Vite：预加载样式失败
  /loading (css )?chunk [\w-]+ failed/i, // webpack 风格
  /chunkloaderror/i,
]

/** 这个错误是不是「分块没加载下来」（其余错误——比如模块自己执行时抛的——不该提示刷新）。 */
export function isChunkLoadError(err: unknown): boolean {
  if (err == null) return false
  let text = ''
  if (typeof err === 'string') text = err
  else if (typeof err === 'object') {
    const e = err as { name?: unknown; message?: unknown }
    text = `${typeof e.name === 'string' ? e.name : ''} ${typeof e.message === 'string' ? e.message : ''}`
  }
  return CHUNK_ERROR_PATTERNS.some(re => re.test(text))
}

export interface ChunkFailureCopy {
  title: string
  description: string
  actionLabel: string
}

/** 提示文案：不写技术细节，只说「多半是什么、怎么办」。 */
export function chunkFailureCopy(ctx: ChunkFailureContext): ChunkFailureCopy {
  if (ctx.kind === 'route') {
    return {
      title: '这个页面没能加载',
      description: '多半是刚更新了版本，当前页面还是旧的；也可能是网络抖了一下。刷新后会直接前往你要去的页面。',
      actionLabel: '刷新',
    }
  }
  return {
    title: '这个窗口没能加载',
    description: '多半是刚更新了版本，当前页面还是旧的；也可能是网络抖了一下。刷新后再试。',
    actionLabel: '刷新',
  }
}

export interface ChunkFailureNotice extends ChunkFailureCopy {
  id: string
  onAction: () => void
}

export interface ChunkFailureDeps {
  /** 呈现提示（同一个 id 的提示就地替换）。 */
  notify: (notice: ChunkFailureNotice) => void
  /** 点「刷新」：有 target 就整页前往它（拿到新的首页与分块），没有就刷新当前页。 */
  reload: (target?: string) => void
}

/**
 * 做一个上报器：传入任何错误，是分块加载失败就提示并返回 true，不是就什么也不做、返回 false（由调用方按自己的方式处理）。
 * 上报器本身不记状态；「只有一条」靠提示的固定编号保证。
 */
export function createChunkFailureReporter(deps: ChunkFailureDeps) {
  return function report(err: unknown, ctx: ChunkFailureContext = { kind: 'component' }): boolean {
    if (!isChunkLoadError(err)) return false
    const copy = chunkFailureCopy(ctx)
    deps.notify({
      id: CHUNK_TOAST_ID,
      ...copy,
      onAction: () => deps.reload(ctx.kind === 'route' ? ctx.target : undefined),
    })
    return true
  }
}
