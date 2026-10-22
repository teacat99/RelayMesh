// 「没变就别往下传」：高频变化的源（每秒时钟、事件流）与它下游的渲染之间的隔离件（#215）。
//
// 背景：Vue 里一个 computed 重新算出来的是新对象，哪怕内容和上次一模一样，依赖它的渲染也会被唤醒（对象身份变了）。
// 侧栏有几十张卡、每张卡都读「现在几点」和 Agent 状态：时钟每秒走一下、Agent 每来一个事件，几十张卡就整张重画一遍，
// 而其中绝大多数的显示根本没变。stableComputed 在重算之后与上一次的结果比一下，相同就把旧对象原样返回，
// 下游看到的是「没有变化」，什么都不会重画。
import { computed, type ComputedRef } from 'vue'

/**
 * 两个值是否逐项相同。只认 JSON 形状的东西：原始值、数组、普通对象（键的顺序不计）；
 * 函数、Date、Map 之类按引用比较——会话状态 / 身份这些记录里没有它们，用到时再扩。
 */
export function deepEqual(a: unknown, b: unknown): boolean {
  if (Object.is(a, b)) return true
  if (typeof a !== 'object' || typeof b !== 'object' || a === null || b === null) return false
  const arrA = Array.isArray(a)
  if (arrA !== Array.isArray(b)) return false
  if (arrA) {
    const x = a as unknown[]
    const y = b as unknown[]
    if (x.length !== y.length) return false
    for (let i = 0; i < x.length; i++) if (!deepEqual(x[i], y[i])) return false
    return true
  }
  const protoA = Object.getPrototypeOf(a)
  if (protoA !== Object.getPrototypeOf(b) || (protoA !== Object.prototype && protoA !== null)) return false
  const x = a as Record<string, unknown>
  const y = b as Record<string, unknown>
  const keys = Object.keys(x)
  if (keys.length !== Object.keys(y).length) return false
  for (const k of keys) {
    if (!Object.prototype.hasOwnProperty.call(y, k) || !deepEqual(x[k], y[k])) return false
  }
  return true
}

/**
 * 与 computed 一样，但重算结果与上一次相同（默认逐项比较）时返回上一次的对象本身：
 * 依赖它的渲染 / watch 不会被唤醒。getter 里读到的高频源（时钟、事件）仍然会让它重算，
 * 只是重算的代价（几十微秒的纯计算）比整张卡重画（虚拟节点 + 比对）小得多。
 */
export function stableComputed<T>(getter: () => T, equals: (a: T, b: T) => boolean = deepEqual): ComputedRef<T> {
  return computed<T>(prev => {
    const next = getter()
    return prev !== undefined && equals(prev, next) ? prev : next
  })
}
