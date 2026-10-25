// 懒加载组件的统一包装（#186）：入口脚本 480 KB gzip 里有约 40% 是整页 / 大对话框，首次打开只需要会话页本身，
// 其余的（任务清单、项目、文档、群聊、设置、解锁 / 启用 Agent 对话框）改成点开时才取，并在首屏安静后悄悄预取（utils/lazyChunks）。
//
// 三种包装，区别只在「块还没取回时看见什么」：
//   · lazyPage   整页：画出页头骨架 + 「加载中」（汉堡 / 展开侧栏能点，版面不跳）；失败时给「刷新页面」；
//   · lazyPart   对话框 / 面板里的内容区：「加载中」/ 失败的紧凑版；
//   · lazyDialog 全局挂载、由 store 状态打开的小弹窗：块取回之前什么也不画（弹窗本来就是点了才出现），失败只弹全局「刷新」提示。
// 返回值保持原组件的类型（泛型 T），模板里的属性与事件仍然被 vue-tsc 检查。
// 加载失败都会先走 reportChunkFailure：分块没取下来（多半是页面开着期间发了新版本）弹一条带「刷新」的提示；
// 浏览器会记住失败的模块，同一页面里重试没用，所以出路只有刷新。非分块错误（模块自己执行时抛的）不弹，交给 Vue 的错误通道。
import { defineAsyncComponent, h, type AsyncComponentLoader, type Component, type FunctionalComponent } from 'vue'
import LazyPageLoading from '../components/shell/LazyPageLoading.vue'
import LazyPageError from '../components/shell/LazyPageError.vue'
import PageState from '../components/shell/PageState.vue'
import Button from '../components/ui/button/Button.vue'
import { reportChunkFailure } from './chunkFailure'

type OnError = (error: Error, retry: () => void, fail: () => void, attempts: number) => void

function failHandler(onFail?: (error: unknown) => void): OnError {
  return (error, _retry, fail) => {
    reportChunkFailure(error)
    onFail?.(error)
    fail()
  }
}

/** 整页（任务清单 / 项目 / 文档 / 群聊）。 */
export function lazyPage<T extends Component>(loader: AsyncComponentLoader<T>): T {
  return defineAsyncComponent<T>({
    loader,
    loadingComponent: LazyPageLoading,
    errorComponent: LazyPageError,
    delay: 0,
    onError: failHandler(),
  })
}

const PartLoading: FunctionalComponent = () => h(PageState, { kind: 'loading', title: '正在载入…', class: 'h-full', 'data-testid': 'lazy-part-loading' })

const PartError: FunctionalComponent = () =>
  h(
    PageState,
    {
      kind: 'error',
      title: '这一块没能载入',
      hint: '多半是刚更新了版本，当前页面还是旧的；也可能是网络抖了一下。刷新后再试。',
      class: 'h-full',
      'data-testid': 'lazy-part-error',
    },
    {
      actions: () =>
        h(Button, { size: 'sm', variant: 'outline', 'data-testid': 'lazy-reload', onClick: () => window.location.reload() }, () => '刷新页面'),
    },
  )

/** 对话框 / 面板里的内容区（外壳在入口里，内容懒加载）。 */
export function lazyPart<T extends Component>(loader: AsyncComponentLoader<T>): T {
  return defineAsyncComponent<T>({
    loader,
    loadingComponent: PartLoading,
    errorComponent: PartError,
    delay: 0,
    onError: failHandler(),
  })
}

/**
 * 全局挂载、由 store 状态打开的小弹窗：取回之前什么也不画。
 * onFail 用来收回「已经向用户要了、却永远出不来」的状态（例如解锁对话框：取消这次等待，免得调用方一直挂着）。
 */
export function lazyDialog<T extends Component>(loader: AsyncComponentLoader<T>, onFail?: (error: unknown) => void): T {
  return defineAsyncComponent<T>({
    loader,
    // 失败后什么也不画：全局已经弹了「刷新」提示，空组件也避免把错误当成未处理异常抛到控制台
    errorComponent: () => null,
    delay: 0,
    onError: failHandler(onFail),
  })
}
