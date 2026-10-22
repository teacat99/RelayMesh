// 运行：pnpm --dir frontend run test:unit
// 守护「页脚快捷条里的芯片不因为文字变化而抖」（#197）。
// 背景：快捷条是一行靠左排的 flex，前面某个芯片的宽度变了，后面所有芯片都会被推着走（窄屏是横滑条，宽屏还可能换行）。
// 在真实会话里量到两处最频繁的来源：Agent 子活动文字（几秒换一次，宽度差最多十几 em）与收件箱芯片的「hook N 秒前」。
// 做法：收件箱芯片的随时间变的文字预留固定宽度；Agent 子活动文字当时也预留了（固定 10em 的盒子），但那个盒子带来芯片里的空白
// （#222 / #833），用户确认项 Q74 选 b：宁可后面的芯片随文字挪动，也不要空白——所以子活动现在宽度完全跟着文字走，这里守住「不再预留」。
// 其余芯片的文字按现在的格式宽度本来就不变，这里用断言把「为什么不用预留」钉住，以后谁改了格式让宽度开始变，测试会提醒他补预留。
import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, it } from 'node:test'
import type { InboxConfig, InboxConfigView } from '../api/types'
import { subText } from './agentState'
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

  it('预留宽度的盒子里文字必须靠左：不写 text-left 会继承 <button> 默认的居中，盒子两边各空一截（#222 / #833）', () => {
    const vue = read('components/feedback/InboxStatusChip.vue')
    const line = vue.split('\n').find(l => l.includes('data-testid="inbox-chip-text"')) ?? ''
    const m = line.match(/class="([^"]*)"/)
    assert.ok(m, '找不到收件箱芯片文字的 class')
    assert.ok(m![1]!.split(/\s+/).includes('text-left'), `收件箱芯片文字缺少 text-left（现有：${m![1]}）`)
  })
})

describe('Agent 状态芯片：子活动文字的宽度完全跟着文字走（Q74 选 b，取代 #197 的固定 10em 盒子）', () => {
  it('子活动文字的宽度差确实很大：它换文字时芯片宽度会跟着变——这是用户选定的取舍（宁可后面的芯片挪动，也不要空白）', () => {
    const widths = ['thinking', 'tool:Shell', 'tool:StrReplace'].map(s => widthEm(subText(s)))
    assert.ok(Math.max(...widths) - Math.min(...widths) > 4, `宽度差 ${Math.max(...widths) - Math.min(...widths)}em`)
  })

  it('子活动文字本身有长度上限（工具名被截短），英文工具名再长也放得进芯片的最大宽度（sm:max-w-[30rem]，12px 字号下 = 40em）', () => {
    const longest = subText('tool:' + 'X'.repeat(200))
    assert.ok(widthEm(longest) <= 40 - 12, `最长的子活动「${longest}」约 ${widthEm(longest)}em，要给前面的圆点 / 状态词 / 后面的用时留出至少 12em`)
    // 芯片的最大宽度写在模板里：这条保证上面的 40em 没有悄悄变
    assert.ok(/sm:max-w-\[30rem\]/.test(read('components/feedback/AgentStatusChip.vue')), '芯片的最大宽度变了，同步改上面的 40em')
  })

  it('模板：不预留固定宽度、不禁止收缩（空白会让芯片里出现一截一截的空）；写不下才截断；窄屏仍然不显示', () => {
    const vue = read('components/feedback/AgentStatusChip.vue')
    const m = vue.match(/<span data-testid="agent-status-detail" class="([^"]*)"/)
    assert.ok(m, '找不到子活动文字的 span')
    const classes = m![1]!.split(/\s+/)
    for (const c of ['truncate', 'hidden', 'sm:block']) {
      assert.ok(classes.includes(c), `子活动文字缺少类名 ${c}（现有：${classes.join(' ')}）`)
    }
    assert.ok(!classes.some(c => /^(sm:)?w-\[/.test(c) || /^(sm:)?min-w-(?!0$)/.test(c)), `不能再给子活动预留固定宽度（现有：${classes.join(' ')}）`)
    assert.ok(!classes.includes('shrink-0'), '不能禁止收缩：写不下的长文字要截断，而不是把芯片撑出屏幕')
    assert.ok(!classes.some(c => /^(sm:)?inline$/.test(c)), '行内元素上 truncate 不生效，必须是块级')
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
