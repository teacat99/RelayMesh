// 运行：pnpm --dir frontend run test:unit
// 引用装饰（composables/useLinkRefs.ts）的源码级防线：会话号引用要靠「选中的会话属于本工作流」才解析得了，
// 直接打开文档页时正文常常比「选中默认会话」先到，选中之后装饰必须重来一次，才会把整表按需取回来（load-budget.mjs 第 7 步守着）。
import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, it } from 'node:test'

function srcDir(): string {
  for (const base of [join(process.cwd(), 'src'), join(process.cwd(), 'frontend', 'src')]) {
    if (existsSync(join(base, 'composables', 'useLinkRefs.ts'))) return base
  }
  throw new Error('找不到 frontend/src，请在仓库根目录或 frontend 目录下运行测试')
}

describe('useLinkRefs：会话号引用的装饰（源码级防线）', () => {
  const src = readFileSync(join(srcDir(), 'composables', 'useLinkRefs.ts'), 'utf8')

  it('「选中的会话属于本工作流」是一个独立的计算属性，会话号解析与按需取整表都用它', () => {
    assert.match(src, /const sessionScopeReady = computed\(\(\) => \{[\s\S]*?\(sel\?\.workflow_id \|\| sel\?\.session_id \|\| ''\) === id[\s\S]*?\}\)/)
    const byHash = src.slice(src.indexOf('const sessionByHash = computed'), src.indexOf('/** 任何一种来源的「存在集合」'))
    // #186 C2a：索引本身在 store 里共用一份（sessionStore.sessionByHash），这里只做「选中归属」的判断，不属于时是空索引
    assert.ok(byHash.includes('sessionScopeReady.value') && byHash.includes('sessionStore.sessionByHash') && byHash.includes('NO_SESSIONS'), 'sessionByHash 要用 sessionScopeReady 判断选中归属，不属于时是空索引')
    const ensure = src.slice(src.indexOf('function ensureSessions()'), src.indexOf('/** 当前是不是正显示这个工作流的会话页'))
    assert.ok(ensure.includes('!sessionScopeReady.value') && ensure.includes('ensureWorkflowSessions(id)'), 'ensureSessions 要用 sessionScopeReady 判断选中归属')
  })

  it('选中归属在签名里：选中晚于正文到达时签名会变，装饰重来、ensureSessions 才会重新问一遍', () => {
    const sig = src.slice(src.indexOf('const signature = computed'), src.indexOf('const index: LinkRefIndex'))
    assert.ok(sig.includes('sessionScopeReady.value'), '整表没加载时 sessionByHash 是空的、大小不变，没有这一项签名就不变，会话号引用会一直是纯文本')
  })
})
