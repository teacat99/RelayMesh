import { describe, it } from 'node:test'
import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { BODY_CHUNK, applyBodies, chunk, isLightRow, lightIdsAfter, preserveFullBodies } from './lightRows'
import type { FeedbackSession, SessionBody } from '../api/types'

const T = (n: number) => new Date(Date.UTC(2026, 9, 1, 0, n)).toISOString()

const full = (id: string, n: number, extra: Partial<FeedbackSession> = {}): FeedbackSession => ({
  session_id: id,
  summary: `# ${id}\n完整正文 ${id}`,
  response_text: `回复 ${id}\n第二行`,
  user_messages: ['快捷'],
  images: [],
  status: 'completed',
  created_at: T(n),
  updated_at: T(n + 1000),
  ...extra,
})

const light = (id: string, n: number, extra: Partial<FeedbackSession> = {}): FeedbackSession => ({
  session_id: id,
  summary: `# ${id}`,
  response_text: `回复 ${id}`,
  status: 'completed',
  created_at: T(n),
  updated_at: T(n + 1000),
  body_omitted: true,
  ...extra,
})

const bodyOf = (row: FeedbackSession, extra: Partial<SessionBody> = {}): SessionBody => ({
  session_id: row.session_id,
  updated_at: row.updated_at,
  summary: `# ${row.session_id}\n完整正文 ${row.session_id}`,
  response_text: `回复 ${row.session_id}\n第二行`,
  user_messages: ['快捷'],
  images: [],
  ...extra,
})

describe('轻量行判断', () => {
  it('只有 body_omitted === true 才是轻量行', () => {
    assert.equal(isLightRow(light('a', 1)), true)
    assert.equal(isLightRow(full('a', 1)), false)
    assert.equal(isLightRow({ body_omitted: false }), false)
    assert.equal(isLightRow(null), false)
    assert.equal(isLightRow(undefined), false)
  })
})

describe('preserveFullBodies：再取一次表不丢已经取过的正文', () => {
  it('本地有同版本的完整行：把正文接回来，其余字段用刚取到的，body_omitted 去掉', () => {
    const mine = full('a', 1)
    const fetched = [light('a', 1, { status: 'archived' }), full('b', 2)]
    const out = preserveFullBodies([mine], fetched)
    assert.equal(out[0].summary, mine.summary)
    assert.equal(out[0].response_text, mine.response_text)
    assert.deepEqual(out[0].user_messages, mine.user_messages)
    assert.equal(out[0].status, 'archived', '元数据以刚取到的为准')
    assert.equal('body_omitted' in out[0], false)
    assert.equal(out[1], fetched[1], '本来就是完整行的原样')
  })

  it('版本不同（服务端已更新）：保持轻量，正文之后重取', () => {
    const mine = full('a', 1)
    const fetched = [light('a', 1, { updated_at: T(5000) })]
    const out = preserveFullBodies([mine], fetched)
    assert.equal(out[0].body_omitted, true)
    assert.equal(out[0].summary, '# a')
  })

  it('本地那行本身也是轻量行：不能拿预览冒充正文', () => {
    const out = preserveFullBodies([light('a', 1)], [light('a', 1)])
    assert.equal(out[0].body_omitted, true)
  })

  it('没有可接的行：返回传入的同一个数组', () => {
    const fetched = [light('a', 1)]
    assert.equal(preserveFullBodies(undefined, fetched), fetched)
    assert.equal(preserveFullBodies([], fetched), fetched)
    assert.equal(preserveFullBodies([full('x', 9)], fetched), fetched)
    const allFull = [full('a', 1)]
    assert.equal(preserveFullBodies([full('a', 1)], allFull), allFull)
  })

  it('不改传入的数组与行对象', () => {
    const mine = full('a', 1)
    const fetched = [light('a', 1)]
    const snapshot = JSON.stringify(fetched)
    preserveFullBodies([mine], fetched)
    assert.equal(JSON.stringify(fetched), snapshot)
  })
})

