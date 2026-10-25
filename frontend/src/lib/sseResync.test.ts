// 运行：pnpm --dir frontend run test:unit
// 「实时流没听见的事件，要靠重连后重取来补」的源码级防线（#204）。
//
// 背景：服务端的 conversation_changed 是立即发的、conversation_updated 去抖约 150ms 后发，都不补发。页面载入后要先取一次列表、
// 再换票据建立实时流，这中间（以及断线期间）发生的消息没有任何订阅者听见。session.ts 的 onopen 早就为 Agent 状态、通知偏好、
// 工作项、确认项整表校准过，唯独聊天与群聊没有：群列表停在事件之前的旧数据上，侧栏的「@我」直到下一个事件才会出现；打开着的群窗口同理，
// 断线期间别人发的消息要等下一个事件才补上。压力下 e2e 偶发的「侧栏 15 秒没出现 @我」就是它（group-chat.mjs 的 4b / 4c / 4d 用请求拦截，
// 把这几种先后顺序变成确定的）。
// 第二个洞在 groups 仓库里：onServerEvent 只在 loaded 为真时才排重取，而第一次响应落下来之前 loaded 一直是假的——这段时间里到的事件被丢掉，
// 随后落下来的第一次响应又是事件之前算的旧数据（取证时的现场：两个事件都到了，仓库 loaded=true、列表里 mentioned=0，侧栏就是不更新）。
// #243 把同一个洞补全：onopen 里原来有一段「重连才做」的整表校准（hasConnectedOnce 分支，注释说「首次连接没有这个问题」），
// 其实首次连接有同样的缺口——页面载入后几百毫秒内的改动（e2e 里 preset-versions 在 0.46–0.75 秒改设置、实时流 0.37–0.41 秒才连上）
// 与设置、托管自驾任务这两类压根没有校准。现在首次与重连走同一个 resyncAfterConnect()；任务清单、确认项与惰性加载的仓库经 lib/connectHooks.ts 登记，
// 连上时只补取已经被用过的：直接 use…Store() 会把设置页这类用不到它们的页面也实例化出来、白发请求，惰性的三个还会为此在启动时拉代码块。
// 整张会话表首次连上不无条件再取（服务端代价与完整下载相同，load-budget 钉着「深链接页整表恰好 1 次」），而是等刚取回的会话列表到齐、
// 核对整表有没有落后于它（utils/sessionSync.ts 的 isWorkflowTableBehind，有行为单测；reconcileWorkflowTable 接线），落后了才取一次——
// 这是 #243 最初登记的现象：侧栏更新了、会话页的对话还停在旧的。
// 行为靠 e2e 守住（sse-first-connect.mjs 用 CDP 扣住实时流请求，把「连上之前的改动」做成确定的先后顺序），这里只守住接线，防止退回旧写法。
import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, it } from 'node:test'

function srcDir(): string {
  for (const base of [join(process.cwd(), 'src'), join(process.cwd(), 'frontend', 'src')]) {
    if (existsSync(join(base, 'lib', 'sseResync.test.ts'))) return base
  }
  throw new Error('找不到 frontend/src，请在仓库根目录或 frontend 目录下运行测试')
}
const read = (rel: string) => readFileSync(join(srcDir(), rel), 'utf8')

/** 取出 `function name(...) {` 到与它配对的右花括号之间的函数体。 */
function bodyOf(src: string, signature: RegExp): string {
  const m = signature.exec(src)
  assert.ok(m, `找不到 ${signature}`)
  const open = src.indexOf('{', m.index + m[0].length - 1)
  let depth = 0
  for (let i = open; i < src.length; i++) {
    if (src[i] === '{') depth++
    else if (src[i] === '}' && --depth === 0) return src.slice(open + 1, i)
  }
  throw new Error(`${signature} 的花括号没有配对`)
}

/** session.ts 里 eventSource.onopen 回调的函数体文本（到 onerror 之前）。 */
function onopenOf(session: string): string {
  const start = session.indexOf('eventSource.onopen = () => {')
  assert.ok(start >= 0, '找不到 eventSource.onopen')
  const end = session.indexOf('eventSource.onerror', start)
  return session.slice(start, end)
}

describe('实时流（重）连上：聊天与群聊也要整表校准', () => {
  const session = read('stores/session.ts')

  it('session.ts 的 onopen 里调用聊天与群聊仓库的 onConnected', () => {
    const onopen = onopenOf(session)
    assert.match(onopen, /useChatStore\(\)\.onConnected\(\)/, '连上时要让聊天仓库补取打开着的会话、重取未读数')
    assert.match(onopen, /useGroupStore\(\)\.onConnected\(\)/, '连上时要让群聊仓库重取群列表')
  })
})

