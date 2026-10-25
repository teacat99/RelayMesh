// 正文里的四种引用（任务 #N / 确认项 Q<N> / 文档 [[路径]] / 会话号 sess-xxxxxxxx）：识别要保守——
// 只在目标确实存在时链接，宁可漏链接也不能误链接；文字内容无损、规则与服务端 [[ ]] 语法一致。
import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  linkRefClass,
  linkRefHint,
  linkRefHref,
  parseDocRef,
  splitLinkRefs,
  wholeLinkRef,
  buildSeqIndex,
  type LinkRef,
  type LinkRefIndex,
} from './linkRefs'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

interface IndexSpec {
  tasks?: Record<number, string>
  confirms?: Record<number, string>
  docs?: Record<string, string>
  sessions?: Record<string, string>
}

/** 造一个「哪些目标存在」的索引；值是悬停里的标题。 */
function index(spec: IndexSpec = {}): LinkRefIndex {
  return {
    task: seq => (seq in (spec.tasks ?? {}) ? { title: spec.tasks![seq] } : null),
    confirm: n => (n in (spec.confirms ?? {}) ? { key: `Q${n}`, title: spec.confirms![n] } : null),
    doc: path => (path in (spec.docs ?? {}) ? { key: path, title: spec.docs![path] } : null),
    session: hash => {
      const full = (spec.sessions ?? {})[hash]
      return full ? { key: full, title: '某一轮' } : null
    },
  }
}

const EVERYTHING = index({
  tasks: { 36: '任务三十六', 7: '' },
  confirms: { 23: '要不要做', 5: '', 6: '', 3: 'Q3 题' },
  docs: { 'design/preview-view.md': '预览视图', 'design/x.md': '文档 X', 'x.md': '根文档', 'design/b.md': 'B', 'c.md': 'C' },
  sessions: { '9f02517c': 'sess-9f02517c' },
})

/** 一段文字里被链接的引用：[种类, 键, 显示文字]。 */
function refs(text: string, idx: LinkRefIndex = EVERYTHING, fromPath = ''): Array<[string, string, string]> {
  return splitLinkRefs(text, idx, fromPath).flatMap(s => (s.ref ? [[s.ref.kind, s.ref.key, s.shown ?? s.text] as [string, string, string]] : []))
}

test('splitLinkRefs：四种引用一起切分，原文无损', () => {
  const text = '任务 #36 已答 Q23，见 [[design/preview-view.md#8-14]] 与 sess-9f02517c。'
  const segs = splitLinkRefs(text, EVERYTHING)
  assert.equal(segs.map(s => s.text).join(''), text)
  assert.deepEqual(
    segs.map(s => [s.ref?.kind, s.shown ?? s.text]),
    [
      [undefined, '任务 '],
      ['task', '#36'],
      [undefined, ' 已答 '],
      ['confirm', 'Q23'],
      [undefined, '，见 '],
      ['doc', 'design/preview-view.md#8-14'],
      [undefined, ' 与 '],
      ['session', 'sess-9f02517c'],
      [undefined, '。'],
    ]
  )
  const doc = segs.find(s => s.ref?.kind === 'doc')?.ref
  assert.deepEqual(doc, { kind: 'doc', key: 'design/preview-view.md', anchor: '8-14', title: '预览视图' })
})

test('splitLinkRefs：目标不存在 / 索引为空时一律不链接，保持单个普通片段', () => {
  const text = '任务 #36 已答 Q23，见 [[design/preview-view.md]] 与 sess-9f02517c。'
  assert.deepEqual(splitLinkRefs(text, index()), [{ text }])
  assert.deepEqual(splitLinkRefs('', EVERYTHING), [])
  assert.deepEqual(splitLinkRefs('没有任何引用', EVERYTHING), [{ text: '没有任何引用' }])
  // 只有一部分存在：只链接存在的
  assert.deepEqual(refs('#36 #99 Q23 Q99 [[nope.md]] sess-0000aaaa'), [
    ['task', '36', '#36'],
    ['confirm', 'Q23', 'Q23'],
  ])
})

