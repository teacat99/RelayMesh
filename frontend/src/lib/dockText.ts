// 输入区（FeedbackInputDock）里调用方可以覆盖的两处文案：发送按钮的文字与输入框的占位提示（#249）。
//
// 背景：输入区早就声明了 buttonText / placeholder 两个属性，托管自驾页也传了（「发送最高指令」「向指挥端投递人工最高指令……」），
// 可是组件里没有任何地方读它们——文案一直取自「当前选中的会话」的状态（提交反馈 / 追加反馈 / 追加指令）。
// 在任务页上这些字样是误导的：那里发出去的是给指挥端的最高指令，不是对某个会话的反馈。
// 调用方不传（默认空串）时保持原样，会话页因此不受影响；纯文字的空白串当作没传。

export interface SubmitButtonInfo {
  /** 按钮上的主文案。 */
  label: string
  /** 提交过程中的文案。 */
  submittingLabel: string
  /** 悬停提示。 */
  description: string
}

/**
 * 调用方指定了按钮文字就覆盖主文案。
 * 悬停提示是按「当前会话的状态」写的（如「向 AI 提交针对当前汇报的即时反馈」），文案换了就不再适用，清空。
 * 提交中的文案（「提交中...」「暂存中...」）只是进度，保留。
 */
export function withButtonText(info: SubmitButtonInfo, custom: string | undefined): SubmitButtonInfo {
  const text = (custom ?? '').trim()
  return text ? { ...info, label: text, description: '' } : info
}

/** 调用方指定了占位提示就用它的，否则用由会话状态推出来的那一句（fallback）。 */
export function pickPlaceholder(custom: string | undefined, fallback: string): string {
  const text = (custom ?? '').trim()
  return text || fallback
}
