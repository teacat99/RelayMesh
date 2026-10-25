import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, it } from 'node:test'
import type { WorkflowPromptsView } from '../api/workflowPrompts'
import {
  RESIDENT_MAX_DEFAULT,
  RESIDENT_TEMPLATES,
  builtinTemplate,
  formatRevisionTime,
  isTemplateOverridden,
  normalizeResidentText,
  residentChipText,
  residentChipTitle,
  residentLength,
  residentReadState,
  residentStateLabel,
  residentSummary,
  residentToldLine,
  templateOptions,
  templateText,
  withTemplateOverride,
} from './residentPrompt'

function srcDir(): string {
  for (const base of [join(process.cwd(), 'src'), join(process.cwd(), 'frontend', 'src')]) {
    if (existsSync(join(base, 'lib', 'residentPrompt.test.ts'))) return base
  }
  throw new Error('找不到 frontend/src，请在仓库根目录或 frontend 目录下运行测试')
}

function view(patch: Partial<WorkflowPromptsView> = {}): WorkflowPromptsView {
  return {
    workflow_id: 'wf-1',
    resident: '',
    resident_rev: 0,
    told: true,
    told_rev: 0,
    max: RESIDENT_MAX_DEFAULT,
    revisions: [],
    ...patch,
  }
}

describe('字数上限与服务端一致', () => {
  it('RESIDENT_MAX_DEFAULT 等于 Go 端 model.ResidentPromptMax', () => {
    const go = readFileSync(join(srcDir(), '..', '..', 'internal', 'model', 'workflow_prompt.go'), 'utf8')
    const m = go.match(/ResidentPromptMax\s*=\s*(\d+)/)
    assert.ok(m, '没找到 ResidentPromptMax')
    assert.equal(RESIDENT_MAX_DEFAULT, Number(m![1]))
  })
})

describe('内置模板', () => {
  it('主线 / 分支各一份，文字都不超过上限，且写明确认卡规则（Q84）', () => {
    assert.deepEqual(RESIDENT_TEMPLATES.map(t => t.id), ['main', 'branch'])
    for (const t of RESIDENT_TEMPLATES) {
      assert.ok(residentLength(t.text) <= RESIDENT_MAX_DEFAULT, `${t.id} 太长：${residentLength(t.text)}`)
      assert.ok(t.text.includes('确认卡') && t.text.includes('async_ask'), `${t.id} 应写明「需要我决策的一律发确认卡」`)
      assert.equal(normalizeResidentText(t.text), t.text, `${t.id} 的出厂文字应已是整理后的形态（保存不会改动它）`)
    }
    assert.ok(builtinTemplate('main').text.includes('授权以回执 auth'), '主线模板应声明不放宽授权')
    assert.ok(builtinTemplate('branch').text.includes('分支会话'))
  })
})

describe('normalizeResidentText / residentLength：与服务端 NormalizePromptText 一致', () => {
  it('统一换行、去首尾空白、保留换行与制表符、去掉其它控制字符', () => {
    assert.equal(normalizeResidentText('  a\r\nb\rc\td\u0000e\u0007f\u007fg\u0085h  '), 'a\nb\nc\tdefgh')
    assert.equal(normalizeResidentText('   \n\t  '), '')
  })
  it('按字符数计：一个表情是一个字符，不是两个', () => {
    assert.equal(residentLength('😀😀abc'), 5)
    assert.equal(residentLength('  你好  '), 2)
    assert.equal(residentLength('x'.repeat(RESIDENT_MAX_DEFAULT)), RESIDENT_MAX_DEFAULT)
  })
})

