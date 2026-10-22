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
// 行为靠 e2e 守住，这里只守住接线，防止退回旧写法。
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

describe('实时流（重）连上：聊天与群聊也要整表校准', () => {
  const session = read('stores/session.ts')

  it('session.ts 的 onopen 里调用聊天与群聊仓库的 onConnected', () => {
    const start = session.indexOf('eventSource.onopen = () => {')
    assert.ok(start >= 0, '找不到 eventSource.onopen')
    const end = session.indexOf('eventSource.onerror', start)
    const onopen = session.slice(start, end)
    assert.match(onopen, /useChatStore\(\)\.onConnected\(\)/, '连上时要让聊天仓库补取打开着的会话、重取未读数')
    assert.match(onopen, /useGroupStore\(\)\.onConnected\(\)/, '连上时要让群聊仓库重取群列表')
    // 首次连上与重连都要做（页面载入到连上之间的事件同样没人听见），不能只放在 hasConnectedOnce 的重连分支里
    const reconnectOnly = onopen.slice(onopen.indexOf('if (hasConnectedOnce) {'))
    assert.doesNotMatch(reconnectOnly, /useChatStore\(\)\.onConnected|useGroupStore\(\)\.onConnected/, '不能只在重连分支里校准：首次连上之前的事件同样丢了')
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
