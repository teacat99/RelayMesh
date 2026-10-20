import test from 'node:test'
import assert from 'node:assert/strict'
import { Flattener, altScreenBlock, findClear, findEvent, pushScreenSequence, trimScreen, type FlattenHost, type ScreenProbe } from './flatten'
import { displayWidth } from './divider'

const dec = new TextDecoder()
const b = (s: string) => new TextEncoder().encode(s)

class FakeHost implements FlattenHost {
  log: string[] = []
  probes = 0
  screen: () => Partial<ScreenProbe> = () => ({})
  async write(d: Uint8Array): Promise<void> {
    this.log.push(dec.decode(d))
  }
  probe(): ScreenProbe {
    this.probes++
    return { rows: 24, cols: 80, lines: [], cursorX: 0, cursorY: 0, alt: false, used: 5, fingerprint: 'frame-A', ...this.screen() }
  }
}

const PUSH5 = pushScreenSequence(24, 5)

test('findClear：只认 2J / 3J / J / 0J / ESC c，其余序列原样跳过', () => {
  const f = (s: string) => findClear(b(s), 0)
  assert.deepEqual(f('ab\x1b[2Jcd'), { start: 2, end: 6, kind: 'screen' })
  assert.deepEqual(f('\x1b[3J'), { start: 0, end: 4, kind: 'scrollback' })
  assert.deepEqual(f('\x1b[J'), { start: 0, end: 3, kind: 'below' })
  assert.deepEqual(f('\x1b[0J'), { start: 0, end: 4, kind: 'below' })
  assert.deepEqual(f('x\x1bc'), { start: 1, end: 3, kind: 'reset' })
  assert.equal(f('\x1b[?2J'), null, '带 ? 的是选择性擦除，不是清屏')
  assert.equal(f('\x1b[1J'), null)
  assert.equal(f('\x1b[20J'), null)
  assert.equal(f('\x1b[31mred\x1b[0m\x1b[K'), null)
  assert.equal(f('\x1b]0;title\x07\x1b[38;5;12m'), null)
  assert.deepEqual(f('\x1b[31m\x1b[2J'), { start: 5, end: 9, kind: 'screen' })
})

test('findClear：末尾被截断的序列返回 partial，明显不是目标的不留', () => {
  assert.deepEqual(findClear(b('ab\x1b'), 0), { partial: 2 })
  assert.deepEqual(findClear(b('ab\x1b['), 0), { partial: 2 })
  assert.deepEqual(findClear(b('ab\x1b[3'), 0), { partial: 2 })
  assert.equal(findClear(b('ab\x1b[38;5'), 0), null)
  assert.equal(findClear(b('ab\x1b[?25'), 0), null)
})

test('没有清屏时普通输出被攒起来，flush 时一次写入', async () => {
  const h = new FakeHost()
  const f = new Flattener(h)
  await f.feed(b('hello '), 0)
  await f.feed(b('world'), 10)
  assert.deepEqual(h.log, [])
  await f.flush()
  assert.deepEqual(h.log, ['hello world'])
  assert.equal(h.probes, 0, '没有清屏就不用探测屏幕')
})

test('攒够 batchBytes 就自动写出', async () => {
  const h = new FakeHost()
  const f = new Flattener(h, { batchBytes: 8 })
  await f.feed(b('1234'), 0)
  assert.deepEqual(h.log, [])
  await f.feed(b('56789'), 1)
  assert.deepEqual(h.log, ['123456789'])
})

test('孤立的清屏：先把屏幕推进历史，忽略 ESC[3J，保留 ESC[2J', async () => {
  const h = new FakeHost()
  const f = new Flattener(h)
  await f.feed(b('before\x1b[H\x1b[2J\x1b[3Jafter'), 100)
  await f.flush()
  assert.deepEqual(h.log, ['before\x1b[H', PUSH5, '\x1b[2J', 'after'])
  assert.equal(f.pushed, 1)
})

test('推进序列：保存光标与属性、复位颜色、移到底行、按已用行数换行、恢复', () => {
  assert.equal(pushScreenSequence(24, 3), '\x1b7\x1b[0m\x1b[24;1H\n\n\n\x1b8')
})

