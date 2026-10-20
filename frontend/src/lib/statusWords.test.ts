// 预览视图状态词汇表的单元测试（#120 · D-273 阶段 D2）：词表本身、回复 / 追加的徽标词，以及「预览卡片只从词汇表取词、经典不碰」的源码防线。
import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, it } from 'node:test'
import { inboxBadgeWord, replyBadgeWord, STATUS_WORD } from './statusWords'

/** 单测是编译到缓存目录里跑的，不能用 import.meta.url 找源码；从当前目录往 frontend/src 找（与其他源码级防线同一做法）。 */
function srcDir(): string {
  for (const base of [join(process.cwd(), 'src'), join(process.cwd(), 'frontend', 'src')]) {
    if (existsSync(join(base, 'lib', 'statusWords.test.ts'))) return base
  }
  throw new Error('找不到 frontend/src，请在仓库根目录或 frontend 目录下运行测试')
}
const read = (rel: string) => readFileSync(join(srcDir(), rel), 'utf8')

describe('预览状态词汇表', () => {
  it('六个词：待您处理 / 已处理 / 待读取 / 已读 / 已取消 / 已超时', () => {
    assert.deepEqual({ ...STATUS_WORD }, {
      pending: '待您处理',
      done: '已处理',
      unread: '待读取',
      read: '已读',
      cancelled: '已取消',
      timeout: '已超时',
    })
  })

  it('您的回复：没被取走 = 待读取，取走了 = 已读', () => {
    assert.equal(replyBadgeWord(false), '待读取')
    assert.equal(replyBadgeWord(true), '已读')
  })

  it('您的追加：已排队 = 待读取；已读取 / 已确认都是已读；已过期等其他阶段沿用原词', () => {
    assert.equal(inboxBadgeWord({ stage: 'queued', badge: '待 AI 提取' }), '待读取')
    assert.equal(inboxBadgeWord({ stage: 'delivered', badge: 'AI 已读取' }), '已读')
    assert.equal(inboxBadgeWord({ stage: 'acked', badge: 'AI 已接收' }), '已读')
    assert.equal(inboxBadgeWord({ stage: 'expired', badge: '已过期' }), '已过期')
    assert.equal(inboxBadgeWord({ stage: 'whatever', badge: '原词' }), '原词')
  })
})

describe('预览卡片只从词汇表取词，经典一个字没动', () => {
  it('预览的卡片 / 事件行 / 右侧轨都引用 STATUS_WORD（或它的小函数）', () => {
    assert.match(read('components/feedback/preview-view/PreviewReplyCard.vue'), /STATUS_WORD\.unread[\s\S]*STATUS_WORD\.read/)
    assert.match(read('components/feedback/preview-view/PreviewInboxCard.vue'), /inboxBadgeWord\(status\)/)
    assert.match(read('components/feedback/preview-view/PreviewConfirmCard.vue'), /STATUS_WORD\.pending/)
    assert.match(read('components/confirms/DecisionGroupCard.vue'), /done \? STATUS_WORD\.done : STATUS_WORD\.pending/)
    assert.match(read('components/confirms/ConfirmTimelineCard.vue'), /status === 'answered' \? STATUS_WORD\.done/)
    assert.match(read('lib/timelineRail.ts'), /pending: STATUS_WORD\.pending, done: STATUS_WORD\.done/)
    assert.match(read('lib/messageCard.ts'), /label: STATUS_WORD\.pending/)
  })

  it('经典列表与经典叶子的说法不变：待 AI 提取 / AI 已接收 / 等待确认 / 待决策', () => {
    const classicList = read('components/feedback/FeedbackMessageList.vue')
    assert.ok(classicList.includes('<span>待 AI 提取</span>') && classicList.includes('<span>AI 已接收</span>'))
    assert.ok(classicList.includes("label: '等待确认'"))
    assert.ok(!/STATUS_WORD/.test(classicList), '经典列表不引用预览的词汇表')
    const decision = read('components/confirms/DecisionGroupCard.vue')
    // 经典分支（v-else）里仍是「N 项待决策」
    assert.match(decision, /<template v-else>有 <b[^>]*>\{\{ group\.pending \}\}<\/b> 项待决策/)
  })
})
