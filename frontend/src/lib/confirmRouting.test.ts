// 决策路由（#236 P2）界面接线的源码级防线：纯函数的行为在 confirms.test.ts / branch.test.ts 里测，
// 这里只确认 .vue 组件确实用上了它们（单测不能挂载 Vue 组件，与 statusWords.test.ts 同一做法），防止以后改版时悄悄丢掉。
import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, it } from 'node:test'

/** 单测是编译到缓存目录里跑的，不能用 import.meta.url 找源码；从当前目录往 frontend/src 找。 */
function srcDir(): string {
  for (const base of [join(process.cwd(), 'src'), join(process.cwd(), 'frontend', 'src')]) {
    if (existsSync(join(base, 'lib', 'confirmRouting.test.ts'))) return base
  }
  throw new Error('找不到 frontend/src，请在仓库根目录或 frontend 目录下运行测试')
}
const read = (rel: string) => readFileSync(join(srcDir(), rel), 'utf8')

describe('决策路由的界面接线', () => {
  it('提示条：待主会话 / 被转交两种状态，两个视图的待处理卡都挂着', () => {
    const notice = read('components/confirms/ConfirmRouteNotice.vue')
    assert.match(notice, /routedToMain\(props\.item\)/)
    assert.match(notice, /escalationText\(props\.item\)/)
    assert.match(notice, /\$\{testid\}-routed/)
    assert.match(notice, /\$\{testid\}-escalated/)
    assert.match(read('components/confirms/ConfirmCard.vue'), /<ConfirmRouteNotice[^>]*:item="item"[^>]*:testid="testid"/)
    assert.match(read('components/feedback/preview-view/PreviewConfirmCard.vue'), /<ConfirmRouteNotice[^>]*:item="item"[^>]*:testid="testid"/)
  })

  it('预览的待处理卡：路由给主会话时状态词换成「待主会话」且不脉动', () => {
    const card = read('components/feedback/preview-view/PreviewConfirmCard.vue')
    assert.match(card, /routedToMain\(props\.item\)/)
    assert.match(card, /<EventStatus v-if="routed" tone="violet">\{\{ STATUS_WORD\.routed \}\}<\/EventStatus>/)
    assert.match(card, /<EventStatus v-else :tone="statusTone" pulse>\{\{ STATUS_WORD\.pending \}\}<\/EventStatus>/)
  })

  it('已处理的确认行：主会话代答要写明「主会话代答」与「不是您本人的答复」，转交过的留一句原因', () => {
    const row = read('components/confirms/ConfirmTimelineCard.vue')
    assert.match(row, /answeredByMain\(props\.item\)/)
    assert.match(row, /answerLead\(props\.item\)/)
    assert.match(row, /escalationText\(props\.item\)/)
    assert.match(row, /\$\{item\.ref\}-by-main/)
    assert.match(row, /\$\{item\.ref\}-escalation/)
    assert.match(row, /byMain \? '主会话选了这个' : '你选了这个'/)
    // 状态词汇表的既有防线（statusWords.test.ts）依赖这一句，改写时要一起改
    assert.match(row, /status === 'answered' \? STATUS_WORD\.done/)
  })

  it('设置页：「等待策略」里有分支请示偏好卡，写入走 branchesApi，输入先过 parseEscalateMinutes', () => {
    assert.match(read('components/settings/WaitingSettings.vue'), /<BranchRoutingSettings \/>/)
    const card = read('components/settings/BranchRoutingSettings.vue')
    assert.match(card, /branchesApi\.prefs\(\)/)
    assert.match(card, /branchesApi\.updatePrefs\(patch\)/)
    assert.match(card, /parseEscalateMinutes\(minutesText\.value, p\.min_escalate_minutes, p\.max_escalate_minutes\)/)
    for (const id of ['branch-prefs', 'branch-prefs-minutes', 'branch-prefs-save', 'branch-prefs-reset', 'branch-prefs-l2-guard', 'branch-prefs-summary', 'branch-prefs-error']) {
      assert.ok(card.includes(`data-testid="${id}"`), `缺 ${id}`)
    }
  })

  it('分支事件行：转交 / 代答事件走 branchEventText，卡片自己不再另写文案', () => {
    assert.match(read('components/feedback/preview-view/PreviewBranchCard.vue'), /branchEventText\(item\.value, props\.titles\)/)
  })
})
