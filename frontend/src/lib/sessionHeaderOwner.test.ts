// 运行：pnpm --dir frontend run test:unit
// 会话页头「项目」入口查归属项目的源码级防线（#208）：
// 行为（失败后补查、真没有归属不补查、切走就停）由 docNav.test.ts 里 retryWithBackoff / resolveOwnerPatiently 的用例量，
// 页头真的在失败后补上编号由端到端套件 scripts/e2e-terminal/e2e/owner-retry.mjs 量；
// 这里钉住「靠它们成立」的接线，免得有人整理页头时顺手改回「只查一次」、而 e2e 又没跑到：
//   1. 页头用 resolveOwnerPatiently 查归属（失败不进缓存，所以必须补查），不再直接 await 一次 ownerProjectId；
//   2. 篇数（角标）的目录加载也走 retryWithBackoff，失败被它吞掉、不留未处理的 Promise 拒绝；
//   3. 补查在「切走了工作流」或「页头卸载」时停下（stillWanted 同时看这两件事）；
//   4. projectDocs 仓库里没有人等结果的后台刷新（服务端事件触发）一律 .catch，不再 `void loadList(…)` 裸放。
import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, it } from 'node:test'

function srcDir(): string {
  for (const base of [join(process.cwd(), 'src'), join(process.cwd(), 'frontend', 'src')]) {
    if (existsSync(join(base, 'lib', 'sessionHeaderOwner.test.ts'))) return base
  }
  throw new Error('找不到 frontend/src，请在仓库根目录或 frontend 目录下运行测试')
}
const read = (rel: string) => readFileSync(join(srcDir(), rel), 'utf8')
const squash = (s: string) => s.replace(/\s+/g, ' ').trim()

const HEADER = read('components/feedback/SessionHeader.vue')
const DOCS_STORE = read('stores/projectDocs.ts')

/** 页头里监听工作流编号、查归属的那个 watch 块（从 watch( 到 { immediate: true }）。 */
function ownerWatch(): string {
  const start = HEADER.indexOf('watch(sessionWorkflowId, async id => {')
  assert.ok(start > 0, '页头里找不到查归属的 watch')
  const end = HEADER.indexOf('{ immediate: true })', start)
  assert.ok(end > start, '查归属的 watch 没有 immediate')
  return squash(HEADER.slice(start, end))
}

describe('会话页头：查所属项目失败后补查（#208）', () => {
  it('从 docNav 引入 resolveOwnerPatiently / retryWithBackoff，从 docNavClient 引入 ownerProjects（不再引一次性的 ownerProjectId）', () => {
    const imports = squash(HEADER.slice(0, HEADER.indexOf('const props')))
    assert.match(imports, /import \{[^}]*\bresolveOwnerPatiently\b[^}]*\} from '..\/..\/lib\/docNav'/)
    assert.match(imports, /import \{[^}]*\bretryWithBackoff\b[^}]*\} from '..\/..\/lib\/docNav'/)
    assert.match(imports, /import \{[^}]*\bownerProjects\b[^}]*\} from '..\/..\/lib\/docNavClient'/)
    assert.doesNotMatch(imports, /\bownerProjectId\b(?!Within)/, '页头不该再引一次性的 ownerProjectId')
  })

  it('watch 里用 resolveOwnerPatiently 查归属，没有直接 await ownerProjectId(', () => {
    const w = ownerWatch()
    assert.match(w, /await resolveOwnerPatiently\(ownerProjects, id, \{ stillWanted \}\)/)
    assert.doesNotMatch(w, /await ownerProjectId\(/)
  })

  it('目录加载也走 retryWithBackoff：失败被吞掉、会补查，不留未处理的 Promise 拒绝', () => {
    const w = ownerWatch()
    assert.match(w, /void retryWithBackoff\(async \(\) => \{ await docStore\.loadList\(id\) return true \}, \{ stillWanted \}\)/)
    assert.doesNotMatch(w, /void docStore\.loadList\(/)
  })

  it('补查在切走工作流或页头卸载时停下：stillWanted 同时看 headerGone 与当前工作流', () => {
    const w = ownerWatch()
    assert.match(w, /const stillWanted = \(\) => !headerGone && sessionWorkflowId\.value === id/)
    const src = squash(HEADER)
    assert.match(src, /let headerGone = false onBeforeUnmount\(\(\) => \{ headerGone = true \}\)/)
  })

  it('解析结果落到 ownerId / ownerName 之前先确认还需要（切走就丢弃）', () => {
    const src = squash(HEADER)
    assert.match(src, /if \(!stillWanted\(\)\) return ownerId\.value = pid ownerName\.value = pid > 0 \? ownerProjectName\(id\) : ''/)
  })
})

describe('projectDocs 仓库：后台刷新失败不留未处理的 Promise 拒绝（#208）', () => {
  it('服务端事件触发的刷新（目录 / 打开的文档 / 确认项变化）都带 .catch', () => {
    const src = squash(DOCS_STORE)
    assert.doesNotMatch(src, /void loadList\(/)
    assert.doesNotMatch(src, /void loadDetail\(/)
    // 重取目录时带着筛选条件（listFilter，#243）：不带的话筛选着的目录会在事件 / 实时流连上之后变回没筛选
    assert.match(src, /loadList\(listWf, listFilter\)\.catch\(ignoreBackgroundError\)/)
    assert.equal((src.match(/loadDetail\(cur\.workflow_id, cur\.path, true\)\.catch\(ignoreBackgroundError\)/g) ?? []).length, 2)
    assert.match(src, /const ignoreBackgroundError = \(\) => \{\}/)
  })
})
