import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, it } from 'node:test'
import {
  LEAD_SETTLE_MS,
  SHELL_HINT_KEY,
  SHELL_HINT_MAX,
  SIDEBAR_GROUP_ROWS_KEY,
  SIDEBAR_GROUP_ROWS_MAX,
  chipReservation,
  leadReservation,
  leadWidthOf,
  patchShellHint,
  readShellHint,
  readSidebarGroupRows,
  shellWorkflowKey,
  stripMode,
  writeSidebarGroupRows,
} from './shellHints'

/** 内存里的假存储：记录写了几次，方便断言「内容没变不写」。 */
function fakeStorage(initial?: string) {
  const data = new Map<string, string>()
  if (initial !== undefined) data.set(SHELL_HINT_KEY, initial)
  const store = {
    writes: 0,
    getItem: (k: string) => data.get(k) ?? null,
    setItem: (k: string, v: string) => {
      store.writes++
      data.set(k, v)
    },
    raw: () => data.get(SHELL_HINT_KEY),
  }
  return store
}

/** 单测是编译到缓存目录里跑的，不能用 import.meta.url 找源码；从当前目录往 frontend/src 找。 */
function srcDir(): string {
  for (const base of [join(process.cwd(), 'src'), join(process.cwd(), 'frontend', 'src')]) {
    if (existsSync(join(base, 'lib', 'shellHints.test.ts'))) return base
  }
  throw new Error('找不到 frontend/src，请在仓库根目录或 frontend 目录下运行测试')
}
const read = (rel: string) => readFileSync(join(srcDir(), rel), 'utf8')

