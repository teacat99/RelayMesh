// 日历日的纯逻辑：是不是同一天（按本机时区）、日期分隔条上的文字。
// 聊天视图（chatView.ts）与预览视图的日期分隔（daySeparators.ts）共用，不依赖 Vue 与 DOM。

export function sameDay(a: Date, b: Date): boolean {
  return a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate()
}

/** 日期分隔条上的文字：今天 / 昨天 / 10月16日 / 2025年10月16日（跨年才带年份）。 */
export function dateLabel(at: Date, now: Date): string {
  if (sameDay(at, now)) return '今天'
  const yesterday = new Date(now)
  yesterday.setDate(now.getDate() - 1)
  if (sameDay(at, yesterday)) return '昨天'
  const md = `${at.getMonth() + 1}月${at.getDate()}日`
  return at.getFullYear() === now.getFullYear() ? md : `${at.getFullYear()}年${md}`
}
