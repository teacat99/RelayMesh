import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import {
  FOLLOW_RECHECK_FRAMES,
  STALE_FRAME_MS,
  USER_SCROLL_GRACE_MS,
  isEditableTarget,
  isInputPending,
  isStaleFrame,
  isUpwardKey,
  isUpwardTouchMove,
  isUpwardWheel,
  mayFollow,
  userHolding,
} from './followBottom'

describe('userHolding：用户刚表达过「向上看」，程序化贴底让位', () => {
  it('从没有过意图：不让位', () => {
    assert.equal(userHolding(null, 1000), false)
  })

  it('宽限期内让位，到点放开（边界：恰好等于宽限期已经放开）', () => {
    assert.equal(userHolding(1000, 1000), true)
    assert.equal(userHolding(1000, 1000 + USER_SCROLL_GRACE_MS - 1), true)
    assert.equal(userHolding(1000, 1000 + USER_SCROLL_GRACE_MS), false)
    assert.equal(userHolding(1000, 1000 + USER_SCROLL_GRACE_MS + 5000), false)
  })

  it('宽限期可以传：更短的宽限期更早放开', () => {
    assert.equal(userHolding(0, 99, 100), true)
    assert.equal(userHolding(0, 100, 100), false)
  })

  it('时钟回拨（now 比时间戳小）也算让位，不会把负数当成过期', () => {
    assert.equal(userHolding(2000, 1500), true)
  })
})

describe('mayFollow：四个让位条件任何一个成立都不跟随', () => {
  const open = { scrolledUp: false, holding: false, inputPending: false, settling: false }

  it('全都不成立才跟随', () => {
    assert.equal(mayFollow(open), true)
  })

  for (const key of ['scrolledUp', 'holding', 'inputPending', 'settling'] as const) {
    it(`${key} 成立：让位`, () => {
      assert.equal(mayFollow({ ...open, [key]: true }), false)
    })
  }

  it('几个同时成立也只是让位', () => {
    assert.equal(mayFollow({ scrolledUp: true, holding: true, inputPending: true, settling: true }), false)
  })
})

describe('意图判定', () => {
  it('滚轮：deltaY < 0 才是向上；0 与向下都不是', () => {
    assert.equal(isUpwardWheel(-1), true)
    assert.equal(isUpwardWheel(-120), true)
    assert.equal(isUpwardWheel(0), false)
    assert.equal(isUpwardWheel(120), false)
  })

  it('按键：方向键上 / PageUp / Home / Shift+空格是向上；方向键下 / PageDown / End / 空格不是', () => {
    for (const key of ['ArrowUp', 'PageUp', 'Home']) assert.equal(isUpwardKey({ key }), true, key)
    assert.equal(isUpwardKey({ key: ' ', shiftKey: true }), true)
    for (const key of ['ArrowDown', 'PageDown', 'End', 'a', 'Enter', 'Escape']) assert.equal(isUpwardKey({ key }), false, key)
    assert.equal(isUpwardKey({ key: ' ' }), false)
    assert.equal(isUpwardKey({ key: ' ', shiftKey: false }), false)
  })

  it('触摸：手指向下拖（clientY 变大）才是向上看；不足 2 px 的抖动与向上拖都不算', () => {
    assert.equal(isUpwardTouchMove(100, 140), true)
    assert.equal(isUpwardTouchMove(100, 102), true)
    assert.equal(isUpwardTouchMove(100, 101), false)
    assert.equal(isUpwardTouchMove(100, 100), false)
    assert.equal(isUpwardTouchMove(100, 60), false)
    assert.equal(isUpwardTouchMove(100, 101, 1), true, 'minPx 可以传')
  })

  it('输入控件里的按键不算滚动意图', () => {
    assert.equal(isEditableTarget({ tagName: 'TEXTAREA' }), true)
    assert.equal(isEditableTarget({ tagName: 'input' }), true)
    assert.equal(isEditableTarget({ tagName: 'SELECT' }), true)
    assert.equal(isEditableTarget({ tagName: 'DIV', isContentEditable: true }), true)
    assert.equal(isEditableTarget({ tagName: 'DIV' }), false)
    assert.equal(isEditableTarget({ tagName: 'BODY' }), false)
    assert.equal(isEditableTarget({}), false)
    assert.equal(isEditableTarget(null), false)
    assert.equal(isEditableTarget(undefined), false)
  })
})

describe('isInputPending：只有 Chromium 的 navigator.scheduling 才查得到', () => {
  it('没有 scheduling / 没有 isInputPending / 没传：一律当作没有排队的输入', () => {
    assert.equal(isInputPending({}), false)
    assert.equal(isInputPending({ scheduling: {} }), false)
    assert.equal(isInputPending(undefined), isInputPending(typeof navigator === 'undefined' ? undefined : (navigator as never)))
  })

  it('带 includeContinuous 去问（滚轮是连续事件，不带就查不到）', () => {
    let asked: unknown
    const nav = { scheduling: { isInputPending: (o?: { includeContinuous?: boolean }) => { asked = o; return true } } }
    assert.equal(isInputPending(nav), true)
    assert.deepEqual(asked, { includeContinuous: true })
  })

  it('返回假 / 抛异常：都是没有', () => {
    assert.equal(isInputPending({ scheduling: { isInputPending: () => false } }), false)
    assert.equal(isInputPending({ scheduling: { isInputPending: () => { throw new Error('boom') } } }), false)
  })
})

describe('isStaleFrame：主线程刚被长任务占过的「迟到的帧」不做贴底决定', () => {
  it('正常的帧（落后几毫秒到几十毫秒）不算迟到；边界：恰好等于阈值不算，超过才算', () => {
    assert.equal(isStaleFrame(1000, 1000), false)
    assert.equal(isStaleFrame(1000, 1010), false)
    assert.equal(isStaleFrame(1000, 1000 + STALE_FRAME_MS), false)
    assert.equal(isStaleFrame(1000, 1000 + STALE_FRAME_MS + 1), true)
  })

  it('实测的两种帧：忙了 1.5 s 之后的第一帧落后 1.3 ～ 1.5 s（迟到），紧跟着的一帧落后 10 ms 上下（正常）', () => {
    assert.equal(isStaleFrame(2931, 2931 + 1472), true)
    assert.equal(isStaleFrame(4402, 4402 + 10), false)
  })

  it('取不到帧时间（缺省 / null / NaN / 无穷）一律当作不迟到，免得贴底永远做不成', () => {
    assert.equal(isStaleFrame(undefined, 1e9), false)
    assert.equal(isStaleFrame(null, 1e9), false)
    assert.equal(isStaleFrame(Number.NaN, 1e9), false)
    assert.equal(isStaleFrame(Number.POSITIVE_INFINITY, 1e9), false)
  })

  it('自定义阈值；帧时间晚于 now（时钟抖动）不算迟到', () => {
    assert.equal(isStaleFrame(1000, 1100, 200), false)
    assert.equal(isStaleFrame(1000, 1201, 200), true)
    assert.equal(isStaleFrame(1005, 1000), false)
  })

  it('阈值是个合理的小值：大于一帧（16.7 ms）的几倍、远小于一个长任务（50 ms 起）的整数倍', () => {
    assert.ok(STALE_FRAME_MS >= 32 && STALE_FRAME_MS <= 100)
  })
})

describe('常量', () => {
  it('复核帧数是个小正整数（D-142 的双帧校准）', () => {
    assert.ok(Number.isInteger(FOLLOW_RECHECK_FRAMES) && FOLLOW_RECHECK_FRAMES >= 1 && FOLLOW_RECHECK_FRAMES <= 4)
  })
})
