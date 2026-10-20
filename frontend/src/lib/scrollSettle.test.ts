// 运行：pnpm --dir frontend run test:unit
// 滚动位置校准（#158）的纯函数：平滑滚动落地后落点怎么算、钉锚点时偏了多少、什么时候不用动；
// 驱动（composables/useScrollSettle.ts）靠 e2e（preview-longlist.mjs）与下面的源码级防线。
import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, it } from 'node:test'
import {
  HOLD_BOUNDARY_ROUNDS,
  HOLD_QUIET_MS,
  HOLD_TIMEOUT_MS,
  HOLD_TOLERANCE_PX,
  SETTLE_MAX_FIXES,
  SETTLE_PADDING_PX,
  SETTLE_QUIET_MS,
  SETTLE_TIMEOUT_MS,
  SETTLE_TOLERANCE_PX,
  foreignScroll,
  holdBoundaryKeys,
  holdDelta,
  holdStep,
  settleTarget,
  type HoldState,
} from './scrollSettle'
import { jumpScrollTop } from './timelineRail'

const M = { scrollHeight: 10_000, clientHeight: 600 }

describe('settleTarget：落地后该把容器放到哪', () => {
  it('已经在目标的容差之内：不动（null）', () => {
    // 条目顶边在 1000，留 24 的 padding → 目标 976
    assert.equal(settleTarget({ itemTop: 1000, scrollTop: 976, ...M }), null)
    assert.equal(settleTarget({ itemTop: 1000, scrollTop: 976 + SETTLE_TOLERANCE_PX, ...M }), null)
    assert.equal(settleTarget({ itemTop: 1000, scrollTop: 976 - SETTLE_TOLERANCE_PX, ...M }), null)
  })

  it('偏出容差：返回要去的 scrollTop（与 jumpScrollTop 同一套规则）', () => {
    assert.equal(settleTarget({ itemTop: 1000, scrollTop: 976 + SETTLE_TOLERANCE_PX + 1, ...M }), 976)
    assert.equal(settleTarget({ itemTop: 1000, scrollTop: 500, ...M }), jumpScrollTop(1000, M))
    // 平滑滚动途中新渲染的轮次让目标往下挪了 1200px：目标变了，要补
    assert.equal(settleTarget({ itemTop: 2200, scrollTop: 976, ...M }), 2176)
  })

  it('夹在可滚范围内：贴顶不为负，贴底不超过 scrollHeight - clientHeight', () => {
    assert.equal(settleTarget({ itemTop: 10, scrollTop: 300, ...M }), 0)
    assert.equal(settleTarget({ itemTop: 9900, scrollTop: 100, ...M }), M.scrollHeight - M.clientHeight)
    // 已经贴底、目标本来就被夹到底：不动
    assert.equal(settleTarget({ itemTop: 9900, scrollTop: M.scrollHeight - M.clientHeight, ...M }), null)
    // 已经贴顶、目标被夹到 0：不动
    assert.equal(settleTarget({ itemTop: 10, scrollTop: 0, ...M }), null)
  })

  it('padding 与容差可调', () => {
    assert.equal(settleTarget({ itemTop: 1000, scrollTop: 900, ...M }, 100), null)
    assert.equal(settleTarget({ itemTop: 1000, scrollTop: 900, ...M }, 0), 1000)
    assert.equal(settleTarget({ itemTop: 1000, scrollTop: 970, ...M }, 24, 10), null)
    assert.equal(settleTarget({ itemTop: 1000, scrollTop: 960, ...M }, 24, 10), 976)
  })

  it('任何一项不是有限数：不动（宁可不补，也别把容器放到 NaN）', () => {
    for (const bad of [NaN, Infinity, -Infinity]) {
      assert.equal(settleTarget({ itemTop: bad, scrollTop: 0, ...M }), null)
      assert.equal(settleTarget({ itemTop: 100, scrollTop: bad, ...M }), null)
      assert.equal(settleTarget({ itemTop: 100, scrollTop: 0, scrollHeight: bad, clientHeight: 600 }), null)
      assert.equal(settleTarget({ itemTop: 100, scrollTop: 0, scrollHeight: 10_000, clientHeight: bad }), null)
    }
  })

  it('默认参数与轮次元素的 scroll-mt-6（24px）、jumpScrollTop 的 padding 一致', () => {
    assert.equal(SETTLE_PADDING_PX, 24)
    assert.equal(jumpScrollTop(1000, M), 1000 - SETTLE_PADDING_PX)
    assert.ok(SETTLE_MAX_FIXES >= 1 && SETTLE_MAX_FIXES <= 5, '补的次数要有上限，防止来回震荡')
    assert.ok(SETTLE_QUIET_MS >= 50 && SETTLE_TIMEOUT_MS > SETTLE_QUIET_MS * SETTLE_MAX_FIXES)
  })
})

