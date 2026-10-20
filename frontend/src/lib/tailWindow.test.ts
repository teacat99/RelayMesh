import { describe, it } from 'node:test'
import assert from 'node:assert/strict'
import {
  TAIL_BATCH_BLOCKS,
  TAIL_INITIAL_BLOCKS,
  TAIL_PROBE_PX,
  TAIL_REVEAL_SLACK,
  effectiveTailStart,
  initialTailStart,
  nextTailStart,
  revealTailStart,
  shouldFillNow,
  shouldHoldFirst,
} from './tailWindow'

describe('末尾渐进渲染的窗口规则（#169 第三期）', () => {
  it('块数不超过 TAIL_INITIAL_BLOCKS：起点是 0，一块都不延后（行为与以前完全一样）', () => {
    for (const n of [0, 1, TAIL_INITIAL_BLOCKS - 1, TAIL_INITIAL_BLOCKS]) assert.equal(initialTailStart(n), 0, `n=${n}`)
  })

  it('超过之后：只先渲染最新的 TAIL_INITIAL_BLOCKS 块', () => {
    assert.equal(initialTailStart(TAIL_INITIAL_BLOCKS + 1), 1)
    assert.equal(initialTailStart(131), 131 - TAIL_INITIAL_BLOCKS)
  })

  it('钉住之后：新通知追加在尾部，起点不动，窗口只会变长（不滑动）', () => {
    const pinned = initialTailStart(131)
    assert.equal(effectiveTailStart(pinned, 131), pinned)
    assert.equal(effectiveTailStart(pinned, 132), pinned)
    assert.equal(effectiveTailStart(pinned, 140), pinned)
  })

  it('没钉住（null）时按当前总数取最新 TAIL_INITIAL_BLOCKS 块', () => {
    assert.equal(effectiveTailStart(null, 10), 0)
    assert.equal(effectiveTailStart(null, 131), 131 - TAIL_INITIAL_BLOCKS)
  })

  it('总数缩水（重新分摞、条目被收走）：起点被压回「最新 TAIL_INITIAL_BLOCKS 块」的位置以内，末尾不会渲染成空', () => {
    assert.equal(effectiveTailStart(100, 60), 60 - TAIL_INITIAL_BLOCKS)
    assert.equal(effectiveTailStart(100, 20), 0)
    assert.equal(effectiveTailStart(100, 0), 0)
  })

  it('补齐一批：起点往前挪 TAIL_BATCH_BLOCKS 块，到 0 为止，不会出负数', () => {
    assert.equal(nextTailStart(101), 101 - TAIL_BATCH_BLOCKS)
    assert.equal(nextTailStart(TAIL_BATCH_BLOCKS + 1), 1)
    assert.equal(nextTailStart(TAIL_BATCH_BLOCKS), 0)
    assert.equal(nextTailStart(3), 0)
    assert.equal(nextTailStart(0), 0)
  })

  it('从总数 N 一路补齐：批数是确定的，最后一定落在 0', () => {
    let start = initialTailStart(131)
    let batches = 0
    while (start > 0) {
      start = nextTailStart(start)
      batches++
      assert.ok(batches < 100, '补齐没有收敛')
    }
    assert.equal(batches, Math.ceil((131 - TAIL_INITIAL_BLOCKS) / TAIL_BATCH_BLOCKS))
  })

  it('被点名要看某一块：起点退到它前面 TAIL_REVEAL_SLACK 块；已经渲染的不动；无效下标不动', () => {
    assert.equal(revealTailStart(101, 40), 40 - TAIL_REVEAL_SLACK)
    assert.equal(revealTailStart(101, 1), 0)
    assert.equal(revealTailStart(101, 0), 0)
    assert.equal(revealTailStart(40, 120), 40, '目标在已渲染部分里：起点不变')
    assert.equal(revealTailStart(40, 40), 40 - TAIL_REVEAL_SLACK, '目标正好是已渲染的第一块：再往前留几块')
    assert.equal(revealTailStart(40, -1), 40)
  })

  it('用户向上够到已渲染的第一块（顶边离视口顶边不到 TAIL_PROBE_PX）才不等空闲、立刻补齐', () => {
    assert.equal(shouldFillNow(-5000), false, '还远在视口上方')
    assert.equal(shouldFillNow(-TAIL_PROBE_PX), false, '刚好在边界上：还不算')
    assert.equal(shouldFillNow(-TAIL_PROBE_PX + 1), true)
    assert.equal(shouldFillNow(0), true)
    assert.equal(shouldFillNow(400), true, '已经在视口里了')
  })

  it('补齐时钉住第一块的条件：它在视口顶边或更上面（视口整个在插入点之下）；在视口里面 / 下面就不钉', () => {
    assert.equal(shouldHoldFirst(-3000), true, '用户停在底部：第一块远在视口上方')
    assert.equal(shouldHoldFirst(0), true)
    assert.equal(shouldHoldFirst(1), false)
    assert.equal(shouldHoldFirst(600), false, '用户正在看插入点上面的轮次：不能把视口推走')
  })
})
