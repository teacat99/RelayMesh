// 运行：pnpm --dir frontend run test:unit
// 懒加载拆包（#186）的接线守卫：入口脚本里被拆出去的整页 / 大对话框只要有一处又被静态 import 回来，这个块就会悄悄回到入口、
// 首访体积原样回升而没人察觉。这里按源码文本断言「谁不能静态引用谁」「登记表与入口属性对得上」。
// 体积本身的前后对比用 `node frontend/scripts/bundle-report.mjs`；运行时行为（占位、预取、失败提示）由 e2e `lazy-views` 覆盖。
import assert from 'node:assert/strict'
import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { describe, it } from 'node:test'

/** 单测是编译到缓存目录里跑的，不能用 import.meta.url 找源码；从当前目录往 frontend/src 找。 */
function srcDir(): string {
  for (const base of [join(process.cwd(), 'src'), join(process.cwd(), 'frontend', 'src')]) {
    if (existsSync(join(base, 'lib', 'lazyWiring.test.ts'))) return base
  }
  throw new Error('找不到 frontend/src，请在仓库根目录或 frontend 目录下运行测试')
}
const read = (rel: string) => readFileSync(join(srcDir(), rel), 'utf8')

function walk(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    const full = join(dir, name)
    if (statSync(full).isDirectory()) walk(full, out)
    else out.push(full)
  }
  return out
}

/** 某个源文件里有没有对指定模块（说明符以 tail 结尾）的静态 import（`import … from '…tail'`，不含动态 import()）。 */
function staticallyImports(src: string, tail: string): boolean {
  const re = new RegExp(`^\\s*import\\s[^;]*?from\\s+['"][^'"]*${tail.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}['"]`, 'm')
  return re.test(src)
}

