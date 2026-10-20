// 运行：pnpm --dir frontend run test:unit
// 「预览视图」开关规则（#120，#165 起默认开）：本机开关三态（'1' 预览 / '0' 经典 / 没有值 = 默认预览）、地址参数临时覆盖且优先。
import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, it } from 'node:test'
import {
  LEGACY_CHAT_PREVIEW_KEY,
  PREVIEW_VIEW_DEFAULT,
  PREVIEW_VIEW_KEY,
  parseStoredFlag,
  parseViewParam,
  resolvePreviewView,
  serializeFlag,
} from './previewView'

function srcDir(): string {
  for (const base of [join(process.cwd(), 'src'), join(process.cwd(), 'frontend', 'src')]) {
    if (existsSync(join(base, 'lib', 'previewView.test.ts'))) return base
  }
  throw new Error('找不到 frontend/src，请在仓库根目录或 frontend 目录下运行测试')
}
const read = (rel: string) => readFileSync(join(srcDir(), rel), 'utf8')

describe('预览视图开关', () => {
  it('存储键固定（改了会让已经明确选过的设备掉回默认）', () => {
    assert.equal(PREVIEW_VIEW_KEY, 'relaymesh_preview_view')
    assert.equal(LEGACY_CHAT_PREVIEW_KEY, 'relaymesh_chat_preview')
  })

  it('默认是预览视图（#165）：没有值、空串、旧值、乱值都是开', () => {
    assert.equal(PREVIEW_VIEW_DEFAULT, true)
    assert.equal(parseStoredFlag(null), true)
    assert.equal(parseStoredFlag(undefined), true)
    assert.equal(parseStoredFlag(''), true)
    assert.equal(parseStoredFlag('true'), true)
    assert.equal(parseStoredFlag('false'), true)
    assert.equal(parseStoredFlag('yes'), true)
  })

  it('明确选过才有值：\'1\' = 预览，\'0\' = 经典', () => {
    assert.equal(parseStoredFlag('1'), true)
    assert.equal(parseStoredFlag('0'), false)
  })

  it('写进存储的值与读出来的一一对应（经典写 \'0\'，不靠清掉键——清掉等于回到默认）', () => {
    assert.equal(serializeFlag(true), '1')
    assert.equal(serializeFlag(false), '0')
    for (const v of [true, false]) assert.equal(parseStoredFlag(serializeFlag(v)), v)
  })

  it('?view=preview 临时开、?view=classic 临时关，别的写法都不覆盖', () => {
    assert.equal(parseViewParam('?view=preview'), true)
    assert.equal(parseViewParam('?view=classic'), false)
    assert.equal(parseViewParam('?wf=abc&view=preview&s=1'), true)
    assert.equal(parseViewParam(''), null)
    assert.equal(parseViewParam('?wf=abc'), null)
    assert.equal(parseViewParam('?view='), null)
    assert.equal(parseViewParam('?view=Preview'), null)
    assert.equal(parseViewParam('?view=chat'), null)
    // 旧的聊天视图参数不再有任何作用（#165）
    assert.equal(parseViewParam('?chat=1'), null)
  })

  it('地址参数优先于本机开关；没有参数时看本机开关', () => {
    assert.equal(resolvePreviewView(false, null), false)
    assert.equal(resolvePreviewView(true, null), true)
    assert.equal(resolvePreviewView(false, true), true)
    assert.equal(resolvePreviewView(true, false), false)
  })
})

describe('预览视图开关（接线）：回到经典要写 \'0\'，旧聊天视图的键启动时清掉', () => {
  const src = read('composables/usePreviewView.ts')

  it('set 两个方向都写存储（serializeFlag），不再「关 = 清掉键」', () => {
    assert.match(src, /localStorage\.setItem\(PREVIEW_VIEW_KEY, serializeFlag\(value\)\)/)
    assert.ok(!/localStorage\.removeItem\(PREVIEW_VIEW_KEY\)/.test(src), '清掉键等于回到默认（预览），不能用来表达「选了经典」')
  })

  it('别的标签页改了开关 / 清空了存储：跟着变', () => {
    assert.match(src, /e\.key === PREVIEW_VIEW_KEY \|\| e\.key === null/)
  })

  it('启动时清掉旧版聊天视图（预览）的开关键', () => {
    assert.match(src, /localStorage\.removeItem\(LEGACY_CHAT_PREVIEW_KEY\)/)
    assert.match(src, /\ndropLegacyChatPreviewKey\(\)\n/)
  })

  it('旧的 useChatPreview 已删除，没人再引用', () => {
    assert.ok(!existsSync(join(srcDir(), 'composables/useChatPreview.ts')), 'useChatPreview.ts 应已删除')
  })
})
