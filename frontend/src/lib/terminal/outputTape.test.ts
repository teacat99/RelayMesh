import test from 'node:test'
import assert from 'node:assert/strict'
import { OutputTape, lastClearEnd, splitTape, type TapeChunk } from './outputTape'

const enc = new TextEncoder()
const dec = new TextDecoder()
const b = (s: string) => enc.encode(s)
const text = (chunks: TapeChunk[]) => chunks.map(c => dec.decode(c.data)).join('')

test('输出带：按顺序记录，会话输出与其他内容分开计数', () => {
  const t = new OutputTape(1024)
  t.push('prefix', '\x1b[?2004h')
  t.push('stream', b('hello\r\n'))
  t.push('local', '\r\n[RelayMesh] 断线\r\n')
  t.push('stream', b('world\r\n'))
  assert.equal(t.streamBytes, 14)
  assert.equal(dec.decode(t.concatStream()), 'hello\r\nworld\r\n')
  assert.equal(t.snapshot().length, 4)
  assert.equal(t.trimmed, false)
})

test('输出带：空内容忽略；reset 清空全部状态', () => {
  const t = new OutputTape(1024)
  t.push('stream', new Uint8Array(0))
  t.push('local', '')
  assert.equal(t.snapshot().length, 0)
  t.push('stream', b('abc'))
  t.reset()
  assert.equal(t.streamBytes, 0)
  assert.equal(t.snapshot().length, 0)
  assert.equal(t.trimmed, false)
})

test('输出带：超出上限从最旧的一端丢弃，开头对齐到行首，且不再以本地提示 / 模式序列开头', () => {
  const t = new OutputTape(64)
  t.push('prefix', '\x1b[?2004h')
  for (let i = 0; i < 12; i++) t.push('stream', b(`line-${String(i).padStart(2, '0')}\r\n`))
  assert.equal(t.trimmed, true)
  const snap = t.snapshot()
  assert.equal(snap[0].kind, 'stream')
  const all = dec.decode(t.concatStream())
  // 第一块是一整块输出的情况下，对齐会跳到第一个换行之后；这里每块本身以换行结尾，所以开头一定是完整的一行。
  assert.match(all, /^line-\d\d\r\n/)
  assert.ok(all.endsWith('line-11\r\n'))
  assert.ok(t.streamBytes <= 64 + 9)
})

test('输出带：开头落在一行中间时跳到下一个换行之后', () => {
  const t = new OutputTape(30)
  t.push('stream', b('aaaaaaaaaa'))
  t.push('stream', b('bbbbbbbbbb\ncccccccccc\n'))
  t.push('stream', b('dddddddddd\n'))
  assert.equal(t.trimmed, true)
  const all = dec.decode(t.concatStream())
  assert.ok(all.startsWith('cccccccccc\n') || all.startsWith('dddddddddd\n'), all)
})

test('输出带：单个块超过上限时保留（不能把仅有的内容丢光）', () => {
  const t = new OutputTape(8)
  t.push('stream', b('0123456789abcdef'))
  assert.equal(t.streamBytes, 16)
})

test('最后一次清屏：ESC[2J / ESC[3J / ESC c 都算，返回其后的位置', () => {
  assert.equal(lastClearEnd(b('plain text')), 0)
  const a = 'before\x1b[H\x1b[2J\x1b[3Jafter'
  assert.equal(lastClearEnd(b(a)), a.indexOf('after'))
  const two = 'x\x1b[2Jy\x1b[2Jz'
  assert.equal(lastClearEnd(b(two)), two.length - 1)
  const reset = 'x\x1bcy'
  assert.equal(lastClearEnd(b(reset)), 3)
})

test('最后一次清屏：ESC[J 只有紧跟在 ESC[H 后面（光标在左上角）时才算清整屏', () => {
  assert.equal(lastClearEnd(b('abc\x1b[Jdef')), 0)
  const home = 'abc\x1b[H\x1b[Jdef'
  assert.equal(lastClearEnd(b(home)), home.indexOf('def'))
})

test('最后一次清屏：颜色等其他转义序列不受影响，末尾被截断的序列不算', () => {
  assert.equal(lastClearEnd(b('\x1b[31mred\x1b[0m\x1b[1;2H')), 0)
  assert.equal(lastClearEnd(b('abc\x1b[')), 0)
  const mixed = '\x1b[32mok\x1b[0m\x1b[2Jtail\x1b['
  assert.equal(lastClearEnd(b(mixed)), mixed.indexOf('tail'))
})

test('切分：在第 N 个会话输出字节处分开，横跨的块被拆开，模式序列丢弃', () => {
  const chunks: TapeChunk[] = [
    { kind: 'prefix', data: b('PFX') },
    { kind: 'stream', data: b('0123') },
    { kind: 'local', data: b('<L1>') },
    { kind: 'stream', data: b('456789') },
    { kind: 'local', data: b('<L2>') },
    { kind: 'stream', data: b('abc') },
  ]
  const { head, tail } = splitTape(chunks, 7)
  assert.equal(text(head), '0123<L1>456')
  assert.equal(text(tail), '789<L2>abc')
})

test('切分：恰好落在块边界，边界上的本地提示归入分割线之后', () => {
  const chunks: TapeChunk[] = [
    { kind: 'stream', data: b('0123') },
    { kind: 'local', data: b('<L>') },
    { kind: 'stream', data: b('4567') },
  ]
  const { head, tail } = splitTape(chunks, 4)
  assert.equal(text(head), '0123')
  assert.equal(text(tail), '<L>4567')
})

test('切分：cut 为 0 时全部在分割线之后，超过总量时全部在之前', () => {
  const chunks: TapeChunk[] = [
    { kind: 'prefix', data: b('P') },
    { kind: 'stream', data: b('abc') },
    { kind: 'local', data: b('<L>') },
  ]
  const zero = splitTape(chunks, 0)
  assert.equal(text(zero.head), '')
  assert.equal(text(zero.tail), 'abc<L>')
  const big = splitTape(chunks, 100)
  assert.equal(text(big.head), 'abc<L>')
  assert.equal(text(big.tail), '')
})