describe('实时流（首次或重新）连上：其余仓库同样补取（#243）', () => {
  const session = read('stores/session.ts')

  it('onopen 不再按「首次 / 重连」分支：补取无条件执行，区分只发生在 resyncAfterConnect 里', () => {
    const onopen = onopenOf(session)
    assert.doesNotMatch(session, /hasConnectedOnce/, '页面载入到首次连上之间的事件同样没人听见，不能只在重连时校准')
    assert.doesNotMatch(onopen, /\bif\s*\(/, 'onopen 里不要再按「首次 / 重连」分支：补取要在每次连上都做')
    assert.match(onopen, /const current = fetchCurrentSession\(\)/, '当前会话的取数要留住 promise，首次连上核对整表要等它')
    assert.match(onopen, /const listed = fetchSessions\(\)/, '会话列表的取数要留住 promise，首次连上核对整表要等它')
    assert.match(onopen, /\n\s+resyncAfterConnect\(reconnected, Promise\.allSettled\(\[current, listed\]\)\)\n/, 'onopen 要调用 resyncAfterConnect(reconnected, 列表到齐的 promise)')
  })

  it('整表：重连时强制重取；首次连上等会话列表到齐、只在整表落后时才取（服务端代价与完整下载相同，常态不多一个请求）', () => {
    const body = bodyOf(session, /function resyncAfterConnect\([^)]*\)[^{]*\{/)
    const reconnectOnly = bodyOf(body, /if \(reconnected\) \{/)
    assert.match(reconnectOnly, /loadWorkflowSessionsIfShown\(wId, true\)/, '重连时选中工作流的整表要强制校准')
    assert.match(body, /\} else \{\s*void listsLanded\.then\(reconcileWorkflowTable\)\s*\}/, '首次连上：等会话列表到齐再核对整表')
    const rest = body.replace(reconnectOnly, '')
    assert.doesNotMatch(rest, /loadWorkflowSessionsIfShown/, '整表不能无条件强制重取：首次连上会让深链接页整表取 2 次（load-budget 的预算是恰好 1 次）')
    assert.doesNotMatch(rest.replace('if (reconnected) {', ''), /\bif\s*\(/, '其余补取不要再加条件分支：首次连上与重连一样要做')
  })

  it('reconcileWorkflowTable：整表还在途先等它落地、不另发；只在落后于列表时才强制重取', () => {
    const body = bodyOf(session, /async function reconcileWorkflowTable\(\)\s*\{/)
    assert.match(body, /if \(workflowFlight\.isBusy\(wId\)\) await workflowFlight\.request\(wId\)/, '在途的整表要等它落地（不带 fresh，加入在途、不新发一次）')
    assert.doesNotMatch(body, /workflowFlight\.request\(wId, true\)/, '核对不能再发起一次新的整表请求')
    assert.match(body, /\(s\.workflow_id \|\| s\.session_id\) === wId/, '只看属于这个工作流的行')
    assert.match(body, /if \(isWorkflowTableBehind\(table, rows\)\) loadWorkflowSessionsIfShown\(wId, true\)/, '只有落后才强制重取')
  })

  it('resyncAfterConnect：待发队列、设置、托管自驾任务直接补取，其余仓库经登记处补取', () => {
    const body = bodyOf(session, /function resyncAfterConnect\([^)]*\)[^{]*\{/)
    assert.match(body, /void fetchQueuedFeedbacks\(\)/, '待发队列要重取')
    assert.match(body, /useSettingsStore\(\)\.onConnected\(\)/, '全局设置要重取（settings_updated 不补发）')
    assert.match(body, /useTaskStore\(\)\.onConnected\(\)/, '托管自驾任务列表要重取（task_update 不补发）')
    assert.match(body, /runConnectHooks\(\)/, '任务清单 / 确认项 / 惰性加载的仓库经登记处补取')
  })

  it('任务清单与确认项不由 resyncAfterConnect 直接 use…Store()：那会在用不到它们的页面上把它们实例化出来、白发请求', () => {
    const body = bodyOf(session, /function resyncAfterConnect\([^)]*\)[^{]*\{/)
    // 注释里会出现这两个名字，只看代码行
    const code = body.split('\n').filter(l => !l.trim().startsWith('//')).join('\n')
    assert.doesNotMatch(code, /useWorkItemStore\(\)/, '任务清单仓库在设置页这类页面上不该因为连上实时流而存在（bfcache.mjs 的 /work 断言靠这一点成立）')
    assert.doesNotMatch(code, /useConfirmStore\(\)/, '确认项仓库同理')
  })

  it('补取里不为惰性仓库动态 import：没用过的页面不该在连上时拉它们的代码块', () => {
    const body = bodyOf(session, /function resyncAfterConnect\([^)]*\)[^{]*\{/)
    assert.doesNotMatch(body, /import\(/, '惰性仓库走 lib/connectHooks.ts 的登记，登记了才说明它被用过')
    assert.doesNotMatch(session, /^import .*from '\.\/(projectDocs|asyncMode|workflowPrompts)'/m, '这三个仓库必须保持惰性加载')
  })

  it('任务清单、确认项与三个惰性仓库都在自己的 setup 里登记补取', () => {
    assert.match(read('stores/workItems.ts'), /registerConnectHook\('workItems', \(\) => onServerEvent\(\)\)/)
    assert.match(read('stores/confirms.ts'), /registerConnectHook\('confirms', \(\) => onServerEvent\(\)\)/)
    assert.match(read('stores/workflowPrompts.ts'), /registerConnectHook\('workflowPrompts', onConnected\)/)
    assert.match(read('stores/asyncMode.ts'), /registerConnectHook\('asyncMode', onProfilesEvent\)/)
    assert.match(read('stores/projectDocs.ts'), /registerConnectHook\('projectDocs', \(\) => onServerEvent\(\)\)/)
  })
})

describe('全局设置仓库（stores/settings.ts）：连上后重取', () => {
  const src = read('stores/settings.ts')

  it('onConnected 重取全局设置，并导出给 session.ts 用', () => {
    const body = bodyOf(src, /function onConnected\(\)[^{]*\{/)
    assert.match(body, /fetchRemoteSettings\(\)/)
    const exported = src.slice(src.lastIndexOf('return {'))
    assert.match(exported, /\bonConnected\b/)
  })

  it('读取按发出的先后落地：晚发的先到之后，早发的慢响应不能再盖回去', () => {
    const body = bodyOf(src, /async function fetchRemoteSettings\(\)[^{]*\{/)
    assert.match(body, /const mine = \+\+fetchRemoteIssued/, '每次读取先领一个序号')
    const guard = body.indexOf('if (mine < fetchRemoteApplied) return')
    assert.ok(guard >= 0, '响应回来要先对序号')
    assert.ok(body.indexOf('await settingsApi.get()') < guard && guard < body.indexOf('settings.value ='), '对序号要在响应之后、写入设置之前')
    assert.match(body, /fetchRemoteApplied = mine/)
  })
})

describe('任务仓库（stores/task.ts）：连上后重取', () => {
  const src = read('stores/task.ts')

  it('onConnected：被用过（取过列表或打开过任务）才重取；并导出给 session.ts 用', () => {
    const body = bodyOf(src, /function onConnected\(\)[^{]*\{/)
    assert.match(body, /if \(fetchTasksSeq === 0 && !currentTask\.value\) return/)
    assert.match(body, /debouncedRefresh\(\)/)
    const exported = src.slice(src.lastIndexOf('return {'))
    assert.match(exported, /\bonConnected\b/)
  })
})

describe('项目文档仓库（stores/projectDocs.ts）：后台校准沿用目录的筛选条件', () => {
  const src = read('stores/projectDocs.ts')

  it('loadList 记下这次的筛选条件，onServerEvent 重取目录时原样带上（否则连上 / 事件之后筛选着的目录悄悄变回没筛选）', () => {
    const load = bodyOf(src, /async function loadList\([^)]*\)[^{]*\{/)
    assert.match(load, /listParams = params/)
    const onEvent = bodyOf(src, /function onServerEvent\([^)]*\)[^{]*\{/)
    assert.match(onEvent, /const listFilter = listParams/, '要在调用的那一刻取下筛选条件（去抖期间页面可能又换了）')
    assert.match(onEvent, /loadList\(listWf, listFilter\)/)
    assert.doesNotMatch(onEvent, /loadList\(listWf\)/, '不带筛选条件的重取会把目录变回没筛选')
  })
})

describe('群列表仓库（stores/groups.ts）', () => {
  const src = read('stores/groups.ts')

  it('事件门槛是「被要过」，不是 loaded：第一次响应落下来之前到的事件不能丢', () => {
    const body = bodyOf(src, /function onServerEvent\([^)]*\)[^{]*\{/)
    assert.match(body, /\brequested\b/, 'onServerEvent 要以 requested 为门槛')
    assert.doesNotMatch(body, /loaded\.value/, '用 loaded 当门槛，第一次响应在路上时到的事件会被丢掉，随后落下来的是事件之前的旧数据')
  })

  it('load() 一开始就置 requested（含第一次请求还在路上 / 失败了的情况）', () => {
    const body = bodyOf(src, /async function load\(\)[^{]*\{/)
    assert.match(body, /requested = true/)
    assert.ok(body.indexOf('requested = true') < body.indexOf('await '), '要在发请求之前置位，不能等响应回来')
  })

  it('onConnected：被要过才重取，立即排一次（不再等 400ms 去抖）；并导出给 session.ts 用', () => {
    const body = bodyOf(src, /function onConnected\(\)[^{]*\{/)
    assert.match(body, /if \(requested\) schedule\(0\)/)
    const exported = src.slice(src.lastIndexOf('return {'))
    assert.match(exported, /\bonConnected\b/)
  })
})

describe('聊天仓库（stores/chat.ts）', () => {
  const src = read('stores/chat.ts')

  it('onConnected：补取打开着的会话的最新一页，并重取侧栏关注的未读数；并导出给 session.ts 用', () => {
    const body = bodyOf(src, /function onConnected\(\)[^{]*\{/)
    assert.match(body, /void refresh\(\)/)
    assert.match(body, /scheduleSummaries\(0\)/)
    const exported = src.slice(src.lastIndexOf('return {'))
    assert.match(exported, /\bonConnected\b/)
  })
})
