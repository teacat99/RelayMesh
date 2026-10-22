// 运行：pnpm --dir frontend run test:unit
// 统一消息卡的词汇表（#120 · D-273 阶段 B）：类型 → 色调 / 标签 / 发送方的对照、色调样式表、头部时间文案。
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import dayjs from 'dayjs'
import {
  CARD_HUE_TONE_CLASS,
  CARD_SENDERS,
  CARD_TONE_CLASS,
  CARD_TYPES,
  cardTimeText,
  sessionStatusBadge,
  type CardSender,
  type CardTone,
  type CardType,
} from './messageCard'

const TYPES = Object.keys(CARD_TYPES) as CardType[]
const TONES = Object.keys(CARD_TONE_CLASS) as CardTone[]
const SENDERS = Object.keys(CARD_SENDERS) as CardSender[]

describe('消息卡：类型对照表', () => {
  it('覆盖设计稿里的十种类型，外加群聊（阶段 E-d）的两种：member / self', () => {
    assert.deepEqual([...TYPES].sort(), ['append', 'ask', 'capsule', 'decision', 'guard', 'member', 'notice', 'plan', 'reply', 'report', 'self', 'task'])
  })

  it('群聊的两种：成员的话是智能体侧（靠左、灰），您的话是您侧（靠右、绿）', () => {
    assert.deepEqual(CARD_TYPES.member, { label: '成员', tone: 'zinc', sender: 'agent' })
    assert.deepEqual(CARD_TYPES.self, { label: '您', tone: 'green', sender: 'user' })
  })

  it('每种类型都有非空标签，标签互不相同（头部一眼能分清）', () => {
    const labels = TYPES.map(t => CARD_TYPES[t].label)
    for (const l of labels) assert.ok(l.length > 0 && l.length <= 2, `标签应是 1~2 个字：${l}`)
    assert.equal(new Set(labels).size, labels.length, `标签重复：${labels.join(' / ')}`)
  })

  it('色调与发送方都指向存在的条目', () => {
    for (const t of TYPES) {
      assert.ok(TONES.includes(CARD_TYPES[t].tone), `${t} 的色调 ${CARD_TYPES[t].tone} 没有样式`)
      assert.ok(SENDERS.includes(CARD_TYPES[t].sender), `${t} 的发送方 ${CARD_TYPES[t].sender} 没有头像`)
    }
  })

  it('色调语义对照设计稿：计划红 / 提问琥珀 / 进度天蓝 / 汇报灰 / 您说的话绿 / 决策紫 / 事件石板', () => {
    const tone = (t: CardType) => CARD_TYPES[t].tone
    assert.equal(tone('plan'), 'red')
    assert.equal(tone('ask'), 'amber')
    assert.equal(tone('notice'), 'sky')
    assert.equal(tone('report'), 'zinc')
    assert.equal(tone('reply'), 'green')
    assert.equal(tone('append'), 'green')
    assert.equal(tone('decision'), 'violet')
    assert.equal(tone('task'), 'slate')
    assert.equal(tone('capsule'), 'slate')
  })

  it('发送方：您的回复与追加是「您」，守护告警与恢复胶囊是「系统」，其余是智能体', () => {
    const sender = (t: CardType) => CARD_TYPES[t].sender
    assert.equal(sender('reply'), 'user')
    assert.equal(sender('append'), 'user')
    assert.equal(sender('guard'), 'system')
    assert.equal(sender('capsule'), 'system')
    for (const t of ['notice', 'report', 'ask', 'plan', 'decision', 'task'] as CardType[]) assert.equal(sender(t), 'agent', t)
  })

  it('进度通知 = 智能体 · 进度 · 天蓝（用户反馈 #218 的主角）', () => {
    assert.deepEqual(CARD_TYPES.notice, { label: '进度', tone: 'sky', sender: 'agent' })
  })
})

describe('消息卡：群聊成员的色调（阶段 E-d）', () => {
  it('只定义 --card-tone，亮 / 暗各一个，色相来自内联变量 --card-hue（字面量类名，Tailwind 才扫得到）', () => {
    assert.match(
      CARD_HUE_TONE_CLASS,
      /^\[--card-tone:hsl\(var\(--card-hue\)_\d+%_\d+%\)\] dark:\[--card-tone:hsl\(var\(--card-hue\)_\d+%_\d+%\)\]$/,
    )
  })

  it('暗色比亮色更亮（暗色背景上要提亮），与固定色调表同一个规矩', () => {
    const [light, dark] = [...CARD_HUE_TONE_CLASS.matchAll(/_(\d+)%\)\]/g)].map(m => Number(m[1]))
    assert.ok(dark > light, `暗色明度 ${dark} 应高于亮色 ${light}`)
  })
})