describe('外壳件占位记忆（#246）：读写', () => {
  it('没有记忆、工作流编号为空、存储里是坏 JSON：都当没有', () => {
    assert.equal(readShellHint('wf-a', fakeStorage()), null)
    assert.equal(readShellHint('', fakeStorage('{"": {"tasks": 3, "at": 1}}')), null)
    assert.equal(readShellHint('wf-a', fakeStorage('{坏的')), null)
    assert.equal(readShellHint('wf-a', fakeStorage('[1,2,3]')), null)
    assert.equal(readShellHint('wf-a', null), null)
  })

  it('任务数与芯片宽度各写各的，互不覆盖；宽度取整', () => {
    const s = fakeStorage()
    patchShellHint('wf-a', { tasks: 8 }, { storage: s, now: 100 })
    patchShellHint('wf-a', { chip: { w: 152.4, wide: true } }, { storage: s, now: 200 })
    assert.deepEqual(readShellHint('wf-a', s), { tasks: 8, chip: { w: 152, wide: true }, at: 200 })
    patchShellHint('wf-a', { tasks: 9 }, { storage: s, now: 300 })
    assert.deepEqual(readShellHint('wf-a', s), { tasks: 9, chip: { w: 152, wide: true }, at: 300 })
  })

  it('任务数为 0 / null 就忘掉这一项；两项都没有时整条删掉', () => {
    const s = fakeStorage()
    patchShellHint('wf-a', { tasks: 5, chip: { w: 100, wide: false } }, { storage: s, now: 1 })
    patchShellHint('wf-a', { tasks: 0 }, { storage: s, now: 2 })
    assert.deepEqual(readShellHint('wf-a', s), { chip: { w: 100, wide: false }, at: 2 })
    patchShellHint('wf-a', { chip: null }, { storage: s, now: 3 })
    assert.equal(readShellHint('wf-a', s), null)
    assert.equal(s.raw(), '{}')
  })

  it('内容没变不写存储（任务清单每次刷新、芯片每次量宽都会来改写，不能每次都落盘）', () => {
    const s = fakeStorage()
    patchShellHint('wf-a', { tasks: 4 }, { storage: s, now: 1 })
    assert.equal(s.writes, 1)
    patchShellHint('wf-a', { tasks: 4 }, { storage: s, now: 2 })
    patchShellHint('wf-a', {}, { storage: s, now: 3 })
    assert.equal(s.writes, 1)
    // 本来就没有记忆，又要忘掉：也不写
    patchShellHint('wf-b', { tasks: 0 }, { storage: s, now: 4 })
    patchShellHint('wf-b', { chip: null }, { storage: s, now: 5 })
    assert.equal(s.writes, 1)
    patchShellHint('wf-a', { tasks: 5 }, { storage: s, now: 6 })
    assert.equal(s.writes, 2)
  })

  it('脏数据逐条丢掉，好的保留；芯片宽度超出合理范围当脏数据', () => {
    const s = fakeStorage(
      JSON.stringify({
        ok: { tasks: 3, chip: { w: 120, wide: true }, at: 5 },
        negative: { tasks: -2, at: 1 },
        fractional: { tasks: 2.5, at: 1 },
        chipTiny: { chip: { w: 3, wide: true }, at: 1 },
        chipHuge: { chip: { w: 9999, wide: true }, at: 1 },
        chipNoFlag: { chip: { w: 100 }, at: 1 },
        notObject: 7,
        empty: {},
      }),
    )
    assert.deepEqual(readShellHint('ok', s), { tasks: 3, chip: { w: 120, wide: true }, at: 5 })
    for (const k of ['negative', 'fractional', 'chipTiny', 'chipHuge', 'chipNoFlag', 'notObject', 'empty']) {
      assert.equal(readShellHint(k, s), null, k)
    }
    // 写入一个不合理的宽度：不记，也不抹掉原来合理的
    patchShellHint('ok', { chip: { w: 5, wide: true } }, { storage: s, now: 9 })
    assert.equal(readShellHint('ok', s)?.chip, undefined)
  })

  it(`最多留 ${SHELL_HINT_MAX} 个工作流，淘汰最久没写过的`, () => {
    const s = fakeStorage()
    for (let i = 0; i < SHELL_HINT_MAX + 12; i++) patchShellHint(`wf-${i}`, { tasks: i + 1 }, { storage: s, now: 1000 + i })
    const kept = Object.keys(JSON.parse(s.raw() ?? '{}'))
    assert.equal(kept.length, SHELL_HINT_MAX)
    assert.equal(readShellHint('wf-0', s), null, '最老的被淘汰')
    assert.equal(readShellHint('wf-11', s), null)
    assert.equal(readShellHint('wf-12', s)?.tasks, 13, '第 12 个起保留')
    assert.equal(readShellHint(`wf-${SHELL_HINT_MAX + 11}`, s)?.tasks, SHELL_HINT_MAX + 12, '最新的在')
  })

  it('存储读写抛错（隐私模式 / 配额满）：不抛出，当作没有记忆', () => {
    const broken = {
      getItem: () => {
        throw new Error('denied')
      },
      setItem: () => {
        throw new Error('quota')
      },
    }
    assert.equal(readShellHint('wf-a', broken), null)
    assert.doesNotThrow(() => patchShellHint('wf-a', { tasks: 3 }, { storage: broken, now: 1 }))
    const writeOnlyBroken = { getItem: () => null, setItem: broken.setItem }
    assert.doesNotThrow(() => patchShellHint('wf-a', { tasks: 3 }, { storage: writeOnlyBroken, now: 1 }))
    assert.doesNotThrow(() => patchShellHint('wf-a', { tasks: 3 }, { storage: null, now: 1 }))
  })
})

describe('外壳件占位记忆（#246）：「项目任务」一行', () => {
  const base = { workflowId: 'wf-a', loaded: false, error: '', total: 0, hinted: 0 }

  it('没有工作流：什么都不画', () => {
    assert.equal(stripMode({ ...base, workflowId: '', hinted: 5 }), 'hidden')
    assert.equal(stripMode({ ...base, workflowId: '', loaded: true, total: 5 }), 'hidden')
  })

  it('任务还在路上：上次有任务才占位，上次没有（或没来过）不留空带', () => {
    assert.equal(stripMode({ ...base, hinted: 8 }), 'reserved')
    assert.equal(stripMode({ ...base, hinted: 0 }), 'hidden')
  })

  it('取任务出了错：占位收起，不让骨架一直挂着', () => {
    assert.equal(stripMode({ ...base, hinted: 8, error: '无法加载这个项目的任务' }), 'hidden')
  })

  it('任务取到了：以真数据为准，与记忆无关（记忆过期时占位收起）', () => {
    assert.equal(stripMode({ ...base, loaded: true, total: 3, hinted: 0 }), 'ready')
    assert.equal(stripMode({ ...base, loaded: true, total: 3, hinted: 9 }), 'ready')
    assert.equal(stripMode({ ...base, loaded: true, total: 0, hinted: 9 }), 'hidden')
  })
})

