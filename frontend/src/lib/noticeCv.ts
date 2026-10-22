// 预览视图里进度通知卡的离屏跳过渲染（#169）。
//
// 背景：异步模式下 Agent 可以连发上百条进度通知，生产上一个工作流的时间线末尾就有 127～176 张通知卡，
// 占了预览视图 DOM 元素的一半以上（127 张 ≈ 8200 个元素 / 15.5k），却一直全部渲染；只给它们加一条
// content-visibility:auto（实验，生产只读，约 3 Mbps）长任务合计降 30～45%、最长一个降 35～50%。
//
// 做法：最新 LIVE_TAIL_NOTICES 条保持完整渲染——用户停留最多的位置，贴底、新消息、回到底部都要精确的几何；
// 更早的套 pv-cv-notice（占位高度 140px，渲染过的会被浏览器记住真实高度）。通知总数不超过 LIVE_TAIL_NOTICES 时一个都不套，
// 行为与以前完全一样。不折叠、不截断：通知卡仍是完整正文（D-273 阶段 B 的决定），只是离开视口时浏览器不再为它做样式 / 布局 / 绘制。
//
// 只用在时间线「末尾」（比所有轮次都新的那一段，不在任何轮次组里）：轮次之间的间隙在轮次组（turn-group）里面，
// 更早的轮次已经整组套了 pv-cv，最近 20 轮要精确的几何（贴底、向上载入的锚点）。第一版把它也套在了间隙里，
// e2e/preview-longlist.mjs（seed-perf 的 150 轮、轮次之间 300 条通知）实测向上载入一批之后原来的第一轮漂了 197px，
// 而且是每一批都漂，所以只留末尾那一处。
import type { TimelineBlock } from './timeline'

export const LIVE_TAIL_NOTICES = 16

/** 通知条目的时间线键（与 PreviewMessageList 的 timelineEntries 一致）。 */
export function noticeEntryKey(sessionId: string): string {
  return `notice-${sessionId}`
}

/**
 * 保持完整渲染的通知卡的键集合（最新的 live 条）；通知总数不超过 live 时返回 null，表示一个都不套离屏跳过渲染。
 * 按时间取最新，不假定传入顺序；时间相同按传入顺序靠后者算新。
 */
export function liveNoticeKeys(
  notices: ReadonlyArray<{ session_id: string; created_at: string }>,
  live: number = LIVE_TAIL_NOTICES,
): ReadonlySet<string> | null {
  if (notices.length <= live) return null
  const stamped = notices.map((n, i) => {
    const t = Date.parse(n.created_at)
    return { key: noticeEntryKey(n.session_id), t: Number.isFinite(t) ? t : 0, i }
  })
  stamped.sort((a, b) => b.t - a.t || b.i - a.i)
  return new Set(stamped.slice(0, live).map(s => s.key))
}

/**
 * 一块时间线条目要不要套 pv-cv-notice：只有单张的通知卡、所在位置允许（cv）、且不在最新的 live 条里才套。
 * cv 由调用处决定：只有时间线末尾那一处传 true；轮次之间的间隙（含「提问与回复之间」）不套，见文件头的说明。
 */
export function noticeCvClass(live: ReadonlySet<string> | null, block: TimelineBlock, cv: boolean | undefined): string | undefined {
  if (!cv || !live) return undefined
  if (block.type !== 'single' || block.entry.kind !== 'notice') return undefined
  return live.has(block.key) ? undefined : 'pv-cv-notice'
}