describe('模板覆盖', () => {
  it('没覆盖用出厂文字；覆盖的优先；空白覆盖当作没覆盖', () => {
    assert.equal(templateText('main'), builtinTemplate('main').text)
    assert.equal(templateText('main', { residentMain: '  我的主线  ' }), '我的主线')
    assert.equal(templateText('main', { residentMain: '   ' }), builtinTemplate('main').text)
    assert.equal(isTemplateOverridden('main', { residentMain: '我的主线' }), true)
    assert.equal(isTemplateOverridden('branch', { residentMain: '我的主线' }), false)
    assert.equal(isTemplateOverridden('main', null), false)
  })
  it('withTemplateOverride：写入覆盖；与出厂一致或为空 = 清掉；不改入参', () => {
    const base = { residentBranch: '分支覆盖' }
    const next = withTemplateOverride(base, 'main', '  新主线  ')
    assert.deepEqual(next, { residentBranch: '分支覆盖', residentMain: '新主线' })
    assert.deepEqual(base, { residentBranch: '分支覆盖' })
    assert.deepEqual(withTemplateOverride(next, 'main', builtinTemplate('main').text), { residentBranch: '分支覆盖' })
    assert.deepEqual(withTemplateOverride(next, 'main', '   '), { residentBranch: '分支覆盖' })
    assert.deepEqual(withTemplateOverride(undefined, 'branch', '甲'), { residentBranch: '甲' })
  })
  it('templateOptions：每个模板带当前生效文字与是否被覆盖', () => {
    const opts = templateOptions({ residentBranch: '分支覆盖' })
    assert.deepEqual(opts.map(o => [o.id, o.overridden]), [['main', false], ['branch', true]])
    assert.equal(opts[1].current, '分支覆盖')
    assert.equal(opts[0].current, builtinTemplate('main').text)
  })
})

describe('小标签与弹窗里的状态文字', () => {
  it('residentReadState：没配置 / 已清空 = unset；有文字看有没有被读到', () => {
    assert.equal(residentReadState(null), 'unset')
    assert.equal(residentReadState(view()), 'unset')
    assert.equal(residentReadState(view({ resident: '', resident_rev: 3 })), 'unset')
    assert.equal(residentReadState(view({ resident: '规则', resident_rev: 1, told: false })), 'unread')
    assert.equal(residentReadState(view({ resident: '规则', resident_rev: 1, told: true })), 'read')
  })
  it('residentChipText：未设置 / 待读 / 已读', () => {
    assert.equal(residentChipText(null), '常驻 · 未设置')
    assert.equal(residentChipText(view({ resident: '规则', resident_rev: 2, told: false })), '常驻 · v2 · 待读')
    assert.equal(residentChipText(view({ resident: '规则', resident_rev: 2, told: true })), '常驻 · v2')
  })
  it('residentStateLabel（手机页头「⋯」菜单里的状态）：未设置 / 待读 / 已读', () => {
    assert.equal(residentStateLabel(null), '未设置')
    assert.equal(residentStateLabel(view({ resident: '', resident_rev: 3 })), '未设置')
    assert.equal(residentStateLabel(view({ resident: '规则', resident_rev: 2, told: false })), 'v2 · 待读')
    assert.equal(residentStateLabel(view({ resident: '规则', resident_rev: 2, told: true })), 'v2 · 已读')
  })
  it('residentChipTitle：写明版本、字数、读到没有；清空未告知时说明下一次回执会告知', () => {
    assert.ok(residentChipTitle(null).includes('每次回执'))
    assert.ok(residentChipTitle(view()).includes('还没有常驻规则'))
    assert.ok(residentChipTitle(view({ resident: '', resident_rev: 2, told: false })).includes('已清除'))
    const unread = residentChipTitle(view({ resident: '一二三', resident_rev: 2, told: false }))
    assert.ok(unread.includes('v2') && unread.includes('3 字') && unread.includes('还没读到'))
    const read = residentChipTitle(view({ resident: '一二三', resident_rev: 2, told: true }))
    assert.ok(read.includes('已读到'))
  })
  it('residentToldLine：四种状态各有一句话', () => {
    assert.ok(residentToldLine(view()).includes('还没有保存过'))
    assert.ok(residentToldLine(view({ resident: '规则', resident_rev: 1, told: false })).includes('还没读到 v1'))
    assert.ok(residentToldLine(view({ resident: '规则', resident_rev: 1, told: true })).includes('已读到 v1'))
    assert.ok(residentToldLine(view({ resident: '', resident_rev: 2, told: false })).includes('还没收到说明'))
    assert.ok(residentToldLine(view({ resident: '', resident_rev: 2, told: true })).includes('已收到说明'))
  })
})