test('确认项 Q<N>：只认大写 Q + 不带前导 0 的整数，前后不能贴着词字符', () => {
  for (const [text, want] of [
    ['Q5', ['Q5']],
    ['（Q5）', ['Q5']],
    ['【确认 Q5】已答', ['Q5']],
    ['Q5。', ['Q5']],
    ['Q5，Q6', ['Q5', 'Q6']],
    ['Q5-Q6', ['Q5', 'Q6']],
    ['Q5/Q6', ['Q5']], // 斜杠后面的 Q6 像路径，不链接
    ['答复Q5已收到', ['Q5']], // 中文紧贴
    ['\nQ23\n', ['Q23']],
  ] as Array<[string, string[]]>) {
    assert.deepEqual(refs(text).map(r => r[2]), want, text)
  }
  for (const text of [
    'AQ5', // 前面是字母
    'iQ5',
    'Q05', // 前导 0
    'Q0',
    'Q5a', // 后面是词字符
    'Q5_x',
    'Q1.5', // 小数
    'q5', // 小写
    'Q1234567', // 超过 6 位
    'a/Q5', // 路径
    '&Q5',
    '#Q5',
    'x.Q5',
    '5Q5',
  ]) {
    assert.deepEqual(refs(text), [], text)
  }
})

test('会话号 sess-xxxxxxxx：6–12 位十六进制，前后不能再贴着字母 / 数字 / 下划线 / 连字符', () => {
  assert.deepEqual(refs('见 sess-9f02517c。'), [['session', 'sess-9f02517c', 'sess-9f02517c']])
  assert.deepEqual(refs('（sess-9f02517c）'), [['session', 'sess-9f02517c', 'sess-9f02517c']])
  assert.deepEqual(refs('SESS-9F02517C'), [['session', 'sess-9f02517c', 'SESS-9F02517C']], '大小写不敏感，查找用小写')
  for (const text of [
    'xsess-9f02517c', // 前面贴着字母
    '1sess-9f02517c',
    '-sess-9f02517c',
    'sess-9f02517c-extra', // 后面是连字符
    'sess-9f02517cxyz', // 后面是字母
    'sess-9f02517c_1',
    'sess-9f025', // 不到 6 位
    'sess-9f02517cdd', // 位数对但目标不存在
  ]) {
    assert.deepEqual(refs(text), [], text)
  }
})

test('parseDocRef：规则与服务端 ExtractLinks / NormalizePath 一致', () => {
  assert.deepEqual(parseDocRef('design/x.md'), { path: 'design/x.md', anchor: '', label: '', written: 'design/x.md' })
  assert.equal(parseDocRef('design/x')?.path, 'design/x.md', '没有 .md 后缀就补上')
  assert.equal(parseDocRef('Design/X.MD')?.path, 'Design/X.MD', '后缀大小写不敏感，路径本身区分大小写')
  assert.deepEqual(parseDocRef('design/x.md#8-14'), { path: 'design/x.md', anchor: '8-14', label: '', written: 'design/x.md#8-14' })
  assert.deepEqual(parseDocRef(' design/x.md | 显示名 '), { path: 'design/x.md', anchor: '', label: '显示名', written: 'design/x.md' })
  assert.deepEqual(parseDocRef('design/x.md#a|显示名'), { path: 'design/x.md', anchor: 'a', label: '显示名', written: 'design/x.md#a' })
  assert.equal(parseDocRef('design/x.md?v=1')?.path, 'design/x.md', '查询串去掉')
  assert.equal(parseDocRef('design\\x.md')?.path, 'design/x.md', '反斜杠当斜杠')
  assert.equal(parseDocRef('./design//x.md')?.path, 'design/x.md', '折叠重复斜杠与 ./')
  assert.equal(parseDocRef('a/../b.md')?.path, 'b.md', '.. 抵消前一段，与 path.Clean 一致')
  assert.equal(parseDocRef('v1.2 说明')?.path, undefined, '带非 .md 扩展名的不是文档（与 path.Ext 一致）')
  for (const bad of ['', '  ', '|只有显示名', 'artifact:12', 'Artifact:12', 'https://x.test/a', 'mailto:a@b.c', '/abs/x.md', 'a.png', 'a/b.pdf', '../x.md', 'a/../../b.md', 'a..b', '#只有锚点']) {
    assert.equal(parseDocRef(bad), null, JSON.stringify(bad))
  }
})

test('parseDocRef：./ 与 ../ 开头的相对写法按所在文档的目录解析，其余仍按工作流根目录', () => {
  assert.equal(parseDocRef('./b', 'design/a.md')?.path, 'design/b.md')
  assert.equal(parseDocRef('../c.md', 'design/a.md')?.path, 'c.md')
  assert.equal(parseDocRef('../../c.md', 'design/a.md'), null, '退出根目录之外不是文档')
  assert.equal(parseDocRef('./b', 'a.md')?.path, 'b.md', '根文档里 ./ 就是根目录')
  assert.equal(parseDocRef('design/x', 'design/a.md')?.path, 'design/x.md', '不带 ./ 的写法仍按根目录（与服务端 wiki 链接一致）')
  assert.equal(parseDocRef('../c.md'), null, '没有所在文档时 ../ 无从解析')
})

