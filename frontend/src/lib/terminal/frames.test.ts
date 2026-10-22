// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import {
  MAX_INPUT_PAYLOAD,
  FRAME_INPUT,
  ackFrame,
  clampDim,
  decodeBase64,
  decodeOutput,
  encodeBinaryString,
  encodeInput,
  encodeInputBytes,
  parseControl,
  resizeFrame,
} from './frames'

function outputFrame(offset: bigint, payload: number[]): Uint8Array {
  const buf = new Uint8Array(9 + payload.length)
  buf[0] = 0x01
  new DataView(buf.buffer).setBigUint64(1, offset, false)
  buf.set(payload, 9)
  return buf
}

describe('decodeOutput', () => {
  it('解出大端偏移与净荷', () => {
    const r = decodeOutput(outputFrame(0x0102030405n, [104, 105]))
    assert.ok(r)
    assert.equal(r.offset, 0x0102030405)
    assert.deepEqual([...r.payload], [104, 105])
  })

  it('接受带 byteOffset 的视图（ws 库常返回共享缓冲区的切片）', () => {
    const inner = outputFrame(7n, [1, 2, 3])
    const shared = new Uint8Array(inner.length + 5)
    shared.set(inner, 5)
    const r = decodeOutput(shared.subarray(5))
    assert.ok(r)
    assert.equal(r.offset, 7)
    assert.deepEqual([...r.payload], [1, 2, 3])
  })

  it('拒绝过短、类型不符与超出安全整数的偏移', () => {
    assert.equal(decodeOutput(new Uint8Array([1, 0, 0])), null)
    assert.equal(decodeOutput(outputFrame(0n, []).map((b, i) => (i === 0 ? 0x02 : b))), null)
    assert.equal(decodeOutput(outputFrame(1n << 60n, [1])), null)
  })

  it('允许空净荷', () => {
    const r = decodeOutput(outputFrame(5n, []))
    assert.ok(r)
    assert.equal(r.payload.length, 0)
  })
})

describe('parseControl', () => {
  it('解析 ready 并解码 prefix', () => {
    const prefix = btoa('\x1b[?1049h')
    const f = parseControl(
      JSON.stringify({ t: 'ready', sid: 's1', base: 0, end: 100, from: 10, truncated: true, prefix, rows: 24, cols: 80, state: 'running' }),
    )
    assert.ok(f && f.t === 'ready')
    assert.equal(f.from, 10)
    assert.equal(f.truncated, true)
    assert.equal(new TextDecoder().decode(f.prefix), '\x1b[?1049h')
    assert.equal(f.exitCode, null)
  })

  it('ready 携带退出码', () => {
    const f = parseControl(JSON.stringify({ t: 'ready', sid: 's', base: 0, end: 0, from: 0, rows: 1, cols: 1, state: 'exited', exit_code: 3 }))
    assert.ok(f && f.t === 'ready')
    assert.equal(f.exitCode, 3)
    assert.equal(f.truncated, false)
    assert.equal(f.prefix.length, 0)
  })

  it('拒绝字段缺失、负数、非法 base64 的 ready', () => {
    assert.equal(parseControl(JSON.stringify({ t: 'ready', sid: 's', base: 0, end: 0 })), null)
    assert.equal(parseControl(JSON.stringify({ t: 'ready', sid: 's', base: -1, end: 0, from: 0 })), null)
    assert.equal(parseControl(JSON.stringify({ t: 'ready', sid: 's', base: 0, end: 0, from: 0, prefix: '***' })), null)
  })

  it('解析 exit 与 closed；忽略未知帧与非 JSON', () => {
    assert.deepEqual(parseControl('{"t":"exit","code":0}'), { t: 'exit', code: 0, signal: '' })
    assert.deepEqual(parseControl('{"t":"exit","signal":"killed"}'), { t: 'exit', code: null, signal: 'killed' })
    assert.deepEqual(parseControl('{"t":"closed","reason":"bye"}'), { t: 'closed', reason: 'bye' })
    assert.equal(parseControl('{"t":"future-frame"}'), null)
    assert.equal(parseControl('not json'), null)
    assert.equal(parseControl('42'), null)
  })
})