describe('holdDelta：钉锚点时锚点偏了多少', () => {
  it('在容差之内：不动（null）', () => {
    assert.equal(holdDelta(100, 100), null)
    assert.equal(holdDelta(100 + HOLD_TOLERANCE_PX, 100), null)
    assert.equal(holdDelta(100 - HOLD_TOLERANCE_PX, 100), null)
    assert.equal(holdDelta(-30.4, -30), null)
  })

  it('偏出容差：正数 = 锚点比预期更靠下（scrollTop 要加上这么多），负数反之', () => {
    assert.equal(holdDelta(623 + 1974, 623), 1974)
    assert.equal(holdDelta(200, 250), -50)
    assert.equal(holdDelta(100 + HOLD_TOLERANCE_PX + 0.5, 100), HOLD_TOLERANCE_PX + 0.5)
    // 预期位置可以是负的（锚点的顶边在视口上方）
    assert.equal(holdDelta(-100, -140), 40)
  })

  it('容差可调；任何一项不是有限数：不动（宁可不补，也别把容器放到 NaN）', () => {
    assert.equal(holdDelta(110, 100, 10), null)
    assert.equal(holdDelta(111, 100, 10), 11)
    for (const bad of [NaN, Infinity, -Infinity]) {
      assert.equal(holdDelta(bad, 100), null)
      assert.equal(holdDelta(100, bad), null)
    }
  })

  it('常量：容差是亚像素量级，安静期短于总时限，总时限有上限', () => {
    assert.ok(HOLD_TOLERANCE_PX >= 0.5 && HOLD_TOLERANCE_PX <= 2)
    assert.ok(HOLD_QUIET_MS >= 100 && HOLD_TIMEOUT_MS > HOLD_QUIET_MS * 2 && HOLD_TIMEOUT_MS <= 5000)
  })
})

describe('foreignScroll：scrollTop 被别人动过，是浏览器的滚动锚定还是用户', () => {
  it('没被动过：none（版面高度变了也一样——锚点偏了多少由 holdDelta 去补）', () => {
    assert.equal(foreignScroll(500, 500, 9000, 9000), 'none')
    assert.equal(foreignScroll(500, 500, 9000, 12000), 'none')
    assert.equal(foreignScroll(0, 0, 100, 100), 'none')
  })

  it('scrollTop 与版面高度都变了：layout（浏览器的滚动锚定，继续钉；它常常只补一部分增高）', () => {
    // 实测的一批：向上载入后，紧挨锚点的两轮从占位变成真实高度，版面 +3947、锚定只把 scrollTop 加了 1973
    assert.equal(foreignScroll(11341, 9368, 117852, 113905), 'layout')
    // 版面变矮（占位 → 更矮的真实高度）时锚定也会往回拉
    assert.equal(foreignScroll(100, 500, 8000, 9000), 'layout')
  })

  it('scrollTop 变了而版面高度没变：user（手机上手指抬起后的惯性滑动没有任何事件，立刻放手）', () => {
    assert.equal(foreignScroll(480, 500, 9000, 9000), 'user')
    assert.equal(foreignScroll(0, 9368, 113905, 113905), 'user')
  })

  it('任何一项不是有限数：按 user 处理（宁可放手，也别在坏数据上钉）', () => {
    for (const bad of [NaN, Infinity, -Infinity]) {
      assert.equal(foreignScroll(bad, 500, 9000, 9000), 'user')
      assert.equal(foreignScroll(500, bad, 9000, 9000), 'user')
      assert.equal(foreignScroll(500, 500, bad, 9000), 'user')
      assert.equal(foreignScroll(500, 500, 9000, bad), 'user')
    }
  })
})

