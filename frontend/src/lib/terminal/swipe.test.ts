// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import {
  SWIPE_DEFAULTS,
  SWIPE_FLICK_SPEED,
  SWIPE_IDLE,
  clampOffset,
  swipeCancel,
  swipeEnd,
  swipeMove,
  swipeStart,
  swipeTranslate,
  swipeVelocity,
  type SwipeConfig,
  type SwipeState,
} from './swipe'

const cfg: SwipeConfig = { width: 200, ...SWIPE_DEFAULTS }

/** 依次喂入一串坐标，返回最后的状态。 */
function drag(start: SwipeState, points: Array<[number, number]>): SwipeState {
  return points.reduce((s, [x, y]) => swipeMove(s, x, y, cfg), start)
}

describe('clampOffset', () => {
  it('限制在 [0, width]', () => {
    assert.equal(clampOffset(-30, 200), 0)
    assert.equal(clampOffset(80, 200), 80)
    assert.equal(clampOffset(999, 200), 200)
  })
  it('width 无效时恒为 0，NaN 不会传染到界面', () => {
    assert.equal(clampOffset(50, 0), 0)
    assert.equal(clampOffset(50, -10), 0)
    assert.equal(clampOffset(Number.NaN, 200), 0)
    assert.equal(clampOffset(50, Number.NaN), 0)
  })
})

describe('swipeMove 方向判定', () => {
  it('位移没超过锁定距离时不判定方向，也不移动内容', () => {
    const s = drag(swipeStart(100, 100, false, cfg), [[104, 101]])
    assert.equal(s.phase, 'pending')
    assert.equal(s.offset, 0)
  })

  it('向右的横向滑动：进入拖动状态，内容跟手', () => {
    const s = drag(swipeStart(100, 100, false, cfg), [[112, 102], [160, 104]])
    assert.equal(s.phase, 'dragging')
    assert.equal(s.offset, 60)
  })

  it('纵向滚动：判定为忽略，之后再怎么横向移动也不处理', () => {
    let s = drag(swipeStart(100, 100, false, cfg), [[104, 130]])
    assert.equal(s.phase, 'ignored')
    s = drag(s, [[200, 131], [300, 131]])
    assert.equal(s.phase, 'ignored')
    assert.equal(s.offset, 0)
  })

  it('斜着划但横向不够占优（没达到比例）按纵向处理', () => {
    // dx=20, dy=18：20 < 18*1.5，不算横向
    const s = drag(swipeStart(100, 100, false, cfg), [[120, 118]])
    assert.equal(s.phase, 'ignored')
  })

  it('收起状态下向左滑没有意义，交还给页面', () => {
    const s = drag(swipeStart(100, 100, false, cfg), [[70, 101]])
    assert.equal(s.phase, 'ignored')
  })

  it('展开状态下向左滑可以收起：偏移从 width 往回走', () => {
    const s = drag(swipeStart(100, 100, true, cfg), [[85, 100], [40, 101]])
    assert.equal(s.phase, 'dragging')
    assert.equal(s.offset, 140)
  })

  it('偏移不会超出 [0, width]', () => {
    const over = drag(swipeStart(0, 0, false, cfg), [[20, 0], [900, 0]])
    assert.equal(over.offset, 200)
    const under = drag(swipeStart(0, 0, true, cfg), [[-20, 0], [-900, 0]])
    assert.equal(under.offset, 0)
  })

  it('idle / ignored 状态下 swipeMove 原样返回', () => {
    assert.equal(swipeMove(SWIPE_IDLE, 50, 50, cfg), SWIPE_IDLE)
  })
})

describe('swipeEnd 松手判定', () => {
  it('没拖动过：保持原状且不算 moved（点击照常触发）', () => {
    const tap = swipeEnd(swipeStart(10, 10, false, cfg), cfg)
    assert.deepEqual(tap, { open: false, moved: false })
    const tapOpen = swipeEnd(swipeStart(10, 10, true, cfg), cfg)
    assert.deepEqual(tapOpen, { open: true, moved: false })
    const scroll = swipeEnd(drag(swipeStart(10, 10, false, cfg), [[12, 60]]), cfg)
    assert.deepEqual(scroll, { open: false, moved: false })
  })

  it('从收起拖到 40% 以上就展开，不到就弹回', () => {
    const past = drag(swipeStart(0, 0, false, cfg), [[20, 0], [90, 0]]) // 90 ≥ 80
    assert.deepEqual(swipeEnd(past, cfg), { open: true, moved: true })
    const short = drag(swipeStart(0, 0, false, cfg), [[20, 0], [70, 0]]) // 70 < 80
    assert.deepEqual(swipeEnd(short, cfg), { open: false, moved: true })
  })

  it('已展开时要往回拖过 40%（偏移低于 60%）才收起', () => {
    const slight = drag(swipeStart(100, 0, true, cfg), [[85, 0], [60, 0]]) // 偏移 160 ≥ 120
    assert.deepEqual(swipeEnd(slight, cfg), { open: true, moved: true })
    const much = drag(swipeStart(100, 0, true, cfg), [[85, 0], [10, 0]]) // 偏移 110 < 120
    assert.deepEqual(swipeEnd(much, cfg), { open: false, moved: true })
  })

  it('快速向右甩直接展开，快速向左甩直接收起，不看位置', () => {
    const small = drag(swipeStart(0, 0, false, cfg), [[12, 0], [30, 0]])
    assert.equal(swipeEnd(small, cfg, SWIPE_FLICK_SPEED).open, true)
    const nearlyOpen = drag(swipeStart(100, 0, true, cfg), [[85, 0], [70, 0]])
    assert.equal(swipeEnd(nearlyOpen, cfg, -SWIPE_FLICK_SPEED).open, false)
  })

  it('速度没达到甩动阈值时仍按位置判断', () => {
    const s = drag(swipeStart(0, 0, false, cfg), [[12, 0], [30, 0]])
    assert.equal(swipeEnd(s, cfg, SWIPE_FLICK_SPEED - 0.01).open, false)
  })
})

describe('其他', () => {
  it('swipeCancel 回到手势开始前的状态', () => {
    const dragging = drag(swipeStart(0, 0, true, cfg), [[-20, 0], [-60, 0]])
    assert.deepEqual(swipeCancel(dragging), { open: true, moved: true })
    assert.deepEqual(swipeCancel(swipeStart(0, 0, false, cfg)), { open: false, moved: false })
  })

  it('swipeVelocity：正常采样返回 px/ms；时间差为 0 或停顿太久返回 0', () => {
    assert.equal(swipeVelocity(100, 1000, 130, 1030), 1)
    assert.equal(swipeVelocity(100, 1000, 40, 1030), -2)
    assert.equal(swipeVelocity(100, 1000, 130, 1000), 0)
    assert.equal(swipeVelocity(100, 1000, 130, 1500), 0)
  })

  it('swipeTranslate：拖动中跟手，否则由展开状态决定', () => {
    const s = drag(swipeStart(0, 0, false, cfg), [[20, 0], [75, 0]])
    assert.equal(swipeTranslate(s, false, cfg), 75)
    assert.equal(swipeTranslate(SWIPE_IDLE, true, cfg), 200)
    assert.equal(swipeTranslate(SWIPE_IDLE, false, cfg), 0)
  })
})
