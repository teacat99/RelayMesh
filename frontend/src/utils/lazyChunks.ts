// 懒加载块的登记表与预取接线（#186）：整页 / 大对话框改成「点开时才取」之后，真正点开时别让人等——
//   · 首屏的接口请求都回来并安静一小会儿之后，在浏览器空闲时按顺序悄悄预取（startIdlePrefetch，慢网络上不抢首屏带宽；省流量模式不做）；
//   · 鼠标悬停 / 键盘聚焦 / 触摸到带 data-prefetch="<块名>" 的入口时立刻取（installPrefetchHints，全局一个事件委托）。
// 登记表里的 `() => import('…')` 与各处 lazyPage / lazyPart / lazyDialog 用的是同一个说明符，Vite 会落在同一个块里。
// 逻辑（顺序、去重、失败不抛、安静判断）在 lib/prefetch.ts 与 lib/netIdle.ts，这里只做浏览器接线。
import { browserPrefetchEnv, createPrefetcher, type ChunkLoader } from '../lib/prefetch'
import { netIdle } from './netActivity'

export const chunkLoaders = {
  settings: () => import('../components/SettingsForm.vue'),
  work: () => import('../components/work/WorkPage.vue'),
  workDetail: () => import('../components/work/WorkItemDetail.vue'),
  projects: () => import('../components/projects/ProjectsPage.vue'),
  docs: () => import('../components/docs/DocsPage.vue'),
  group: () => import('../components/group/GroupPage.vue'),
  unlock: () => import('../components/terminal/UnlockDialog.vue'),
  presetUpdate: () => import('../components/settings/PresetUpdateDialog.vue'),
  enableAgent: () => import('../components/terminal/EnableAgentDialog.vue'),
} satisfies Record<string, ChunkLoader>

export type ChunkName = keyof typeof chunkLoaders

export const prefetcher = createPrefetcher(chunkLoaders, browserPrefetchEnv())

// 预取顺序 = 用户接下来最可能点开的顺序：设置（齿轮）、任务清单、项目、文档、群聊；三个小弹窗最后。
const IDLE_ORDER: ChunkName[] = ['settings', 'work', 'workDetail', 'projects', 'docs', 'group', 'enableAgent', 'unlock', 'presetUpdate']

/** 等接口请求安静 800 ms（最多等 30 秒，等不到就放弃这次空闲预取），再按顺序空闲预取。只需调用一次。 */
let idleStarted = false
export async function startIdlePrefetch(): Promise<void> {
  if (idleStarted) return
  idleStarted = true
  if (!(await netIdle.whenQuiet(800, 30000))) return
  await prefetcher.idle(IDLE_ORDER)
}

/** 悬停 / 聚焦 / 触摸到带 data-prefetch 的入口就立刻取对应的块；返回卸载函数。 */
export function installPrefetchHints(doc: Document = document): () => void {
  const handler = (ev: Event) => {
    const target = ev.target
    if (!(target instanceof Element)) return
    const name = target.closest('[data-prefetch]')?.getAttribute('data-prefetch')
    if (name) void prefetcher.now(name)
  }
  const events = ['pointerover', 'focusin', 'touchstart'] as const
  for (const e of events) doc.addEventListener(e, handler, { passive: true })
  return () => {
    for (const e of events) doc.removeEventListener(e, handler)
  }
}
