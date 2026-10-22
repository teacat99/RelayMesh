// 预览视图的状态词汇表（#120 · D-273「状态词汇全局统一」，阶段 D2）。
//
// 卡片状态槽（汇报 / 您的回复 / 您的追加）、事件行里的待处理 / 已处理、右侧轨的悬停提示，只用下面这几个词；
// 经典视图保持原有说法（待 AI 提取 / AI 已接收 / 等待确认 / 已完成……），一个字没动。
//   待您处理：要您动手的——等回复的汇报、待答复的提问 / 计划 / 文档决策
//   已处理：  您已经处理完的
//   待读取：  您发出的话，智能体还没读到（提交的回复 / 追加）
//   已读：    智能体读到了
//   已取消 / 已超时：汇报那一轮的结局
// 悬停提示（title）仍写完整的说明（谁在什么时候怎么读到的），词汇表只管徽标上那两三个字。
// 纯常量与小函数，不依赖 Vue，方便单元测试与被 lib 层（右侧轨）引用。

export const STATUS_WORD = {
  pending: '待您处理',
  done: '已处理',
  unread: '待读取',
  read: '已读',
  cancelled: '已取消',
  timeout: '已超时',
} as const

export type StatusWord = (typeof STATUS_WORD)[keyof typeof STATUS_WORD]

/** 您的确认回复：智能体取走了没有。 */
export function replyBadgeWord(consumed: boolean): StatusWord {
  return consumed ? STATUS_WORD.read : STATUS_WORD.unread
}

/**
 * 您的追加：已排队 → 待读取；智能体读到过（已读取、已确认）→ 已读；已过期等其他阶段沿用调用方给的原词。
 * 「已读取」与「已确认」在徽标上是同一个词，区别靠悬停全称、hook 提取详情和有没有「撤回」按钮看。
 */
export function inboxBadgeWord(s: { stage: string; badge: string }): string {
  switch (s.stage) {
    case 'queued':
      return STATUS_WORD.unread
    case 'delivered':
    case 'acked':
      return STATUS_WORD.read
    default:
      return s.badge
  }
}
