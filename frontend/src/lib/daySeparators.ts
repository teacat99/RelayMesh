// 预览视图的日期分隔（#120 阶段 E-b）：一天里的第一个顶层条目前画一条「今天 / 昨天 / 日期」小条。
// 纯逻辑，不依赖 Vue 与 DOM：列表把「当前显示的顶层条目（按显示顺序）」交进来，拿回「哪个条目前要画、写什么」。
import { dateLabel, sameDay } from './dateLabel'

export interface DayItem {
  /** 条目的键：间隙里的块用 block.key，轮次用 session_id（两者不会重名）。 */
  key: string
  /** 条目的时间（ISO 字符串或毫秒）；解析不了的条目不参与，当作和前一个同一天（例如守护告警描述的是「现在」）。 */
  at: string | number | undefined | null
}

/**
 * 找出每个日历日（本机时区）的第一个条目，返回 键 → 标签。
 * items 必须按显示顺序给出：时间线按时间升序，所以「和前一个条目不是同一天」就是新的一天开始。
 * 第一个条目永远有标记：往上翻页载入更早的轮次时，顶上总能看到它是哪天的内容。
 * 传进来的条目少一个（例如更早的轮次还没载入）只会改变开头的标记，不会让中间的标记错位。
 */
export function daySeparators(items: ReadonlyArray<DayItem>, now: Date): Record<string, string> {
  const marks: Record<string, string> = {}
  let prev: Date | null = null
  for (const it of items) {
    const t = typeof it.at === 'number' ? it.at : Date.parse(String(it.at ?? ''))
    if (!Number.isFinite(t)) continue
    const at = new Date(t)
    if (!prev || !sameDay(prev, at)) marks[it.key] = dateLabel(at, now)
    prev = at
  }
  return marks
}
