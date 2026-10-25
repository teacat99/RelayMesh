import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, it } from 'node:test'
import type { WakeKind, WakeVarInfo } from '../api/wakePrompts'
import { LAUNCHER_DEFAULT_LIMITS } from './terminal/launcher'
import {
  WAKE_KINDS,
  WAKE_TEMPLATE_MAX_DEFAULT,
  insertVarAt,
  normalizeWakeText,
  wakeDirty,
  wakeFlattenNote,
  wakeKeyOf,
  wakeKindLabel,
  wakeLayerHint,
  wakeLayerLabel,
  wakeLength,
  wakeOverrideOf,
  wakeTemplateProblem,
  wakeVarNames,
  wakeVarsFor,
  withWakeOverride,
} from './wakePrompts'

function repoRoot(): string {
  for (const base of [process.cwd(), join(process.cwd(), '..')]) {
    if (existsSync(join(base, 'internal', 'sessionprompt', 'sessionprompt.go'))) return base
  }
  throw new Error('找不到仓库根目录，请在仓库根目录或 frontend 目录下运行测试')
}

function goSource(...parts: string[]): string {
  return readFileSync(join(repoRoot(), ...parts), 'utf8')
}

describe('来源与名称', () => {
  it('三类唤醒词各有字段名与中文名（原来的「键入唤醒」随交互式预设下线，#245）', () => {
    assert.deepEqual([...WAKE_KINDS], ['resume', 'new', 'web_start'])
    assert.deepEqual(WAKE_KINDS.map(wakeKeyOf), ['resume', 'new', 'webStart'])
    assert.deepEqual(WAKE_KINDS.map(wakeKindLabel), ['续聊', '新开对话', '网页启动提示词'])
  })
  it('分层名称：已知的有中文名与说明，未知的原样返回', () => {
    for (const l of ['preset', 'session', 'global', 'builtin', 'draft']) {
      assert.notEqual(wakeLayerLabel(l), l, l)
      assert.notEqual(wakeLayerHint(l), '', l)
    }
    assert.equal(wakeLayerLabel('whatever'), 'whatever')
    assert.equal(wakeLayerHint('whatever'), '')
    assert.equal(wakeLayerLabel(''), '')
  })
})

describe('变量清单', () => {
  it('每一类能用的变量', () => {
    const resume = wakeVarNames('resume')
    assert.ok(resume.includes('workflow') && resume.includes('limit') && resume.includes('anchor'))
    assert.ok(!resume.includes('n') && !resume.includes('max') && !resume.includes('reason'), 'n / max / reason 随键入唤醒下线，变量按钮里没有')
    assert.deepEqual(wakeVarNames('new'), resume, '新开对话与续聊同一组')
    assert.deepEqual(wakeVarNames('web_start'), ['workflow', 'title', 'project'])
  })
  it('与服务端变量表一致：三类变量合起来正好是 sessionprompt.VarList 的全部名字', () => {
    const src = goSource('internal', 'sessionprompt', 'sessionprompt.go')
    const body = src.slice(src.indexOf('func VarList()'))
    const end = body.indexOf('\n}\n')
    const names = [...body.slice(0, end).matchAll(/Name:\s*"(\w+)"/g)].map(m => m[1])
    assert.ok(names.length >= 13, `解析出的变量太少：${names.join(',')}`)
    const ours = new Set<string>(['resume', 'new', 'web_start'].flatMap(k => wakeVarNames(k as WakeKind)))
    assert.deepEqual([...ours].sort(), [...names].sort())
  })
  it('按服务端给的顺序筛选', () => {
    const all: WakeVarInfo[] = [
      { name: 'future_var', desc: '服务端以后新增、前端还不认识的变量：任何一类都不显示' },
      { name: 'workflow', desc: 'w' },
      { name: 'limit', desc: 'l' },
      { name: 'title', desc: 't' },
    ]
    assert.deepEqual(wakeVarsFor('web_start', all).map(v => v.name), ['workflow', 'title'])
    assert.deepEqual(wakeVarsFor('resume', all).map(v => v.name), ['workflow', 'limit', 'title'])
    assert.deepEqual(wakeVarsFor('resume', []), [])
  })
})

describe('文字整理与字数', () => {
  it('统一换行、去控制字符、去首尾空白，中间的换行与制表符保留', () => {
    assert.equal(normalizeWakeText('  a\r\nb\rc\u0000d\u0007e\tf  '), 'a\nb\ncde\tf')
    assert.equal(normalizeWakeText('\u200b'), '\u200b', '零宽字符不是控制字符，不动')
    assert.equal(normalizeWakeText('   '), '')
  })
  it('字数按字符数：中文与 emoji 都算 1', () => {
    assert.equal(wakeLength('续聊'), 2)
    assert.equal(wakeLength('续聊😀'), 3)
    assert.equal(wakeLength('  \n  '), 0)
    assert.equal(wakeLength('a\r\nb'), 3, '换行统一成一个字符')
  })
  it('上限与服务端一致', () => {
    const src = goSource('internal', 'sessionprompt', 'sessionprompt.go')
    assert.match(src, new RegExp(`TemplateMaxRunes\\s*=\\s*${WAKE_TEMPLATE_MAX_DEFAULT}\\b`))
    // 预设正文与模板同一个上限：用「以默认为底稿」改写的内置默认必须能存下
    assert.equal(LAUNCHER_DEFAULT_LIMITS.runPrompt, WAKE_TEMPLATE_MAX_DEFAULT)
    assert.match(goSource('internal', 'store', 'terminal_launcher_run.go'), new RegExp(`MaxHeadlessPromptRunes\\s*=\\s*${WAKE_TEMPLATE_MAX_DEFAULT}\\b`))
  })
})

