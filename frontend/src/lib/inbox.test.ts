// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import type { InboxConfig, InboxConfigView, QueuedFeedback } from '../api/types'
import {
  agentActivity,
  ago,
  appendDescription,
  appendPlaceholder,
  appendedToast,
  hasLiveItems,
  hookInstallCommand,
  hookInstallCommands,
  hookStatus,
  inboxChipView,
  inboxStatus,
  layersSummary,
  reminderNote,
  remindsAgent,
  retractedToast,
  shQuote,
  stateOf,
  viaLabel,
} from './inbox'

const NOW = Date.parse('2026-10-09T12:00:00Z')
const at = (secondsAgo: number) => new Date(NOW - secondsAgo * 1000).toISOString()

const item = (over: Partial<QueuedFeedback> = {}): QueuedFeedback => ({
  id: 1,
  workflow_id: 'wf',
  response_text: '把按钮改成蓝色',
  status: 'pending',
  created_at: at(30),
  updated_at: at(30),
  ...over,
})

const allOn: InboxConfig = { enabled: true, receipt_hint: true, hooks: true, prompt_clause: true }
const view = (over: Partial<InboxConfig> = {}, envOff = false): InboxConfigView => {
  const saved = { ...allOn, ...over }
  return { ...saved, env_off: envOff, effective: envOff ? { ...saved, enabled: false } : saved }
}

describe('stateOf', () => {
  it('服务端给出的 state 优先', () => {
    assert.equal(stateOf(item({ state: 'delivered' })), 'delivered')
    assert.equal(stateOf(item({ state: 'retracted', status: 'revoked' })), 'retracted')
  })
  it('旧服务端没有 state 时按 status 与时间戳推导', () => {
    assert.equal(stateOf(item()), 'queued')
    assert.equal(stateOf(item({ delivered_at: at(5) })), 'delivered')
    assert.equal(stateOf(item({ status: 'consumed' })), 'acked')
    assert.equal(stateOf(item({ status: 'revoked' })), 'revoked')
    assert.equal(stateOf(item({ status: 'revoked', delivered_at: at(5) })), 'retracted')
  })
})

describe('ago', () => {
  it('分档说成人话', () => {
    assert.equal(ago(at(2), NOW), '刚刚')
    assert.equal(ago(at(30), NOW), '30 秒前')
    assert.equal(ago(at(3 * 60), NOW), '3 分钟前')
    assert.equal(ago(at(2 * 3600), NOW), '2 小时前')
    assert.equal(ago(at(3 * 86400), NOW), '3 天前')
  })
  it('空值与坏值返回空串；时钟略微超前不出现负数', () => {
    assert.equal(ago(undefined, NOW), '')
    assert.equal(ago(null, NOW), '')
    assert.equal(ago('not a date', NOW), '')
    assert.equal(ago(at(-20), NOW), '刚刚')
  })
})