describe('holdBoundaryKeys：向上载入时紧挨着锚点、不套离屏占位的那几轮', () => {
  const keys = Array.from({ length: 150 }, (_, i) => `r${i}`)

  it('展示最近 10 轮再载 10 轮：原来的第一轮是 r140，紧挨着它上面的 4 轮（r136–r139）豁免', () => {
    assert.deepEqual(holdBoundaryKeys(keys, 10, 10), ['r136', 'r137', 'r138', 'r139'])
    // 再载一批（现在展示最近 20 轮）：原来的第一轮是 r130，豁免 r126–r129
    assert.deepEqual(holdBoundaryKeys(keys, 20, 10), ['r126', 'r127', 'r128', 'r129'])
  })

  it('这批不够 boundary 轮：只取这批里有的（最后一批）', () => {
    // 共 12 轮、已展示 10 轮、再载 10 轮：只多出最早的 2 轮
    assert.deepEqual(holdBoundaryKeys(keys.slice(0, 12), 10, 10), ['r0', 'r1'])
    assert.deepEqual(holdBoundaryKeys(keys.slice(0, 11), 10, 10), ['r0'])
  })

  it('没有更早的轮次、或没有要载的轮次：空', () => {
    assert.deepEqual(holdBoundaryKeys(keys, 150, 10), [])
    assert.deepEqual(holdBoundaryKeys(keys, 999, 10), [], '已展示数大于总数也不该出错')
    assert.deepEqual(holdBoundaryKeys(keys, 10, 0), [])
    assert.deepEqual(holdBoundaryKeys([], 10, 10), [])
    assert.deepEqual(holdBoundaryKeys(keys, 10, 10, 0), [])
  })

  it('负数 / 小数之类的坏参数不抛、不越界', () => {
    assert.deepEqual(holdBoundaryKeys(keys, -5, 10), ['r146', 'r147', 'r148', 'r149'], '已展示数按 0 算：整批（最后 10 轮）都是新进来的，取这批最下面的 4 轮')
    assert.deepEqual(holdBoundaryKeys(keys, 10, -3), [])
  })

  it('默认个数是 HOLD_BOUNDARY_ROUNDS（4）；不修改入参', () => {
    assert.equal(HOLD_BOUNDARY_ROUNDS, 4)
    assert.equal(holdBoundaryKeys(keys, 10, 10).length, HOLD_BOUNDARY_ROUNDS)
    assert.equal(holdBoundaryKeys(keys, 10, 10, 2).length, 2)
    assert.equal(keys.length, 150)
    assert.equal(keys[0], 'r0')
  })

  it('逐批展开到顶：每一批豁免的都落在这一批新进来的轮次里、紧挨着原来的第一轮', () => {
    let visible = 10
    while (visible < keys.length) {
      const oldFirst = keys.length - visible
      const picked = holdBoundaryKeys(keys, visible, 10)
      assert.ok(picked.length > 0, `visible=${visible}`)
      const idx = picked.map(k => keys.indexOf(k))
      // 连续、紧挨着原来的第一轮（下标 oldFirst 的上面）、都在新窗口里（下标 ≥ 新的第一轮）
      assert.equal(idx[idx.length - 1], oldFirst - 1)
      assert.deepEqual(idx, Array.from({ length: idx.length }, (_, i) => idx[0] + i))
      assert.ok(idx[0] >= Math.max(0, keys.length - visible - 10))
      visible += 10
    }
  })
})