describe('外壳件占位记忆（#246）：Agent 状态芯片', () => {
  const hint = { chip: { w: 152, wide: true }, at: 1 }
  const base = { hint, wide: true, hasSession: false, overviewLoaded: false, overviewFailed: false, supported: true }

  it('有记忆、同一断点、概览还没取到、芯片还没出现：按记忆的宽度占位', () => {
    assert.equal(chipReservation(base), 152)
  })

  it('没有记忆、记忆里没有芯片：不占位', () => {
    assert.equal(chipReservation({ ...base, hint: null }), 0)
    assert.equal(chipReservation({ ...base, hint: { tasks: 3, at: 1 } }), 0)
  })

  it('断点不同不借用宽度（窄屏芯片更短）', () => {
    assert.equal(chipReservation({ ...base, wide: false }), 0)
    assert.equal(chipReservation({ ...base, hint: { chip: { w: 90, wide: false }, at: 1 }, wide: false }), 90)
  })

  it('芯片已经出现 / 概览已经取到（取到而没有会话 = 现在没有芯片）/ 概览取失败 / 服务端不支持：不占位', () => {
    assert.equal(chipReservation({ ...base, hasSession: true }), 0)
    assert.equal(chipReservation({ ...base, overviewLoaded: true }), 0)
    assert.equal(chipReservation({ ...base, overviewFailed: true }), 0)
    assert.equal(chipReservation({ ...base, supported: false }), 0)
  })
})

