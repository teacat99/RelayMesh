import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, it } from 'node:test'
import { TASK_MAX_PAGES, TASK_PAGE_SIZE, collectAllPages, type PageOf } from './taskPaging'

/** 单测是编译到缓存目录里跑的，不能用 import.meta.url 找源码；从当前目录往 frontend/src 找。 */
function srcDir(): string {
  for (const base of [join(process.cwd(), 'src'), join(process.cwd(), 'frontend', 'src')]) {
    if (existsSync(join(base, 'lib', 'taskPaging.test.ts'))) return base
  }
  throw new Error('找不到 frontend/src，请在仓库根目录或 frontend 目录下运行测试')
}
const read = (rel: string) => readFileSync(join(srcDir(), rel), 'utf8')

/** 按给定的页序列回答；记录每次收到的游标。 */
function pager(pages: PageOf<string>[]) {
  const cursors: string[] = []
  return {
    cursors,
    fetchPage: async (cursor: string) => {
      cursors.push(cursor)
      return pages[cursors.length - 1] ?? { tasks: [] }
    },
  }
}

describe('托管自驾工单翻页（#250）：collectAllPages', () => {
  it('只有一页：取一次，游标是空串', async () => {
    const p = pager([{ tasks: ['a', 'b'] }])
    assert.deepEqual(await collectAllPages(p.fetchPage), ['a', 'b'])
    assert.deepEqual(p.cursors, [''])
  })

  it('多页：按 next_cursor 一页页取，顺序保持服务端给的顺序', async () => {
    const p = pager([
      { tasks: ['a', 'b'], next_cursor: 'c1' },
      { tasks: ['c'], next_cursor: 'c2' },
      { tasks: ['d', 'e'] },
    ])
    assert.deepEqual(await collectAllPages(p.fetchPage), ['a', 'b', 'c', 'd', 'e'])
    assert.deepEqual(p.cursors, ['', 'c1', 'c2'])
  })

  it('空列表与 tasks 为 null（Go 的空切片可能序列化成 null）都得到空数组', async () => {
    assert.deepEqual(await collectAllPages(pager([{ tasks: [] }]).fetchPage), [])
    assert.deepEqual(await collectAllPages(pager([{ tasks: null }]).fetchPage), [])
    assert.deepEqual(await collectAllPages(pager([{}]).fetchPage), [])
  })

  it('翻页数有上限：服务端一直给 next_cursor 也只取 maxPages 页', async () => {
    let calls = 0
    const out = await collectAllPages(
      async () => {
        calls++
        return { tasks: [`t${calls}`], next_cursor: `c${calls}` }
      },
      { maxPages: 3 },
    )
    assert.equal(calls, 3)
    assert.deepEqual(out, ['t1', 't2', 't3'])
  })

  it('游标原地不动（服务端异常）也停下，不会无限翻', async () => {
    let calls = 0
    const out = await collectAllPages(async () => {
      calls++
      return { tasks: [`t${calls}`], next_cursor: 'same' }
    })
    // 第一页游标是空串 → 给 same；第二页游标 same → 又给 same，停
    assert.equal(calls, 2)
    assert.deepEqual(out, ['t1', 't2'])
  })

  it('取的途中有更新的取数发出：放弃这一轮、不再取下一页', async () => {
    let calls = 0
    let stale = false
    const out = await collectAllPages(
      async () => {
        calls++
        stale = true // 第一页回来的时候，已经有更新的一次取数发出了
        return { tasks: ['a'], next_cursor: 'c1' }
      },
      { isStale: () => stale },
    )
    assert.equal(out, null)
    assert.equal(calls, 1)
  })

  it('取页失败：错误原样抛给调用方（由调用方决定留着旧列表）', async () => {
    await assert.rejects(
      collectAllPages(async () => {
        throw new Error('boom')
      }),
      /boom/,
    )
  })

  it('常量：每页取服务端上限 100，最多 20 页', () => {
    assert.equal(TASK_PAGE_SIZE, 100)
    assert.equal(TASK_MAX_PAGES, 20)
  })
})

describe('托管自驾工单翻页（#250）：任务仓库的接线', () => {
  const store = read('stores/task.ts')

  it('fetchTasks 走 collectAllPages、带上 limit 与 cursor，不再只取第一页', () => {
    assert.ok(/import \{[^}]*TASK_PAGE_SIZE[^}]*collectAllPages[^}]*\} from '..\/lib\/taskPaging'|import \{[^}]*collectAllPages[^}]*TASK_PAGE_SIZE[^}]*\} from '..\/lib\/taskPaging'/.test(store), '引入翻页函数与每页条数')
    assert.ok(/collectAllPages<TaskSummary>\(/.test(store), '走翻页函数')
    assert.ok(/tasksApi\.list\(\{ updates_only: updatesOnly, limit: TASK_PAGE_SIZE, cursor: cursor \|\| undefined \}\)/.test(store), '每页取 TASK_PAGE_SIZE 条、带游标')
    assert.ok(!/tasksApi\.list\(\{ updates_only: updatesOnly \}\)/.test(store), '不能再只取第一页')
  })

  it('旧的取数结果不能覆盖新的：途中检查 reqSeq，被放弃（null）时不写列表', () => {
    assert.ok(/isStale: \(\) => reqSeq !== fetchTasksSeq/.test(store), '翻页途中检查是不是已经有更新的取数')
    assert.ok(/if \(all === null\) return/.test(store), '被放弃时直接返回')
    assert.ok(/tasks\.value = all/.test(store), '取全之后一次写入列表')
  })
})