describe('消息卡：色调样式表', () => {
  it('每个色调只定义 --card-tone 一个变量，亮 / 暗各一个十六进制色', () => {
    for (const tone of TONES) {
      assert.match(
        CARD_TONE_CLASS[tone],
        /^\[--card-tone:#[0-9a-f]{6}\] dark:\[--card-tone:#[0-9a-f]{6}\]$/,
        `${tone} 的样式应是「亮色 + dark: 暗色」的字面量类名（Tailwind 才扫得到）`
      )
    }
  })

  it('亮 / 暗两套颜色不同（暗色背景上要提亮）', () => {
    for (const tone of TONES) {
      const [light, dark] = CARD_TONE_CLASS[tone].match(/#[0-9a-f]{6}/g) as string[]
      assert.notEqual(light, dark, `${tone} 的亮暗色相同`)
    }
  })

  it('色调之间的颜色互不相同', () => {
    const light = TONES.map(t => (CARD_TONE_CLASS[t].match(/#[0-9a-f]{6}/) as RegExpMatchArray)[0])
    assert.equal(new Set(light).size, light.length)
  })
})

describe('消息卡：发送方', () => {
  it('头像是单个字，名称非空', () => {
    for (const s of SENDERS) {
      assert.equal([...CARD_SENDERS[s].avatar].length, 1, `${s} 的头像应是单个字`)
      assert.ok(CARD_SENDERS[s].label.length > 0)
    }
  })

  it('三类发送方的头像互不相同', () => {
    const avatars = SENDERS.map(s => CARD_SENDERS[s].avatar)
    assert.equal(new Set(avatars).size, avatars.length)
  })
})

describe('消息卡：头部时间文案（与经典进度通知一致）', () => {
  const now = dayjs('2026-10-17T12:00:00')
  // dayjs('…T…') 不带 Z，按本地时区解析；转成 ISO 再交给 cardTimeText，格式化回本地时间，所以断言与运行环境的时区无关
  const at = (local: string) => dayjs(local).toISOString()

  it('今天只显示 HH:mm', () => {
    assert.equal(cardTimeText(at('2026-10-17T09:05:00'), now), '09:05')
    assert.equal(cardTimeText(at('2026-10-17T23:59:00'), now), '23:59')
  })

  it('今年显示 MM-DD·HH:mm', () => {
    assert.equal(cardTimeText(at('2026-10-16T21:30:00'), now), '10-16·21:30')
    assert.equal(cardTimeText(at('2026-01-02T03:04:00'), now), '01-02·03:04')
  })

  it('更早带年份', () => {
    assert.equal(cardTimeText(at('2025-12-31T08:00:00'), now), '2025·12-31·08:00')
  })

  it('没有时间或时间无效时是空串（头部不画时间）', () => {
    assert.equal(cardTimeText(undefined, now), '')
    assert.equal(cardTimeText('', now), '')
    assert.equal(cardTimeText('not-a-date', now), '')
  })
})

describe('消息卡：轮次状态标签（汇报卡头部的状态槽；样式与经典列表一致，文字取预览状态词汇表，阶段 D2）', () => {
  it('五种状态各有标签与徽标样式', () => {
    assert.deepEqual(sessionStatusBadge('completed'), { label: '已处理', variant: 'default' })
    assert.deepEqual(sessionStatusBadge('pending'), { label: '待您处理', variant: 'secondary' })
    assert.deepEqual(sessionStatusBadge('cancelled'), { label: '已取消', variant: 'outline' })
    assert.deepEqual(sessionStatusBadge('timeout'), { label: '已超时', variant: 'destructive' })
    assert.deepEqual(sessionStatusBadge('archived'), { label: '已归档', variant: 'outline' })
  })

  it('没见过的状态原样显示，不报错', () => {
    assert.deepEqual(sessionStatusBadge('weird'), { label: 'weird', variant: 'outline' })
    assert.deepEqual(sessionStatusBadge(''), { label: '', variant: 'outline' })
  })
})
