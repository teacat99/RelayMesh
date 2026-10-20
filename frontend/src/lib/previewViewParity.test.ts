// 运行：pnpm --dir frontend run test:unit
// 预览视图（#120）与经典会话列表的对等检查（源码级）。
//
// 预览视图 = 预览列表（components/feedback/preview-view/PreviewMessageList.vue）+ 同目录下它用到的卡片组件
// （阶段 C 起，汇报 / 回复 / 追加 / 附件各抽成了自己的组件）；经典则是一个文件（FeedbackMessageList.vue）。
// 阶段 1 起预览的版式与经典不同（紧凑行、统一消息卡、竖图底色），但「经典有的信息，预览必须有」不变：这里查的是信息，不是外观。
// 复刻最怕悄悄漂移：经典里新增了一种条目、一个子组件或一个数据来源，预览却没跟上，用户在预览里就少了东西。
// 这里按「经典有的，预览必须有」检查四件事：对外接口（props / emits / 暴露的方法，只看列表本身，FeedbackView 要能原样换用）、
// data-testid、导入的模块与名字（子组件、store、lib 都从这里进来）、模板里用到的组件；
// 后三件在「列表 + 它的所有卡片组件」的并集里找。图标不按导入名查（导入了没用到的图标不是内容），而是按模板里的用法查（组件那一项）。
// 预览刻意不要的东西写进 EXEMPT，并写明原因；加豁免是一次明示的决定，不是顺手绕过测试。
import assert from 'node:assert/strict'
import { existsSync, readdirSync, readFileSync } from 'node:fs'
import { basename, dirname, join, resolve } from 'node:path'
import { describe, it } from 'node:test'

function srcDir(): string {
  for (const base of [join(process.cwd(), 'src'), join(process.cwd(), 'frontend', 'src')]) {
    if (existsSync(join(base, 'lib', 'previewViewParity.test.ts'))) return base
  }
  throw new Error('找不到 frontend/src，请在仓库根目录或 frontend 目录下运行测试')
}

const CLASSIC = 'components/feedback/FeedbackMessageList.vue'
const PREVIEW = 'components/feedback/preview-view/PreviewMessageList.vue'
/** 图标库：导入名不查（见文件头），用到没用到看模板里的 <图标>。 */
const ICON_MODULE = 'lucide-vue-next'

/** 预览刻意没有的东西（键 = 经典里的写法，值 = 原因）。 */
export interface Exempt {
  testids: Record<string, string>
  /** 键写 "模块的绝对路径或包名#导入的名字"，默认导入写 default。 */
  imports: Record<string, string>
  components: Record<string, string>
}
// 阶段 2（#120）：预览用自己的右侧轨（PreviewTimelineRail，按真实位置等比缩放、含任务 / 卡片 / 提问等所有条目）
// 取代经典的时间微缩轴 TimelineScrubber（只有轮次、等距排布），所以这一个导入和这一个组件是刻意不带的。
const SCRUBBER_WHY = '预览用右侧轨 PreviewTimelineRail 取代经典的 TimelineScrubber（#120 阶段 2）'
// 阶段 C（#120 · D-273）：汇报 / 回复 / 追加换成统一消息卡，以下几个模板组件随旧版式一起退场。
const AVATAR_WHY = '统一消息卡的头像（智 / 您 / 系）取代了经典头部里的 User 图标（#120 阶段 C）'
const TRASH_WHY = '「删除此轮」收进汇报卡的「⋯」菜单（仍是两段式确认），不再有带垃圾桶图标的独立按钮（#120 阶段 C）'
const INBOX_REUSE_WHY = '预览的追加卡是独立组件 PreviewInboxCard，不再用 createReusableTemplate 在列表里定义一次再复用（#120 阶段 C）'
const EXEMPT: Exempt = {
  testids: {},
  imports: { [`${join(srcDir(), 'components', 'TimelineScrubber.vue')}#default`]: SCRUBBER_WHY },
  components: {
    TimelineScrubber: SCRUBBER_WHY,
    User: AVATAR_WHY,
    Trash2: TRASH_WHY,
    DefineInboxBubble: INBOX_REUSE_WHY,
    ReuseInboxBubble: INBOX_REUSE_WHY,
  },
}

function stripHtmlComments(s: string): string {
  return s.replace(/<!--[\s\S]*?-->/g, '')
}

