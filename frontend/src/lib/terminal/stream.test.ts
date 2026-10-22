// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import { AckBatcher, OutputAssembler } from './stream'

const bytes = (n: number) => new Uint8Array(n).fill(65)

describe('OutputAssembler', () => {
  it('ready 之前到达的输出被丢弃', () => {
    const a = new OutputAssembler()
    assert.equal(a.push(0, bytes(4)).data, null)
    assert.equal(a.nextOffset, 0)
  })

  it('连续帧原样通过并推进偏移', () => {
    const a = new OutputAssembler()
    a.start(100)
    const r1 = a.push(100, bytes(10))
    assert.equal(r1.data?.length, 10)
    assert.equal(r1.gap, 0)
    assert.equal(r1.end, 110)
    const r2 = a.push(110, bytes(5))
    assert.equal(r2.data?.length, 5)
    assert.equal(a.nextOffset, 115)
  })

  it('完全重复的帧被丢弃，部分重叠的帧只保留新增部分', () => {
    const a = new OutputAssembler()
    a.start(0)
    a.push(0, bytes(10))
    assert.equal(a.push(0, bytes(10)).data, null)
    assert.equal(a.push(4, bytes(6)).data, null)
    const r = a.push(6, bytes(8)) // 覆盖 [6,14)，其中 [10,14) 是新的
    assert.equal(r.data?.length, 4)
    assert.equal(r.gap, 0)
    assert.equal(a.nextOffset, 14)
  })

  it('偏移跳变时报告缺口，数据仍然交付', () => {
    const a = new OutputAssembler()
    a.start(0)
    a.push(0, bytes(10))
    const r = a.push(25, bytes(5))
    assert.equal(r.gap, 15)
    assert.equal(r.data?.length, 5)
    assert.equal(a.nextOffset, 30)
  })

  it('空净荷不改变状态', () => {
    const a = new OutputAssembler()
    a.start(7)
    assert.equal(a.push(7, new Uint8Array(0)).data, null)
    assert.equal(a.nextOffset, 7)
  })
})

describe('AckBatcher', () => {
  it('低于阈值时不立即确认，flush 兜底', () => {
    const b = new AckBatcher(100)
    b.reset(0)
    assert.equal(b.onRendered(40), null)
    assert.equal(b.pending, true)
    assert.equal(b.flush(), 40)
    assert.equal(b.pending, false)
    assert.equal(b.flush(), null)
  })

  it('累计达到阈值立即确认到最新进度', () => {
    const b = new AckBatcher(100)
    b.reset(0)
    assert.equal(b.onRendered(60), null)
    assert.equal(b.onRendered(130), 130)
    assert.equal(b.onRendered(150), null)
    assert.equal(b.flush(), 150)
  })

  it('乱序的旧进度不会让确认倒退', () => {
    const b = new AckBatcher(10)
    b.reset(0)
    assert.equal(b.onRendered(50), 50)
    assert.equal(b.onRendered(30), null)
    assert.equal(b.flush(), null)
  })

  it('reset 之后以新的回放起点为基线', () => {
    const b = new AckBatcher(100)
    b.reset(5000)
    assert.equal(b.onRendered(5050), null)
    assert.equal(b.onRendered(5100), 5100)
  })
})
