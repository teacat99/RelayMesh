import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, it } from 'node:test'
import { displayRounds, mergeWorkflowRounds, type WorkflowRounds } from './workflowRounds'

/** 单测是编译到缓存目录里跑的，不能用 import.meta.url 找源码；从当前目录往 frontend/src 找。 */
function srcDir(): string {
  for (const base of [join(process.cwd(), 'src'), join(process.cwd(), 'frontend', 'src')]) {
    if (existsSync(join(base, 'lib', 'workflowRounds.test.ts'))) return base
  }
  throw new Error('找不到 frontend/src，请在仓库根目录或 frontend 目录下运行测试')
}
const read = (rel: string) => readFileSync(join(srcDir(), rel), 'utf8')

describe('mergeWorkflowRounds：把会话行里的工作流总轮数并进表（#173）', () => {
  it('按工作流存一份；同一批里同一工作流的各行只取第一行', () => {
    const next = mergeWorkflowRounds({}, [
      { workflow_id: 'wf-a', workflow_rounds: 265 },
      { workflow_id: 'wf-a', workflow_rounds: 265 },
      { workflow_id: 'wf-b', workflow_rounds: 1 },
    ])
    assert.deepEqual(next, { 'wf-a': 265, 'wf-b': 1 })
  })

  it('没有变化返回 null，调用方不必替换响应式对象', () => {
    const prev: WorkflowRounds = { 'wf-a': 3 }
    assert.equal(mergeWorkflowRounds(prev, [{ workflow_id: 'wf-a', workflow_rounds: 3 }]), null)
    assert.equal(mergeWorkflowRounds(prev, []), null)
  })

  it('以服务端为准直接覆盖，可以变小（清理过历史）', () => {
    const prev: WorkflowRounds = { 'wf-a': 10 }
    assert.deepEqual(mergeWorkflowRounds(prev, [{ workflow_id: 'wf-a', workflow_rounds: 4 }]), { 'wf-a': 4 })
    assert.deepEqual(prev, { 'wf-a': 10 }, '不改动传入的旧表')
  })

  it('没带总数 / 没有工作流编号 / 值不合法的行跳过，保留已有的', () => {
    const prev: WorkflowRounds = { 'wf-a': 7 }
    const rows = [
      { workflow_id: 'wf-a' }, // 旧服务端 / 事件补丁出来的行
      { workflow_rounds: 9 }, // 没有工作流编号
      { workflow_id: 'wf-a', workflow_rounds: -1 },
      { workflow_id: 'wf-a', workflow_rounds: Number.NaN },
      { workflow_id: 'wf-c', workflow_rounds: '5' as unknown as number },
    ]
    assert.equal(mergeWorkflowRounds(prev, rows), null)
  })

  it('值向下取整', () => {
    assert.deepEqual(mergeWorkflowRounds({}, [{ workflow_id: 'wf-a', workflow_rounds: 6.9 }]), { 'wf-a': 6 })
  })
})

describe('displayRounds：徽标上写几轮', () => {
  it('总数与列表行数取较大者', () => {
    assert.equal(displayRounds(265, 104), 265, '列表窗口里只有 104 行，实际 265 轮')
    assert.equal(displayRounds(61, 1), 61, '较早的工作流只补了一行，也要写它真实的轮数')
    assert.equal(displayRounds(5, 6), 6, '总数是更早的快照时，不比已经在列表里的行数少')
  })

  it('没有总数时按列表里的行数显示（旧服务端 / 聚合失败 / 没有工作流编号）', () => {
    assert.equal(displayRounds(undefined, 3), 3)
    assert.equal(displayRounds(Number.NaN, 3), 3)
  })
})

// 接线检查（项目里的 store / 视图没有组件级单测，用源码断言守住几处关键接线，免得改着改着丢了）
describe('接线：侧栏与归档库拿到「每个工作流一条」并显示真实轮数（#173）', () => {
  it('会话列表请求带 cover_workflows，返回的行同时并进总轮数表', () => {
    const store = read('stores/session.ts')
    assert.match(store, /sessionsApi\.list\(\{ status, limit: 200, brief: true, cover_workflows: true \}\)/)
    assert.match(store, /absorbWorkflowRounds\(res\.sessions\)/)
    assert.match(store, /absorbWorkflowRounds\(\[res\.session\]\)/)
    assert.match(store, /\n\s+workflowRounds,\n/)
  })

  it('接口类型与请求参数都声明了新字段', () => {
    assert.match(read('api/types.ts'), /workflow_rounds\?: number/)
    assert.match(read('api/client.ts'), /cover_workflows\?: boolean/)
  })

  it('侧栏与归档库的轮数走 displayRounds，不再直接拿行数', () => {
    const sidebar = read('components/AppSidebar.vue')
    assert.match(sidebar, /rounds_count: displayRounds\(sessionStore\.workflowRounds\[key\], group\.length\)/)
    const archive = read('components/ArchiveList.vue')
    assert.match(archive, /rounds: displayRounds\(sessionStore\.workflowRounds\[key\], group\.length\)/)
  })
})