describe('holdStep：钉锚点每帧的决策', () => {
  const base = (over: Partial<HoldState> = {}): HoldState => ({ ours: 9368, lastHeight: 113905, quietSince: 1000, ...over })
  const tick = (over: Partial<Parameters<typeof holdStep>[1]> = {}) => ({
    now: 1016,
    startedAt: 1000,
    scrollTop: 9368,
    scrollHeight: 113905,
    offsetNow: 622,
    offsetWanted: 622,
    ...over,
  })

  it('没有偏差、安静期没到：等下一帧，状态不变', () => {
    const r = holdStep(base(), tick())
    assert.deepEqual(r.action, { kind: 'wait' })
    assert.deepEqual(r.state, base())
  })

  it('锚点偏了：返回要补的量，安静期从此刻重新算', () => {
    const r = holdStep(base(), tick({ offsetNow: 622 + 1974 }))
    assert.deepEqual(r.action, { kind: 'fix', delta: 1974 })
    assert.equal(r.state.quietSince, 1016)
    // 偏上去也一样：负数
    assert.deepEqual(holdStep(base(), tick({ offsetNow: 600 })).action, { kind: 'fix', delta: -22 })
  })

  it('连续 quietMs 没有偏差：收手（settled）；差一毫秒还不收', () => {
    assert.deepEqual(holdStep(base(), tick({ now: 1000 + HOLD_QUIET_MS })).action, { kind: 'stop', reason: 'settled' })
    assert.deepEqual(holdStep(base(), tick({ now: 1000 + HOLD_QUIET_MS - 1 })).action, { kind: 'wait' })
    // 容差之内的偏差不算偏差
    assert.deepEqual(holdStep(base(), tick({ now: 1000 + HOLD_QUIET_MS, offsetNow: 622 + HOLD_TOLERANCE_PX })).action, { kind: 'stop', reason: 'settled' })
  })

  it('每补一次安静期就重新算：补完要再安静满 quietMs 才收手', () => {
    const fixed = holdStep(base(), tick({ now: 1100, offsetNow: 700 }))
    assert.equal(fixed.action.kind, 'fix')
    assert.deepEqual(holdStep(fixed.state, tick({ now: 1100 + HOLD_QUIET_MS - 1 })).action, { kind: 'wait' })
    assert.deepEqual(holdStep(fixed.state, tick({ now: 1100 + HOLD_QUIET_MS })).action, { kind: 'stop', reason: 'settled' })
  })

  it('超过总时限：放弃（timeout），哪怕锚点还偏着', () => {
    assert.deepEqual(holdStep(base(), tick({ now: 1000 + HOLD_TIMEOUT_MS + 1, offsetNow: 900 })).action, { kind: 'stop', reason: 'timeout' })
    assert.equal(holdStep(base(), tick({ now: 1000 + HOLD_TIMEOUT_MS, offsetNow: 900 })).action.kind, 'fix', '刚好到时限那一刻还算数')
  })

  it('用户的惯性滑动（scrollTop 变了、版面高度没变）：立刻放手，哪怕锚点偏着也不把滑动拉回来', () => {
    const r = holdStep(base(), tick({ scrollTop: 9200, offsetNow: 790 }))
    assert.deepEqual(r.action, { kind: 'stop', reason: 'user' })
  })

  it('浏览器的滚动锚定只补了一部分增高（scrollTop、版面高度都变了，锚点还偏着）：不放手，补上剩下的——生产与慢 CPU 实测的那一批', () => {
    // 钉好之后下一帧：紧挨锚点的两轮从占位变成真实高度，版面 +3947；锚定只把 scrollTop 加了 1973（补了更上面那一轮），锚点被顶下去 1974
    const r = holdStep(base(), tick({ scrollTop: 9368 + 1973, scrollHeight: 113905 + 3947, offsetNow: 622 + 1974 }))
    assert.deepEqual(r.action, { kind: 'fix', delta: 1974 })
    assert.equal(r.state.ours, 9368 + 1973, '认下锚定放的位置：之后用户再动才看得出来')
    assert.equal(r.state.lastHeight, 113905 + 3947)
  })

  it('锚定把增高补全了（scrollTop、版面高度都变了，锚点没偏）：不放手也不动；认下新位置，之后用户再动才算用户', () => {
    const r1 = holdStep(base(), tick({ scrollTop: 13315, scrollHeight: 117852, now: 1032 }))
    assert.deepEqual(r1.action, { kind: 'wait' })
    assert.equal(r1.state.ours, 13315)
    // 版面高度没再变，scrollTop 却被动了：这回是用户
    assert.deepEqual(holdStep(r1.state, tick({ scrollTop: 13000, scrollHeight: 117852, offsetNow: 937, now: 1048 })).action, { kind: 'stop', reason: 'user' })
  })

  it('版面高度变了但 scrollTop 还在我们放的位置（浏览器没补）：照常按锚点偏差去补', () => {
    const r = holdStep(base(), tick({ scrollHeight: 113905 + 1974, offsetNow: 622 + 1974 }))
    assert.deepEqual(r.action, { kind: 'fix', delta: 1974 })
    assert.equal(r.state.ours, 9368)
  })

  it('scrollTop / 版面高度不是有限数：放手；锚点位置不是有限数：不补（宁可不动）', () => {
    assert.deepEqual(holdStep(base(), tick({ scrollTop: NaN })).action, { kind: 'stop', reason: 'user' })
    assert.deepEqual(holdStep(base(), tick({ scrollHeight: Infinity })).action, { kind: 'stop', reason: 'user' })
    assert.equal(holdStep(base(), tick({ offsetNow: NaN })).action.kind, 'wait')
  })

  it('模拟：紧挨锚点的占位晚一帧变高 + 锚定只补一半，逐帧推进，锚点最终回到原位后收手（用户没动）', () => {
    // 极简版面：锚点在内容里的位置 docY，视口里的位置 = docY - scrollTop
    let docY = 9368 + 622
    let scrollTop = 9368
    let height = 113905
    let state: HoldState = { ours: scrollTop, lastHeight: height, quietSince: 1000 }
    const events: Record<number, () => void> = {
      // 第 2 帧：两个占位变成真实高度（版面 +3947），浏览器的锚定只补了一半（scrollTop +1973）
      2: () => {
        docY += 3947
        height += 3947
        scrollTop += 1973
      },
      // 第 5 帧：又来一点晚到的增高（图片加载），锚定没补
      5: () => {
        docY += 120
        height += 120
      },
    }
    let now = 1000
    let stopped: string | null = null
    let fixes = 0
    for (let frame = 0; frame < 200 && !stopped; frame++) {
      events[frame]?.()
      now += 16
      const step = holdStep(state, { now, startedAt: 1000, scrollTop, scrollHeight: height, offsetNow: docY - scrollTop, offsetWanted: 622 })
      state = step.state
      if (step.action.kind === 'stop') stopped = step.action.reason
      else if (step.action.kind === 'fix') {
        scrollTop += step.action.delta
        state = { ...state, ours: scrollTop }
        fixes++
      }
    }
    assert.equal(stopped, 'settled')
    assert.equal(docY - scrollTop, 622, '锚点回到钉住的位置')
    assert.equal(fixes, 2, '两次偏差各补一次')
  })

  it('模拟：补的过程中用户接手（惯性滑动）就停，不再动 scrollTop', () => {
    let docY = 9368 + 622
    let scrollTop = 9368
    const height = 113905
    let state: HoldState = { ours: scrollTop, lastHeight: height, quietSince: 1000 }
    // 锚点偏了 300，补一次
    docY += 300
    let step = holdStep(state, { now: 1016, startedAt: 1000, scrollTop, scrollHeight: height, offsetNow: docY - scrollTop, offsetWanted: 622 })
    assert.equal(step.action.kind, 'fix')
    scrollTop += (step.action as { delta: number }).delta
    state = { ...step.state, ours: scrollTop }
    // 下一帧：用户的惯性把 scrollTop 往上带了 80，版面没变
    scrollTop -= 80
    step = holdStep(state, { now: 1032, startedAt: 1000, scrollTop, scrollHeight: height, offsetNow: docY - scrollTop, offsetWanted: 622 })
    assert.deepEqual(step.action, { kind: 'stop', reason: 'user' })
  })
})

