// 统一消息卡的词汇表（#120 · D-273 阶段 B；只给预览视图用，经典视图不碰）。
//
// 背景：正文体系原本是按 1:1 设计的（一轮一张大卡 + 时间流里零散的气泡 / 紧凑行），异步交流里「智能体进度」
// 本质就是正文，不该折叠，所以整套正文换成同一种卡片语法：
//   色条（按类型）· 发送方头像 · 类型标签 · 标题 · 时间 · （状态）· 更多菜单 ⋯ · 正文全文 · 脚注
// 这里只放纯数据与纯函数——类型到「色调 / 标签 / 发送方」的查表、色调到样式的查表、时间文案——
// 组件（PreviewMessageCard 等）只负责摆放，所以可以脱离 DOM 单测。
import dayjs from 'dayjs'
import { STATUS_WORD } from './statusWords'

/**
 * 正文里出现的消息类型；阶段 B 起 notice 在用，阶段 C 接入 report / reply / append，其余类型随后续批次接入同一张卡。
 * 阶段 E-d（预览视图里的群聊）再加两种：member = 群里某位成员（智能体）说的话，self = 您在群里说的话；
 * 它们不显示类型标签（群里的话没有「汇报 / 回复」之分），只借类型决定靠左还是靠右、默认色调。
 */
export type CardType = 'report' | 'notice' | 'ask' | 'plan' | 'reply' | 'append' | 'decision' | 'task' | 'capsule' | 'guard' | 'member' | 'self'

/** 色调：色条、头像底色、类型标签的描边与字色都取自它。 */
export type CardTone = 'zinc' | 'sky' | 'amber' | 'red' | 'green' | 'violet' | 'slate'

/** 发送方：智能体 / 您 / 系统（守护、恢复胶囊这类不是任何一方说的话）。 */
export type CardSender = 'agent' | 'user' | 'system'

export interface CardTypeMeta {
  /** 类型标签的字 */
  label: string
  tone: CardTone
  sender: CardSender
}

/**
 * 类型 → 色调 / 标签 / 发送方。
 * 约定：红 = 要您拍板（计划），琥珀 = 要您回答（提问）/ 需留意（告警），天蓝 = 智能体的进度，
 * 灰 = 例行汇报，绿 = 您说的话，紫 = 文档决策，石板 = 事件（任务进展、恢复胶囊）。
 */
export const CARD_TYPES: Record<CardType, CardTypeMeta> = {
  report: { label: '汇报', tone: 'zinc', sender: 'agent' },
  notice: { label: '进度', tone: 'sky', sender: 'agent' },
  ask: { label: '提问', tone: 'amber', sender: 'agent' },
  plan: { label: '计划', tone: 'red', sender: 'agent' },
  reply: { label: '回复', tone: 'green', sender: 'user' },
  append: { label: '追加', tone: 'green', sender: 'user' },
  decision: { label: '决策', tone: 'violet', sender: 'agent' },
  task: { label: '任务', tone: 'slate', sender: 'agent' },
  capsule: { label: '恢复', tone: 'slate', sender: 'system' },
  guard: { label: '告警', tone: 'amber', sender: 'system' },
  member: { label: '成员', tone: 'zinc', sender: 'agent' },
  self: { label: '您', tone: 'green', sender: 'user' }
}

/** 色调 → 样式：只定义 --card-tone 一个变量（亮 / 暗两套），其余颜色在组件里用 var(--card-tone) 派生。类名必须是字面量，Tailwind 才扫得到。 */
export const CARD_TONE_CLASS: Record<CardTone, string> = {
  zinc: '[--card-tone:#3f3f46] dark:[--card-tone:#d4d4d8]',
  sky: '[--card-tone:#0284c7] dark:[--card-tone:#38bdf8]',
  amber: '[--card-tone:#d97706] dark:[--card-tone:#fbbf24]',
  red: '[--card-tone:#dc2626] dark:[--card-tone:#f87171]',
  green: '[--card-tone:#3b8259] dark:[--card-tone:#4ade80]',
  violet: '[--card-tone:#7c3aed] dark:[--card-tone:#a78bfa]',
  slate: '[--card-tone:#64748b] dark:[--card-tone:#94a3b8]'
}

/**
 * 群聊成员的色调（阶段 E-d）：色相取自调色板（lib/groupCard 的 assignMemberHues / memberHue），由卡片根上的内联变量 --card-hue 给出；
 * 饱和度与明度固定（亮色偏深、暗色偏浅），让头像字、色条与标签在两种主题下对比度都够用。
 * 和 CARD_TONE_CLASS 一样只定义 --card-tone，其余颜色仍由组件用 var(--card-tone) 派生。类名必须是字面量，Tailwind 才扫得到。
 */
