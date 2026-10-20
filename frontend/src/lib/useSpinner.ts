// 盲文旋转符的浏览器侧绑定（#130）：一个全页共用的 SpinnerClock + 一个响应式帧号。
// 纯逻辑与测试在 spinner.ts / spinner.test.ts。
import { onBeforeUnmount, onMounted, readonly, ref, type Ref } from 'vue'
import { SpinnerClock, type SpinnerEnv } from '@/lib/spinner'

const frame = ref(0)

const REDUCED_QUERY = '(prefers-reduced-motion: reduce)'

const browserEnv: SpinnerEnv = {
  hidden: () => typeof document !== 'undefined' && document.hidden,
  reduced: () => typeof window !== 'undefined' && typeof window.matchMedia === 'function' && window.matchMedia(REDUCED_QUERY).matches,
  setInterval: (fn, ms) => window.setInterval(fn, ms),
  clearInterval: handle => window.clearInterval(handle as number),
  onChange: fn => {
    const media = typeof window.matchMedia === 'function' ? window.matchMedia(REDUCED_QUERY) : null
    document.addEventListener('visibilitychange', fn)
    media?.addEventListener?.('change', fn)
    return () => {
      document.removeEventListener('visibilitychange', fn)
      media?.removeEventListener?.('change', fn)
    }
  },
}

let clock: SpinnerClock | undefined

function sharedClock(): SpinnerClock {
  if (!clock) clock = new SpinnerClock(browserEnv, i => (frame.value = i))
  return clock
}

/**
 * 组件挂载时登记、卸载时释放；返回全页共用的帧号（只读）。
 * 同一时刻无论有多少个指示符，页面里只有一个定时器。
 */
export function useSpinnerFrame(): Readonly<Ref<number>> {
  let release: (() => void) | undefined
  onMounted(() => {
    release = sharedClock().subscribe()
  })
  onBeforeUnmount(() => {
    release?.()
    release = undefined
  })
  return readonly(frame)
}
