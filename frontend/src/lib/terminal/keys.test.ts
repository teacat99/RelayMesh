// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import { PAGE_DOWN, PAGE_UP, applyModifiers, cursorKey } from './keys'

describe('cursorKey', () => {
  it('普通模式用 CSI，应用光标模式用 SS3', () => {
    assert.equal(cursorKey('up', false), '\x1b[A')
    assert.equal(cursorKey('left', false), '\x1b[D')
    assert.equal(cursorKey('up', true), '\x1bOA')
    assert.equal(cursorKey('end', true), '\x1bOF')
    assert.equal(cursorKey('home', false), '\x1b[H')
  })

  it('翻页键序列固定', () => {
    assert.equal(PAGE_UP, '\x1b[5~')
    assert.equal(PAGE_DOWN, '\x1b[6~')
  })
})

describe('applyModifiers', () => {
  it('没有修饰键时原样通过且不消耗', () => {
    assert.deepEqual(applyModifiers('a', { ctrl: false, alt: false }), { data: 'a', applied: false })
  })

  it('Ctrl + 字母 → 控制码（大小写等价）', () => {
    assert.equal(applyModifiers('c', { ctrl: true, alt: false }).data, '\x03')
    assert.equal(applyModifiers('C', { ctrl: true, alt: false }).data, '\x03')
    assert.equal(applyModifiers('d', { ctrl: true, alt: false }).data, '\x04')
    assert.equal(applyModifiers('z', { ctrl: true, alt: false }).data, '\x1a')
    assert.equal(applyModifiers('l', { ctrl: true, alt: false }).data, '\x0c')
  })

  it('Ctrl + 符号', () => {
    assert.equal(applyModifiers('[', { ctrl: true, alt: false }).data, '\x1b')
    assert.equal(applyModifiers('\\', { ctrl: true, alt: false }).data, '\x1c')
    assert.equal(applyModifiers(' ', { ctrl: true, alt: false }).data, '\x00')
    assert.equal(applyModifiers('?', { ctrl: true, alt: false }).data, '\x7f')
    assert.equal(applyModifiers('1', { ctrl: true, alt: false }).data, '1')
  })

  it('Alt 加 ESC 前缀，可与 Ctrl 叠加', () => {
    assert.equal(applyModifiers('b', { ctrl: false, alt: true }).data, '\x1bb')
    assert.equal(applyModifiers('b', { ctrl: true, alt: true }).data, '\x1b\x02')
  })

  it('多字符输入（粘贴 / 输入法 / 转义序列）不受影响，也不消耗粘滞状态', () => {
    assert.deepEqual(applyModifiers('hello', { ctrl: true, alt: true }), { data: 'hello', applied: false })
    assert.deepEqual(applyModifiers('\x1b[A', { ctrl: true, alt: false }), { data: '\x1b[A', applied: false })
  })
})
