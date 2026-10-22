import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, it } from 'node:test'
import {
  RUN_MODE_KEY,
  SIDEBAR_PREF_KEY,
  countVisibleWorkflows,
  effectiveSidebarCollapsed,
  parseRunMode,
  parseSidebarPref,
} from './sidebarDefault'

/** 单测是编译到缓存目录里跑的，不能用 import.meta.url 找源码；从当前目录往 frontend/src 找。 */
function srcDir(): string {
  for (const base of [join(process.cwd(), 'src'), join(process.cwd(), 'frontend', 'src')]) {
    if (existsSync(join(base, 'lib', 'sidebarDefault.test.ts'))) return base
  }
  throw new Error('找不到 frontend/src，请在仓库根目录或 frontend 目录下运行测试')
}
const read = (rel: string) => readFileSync(join(srcDir(), rel), 'utf8')

describe('侧栏默认收起（#190）：手动选择优先', () => {
  it('手动收起 / 手动展开一律照办，不管运行方式与工作流个数', () => {
    for (const runMode of ['serve', 'stdio'] as const) {
      for (const workflowCount of [0, 1, 2, 9]) {
        assert.equal(effectiveSidebarCollapsed({ runMode, pref: true, workflowCount }), true)
        assert.equal(effectiveSidebarCollapsed({ runMode, pref: false, workflowCount }), false)
      }
    }
  })

  it('常驻服务没选过：始终展开，与以前完全一样', () => {
    for (const workflowCount of [0, 1, 2, 30]) {
      assert.equal(effectiveSidebarCollapsed({ runMode: 'serve', pref: null, workflowCount }), false)
    }
  })

  it('stdio 没选过：0 或 1 个工作流收起，第二个出现就展开', () => {
    assert.equal(effectiveSidebarCollapsed({ runMode: 'stdio', pref: null, workflowCount: 0 }), true)
    assert.equal(effectiveSidebarCollapsed({ runMode: 'stdio', pref: null, workflowCount: 1 }), true)
    assert.equal(effectiveSidebarCollapsed({ runMode: 'stdio', pref: null, workflowCount: 2 }), false)
    assert.equal(effectiveSidebarCollapsed({ runMode: 'stdio', pref: null, workflowCount: 5 }), false)
  })
})

describe('侧栏默认收起（#190）：读取与计数', () => {
  it('只认 true / false，其他一律当没选过（旧版本只写这两个值）', () => {
    assert.equal(parseSidebarPref('true'), true)
    assert.equal(parseSidebarPref('false'), false)
    for (const raw of [null, undefined, '', '1', 'yes', 'TRUE', '{}']) assert.equal(parseSidebarPref(raw), null)
  })

  it('运行方式只认 stdio，其余都是 serve', () => {
    assert.equal(parseRunMode('stdio'), 'stdio')
    for (const raw of [null, undefined, '', 'serve', 'STDIO', 'x']) assert.equal(parseRunMode(raw), 'serve')
  })

  it('工作流个数与侧栏一致：按 workflow_id 聚合、没有就按 session_id、已归档的不算', () => {
    assert.equal(countVisibleWorkflows([]), 0)
    assert.equal(
      countVisibleWorkflows([
        { session_id: 's1', workflow_id: 'wf-a', status: 'pending' },
        { session_id: 's2', workflow_id: 'wf-a', status: 'replied' },
      ]),
      1,
    )
    assert.equal(
      countVisibleWorkflows([
        { session_id: 's1', workflow_id: 'wf-a' },
        { session_id: 's2', workflow_id: 'wf-b' },
      ]),
      2,
    )
    assert.equal(countVisibleWorkflows([{ session_id: 's1' }, { session_id: 's2' }]), 2)
    assert.equal(
      countVisibleWorkflows([
        { session_id: 's1', workflow_id: 'wf-a' },
        { session_id: 's2', workflow_id: 'wf-old', status: 'archived' },
      ]),
      1,
    )
  })
})

describe('侧栏默认收起（#190）：接线', () => {
  it('会话页：自动状态不落盘，只有手动切换才写偏好；偏好键沿用旧键', () => {
    assert.equal(SIDEBAR_PREF_KEY, 'relaymesh_sidebar_collapsed')
    const src = read('views/FeedbackView.vue')
    assert.match(src, /effectiveSidebarCollapsed\(/)
    assert.match(src, /countVisibleWorkflows\(/)
    // 写 localStorage 的只有手动切换那一处
    const writes = src.match(/localStorage\.setItem\(SIDEBAR_PREF_KEY/g) || []
    assert.equal(writes.length, 1)
    assert.doesNotMatch(src, /localStorage\.setItem\('relaymesh_sidebar_collapsed'/)
  })

  it('鉴权状态里带运行方式，并缓存最近一次的值', () => {
    assert.equal(RUN_MODE_KEY, 'relaymesh_run_mode')
    const src = read('stores/auth.ts')
    assert.match(src, /run_mode\?: string/)
    assert.match(src, /parseRunMode\(res\.data\.run_mode\)/)
    assert.match(src, /localStorage\.setItem\(RUN_MODE_KEY/)
  })
})