describe('applyBodies：把取回的正文填进轻量行', () => {
  it('填正文、去掉 body_omitted，其余字段不动，未被点名的行原样（同一个对象）', () => {
    const a = light('a', 1)
    const b = light('b', 2)
    const c = full('c', 3)
    const out = applyBodies([a, b, c], [bodyOf(a)])
    assert.notEqual(out, undefined)
    assert.equal(out[0].summary, '# a\n完整正文 a')
    assert.equal(out[0].response_text, '回复 a\n第二行')
    assert.deepEqual(out[0].user_messages, ['快捷'])
    assert.equal('body_omitted' in out[0], false)
    assert.equal(out[0].status, a.status)
    assert.equal(out[0].created_at, a.created_at)
    assert.equal(out[1], b)
    assert.equal(out[2], c)
  })

  it('已经是完整行的不覆盖（它可能刚被 SSE 补丁更新过）', () => {
    const rows = [full('a', 1, { summary: '较新的正文' })]
    const out = applyBodies(rows, [bodyOf(rows[0], { summary: '旧正文' })])
    assert.equal(out, rows)
    assert.equal(out[0].summary, '较新的正文')
  })

  it('取回的正文比缓存里的行旧：不填；更新：填并采用它的版本', () => {
    const a = light('a', 1, { updated_at: T(2000) })
    assert.equal(applyBodies([a], [bodyOf(a, { updated_at: T(1500) })])[0].body_omitted, true)
    const out = applyBodies([a], [bodyOf(a, { updated_at: T(2500) })])
    assert.equal(out[0].updated_at, T(2500))
    assert.equal('body_omitted' in out[0], false)
  })

  it('没有任何行被填：返回传入的同一个数组；点名了不存在的 id 也不影响', () => {
    const rows = [light('a', 1), full('b', 2)]
    assert.equal(applyBodies(rows, []), rows)
    assert.equal(applyBodies(rows, [bodyOf(full('zzz', 9))]), rows)
    assert.equal(applyBodies(rows, [bodyOf(rows[1])]), rows)
  })

  it('取回的正文缺字段：按空处理，而不是留下 undefined 之外的脏值', () => {
    const a = light('a', 1)
    const out = applyBodies([a], [{ session_id: 'a', updated_at: a.updated_at, summary: '正文' }])
    assert.equal(out[0].response_text, '')
    assert.deepEqual(out[0].user_messages, [])
    assert.deepEqual(out[0].images, [])
  })

  it('不改传入的行对象', () => {
    const a = light('a', 1)
    const snapshot = JSON.stringify(a)
    applyBodies([a], [bodyOf(a)])
    assert.equal(JSON.stringify(a), snapshot)
  })
})

describe('lightIdsAfter：窗口要补哪些行', () => {
  const rows = [light('r1', 1), light('n1', 2, { kind: 'notice' }), full('r2', 3), light('r3', 4), light('n2', 5, { kind: 'notice' }), full('r4', 6)]

  it('null：从头开始，所有轻量行（含通知），按表里的顺序', () => {
    assert.deepEqual(lightIdsAfter(rows, null), ['r1', 'n1', 'r3', 'n2'])
  })

  it('某一行之后：不含它自己，只要轻量行', () => {
    assert.deepEqual(lightIdsAfter(rows, 'r1'), ['n1', 'r3', 'n2'])
    assert.deepEqual(lightIdsAfter(rows, 'r2'), ['r3', 'n2'])
    assert.deepEqual(lightIdsAfter(rows, 'r4'), [])
  })

  it('表里找不到这个 id：当作从头开始', () => {
    assert.deepEqual(lightIdsAfter(rows, 'nope'), ['r1', 'n1', 'r3', 'n2'])
  })

  it('没有轻量行：空', () => {
    assert.deepEqual(lightIdsAfter([full('a', 1), full('b', 2)], null), [])
    assert.deepEqual(lightIdsAfter([], 'x'), [])
  })
})

describe('chunk', () => {
  it('按批切开，最后一批可以不满', () => {
    assert.deepEqual(chunk([1, 2, 3, 4, 5], 2), [[1, 2], [3, 4], [5]])
    assert.deepEqual(chunk([], 3), [])
    assert.deepEqual(chunk([1, 2], 10), [[1, 2]])
  })

  it('批大小不合法时按 1 处理，不死循环', () => {
    assert.deepEqual(chunk([1, 2], 0), [[1], [2]])
    assert.deepEqual(chunk([1, 2], -3), [[1], [2]])
  })

  it('默认批大小不超过服务端单次上限 50', () => {
    assert.ok(BODY_CHUNK > 0 && BODY_CHUNK <= 50)
  })
})