function scriptOf(src: string): string {
  const a = src.indexOf('<script')
  const b = src.indexOf('</script>')
  assert.ok(a >= 0 && b > a, '找不到 <script>')
  return src.slice(src.indexOf('>', a) + 1, b)
}

function templateOf(src: string): string {
  const i = src.indexOf('\n<template>')
  assert.ok(i >= 0, '找不到顶层 <template>')
  return stripHtmlComments(src.slice(i))
}

/** data-testid 的取值；动态的模板字符串把 ${…} 写成 *。 */
export function testidsOf(src: string): Set<string> {
  const out = new Set<string>()
  const re = /(:?)data-testid=(?:"([^"]*)"|'([^']*)')/g
  const t = templateOf(src)
  let m: RegExpExecArray | null
  while ((m = re.exec(t)) !== null) {
    const dynamic = m[1] === ':'
    const raw = (m[2] ?? m[3] ?? '').trim()
    if (!dynamic) out.add(raw)
    else out.add(raw.replace(/^`|`$/g, '').replace(/\$\{[^}]*\}/g, '*'))
  }
  return out
}

/** 模板里用到的 PascalCase 组件（含 lucide 图标）。 */
export function componentsOf(src: string): Set<string> {
  const out = new Set<string>()
  const re = /<([A-Z][A-Za-z0-9]*)\b/g
  const t = templateOf(src)
  let m: RegExpExecArray | null
  while ((m = re.exec(t)) !== null) out.add(m[1])
  return out
}

/** 静态导入的「模块#名字」集合；相对路径按文件所在目录解析成绝对路径，这样两个目录下的写法能对上。 */
export function importsOf(src: string, fileDir: string): Set<string> {
  const out = new Set<string>()
  const body = scriptOf(src)
  const re = /^import\s+(type\s+)?([\s\S]*?)\s+from\s+'([^']+)'/gm
  let m: RegExpExecArray | null
  while ((m = re.exec(body)) !== null) {
    const spec = m[3]
    const mod = spec.startsWith('.') ? resolve(fileDir, spec) : spec
    const clause = m[2].trim()
    const named = clause.match(/\{([\s\S]*)\}/)
    const head = clause.replace(/\{[\s\S]*\}/, '').replace(/,\s*$/, '').trim()
    if (head) out.add(`${mod}#default`)
    if (named) {
      for (const part of named[1].split(',')) {
        const name = part.trim().replace(/^type\s+/, '').split(/\s+as\s+/)[0].trim()
        if (name) out.add(`${mod}#${name}`)
      }
    }
  }
  return out
}