describe('输入编码', () => {
  it('短文本只有一帧，首字节是输入类型', () => {
    const frames = encodeInput('ls -la\r')
    assert.equal(frames.length, 1)
    assert.equal(frames[0][0], FRAME_INPUT)
    assert.equal(new TextDecoder().decode(frames[0].subarray(1)), 'ls -la\r')
  })

  it('空输入不产生帧', () => {
    assert.deepEqual(encodeInput(''), [])
  })

  it('大文本按上限分帧，且拼接后与原文一致', () => {
    const text = 'x'.repeat(MAX_INPUT_PAYLOAD * 3 + 17)
    const frames = encodeInput(text)
    assert.equal(frames.length, 4)
    for (const f of frames) assert.ok(f.length - 1 <= MAX_INPUT_PAYLOAD)
    const joined = frames.map(f => new TextDecoder().decode(f.subarray(1))).join('')
    assert.equal(joined, text)
  })

  it('不在 UTF-8 多字节字符中间切开', () => {
    // 「终」占 3 字节；让边界恰好落在字符中间
    const text = 'a' + '终'.repeat(Math.ceil(MAX_INPUT_PAYLOAD / 3) + 5)
    const frames = encodeInput(text)
    assert.ok(frames.length >= 2)
    const dec = new TextDecoder('utf-8', { fatal: true })
    let joined = ''
    for (const f of frames) {
      assert.ok(f.length - 1 <= MAX_INPUT_PAYLOAD)
      joined += dec.decode(f.subarray(1)) // fatal 模式下任何半个字符都会抛错
    }
    assert.equal(joined, text)
  })

  it('全是续字节的病态输入也不会死循环', () => {
    const frames = encodeInputBytes(new Uint8Array(MAX_INPUT_PAYLOAD * 2 + 1).fill(0x80))
    const total = frames.reduce((n, f) => n + f.length - 1, 0)
    assert.equal(total, MAX_INPUT_PAYLOAD * 2 + 1)
  })

  it('二进制字符串逐字符映射为字节，不做 UTF-8 放大', () => {
    const frames = encodeBinaryString('\x1b[M\xe0!!')
    assert.equal(frames.length, 1)
    assert.deepEqual([...frames[0].subarray(1)], [0x1b, 0x5b, 0x4d, 0xe0, 0x21, 0x21])
  })
})

describe('控制帧与尺寸', () => {
  it('ack / resize 的 JSON 形状与服务端一致', () => {
    assert.deepEqual(JSON.parse(ackFrame(4096)), { t: 'ack', offset: 4096 })
    assert.deepEqual(JSON.parse(resizeFrame(30.9, 120)), { t: 'resize', rows: 30, cols: 120 })
  })

  it('尺寸被夹取到 [1, 1000]', () => {
    assert.equal(clampDim(0), 1)
    assert.equal(clampDim(-5), 1)
    assert.equal(clampDim(NaN), 1)
    assert.equal(clampDim(99999), 1000)
    assert.equal(clampDim(24.7), 24)
  })

  it('base64 解码失败返回 null', () => {
    assert.equal(decodeBase64('!!!'), null)
    assert.deepEqual([...(decodeBase64('aGk=') ?? [])], [104, 105])
  })
})

describe('lag 控制帧', () => {
  it('解析 on / off；缺字段按 off（保守：不盖遮罩）', () => {
    assert.deepEqual(parseControl('{"t":"lag","on":true}'), { t: 'lag', on: true })
    assert.deepEqual(parseControl('{"t":"lag","on":false}'), { t: 'lag', on: false })
    assert.deepEqual(parseControl('{"t":"lag"}'), { t: 'lag', on: false })
    assert.deepEqual(parseControl('{"t":"lag","on":"yes"}'), { t: 'lag', on: false })
  })
})
