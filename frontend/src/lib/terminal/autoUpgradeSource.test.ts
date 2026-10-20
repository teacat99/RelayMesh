// 运行：pnpm --dir frontend run test:unit
// Agent 自动升级（#142 一期）界面的源码级防线——这些是产品承诺，不能被「顺手加个按钮」悄悄破坏：
//   · Web 界面永远不替主机打开自动升级：API 层只有「服务端总闸」这一个相关端点，没有任何按主机写同意的端点；
//   · 服务端总闸的改动要重新验证密码（解锁）：store 里的动作必须经过 withUnlock；
//   · 主机卡片上的标签与展开里的说明只是展示：不是按钮、没有点击处理；
//   · 设置组件只通过 store 的动作改总闸，不直接碰 API。
import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, it } from 'node:test'

function srcDir(): string {
  for (const base of [join(process.cwd(), 'src'), join(process.cwd(), 'frontend', 'src')]) {
    if (existsSync(join(base, 'lib', 'terminal', 'autoUpgradeSource.test.ts'))) return base
  }
  throw new Error('找不到 frontend/src，请在仓库根目录或 frontend 目录下运行测试')
}
const read = (rel: string) => readFileSync(join(srcDir(), rel), 'utf8')

/** 取出以某个 data-testid 标记的那个元素的整段开始标签（到第一个 `>` 为止，属性可能跨行）。 */
function openingTag(source: string, testid: string): string {
  const at = source.indexOf(`data-testid="${testid}"`)
  assert.ok(at >= 0, `找不到 data-testid="${testid}"`)
  const start = source.lastIndexOf('<', at)
  const end = source.indexOf('>', at)
  return source.slice(start, end + 1)
}

describe('自动升级：Web 界面不替主机做决定', () => {
  it('API 层与「自动升级」相关的端点只有服务端总闸一个', () => {
    const api = read('api/terminal.ts')
    const urls = [...api.matchAll(/['"`]([^'"`\n]*auto-upgrade[^'"`\n]*)['"`]/g)].map(m => m[1])
    assert.deepEqual(urls, ['/terminal/prefs/auto-upgrade'], '不应有按主机写自动升级同意的端点：开关只存在主机本地')
  })

  it('总闸的改动经过解锁（重新验证密码）', () => {
    const store = read('stores/terminal.ts')
    const m = store.match(/async function saveAgentAutoUpgrade\([^)]*\)[^{]*\{([\s\S]*?)\n  \}/)
    assert.ok(m, '找不到 saveAgentAutoUpgrade')
    assert.match(m[1], /withUnlock\(/)
    assert.match(m[1], /setAutoUpgradePrefs\(/)
  })

  it('设置组件只通过 store 的动作改总闸，不直接调用 API', () => {
    const pref = read('components/settings/AgentAutoUpgradePref.vue')
    assert.match(pref, /terminal\.saveAgentAutoUpgrade\(/)
    assert.doesNotMatch(pref, /terminalApi/, '组件不应直接调用 terminalApi（解锁在 store 的动作里）')
  })

  it('主机卡片上的标签与展开里的说明只是展示：不是按钮、没有点击处理', () => {
    const view = read('views/exec/ExecHostsView.vue')
    for (const id of ['auto-upgrade-chip', 'auto-upgrade-note']) {
      const tag = openingTag(view, id)
      assert.match(tag, /^<span\b/, `${id} 应是 span，不是按钮`)
      assert.doesNotMatch(tag, /@click|v-on:click|role="button"|tabindex/, `${id} 不应可点击`)
    }
  })

  it('设置页同时讲清提示与静默两种模式，并写明静默的硬性前提（发布签名）与「不降级」', () => {
    const pref = read('components/settings/AgentAutoUpgradePref.vue')
    assert.match(openingTag(pref, 'pref-auto-upgrade-notify'), /^<p\b/)
    assert.match(openingTag(pref, 'pref-auto-upgrade-silent'), /^<p\b/)
    const silent = pref.slice(pref.indexOf('data-testid="pref-auto-upgrade-silent"'))
    assert.match(silent, /没有发布签名的版本一律不安装/)
    assert.match(silent, /只升级、不降级/)
    assert.match(silent, /AUTO_UPGRADE_SILENT_COMMAND/)
    assert.doesNotMatch(pref, /v-html/, '说明文字里不要用 v-html')
  })

  it('纯逻辑文件只被主机页与设置组件引用（别的页面不该各自再解释一遍）', () => {
    for (const rel of ['views/exec/ExecHostsView.vue', 'components/settings/AgentAutoUpgradePref.vue']) {
      assert.match(read(rel), /lib\/terminal\/autoUpgrade'/, `${rel} 应使用 lib/terminal/autoUpgrade`)
    }
  })
})
