import test from 'node:test'
import assert from 'node:assert/strict'
import { isTouchPrimaryInput, moveCaretToEnd, type CaretTarget } from './textareaCaret'

// 记录调用顺序的假输入框
function fakeTextarea(value: string, scrollHeight = 480) {
  const calls: string[] = []
  const el: CaretTarget & { selection: [number, number] | null; focusOptions: unknown } = {
    value,
    scrollHeight,
    scrollTop: 0,
    selection: null,
    focusOptions: undefined,
    focus(options) {
      calls.push('focus')
      el.focusOptions = options
    },
    setSelectionRange(start, end) {
      calls.push('select')
      el.selection = [start, end]
    }
  }
  return { el, calls }
}

test('光标放到全文末尾，并滚动到最底部', () => {
  const { el, calls } = fakeTextarea('第一行\n第二行\n新增的内容', 900)
  moveCaretToEnd(el, { focus: true, focused: false })
  assert.deepEqual(el.selection, [el.value.length, el.value.length])
  assert.equal(el.scrollTop, 900)
  // 先聚焦再设置选区：聚焦时浏览器可能还原旧选区，顺序反过来会被它覆盖
  assert.deepEqual(calls, ['focus', 'select'])
})

test('聚焦时不让整页跟着滚动（preventScroll）', () => {
  const { el } = fakeTextarea('abc')
  moveCaretToEnd(el, { focus: true, focused: false })
  assert.deepEqual(el.focusOptions, { preventScroll: true })
})

test('输入框已经拿着焦点时不重复聚焦', () => {
  const { el, calls } = fakeTextarea('abc')
  moveCaretToEnd(el, { focus: true, focused: true })
  assert.deepEqual(calls, ['select'])
})

test('触屏设备（focus=false）只移动光标与滚动，不抢焦点以免弹出软键盘', () => {
  const { el, calls } = fakeTextarea('abc', 300)
  moveCaretToEnd(el, { focus: false, focused: false })
  assert.deepEqual(calls, ['select'])
  assert.deepEqual(el.selection, [3, 3])
  assert.equal(el.scrollTop, 300)
})

test('空内容也不会出错：光标 0，滚动到 scrollHeight', () => {
  const { el } = fakeTextarea('', 0)
  moveCaretToEnd(el, { focus: false, focused: false })
  assert.deepEqual(el.selection, [0, 0])
  assert.equal(el.scrollTop, 0)
})

test('按 UTF-16 长度定位末尾：含表情与中文也落在最后', () => {
  const text = '继续\n👍 好的'
  const { el } = fakeTextarea(text)
  moveCaretToEnd(el, { focus: false, focused: false })
  assert.deepEqual(el.selection, [text.length, text.length])
})

test('没有 matchMedia（如纯 Node 环境）时按非触屏处理', () => {
  const original = (globalThis as { matchMedia?: unknown }).matchMedia
  delete (globalThis as { matchMedia?: unknown }).matchMedia
  try {
    assert.equal(isTouchPrimaryInput(), false)
  } finally {
    if (original !== undefined) (globalThis as { matchMedia?: unknown }).matchMedia = original
  }
})

test('matchMedia 判定粗糙指针时视为触屏', () => {
  const original = (globalThis as { matchMedia?: unknown }).matchMedia
  ;(globalThis as { matchMedia?: unknown }).matchMedia = (query: string) => ({ matches: query === '(pointer: coarse)' })
  try {
    assert.equal(isTouchPrimaryInput(), true)
  } finally {
    if (original === undefined) delete (globalThis as { matchMedia?: unknown }).matchMedia
    else (globalThis as { matchMedia?: unknown }).matchMedia = original
  }
})
