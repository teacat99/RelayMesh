// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import { FRESH_REQUEST_MS, NOTICE_DURATION_MS, describeRequest, isFreshRequest, relayNoticeId, relayRequestNotice } from './crossNotice'

const NOW = Date.parse('2026-10-08T03:00:00Z')
const ago = (ms: number) => new Date(NOW - ms).toISOString()
const req = { sessionId: 's1', workflowId: 'w1', title: '请确认迁移方案', createdAt: ago(5_000) }

describe('isFreshRequest', () => {
  it('窗口内是新的，窗口外不是', () => {
    assert.equal(isFreshRequest(ago(1_000), NOW), true)
    assert.equal(isFreshRequest(ago(FRESH_REQUEST_MS), NOW), true)
    assert.equal(isFreshRequest(ago(FRESH_REQUEST_MS + 1), NOW), false)
  })

  it('没有时间戳或解析不了：当作新的（宁可多提醒）', () => {
    assert.equal(isFreshRequest(undefined, NOW), true)
    assert.equal(isFreshRequest('', NOW), true)
    assert.equal(isFreshRequest('不是时间', NOW), true)
  })

  it('比本机时钟还新（时钟偏差）：当作新的', () => {
    assert.equal(isFreshRequest(ago(-30_000), NOW), true)
  })

  it('窗口可调', () => {
    assert.equal(isFreshRequest(ago(10_000), NOW, 5_000), false)
    assert.equal(isFreshRequest(ago(10_000), NOW, 20_000), true)
  })
})

describe('describeRequest', () => {
  it('优先用标题', () => {
    assert.equal(describeRequest({ title: '  请确认迁移方案  ', summary: '正文' }), '请确认迁移方案')
  })

  it('没有标题时取摘要里第一行有字的内容，并去掉 Markdown 前缀', () => {
    assert.equal(describeRequest({ summary: '\n\n## 阶段汇报\n细节' }), '阶段汇报')
    assert.equal(describeRequest({ title: ' ', summary: '> - 方案已完成' }), '方案已完成')
  })

  it('都没有：兜底成一句话', () => {
    assert.equal(describeRequest({}), '智能体在等你回复')
    assert.equal(describeRequest({ title: '', summary: '\n  \n' }), '智能体在等你回复')
  })

  it('过长时按字符截断并加省略号（不把一个汉字或表情劈成两半）', () => {
    const out = describeRequest({ title: '字'.repeat(200) })
    assert.equal(Array.from(out).length, 80)
    assert.ok(out.endsWith('…'))
    const emoji = describeRequest({ title: '😀'.repeat(100) })
    assert.equal(Array.from(emoji).length, 80)
    assert.doesNotMatch(emoji, /[\ud800-\udbff](?![\udc00-\udfff])/)
  })

  it('刚好 80 个字符不截断', () => {
    const exact = '字'.repeat(80)
    assert.equal(describeRequest({ title: exact }), exact)
  })
})

describe('relayRequestNotice', () => {
  it('人在 ExecMesh 里：弹出带「查看」入口的消息', () => {
    const n = relayRequestNotice('exec', req, NOW)
    assert.ok(n)
    assert.match(n.title, /RelayMesh/)
    assert.equal(n.description, '请确认迁移方案')
    assert.equal(n.action, '↗')
    assert.equal(n.durationMs, NOTICE_DURATION_MS)
  })

  it('人在 RelayMesh 里：界面上本来就看得见，不弹', () => {
    assert.equal(relayRequestNotice('relay', req, NOW), null)
  })

  it('页面刚打开时已经等了很久的请求：不弹（切换器上的圆点负责）', () => {
    assert.equal(relayRequestNotice('exec', { ...req, createdAt: ago(FRESH_REQUEST_MS + 1_000) }, NOW), null)
  })
})

describe('relayNoticeId', () => {
  it('同一条请求同一个 id；不同请求不同 id', () => {
    assert.equal(relayNoticeId('a'), relayNoticeId('a'))
    assert.notEqual(relayNoticeId('a'), relayNoticeId('b'))
  })
})
