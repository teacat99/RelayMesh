// 运行：pnpm --dir frontend run test:unit
// 分支「改参数」（#236 P3）界面接线的源码级防线：纯函数的行为在 branch.test.ts / unlockRetry.test.ts 里测，
// 这里只确认 .vue 组件、仓库与接口层确实用上了它们（单测不能挂载 Vue 组件，与 confirmRouting.test.ts 同一做法），防止以后改版时悄悄丢掉：
//   · 放宽授权要解锁：改开放范围、新建协作 / 托管分支都包在 unlockOnDemand 里，取消解锁静默放弃；
//   · 终端不可用时服务端把放宽的项压回，界面要把原因说出来（clampedNotice），不能让用户以为已经放宽；
//   · 撤销只要登录：撤销路径里不能出现解锁；
//   · 解锁令牌只发往约定的精确接口（api/terminal.ts 的拦截器要带上请求方法）。
import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, it } from 'node:test'

/** 单测是编译到缓存目录里跑的，不能用 import.meta.url 找源码；从当前目录往 frontend/src 找。 */
function srcDir(): string {
  for (const base of [join(process.cwd(), 'src'), join(process.cwd(), 'frontend', 'src')]) {
    if (existsSync(join(base, 'lib', 'branchParams.test.ts'))) return base
  }
  throw new Error('找不到 frontend/src，请在仓库根目录或 frontend 目录下运行测试')
}
const read = (rel: string) => readFileSync(join(srcDir(), rel), 'utf8')

