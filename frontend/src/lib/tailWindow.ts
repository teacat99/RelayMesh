// 预览视图时间线「末尾」的渐进渲染（#169 第三期）。
//
// 背景：异步模式下 Agent 可以连发上百条进度通知，生产上一个工作流的末尾就有 130 多张通知卡，平均每张 65 个元素，
// 合计约 9700 个——占预览视图 DOM 的 61%，打开会话时主线程要为它们满负荷干 3.5 秒以上（Chrome 追踪：一次样式重算 2.66 s、
// 涉及两万多个元素），而用户一进来看的只是最底下的最新几条。第一期的离屏跳过渲染（lib/noticeCv.ts）只省了布局与绘制，
// 元素照建、样式照算；实验（只注入 CSS、末尾只留最新 30 块）热遍第一轮 −37%，真正不渲染只会更好。
//
// 做法：末尾先只渲染最新的 TAIL_INITIAL_BLOCKS 块，更早的在浏览器空闲时一批一批补齐（每批 TAIL_BATCH_BLOCKS 块）。
// 不隐藏、不折叠、不截断：内容一个不少，只是晚几百毫秒出现。块的个数不超过 TAIL_INITIAL_BLOCKS 时一块都不延后，行为与以前完全一样。
//
// 窗口用「起点下标」表示（末尾的第 start 块起渲染，start 之前的暂不渲染）：新通知追加在尾部，不改变更早的下标，
// 所以新消息进来时窗口只会变长，不会滑动（滑动会把最旧的那块从 DOM 里拆掉）。
// 起点在第一次看到末尾时「钉住」；之后只会往前（补齐、被点名要看某一块）、不会往后。
//
// 滚动：补齐发生在已渲染的第一块之前。用户在已渲染部分里面（插入点在视口顶边之上）时，要把已渲染的第一块钉在原位
// （useScrollSettle.hold），视口里的内容才纹丝不动；用户正在看插入点上面（轮次）时不用钉；
// 用户停在底部时也不钉锚点，改走「贴底」（#252，理由见 tailHoldMode）。

import { PIN_TOLERANCE_PX } from './stickBottom'

/**
 * 末尾先渲染的最新块数（块 = 单条条目或一摞卡片，见 lib/timeline 的 groupBlocks）。
 * 30 → 12（#186 C2b-1）：一屏加缓冲够了，更早的由空闲补齐 / 用户向上够到时立刻补。生产形状夹具（154 轮 + 203 条通知）上与
 * 预览视图的首批轮次数 4（PreviewMessageList 的 INITIAL_VISIBLE_ROUNDS）一起成对对照：打开后头 6 秒的长任务合计中位数 −30%、
 * 阻塞帧合计 −34%（数据与限度见 .cursor/planning/2026-10-23-186-C-open-phase-plan.md §10）。
 */
export const TAIL_INITIAL_BLOCKS = 12
/** 每次空闲补齐的块数：一批约 700～800 个元素，主线程占用控制在百毫秒以内。 */
export const TAIL_BATCH_BLOCKS = 12
/** 被点名要看某一块时，起点比目标再往前留几块，免得目标贴在窗口边缘又被下一批补齐挤动。 */
export const TAIL_REVEAL_SLACK = 2
/** 已渲染的第一块的顶边离视口顶边不到这个距离（向上 px）时，不等空闲、立刻补齐：用户正在向上够它。 */
export const TAIL_PROBE_PX = 900
/** requestIdleCallback 的最长等待：浏览器一直忙时也要往前走，免得补齐被饿死。 */
export const TAIL_FILL_IDLE_TIMEOUT_MS = 300
/** 不支持 requestIdleCallback 的浏览器（Safari）退回定时器的间隔。 */
export const TAIL_FILL_FALLBACK_MS = 80

/** 首次钉住的起点：总块数不超过 initial 时为 0（全部渲染）。 */
export function initialTailStart(total: number, initial: number = TAIL_INITIAL_BLOCKS): number {
  return total > initial ? total - initial : 0
}

/**
 * 钉住的起点（null = 还没钉）与块总数 → 实际起点。
 * 起点不会超过「只渲染最新 initial 块」的位置：总数缩水（卡片重新分摞、条目被收走）时，不会算出一个把末尾渲染空的起点。
 */
export function effectiveTailStart(pinned: number | null, total: number, initial: number = TAIL_INITIAL_BLOCKS): number {
  const cap = initialTailStart(total, initial)
  if (pinned === null) return cap
  return Math.max(0, Math.min(pinned, cap))
}

/** 补齐一批：起点往前挪 batch 块（到 0 为止）。 */
export function nextTailStart(start: number, batch: number = TAIL_BATCH_BLOCKS): number {
  return start > batch ? start - batch : 0
}

/** 要让下标 index 的块渲染出来：起点至少退到它前面 slack 块；已经够了就原样返回；index 无效（-1）原样返回。 */
export function revealTailStart(start: number, index: number, slack: number = TAIL_REVEAL_SLACK): number {
  if (index < 0) return start
  return Math.min(start, Math.max(0, index - slack))
}

/** 已渲染的第一块的顶边相对视口顶边的位置（向下为正）→ 要不要不等空闲、立刻补齐一批。 */
export function shouldFillNow(firstTop: number, probePx: number = TAIL_PROBE_PX): boolean {
  return firstTop > -probePx
}

/** 同一个位置 → 补齐时要不要把第一块钉在原位：它在视口顶边或更上面 = 视口整个在插入点之下，不钉的话内容会被推走。 */
export function shouldHoldFirst(firstTop: number): boolean {
  return firstTop <= 0
}

/** 视口底边离内容底边不超过这么多 px 就算「停在底部」（与 lib/stickBottom.PIN_TOLERANCE_PX 同值：scrollTop 按设备像素取整，留一点余量）。 */
export const TAIL_BOTTOM_PX = PIN_TOLERANCE_PX

/** 补齐一批时用什么保住视口（#252）。 */
export type TailHoldMode = 'bottom' | 'anchor' | 'none'

/**
 * 补齐一批时用什么保住视口：
 *   - 'bottom'：用户停在底部（视口底边离内容底边不超过 TAIL_BOTTOM_PX）。不钉锚点，交给「贴底」（lib/stickBottom + followBottom）——
 *     停在底部时不变的是「底部」，不是某一张卡在屏幕上的位置。钉锚点只认后者：补齐的那几百毫秒里，锚点下方的内容会变
 *     （占位高度换成真实高度、时间线末尾晚到的卡片与新通知），贴底与钉锚点先后写 scrollTop，钉锚点写得最晚，页面就停在底部上方
 *     几百到几千 px 处，而且 pinnedAtBottom 随之作废、之后没有谁再贴回去（生产长会话冷开实测离底 296～4400px，#252）；
 *   - 'anchor'：没贴底，但视口在已渲染的第一块或它下面（用户正在看已渲染的通知）：钉住第一块，视口里的内容纹丝不动；
 *   - 'none'：用户正在看插入点上面（轮次）：不钉，免得把视口推走。
 * bottomGap：视口底边离内容底边的距离；调用方在「用户刚表达过向上看」时传 Infinity（他正要离开底部，别再按贴底算）。
 */
export function tailHoldMode(firstTop: number, bottomGap: number, bottomPx: number = TAIL_BOTTOM_PX): TailHoldMode {
  if (bottomGap <= bottomPx) return 'bottom'
  return shouldHoldFirst(firstTop) ? 'anchor' : 'none'
}
