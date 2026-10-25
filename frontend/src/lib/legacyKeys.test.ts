// 运行：pnpm --dir frontend run test:unit
// 已删功能留在本机存储里的开关键（#165 旧聊天视图、#166 预览视图的「经典」开关）：启动时清掉；经典视图删除后源码里不再有任何分支。
import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, it } from 'node:test'
import { LEGACY_STORAGE_KEYS, dropLegacyStorageKeys } from './legacyKeys'

function srcDir(): string {
  for (const base of [join(process.cwd(), 'src'), join(process.cwd(), 'frontend', 'src')]) {
    if (existsSync(join(base, 'lib', 'legacyKeys.test.ts'))) return base
  }
  throw new Error('找不到 frontend/src，请在仓库根目录或 frontend 目录下运行测试')
}
const read = (rel: string) => readFileSync(join(srcDir(), rel), 'utf8')

/** 假存储：记录哪些键被清掉、别的键原样留着。 */
function fakeStorage(initial: Record<string, string>) {
  const data = new Map(Object.entries(initial))
  return {
    data,
    removeItem(key: string) {
      data.delete(key)
    },
  }
}

describe('旧开关键清理', () => {
  it('键名固定（改了就清不掉老设备上的那一份）', () => {
    assert.deepEqual([...LEGACY_STORAGE_KEYS], ['relaymesh_chat_preview', 'relaymesh_preview_view'])
  })

  it('选过经典（\'0\'）、选过预览（\'1\'）、旧聊天视图的值都清掉，别的键一个不动', () => {
    for (const value of ['0', '1', 'true', '']) {
      const s = fakeStorage({
        relaymesh_preview_view: value,
        relaymesh_chat_preview: '1',
        relaymesh_token: 'keep-me',
        relaymesh_sidebar_collapsed: '1',
      })
      dropLegacyStorageKeys(s)
      assert.deepEqual([...s.data.keys()].sort(), ['relaymesh_sidebar_collapsed', 'relaymesh_token'], `值 ${JSON.stringify(value)}`)
    }
  })

  it('没有这些键时什么也不发生；存储不可用（removeItem 抛错）时不抛错', () => {
    const empty = fakeStorage({ relaymesh_token: 'keep-me' })
    dropLegacyStorageKeys(empty)
    assert.deepEqual([...empty.data.keys()], ['relaymesh_token'])
    assert.doesNotThrow(() =>
      dropLegacyStorageKeys({
        removeItem() {
          throw new Error('SecurityError')
        },
      }),
    )
  })

  it('启动入口调用它，且在挂载之前', () => {
    const main = read('main.ts')
    assert.match(main, /import \{ dropLegacyStorageKeys \} from '\.\/lib\/legacyKeys'/)
    const call = main.indexOf('\ndropLegacyStorageKeys()\n')
    assert.ok(call > 0, 'main.ts 应在启动时调用 dropLegacyStorageKeys()')
    assert.ok(call < main.indexOf("app.mount('#app')"), '清理要在挂载之前')
  })
})

describe('经典视图已经删除（#166）：开关与入口没有残留', () => {
  it('开关规则 / 组合式 / 入口组件的文件不再存在', () => {
    for (const gone of ['lib/previewView.ts', 'composables/usePreviewView.ts', 'components/feedback/ViewTrialLinks.vue']) {
      assert.ok(!existsSync(join(srcDir(), gone)), `${gone} 应已删除`)
    }
  })

  it('会话页只挂预览列表：没有开关变量、没有经典列表分支', () => {
    const view = read('views/FeedbackView.vue')
    assert.ok(!/showPreviewView|usePreviewView|FeedbackMessageList|ViewTrialLinks/.test(view), '会话页不该再有视图开关或经典列表')
    assert.match(view, /<PreviewMessageList\s+ref="messageListRef"/)
  })

  it('页面里没有「回到经典视图 / 试用预览视图」的字样与测试标记', () => {
    for (const rel of ['views/FeedbackView.vue', 'components/feedback/FeedbackInputDock.vue', 'components/feedback/MessageSearchLink.vue']) {
      const src = read(rel)
      assert.ok(!/回到经典视图|试用预览视图|preview-view-on|preview-view-off/.test(src), `${rel} 不该再有视图切换入口`)
    }
  })
})
