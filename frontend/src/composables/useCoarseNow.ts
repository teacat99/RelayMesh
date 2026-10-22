import { onScopeDispose, ref, type Ref } from 'vue'

/**
 * 慢速的共享时钟（30 秒一跳）：显示精度是分钟的组件用它（任务页每一行的「N 分钟前」）。
 *
 * 与 useSharedNow（秒级，驱动倒计时 / 走字的计时）的分工：这里的值一跳就要换掉整列任务行的相对时间，所以更要遵守同一条规矩——
 * 不要把它当 prop 从父组件往一整批子组件里灌：prop 一变，子组件不管模板里用没用到它都会被整个重画一遍；
 * 任务页 200 行时，30 秒一次的整页重画实测要占主线程 28~66 ms（#219）。谁要用就自己在 computed 里读，
 * 计算结果（「3 分钟前」）没变的行就不会被唤醒；大多数行的标签几十分钟甚至几小时才换一次。
 *
 * 引用计数：第一个订阅者进来才起表，最后一个退订（所在的作用域结束 / 组件卸载）就停表；停表期间值会变旧，
 * 所以第一个订阅者进来时先对一次表。已经在走的时候新进来的订阅者拿到的值最多旧 30 秒，与分钟精度的标签相比可以接受（原来任务页的页级时钟也是这个精度）。
 */
export const COARSE_NOW_INTERVAL_MS = 30_000

const coarseNow = ref(Date.now())
let timer: ReturnType<typeof setInterval> | null = null
let subscribers = 0

export function useCoarseNow(): Readonly<Ref<number>> {
  if (subscribers === 0) {
    coarseNow.value = Date.now()
    timer = setInterval(() => {
      coarseNow.value = Date.now()
    }, COARSE_NOW_INTERVAL_MS)
  }
  subscribers++
  onScopeDispose(() => {
    subscribers--
    if (subscribers <= 0) {
      subscribers = 0
      if (timer) clearInterval(timer)
      timer = null
    }
  })
  return coarseNow
}

/** 只给测试用：当前有多少订阅者、表是否在走。 */
export function coarseNowState(): { subscribers: number; running: boolean } {
  return { subscribers, running: timer !== null }
}