test('3J 在前、2J 在后的写法也共享同一个判定（仍然推进历史）', async () => {
  const h = new FakeHost()
  const f = new Flattener(h)
  await f.feed(b('x\x1b[3J\x1b[H\x1b[2Jy'), 500)
  await f.flush()
  assert.deepEqual(h.log, ['x', '\x1b[H', PUSH5, '\x1b[2J', 'y'])
})

test('相隔很近的清屏是程序在刷新画面：原样处理（不推进，3J 也照常发出）', async () => {
  const h = new FakeHost()
  const f = new Flattener(h)
  await f.feed(b('A\x1b[2J\x1b[3J'), 0)
  await f.feed(b('B\x1b[2J\x1b[3J'), 1000)
  await f.flush()
  assert.deepEqual(h.log, ['A', PUSH5, '\x1b[2J', 'B', '\x1b[2J', '\x1b[3J'])
  assert.equal(f.pushed, 1)
})

test('相隔超过 rapidMs 的两次清屏都平铺', async () => {
  const h = new FakeHost()
  let n = 0
  h.screen = () => ({ fingerprint: `frame-${n++}` })
  const f = new Flattener(h, { rapidMs: 3000 })
  await f.feed(b('A\x1b[2J'), 0)
  await f.feed(b('B\x1b[2J'), 3500)
  await f.flush()
  assert.equal(f.pushed, 2)
  assert.equal(h.log.filter(s => s === PUSH5).length, 2)
})

test('同样的画面不重复推进', async () => {
  const h = new FakeHost()
  const f = new Flattener(h, { rapidMs: 10 })
  await f.feed(b('A\x1b[2J'), 0)
  await f.feed(b('B\x1b[2J\x1b[3J'), 5000)
  await f.flush()
  assert.equal(f.pushed, 1)
  assert.deepEqual(h.log, ['A', PUSH5, '\x1b[2J', 'B', '\x1b[2J'])
})

test('屏幕是空的时不推进，但清屏序列照常发出', async () => {
  const h = new FakeHost()
  h.screen = () => ({ used: 0, fingerprint: '' })
  const f = new Flattener(h)
  await f.feed(b('\x1b[2J'), 0)
  await f.flush()
  assert.deepEqual(h.log, ['\x1b[2J'])
  assert.equal(f.pushed, 0)
})

test('备用屏幕（全屏程序）里的清屏照常处理', async () => {
  const h = new FakeHost()
  h.screen = () => ({ alt: true })
  const f = new Flattener(h)
  await f.feed(b('a\x1b[2J\x1b[3Jb'), 0)
  await f.flush()
  assert.deepEqual(h.log, ['a', '\x1b[2J', '\x1b[3J', 'b'])
  assert.equal(f.pushed, 0)
})

test('ESC[J 只有光标在左上角才算清屏', async () => {
  const h = new FakeHost()
  h.screen = () => ({ cursorX: 4, cursorY: 3 })
  const f = new Flattener(h)
  await f.feed(b('p\x1b[J'), 0)
  await f.flush()
  assert.deepEqual(h.log, ['p', '\x1b[J'])
  assert.equal(f.pushed, 0)

  const h2 = new FakeHost()
  const f2 = new Flattener(h2)
  await f2.feed(b('p\x1b[H\x1b[J'), 0)
  await f2.flush()
  assert.deepEqual(h2.log, ['p\x1b[H', PUSH5, '\x1b[J'])
})

test('ESC c（RIS）：先推进历史，再换成不清历史的软复位', async () => {
  const h = new FakeHost()
  const f = new Flattener(h)
  await f.feed(b('x\x1bcy'), 0)
  await f.flush()
  assert.deepEqual(h.log, ['x', PUSH5, '\x1b[!p\x1b[H\x1b[2J', 'y'])

  const h2 = new FakeHost()
  const f2 = new Flattener(h2)
  await f2.feed(b('\x1b[2J'), 0)
  await f2.feed(b('\x1bc'), 200)
  await f2.flush()
  assert.deepEqual(h2.log.slice(-1), ['\x1bc'], '相隔很近的 RIS 原样发出')
})

test('序列被拆在两批数据之间也能认出来', async () => {
  const h = new FakeHost()
  const f = new Flattener(h)
  await f.feed(b('x\x1b['), 0)
  await f.feed(b('2Jy'), 5)
  await f.flush()
  assert.deepEqual(h.log, ['x', PUSH5, '\x1b[2J', 'y'])

  const h2 = new FakeHost()
  const f2 = new Flattener(h2)
  await f2.feed(b('a\x1b'), 0)
  await f2.feed(b('[3J'), 5)
  await f2.feed(b('b'), 6)
  await f2.flush()
  assert.deepEqual(h2.log, ['a', 'b'], '3J 被丢弃，前后的输出完整保留')
})

