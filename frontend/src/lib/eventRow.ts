// 预览视图「事件行」的版式常量与小工具（#120 · D-273 阶段 C2；取代阶段 1 的「紧凑行」）。
//
// 任务进展、已处理的提问 / 决策、文档决策汇总、守护告警、恢复胶囊、「N 张卡片」汇总——这些「没有正文的事件」，
// 在预览视图里统一成同一种单行：
//   左色条（按类型的色调） · 类型标签 · [编号] · 标题 · 一行预览 · 时间 · [状态] · [展开箭头]    行末可再放按钮
// 固定 32px 高：上下边框各 1px + 里面一行 30px（18px 的标签居中）。预览只在宽屏（≥ sm）出现，手机上让位给标题。
// 这一行本身由 EventRowHead.vue 摆放；外面的盒子（左色条、边框、展开体）由各叶子用 EVENT_BOX 自己画，因为展开后的内容各不相同。
//
// 经典视图不用这里的任何东西（除了 firstLinePreview，右侧轨也在用）：各叶子组件的 compact 参数默认关闭，经典列表不传它。
// e2e（scripts/e2e-terminal/e2e/preview-view.mjs）按 EVENT_ROW_PX / EVENT_ROW_MAX_PX 量折叠状态下每种事件行的实际高度。

/** 事件行折叠时的高度（px）：上下边框各 1px + 一行 30px。 */
export const EVENT_ROW_PX = 32

/** 一行里面的高度（px）：EVENT_ROW_PX 扣掉上下边框。 */
export const EVENT_LINE_PX = EVENT_ROW_PX - 2

/** 折叠状态下事件行允许的最大高度（px）：基线 + 4px，留给亚像素取整与字体回退。 */
export const EVENT_ROW_MAX_PX = EVENT_ROW_PX + 4

/**
 * 事件行的盒子：1px 描边、左边 3px 色条（取 --card-tone，所以要再配一个 CARD_TONE_CLASS）、卡片底色。
 * 展开后的内容（回看详情、胶囊明细）放在这个盒子里、那一行的下面，用 border-t 隔开。
 */
export const EVENT_BOX = 'relative rounded-lg border border-border border-l-[3px] border-l-[color:var(--card-tone)] bg-card text-xs text-card-foreground'

/**
 * 把一段 Markdown 压成「一行预览」：取第一行有内容的文字，去掉标题 / 引用 / 列表 / 粗体 / 行内代码 / 链接 / 图片的标记，
 * 超过 max 个字符用省略号截断。预览行本身还会被 CSS 单行截断，这里的 max 只是不往 DOM 里塞整篇正文。
 * 没有可显示的文字（空串、只有分隔线或代码围栏）返回空串。
 */
export function firstLinePreview(markdown: string, max = 160): string {
  for (const raw of (markdown ?? '').split(/\r?\n/)) {
    let line = raw.trim()
    if (!line) continue
    // 代码围栏的开关行本身不是内容，跳过它去找里面的第一行
    if (/^(```|~~~)/.test(line)) continue
    line = line
      .replace(/^#{1,6}\s+/, '') // 标题
      .replace(/^>\s?/, '') // 引用
      .replace(/^(?:[-*+]|\d+[.)])\s+(?:\[[ xX]\]\s+)?/, '') // 列表 / 任务列表
      .replace(/!\[([^\]]*)\]\([^)]*\)/g, '$1') // 图片 → 说明文字
      .replace(/\[([^\]]*)\]\([^)]*\)/g, '$1') // 链接 → 链接文字
      .replace(/\*\*(.+?)\*\*/g, '$1') // 粗体
      .replace(/~~(.+?)~~/g, '$1') // 删除线
      .replace(/`([^`]+)`/g, '$1') // 行内代码
      .replace(/\s+/g, ' ')
      .trim()
    if (!line) continue
    // 水平分隔线与表格的对齐行不当预览
    if (/^[-*_]{3,}$/.test(line) || /^\|?[\s:|-]+\|?$/.test(line)) continue
    return line.length > max ? `${line.slice(0, max - 1)}…` : line
  }
  return ''
}
