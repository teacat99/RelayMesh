// 运行：pnpm --dir frontend run test:unit
// 分支「任务派单」（#236 P5）界面接线的源码级防线：纯函数的行为在 branch.test.ts 里测，
// 这里只确认 .vue 组件、仓库与接口层确实用上了它们（单测不能挂载 Vue 组件，与 branchStatus.test.ts 同一做法），防止以后改版时悄悄丢掉：
//   · 「任务」开关走 setExpose → saveSettings，保存没成功时复选框回到真实取值；打开派单不放宽授权，所以不标锁、不要解锁；
//   · 派单状态的文字全部来自 branchAssignStatus，组件里不自己再写一份；
//   · 结束分支的提示里带上退回主会话的任务（来自接口返回的 returned_tasks）；
//   · 接口层字段与 Go 的 JSON 标签一致。
import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, it } from 'node:test'
import type { BranchExpose } from '../api/branches'
import { widensExpose } from './branch'

/** 单测是编译到缓存目录里跑的，不能用 import.meta.url 找源码；从当前目录往 frontend/src 找。 */
function srcDir(): string {
  for (const base of [join(process.cwd(), 'src'), join(process.cwd(), 'frontend', 'src')]) {
    if (existsSync(join(base, 'lib', 'branchAssign.test.ts'))) return base
  }
  throw new Error('找不到 frontend/src，请在仓库根目录或 frontend 目录下运行测试')
}
const read = (rel: string) => readFileSync(join(srcDir(), rel), 'utf8')