describe('历史版本的显示', () => {
  it('formatRevisionTime：今年省略年份，往年带年份，无法解析原样返回', () => {
    const now = new Date(2026, 9, 22, 12, 0, 0)
    assert.equal(formatRevisionTime(new Date(2026, 9, 22, 16, 40).toISOString(), now), '10-22 16:40')
    assert.equal(formatRevisionTime(new Date(2025, 0, 3, 8, 5).toISOString(), now), '2025-01-03 08:05')
    assert.equal(formatRevisionTime('不是时间', now), '不是时间')
  })
  it('residentSummary：折成一行、超长截断、空文字写明是清除', () => {
    assert.equal(residentSummary('第一行\n第二行\n\n第三行'), '第一行 第二行 第三行')
    assert.equal(residentSummary(''), '（空：清除）')
    const long = residentSummary('字'.repeat(100), 10)
    assert.equal(long, `${'字'.repeat(10)}…`)
  })
})

// 手机上输入区工具条是横向滑动的条，「常驻」小标签排在最右边、不滑就看不到（信息架构套件 ia-reach 在手机档案上抓到的）：
// 页头「⋯」菜单里再放一个入口，点开的是同一个弹窗。这里钉住「靠它们成立」的写法。
describe('手机入口：页头「⋯」菜单里的「常驻规则」', () => {
  const MORE = readFileSync(join(srcDir(), 'components/feedback/SessionHeaderMore.vue'), 'utf8')
  const CHIP = readFileSync(join(srcDir(), 'components/feedback/ResidentPromptChip.vue'), 'utf8')
  const STORE = readFileSync(join(srcDir(), 'stores/workflowPrompts.ts'), 'utf8')
  const tpl = MORE.slice(MORE.indexOf('\n<template>')).replace(/\s+/g, ' ')

  it('条目在「提醒」与「工作表」之间，点它通过 store 打开弹窗（不新增 emit，页头原有的事件签名不变）', () => {
    const notify = tpl.indexOf('<ConversationNotifyItems')
    const entry = tpl.indexOf('data-testid="session-resident-entry"')
    const sheet = tpl.indexOf('data-testid="session-sheet-entry"')
    assert.ok(notify > 0 && notify < entry && entry < sheet, '顺序应是 提醒 → 常驻规则 → 工作表')
    assert.match(tpl, /data-testid="session-resident-entry"[^>]*@select="prompts\.openDialog\(props\.workflowId\)"/)
    assert.match(tpl, /data-testid="session-resident-state"[^>]*>\{\{ residentStateLabel\(residentView\) \}\}/)
    assert.match(MORE, /\(e: 'open-sheet'\): void; \(e: 'open-project'\): void/)
  })

  it('弹窗只有一个：小标签与菜单条目共用 store.dialogFor，换会话时关掉别人的弹窗', () => {
    assert.match(STORE, /const dialogFor = ref\(''\)/)
    assert.match(STORE, /return \{[^}]*\bdialogFor\b[^}]*\bopenDialog\b[^}]*\bcloseDialog\b/)
    assert.match(CHIP, /get: \(\) => store\.dialogFor === props\.workflowId/)
    assert.match(CHIP, /@click="store\.openDialog\(workflowId\)"/)
    assert.match(CHIP, /store\.dialogFor !== id\) store\.closeDialog\(\)/)
    assert.ok(!/const open = ref\(/.test(CHIP), '小标签里不该再有自己的 open 状态')
  })
})