describe('外壳件占位记忆（#246 第二版）：会话相关芯片整体的宽度', () => {
  it('lead 与 tasks / chip 各写各的，互不覆盖；宽度取整；断点标记跟着记', () => {
    const s = fakeStorage()
    patchShellHint('wf-a', { tasks: 8, chip: { w: 74, wide: true } }, { storage: s, now: 100 })
    patchShellHint('wf-a', { lead: { w: 671.4, wide: true } }, { storage: s, now: 200 })
    assert.deepEqual(readShellHint('wf-a', s), { tasks: 8, chip: { w: 74, wide: true }, lead: { w: 671, wide: true }, at: 200 })
    patchShellHint('wf-a', { tasks: 9 }, { storage: s, now: 300 })
    assert.deepEqual(readShellHint('wf-a', s)?.lead, { w: 671, wide: true }, '改任务数不动 lead')
    patchShellHint('wf-a', { lead: null }, { storage: s, now: 400 })
    assert.deepEqual(readShellHint('wf-a', s), { tasks: 9, chip: { w: 74, wide: true }, at: 400 })
  })

  it('只有 lead 的工作流也算有记忆；忘掉最后一项时整条删掉', () => {
    const s = fakeStorage()
    patchShellHint('wf-a', { lead: { w: 500, wide: false } }, { storage: s, now: 1 })
    assert.deepEqual(readShellHint('wf-a', s), { lead: { w: 500, wide: false }, at: 1 })
    patchShellHint('wf-a', { lead: null }, { storage: s, now: 2 })
    assert.equal(readShellHint('wf-a', s), null)
    assert.equal(s.raw(), '{}')
  })

  it('内容没变不写存储（量宽的回调每次都会来改写）', () => {
    const s = fakeStorage()
    patchShellHint('wf-a', { lead: { w: 671, wide: true } }, { storage: s, now: 1 })
    patchShellHint('wf-a', { lead: { w: 671.3, wide: true } }, { storage: s, now: 2 })
    assert.equal(s.writes, 1, '取整后相同不写')
    patchShellHint('wf-a', { lead: { w: 671, wide: false } }, { storage: s, now: 3 })
    assert.equal(s.writes, 2, '断点标记变了要写')
    patchShellHint('wf-a', { lead: { w: 700, wide: false } }, { storage: s, now: 4 })
    assert.equal(s.writes, 3)
  })

  it('宽度超出合理范围当脏数据：读时丢掉、写时不记也不抹掉原来合理的', () => {
    const s = fakeStorage(
      JSON.stringify({
        ok: { lead: { w: 600, wide: true }, at: 5 },
        tiny: { lead: { w: 5, wide: true }, at: 1 },
        huge: { lead: { w: 99999, wide: true }, at: 1 },
        noFlag: { lead: { w: 600 }, at: 1 },
      }),
    )
    assert.deepEqual(readShellHint('ok', s), { lead: { w: 600, wide: true }, at: 5 })
    for (const k of ['tiny', 'huge', 'noFlag']) assert.equal(readShellHint(k, s), null, k)
    patchShellHint('ok', { lead: { w: 5, wide: true } }, { storage: s, now: 9 })
    assert.equal(readShellHint('ok', s)?.lead, undefined, '不合理的宽度按忘掉处理')
  })

  it('leadReservation：有记忆、同一断点、还没到齐才预留；到齐之后放开', () => {
    const hint = { lead: { w: 671, wide: true }, at: 1 }
    const base = { hint, wide: true, settled: false }
    assert.equal(leadReservation(base), 671)
    assert.equal(leadReservation({ ...base, settled: true }), 0, '到齐之后让真实宽度说了算')
    assert.equal(leadReservation({ ...base, wide: false }), 0, '断点不同不借用（窄屏是不换行的横滑条）')
    assert.equal(leadReservation({ ...base, hint: null }), 0)
    assert.equal(leadReservation({ ...base, hint: { tasks: 3, at: 1 } }), 0, '记忆里没有 lead')
    assert.equal(leadReservation({ ...base, hint: { lead: { w: 480, wide: false }, at: 1 }, wide: false }), 480)
  })

  it('leadWidthOf：最左左缘到最右右缘；宽度为 0 的节点不算；没有芯片是 0', () => {
    assert.equal(leadWidthOf([{ left: 344, right: 418 }, { left: 424, right: 583.6 }, { left: 589.7, right: 727.7 }, { left: 873.7, right: 1015.3 }]), 671)
    assert.equal(leadWidthOf([{ left: 0, right: 0 }, { left: 344, right: 418 }, { left: 0, right: 0 }]), 74, '没有渲染内容的占位节点（宽度 0，位置在原点）不能把整体拉到原点')
    assert.equal(leadWidthOf([]), 0)
    assert.equal(leadWidthOf([{ left: 10, right: 10 }]), 0)
  })

  it(`到齐的等待时间在合理范围（${LEAD_SETTLE_MS}ms）：比生产上最晚的芯片（解析后约 0.5 秒）长，又不至于让过期的预留挂太久`, () => {
    assert.ok(LEAD_SETTLE_MS >= 1000 && LEAD_SETTLE_MS <= 3000)
  })
})

describe('外壳件占位记忆（#246 第二版）：侧栏「群聊」列表的行数', () => {
  const raw = (s: ReturnType<typeof fakeStorage>) => s.getItem(SIDEBAR_GROUP_ROWS_KEY)

  it('没有记忆 / 坏数据 / 超出范围 / 存储不可用：都读成 0（不占位）', () => {
    assert.equal(readSidebarGroupRows(fakeStorage()), 0)
    assert.equal(readSidebarGroupRows(null), 0)
    for (const bad of ['', 'abc', '-1', '2.5', '0', String(SIDEBAR_GROUP_ROWS_MAX + 1), '99999']) {
      const s = fakeStorage()
      s.setItem(SIDEBAR_GROUP_ROWS_KEY, bad)
      assert.equal(readSidebarGroupRows(s), 0, `坏数据 ${JSON.stringify(bad)}`)
    }
    const broken = {
      getItem: () => {
        throw new Error('denied')
      },
      setItem: () => {
        throw new Error('quota')
      },
    }
    assert.equal(readSidebarGroupRows(broken), 0)
    assert.doesNotThrow(() => writeSidebarGroupRows(3, broken))
  })

  it('写下去能读回来；超出上限按上限取；非整数 / 负数当没有；没有变化不写存储', () => {
    const s = fakeStorage()
    writeSidebarGroupRows(4, s)
    assert.equal(readSidebarGroupRows(s), 4)
    assert.equal(s.writes, 1)
    writeSidebarGroupRows(4, s)
    assert.equal(s.writes, 1, '没有变化不写')
    writeSidebarGroupRows(99, s)
    assert.equal(readSidebarGroupRows(s), SIDEBAR_GROUP_ROWS_MAX)
    writeSidebarGroupRows(Number.NaN, s)
    assert.equal(readSidebarGroupRows(s), 0, '非法值按没有群处理')
    assert.equal(raw(s), '0')
    writeSidebarGroupRows(-3, s)
    assert.equal(s.writes, 3, '已经是 0 再写 0 不落盘')
    // 本来就没有记忆，又要记 0：不写
    const fresh = fakeStorage()
    writeSidebarGroupRows(0, fresh)
    assert.equal(fresh.writes, 0)
  })

  it('与按工作流的记忆互不干扰（不同的存储键）', () => {
    assert.notEqual(SIDEBAR_GROUP_ROWS_KEY, SHELL_HINT_KEY)
    const s = fakeStorage()
    patchShellHint('wf-a', { tasks: 3 }, { storage: s, now: 1 })
    writeSidebarGroupRows(2, s)
    assert.equal(readShellHint('wf-a', s)?.tasks, 3)
    assert.equal(readSidebarGroupRows(s), 2)
  })
})