export const CARD_HUE_TONE_CLASS = '[--card-tone:hsl(var(--card-hue)_55%_36%)] dark:[--card-tone:hsl(var(--card-hue)_70%_68%)]'

/** 发送方 → 头像字 / 名称。头像是一个 16px 的色点加一个字，不用图片，所以任何主题下都清晰。 */
export const CARD_SENDERS: Record<CardSender, { avatar: string; label: string }> = {
  agent: { avatar: '智', label: '智能体' },
  user: { avatar: '您', label: '您' },
  system: { avatar: '系', label: '系统' }
}

/**
 * 小标签（类型、状态）的几何：高 18px = 行高 16 + 上下边框各 1。带边框的小标签必须 leading-4——
 * 默认的 18px 行高会让它变成 20px，把卡片头部与事件行撑高。颜色另配（见下面两个）。
 */
export const CARD_CHIP_BASE = 'inline-flex h-[18px] shrink-0 items-center rounded-sm border px-1.5 text-[10px] leading-4'

/** 取色调的小标签：描边、底、字都取 --card-tone（元素自己或祖先上要配 CARD_TONE_CLASS，所以一行里也能混用别的色调）。 */
export const CARD_CHIP_CLASS = `${CARD_CHIP_BASE} border-[color:color-mix(in_srgb,var(--card-tone)_45%,transparent)] bg-[color:color-mix(in_srgb,var(--card-tone)_7%,transparent)] text-[color:var(--card-tone)]`

/** 中性的小标签：「已处理」这类不需要色调的状态。 */
export const CARD_CHIP_PLAIN_CLASS = `${CARD_CHIP_BASE} border-border text-muted-foreground`

/**
 * 卡片正文里的「作答区」面板（待处理的提问 / 计划的选项、备注、提交）：描边与底都取 --card-tone 的一小部分，
 * 让要您动手的那一块从正文里浮出来，又不抢色条与状态标签的风头。元素自己或祖先上要有 CARD_TONE_CLASS（统一卡的根上就有）。
 */
export const CARD_PANEL_CLASS = 'rounded-md border border-[color:color-mix(in_srgb,var(--card-tone)_30%,transparent)] bg-[color:color-mix(in_srgb,var(--card-tone)_4%,transparent)]'

/** 「更多」菜单的一项。 */
export interface CardMenuItem {
  key: string
  label: string
  onSelect: () => void
  /** 破坏性动作（删除）：文字画成破坏色。 */
  destructive?: boolean
  /** 选中后菜单不关闭：「两段式确认」的第一击用它——菜单留着，这一项的文字换成「再点一次确认」。 */
  keepOpen?: boolean
}

/**
 * 轮次（会话）状态 → 头部状态标签的文字与徽标样式。样式与经典列表一致（只是搬出来让汇报卡也能用），
 * 文字取预览视图的状态词汇表（lib/statusWords，阶段 D2）：等回复 → 待您处理、已确认 → 已处理；经典列表仍是「等待确认 / 已完成」。
 */
export type StatusBadgeVariant = 'default' | 'secondary' | 'outline' | 'destructive'
export function sessionStatusBadge(status: string): { label: string; variant: StatusBadgeVariant } {
  switch (status) {
    case 'completed': return { label: STATUS_WORD.done, variant: 'default' }
    case 'pending': return { label: STATUS_WORD.pending, variant: 'secondary' }
    case 'cancelled': return { label: STATUS_WORD.cancelled, variant: 'outline' }
    case 'timeout': return { label: STATUS_WORD.timeout, variant: 'destructive' }
    case 'archived': return { label: '已归档', variant: 'outline' }
    default: return { label: status, variant: 'outline' }
  }
}

/**
 * 卡片头部的时间文案，与经典的进度通知一致：今天只显示 HH:mm，今年显示 MM-DD·HH:mm，更早带年份。
 * 时间无效时返回空串（头部不画时间）。now 可注入，方便测试。
 */
export function cardTimeText(iso: string | undefined, now: dayjs.Dayjs = dayjs()): string {
  if (!iso) return ''
  const target = dayjs(iso)
  if (!target.isValid()) return ''
  if (target.isSame(now, 'day')) return target.format('HH:mm')
  if (target.isSame(now, 'year')) return target.format('MM-DD·HH:mm')
  return target.format('YYYY·MM-DD·HH:mm')
}
