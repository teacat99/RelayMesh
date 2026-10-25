// 运行：pnpm --dir frontend run test:unit
// 分支「状态与启动」（#236 P4）界面接线的源码级防线：纯函数的行为在 branch.test.ts 里测，
// 这里只确认 .vue 组件与接口层确实用上了它们（单测不能挂载 Vue 组件，与 branchParams.test.ts 同一做法），防止以后改版时悄悄丢掉：
//   · 「启动」开关走 setExpose → saveSettings（打开要解锁），保存没成功时复选框回到真实取值；
//   · 状态点用状态 store 的实时数据，不读 BranchBrief.agent_state 快照；已结束的分支不画状态；
//   · 启动方式的文字全部来自 branchLaunchStatus，组件里不自己再写一份；
//   · lib/branch.ts 保持纯函数（不 import 仓库 / 组件）。
import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, it } from 'node:test'

/** 单测是编译到缓存目录里跑的，不能用 import.meta.url 找源码；从当前目录往 frontend/src 找。 */
function srcDir(): string {
  for (const base of [join(process.cwd(), 'src'), join(process.cwd(), 'frontend', 'src')]) {
    if (existsSync(join(base, 'lib', 'branchStatus.test.ts'))) return base
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

describe('分支状态与启动的界面接线（#236 P4）', () => {
  it('接口层：BranchBrief 带 agent_state / agent_state_at / tasks / launch，launch 的字段与服务端一致', () => {
    const api = read('api/branches.ts')
    assert.match(api, /agent_state\?: string/)
    assert.match(api, /agent_state_at\?: string/)
    assert.match(api, /tasks\?: BranchTasks/)
    assert.match(api, /launch\?: BranchLaunch/)
    const launch = /export interface BranchLaunch \{([\s\S]*?)\n\}/.exec(api)
    assert.ok(launch, '找不到 BranchLaunch')
    for (const field of ['open: boolean', 'presets: number', 'preset?: string', 'name?: string']) assert.ok(launch[1].includes(field), `BranchLaunch 缺 ${field}`)
    assert.doesNotMatch(launch[1], /can_run/, '此刻能不能运行不在面板数据里（现查的事交给 preset_run list）')
    const tasks = /export interface BranchTasks \{([\s\S]*?)\n\}/.exec(api)
    assert.ok(tasks, '找不到 BranchTasks')
    for (const field of ['todo?: number', 'doing?: number', 'review?: number']) assert.ok(tasks[1].includes(field), `BranchTasks 缺 ${field}`)
  })

  it('接口层字段与 Go 的 JSON 标签一致（读 Go 源码核对，防两边悄悄漂移）', () => {
    const roots = [join(process.cwd(), 'internal', 'store', 'branch_status.go'), join(process.cwd(), '..', 'internal', 'store', 'branch_status.go')]
    const file = roots.find(p => existsSync(p))
    if (!file) return // 从别的目录跑（读不到 Go 源码）时跳过，其余用例照常
    const go = readFileSync(file, 'utf8')
    for (const tag of ['todo,omitempty', 'doing,omitempty', 'review,omitempty', 'open', 'presets', 'preset,omitempty', 'name,omitempty']) {
      assert.ok(go.includes(`json:"${tag}"`), `Go 的 branch_status.go 里应有 json:"${tag}"`)
    }
    const branches = readFileSync(file.replace('branch_status.go', 'branches.go'), 'utf8')
    for (const tag of ['agent_state,omitempty', 'agent_state_at,omitempty', 'tasks,omitempty', 'launch,omitempty']) {
      assert.ok(branches.includes(`json:"${tag}"`), `Go 的 BranchBrief 里应有 json:"${tag}"`)
    }
  })

  it('分支面板：「启动」开关走 setExpose（于是包在 unlockOnDemand 里），保存没成功时复选框回到真实取值', () => {
    const panel = read('components/branch/BranchPanel.vue')
    const toggle = fnBody(panel, 'toggleLaunch')
    assert.match(toggle, /await setExpose\(b, \{ launch: box\.checked \}\)/)
    assert.match(toggle, /box\.checked = !!branchStore\.asBranch\(props\.workflowId\)\?\.expose\.launch/)
    assert.doesNotMatch(toggle, /updateSettings\(/, '开关不能绕开 saveSettings 直接改，否则就绕开了解锁')
    assert.equal((panel.match(/branchStore\.updateSettings\(/g) ?? []).length, 1, '面板里仍只有 saveSettings 一处调用 updateSettings')
    assert.match(panel, /data-testid="branch-panel-launch"[\s\S]{0,80}@change="toggleLaunch\(asBranch, \$event\)"/)
    // 打开才要解锁：关着时标小锁，开着时不标
    assert.match(panel, /<Lock v-if="!asBranch\.expose\.launch"[^>]*data-testid="branch-panel-launch-lock"/)
  })

  it('分支面板：状态点用状态 store 的实时数据，不读 agent_state 快照；已结束的分支不画状态', () => {
    const panel = read('components/branch/BranchPanel.vue')
    assert.match(fnBody(panel, 'stateOf'), /stateView\(agentStore\.entryFor\(b\.branch_workflow_id\)\.primary\)/)
    const shown = fnBody(panel, 'stateShown')
    assert.match(shown, /b\.status === 'active'/)
    assert.match(shown, /stateOf\(b\)\.key !== 'unknown'/)
    const code = panel.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '') // 注释里可以提到这个字段，代码里不能读它
    assert.doesNotMatch(code, /\.agent_state\b/, '面板不读服务端的状态快照')
    assert.match(panel, /useAgentStateStore\(\)/)
    // 状态行只给进行中的分支，已结束的行不带
    assert.match(panel, /v-if="b\.status === 'active'"\s+class="mt-1 flex flex-wrap[^"]*"\s+:data-testid="`branch-panel-status-\$\{b\.branch_workflow_id\}`"/)
  })

  it('分支面板：每次打开重取一遍关系（任务数、有没有预设没有自己的实时事件）', () => {
    const panel = read('components/branch/BranchPanel.vue')
    assert.match(panel, /if \(!isOpen\) resetConfirm\(\)\s*\n\s*\/\/[^\n]*\n\s*else void branchStore\.loadRelations\(\)/)
  })

  it('分支面板：任务数与启动芯片的文字来自 lib/branch（组件里不自己写第二份）', () => {
    const panel = read('components/branch/BranchPanel.vue')
    assert.match(panel, /\{\{ openTasksText\(b\.tasks\) \}\}/)
    assert.match(panel, /v-if="openTaskTotal\(b\.tasks\)"/)
    assert.match(panel, /\{\{ launchOf\(b\)\.label \}\}/)
    assert.match(panel, /:title="launchOf\(b\)\.hint"/)
    assert.match(panel, /\{\{ ownLaunch\.label \}\}/)
    assert.match(panel, /\{\{ ownLaunch\.hint \}\}/)
    assert.match(fnBody(panel, 'launchOf'), /branchLaunchStatus\(b\)/)
    for (const literal of ['启动未开', '启动已开', '可启动 ·', '没有可运行的东西']) {
      assert.ok(!panel.includes(literal), `启动方式的文字「${literal}」应只在 lib/branch.ts 里，组件不自己再写一份`)
    }
  })

  it('lib/branch.ts 保持纯函数：只引用接口类型，不 import 仓库、组件或 vue', () => {
    const lib = read('lib/branch.ts')
    const imports = [...lib.matchAll(/^import .* from '([^']+)'/gm)].map(m => m[1])
    assert.deepEqual(imports, ['../api/branches'])
    assert.match(lib, /^import type \{[^}]*BranchTasks[^}]*\} from '\.\.\/api\/branches'/m)
  })
})