describe('inboxStatus', () => {
  it('已排队：一个勾，可撤回；没有提醒时说明在等什么', () => {
    const s = inboxStatus(item(), NOW)
    assert.deepEqual([s.stage, s.ticks, s.tone, s.canRetract], ['queued', 1, 'pending', true])
    assert.equal(s.label, '已排队')
    assert.match(s.detail, /等待 Agent 下次调用/)
    assert.equal(s.retractLabel, '撤回指令')
    assert.equal(s.badge, '待 AI 提取')
  })
  it('已排队但被提醒过：说明提醒了几次、提示行有没有出现', () => {
    const s = inboxStatus(item({ notified_count: 2, receipt_hint_at: at(70) }), NOW)
    assert.match(s.detail, /hook 已提醒 2 次/)
    assert.match(s.detail, /回执提示行已出现过（1 分钟前）/)
  })
  it('已读取：两个勾，说明经哪条路径，撤回要通知 Agent', () => {
    const s = inboxStatus(item({ state: 'delivered', delivered_at: at(120), delivered_via: 'receipt' }), NOW)
    assert.deepEqual([s.stage, s.ticks, s.tone, s.canRetract], ['delivered', 2, 'read', true])
    assert.match(s.detail, /经回执末尾的 inbox: 提示行读取/)
    assert.match(s.detail, /2 分钟前/)
    assert.match(s.detail, /等待确认/)
    assert.equal(s.retractLabel, '撤回并通知 Agent')
    assert.match(s.retractTitle, /告诉它忽略/)
    assert.equal(s.badge, 'AI 已读取')
  })
  it('已确认：三个勾，不能再撤回', () => {
    const s = inboxStatus(item({ state: 'acked', status: 'consumed', delivered_via: 'hook', acked_at: at(45) }), NOW)
    assert.deepEqual([s.stage, s.ticks, s.tone, s.canRetract], ['acked', 3, 'done', false])
    assert.match(s.detail, /hook/)
    assert.match(s.detail, /45 秒前确认/)
    assert.equal(s.retractLabel, '')
    assert.equal(s.badge, 'AI 已接收')
  })
  it('已过期：没有勾，不能撤回，说明没有被读到', () => {
    const s = inboxStatus(item({ state: 'expired', status: 'expired' }), NOW)
    assert.deepEqual([s.stage, s.ticks, s.tone, s.canRetract], ['expired', 0, 'muted', false])
    assert.equal(s.badge, '已过期')
    assert.match(s.detail, /没有读到/)
    // 旧服务端没有 state 字段时按 status 推导
    assert.equal(stateOf(item({ status: 'expired' })), 'expired')
  })
  it('每条路径都有文案', () => {
    for (const via of ['hook', 'receipt', 'tool', 'feedback'] as const) {
      assert.ok(viaLabel(via).length > 3, via)
    }
    assert.equal(viaLabel(''), '已读取')
    assert.equal(viaLabel(undefined), '已读取')
  })
  it('reminderNote 没有任何提醒时为空', () => {
    assert.equal(reminderNote({}, NOW), '')
    assert.equal(reminderNote({ notified_count: 0 }, NOW), '')
  })
})

describe('agentActivity', () => {
  it('按新鲜度分档', () => {
    assert.deepEqual(agentActivity(at(8), NOW), { text: '8 秒前', level: 'active' })
    assert.deepEqual(agentActivity(at(4 * 60), NOW), { text: '4 分钟前', level: 'recent' })
    assert.deepEqual(agentActivity(at(2 * 3600), NOW), { text: '2 小时前', level: 'idle' })
  })
  it('没有记录时明说（服务重启后内存清空）', () => {
    for (const v of [null, undefined, '', 'garbage']) {
      const a = agentActivity(v as string | null | undefined, NOW)
      assert.equal(a.level, 'unknown')
      assert.match(a.text, /暂无记录/)
    }
  })
})

describe('layersSummary', () => {
  it('全开', () => {
    const s = layersSummary(view())
    assert.equal(s.mode, 'on')
    assert.equal(s.text, '提醒方式：回执提示行 · 编辑器 hook · 提示词条款')
  })
  it('部分开启', () => {
    const s = layersSummary(view({ hooks: false }))
    assert.equal(s.mode, 'partial')
    assert.equal(s.text, '提醒方式：回执提示行 · 提示词条款')
  })
  it('总开关关闭、三层全关、环境变量强制关闭各有说明', () => {
    assert.equal(layersSummary(view({ enabled: false })).mode, 'off')
    assert.match(layersSummary(view({ enabled: false })).text, /下次发起交互时带走/)
    const none = layersSummary(view({ receipt_hint: false, hooks: false, prompt_clause: false }))
    assert.equal(none.mode, 'off')
    assert.match(none.text, /仍可主动读取/)
    const env = layersSummary(view({}, true))
    assert.equal(env.mode, 'env-off')
    assert.match(env.text, /RELAYMESH_INBOX=off/)
  })
  it('还没取到配置时不显示任何东西', () => {
    assert.equal(layersSummary(null).text, '')
    assert.equal(layersSummary(undefined).text, '')
  })
})

describe('toasts', () => {
  it('提醒开着时告诉用户 Agent 会收到提示；全关时回到旧说法', () => {
    assert.match(appendedToast(view()), /会收到提示/)
    assert.match(appendedToast(view({ hooks: false })), /会收到提示/)
    assert.match(appendedToast(view({ receipt_hint: false, hooks: false })), /发起交互时秒回/)
    assert.match(appendedToast(view({ enabled: false })), /发起交互时秒回/)
    assert.match(appendedToast(view({}, true)), /发起交互时秒回/)
    assert.match(appendedToast(null), /发起交互时秒回/)
  })
  it('读后撤回要说明会通知 Agent', () => {
    assert.match(retractedToast(true), /告知忽略/)
    assert.doesNotMatch(retractedToast(false), /告知/)
    assert.doesNotMatch(retractedToast(undefined), /告知/)
  })
})

