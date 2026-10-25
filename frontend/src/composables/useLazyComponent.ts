import { markRaw, shallowRef, type Component, type ShallowRef } from 'vue'
import { toast } from 'vue-sonner'
import { reportChunkFailure } from '../utils/chunkFailure'

/**
 * 按需加载的弹窗组件：第一次要打开时才取分块并挂载，不拖慢页面本身（分支面板、新建分支窗口等，#236）。
 *
 * 不用 defineAsyncComponent：它会把第一次失败的加载永久缓存，之后每次点击都不再重试，页面开着期间热更新换掉了分块文件名时
 * 弹窗就一直点不开，只能刷新网页（#137）。这里自己管：加载成功才缓存组件；失败不缓存、出一条带「刷新」的提示，下一次点击重新尝试。
 *
 * 用法：`const { component: Panel, ensure: ensurePanel } = useLazyComponent(() => import('./X.vue'), '某某窗口')`；
 * 模板里 `<component :is="Panel" v-if="Panel" v-model:open="open" />`；打开前 `if (await ensurePanel()) open.value = true`。
 */
export function useLazyComponent(load: () => Promise<{ default: Component }>, label: string) {
  const component: ShallowRef<Component | null> = shallowRef(null)
  let loading: Promise<boolean> | null = null

  function ensure(): Promise<boolean> {
    if (component.value) return Promise.resolve(true)
    loading ??= load()
      .then(m => {
        component.value = markRaw(m.default)
        return true
      })
      .catch((err: unknown) => {
        if (!reportChunkFailure(err)) {
          console.error(`[lazy-component] ${label}加载出错`, err)
          toast.error(`${label}加载出错`, { description: err instanceof Error ? err.message : String(err) })
        }
        return false
      })
      .finally(() => {
        loading = null
      })
    return loading
  }

  return { component, ensure }
}
