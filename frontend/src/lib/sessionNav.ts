// 「回到会话页」的统一做法：任务清单页、项目页顶部的「会话」按钮都走这里；归档库这类没有固定上一页的整页，「返回」走 backOrSession。
import type { Router } from 'vue-router'

/**
 * 上一步是不是会话页（路径 '/'，带不带查询串、锚点都算）。
 * 读的是 vue-router 写进 history.state 的 back：从会话页点进来时它就是 '/'；直接打开链接、刷新后再前进等情形没有它或指向别处。
 */
export function cameFromSession(state: unknown): boolean {
  const back = (state as { back?: unknown } | null | undefined)?.back
  if (typeof back !== 'string') return false
  const path = back.split('#')[0]?.split('?')[0]
  return path === '/'
}

/**
 * 回到会话页。上一步就是会话页时用浏览器的「返回」（历史里不会叠出来回跳的两层，浏览器的返回键与这个按钮同一个去处）；
 * 否则直接前往会话页。state 默认取当前的 history.state，测试里可以直接传。
 */
export function backToSession(router: Pick<Router, 'back' | 'push'>, state: unknown = window.history.state): void {
  if (cameFromSession(state)) router.back()
  else void router.push('/')
}

/**
 * 上一步是不是应用里的页面：vue-router 写进 history.state 的 back 是个路径字符串。
 * 浏览器历史里有上一条，不等于上一条还在本应用里（可能是外站、新标签页的空白页），所以不能只看 history.length。
 */
export function hasInAppBack(state: unknown): boolean {
  const back = (state as { back?: unknown } | null | undefined)?.back
  return typeof back === 'string' && back.length > 0
}

/**
 * 没有固定上一页的整页（例如归档库）的「返回」：上一步在本应用里就用浏览器的返回（不叠历史、与浏览器返回键同一个去处）；
 * 否则（直接打开的链接、新标签页）前往会话页，不会把人退到应用外面去。
 */
export function backOrSession(router: Pick<Router, 'back' | 'push'>, state: unknown = window.history.state): void {
  if (hasInAppBack(state)) router.back()
  else void router.push('/')
}