describe('输入框文案随提醒开关变化', () => {
  it('remindsAgent：提示行或 hook 至少开一个，且总开关与环境变量都放行', () => {
    assert.equal(remindsAgent(view()), true)
    assert.equal(remindsAgent(view({ hooks: false })), true)
    assert.equal(remindsAgent(view({ receipt_hint: false })), true)
    assert.equal(remindsAgent(view({ receipt_hint: false, hooks: false })), false)
    assert.equal(remindsAgent(view({ prompt_clause: false, receipt_hint: false, hooks: false })), false)
    assert.equal(remindsAgent(view({ enabled: false })), false)
    assert.equal(remindsAgent(view({}, true)), false)
    assert.equal(remindsAgent(null), false)
  })
  it('开着提醒时说明 Agent 会收到提示；否则保持原来的说法', () => {
    assert.match(appendDescription(view()), /收到提示/)
    assert.match(appendPlaceholder(view()), /收到提示/)
    for (const cfg of [view({ enabled: false }), view({}, true), null]) {
      assert.match(appendDescription(cfg), /下次发起交互时秒回/)
      assert.doesNotMatch(appendDescription(cfg), /收到提示/)
      assert.match(appendPlaceholder(cfg), /下次发起交互时直接秒回/)
      assert.doesNotMatch(appendPlaceholder(cfg), /收到提示/)
    }
  })
})

describe('hasLiveItems', () => {
  it('只有已确认的条目时不必再轮询', () => {
    assert.equal(hasLiveItems([]), false)
    assert.equal(hasLiveItems([item({ state: 'acked', status: 'consumed' })]), false)
    assert.equal(hasLiveItems([item({ state: 'expired', status: 'expired' })]), false)
    assert.equal(hasLiveItems([item({ state: 'acked', status: 'consumed' }), item({ id: 2 })]), true)
    assert.equal(hasLiveItems([item({ state: 'delivered' })]), true)
  })
})

describe('inboxChipView', () => {
  const base = { hookSeenAt: at(20), activityAt: at(8), items: [] as QueuedFeedback[], nowMs: NOW }

  it('配置还没取到时不显示', () => {
    assert.equal(inboxChipView({ ...base, cfg: null }), null)
    assert.equal(inboxChipView({ ...base, cfg: undefined }), null)
  })

  it('hook 来过：按钮上写它最近一次来问的时间，指示灯随新鲜度分级', () => {
    const a = inboxChipView({ ...base, cfg: view() })!
    assert.deepEqual([a.level, a.text], ['active', 'hook 20 秒前'])
    assert.equal(inboxChipView({ ...base, cfg: view(), hookSeenAt: at(300) })!.level, 'recent')
    const idle = inboxChipView({ ...base, cfg: view(), hookSeenAt: at(3 * 3600) })!
    assert.deepEqual([idle.level, idle.text], ['idle', 'hook 3 小时前'])
  })

  it('hook 层开着却从没来过：提示还没装', () => {
    const v = inboxChipView({ ...base, cfg: view(), hookSeenAt: null })!
    assert.deepEqual([v.level, v.text], ['none', 'hook 未装'])
    assert.match(v.title, /还没收到过查询/)
  })

  it('hook 层关了：按钮只当收件箱入口，来过的标成已静音', () => {
    const never = inboxChipView({ ...base, cfg: view({ hooks: false }), hookSeenAt: null })!
    assert.deepEqual([never.level, never.text], ['off', '收件箱'])
    const muted = inboxChipView({ ...base, cfg: view({ hooks: false }), hookSeenAt: at(30) })!
    assert.deepEqual([muted.level, muted.text], ['muted', '收件箱'])
  })

  it('提醒整体关了（总开关、三层全关、环境变量）都写「提醒已关」', () => {
    const allOff = view({ receipt_hint: false, hooks: false, prompt_clause: false })
    for (const cfg of [view({ enabled: false }), allOff, view({}, true)]) {
      const v = inboxChipView({ ...base, cfg })!
      assert.deepEqual([v.level, v.text], ['off', '提醒已关'])
    }
  })

  it('统计排队中与待确认的条数；已确认、已过期的不计，悬停提示带上关键状态', () => {
    const items = [
      item({ id: 1 }),
      item({ id: 2 }),
      item({ id: 3, state: 'delivered' }),
      item({ id: 4, state: 'acked', status: 'consumed' }),
      item({ id: 5, state: 'expired', status: 'expired' }),
    ]
    const v = inboxChipView({ ...base, cfg: view(), items })!
    assert.deepEqual([v.queued, v.delivered], [2, 1])
    assert.match(v.title, /Agent 最近一次与 RelayMesh 交互：8 秒前/)
    assert.match(v.title, /提醒方式：回执提示行 · 编辑器 hook · 提示词条款/)
    assert.match(v.title, /排队中 2 条，已读取待确认 1 条/)
    assert.doesNotMatch(inboxChipView({ ...base, cfg: view() })!.title, /排队中/)
  })

  it('没有 Agent 活动记录时明说', () => {
    const v = inboxChipView({ ...base, cfg: view(), activityAt: null })!
    assert.match(v.title, /暂无交互记录/)
  })
})