test('明显不是目标的被截断序列不会被扣留', async () => {
  const h = new FakeHost()
  const f = new Flattener(h)
  await f.feed(b('a\x1b[38;5'), 0)
  await f.flush()
  assert.deepEqual(h.log, ['a\x1b[38;5'])
  await f.feed(b(';12mb'), 1)
  await f.flush()
  assert.deepEqual(h.log, ['a\x1b[38;5', ';12mb'])
})

test('finish 把末尾悬着的半截序列原样写出', async () => {
  const h = new FakeHost()
  const f = new Flattener(h)
  await f.feed(b('x\x1b[3'), 0)
  assert.deepEqual(h.log, [])
  await f.finish()
  assert.deepEqual(h.log, ['x', '\x1b[3'])
})

test('maxPushes 限制往历史里推屏幕的次数', async () => {
  const h = new FakeHost()
  let n = 0
  h.screen = () => ({ fingerprint: `f${n++}` })
  const f = new Flattener(h, { maxPushes: 2, rapidMs: 10 })
  for (let i = 0; i < 5; i++) await f.feed(b(`s${i}\x1b[2J`), i * 1000)
  await f.flush()
  assert.equal(f.pushed, 2)
  assert.equal(h.log.filter(s => s === PUSH5).length, 2)
})

test('照原样抹掉历史之后，相同的画面可以再推进一次', async () => {
  const h = new FakeHost()
  const f = new Flattener(h, { rapidMs: 3000 })
  await f.feed(b('A\x1b[2J'), 0) // 孤立：推进 frame-A
  await f.feed(b('B\x1b[2J\x1b[3J'), 1000) // 很近：照原样，3J 抹掉历史
  await f.feed(b('C\x1b[2J'), 10000) // 孤立：同样的 frame-A 应当再推进
  await f.flush()
  assert.equal(f.pushed, 2)
})

test('一批数据里连着两次清屏：它们属于同一次清屏，第二次时屏幕已经是空的，不会重复推进', async () => {
  const h = new FakeHost()
  let n = 0
  h.screen = () => (n++ === 0 ? {} : { used: 0, fingerprint: '' })
  const f = new Flattener(h)
  await f.feed(b('1\x1b[2J2\x1b[2J3'), 0)
  await f.flush()
  assert.equal(f.pushed, 1)
  assert.deepEqual(h.log, ['1', PUSH5, '\x1b[2J', '2', '\x1b[2J', '3'])
})

// ---------- 全屏程序（备用屏幕）退出前的最后一屏 ----------