/** 登记表里的 name → 说明符（`name: () => import('…')`）。 */
function loaderTable(): Record<string, string> {
  const src = read('utils/lazyChunks.ts')
  const block = src.match(/export const chunkLoaders = \{([\s\S]*?)\n\} satisfies/)
  assert.ok(block, '找不到 chunkLoaders 登记表')
  const table: Record<string, string> = {}
  for (const m of block![1].matchAll(/(\w+):\s*\(\)\s*=>\s*import\('([^']+)'\)/g)) table[m[1]] = m[2]
  return table
}

describe('懒加载拆包接线（#186）', () => {
  it('登记表里每个块的说明符都指向存在的文件', () => {
    const table = loaderTable()
    assert.ok(Object.keys(table).length >= 9, `登记表应至少有 9 个块，实际 ${Object.keys(table).length}`)
    for (const [name, spec] of Object.entries(table)) {
      assert.ok(spec.startsWith('../'), `${name} 的说明符应是相对 utils/ 的路径：${spec}`)
      const file = join(srcDir(), 'utils', spec)
      assert.ok(existsSync(file), `${name} 指向的文件不存在：${spec}`)
    }
  })

  it('会话页 FeedbackView 不再静态引用被拆出去的整页与任务详情弹窗，而是走 lazyPage / lazyDialog', () => {
    const src = read('views/FeedbackView.vue')
    for (const tail of ['work/WorkPage.vue', 'group/GroupPage.vue', 'projects/ProjectsPage.vue', 'docs/DocsPage.vue', 'work/WorkItemDetail.vue']) {
      assert.equal(staticallyImports(src, tail), false, `FeedbackView 不应静态引用 ${tail}（会把它拉回入口）`)
    }
    for (const [v, loader, wrap] of [
      ['WorkPage', 'work', 'lazyPage'],
      ['GroupPage', 'group', 'lazyPage'],
      ['ProjectsPage', 'projects', 'lazyPage'],
      ['DocsPage', 'docs', 'lazyPage'],
      ['WorkItemDetail', 'workDetail', 'lazyDialog'],
    ]) {
      assert.match(src, new RegExp(`const ${v} = ${wrap}\\(chunkLoaders\\.${loader}\\)`), `${v} 应是 ${wrap}(chunkLoaders.${loader})`)
    }
    // 任务详情弹窗第一次要打开时才挂载，挂上之后保持挂载
    assert.match(src, /const taskPreviewMounted = ref\(taskPreviewOpen\.value\)/)
    assert.match(src, /<WorkItemDetail\s+v-if="taskPreviewMounted"/)
  })

  it('设置对话框：外壳在入口、内容区（SettingsForm）懒加载；整页设置仍静态引用同一个模块', () => {
    const dlg = read('components/SettingsDialog.vue')
    assert.equal(staticallyImports(dlg, 'SettingsForm.vue'), false, 'SettingsDialog 不应静态引用 SettingsForm')
    assert.match(dlg, /const SettingsForm = lazyPart\(chunkLoaders\.settings\)/)
    // 内容区是懒加载的：打开时分类标签可能还没渲染，落焦点要等它出现（逐帧找，有上限，新一次打开会放弃旧的）
    assert.match(dlg, /requestAnimationFrame\(tryFocus\)/)
    assert.match(dlg, /CATEGORY_FOCUS_MAX_WAIT_MS/)
    assert.match(dlg, /mine !== focusAttempt/)
    assert.match(read('views/SettingsView.vue'), /import SettingsForm from '\.\.\/components\/SettingsForm\.vue'/)
  })

  it('全局弹窗：解锁与预设更新只在第一次需要时才挂载；预设「已自动更新」提示留在 App.vue（不依赖弹窗挂载）', () => {
    const app = read('App.vue')
    assert.equal(staticallyImports(app, 'terminal/UnlockDialog.vue'), false)
    assert.equal(staticallyImports(app, 'settings/PresetUpdateDialog.vue'), false)
    assert.match(app, /const UnlockDialog = lazyDialog\(chunkLoaders\.unlock, \(\) => terminalStore\.settleUnlock\(false\)\)/)
    assert.match(app, /const PresetUpdateDialog = lazyDialog\(chunkLoaders\.presetUpdate\)/)
    assert.match(app, /<UnlockDialog v-if="unlockMounted" \/>/)
    assert.match(app, /<PresetUpdateDialog v-if="presetMounted" \/>/)
    assert.match(app, /watch\(\s*\(\) => settingsStore\.presetUpgrade/)
    assert.doesNotMatch(read('components/settings/PresetUpdateDialog.vue'), /presetUpgrade/, '弹窗里不该再有「已自动更新」提示（它只在出现冲突时才挂载）')
  })

  it('「启用 Agent」弹窗：懒加载，只看 armed 不看按钮是否还在（见 newSession.test.ts 的行为断言）', () => {
    const src = read('components/feedback/SessionEnableAgent.vue')
    assert.equal(staticallyImports(src, 'terminal/EnableAgentDialog.vue'), false)
    assert.match(src, /const EnableAgentDialog = lazyDialog\(chunkLoaders\.enableAgent\)/)
  })

  it('每个 data-prefetch 都对应登记表里的块；入口、空闲预取与接口计数都接上了', () => {
    const names = new Set(Object.keys(loaderTable()))
    const used = new Set<string>()
    for (const file of walk(srcDir())) {
      if (!file.endsWith('.vue')) continue
      for (const m of readFileSync(file, 'utf8').matchAll(/data-prefetch="([^"]+)"/g)) used.add(m[1])
    }
    assert.ok(used.size >= 5, `应至少有 5 种入口带 data-prefetch，实际 ${[...used].join(',')}`)
    for (const n of used) assert.ok(names.has(n), `data-prefetch="${n}" 不在登记表里`)

    assert.match(read('App.vue'), /installPrefetchHints\(\)/)
    assert.match(read('layouts/AppLayout.vue'), /void startIdlePrefetch\(\)/)
    const client = read('api/client.ts')
    assert.match(client, /netIdle\.begin\(\)/)
    assert.match(client, /response => \{\s*netIdle\.end\(\)/)
    assert.match(client, /error => \{\s*netIdle\.end\(\)/)
  })

  it('空闲预取的顺序里没有登记表之外的名字，也没有漏掉的块', () => {
    const names = Object.keys(loaderTable())
    const src = read('utils/lazyChunks.ts')
    const order = src.match(/const IDLE_ORDER: ChunkName\[\] = \[([^\]]*)\]/)
    assert.ok(order, '找不到 IDLE_ORDER')
    const listed = [...order![1].matchAll(/'(\w+)'/g)].map(m => m[1])
    assert.deepEqual([...listed].sort(), [...names].sort(), 'IDLE_ORDER 应恰好包含登记表里的全部块')
  })
})
