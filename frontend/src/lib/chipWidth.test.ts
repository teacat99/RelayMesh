// 运行：pnpm --dir frontend run test:unit
// 守护「页脚快捷条里的芯片不因为文字变化而抖」（#197）。
// 背景：快捷条是一行靠左排的 flex，前面某个芯片的宽度变了，后面所有芯片都会被推着走（窄屏是横滑条，宽屏还可能换行）。
// 在真实会话里量到两处最频繁的来源：Agent 子活动文字（几秒换一次，宽度差最多十几 em）与收件箱芯片的「hook N 秒前」。
// 做法：这两处给文字预留固定宽度；其余芯片的文字按现在的格式宽度本来就不变，这里用断言把「为什么不用预留」钉住，
// 以后谁改了格式让宽度开始变，测试会提醒他补预留。
import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, it } from 'node:test'
import type { InboxConfig, InboxConfigView } from '../api/types'
import { AGENT_STATUS_DETAIL_BOX_EM, subText } from './agentState'
import { INBOX_CHIP_TIMED_TEXT_MIN_EM, inboxChipTextIsTimed, inboxChipView, type InboxChipView } from './inbox'
import { windowView } from './replyWindow'

/** 单测是编译到缓存目录里跑的，不能用 import.meta.url 找源码；从当前目录往 frontend/src 找。 */
function srcDir(): string {
  for (const base of [join(process.cwd(), 'src'), join(process.cwd(), 'frontend', 'src')]) {
    if (existsSync(join(base, 'lib', 'chipWidth.test.ts'))) return base
  }
  throw new Error('找不到 frontend/src，请在仓库根目录或 frontend 目录下运行测试')
}
const read = (rel: string) => readFileSync(join(srcDir(), rel), 'utf8')

/** 等宽字体里的估算宽度（单位 em）：ASCII 约 0.6em，汉字等全角约 1em。 */
function widthEm(text: string): number {
  let w = 0
  for (const ch of text) w += (ch.codePointAt(0) ?? 0) < 0x80 ? 0.6 : 1
  return w
}

const NOW = Date.parse('2026-10-09T12:00:00Z')
const at = (secondsAgo: number) => new Date(NOW - secondsAgo * 1000).toISOString()
const allOn: InboxConfig = { enabled: true, receipt_hint: true, hooks: true, prompt_clause: true }
const cfgView = (over: Partial<InboxConfig> = {}, envOff = false): InboxConfigView => {
  const saved = { ...allOn, ...over }
  return { ...saved, env_off: envOff, effective: envOff ? { ...saved, enabled: false } : saved }
}
const chip = (over: { cfg?: InboxConfigView; hookSeenAt?: string | null }): InboxChipView =>
  inboxChipView({ cfg: over.cfg ?? cfgView(), hookSeenAt: over.hookSeenAt ?? null, activityAt: null, items: [], nowMs: NOW })!

/** 「hook 来问过」的各种新鲜度：刚刚、秒、分钟、小时、天的边界与最宽的写法。 */
const AGES_SEC = [0, 3, 5, 9, 10, 59, 60, 61, 599, 600, 3599, 3600, 47 * 3600, 47 * 3600 + 3599, 48 * 3600, 99 * 86400, 100 * 86400]
const TIMED_TEXT = /^hook (刚刚|\d+ (秒|分钟|小时|天)前)$/