/** 取出 `function name(` / `async function name(` 开头、到下一个顶层 `function` / `async function` / `const x =` 之前的源码。 */
function fnBody(src: string, name: string): string {
  const m = new RegExp(`(?:async )?function ${name}\\(`).exec(src)
  assert.ok(m, `找不到函数 ${name}`)
  const rest = src.slice(m.index + m[0].length)
  const next = /\n(?:async )?function \w+\(|\nconst \w+ = /.exec(rest)
  return rest.slice(0, next ? next.index : rest.length)
}

describe('分支改参数的界面接线（#236 P3）', () => {
  it('请求拦截器按「方法 + 路径」带解锁令牌：carriesUnlock 收到 config.method', () => {
    assert.match(read('api/terminal.ts'), /carriesUnlock\(config\.url, config\.method\)/)
  })

  it('接口层：改设置返回 clamped，参数记录与撤销走约定的路径', () => {
    const api = read('api/branches.ts')
    assert.match(api, /async update\(workflowId: string, patch: UpdateBranchRequest\): Promise<UpdateBranchResult>/)
    assert.match(api, /clamped: \(res\.data\.clamped \?\? \[\]\) as BranchClamped\[\]/)
    assert.match(api, /\/branch\/params-events`/)
    assert.match(api, /\/branch\/params-events\/\$\{eventId\}\/revert`/)
    assert.match(read('api/client.ts'), /clamped\?: Array<'params' \| 'launch'>/)
  })

  it('仓库：改设置把结果（含 clamped）原样交给调用方；撤销后触发一次重取，重取完成递增 refreshTick', () => {
    const store = read('stores/branches.ts')
    assert.match(store, /async function updateSettings\(workflowId: string, patch: UpdateBranchRequest\): Promise<UpdateBranchResult>/)
    assert.match(fnBody(store, 'updateSettings'), /return res/)
    const revert = fnBody(store, 'revertParamEvent')
    assert.match(revert, /branchesApi\.revertParamEvent/)
    assert.match(revert, /scheduleRefresh\(\)/)
    assert.match(fnBody(store, 'refreshNow'), /refreshTick\.value\+\+/)
    assert.match(store, /refreshTick,/, 'refreshTick 要对外暴露，面板靠它重取参数记录')
  })

  it('分支面板：放宽包在 unlockOnDemand 里，取消解锁静默，压回的项写明原因', () => {
    const panel = read('components/branch/BranchPanel.vue')
    const save = fnBody(panel, 'saveSettings')
    assert.match(save, /terminalStore\.unlockOnDemand\(\(\) => branchStore\.updateSettings\(b\.branch_workflow_id, patch\), BRANCH_UNLOCK_REASON\)/)
    assert.match(save, /clampedNotice\(res\.clamped\)/)
    assert.match(save, /toast\.warning\(/)
    assert.match(save, /if \(err instanceof UnlockCancelled\) return/)
    // 三个入口（预设、抄送、逐项）都走 saveSettings，不会绕开解锁
    assert.match(fnBody(panel, 'pickPreset'), /saveSettings\(b, 'preset'/)
    assert.match(fnBody(panel, 'toggleCopy'), /saveSettings\(b, 'copy'/)
    assert.match(fnBody(panel, 'setExpose'), /saveSettings\(b, 'expose', \{ expose: \{ \.\.\.b\.expose, \.\.\.change \} \}/)
    assert.equal((panel.match(/branchStore\.updateSettings\(/g) ?? []).length, 1, '面板里只有 saveSettings 一处调用 updateSettings，不会绕开解锁')
  })

  it('分支面板：撤销只要登录（路径里没有解锁），不论成败都重取参数记录', () => {
    const panel = read('components/branch/BranchPanel.vue')
    const revert = fnBody(panel, 'revertEvent')
    assert.match(revert, /branchStore\.revertParamEvent\(b\.branch_workflow_id, ev\.id\)/)
    assert.doesNotMatch(revert, /unlockOnDemand|withUnlock|freshUnlock/, '撤销是收紧类操作，不需要解锁')
    assert.match(revert, /finally \{[\s\S]*void loadEvents\(\)/)
    // 只有 revertable 的记录才有「撤销」按钮；不能撤销的原因展示给用户
    assert.match(panel, /v-if="ev\.revertable"/)
    assert.match(panel, /ev\.revert_blocked && !ev\.reverted_at/)
    // 主会话的改动随实时流刷新：面板监听 refreshTick
    assert.match(panel, /watch\(\s*\(\) => branchStore\.refreshTick/)
  })

  it('分支面板：放宽的档位标小锁、说明解锁规则，分支编号可复制', () => {
    const panel = read('components/branch/BranchPanel.vue')
    assert.match(panel, /levelNeedsUnlock\(asBranch, l\.id\)/)
    assert.match(panel, /widensExpose\(b\.expose, \{ \.\.\.b\.expose, params: level \}\)/)
    assert.match(panel, /data-testid="branch-panel-unlock-note"/)
    assert.match(panel, /data-testid="branch-panel-copy-id"/)
    assert.match(panel, /branch-panel-copy-id-\$\{b\.branch_workflow_id\}/)
  })

  it('新建分支：创建包在 unlockOnDemand 里，取消解锁静默停在表单，压回的项在结果页写明', () => {
    const dlg = read('components/branch/NewBranchDialog.vue')
    assert.match(dlg, /terminalStore\.unlockOnDemand\(\s*\(\) =>\s*sessionStore\.createWorkflow\(/)
    assert.match(dlg, /NEW_BRANCH_UNLOCK_REASON,\s*\)/, '解锁对话框要带上原因，用户才知道为什么要再输一次密码')
    assert.match(dlg, /if \(!\(e instanceof UnlockCancelled\)\) error\.value = /)
    assert.match(dlg, /clampedNotice\(created\.value\?\.clamped\)/)
    assert.match(dlg, /data-testid="new-branch-clamped"/)
    // 仅可见不需要解锁，说明只在协作 / 托管时显示
    assert.match(dlg, /v-if="preset !== 'visible'"[^>]*data-testid="new-branch-unlock-note"/)
  })

  it('消息流事件行：小标签由 branchEventChip 给出，参数事件显示「参数」', () => {
    const card = read('components/feedback/preview-view/PreviewBranchCard.vue')
    assert.match(card, /branchEventChip\(item\.value\)/)
    assert.match(card, /data-testid="branch-event-chip">\{\{ eventChip \}\}/)
  })
})