// 源码级防线：这几条不变量散在 store、三种卡与预览列表里，靠约定守不住——漏掉任何一处，轻量行（只有首行预览）就会被当成正文渲染，
// 或者窗口增长时才发现要等正文（骨架、高度突变、锚点漂移）。行为本身由 e2e/preview-light.mjs 在真实浏览器里验证。
describe('轻量行的源码级防线（#169 第二期）', () => {
  function srcDir(): string {
    for (const base of [join(process.cwd(), 'src'), join(process.cwd(), 'frontend', 'src')]) {
      if (existsSync(join(base, 'lib', 'lightRows.test.ts'))) return base
    }
    throw new Error('找不到 frontend/src，请在仓库根目录或 frontend 目录下运行测试')
  }
  const read = (...parts: string[]) => readFileSync(join(srcDir(), ...parts), 'utf8')
  const cardDir = ['components', 'feedback', 'preview-view']

  it('预览视图三种卡的正文都套在 PreviewBodyGate 里，轻量行时不给「复制正文」', () => {
    for (const name of ['PreviewReportCard', 'PreviewNoticeCard', 'PreviewReplyCard']) {
      const src = read(...cardDir, `${name}.vue`)
      assert.match(src, /import PreviewBodyGate from '\.\/PreviewBodyGate\.vue'/, `${name} 要引入 PreviewBodyGate`)
      assert.match(src, /<PreviewBodyGate :session="session"[^>]*>[\s\S]+<\/PreviewBodyGate>/, `${name} 的正文要在 PreviewBodyGate 里`)
      assert.match(src, /body_omitted/, `${name} 的菜单在轻量行时不能给「复制正文」`)
    }
    // 正文本体必须在门里面：Markdown / 回复文字都不能写在门外
    assert.match(read(...cardDir, 'PreviewReportCard.vue'), /<PreviewBodyGate[^>]*>\s*<PreviewMarkdown :content="session\.summary"/)
    assert.match(read(...cardDir, 'PreviewNoticeCard.vue'), /<PreviewBodyGate[^>]*>\s*<PreviewMarkdown :content="session\.summary"/)
    assert.match(read(...cardDir, 'PreviewReplyCard.vue'), /<PreviewBodyGate[^>]*>[\s\S]*<LinkedText[\s\S]*<\/PreviewBodyGate>/)
  })

  it('PreviewBodyGate：轻量行画骨架并排队取正文，取失败隔一会儿再试，卸载时清定时器', () => {
    const src = read(...cardDir, 'PreviewBodyGate.vue')
    assert.match(src, /v-if="session\.body_omitted"/)
    assert.match(src, /data-testid="body-skeleton"/)
    assert.match(src, /<slot v-else \/>/)
    assert.match(src, /sessionStore\.queueSessionBody\(props\.session\.workflow_id \|\| props\.session\.session_id, props\.session\.session_id\)/)
    assert.match(src, /setInterval\(request, RETRY_MS\)/)
    assert.match(src, /onBeforeUnmount\(\(\) => \{\s*\n\s*if \(timer\) clearInterval\(timer\)/)
  })

  it('store：整表一律请求轻量表，再接回已取过的正文；不再有按视图开关分叉的分支（#166 删掉经典视图）', () => {
    const src = read('stores', 'session.ts')
    assert.match(src, /const res = await sessionsApi\.getWorkflowSessions\(workflowId, \{ light: true \}\)/)
    assert.match(src, /const fetched = preserveFullBodies\(local, res\.sessions\)/)
    assert.ok(!/displayRows|cacheUsable|upgradeToFull|usePreviewView|previewView|hasLightRows/.test(src), '轻量行不再因视图开关而分支')
    // 摆给展示层的行直接用整表：整表取回 / 秒开缓存 / 补一份缓存 / 选中会话 / 正文取回，五处
    assert.equal((src.match(/currentWorkflowSessions\.value = (?:merged|cached|next|workflowSessionsCache\.value\[wId\] \?\? \[\])\n/g) ?? []).length, 5)
    assert.match(src, /if \(isCacheFresh\(workflowSyncedAt\.get\(workflowId\), lastSseGapAt, Date\.now\(\), WORKFLOW_CACHE_TTL_MS\)\) return/)
    assert.match(src, /if \(cached\?\.length\) \{\s*\n\s*if \(activeWorkflowId\(\) === workflowId/)
  })

  it('预览列表：窗口只在三处改大小，增长都先取回正文；向上载入要在取回之后再量锚点', () => {
    const list = read(...cardDir, 'PreviewMessageList.vue')
    const assigns = [...list.matchAll(/visibleRoundsCount\.value = ([^\n]+)/g)].map(m => m[1].trim())
    assert.equal(assigns.length, 3, `窗口大小只允许在三处赋值（重置 / growWindowTo / loadEarlierRounds），现在是：${assigns.join(' ; ')}`)
    assert.ok(assigns.some(a => a === 'INITIAL_VISIBLE_ROUNDS'), '换工作流时重置')
    assert.ok(assigns.some(a => a.startsWith('Math.max(visibleRoundsCount.value,')), 'growWindowTo（跳转）')
    assert.ok(assigns.some(a => a.startsWith('Math.min(')), 'loadEarlierRounds（向上载入）')
    // 所有跳转路径（轨道 / 搜索命中 / 确认项 / 深链）都经过 growWindowTo
    assert.equal((list.match(/await growWindowTo\(|void growWindowTo\(/g) ?? []).length, 3, 'scrollToSession / revealEntry / focusRef 各调一次')
    const load = list.slice(list.indexOf('function loadEarlierRounds()'), list.indexOf('function setupTopObserver()'))
    assert.ok(load.indexOf('await ensureWindowBodies(') > 0, '向上载入要先取回这一批的正文')
    assert.ok(load.indexOf('await ensureWindowBodies(') < load.indexOf('const oldScrollHeight'), '锚点与 scrollHeight 要在取回之后量')
    assert.ok(load.indexOf('await ensureWindowBodies(') < load.indexOf('visibleRoundsCount.value = Math.min('), '正文取回之后才增长窗口')
    assert.match(load, /prefetchNextBatch\(\)/)
  })
})
