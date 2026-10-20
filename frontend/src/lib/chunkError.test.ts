// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import {
  CHUNK_TOAST_ID,
  chunkFailureCopy,
  createChunkFailureReporter,
  isChunkLoadError,
  type ChunkFailureNotice,
} from './chunkError'

function fakeDeps() {
  const notices: ChunkFailureNotice[] = []
  const reloads: (string | undefined)[] = []
  return {
    notices,
    reloads,
    deps: {
      notify: (n: ChunkFailureNotice) => void notices.push(n),
      reload: (t?: string) => void reloads.push(t),
    },
  }
}

describe('isChunkLoadError：认得各浏览器 / Vite 的分块加载失败', () => {
  it('Chrome / Firefox / Safari / Vite / webpack 的措辞都算', () => {
    const messages = [
      'Failed to fetch dynamically imported module: https://x/assets/TerminalLauncherDialog-abc.js',
      'error loading dynamically imported module: https://x/assets/a.js',
      'Importing a module script failed.',
      "Failed to load module script: Expected a JavaScript module script but the server responded with a MIME type of \"text/html\".",
      'Unable to preload CSS for /assets/ArchiveView-1.css',
      'Loading chunk 12 failed.',
      'Loading CSS chunk 7 failed.',
    ]
    for (const m of messages) assert.equal(isChunkLoadError(new TypeError(m)), true, m)
    const named = new Error('x')
    named.name = 'ChunkLoadError'
    assert.equal(isChunkLoadError(named), true)
  })
  it('字符串形式的错误也认', () => {
    assert.equal(isChunkLoadError('Failed to fetch dynamically imported module: u'), true)
  })
  it('模块自己执行时抛的错误、普通错误、空值都不算（不该提示刷新）', () => {
    assert.equal(isChunkLoadError(new TypeError("Cannot read properties of undefined (reading 'x')")), false)
    assert.equal(isChunkLoadError(new Error('boom')), false)
    assert.equal(isChunkLoadError(null), false)
    assert.equal(isChunkLoadError(undefined), false)
    assert.equal(isChunkLoadError(42), false)
    assert.equal(isChunkLoadError({}), false)
  })
})

describe('chunkFailureCopy', () => {
  it('组件与路由的文案不同，但都给出「刷新」出路', () => {
    const c = chunkFailureCopy({ kind: 'component' })
    const r = chunkFailureCopy({ kind: 'route', target: '/archive' })
    assert.equal(c.actionLabel, '刷新')
    assert.equal(r.actionLabel, '刷新')
    assert.notEqual(c.title, r.title)
    assert.ok(r.description.includes('前往你要去的页面'))
  })
})

describe('createChunkFailureReporter', () => {
  it('分块加载失败：提示一次（固定编号）并返回 true；点「刷新」刷新当前页', () => {
    const { notices, reloads, deps } = fakeDeps()
    const report = createChunkFailureReporter(deps)
    assert.equal(report(new TypeError('Failed to fetch dynamically imported module: u')), true)
    assert.equal(notices.length, 1)
    assert.equal(notices[0]?.id, CHUNK_TOAST_ID)
    notices[0]?.onAction()
    assert.deepEqual(reloads, [undefined])
  })
  it('路由页失败：「刷新」整页前往目标页', () => {
    const { notices, reloads, deps } = fakeDeps()
    const report = createChunkFailureReporter(deps)
    report(new Error('Unable to preload CSS for /a.css'), { kind: 'route', target: '/archive?x=1' })
    notices[0]?.onAction()
    assert.deepEqual(reloads, ['/archive?x=1'])
  })
  it('反复失败用的是同一个编号，界面上只会有一条（后来的就地替换前面的）', () => {
    const { notices, deps } = fakeDeps()
    const report = createChunkFailureReporter(deps)
    report(new Error('Importing a module script failed.'))
    report(new Error('Importing a module script failed.'), { kind: 'route', target: '/settings' })
    assert.equal(notices.length, 2)
    assert.equal(new Set(notices.map(n => n.id)).size, 1)
    assert.equal(notices[1]?.title, chunkFailureCopy({ kind: 'route', target: '/settings' }).title)
  })
  it('不是分块加载失败：什么也不做，返回 false，交给调用方自己处理', () => {
    const { notices, deps } = fakeDeps()
    const report = createChunkFailureReporter(deps)
    assert.equal(report(new Error('boom')), false)
    assert.equal(notices.length, 0)
  })
})
