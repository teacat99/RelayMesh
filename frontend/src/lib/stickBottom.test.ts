// 运行：pnpm --dir frontend run test:unit
// 守护消息列表「贴底」的判定（#222 / #833）：停在底部时内容 / 容器变大要贴回去，用户向上翻过就不抢滚动。
import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, it } from 'node:test'
import { PIN_TOLERANCE_PX, REPIN_MIN_GAP_PX, bottomGap, isPinnedAtBottom, repinTarget, wasPinnedBeforeResize, type ScrollMetrics } from './stickBottom'

/** 一个视口 600、内容 H、滚到 top 的度量。 */
const m = (scrollHeight: number, scrollTop: number, clientHeight = 600): ScrollMetrics => ({ scrollHeight, scrollTop, clientHeight })

describe('贴底判定 isPinnedAtBottom', () => {
  it('正好在底部、差几个像素以内都算贴底；超过容差就不算', () => {
    assert.equal(isPinnedAtBottom(m(2000, 1400)), true) // gap 0
    assert.equal(isPinnedAtBottom(m(2000, 1396)), true) // gap 4（容差边界）
    assert.equal(isPinnedAtBottom(m(2000, 1395.9)), false) // gap 4.1
    assert.equal(isPinnedAtBottom(m(2000, 1300)), false) // 向上翻了 100px
    assert.equal(PIN_TOLERANCE_PX, 4)
  })

  it('负的偏差（iOS 回弹、scrollTop 取整过头）也算贴底', () => {
    assert.equal(bottomGap(m(2000, 1401)), -1)
    assert.equal(isPinnedAtBottom(m(2000, 1401)), true)
  })

  it('内容比视口矮（没有滚动条）时总是贴底', () => {
    assert.equal(isPinnedAtBottom(m(300, 0)), true)
  })

  it('容差比「回到底部」按钮的 80px 阈值严得多：向上翻 5px 就不再自动贴回', () => {
    assert.equal(isPinnedAtBottom(m(2000, 1395)), false)
  })
})

describe('变化之前贴着底部吗 wasPinnedBeforeResize（用上一次的大小推算，不依赖会过期的滚动事件标记）', () => {
  it('没有上一次的大小：当作没贴底（不抢）', () => {
    assert.equal(wasPinnedBeforeResize(null, 1400), false)
  })

  it('原来在底部：内容变高之后 scrollTop 没动，用旧大小算仍是贴底', () => {
    // 旧：2000 / 600，scrollTop 1400 = 贴底；现在内容长到 2120（scrollTop 仍是 1400）
    assert.equal(wasPinnedBeforeResize({ scrollHeight: 2000, clientHeight: 600 }, 1400), true)
    assert.equal(wasPinnedBeforeResize({ scrollHeight: 2000, clientHeight: 600 }, 1396), true) // 4px 容差边界
  })

  it('原来向上翻了一截：不贴底', () => {
    assert.equal(wasPinnedBeforeResize({ scrollHeight: 2000, clientHeight: 600 }, 1300), false)
    assert.equal(wasPinnedBeforeResize({ scrollHeight: 2000, clientHeight: 600 }, 1395), false)
  })

  it('浏览器的滚动锚定已经把 scrollTop 往下挪了：算出负的离底，同样算贴底', () => {
    assert.equal(wasPinnedBeforeResize({ scrollHeight: 2000, clientHeight: 600 }, 1460), true)
  })

  it('容器变矮（输入区拉高 / 窗口缩小）之前贴着底部：用旧的 clientHeight 算', () => {
    // 旧：2000 / 600 / scrollTop 1400 贴底；容器缩到 540 之后 scrollTop 仍是 1400
    assert.equal(wasPinnedBeforeResize({ scrollHeight: 2000, clientHeight: 600 }, 1400), true)
  })
})