test('文档 [[路径]]：显示文字去掉方括号 / 取显示名；目标不存在或不是文档就保持原样', () => {
  assert.deepEqual(refs('见 [[design/x.md]]'), [['doc', 'design/x.md', 'design/x.md']])
  assert.deepEqual(refs('见 [[design/x]]'), [['doc', 'design/x.md', 'design/x']])
  assert.deepEqual(refs('见 [[design/x.md|设计 X]]'), [['doc', 'design/x.md', '设计 X']])
  assert.deepEqual(refs('见 [[x.md#章节]]'), [['doc', 'x.md', 'x.md#章节']])
  assert.deepEqual(refs('见 [[./design/x.md]]'), [['doc', 'design/x.md', './design/x.md']])
  assert.deepEqual(refs('[[./b]]', EVERYTHING, 'design/a.md'), [['doc', 'design/b.md', './b']])
  assert.deepEqual(refs('[[../c.md]]', EVERYTHING, 'design/a.md'), [['doc', 'c.md', '../c.md']])
  for (const text of ['[[artifact:12]]', '[[https://x.test/a.md]]', '[[/abs.md]]', '[[a.png]]', '[[ ]]', '[[a..b]]', '[[nope.md]]', '[x.md]', '[[x.md]', '[[\nx.md]]']) {
    assert.deepEqual(refs(text), [], text)
  }
  // 原文无损：显示名不同于原文时，text 仍是方括号里的整段原文
  const seg = splitLinkRefs('见 [[design/x.md|设计 X]]。', EVERYTHING).find(s => s.ref)
  assert.equal(seg?.text, '[[design/x.md|设计 X]]')
  assert.equal(seg?.shown, '设计 X')
})

test('互相重叠的引用：先出现的赢，[[文档#Q3]] 整体是文档，里面的 Q3 不再单独成链接', () => {
  assert.deepEqual(refs('见 [[design/x.md|参见 Q3]] 与 Q23'), [
    ['doc', 'design/x.md', '参见 Q3'],
    ['confirm', 'Q23', 'Q23'],
  ])
  assert.deepEqual(refs('[[design/x.md#sess-9f02517c]]'), [['doc', 'design/x.md', 'design/x.md#sess-9f02517c']])
  // 文档不存在时 [[ ]] 里面的内容回到各自的规则：md#36 里的 #36 前面是词字符，任务规则本来就不放行
  assert.deepEqual(refs('[[nope.md#36]]'), [])
  assert.deepEqual(refs('[[nope.md|见 Q23]]'), [['confirm', 'Q23', 'Q23']])
})

test('wholeLinkRef：整段文字恰好是一个引用才返回（行内代码用）', () => {
  assert.equal(wholeLinkRef('#36', EVERYTHING)?.ref?.key, '36')
  assert.equal(wholeLinkRef(' Q23 ', EVERYTHING)?.ref?.key, 'Q23')
  assert.equal(wholeLinkRef('[[design/x.md]]', EVERYTHING)?.ref?.key, 'design/x.md')
  assert.equal(wholeLinkRef('sess-9f02517c', EVERYTHING)?.ref?.key, 'sess-9f02517c')
  assert.equal(wholeLinkRef('make #36', EVERYTHING), null)
  assert.equal(wholeLinkRef('#36 和 Q23', EVERYTHING), null)
  assert.equal(wholeLinkRef('', EVERYTHING), null)
  assert.equal(wholeLinkRef('#99', EVERYTHING), null)
})

test('linkRefHref：每种引用的真实深链地址（站内相对路径，参数转义）', () => {
  const wf = 'relaymesh-continuation'
  const href = (ref: LinkRef) => linkRefHref(ref, wf)
  // 任务引用里的 36 是板内序号 #36，地址带 ?seq=（由服务端解析成任务）；写成 ?task=36 会把它当数据库编号、打开别的任务（#242）
  assert.equal(href({ kind: 'task', key: '36' }), '/work/relaymesh-continuation?seq=36')
  assert.ok(!href({ kind: 'task', key: '36' }).includes('task='), '任务引用的深链不能用 ?task=（那是数据库编号）')
  assert.equal(href({ kind: 'confirm', key: 'Q23' }), '/?wf=relaymesh-continuation&q=Q23')
  assert.equal(href({ kind: 'session', key: 'sess-9f02517c' }), '/?wf=relaymesh-continuation&s=sess-9f02517c')
  assert.equal(href({ kind: 'doc', key: 'design/x.md' }), '/docs/relaymesh-continuation/design/x.md')
  assert.equal(href({ kind: 'doc', key: '设计/说明.md', anchor: '8 节' }), `/docs/relaymesh-continuation/${encodeURIComponent('设计')}/${encodeURIComponent('说明.md')}#${encodeURIComponent('8 节')}`)
  assert.equal(linkRefHref({ kind: 'task', key: '1' }, 'a b&c'), '/work/a%20b%26c?seq=1')
})

