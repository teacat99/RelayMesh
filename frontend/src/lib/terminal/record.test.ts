// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { describe, it } from 'node:test'
import {
  KIND_MARK,
  KIND_OUTPUT,
  KIND_RESIZE,
  RecordCorruptError,
  RecordTimeline,
  decodeEvents,
  endReasonLabel,
  extendWindowStart,
  formatSize,
  formatSpan,
  gapReasonLabel,
  initialSize,
  parseRecInfo,
  pickWindowStart,
  rawBytes,
  recStateInfo,
  safeFileName,
  stripScreenControls,
  toAsciicast,
  toPlainText,
  type Item,
  type RecSegInfo,
} from './record'

const enc = new TextEncoder()
const dec = new TextDecoder()

function uvarint(n: number): number[] {
  const out: number[] = []
  let v = BigInt(n)
  while (v >= 0x80n) {
    out.push(Number(v & 0x7fn) | 0x80)
    v >>= 7n
  }
  out.push(Number(v))
  return out
}

function event(kind: number, dt: number, payload: Uint8Array | string): Uint8Array {
  const p = typeof payload === 'string' ? enc.encode(payload) : payload
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

function mark(dt: number, m: Record<string, unknown>): Uint8Array {
  return event(KIND_MARK, dt, JSON.stringify(m))
}

/** 在仓库里向上寻找 Go 侧的对照文件（测试可能从任意子目录启动）。 */
function goldenHex(): string {
  let dir = process.cwd()
  for (let i = 0; i < 6; i++) {
    const p = join(dir, 'internal', 'agent', 'record', 'testdata', 'golden_events.hex')
    if (existsSync(p)) return readFileSync(p, 'utf8').trim()
    const up = resolve(dir, '..')
    if (up === dir) break
    dir = up
  }
  throw new Error('找不到 internal/agent/record/testdata/golden_events.hex')
}

function fromHex(h: string): Uint8Array {
  assert.equal(h.length % 2, 0)
  const out = new Uint8Array(h.length / 2)
  for (let i = 0; i < out.length; i++) out[i] = parseInt(h.slice(i * 2, i * 2 + 2), 16)
  return out
}

function timelineOf(data: Uint8Array): Item[] {
  const { events } = decodeEvents(data)
  const tl = new RecordTimeline()
  tl.push(events)
  return tl.items
}

describe('事件解码', () => {
  it('与 Go 编码器的对照文件逐项一致', () => {
    const data = fromHex(goldenHex())
    const { events, used } = decodeEvents(data)
    assert.equal(used, data.length)
    assert.equal(events.length, 9)
    assert.deepEqual(
      events.map(e => [e.kind, e.dt, e.payload.length]),
      [
        [KIND_MARK, 0, 58],
        [KIND_OUTPUT, 5, 22],
        [KIND_OUTPUT, 130, 2],
        [KIND_OUTPUT, 16384, 5],
        [KIND_RESIZE, 2, 4],
        [KIND_OUTPUT, 0, 0],
        [KIND_OUTPUT, 1, 300],
        [KIND_MARK, 7, 44],
        [KIND_MARK, 900, 36],
      ].map(([k, d, l]) => [k, d, l]),
    )
    const items = timelineOf(data)
    assert.deepEqual(items[0], { type: 'seg', at: 1_700_000_000_123, seq: 3, rows: 24, cols: 80 })
    const out = items.filter(i => i.type === 'output')
    assert.equal(out.length, 4, '空净荷的输出事件不进入时间线')
    assert.equal(items.find(i => i.type === 'resize') && (items.find(i => i.type === 'resize') as { rows: number }).rows, 40)
    const gap = items.find(i => i.type === 'gap')
    assert.deepEqual(gap && { reason: (gap as { reason: string }).reason, bytes: (gap as { bytes: number }).bytes }, { reason: 'disk_low', bytes: 4096 })
    const end = items[items.length - 1]
    assert.equal(end.type, 'end')
    assert.deepEqual(end.type === 'end' && { reason: end.reason, code: end.code }, { reason: 'exit', code: 3 })
    // 时间 = 分段起点 + dt 累加
    assert.equal(items[1].at, 1_700_000_000_128)
    assert.equal(end.at, 1_700_000_000_123 + 5 + 130 + 16384 + 2 + 0 + 1 + 7 + 900)
    // 输出文字：被拆开的汉字合并后完整
    assert.equal(toPlainText(items).split('\n')[1], '你好')
  })

  it('大于 32 位的毫秒数与多字节长度', () => {
    const big = 2 ** 33 + 12345
    const e = event(KIND_OUTPUT, big, 'x'.repeat(200))
    const { events } = decodeEvents(e)
    assert.equal(events[0].dt, big)
    assert.equal(events[0].payload.length, 200)
  })

  it('不足一个完整事件的尾部不是错误，只是不消耗', () => {
    const a = event(KIND_OUTPUT, 1, 'abc')
    const b = event(KIND_OUTPUT, 2, 'defgh')
    for (let cut = 0; cut < b.length; cut++) {
      const { events, used } = decodeEvents(concat(a, b.subarray(0, cut)))
      assert.equal(events.length, 1, `cut=${cut}`)
      assert.equal(used, a.length)
    }
    const all = decodeEvents(concat(a, b))
    assert.equal(all.events.length, 2)
    assert.equal(all.used, a.length + b.length)
  })

  it('超出上限的长度与异常变长整数视为数据损坏', () => {
    assert.throws(() => decodeEvents(Uint8Array.from([KIND_OUTPUT, 0, ...uvarint(40_000)])), RecordCorruptError)
    assert.throws(() => decodeEvents(Uint8Array.from([KIND_OUTPUT, ...new Array(11).fill(0xff), 0, 0])), RecordCorruptError)
  })

  it('未知事件类型被忽略（向前兼容），不影响后面的事件', () => {
    const data = concat(event(99, 0, 'future'), event(KIND_OUTPUT, 0, 'ok'))
    const items = timelineOf(data)
    assert.equal(items.length, 1)
    assert.equal(dec.decode((items[0] as { data: Uint8Array }).data), 'ok')
  })
})

describe('时间线', () => {
  it('每个分段起点重置为绝对时间；跨页续读时间连续', () => {
    const page1 = concat(mark(0, { t: 'seg', seq: 1, t0: 1000, rows: 24, cols: 80 }), event(KIND_OUTPUT, 10, 'a'))
    const page2 = concat(event(KIND_OUTPUT, 20, 'b'), mark(0, { t: 'seg', seq: 2, t0: 5000, rows: 30, cols: 100 }), event(KIND_OUTPUT, 1, 'c'))
    const tl = new RecordTimeline()
    tl.push(decodeEvents(page1).events)
    tl.push(decodeEvents(page2).events)
    assert.deepEqual(
      tl.items.map(i => [i.type, i.at]),
      [['seg', 1000], ['output', 1010], ['output', 1030], ['seg', 5000], ['output', 5001]],
    )
  })

  it('窗口从分段中间开始：没有绝对时间，从 0 累加', () => {
    const items = timelineOf(concat(event(KIND_OUTPUT, 0, 'a'), event(KIND_OUTPUT, 40, 'b')))
    assert.deepEqual(items.map(i => i.at), [0, 40])
    assert.equal(initialSize(items), null)
  })

  it('初始尺寸取第一个输出之前的 seg / resize', () => {
    const items = timelineOf(concat(mark(0, { t: 'seg', seq: 1, t0: 1, rows: 24, cols: 80 }), event(KIND_OUTPUT, 0, 'x'), event(KIND_RESIZE, 1, Uint8Array.from([0, 40, 0, 120]))))
    assert.deepEqual(initialSize(items), { rows: 24, cols: 80 })
  })
})

describe('纯文本导出', () => {
  it('去掉颜色 / 光标 / 标题序列', () => {
    assert.equal(stripScreenControls('\x1b[1;31mred\x1b[0m \x1b[2K\x1b[10;5Hplain\r\n'), 'red plain\n')
    assert.equal(stripScreenControls('\x1b]0;my title\x07after\r\n'), 'after\n')
    assert.equal(stripScreenControls('\x1b]8;;http://x\x1b\\link\x1b]8;;\x1b\\\r\n'), 'link\n')
    assert.equal(stripScreenControls('\x1bPq#0;2;0;0;0\x1b\\done\n'), 'done\n')
    assert.equal(stripScreenControls('\x1b(Bplain\x1b=\n'), 'plain\n')
  })

  it('回车覆盖、退格、制表符还原成最终样子', () => {
    assert.equal(stripScreenControls('progress 10%\rprogress 99%\n'), 'progress 99%\n')
    assert.equal(stripScreenControls('abc\b\bX\n'), 'aXc\n')
    assert.equal(stripScreenControls('a\tb\n'), 'a       b\n')
    assert.equal(stripScreenControls('line1\r\nline2'), 'line1\nline2\n')
    assert.equal(stripScreenControls(''), '')
  })

  it('丢弃响铃等控制字符，保留中文与 emoji', () => {
    assert.equal(stripScreenControls('a\x07b\x00c 你好 😀\n'), 'abc 你好 😀\n')
  })

  it('跨事件拆开的 UTF-8 字符不会乱码', () => {
    const bytes = enc.encode('好')
    const items: Item[] = [
      { type: 'output', at: 0, data: bytes.subarray(0, 1) },
      { type: 'output', at: 1, data: bytes.subarray(1) },
    ]
    assert.equal(toPlainText(items), '好\n')
  })
})

describe('asciicast 与原始流导出', () => {
  const items: Item[] = [
    { type: 'seg', at: 10_000, seq: 1, rows: 24, cols: 80 },
    { type: 'output', at: 10_000, data: enc.encode('hi\r\n') },
    { type: 'output', at: 10_250, data: enc.encode('好').subarray(0, 2) },
    { type: 'resize', at: 10_500, rows: 40, cols: 120 },
    { type: 'output', at: 11_000, data: enc.encode('好').subarray(2) },
    { type: 'end', at: 12_000, reason: 'exit', code: 0, signal: '' },
  ]

  it('v2 头部与事件', () => {
    const lines = toAsciicast(items, { title: '构建 "A"', rows: 24, cols: 80, startedMs: 1_700_000_000_999 }).trimEnd().split('\n')
    const header = JSON.parse(lines[0])
    assert.deepEqual(header, { version: 2, width: 80, height: 24, env: { TERM: 'xterm-256color' }, timestamp: 1_700_000_000, title: '构建 "A"' })
    const events = lines.slice(1).map(l => JSON.parse(l) as [number, string, string])
    assert.deepEqual(events[0], [0, 'o', 'hi\r\n'])
    // 被拆开的汉字：前半个字节先不输出，凑齐后在后一个事件里一起输出，不会出现 U+FFFD
    assert.deepEqual(events[1], [0.5, 'r', '120x40'])
    assert.deepEqual(events[2], [1, 'o', '好'])
    assert.ok(!lines.join('').includes('\ufffd'))
  })

  it('原始流只含输出字节', () => {
    assert.equal(dec.decode(rawBytes(items)), 'hi\r\n好')
  })
})

describe('窗口选择', () => {
  const seg = (seq: number, raw: number): RecSegInfo => ({ seq, t0: seq * 1000, raw, disk: raw })
  const segs = [seg(4, 1000), seg(5, 1000), seg(6, 1000), seg(7, 500)]

  it('从最新往回装，不超预算，至少包含最后一段', () => {
    assert.equal(pickWindowStart(segs, 3000), 1)
    assert.equal(pickWindowStart(segs, 10_000), 0)
    assert.equal(pickWindowStart(segs, 100), 3)
    assert.equal(pickWindowStart([], 100), -1)
  })

  it('加载更早：再多带一批，至少多带一段', () => {
    assert.equal(extendWindowStart(segs, 3, 1000), 2)
    assert.equal(extendWindowStart(segs, 3, 2500), 1)
    assert.equal(extendWindowStart(segs, 1, 10_000), 0)
    assert.equal(extendWindowStart(segs, 0, 10_000), 0)
  })
})

describe('记录描述与文案', () => {
  it('parseRecInfo 容错并按序号排序', () => {
    const info = parseRecInfo({
      sid: 'a'.repeat(32),
      created_ms: 5,
      raw_bytes: 30,
      disk_bytes: 10,
      head: true,
      segments: [{ seq: 3, raw: 2, disk: 1, t0: 9 }, { seq: 2, raw: 1, disk: 1, t0: 8, gz: true }, { seq: 0 }, null, 'x'],
    })
    assert.ok(info)
    assert.deepEqual(info!.segments.map(s => s.seq), [2, 3])
    assert.equal(info!.head, true)
    assert.equal(info!.segments[0].gz, true)
    assert.equal(parseRecInfo(null), null)
    assert.equal(parseRecInfo({ sid: 5 }), null)
  })

  it('结束 / 缺口原因的中文说明，未知原因原样显示', () => {
    assert.equal(endReasonLabel('host_reboot'), '主机重启')
    assert.equal(endReasonLabel(undefined), '进行中')
    assert.equal(endReasonLabel('weird'), 'weird')
    assert.equal(gapReasonLabel('disk_low'), '磁盘剩余空间不足')
    assert.equal(gapReasonLabel(''), '原因不明')
  })

  it('录制状态文案', () => {
    assert.equal(recStateInfo('on')?.text, '录制中')
    assert.equal(recStateInfo('on')?.tone, 'ok')
    assert.equal(recStateInfo('incognito')?.text, '无痕')
    assert.equal(recStateInfo('veto')?.tone, 'muted')
    assert.equal(recStateInfo('paused')?.tone, 'warn')
    assert.equal(recStateInfo(undefined), null)
    assert.equal(recStateInfo(''), null)
  })

  it('尺寸 / 时长 / 文件名', () => {
    assert.equal(formatSize(512), '512 B')
    assert.equal(formatSize(1536), '1.5 KiB')
    assert.equal(formatSize(64 * 1024 * 1024), '64 MiB')
    assert.equal(formatSize(-1), '—')
    assert.equal(formatSpan(65_000), '1:05')
    assert.equal(formatSpan(3_661_000), '1:01:01')
    assert.equal(formatSpan(NaN), '—')
    assert.equal(safeFileName('a/b:c*d?e'), 'a_b_c_d_e')
    assert.equal(safeFileName('   '), 'terminal')
    assert.equal(safeFileName('x'.repeat(200)).length, 60)
    assert.equal(dirname('/a/b'), '/a')
  })
})
