import { onBeforeUnmount, onMounted, ref, type Ref } from 'vue'
import type { Terminal } from '@xterm/xterm'
import { ScrollGate } from '../lib/terminal/scrollGate'

// 终端已经滚到最顶端之后，用户还在往上滚：说明想看更早的内容，这一次滚动就直接去加载（onFire），不再要求「再滚一次」确认。
// 一次手势只触发一次（含触控板惯性滚动的尾巴）；手势判定的纯逻辑在 lib/terminal/scrollGate.ts，这里只负责把滚轮 / 触摸 / 键盘事件接上去。
//
// 其实没有更早的内容可加载时，onFire 返回 false：`hint` 变为 true 并保持一小段时间，调用方据此显示「已是记录的开头」
// 这类只陈述事实的状态提示；向下滚、离开顶端、键入或超时之后撤销。

export interface TopOverscrollOptions {
  host: Ref<HTMLElement | null>
  term: () => Terminal | null
  /** 当前是否参与：全屏程序、鼠标上报、对话框打开、正在加载、没有更早的内容可加载时返回 false，事件被忽略。 */
  enabled: () => boolean
  /**
   * 到顶后又往上滚了一次（一次新的手势）：去加载。
   * 返回 false 表示其实没有可加载的内容：`hint` 会显示一段时间（再滚会重新计时），不会一滚就消失；其他返回值表示已经开始加载。
   */
  onFire: () => boolean | void
}

/** 手指向下拖动超过这个距离，才算一次「往上滚」。 */
const TOUCH_PULL_PX = 28
/** 「没有更多内容」的状态提示显示多久。 */
export const HINT_TTL_MS = 6000

export function useTopOverscroll(o: TopOverscrollOptions) {
  const gate = new ScrollGate()
  /** 到顶后没有更多内容可加载：状态提示是否在显示。 */
  const hint = ref(false)
  let ttl: ReturnType<typeof setTimeout> | null = null
  let touchStartY = 0
  let touchCounted = false

  function clearHint() {
    if (ttl) clearTimeout(ttl)
    ttl = null
    hint.value = false
  }

  /** 向下滚、离开顶端、键入、超时：提示撤销，手势也忘掉。 */
  function dismiss() {
    gate.reset()
    clearHint()
  }

  function showHint() {
    if (ttl) clearTimeout(ttl)
    hint.value = true
    ttl = setTimeout(dismiss, HINT_TTL_MS)
  }

  function atTop(): boolean {
    const t = o.term()
    return !!t && t.buffer.active.viewportY <= 0
  }

  function upAtTop() {
    if (gate.upAtTop(Date.now()) !== 'fire') return
    // 新的一次手势：能加载就开始加载（加载中的进度由调用方显示），没有可加载的内容才陈述事实。
    if (o.onFire() === false) showHint()
    else clearHint()
  }

  function onWheel(ev: WheelEvent) {
    // Ctrl / ⌘ + 滚轮是缩放字号，不是滚动。
    if (ev.ctrlKey || ev.metaKey) return
    if (ev.deltaY > 0) return dismiss()
    if (ev.deltaY === 0) return
    if (!o.enabled()) {
      // 正在加载 / 全屏程序在用滚轮：不触发。但手势若还在继续，记作同一次手势，免得加载结束后它的尾巴又触发一遍。
      gate.hold(Date.now())
      return clearHint()
    }
    if (!atTop()) return dismiss()
    upAtTop()
  }

  function onTouchStart(ev: TouchEvent) {
    if (ev.touches.length !== 1) return
    touchStartY = ev.touches[0].clientY
    touchCounted = false
    gate.newGesture()
  }

  function onTouchMove(ev: TouchEvent) {
    if (ev.touches.length !== 1) return
    if (!o.enabled()) return dismiss()
    const dy = ev.touches[0].clientY - touchStartY
    if (dy < -10) return dismiss() // 往回推：不是在找更早的内容
    if (dy < TOUCH_PULL_PX || touchCounted) return
    if (!atTop()) return dismiss()
    // 一次拖动只算一次：拖动继续时，加载结束后不会再触发一遍。
    touchCounted = true
    upAtTop()
  }

  function onKeydown(ev: KeyboardEvent) {
    if (!o.enabled()) return
    if (ev.key === 'PageUp' && ev.shiftKey) {
      if (atTop()) upAtTop()
      else dismiss()
      return
    }
    // 纯修饰键不算输入；其他按键（敲命令）说明用户已经不在翻历史了。
    if (ev.key === 'Shift' || ev.key === 'Control' || ev.key === 'Alt' || ev.key === 'Meta') return
    if (hint.value) dismiss()
  }

  let attached: HTMLElement | null = null

  function attach(el: HTMLElement) {
    attached = el
    el.addEventListener('wheel', onWheel, { passive: true, capture: true })
    el.addEventListener('touchstart', onTouchStart, { passive: true, capture: true })
    el.addEventListener('touchmove', onTouchMove, { passive: true, capture: true })
    el.addEventListener('keydown', onKeydown, { capture: true })
  }

  function detach() {
    const el = attached
    if (!el) return
    el.removeEventListener('wheel', onWheel, { capture: true })
    el.removeEventListener('touchstart', onTouchStart, { capture: true })
    el.removeEventListener('touchmove', onTouchMove, { capture: true })
    el.removeEventListener('keydown', onKeydown, { capture: true })
    attached = null
  }

  onMounted(() => {
    if (o.host.value) attach(o.host.value)
  })
  onBeforeUnmount(() => {
    detach()
    if (ttl) clearTimeout(ttl)
  })

  return { hint }
}
