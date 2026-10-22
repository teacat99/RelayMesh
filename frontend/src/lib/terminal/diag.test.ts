// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import { DiagLog } from './diag'

describe('DiagLog', () => {
  it('按顺序记事件，超过容量丢最老的', () => {
    let now = 1000
    const log = new DiagLog(3, () => now)
    for (const k of ['a', 'b', 'c', 'd']) {
      log.add(k)
      now += 10
    }
    assert.deepEqual(log.events().map(e => e.k), ['b', 'c', 'd'])
    assert.equal(log.size, 3)
  })

  it('text：头部环境信息 + 相对时间事件；没有事件也不报错', () => {
    let now = 5000
    const log = new DiagLog(10, () => now)
    assert.match(log.text({ session: 'abcd1234' }), /events: 0/)
    log.add('connect', { since: 0, fresh: true })
    now += 1500
    log.add('close', { code: 4414, reason: 'lagging' })
    now += 2000
    const text = log.text({ session: 'abcd1234', pane: '120x40' }, now)
    assert.match(text, /^# RelayMesh 终端连接诊断/)
    assert.match(text, /session: abcd1234/)
    assert.match(text, /pane: 120x40/)
    assert.match(text, /events: 2/)
    assert.match(text, /last event: 2\.0s ago/)
    assert.match(text, /\+\s+0\.000s connect since=0 fresh=true/)
    assert.match(text, /\+\s+1\.500s close code=4414 reason=lagging/)
  })

  it('字符串值只留可打印的短文本：控制字符被替换、超长被截断（防止把终端内容或转义序列带进诊断）', () => {
    const log = new DiagLog()
    log.add('close', { reason: 'bad\u001b[31mred\u0000' + 'x'.repeat(200) })
    const v = String(log.events()[0].d!.reason)
    assert.equal(/[\u0000-\u001f]/.test(v), false)
    assert.ok(v.length <= 80)
    assert.match(v, /^bad·\[31mred·/)
  })
})