/** 取出 `function name(` / `async function name(` 开头、到下一个顶层 `function` / `async function` / `const x =` / 注释块之前的源码。 */
function fnBody(src: string, name: string): string {
  const m = new RegExp(`(?:async )?function ${name}\\(`).exec(src)
  assert.ok(m, `找不到函数 ${name}`)
  const rest = src.slice(m.index + m[0].length)
  const next = /\n(?:async )?function \w+\(|\nconst \w+ = |\n\/\*\*|\n\/\/ -{3,}/.exec(rest)
  return rest.slice(0, next ? next.index : rest.length)
}

const READ_ONLY: BranchExpose = { history: 'full', decide: true, say: true, params: 'resident', launch: false, tasks: 'read' }

describe('分支任务派单的界面接线（#236 P5）', () => {
  it('打开「任务」不算放宽授权：widensExpose 不看 tasks（不要终端解锁），关掉更不要', () => {
    assert.equal(widensExpose(READ_ONLY, { ...READ_ONLY, tasks: 'assign' }), false)
    assert.equal(widensExpose({ ...READ_ONLY, tasks: 'assign' }, READ_ONLY), false)
    // 对照：改参数调高、启动从关到开仍是放宽
    assert.equal(widensExpose(READ_ONLY, { ...READ_ONLY, params: 'all' }), true)
    assert.equal(widensExpose(READ_ONLY, { ...READ_ONLY, launch: true }), true)
  })

  it('分支面板：「任务」开关走 setExpose（于是走 saveSettings），保存没成功时复选框回到真实取值；不标锁', () => {
    const panel = read('components/branch/BranchPanel.vue')
    const toggle = fnBody(panel, 'toggleAssign')
    assert.match(toggle, /await setExpose\(b, \{ tasks: box\.checked \? 'assign' : 'read' \}\)/)
    assert.match(toggle, /box\.checked = branchStore\.asBranch\(props\.workflowId\)\?\.expose\.tasks === 'assign'/)
    assert.doesNotMatch(toggle, /updateSettings\(/, '开关不能绕开 saveSettings 直接改')
    assert.equal((panel.match(/branchStore\.updateSettings\(/g) ?? []).length, 1, '面板里仍只有 saveSettings 一处调用 updateSettings')
    assert.match(panel, /:checked="asBranch\.expose\.tasks === 'assign'"[\s\S]{0,120}data-testid="branch-panel-assign"[\s\S]{0,80}@change="toggleAssign\(asBranch, \$event\)"/)
    assert.doesNotMatch(panel, /branch-panel-assign-lock/, '打开派单不要再验证密码，不该标小锁')
    assert.match(panel, /不需要再验证密码/, '开关的说明里把「不要密码」说在前头')
  })

  it('分支面板：派单状态的文字来自 lib/branch（组件里不自己写第二份），主会话行与分支视角都有', () => {
    const panel = read('components/branch/BranchPanel.vue')
    assert.match(panel, /\{\{ assignOf\(b\)\.label \}\}/)
    assert.match(panel, /:title="assignOf\(b\)\.hint"/)
    assert.match(panel, /:data-testid="`branch-panel-assign-chip-\$\{b\.branch_workflow_id\}`"/)
    assert.match(panel, /\{\{ ownAssign\.label \}\}/)
    assert.match(panel, /\{\{ ownAssign\.hint \}\}/)
    assert.match(panel, /data-testid="branch-panel-assign-block"/)
    assert.match(panel, /data-testid="branch-panel-assign-status"/)
    assert.match(panel, /data-testid="branch-panel-assign-hint"/)
    assert.match(fnBody(panel, 'assignOf'), /branchAssignStatus\(b\)/)
    assert.match(panel, /branchAssignStatus\(asBranch\.value\)/)
    for (const literal of ['可派单', '不同板', '不可派单', '派单状态未知', '不会叫醒分支的智能体']) {
      assert.ok(!panel.includes(literal), `派单状态的文字「${literal}」应只在 lib/branch.ts 里，组件不自己再写一份`)
    }
    // 已结束的分支不画派单状态（关系已结束，没有可说的）
    assert.match(panel, /const ownAssign = computed\(\(\) => \(asBranch\.value && asBranch\.value\.status === 'active' \? branchAssignStatus/)
  })

  it('分支面板：结束分支的提示带上退回主会话的任务，序号来自接口返回', () => {
    const panel = read('components/branch/BranchPanel.vue')
    const end = fnBody(panel, 'endBranch')
    assert.match(end, /const \{ returnedTasks \} = await branchStore\.closeBranch\(b\.branch_workflow_id\)/)
    assert.match(end, /returnedTasksText\(returnedTasks\)/)
    const store = read('stores/branches.ts')
    assert.match(fnBody(store, 'closeBranch'), /return \{ branch: res\.branch, returnedTasks: res\.returnedTasks \}/)
    const api = read('api/branches.ts')
    assert.match(api, /returnedTasks: \(res\.data\.returned_tasks \?\? \[\]\) as number\[\]/)
  })

  it('接口层：BranchBrief 带 assign，字段与服务端一致', () => {
    const api = read('api/branches.ts')
    assert.match(api, /assign\?: BranchAssign/)
    const assign = /export interface BranchAssign \{([\s\S]*?)\n\}/.exec(api)
    assert.ok(assign, '找不到 BranchAssign')
    for (const field of ['open: boolean', 'board_shared: boolean']) assert.ok(assign[1].includes(field), `BranchAssign 缺 ${field}`)
    assert.doesNotMatch(assign[1], /how/, '「下一步怎么做」的文字在前端 lib 里，不走接口')
  })

  it('接口层字段与 Go 的 JSON 标签一致（读 Go 源码核对，防两边悄悄漂移）', () => {
    const roots = [join(process.cwd(), 'internal', 'store', 'branch_status.go'), join(process.cwd(), '..', 'internal', 'store', 'branch_status.go')]
    const file = roots.find(p => existsSync(p))
    if (!file) return // 从别的目录跑（读不到 Go 源码）时跳过，其余用例照常
    const go = readFileSync(file, 'utf8')
    for (const tag of ['open', 'board_shared']) {
      assert.ok(go.includes(`json:"${tag}"`), `Go 的 branch_status.go 里应有 json:"${tag}"`)
    }
    const branches = readFileSync(file.replace('branch_status.go', 'branches.go'), 'utf8')
    assert.ok(branches.includes('json:"assign,omitempty"') || go.includes('json:"assign,omitempty"'), 'Go 的 BranchBrief 里应有 json:"assign,omitempty"')
    assert.ok(branches.includes('json:"returned_tasks,omitempty"'), 'Go 的 CloseBranchResult 里应有 json:"returned_tasks,omitempty"')
  })
})