/** 会跟着写入的内容切换「是否在备用屏幕」的假终端：进入 ESC[?1049h / ?47h 后 alt 为真，退出序列写入后为假。 */
class AltHost implements FlattenHost {
  log: string[] = []
  alt = false
  lines: string[] = []
  cursorX = 0
  cols = 40
  async write(d: Uint8Array): Promise<void> {
    const s = dec.decode(d)
    this.log.push(s)
    if (/\x1b\[\?(1049|47)h/.test(s)) this.alt = true
    if (/\x1b\[\?[0-9;]*(1049|1047|47)[0-9;]*l/.test(s)) this.alt = false
  }
  probe(): ScreenProbe {
    return { rows: 24, cols: this.cols, lines: this.lines, cursorX: this.cursorX, cursorY: 0, alt: this.alt, used: 0, fingerprint: '' }
  }
}

const LEAVE = '\x1b[?1049l'
const ENTER = '\x1b[?1049h'

test('findEvent：认出退出备用屏幕的各种写法，其他私有模式不算', () => {
  const f = (s: string) => findEvent(b(s), 0)
  assert.deepEqual(f('a\x1b[?1049lb'), { start: 1, end: 9, kind: 'altexit' })
  assert.deepEqual(f('\x1b[?1047l'), { start: 0, end: 8, kind: 'altexit' })
  assert.deepEqual(f('\x1b[?47l'), { start: 0, end: 6, kind: 'altexit' })
  assert.deepEqual(f('\x1b[?25;1049l'), { start: 0, end: 11, kind: 'altexit' }, '参数里夹着 1049 也算')
  assert.equal(f('\x1b[?25l'), null, '隐藏光标不是退出备用屏幕')
  assert.equal(f('\x1b[?1049h'), null, '进入不算')
  assert.equal(f('\x1b[?2004l'), null)
  assert.equal(f('\x1b[?1l'), null)
  assert.equal(f('\x1b[?10490l'), null, '参数必须恰好是 47 / 1047 / 1049')
  assert.equal(f('\x1b[1049l'), null, '不带 ? 的不是私有模式')
  assert.deepEqual(f('\x1b[2J\x1b[?1049l'), { start: 0, end: 4, kind: 'screen' }, '清屏仍然先被认出来')
})

test('findEvent：被拆在批次之间的退出序列返回 partial；findClear 的行为不变', () => {
  assert.deepEqual(findEvent(b('ab\x1b[?'), 0), { partial: 2 })
  assert.deepEqual(findEvent(b('ab\x1b[?10'), 0), { partial: 2 })
  assert.deepEqual(findEvent(b('ab\x1b[?1049'), 0), { partial: 2 })
  assert.equal(findEvent(b('ab\x1b[38;5'), 0), null, '普通参数的半截序列不扣留')
  assert.equal(findEvent(b('ab\x1b[?' + '1'.repeat(40)), 0), null, '过长的私有参数不再等')
  assert.equal(findClear(b('ab\x1b[?1049l'), 0), null, 'findClear 只管清屏：输出带据此定位「最后一次清屏」')
  assert.equal(findClear(b('ab\x1b[?10'), 0), null)
})

test('trimScreen：去掉首尾的空行和控制字符，行间的空行保留', () => {
  assert.deepEqual(trimScreen(['', '', 'A  ', '', 'B', '   ', '']), ['A', '', 'B'])
  assert.deepEqual(trimScreen(['', '   ', '']), [])
  assert.deepEqual(trimScreen([]), [])
  const t = trimScreen(['\x1b[31mred\x1b[0m', 'tab\there', 'c1\u0085x'])
  for (const row of t) assert.doesNotMatch(row, /[\u0000-\u001f\u007f-\u009f]/, '屏幕上的文字写回终端前不能带控制字符')
})

test('altScreenBlock：分割线占满一行，上下箭头相对，最后一屏按行写出', () => {
  const block = altScreenBlock(['L1', '', 'L3'], 40, true)
  const m = /^\x1b\[0m\x1b\[2m(.*)\x1b\[0m\r\nL1\r\n\r\nL3\r\n\x1b\[2m(.*)\x1b\[0m\r\n$/.exec(block)
  assert.ok(m, '结构：复位属性 + 上分割线 + 各行 + 下分割线')
  assert.equal(displayWidth(m![1]), 40)
  assert.equal(displayWidth(m![2]), 40)
  assert.match(m![1], /↓ 全屏程序退出前的最后一屏/)
  assert.match(m![2], /↑ 全屏程序退出前的最后一屏/)
})

test('altScreenBlock：光标不在行首先换行；窄屏换更短的标签；放不下标签就只画线；没有内容返回空串', () => {
  assert.ok(altScreenBlock(['x'], 40, false).startsWith('\r\n\x1b[0m'))
  assert.ok(altScreenBlock(['x'], 40, true).startsWith('\x1b[0m'))
  const narrow = altScreenBlock(['x'], 20, true)
  assert.match(narrow, /↓ 最后一屏/)
  assert.match(altScreenBlock(['x'], 10, true), /\x1b\[2m─{10}\x1b\[0m/)
  assert.equal(altScreenBlock([], 40, true), '')
})

test('全屏程序退出：在它还在备用屏幕时读出最后一屏，发出退出序列，再把这一屏写在原位置', async () => {
  const h = new AltHost()
  h.lines = ['', 'LINE-ONE', '', 'LINE-THREE', '', '']
  const f = new Flattener(h)
  await f.feed(b(`$ vim x\r\n${ENTER}HELLO${LEAVE}AFTER`), 0)
  await f.flush()
  assert.deepEqual(h.log, [`$ vim x\r\n${ENTER}HELLO`, LEAVE, altScreenBlock(['LINE-ONE', '', 'LINE-THREE'], 40, true), 'AFTER'])
  assert.equal(f.altKeptCount, 1)
})

test('不在备用屏幕时遇到退出序列：只原样发出，不加任何东西', async () => {
  const h = new AltHost()
  h.lines = ['stale']
  const f = new Flattener(h)
  await f.feed(b(`a${LEAVE}b`), 0)
  await f.flush()
  assert.deepEqual(h.log, ['a', LEAVE, 'b'])
  assert.equal(f.altKeptCount, 0)
})

test('最后一屏是空的：不留东西', async () => {
  const h = new AltHost()
  h.lines = ['', '   ', '']
  const f = new Flattener(h)
  await f.feed(b(`${ENTER}${LEAVE}z`), 0)
  await f.flush()
  assert.deepEqual(h.log, [ENTER, LEAVE, 'z'])
  assert.equal(f.altKeptCount, 0)
})

test('退出之后光标不在行首：先换行，分割线独占一行', async () => {
  const h = new AltHost()
  h.lines = ['X']
  h.cursorX = 7
  const f = new Flattener(h)
  await f.feed(b(`${ENTER}${LEAVE}`), 0)
  await f.flush()
  assert.equal(h.log[2], altScreenBlock(['X'], 40, false))
  assert.ok(h.log[2].startsWith('\r\n'))
})

test('同样的内容相隔很近地反复出现（程序在来回切换备用屏幕）只留一块；隔得久了或内容不同照常保留', async () => {
  const h = new AltHost()
  h.lines = ['SAME']
  const f = new Flattener(h, { rapidMs: 3000 })
  await f.feed(b(`${ENTER}${LEAVE}`), 0)
  await f.feed(b(`${ENTER}${LEAVE}`), 1000)
  await f.feed(b(`${ENTER}${LEAVE}`), 2500)
  assert.equal(f.altKeptCount, 1, '连续切换算一次：时间窗口随最近一次切换顺延')
  await f.feed(b(`${ENTER}${LEAVE}`), 2500 + 3500)
  assert.equal(f.altKeptCount, 2, '隔了一阵再出现，是新的一次')
  h.lines = ['OTHER']
  await f.feed(b(`${ENTER}${LEAVE}`), 2500 + 3500 + 100)
  assert.equal(f.altKeptCount, 3, '内容不同，即使很近也保留')
})

test('maxAltScreens 限制保留的块数，超出的只发退出序列', async () => {
  const h = new AltHost()
  const f = new Flattener(h, { maxAltScreens: 2, rapidMs: 10 })
  for (let i = 0; i < 4; i++) {
    h.lines = [`screen-${i}`]
    await f.feed(b(`${ENTER}${LEAVE}`), i * 1000)
  }
  await f.flush()
  assert.equal(f.altKeptCount, 2)
  assert.equal(h.log.filter(s => s.includes('最后一屏')).length, 2, '每块是一次写入')
  assert.equal(h.log.filter(s => s === LEAVE).length, 4, '退出序列一个不少')
})

test('退出序列被拆在两批数据之间也能认出来', async () => {
  const h = new AltHost()
  h.alt = true
  h.lines = ['PAGE']
  const f = new Flattener(h)
  await f.feed(b('x\x1b[?10'), 0)
  await f.feed(b('49ly'), 5)
  await f.flush()
  assert.deepEqual(h.log, ['x', LEAVE, altScreenBlock(['PAGE'], 40, true), 'y'])
})

test('?47l 与 ?1047l 同样保留最后一屏', async () => {
  for (const leave of ['\x1b[?47l', '\x1b[?1047l']) {
    const h = new AltHost()
    h.alt = true
    h.lines = ['OLD-STYLE']
    const f = new Flattener(h)
    await f.feed(b(`q${leave}r`), 0)
    await f.flush()
    assert.deepEqual(h.log, ['q', leave, altScreenBlock(['OLD-STYLE'], 40, true), 'r'])
  }
})

test('退出之后终端仍在备用屏幕（异常）：不往里面写东西', async () => {
  const h = new AltHost()
  h.alt = true
  h.lines = ['A']
  // 这个假终端写入退出序列后仍然报告在备用屏幕。
  const stuck: FlattenHost = { write: d => h.write(d).then(() => { h.alt = true }), probe: () => h.probe() }
  const f = new Flattener(stuck)
  await f.feed(b(`${LEAVE}`), 0)
  await f.flush()
  assert.deepEqual(h.log, [LEAVE])
  assert.equal(f.altKeptCount, 0, '没写块就不计数')
})
