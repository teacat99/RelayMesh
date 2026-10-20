import { nextTick } from 'vue'
import type { Router } from 'vue-router'
import { MODE_SWITCH_SAFETY_MS, modeSwitchPlan } from '../lib/modeTransition'
import { useFxStore } from '../stores/fx'

// RelayMesh ↔ ExecMesh 切换动画：用 View Transitions 把「旧画面」截下来，路由切换、界面更新都在截图之后发生，
// 再由样式（assets/globals.css「项目切换动画」）让旧画面化作粒子消散、露出下面的新页面。
//
// 流程：
//   beforeResolve（懒加载的页面都已就绪）→ 记下方向到 <html data-mode-switch> → startViewTransition：
//     浏览器先截好旧画面，再调用我们的回调——回调里放行导航（路由切换、界面更新就在这一步发生），
//     并返回一个「等新页面渲染好」的 Promise；
//   afterEach → 等 Vue 把这一轮界面更新渲染完（nextTick，再让出一个宏任务）→ 放行回调的 Promise，浏览器截新画面并开始动画；
//     注意：回调等待期间浏览器会冻结渲染，requestAnimationFrame 在这段时间里不会触发（实测会一直等到兜底超时），
//     所以这里只能用微任务 / 定时器，不能用 rAF 去「等一帧」；
//   动画结束（finished）→ 去掉 data-mode-switch。
// 不满足条件（不支持、设置里关了、要求减少动态效果、页面不可见、不是跨项目）一律什么都不做，导航照常。

interface ViewTransitionLike {
  finished: Promise<unknown>
}
type DocumentWithVT = Document & { startViewTransition?: (update: () => Promise<void> | void) => ViewTransitionLike }

export function installModeTransition(router: Router) {
  // 等待「新页面渲染好」的回调；有值说明有一次切换动画正在等路由收尾。
  let release: (() => void) | null = null
  let safety: ReturnType<typeof setTimeout> | null = null

  function settle() {
    if (safety) {
      clearTimeout(safety)
      safety = null
    }
    const done = release
    release = null
    done?.()
  }

  router.beforeResolve(async (to, from) => {
    // 上一次还没收尾（连续快速切换）：先收掉，免得两次动画互相等
    settle()

    const doc = document as DocumentWithVT
    const direction = modeSwitchPlan(from.matched.length ? from.fullPath : null, to.fullPath, {
      supported: typeof doc.startViewTransition === 'function',
      enabled: useFxStore().modeSwitch,
      reducedMotion: typeof window.matchMedia === 'function' && window.matchMedia('(prefers-reduced-motion: reduce)').matches,
      hidden: document.hidden,
    })
    if (!direction) return

    const root = document.documentElement
    const clear = () => {
      if (root.dataset.modeSwitch === direction) delete root.dataset.modeSwitch
    }
    root.dataset.modeSwitch = direction

    await new Promise<void>(resolve => {
      try {
        const transition = doc.startViewTransition!.call(doc, () => {
          // 旧画面已截好：放行导航，路由切换与界面更新在这一步里发生；返回的 Promise 等 afterEach 收尾后再放行
          resolve()
          return new Promise<void>(done => {
            release = done
          })
        })
        transition.finished.then(clear, clear)
        safety = setTimeout(settle, MODE_SWITCH_SAFETY_MS)
      } catch {
        // 浏览器拒绝发起（例如文档状态不允许）：不要影响导航
        clear()
        resolve()
      }
    })
  })

  router.afterEach(() => {
    if (!release) return
    // 路由已切换：等 Vue 把这一轮界面更新渲染完（含 onMounted / post 副作用），再让出一个宏任务让 Promise 链上的后续更新落地，
    // 然后放行，浏览器开始截新画面并播放动画。不能用 requestAnimationFrame：回调等待期间渲染被冻结，rAF 不会触发。
    void nextTick().then(() => setTimeout(settle, 0))
  })

  // 守卫里抛了非导航类的错误时 afterEach 不会被调用，这里同样要收尾
  router.onError(() => settle())
}