describe('外壳件占位记忆（#246 第二版）：占位的键', () => {
  it('当前工作流优先；没有就用深链指向的 wf；都没有是空串', () => {
    assert.equal(shellWorkflowKey('wf-a', ''), 'wf-a')
    assert.equal(shellWorkflowKey('', 'wf-target'), 'wf-target', '深链接冷打开：目标解析出来之前当前工作流是空的')
    assert.equal(shellWorkflowKey('wf-a', 'wf-target'), 'wf-a')
    assert.equal(shellWorkflowKey('', ''), '')
  })
})

describe('外壳件占位记忆（#246）：接线（组件不进单测，钉住关键点）', () => {
  const strip = read('components/feedback/WorkflowTaskStrip.vue')
  const chip = read('components/feedback/AgentStatusChip.vue')
  const dock = read('components/feedback/FeedbackInputDock.vue')
  const pending = read('composables/usePendingDeepLinkWf.ts')

  it('「项目任务」一行：占位与真内容共用同一组盒子样式；占位不抢测试标识、对读屏隐藏；取到任务后改写记忆', () => {
    assert.ok(strip.includes('stripMode('), '显示与否走 stripMode')
    assert.ok(strip.includes('data-testid="workflow-task-strip-reserved"'), '占位有自己的测试标识（不与真内容的 workflow-task-strip 混）')
    assert.ok(/data-testid="workflow-task-strip-reserved"[\s\S]{0,400}aria-hidden="true"|aria-hidden="true"[\s\S]{0,400}data-testid="workflow-task-strip-reserved"/.test(strip), '占位对读屏隐藏')
    assert.ok(strip.includes('patchShellHint('), '任务取到后要把有无任务记下来')
    const tpl = strip.slice(strip.indexOf('<template>'))
    assert.ok(/:class="\[boxClass, activeClass\]"/.test(tpl) && /:class="boxClass"/.test(tpl), '占位与真内容都用 boxClass，盒子样式一致高度才一致')
  })

  it('Agent 状态芯片：占位走 chipReservation、对读屏隐藏；量到宽度后记下，取到概览而没有会话就忘掉', () => {
    assert.ok(chip.includes('chipReservation('), '占位宽度走 chipReservation')
    assert.ok(chip.includes('data-testid="agent-status-chip-reserved"'), '占位有自己的测试标识')
    assert.ok(/data-testid="agent-status-chip-reserved"[\s\S]{0,500}aria-hidden="true"|aria-hidden="true"[\s\S]{0,500}data-testid="agent-status-chip-reserved"/.test(chip), '占位对读屏隐藏')
    assert.ok(chip.includes('patchShellHint('), '要把芯片宽度记下')
    assert.ok(chip.includes('chip: null'), '概览取到而这个工作流没有会话时，要忘掉旧的宽度')
  })

  it('深链接冷打开：「项目任务」一行的键走 shellWorkflowKey(当前工作流, 深链 wf)，「取到了没有」只认 feed 确实是这个键的那一份', () => {
    assert.ok(strip.includes('usePendingDeepLinkWf()'), '深链处理完之前要拿地址上的目标 wf')
    assert.ok(/shellWorkflowKey\(\s*store\.feed\.workflowId,\s*pendingWf\.value\s*\)/.test(strip), '键 = 当前工作流 || 深链 wf')
    assert.ok(strip.includes('feedIsKey'), '占位会话的清单不能顶替目标的')
    assert.ok(/loaded:\s*feedIsKey\.value && store\.feed\.loaded/.test(strip), 'loaded 要先确认 feed 是这个键的')
    assert.ok(/readShellHint\(hintKey\.value\)/.test(strip), '记忆按键读（不再直接按 store.feed.workflowId 读）')
    assert.ok(/patchShellHint\(workflowId, \{ tasks: n \}\)/.test(strip), '记忆仍按真实的 feed 工作流写')
  })

  it('侧栏「群聊」：群列表没回来之前按上次的行数画同样高的骨架行（对读屏隐藏）；取到后记下行数；取失败不占位', () => {
    const side = read('components/sidebar/SidebarGroups.vue')
    assert.ok(/readSidebarGroupRows\(\)/.test(side), '挂载时读上次的行数')
    assert.ok(/groups\.loaded \|\| groups\.error \? 0 : rowsHint/.test(side), '取到了 / 取失败都不占位')
    assert.ok(/writeSidebarGroupRows\(n\)/.test(side) && /if \(loaded\)/.test(side), '取到之后记下这次的行数')
    assert.ok(/v-if="reservedRows > 0 && !shown\.length"[\s\S]{0,200}aria-hidden="true"|aria-hidden="true"[\s\S]{0,200}data-testid="sidebar-groups-reserved"/.test(side), '骨架对读屏隐藏，真行出来它就让位')
    // 骨架行与真行用同一组盒子样式（py-1、12px 字），行高才一致
    const rowClass = 'flex w-full items-center gap-2 rounded-sm px-2.5 py-1 text-left text-[12px]'
    assert.ok(side.includes(`class="${rowClass}"`), '骨架行的盒子样式与真行一致')
    assert.ok(side.includes('flex w-full cursor-pointer items-center gap-2 rounded-sm px-2.5 py-1 text-left text-[12px]'), '真行的盒子样式（改它要同步改骨架）')
  })

  it('usePendingDeepLinkWf：只在 isDeepLinkPending 时给出 wf（深链处理完、没有深链都是空串）', () => {
    assert.ok(pending.includes('isDeepLinkPending(route.name, route.query)'))
    assert.ok(pending.includes('deepLinkTarget(route.query).wf'))
  })

  it('输入区状态条：会话相关芯片放进带最小宽度的容器；骨架对读屏隐藏；预留宽度夹在状态条所在行的宽度内（不能用百分比）', () => {
    assert.ok(dock.includes('data-testid="quick-lead"'), '容器有自己的测试标识')
    assert.ok(/v-if="currentWorkflowId \|\| leadReserved > 0"[\s\S]{0,200}ref="leadEl"/.test(dock), '没有会话也没有要预留的宽度时容器不渲染（空容器也占一格 gap）')
    assert.ok(/:style="leadMinWidth > 0 \? \{ minWidth: `\$\{leadMinWidth\}px` \} : undefined"/.test(dock), '最小宽度走 leadMinWidth')
    assert.ok(/quickRowWidth\.value > 0 \? Math\.min\(n, quickRowWidth\.value\) : n/.test(dock), '预留宽度不超过状态条所在行的宽度，窄窗口不撑破输入区')
    assert.ok(/quickRowWidth\.value = Math\.floor\(containerWidth\)/.test(dock), '行宽由量行宽的 ResizeObserver 写入')
    // 状态条自己是按内容收缩的：min(Npx, 100%) 的 100% 会对着收缩后的宽度解析，占位阶段与最终折行不同（第 4 步量到过）
    assert.ok(!/minWidth: `min\(/.test(dock), '不能写成 min(Npx, 100%)')
    assert.ok(/h-\[26px\] w-full[^"]*"[\s\S]{0,200}data-testid="quick-lead-reserved"/.test(dock), '骨架与芯片一样高（26px），状态条这一行的高才不变')
    assert.ok(/data-testid="quick-lead-reserved"/.test(dock), '骨架有自己的测试标识')
    assert.ok(/aria-hidden="true"[\s\S]{0,120}data-testid="quick-lead-reserved"|data-testid="quick-lead-reserved"[\s\S]{0,120}aria-hidden="true"/.test(dock), '骨架对读屏隐藏')
    assert.ok(/v-if="!currentWorkflowId && leadReserved > 0"/.test(dock), '芯片一挂载骨架就让位（否则骨架会把芯片推右）')
    // 容器必须把全部会话相关芯片包进去（Agent 状态芯片起，到常驻规则芯片止），预设按钮在容器外
    const open = dock.indexOf('data-testid="quick-lead"')
    const agent = dock.indexOf('<AgentStatusChip v-if="currentWorkflowId"')
    const resident = dock.indexOf('<ResidentPromptChip v-if="currentWorkflowId"')
    const pills = dock.indexOf('v-for="preset in activeQuickPresets"')
    assert.ok(open > 0 && open < agent && agent < resident && resident < pills, '容器 → 全部芯片 → 容器结束 → 预设按钮')
    assert.ok(/<ResidentPromptChip v-if="currentWorkflowId"[^>]*\/>\s*<\/div>/.test(dock), '容器在常驻规则芯片之后立刻结束，预设按钮不在容器里')
  })

  it('输入区状态条：手机宽度下容器沿用条的版式（不换行、不收缩、子项不收缩），并且不被 max-w-full 卡在条的可视宽度里', () => {
    // 条是横向滑动的，芯片总宽超出条宽是常态；容器若被 max-w-full 卡在可视宽度里，溢出的芯片会画到后面的预设按钮上面
    // （第二版第一稿出过：session-prompts / wake-prompts 的手机宽度那一步红，shell-shift 第 6 步量到容器右边缘 388、最右芯片右边缘 653）
    const m = dock.match(/<div\s+v-if="currentWorkflowId \|\| leadReserved > 0"[\s\S]{0,200}?class="([^"]+)"/)
    assert.ok(m, '找到容器的 class')
    const cls = m![1].split(/\s+/)
    for (const c of ['max-md:max-w-none', 'max-md:flex-nowrap', 'max-md:shrink-0', 'max-md:[&>*]:shrink-0']) {
      assert.ok(cls.includes(c), `容器的 class 里要有 ${c}`)
    }
    assert.ok(cls.includes('max-w-full') && cls.includes('flex-wrap'), '宽屏下仍是限宽、可折行（预设按钮要留在同一排，折行由容器自己承担）')
  })

  it('输入区状态条：预留走 leadReservation；到齐后才量、量到整体宽度记下；换工作流重新计时；占位的键在深链处理完之前取目标 wf', () => {
    assert.ok(/leadReservation\(\{ hint: leadHint\.value, wide: !isPhone\.value, settled: leadSettled\.value \}\)/.test(dock))
    assert.ok(/shellWorkflowKey\(pendingDeepLinkWf\.value \? '' : currentWorkflowId\.value, pendingDeepLinkWf\.value\)/.test(dock), '深链处理完之前键是目标 wf，之后是当前工作流')
    assert.ok(/setTimeout\([\s\S]{0,120}leadSettled\.value = true[\s\S]{0,80}LEAD_SETTLE_MS/.test(dock), '到齐用 LEAD_SETTLE_MS 计时')
    assert.ok(/!el \|\| !wf \|\| pendingDeepLinkWf\.value \|\| !leadSettled\.value \|\| el\.offsetHeight > 34/.test(dock), '没到齐 / 深链未处理完 / 折成多行时不量')
    assert.ok(/patchShellHint\(wf, \{ lead: w > 0 \? \{ w, wide: !isPhone\.value \} : null \}\)/.test(dock), '量到的整体宽度记下，量不到就忘掉')
    assert.ok(dock.includes('leadWidthOf('), '整体宽度走 leadWidthOf（宽度 0 的节点不算）')
    assert.ok(dock.includes('onBeforeUnmount(clearLeadTimers)'), '卸载时清掉计时器')
  })
})