describe('贴回去的目标 repinTarget', () => {
  it('停在底部时内容变高了：贴回去（目标 = scrollHeight，由浏览器截断到最大值）', () => {
    // 原来 2000 / 1400（贴底），晚到的装饰让内容长到 2120，scrollTop 没变
    const grown = m(2120, 1400)
    assert.equal(bottomGap(grown), 120)
    assert.equal(repinTarget(grown, true), 2120)
  })

  it('停在底部时容器变矮了（输入区拉高 / 窗口缩小）：同样贴回去', () => {
    const shorter = m(2000, 1400, 540) // 视口少了 60px，底边离内容底边 60px
    assert.equal(bottomGap(shorter), 60)
    assert.equal(repinTarget(shorter, true), 2000)
  })

  it('用户向上翻过（之前没贴底）：不动，绝不抢滚动', () => {
    assert.equal(repinTarget(m(2120, 1300), false), null)
    assert.equal(repinTarget(m(2120, 1400), false), null)
  })

  it('已经在底部（或差不到半个像素）：不动，免得亚像素抖动反复写 scrollTop', () => {
    assert.equal(repinTarget(m(2000, 1400), true), null)
    assert.equal(repinTarget(m(2000, 1399.6), true), null)
    assert.equal(repinTarget(m(2000, 1399.4), true), 2000)
    assert.equal(REPIN_MIN_GAP_PX, 0.5)
  })

  it('一连串晚到变化：每次变高都贴回去，之后用户向上翻一点就停手', () => {
    let scrollTop = 1400
    let height = 2000
    let pinned = isPinnedAtBottom(m(height, scrollTop))
    assert.equal(pinned, true)
    for (const grow of [3, 22, 50, 1]) {
      height += grow
      const t = repinTarget(m(height, scrollTop), pinned)
      assert.notEqual(t, null)
      scrollTop = Math.min(t!, height - 600) // 浏览器截断到最大值
      pinned = isPinnedAtBottom(m(height, scrollTop))
      assert.equal(pinned, true)
    }
    // 用户向上翻 30px：之后的变化不再管
    scrollTop -= 30
    pinned = isPinnedAtBottom(m(height, scrollTop))
    assert.equal(pinned, false)
    height += 80
    assert.equal(repinTarget(m(height, scrollTop), pinned), null)
  })
})

describe('组件接线：PreviewMessageList 用它，并且让位给正在跑的滚动校准', () => {
  /** 单测是编译到缓存目录里跑的，不能用 import.meta.url 找源码；从当前目录往 frontend/src 找。 */
  function srcDir(): string {
    for (const base of [join(process.cwd(), 'src'), join(process.cwd(), 'frontend', 'src')]) {
      if (existsSync(join(base, 'lib', 'stickBottom.test.ts'))) return base
    }
    throw new Error('找不到 frontend/src，请在仓库根目录或 frontend 目录下运行测试')
  }
  const vue = () => readFileSync(join(srcDir(), 'components/feedback/preview-view/PreviewMessageList.vue'), 'utf8')

  it('用 ResizeObserver 观察滚动容器与内容列，大小变了走 repinTarget', () => {
    const src = vue()
    assert.match(src, /new ResizeObserver\(/)
    assert.match(src, /repinTarget\(/)
    assert.match(src, /isPinnedAtBottom\(/)
    // 标记会过期（初次渲染中途的滚动事件），所以「变化之前贴着底部吗」还要用上一次的大小推算
    assert.match(src, /wasPinnedBeforeResize\(lastSize, now\.scrollTop\)/)
    assert.match(src, /ref="columnRef"/)
  })

  it('滚动校准（settle / hold）正在跑时不贴底：两套机制不能抢同一个 scrollTop', () => {
    const src = vue()
    assert.match(src, /scrollJobActive\(\)/)
    assert.match(src, /tailJobActive\(\)/)
  })

  it('滚动事件里更新「最近一次是否贴底」', () => {
    const src = vue()
    const handleScroll = src.slice(src.indexOf('function handleScroll()'), src.indexOf('defineExpose('))
    assert.match(handleScroll, /pinnedAtBottom = isPinnedAtBottom\(/)
  })
})