describe('保存前的本地检查', () => {
  it('空文字没有问题（空 = 清除覆盖）', () => {
    for (const k of WAKE_KINDS) assert.equal(wakeTemplateProblem(k, ''), '', k)
    assert.equal(wakeTemplateProblem('new', '   \n '), '')
  })
  it('超过上限：写明上限与现在的字数', () => {
    const p = wakeTemplateProblem('resume', '长'.repeat(WAKE_TEMPLATE_MAX_DEFAULT + 1))
    assert.match(p, new RegExp(`最多 ${WAKE_TEMPLATE_MAX_DEFAULT} 个字，现在是 ${WAKE_TEMPLATE_MAX_DEFAULT + 1} 个字`))
    assert.equal(wakeTemplateProblem('resume', '长'.repeat(WAKE_TEMPLATE_MAX_DEFAULT)), '')
    assert.match(wakeTemplateProblem('resume', '长'.repeat(11), 10), /最多 10 个字，现在是 11 个字/, '用调用方给的上限')
  })
  it('新开对话与网页启动提示词必须带 %workflow，续聊不要求', () => {
    assert.match(wakeTemplateProblem('new', '没有编号'), /新开对话模板必须包含 %workflow/)
    assert.match(wakeTemplateProblem('web_start', '没有编号'), /网页启动提示词模板必须包含 %workflow/)
    assert.equal(wakeTemplateProblem('new', '接入 %workflow'), '')
    assert.equal(wakeTemplateProblem('web_start', '%title / %workflow'), '')
    assert.equal(wakeTemplateProblem('resume', '没有编号'), '')
  })
})

describe('在光标处插入变量', () => {
  it('光标处插入，返回插入后的光标位置', () => {
    assert.deepEqual(insertVarAt('先读 再做', 3, 3, 'inbox'), { text: '先读 %inbox再做', caret: 9 })
    assert.deepEqual(insertVarAt('', 0, 0, 'workflow'), { text: '%workflow', caret: 9 })
    assert.deepEqual(insertVarAt('abc', 3, 3, 'n'), { text: 'abc%n', caret: 5 })
  })
  it('选区非空时替换选区', () => {
    assert.deepEqual(insertVarAt('读 XXX 再做', 2, 5, 'tasks'), { text: '读 %tasks 再做', caret: 8 })
  })
  it('后面紧跟字母 / 数字 / 下划线时补一个空格，免得被认成更长的变量名', () => {
    const r = insertVarAt('abc', 0, 0, 'n')
    assert.equal(r.text, '%n abc')
    assert.equal(r.caret, 2)
    assert.equal(insertVarAt('_x', 0, 0, 'n').text, '%n _x')
    assert.equal(insertVarAt('9', 0, 0, 'n').text, '%n 9')
    assert.equal(insertVarAt('，继续', 0, 0, 'n').text, '%n，继续', '中文标点后面不用补')
  })
  it('越界与反向的选区被收敛', () => {
    assert.deepEqual(insertVarAt('ab', -5, 99, 'n'), { text: '%n', caret: 2 })
    assert.deepEqual(insertVarAt('ab', 9, 1, 'n'), { text: 'ab%n', caret: 4 })
  })
})

describe('草稿与已保存', () => {
  it('按服务端的整理方式比较', () => {
    assert.equal(wakeDirty('  先读\r\n再做 ', '先读\n再做'), false)
    assert.equal(wakeDirty('先读', '先读 再做'), true)
    assert.equal(wakeDirty('', '   '), false)
    assert.equal(wakeDirty('x', ''), true)
  })
})

describe('全局覆盖的读写', () => {
  it('读：没有或全空白都是空串', () => {
    assert.equal(wakeOverrideOf(undefined, 'resume'), '')
    assert.equal(wakeOverrideOf({}, 'new'), '')
    assert.equal(wakeOverrideOf({ new: '   ' }, 'new'), '')
    assert.equal(wakeOverrideOf({ webStart: '启动 %workflow' }, 'web_start'), '启动 %workflow')
  })
  it('写：规整后写入，不改传入的对象，其它类不动', () => {
    const o = { resume: '旧' }
    const n = withWakeOverride(o, 'new', '  接入 %workflow\r\n')
    assert.deepEqual(n, { resume: '旧', new: '接入 %workflow' })
    assert.deepEqual(o, { resume: '旧' }, '不能改原对象')
    assert.deepEqual(withWakeOverride(undefined, 'web_start', 'x %workflow'), { webStart: 'x %workflow' })
  })
  it('清除：空文字删掉这一类，全部清空时返回 undefined（设置里不留空壳）', () => {
    assert.deepEqual(withWakeOverride({ resume: 'a', new: 'b %workflow' }, 'new', ''), { resume: 'a' })
    assert.equal(withWakeOverride({ resume: 'a' }, 'resume', '  '), undefined)
    assert.equal(withWakeOverride(undefined, 'resume', ''), undefined)
  })
})

describe('合并成一行的说明', () => {
  it('续聊 / 新开对话有说明，网页启动提示词没有', () => {
    assert.match(wakeFlattenNote('resume'), /合并成一行/)
    assert.equal(wakeFlattenNote('new'), wakeFlattenNote('resume'))
    assert.equal(wakeFlattenNote('web_start'), '')
  })
})