describe('useScrollSettle 驱动（源码级防线）', () => {
  function srcDir(): string {
    for (const base of [join(process.cwd(), 'src'), join(process.cwd(), 'frontend', 'src')]) {
      if (existsSync(join(base, 'lib', 'scrollSettle.test.ts'))) return base
    }
    throw new Error('找不到 frontend/src，请在仓库根目录或 frontend 目录下运行测试')
  }
  const src = readFileSync(join(srcDir(), 'composables', 'useScrollSettle.ts'), 'utf8')
  const list = readFileSync(join(srcDir(), 'components', 'feedback', 'preview-view', 'PreviewMessageList.vue'), 'utf8')

  it('用户自己动了（滚轮 / 触摸 / 点按 / 按键）就放弃，不和用户抢滚动', () => {
    for (const ev of ['wheel', 'touchstart', 'pointerdown', 'keydown']) assert.ok(src.includes(`'${ev}'`), `要监听 ${ev}`)
    assert.match(src, /for \(const \[target, type\] of userEvents\) target\.removeEventListener\(type, stop\)/)
  })

  it('条目被摘掉 / 超时 / 卸载都会收手；同一时刻只有一个校准', () => {
    assert.match(src, /if \(!el\.isConnected \|\| !container\.isConnected\) return stop\(\)/)
    assert.match(src, /if \(now - startedAt > timeoutMs\) return stop\(\)/)
    assert.match(src, /onBeforeUnmount\(cancel\)/)
    assert.match(src, /function settle\([^)]*\) \{\s*\n\s*cancel\(\)/)
  })

  it('钉锚点：用户动了（含惯性滑动）就放手；浏览器的滚动锚定动过 scrollTop 不算用户、继续钉；第一次校正不等下一帧', () => {
    const hold = src.slice(src.indexOf('function hold('), src.indexOf('onBeforeUnmount(cancel)'))
    assert.ok(hold.length > 200, '找不到 hold 驱动')
    for (const ev of ['wheel', 'touchstart', 'pointerdown', 'keydown']) assert.ok(hold.includes(`'${ev}'`), `hold 要监听 ${ev}`)
    // 决策都在纯函数 holdStep 里（下面单独测）：驱动每帧带着状态走一步，stop 就收手，fix 就把 scrollTop 加上要补的量并把读回来的位置记成 ours
    assert.match(hold, /let state: HoldState = \{ ours: container\.scrollTop, lastHeight: container\.scrollHeight, quietSince: startedAt \}/)
    assert.match(hold, /const step = holdStep\(state, \{[\s\S]*?scrollTop: container\.scrollTop,\s*\n\s*scrollHeight: container\.scrollHeight,[\s\S]*?offsetNow: offsetInContainer\(container, el\),\s*\n\s*offsetWanted: wantedOffset,/)
    assert.match(hold, /state = step\.state\s*\n\s*if \(step\.action\.kind === 'stop'\) return stop\(\)/)
    assert.match(hold, /container\.scrollTop \+= step\.action\.delta\s*\n[^\n]*\n\s*state = \{ \.\.\.state, ours: container\.scrollTop \}/)
    assert.ok(!/container\.scrollTop !== ours/.test(hold), '不能再把「scrollTop 被动过」一概当成用户（锚定只补一半的那几批会留下 2000px 的偏差）')
    assert.match(hold, /if \(!el\.isConnected \|\| !container\.isConnected\) return stop\(\)/)
    assert.match(hold, /function hold\([\s\S]*?\) \{\s*\n\s*cancel\(\)/)
    assert.match(hold, /stopCurrent = stop\s*\n[^\n]*\n\s*tick\(\)/)
    assert.match(src, /return \{ settle, hold, cancel \}/)
  })

  it('向上载入更早的轮次：钉住原来的第一轮（不再靠 scrollHeight 之差），锚点被摘掉时才退回旧算法', () => {
    const load = list.slice(list.indexOf('function loadEarlierRounds()'), list.indexOf('function setupTopObserver()'))
    assert.ok(load.length > 300, '找不到 loadEarlierRounds')
    assert.match(load, /const firstGroup = container\.querySelector<HTMLElement>\('\[data-testid="turn-group"\]'\)/)
    assert.match(load, /const anchor = firstGroup\?\.querySelector<HTMLElement>\('\[id\^="session-"\]'\) \?\? firstGroup/)
    assert.match(load, /const anchorOffset = anchor \? offsetInContainer\(container, anchor\) : 0/)
    assert.match(load, /if \(anchor\?\.isConnected\) \{[\s\S]*?holdAnchor\(c, anchor, anchorOffset\)[\s\S]*?\} else \{\s*\n\s*c\.scrollTop = oldScrollTop \+ \(c\.scrollHeight - oldScrollHeight\)/)
    assert.match(list, /const \{ settle: settleScroll, hold: holdAnchor \} = useScrollSettle\(\)/)
    assert.match(list, /import \{ offsetInContainer, useScrollSettle \} from/)
  })

  it('向上载入：紧挨着锚点的几轮豁免 pv-cv，与窗口增长在同一个 tick 里改；换工作流时清空', () => {
    const load = list.slice(list.indexOf('function loadEarlierRounds()'), list.indexOf('function setupTopObserver()'))
    assert.match(list, /import \{ holdBoundaryKeys \} from '\.\.\/\.\.\/\.\.\/lib\/scrollSettle'/)
    assert.match(list, /const boundaryRounds = ref<Set<string>>\(new Set\(\)\)/)
    const setAt = load.indexOf('boundaryRounds.value = new Set(')
    const growAt = load.indexOf('visibleRoundsCount.value = Math.min(')
    assert.ok(setAt > 0 && growAt > setAt, '豁免集要在窗口增长之前设好（同一个 tick，同一次渲染里生效）')
    assert.match(load, /holdBoundaryKeys\(props\.conversationRounds\.map\(r => r\.session_id\), visibleRoundsCount\.value, BATCH_LOAD_ROUNDS\)/)
    // 豁免集与窗口增长之间不能隔着 await / nextTick：必须是同一个 tick
    assert.ok(!/await|nextTick/.test(load.slice(setAt, growAt)) && !/nextTick\([\s\S]*boundaryRounds/.test(load), '不能放到 nextTick 里：那时第一次渲染已经按占位画完')
    // 较早的行只带首行预览（#169 第二期）：这一批的正文要先取回（await），再量锚点——等待期间用户还在滚，量早了锚点会过期
    const waitAt = load.indexOf('await ensureWindowBodies(')
    const measureAt = load.indexOf('const oldScrollHeight')
    assert.ok(waitAt > 0 && measureAt > waitAt && setAt > measureAt, '取回正文的等待要在量锚点与设豁免集之前')
    assert.match(list, /visibleRoundsCount\.value = INITIAL_VISIBLE_ROUNDS\s*\n\s*boundaryRounds\.value = new Set\(\)/)
  })

  it('向上载入：忙碌标志放开后重新观察顶部哨兵——忙碌期里丢掉的那次「进入范围」通知不会让列表卡在这一批', () => {
    const load = list.slice(list.indexOf('function loadEarlierRounds()'), list.indexOf('function setupTopObserver()'))
    const releaseAt = load.indexOf('isLoadingEarlier.value = false', load.indexOf('setTimeout'))
    const rearmAt = load.indexOf('rearmTopObserver()', releaseAt)
    assert.ok(releaseAt > 0 && rearmAt > releaseAt, '放开忙碌标志之后要重新观察哨兵：IntersectionObserver 只在进入 / 离开时通知，用户在这一批刚载完的几十毫秒里又滚回最上面，那次通知会撞上忙碌标志被丢掉')
    const rearm = load.slice(load.indexOf('function rearmTopObserver()'))
    const unobserveAt = rearm.indexOf('topObserver.unobserve(sentinel)')
    assert.ok(unobserveAt > 0 && rearm.indexOf('topObserver.observe(sentinel)') > unobserveAt, '重新观察 = 先 unobserve 再 observe：对已在观察的目标直接 observe 是空操作，不会补发当前状态的通知')
  })

  it('三处平滑跳转落地后都做校准：滚到某一轮、轨上点刻度、搜索命中落点', () => {
    assert.match(list, /const \{ settle: settleScroll, hold: holdAnchor \} = useScrollSettle\(\)/)
    assert.equal([...list.matchAll(/settleScroll\(/g)].length, 3)
    assert.match(list, /el\.scrollIntoView\(\{ behavior: 'smooth', block: 'start' \}\)\s*\n\s*settleScroll\(chatScrollContainer\.value, el, \{ onSettled: armHighlight \}\)/)
    assert.equal([...list.matchAll(/container\.scrollTo\(\{ top: jumpScrollTop\(top, container\), behavior: 'smooth' \}\)\s*\n[^\n]*\n?\s*settleScroll\(container, el, \{ onSettled: \(\) => flashElement\(container, el\) \}\)/g)].length, 2)
  })

  it('落点提示在落地时再亮一次：外框 / 闪烁不能只在起跑时亮（远距离平滑滚动要两三秒）', () => {
    // 驱动：平滑滚动自己落定（位置在容差内或补偿次数用完）才调 onSettled；被放弃的路径（用户接手、条目没了、超时）都是直接 stop()，不调
    const settle = src.slice(src.indexOf('function settle('), src.indexOf('function hold('))
    assert.match(src, /onSettled\?: \(\) => void/)
    assert.match(settle, /if \(target === null \|\| fixes >= maxFixes\) \{\s*\n\s*stop\(\)\s*\n\s*opts\.onSettled\?\.\(\)\s*\n\s*return/)
    assert.equal([...settle.matchAll(/onSettled/g)].length, 1, 'onSettled 只在落定那一处调用')
    // 滚到某一轮：起跑时亮、落地时再亮满 2.5 秒（同一个 armHighlight 重新计时）
    const toSession = list.slice(list.indexOf('function scrollToSession('), list.indexOf('function scrollToBottom('))
    assert.match(toSession, /const armHighlight = \(\) => \{\s*\n\s*highlightedSessionId\.value = sessionId\s*\n\s*if \(highlightTimer\) clearTimeout\(highlightTimer\)/)
    assert.match(toSession, /\}, 2500\)/)
    assert.match(toSession, /settleScroll\(chatScrollContainer\.value, el, \{ onSettled: armHighlight \}\)\s*\n\s*armHighlight\(\)/)
    // 轨上点非轮次刻度、搜索命中落点：起跑时闪一下，落地时再闪一下
    assert.equal([...list.matchAll(/onSettled: \(\) => flashElement\(container, el\)/g)].length, 2)
  })
})