describe('收件箱芯片：随时间变的文字预留固定宽度', () => {
  it('inboxChipTextIsTimed 只对「hook 来问过」的三档为真', () => {
    assert.equal(inboxChipTextIsTimed('active'), true)
    assert.equal(inboxChipTextIsTimed('recent'), true)
    assert.equal(inboxChipTextIsTimed('idle'), true)
    assert.equal(inboxChipTextIsTimed('none'), false)
    assert.equal(inboxChipTextIsTimed('muted'), false)
    assert.equal(inboxChipTextIsTimed('off'), false)
  })

  it('「hook 刚刚 / N 秒前 / N 分钟前 / N 小时前 / N 天前」都是 timed，其余几种文字都不是', () => {
    for (const s of AGES_SEC) {
      const v = chip({ hookSeenAt: at(s) })
      assert.match(v.text, TIMED_TEXT, `${s} 秒前：${v.text}`)
      assert.equal(inboxChipTextIsTimed(v.level), true, `${s} 秒前应预留宽度（level=${v.level}）`)
    }
    const others: Array<[string, InboxChipView]> = [
      ['hook 层开着从没来过', chip({ hookSeenAt: null })],
      ['hook 层关了且来过（已静音）', chip({ cfg: cfgView({ hooks: false }), hookSeenAt: at(30) })],
      ['hook 层关了且没来过', chip({ cfg: cfgView({ hooks: false }), hookSeenAt: null })],
      ['提醒总开关关了', chip({ cfg: cfgView({ enabled: false }), hookSeenAt: at(30) })],
      ['环境变量关了提醒', chip({ cfg: cfgView({}, true), hookSeenAt: at(30) })],
    ]
    for (const [name, v] of others) {
      assert.doesNotMatch(v.text, TIMED_TEXT, `${name}：${v.text}`)
      assert.equal(inboxChipTextIsTimed(v.level), false, `${name} 不该预留宽度（level=${v.level}）`)
    }
  })

  it('预留的宽度装得下最宽的写法，又不浪费一整个字符以上', () => {
    const widths = AGES_SEC.map(s => widthEm(chip({ hookSeenAt: at(s) }).text))
    const widest = Math.max(...widths)
    assert.ok(widest <= INBOX_CHIP_TIMED_TEXT_MIN_EM, `最宽 ${widest}em 超过预留的 ${INBOX_CHIP_TIMED_TEXT_MIN_EM}em`)
    assert.ok(INBOX_CHIP_TIMED_TEXT_MIN_EM - widest < 1, `预留 ${INBOX_CHIP_TIMED_TEXT_MIN_EM}em 比最宽的 ${widest}em 宽出一个字符以上`)
    // 明确点名两个最宽的真实场景：59 分钟前、47 小时前。
    assert.ok(Math.abs(widthEm('hook 59 分钟前') - 7.8) < 1e-9)
    assert.ok(widthEm('hook 47 小时前') <= INBOX_CHIP_TIMED_TEXT_MIN_EM)
  })

  it('模板里用的是字面量类名 min-w-[8em]，与常量一致，并且只在 timed 时加', () => {
    const vue = read('components/feedback/InboxStatusChip.vue')
    assert.match(vue, new RegExp(`min-w-\\[${INBOX_CHIP_TIMED_TEXT_MIN_EM}em\\]`), '模板里没有与常量一致的 min-w 类名')
    const line = vue.split('\n').find(l => l.includes('data-testid="inbox-chip-text"')) ?? ''
    assert.ok(line.includes('inboxChipTextIsTimed(view.level)'), '预留宽度必须由 inboxChipTextIsTimed(view.level) 决定')
    assert.match(line, /inboxChipTextIsTimed\(view\.level\) \? 'min-w-\[\d+em\]' : ''/, '写法应是条件类名：timed 才加，其余留空')
  })
})

describe('Agent 状态芯片：子活动文字放进固定宽度的盒子', () => {
  it('常见的子活动（思考 / 回应 / 常用工具）都装得下', () => {
    const samples = ['thinking', 'responding', 'tool:Shell', 'tool:Read', 'tool:Grep', 'tool:Glob', 'tool:Write', 'tool:StrReplace', 'tool:AwaitShell', 'tool:TodoWrite', 'tool:WebSearch', 'tool:Task']
    for (const s of samples) {
      const text = subText(s)
      assert.ok(widthEm(text) <= AGENT_STATUS_DETAIL_BOX_EM, `「${text}」约 ${widthEm(text)}em，超过盒子的 ${AGENT_STATUS_DETAIL_BOX_EM}em`)
    }
  })

  it('子活动文字的宽度差确实很大：这就是必须固定宽度的原因', () => {
    const widths = ['thinking', 'tool:Shell', 'tool:StrReplace'].map(s => widthEm(subText(s)))
    assert.ok(Math.max(...widths) - Math.min(...widths) > 4, `宽度差 ${Math.max(...widths) - Math.min(...widths)}em`)
  })

  it('模板：固定宽度、不收缩、写不下就截断；宽度与常量一致；窄屏仍然不显示', () => {
    const vue = read('components/feedback/AgentStatusChip.vue')
    const m = vue.match(/<span data-testid="agent-status-detail" class="([^"]*)"/)
    assert.ok(m, '找不到子活动文字的 span')
    const classes = m![1]!.split(/\s+/)
    for (const c of [`w-[${AGENT_STATUS_DETAIL_BOX_EM}em]`, 'shrink-0', 'truncate', 'hidden', 'sm:block']) {
      assert.ok(classes.includes(c), `子活动文字缺少类名 ${c}（现有：${classes.join(' ')}）`)
    }
    assert.ok(!classes.includes('min-w-0'), '不能再用 min-w-0 让它随内容收缩')
    assert.ok(!classes.some(c => /^(sm:)?inline$/.test(c)), '行内元素上 truncate 与固定宽度不生效，必须是块级')
  })
})

describe('回复窗口倒计时的宽度本来就不变（不需要预留，改格式时由这里提醒）', () => {
  it('回复窗口倒计时：一小时内总是 mm:ss 五个字符', () => {
    const nowMs = NOW
    const untilIn = (s: number) => new Date(nowMs + s * 1000).toISOString()
    for (const s of [1, 9, 10, 59, 60, 61, 599, 600, 3599]) {
      const v = windowView({ until: untilIn(s) }, nowMs)
      assert.equal(v.kind, 'running')
      assert.match(v.text, /^\d\d:\d\d$/, `${s} 秒：${v.text}`)
    }
    // 过了一小时才变成 h:mm:ss；这是一次性的档位切换，不是抖动
    assert.match(windowView({ until: untilIn(3600) }, nowMs).text, /^\d:\d\d:\d\d$/)
  })
})
