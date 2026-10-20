import test from 'node:test'
import assert from 'node:assert/strict'
import { HistoryLoader, LoaderCancelled, type RecordPageLike, type RecordSource } from './historyLoader'
import { concatOutput } from './historyPlan'

const enc = new TextEncoder()
const dec = new TextDecoder()

function uvarint(n: number): number[] {
  const out: number[] = []
  let v = n
  while (v >= 0x80) {
    out.push((v & 0x7f) | 0x80)
    v = Math.floor(v / 128)
  }
  out.push(v)
  return out
}

function event(kind: number, dt: number, payload: string): Uint8Array {
  const p = enc.encode(payload)
  return Uint8Array.from([kind, ...uvarint(dt), ...uvarint(p.length), ...p])
}

function concat(...parts: Uint8Array[]): Uint8Array {
  const out = new Uint8Array(parts.reduce((n, p) => n + p.length, 0))
  let pos = 0
  for (const p of parts) {
    out.set(p, pos)
    pos += p.length
  }
  return out
}

/** 一个分段：seg 标记 + 若干输出事件。 */
function segment(seq: number, t0: number, texts: string[]): Uint8Array {
  return concat(event(3, 0, JSON.stringify({ t: 'seg', seq, t0, rows: 24, cols: 80 })), ...texts.map(t => event(1, 5, t)))
}

class FakeSource implements RecordSource {
  pageCalls: Array<{ seg: number; off: number }> = []
  constructor(
    private readonly segs: Map<number, Uint8Array>,
    private readonly opts: { online?: boolean; pageSize?: number } = {},
  ) {}

  async info() {
    if (this.opts.online === false) return { online: false }
    const segments = [...this.segs.entries()].map(([seq, data]) => ({ seq, t0: seq * 1000, raw: data.length, disk: data.length }))
    return {
      online: true,
      info: { sid: 's1', created_ms: 1000, raw_bytes: segments.reduce((n, s) => n + s.raw, 0), disk_bytes: 0, segments },
    }
  }

  async page(seg: number, off: number): Promise<RecordPageLike> {
    this.pageCalls.push({ seg, off })
    const data = this.segs.get(seg)
    if (!data) return { data: new Uint8Array(0), nextSeg: seg, nextOff: off, eof: true }
    const size = this.opts.pageSize ?? 1024 * 1024
    const part = data.subarray(off, off + size)
    const end = off + part.length >= data.length
    const lastSeq = Math.max(...this.segs.keys())
    if (end && seg >= lastSeq) return { data: part, nextSeg: seg, nextOff: off + part.length, eof: true }
    if (end) return { data: part, nextSeg: seg + 1, nextOff: 0, eof: false }
    return { data: part, nextSeg: seg, nextOff: off + part.length, eof: false }
  }
}

function build(count: number, opts: { pageSize?: number } = {}): FakeSource {
  const m = new Map<number, Uint8Array>()
  for (let i = 1; i <= count; i++) m.set(i, segment(i, i * 1000, [`seg${i}-a\r\n`, `seg${i}-b\r\n`]))
  return new FakeSource(m, opts)
}

const text = (l: HistoryLoader) => dec.decode(concatOutput(l.items))

test('读取窗口：预算够就从第一个分段读起，已是记录开头', async () => {
  const l = new HistoryLoader(build(3), { windowBytes: 10_000 })
  assert.equal(await l.open(), 'ok')
  assert.equal(l.startIdx, 0)
  assert.equal(l.atBeginning, true)
  assert.equal(l.canLoadEarlier, false)
  assert.equal(text(l), 'seg1-a\r\nseg1-b\r\nseg2-a\r\nseg2-b\r\nseg3-a\r\nseg3-b\r\n')
})

test('读取窗口：预算不够只取最近的分段，可以往前补读且不重叠', async () => {
  const src = build(4)
  const seg = (await src.info()).info as { segments: Array<{ raw: number }> }
  const one = seg.segments[0].raw
  const l = new HistoryLoader(src, { windowBytes: Math.floor(one * 1.5) })
  assert.equal(await l.open(), 'ok')
  assert.equal(l.startIdx, 3)
  assert.equal(text(l), 'seg4-a\r\nseg4-b\r\n')
  assert.equal(l.canLoadEarlier, true)
  assert.equal(l.atBeginning, false)

  await l.loadEarlier()
  assert.equal(l.startIdx, 2)
  assert.equal(text(l), 'seg3-a\r\nseg3-b\r\nseg4-a\r\nseg4-b\r\n')
  await l.loadEarlier()
  await l.loadEarlier()
  assert.equal(l.startIdx, 0)
  assert.equal(l.atBeginning, true)
  assert.equal(l.canLoadEarlier, false)
  assert.equal(text(l), 'seg1-a\r\nseg1-b\r\nseg2-a\r\nseg2-b\r\nseg3-a\r\nseg3-b\r\nseg4-a\r\nseg4-b\r\n')
  // 已经到头：再调用不报错、不变化
  await l.loadEarlier()
  assert.equal(l.startIdx, 0)
})

test('读取窗口：分页很小（事件被页边界切开）也能拼出完整事件', async () => {
  const l = new HistoryLoader(build(2, { pageSize: 7 }), { windowBytes: 10_000 })
  assert.equal(await l.open(), 'ok')
  assert.equal(text(l), 'seg1-a\r\nseg1-b\r\nseg2-a\r\nseg2-b\r\n')
})

test('读取窗口：已载入量达到上限后不再提供「往前」', async () => {
  const src = build(4)
  const l = new HistoryLoader(src, { windowBytes: 1, maxLoaded: 1 })
  assert.equal(await l.open(), 'ok')
  assert.equal(l.startIdx, 3)
  assert.equal(l.canLoadEarlier, false)
})

test('读取窗口：主机离线 / 没有分段', async () => {
  assert.equal(await new HistoryLoader(new FakeSource(new Map(), { online: false })).open(), 'offline')
  assert.equal(await new HistoryLoader(new FakeSource(new Map())).open(), 'empty')
})

test('读取窗口：取消后抛 LoaderCancelled', async () => {
  const l = new HistoryLoader(build(2), { windowBytes: 10_000 })
  l.cancel()
  await assert.rejects(() => l.open(), LoaderCancelled)
})

test('读取窗口：进度回调给出累计读取的字节数', async () => {
  const seen: number[] = []
  const l = new HistoryLoader(build(2, { pageSize: 16 }), { windowBytes: 10_000, onBytes: n => seen.push(n) })
  await l.open()
  assert.ok(seen.length > 1)
  assert.deepEqual(seen, [...seen].sort((a, b) => a - b))
})