describe('hookStatus', () => {
  it('没有配置时不显示', () => {
    assert.equal(hookStatus(at(5), null, NOW).level, 'hidden')
  })

  it('hook 层开着却从没来过：提示多半没装，并给出安装入口', () => {
    const v = hookStatus(null, view(), NOW)
    assert.equal(v.level, 'none')
    assert.equal(v.showInstall, true)
    assert.match(v.text, /还没收到过查询/)
    assert.equal(hookStatus('不是时间', view(), NOW).level, 'none')
  })

  it('来过：按多久以前分级，写明时间，不再给安装入口', () => {
    const a = hookStatus(at(20), view(), NOW)
    assert.deepEqual([a.level, a.showInstall], ['active', false])
    assert.match(a.text, /最近一次查询：20 秒前/)
    assert.equal(hookStatus(at(300), view(), NOW).level, 'recent')
    const idle = hookStatus(at(3 * 3600), view(), NOW)
    assert.equal(idle.level, 'idle')
    assert.match(idle.text, /3 小时前/)
  })

  it('hook 层或总开关关了：从没来过就不显示；来过则说明已静音、不再提醒', () => {
    for (const off of [view({ hooks: false }), view({ enabled: false }), view({}, true)]) {
      assert.equal(hookStatus(null, off, NOW).level, 'hidden')
      const muted = hookStatus(at(30), off, NOW)
      assert.equal(muted.level, 'muted')
      assert.equal(muted.showInstall, false)
      assert.match(muted.text, /已关闭/)
    }
  })
})

describe('安装命令', () => {
  it('shQuote 把单引号转义成 POSIX 能读懂的样子', () => {
    assert.equal(shQuote('wf-1'), `'wf-1'`)
    assert.equal(shQuote(`it's`), `'it'\\''s'`)
    assert.equal(shQuote('a b;rm -rf /'), `'a b;rm -rf /'`)
  })

  it('命令走 install.sh 一键安装 Go hook；地址末尾的斜杠与工作流里的引号都不会破坏命令', () => {
    const cmd = hookInstallCommand('http://hub.local:18775/', 'relaymesh-continuation')
    assert.ok(cmd.includes('/hooks/cursor/install.sh'))
    assert.ok(cmd.includes("RELAYMESH_HOOK_INSTALL_BASE='http://hub.local:18775'"))
    assert.ok(cmd.includes("--workflow 'relaymesh-continuation'"))
    const tricky = hookInstallCommand('https://hub.example', `x'; touch /tmp/pwned; '`)
    assert.ok(tricky.includes(`--workflow 'x'\\''; touch /tmp/pwned; '\\'''`), tricky)
  })

  it('命令里不含任何令牌字样', () => {
    assert.doesNotMatch(hookInstallCommand('https://hub.example', 'wf'), /token|bearer|rm-/i)
  })

  it('只提供 Go 二进制的安装命令，没有 Python 备用', () => {
    const cmds = hookInstallCommands('https://hub.example', 'wf')
    assert.deepEqual(Object.keys(cmds).sort(), ['github', 'primary', 'windows'])
    for (const v of Object.values(cmds)) assert.doesNotMatch(String(v), /python|\.py\b/i)
  })
})