test('linkRefHint：悬停说明写类型 + 引用，有标题就补标题（压缩空白、过长截断）', () => {
  assert.equal(linkRefHint({ kind: 'task', key: '36' }, '#36'), '查看任务 #36')
  assert.equal(linkRefHint({ kind: 'task', key: '36', title: '任务\n三十六' }, '#36'), '查看任务 #36 · 任务 三十六')
  assert.equal(linkRefHint({ kind: 'confirm', key: 'Q23', title: '要不要做' }, 'Q23'), '查看确认项 Q23 · 要不要做')
  assert.equal(linkRefHint({ kind: 'session', key: 'sess-9f02517c' }, 'sess-9f02517c'), '跳到这一轮 sess-9f02517c')
  assert.equal(linkRefHint({ kind: 'doc', key: 'design/x.md', anchor: '8-14', title: '设计 X' }, '显示名'), '打开文档 design/x.md#8-14 · 设计 X')
  const long = linkRefHint({ kind: 'task', key: '1', title: 'x'.repeat(200) }, '#1')
  assert.ok(long.endsWith('…') && long.length < 120, long)
})

test('linkRefClass：每种引用都有 link-ref + link-ref-<kind>，只有任务多带历史名 task-ref', () => {
  assert.equal(linkRefClass('task'), 'link-ref link-ref-task task-ref')
  assert.equal(linkRefClass('confirm'), 'link-ref link-ref-confirm')
  assert.equal(linkRefClass('doc'), 'link-ref link-ref-doc')
  assert.equal(linkRefClass('session'), 'link-ref link-ref-session')
})

// ---- #186 C2a：引用索引在 store 里共用一份，不在每段 Markdown 的 useLinkRefs 实例里各建一份 ----
test('buildSeqIndex：按序号建索引，序号相同后面的覆盖前面的，最大序号至少为 0', () => {
  const a = { seq: 3, t: 'a' }
  const b = { seq: 11, t: 'b' }
  const c = { seq: 3, t: 'c' }
  const idx = buildSeqIndex([a, b, c])
  assert.equal(idx.bySeq.size, 2)
  assert.equal(idx.bySeq.get(3), c)
  assert.equal(idx.bySeq.get(11), b)
  assert.equal(idx.maxSeq, 11)
  const empty = buildSeqIndex([])
  assert.equal(empty.bySeq.size, 0)
  assert.equal(empty.maxSeq, 0)
})

test('结构守卫：useLinkRefs 不再自己扫整张会话表 / 确认项表；共用的索引由两个 store 提供', () => {
  const read = (rel: string) => readFileSync(resolve(process.cwd(), rel), 'utf8')
  const code = (src: string) => src.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '') // 去掉注释再查，免得说明文字里的词误伤
  const composable = code(read('src/composables/useLinkRefs.ts'))
  assert.ok(!/currentWorkflowSessions/.test(composable), 'useLinkRefs 不应遍历 currentWorkflowSessions（每个实例一份是 O(实例 × 行)）')
  assert.ok(!/timelineItems/.test(composable), 'useLinkRefs 不应遍历 timelineItems')
  assert.ok(!/replyHash\(/.test(composable), 'useLinkRefs 不应自己逐行算 replyHash')
  assert.match(composable, /sessionStore\.sessionByHash/)
  assert.match(composable, /confirmStore\.timelineSeqIndex/)
  const sessionStore = code(read('src/stores/session.ts'))
  assert.match(sessionStore, /const sessionByHash = computed\(\(\) => buildSessionHashIndex\(currentWorkflowSessions\.value\)\)/)
  assert.match(sessionStore, /^\s+sessionByHash,$/m, 'store 要把 sessionByHash 导出')
  const confirmStore = code(read('src/stores/confirms.ts'))
  assert.match(confirmStore, /const timelineSeqIndex = computed\(\(\) => buildSeqIndex\(timelineItems\.value\)\)/)
  assert.match(confirmStore, /^\s+timelineSeqIndex,$/m, 'store 要把 timelineSeqIndex 导出')
})