/** 对外接口：props 名、emits 名、defineExpose 暴露的名字。 */
export function interfaceOf(src: string): { props: string[]; emits: string[]; expose: string[] } {
  const body = scriptOf(src)
  const p = body.indexOf('defineProps<{')
  const pe = body.indexOf('}>()', p)
  assert.ok(p >= 0 && pe > p, '找不到 defineProps')
  const props = [...body.slice(p, pe).matchAll(/^ {2}([A-Za-z_][A-Za-z0-9_]*)\??\s*:/gm)].map(x => x[1])
  const e = body.indexOf('defineEmits<{')
  const ee = body.indexOf('}>()', e)
  assert.ok(e >= 0 && ee > e, '找不到 defineEmits')
  const emits = [...body.slice(e, ee).matchAll(/\(e:\s*'([^']+)'/g)].map(x => x[1])
  const x = body.indexOf('defineExpose({')
  const xe = body.indexOf('})', x)
  assert.ok(x >= 0 && xe > x, '找不到 defineExpose')
  const expose = body
    .slice(x + 'defineExpose({'.length, xe)
    .split(',')
    .map(s => s.trim())
    .filter(Boolean)
  return { props: props.sort(), emits: emits.sort(), expose: expose.sort() }
}

/** 预览视图的组成：列表本身，加上同目录下的所有 .vue 卡片组件（按文件名索引，方便负向对照改其中一个）。 */
export function previewParts(base: string): { list: string; parts: Record<string, string> } {
  const dir = dirname(join(base, PREVIEW))
  const parts: Record<string, string> = {}
  for (const f of readdirSync(dir).filter(n => n.endsWith('.vue') && n !== basename(PREVIEW)).sort()) {
    parts[f] = readFileSync(join(dir, f), 'utf8')
  }
  return { list: readFileSync(join(base, PREVIEW), 'utf8'), parts }
}

/**
 * 经典有、预览没有、又不在豁免里的东西，逐条写成一句话。
 * 对外接口只比列表（FeedbackView 直接用它）；data-testid / 导入 / 组件在「列表 + 卡片组件」的并集里找。
 */
export function parityViolations(
  classicSrc: string,
  previewListSrc: string,
  previewPartSrcs: string[],
  classicDir: string,
  previewDir: string,
  exempt: Exempt
): string[] {
  const out: string[] = []
  const ci = interfaceOf(classicSrc)
  const pi = interfaceOf(previewListSrc)
  for (const key of ['props', 'emits', 'expose'] as const) {
    if (JSON.stringify(ci[key]) !== JSON.stringify(pi[key])) {
      out.push(`对外接口 ${key} 不一致：经典 [${ci[key].join(', ')}]，预览 [${pi[key].join(', ')}]（FeedbackView 要能原样换用）`)
    }
  }
  const all = [previewListSrc, ...previewPartSrcs]
  const pt = new Set(all.flatMap(s => [...testidsOf(s)]))
  for (const id of testidsOf(classicSrc)) {
    if (!pt.has(id) && !(id in exempt.testids)) out.push(`经典有 data-testid="${id}"，预览没有`)
  }
  const pImp = new Set(all.flatMap(s => [...importsOf(s, previewDir)]))
  for (const imp of importsOf(classicSrc, classicDir)) {
    if (imp.startsWith(`${ICON_MODULE}#`)) continue
    if (!pImp.has(imp) && !(imp in exempt.imports)) out.push(`经典导入了 ${imp.replace(classicDir, '.')}，预览没有`)
  }
  const pc = new Set(all.flatMap(s => [...componentsOf(s)]))
  for (const c of componentsOf(classicSrc)) {
    if (!pc.has(c) && !(c in exempt.components)) out.push(`经典模板里用了 <${c}>，预览没有`)
  }
  return out
}

describe('预览视图与经典会话列表对等', () => {
  const base = srcDir()
  const classic = readFileSync(join(base, CLASSIC), 'utf8')
  const { list: preview, parts } = previewParts(base)
  const classicDir = dirname(join(base, CLASSIC))
  const previewDir = dirname(join(base, PREVIEW))
  const run = (list: string, partSrcs: Record<string, string> = parts) =>
    parityViolations(classic, list, Object.values(partSrcs), classicDir, previewDir, EXEMPT)
  const withPart = (name: string, src: string): Record<string, string> => ({ ...parts, [name]: src })

  it('检查器自己有数据可查（经典列表的接口、testid、导入、组件都读得到；预览的卡片组件都找得到）', () => {
    const iface = interfaceOf(classic)
    assert.ok(iface.props.includes('conversationRounds') && iface.emits.includes('revoke-session'))
    assert.deepEqual(iface.expose, ['scrollByDelta', 'scrollToBottom', 'scrollToSession'])
    const ids = testidsOf(classic)
    for (const id of ['turn-group', 'timeline-tail', 'in-round-gap', 'anchor-divider', 'inbox-item-*']) assert.ok(ids.has(id), `读不到 ${id}`)
    assert.ok(importsOf(classic, classicDir).size > 30)
    const comps = componentsOf(classic)
    for (const c of ['CardStack', 'MarkdownRenderer', 'SessionTasks']) assert.ok(comps.has(c), `读不到 <${c}>`)
    for (const f of [
      'PreviewAttachments.vue', 'PreviewCardMenu.vue', 'PreviewConfirmCard.vue', 'PreviewInboxCard.vue', 'PreviewMessageCard.vue',
      'PreviewNoticeCard.vue', 'PreviewReplyCard.vue', 'PreviewReportCard.vue', 'PreviewTimelineRail.vue',
    ]) assert.ok(f in parts, `预览目录里找不到 ${f}`)
  })

  it('经典有的接口、data-testid、导入与组件，预览都有（刻意没有的写进 EXEMPT 并写明原因）', () => {
    const v = run(preview)
    assert.deepEqual(v, [], '预览视图落后于经典：\n' + v.join('\n'))
  })

  it('豁免都要写原因，并且仍然对应经典里真实存在的东西', () => {
    const ids = testidsOf(classic)
    const imps = importsOf(classic, classicDir)
    const comps = componentsOf(classic)
    for (const [k, why] of Object.entries(EXEMPT.testids)) assert.ok(why.trim() && ids.has(k), `过期的豁免 testid：${k}`)
    for (const [k, why] of Object.entries(EXEMPT.imports)) assert.ok(why.trim() && imps.has(k), `过期的豁免导入：${k}`)
    for (const [k, why] of Object.entries(EXEMPT.components)) assert.ok(why.trim() && comps.has(k), `过期的豁免组件：${k}`)
  })

  it('负向对照：预览少一个 testid / 少一个导入 / 少一个组件 / 少一个暴露方法，检查器都能发现', () => {
    const drop = (s: string, from: string, to = '') => {
      assert.ok(s.includes(from), `样本里找不到 ${from}`)
      return s.replace(from, to)
    }
    assert.ok(run(drop(preview, 'data-testid="timeline-tail"')).some(x => x.includes('timeline-tail')))
    assert.ok(run(drop(preview, "import CardStack from '../CardStack.vue'\n")).some(x => x.includes('CardStack')))
    assert.ok(run(drop(preview, "import { useWorkItemStore } from '../../../stores/workItems'\n")).some(x => x.includes('useWorkItemStore')))
    assert.ok(run(drop(preview, '<SessionTasks')).some(x => x.includes('SessionTasks')))
    assert.ok(run(drop(preview, '  scrollToSession\n})', '})')).some(x => x.includes('expose')))
    // 豁免能让对应的差异过关
    const exempt: Exempt = { testids: { 'timeline-tail': '示例豁免' }, imports: EXEMPT.imports, components: EXEMPT.components }
    assert.deepEqual(
      parityViolations(classic, drop(preview, 'data-testid="timeline-tail"'), Object.values(parts), classicDir, previewDir, exempt),
      []
    )
  })

  it('负向对照：抽进卡片组件里的东西同样受检——卡片里少一个 testid / 导入 / 组件，检查器也能发现', () => {
    const drop = (s: string, from: string, to = '') => {
      assert.ok(s.includes(from), `样本里找不到 ${from}`)
      return s.replace(from, to)
    }
    // testid：追加卡的撤回按钮（只有这一个组件有）
    const inbox = parts['PreviewInboxCard.vue']
    assert.ok(run(preview, withPart('PreviewInboxCard.vue', drop(inbox, 'inbox-retract-'))).some(x => x.includes('inbox-retract-*')))
    // 导入：交付锚点芯片只有汇报卡导入
    const report = parts['PreviewReportCard.vue']
    assert.ok(run(preview, withPart('PreviewReportCard.vue', drop(report, "import AnchorChip from '../AnchorChip.vue'\n"))).some(x => x.includes('AnchorChip')))
    // 组件：旋转图标（下载中的附件缩略图、搜索中的面板都在用）——并集里所有卡片组件都不用它了，检查器要能发现
    //（不能只改某一个组件：别的预览专属组件也可能用同一个图标，那样「少一个」就不成立了）
    assert.ok(Object.values(parts).some(src => src.includes('<Loader2')), '样本里应有组件用到 <Loader2')
    const noSpinner = Object.fromEntries(Object.entries(parts).map(([name, src]) => [name, src.replaceAll('<Loader2', '<NoSuchIcon')]))
    assert.ok(run(preview, noSpinner).some(x => x.includes('<Loader2>')))
    // 整个组件文件不在了：它独家承担的导入（回复卡的 formatClock / receivedTitle）会一起冒出来
    const { 'PreviewReplyCard.vue': gone, ...rest } = parts
    assert.ok(gone, '样本里找不到回复卡')
    const v = run(preview, rest)
    assert.ok(v.some(x => x.includes('formatClock')) && v.some(x => x.includes('receivedTitle')), '少了回复卡应该有对应的差异：' + v.join(' | '))
  })

  it('图标不按导入名查：经典导入了没用到的图标，预览不必跟着导入', () => {
    const iconImports = [...importsOf(classic, classicDir)].filter(i => i.startsWith(`${ICON_MODULE}#`))
    assert.ok(iconImports.length > 0, '样本里应有图标导入')
    // 预览自己的导入里一个都没有也不报（只要模板里该用的图标用到了）
    const mine = [...importsOf(preview, previewDir)].filter(i => i.startsWith(`${ICON_MODULE}#`))
    assert.ok(mine.length < iconImports.length, '预览的图标导入应比经典少（卡片组件各管各的图标）')
    assert.ok(!run(preview).some(x => x.includes(ICON_MODULE)))
  })
})
