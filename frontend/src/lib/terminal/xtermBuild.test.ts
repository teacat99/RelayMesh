// 运行：pnpm --dir frontend run test:unit
//
// 生产构建回归：用与 vite build 相同的目标（build.target.ts）和压缩设置重新编译 xterm.js，再真的执行一遍。
// 起因见 build.target.ts：esbuild 0.21.x 会把 DECRQM 处理函数编坏，导致解析器抛异常、写入队列永久停摆。
// 只在源码层面跑 xterm.js 测不出这类问题，所以这里测的是「压缩降级之后的产物」。
import assert from 'node:assert/strict'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { pathToFileURL } from 'node:url'
import { after, before, describe, it } from 'node:test'
import { transformSync } from 'esbuild'
import { JSDOM } from 'jsdom'
import { BUILD_TARGET } from '../../../build.target'

type XtermTerminal = import('@xterm/xterm').Terminal
type XtermCtor = new (opts: { cols: number; rows: number; allowProposedApi: boolean }) => XtermTerminal

const E = '\x1b'

let dom: JSDOM | null = null
function installDom(): void {
  if (dom) return
  // xterm.js 只在浏览器里能跑，需要先准备最小的 DOM 环境；必须先于模块求值。
  dom = new JSDOM('<!doctype html><html><body></body></html>', { pretendToBeVisual: true })
  const g = globalThis as unknown as Record<string, unknown>
  g.window = dom.window
  g.document = dom.window.document
  Object.defineProperty(globalThis, 'navigator', { value: dom.window.navigator, configurable: true })
}

/** 按给定目标压缩 xterm.js 的 ESM 构建，落盘后加载，返回 Terminal 构造函数。 */
async function loadMinified(target: string[], dir: string, name: string): Promise<XtermCtor> {
  installDom()
  const require = createRequire(import.meta.url)
  const source = readFileSync(require.resolve('@xterm/xterm/lib/xterm.mjs'), 'utf8')
  const { code } = transformSync(source, { minify: true, target, format: 'esm', legalComments: 'none' })
  const file = join(dir, name)
  writeFileSync(file, code)
  const mod = (await import(pathToFileURL(file).href)) as { Terminal: XtermCtor }
  return mod.Terminal
}

/** 写入并等待解析完成；超过 timeoutMs 仍未回调即视为写入队列卡死。 */
function write(t: XtermTerminal, data: string, timeoutMs = 400): Promise<'done' | 'stuck'> {
  return new Promise(resolve => {
    const timer = setTimeout(() => resolve('stuck'), timeoutMs)
    t.write(data, () => {
      clearTimeout(timer)
      resolve('done')
    })
  })
}

function collect(t: XtermTerminal): string[] {
  const out: string[] = []
  t.onData(d => out.push(d))
  return out
}

/** 终端会应答 / 会改变状态的各类序列；任何一条把写入队列卡死都是严重故障。 */
function corpus(): [string, string][] {
  const seqs: [string, string][] = []
  const modes = [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 12, 20, 25, 45, 47, 66, 67, 1000, 1002, 1003, 1004, 1005, 1006, 1015, 1016, 1047, 1048, 1049, 2004, 2026, 2027, 65535, 99999999999]
  for (const m of modes) {
    seqs.push([`DECRQM ?${m}`, `${E}[?${m}$p`])
    seqs.push([`DECRQM ${m}`, `${E}[${m}$p`])
  }
  for (const m of [1, 6, 7, 9, 12, 25, 45, 47, 66, 1000, 1002, 1003, 1004, 1005, 1006, 1015, 1016, 1047, 1048, 1049, 2004, 2026]) {
    for (const f of ['h', 'l', 's', 'r']) seqs.push([`DECSET ?${m}${f}`, `${E}[?${m}${f}`])
  }
  for (const m of [2, 4, 12, 20]) for (const f of ['h', 'l']) seqs.push([`SM/RM ${m}${f}`, `${E}[${m}${f}`])
  const other: [string, string][] = [
    ['DA1', `${E}[c`],
    ['DA1 0', `${E}[0c`],
    ['DA2', `${E}[>c`],
    ['DA3', `${E}[=c`],
    ['CPR', `${E}[6n`],
    ['DSR 5', `${E}[5n`],
    ['DECXCPR', `${E}[?6n`],
    ['DSR ?15', `${E}[?15n`],
    ['DSR ?25', `${E}[?25n`],
    ['DSR ?26', `${E}[?26n`],
    ['DSR ?996', `${E}[?996n`],
    ['XTVERSION', `${E}[>q`],
    ['XTVERSION 0', `${E}[>0q`],
    ['kitty kbd query', `${E}[?u`],
    ['kitty kbd push', `${E}[>1u`],
    ['kitty kbd pop', `${E}[<u`],
    ['modifyOtherKeys', `${E}[>4;2m`],
    ['DECID', `${E}Z`],
    ['DECREQTPARM', `${E}[1x`],
    ['window 14t', `${E}[14t`],
    ['window 16t', `${E}[16t`],
    ['window 18t', `${E}[18t`],
    ['window 19t', `${E}[19t`],
    ['window 20t', `${E}[20t`],
    ['window 21t', `${E}[21t`],
    ['window 22t', `${E}[22;0t`],
    ['window 23t', `${E}[23;0t`],
    ['DECSCUSR', `${E}[5 q`],
    ['DECSTR', `${E}[!p`],
    ['RIS', `${E}c`],
    ['DECALN', `${E}#8`],
    ['DECRQSS SGR', `${E}P$qm${E}\\`],
    ['DECRQSS DECSTBM', `${E}P$qr${E}\\`],
    ['DECRQSS DECSCUSR', `${E}P$q q${E}\\`],
    ['XTGETTCAP TN', `${E}P+q544e${E}\\`],
    ['OSC 4 query', `${E}]4;1;?${E}\\`],
    ['OSC 10 query', `${E}]10;?${E}\\`],
    ['OSC 11 query', `${E}]11;?${E}\x07`],
    ['OSC 12 query', `${E}]12;?${E}\\`],
    ['OSC 52 query', `${E}]52;c;?${E}\\`],
    ['OSC 52 set', `${E}]52;c;aGVsbG8=${E}\\`],
    ['OSC 8 hyperlink', `${E}]8;;http://example.com${E}\\link${E}]8;;${E}\\`],
    ['OSC 133 marks', `${E}]133;A${E}\\${E}]133;B${E}\\${E}]133;C${E}\\${E}]133;D;0${E}\\`],
    ['OSC title', `${E}]0;title${E}\\${E}]2;t2\x07`],
    ['OSC reset colors', `${E}]104${E}\\${E}]110${E}\\${E}]111${E}\\${E}]112${E}\\`],
    ['SGR truecolor', `${E}[38;2;1;2;3m${E}[48;5;200m${E}[58;2;1;2;3m${E}[4:3m${E}[0m`],
    ['alt screen round trip', `${E}[?1049hhello${E}[?1049l`],
    ['scroll region', `${E}[2;10r${E}[?6h${E}[1;1H${E}[?6l${E}[r`],
    ['wide + emoji', '你好世界 \ud83d\ude00 \u200d \ufe0f é'],
    ['sixel', `${E}Pq#0;2;0;0;0#1;2;100;100;100#1~~@@vv@@~~@@~~$#0??}}GG}}??}}??-#1!14@${E}\\`],
    ['vim 9 startup burst', `${E}[?12$p${E}[6n${E}[>c${E}[c${E}P+q436f${E}\\${E}]10;?${E}\\${E}]11;?${E}\\${E}[?1049h${E}[>4;2m${E}[?1h${E}=`],
    ['tmux startup burst', `${E}[c${E}[>c${E}[>q${E}]10;?${E}\\${E}]11;?${E}\\${E}[?2004h${E}[?1004h${E}[?2026$p`],
  ]
  return seqs.concat(other)
}

describe('生产构建后的 xterm.js（压缩 + 目标降级）', () => {
  let dir: string
  let Terminal: XtermCtor

  before(async () => {
    dir = mkdtempSync(join(tmpdir(), 'relaymesh-xterm-'))
    Terminal = await loadMinified(BUILD_TARGET, dir, 'xterm.release.mjs')
  })

  after(() => {
    rmSync(dir, { recursive: true, force: true })
  })

  it('DECRQM 得到 xterm.js 自己的应答（vim 启动时就会发 ?12$p）', async () => {
    const t = new Terminal({ cols: 80, rows: 24, allowProposedApi: true })
    const out = collect(t)
    assert.equal(await write(t, `${E}[?12$p`), 'done', '写入队列在 DECRQM 之后卡死：检查 build.target 是否被改低')
    assert.equal(await write(t, `${E}[4$p`), 'done')
    assert.equal(await write(t, 'hello'), 'done')
    assert.deepEqual(out, [`${E}[?12;2$y`, `${E}[4;2$y`])
    assert.equal(t.buffer.active.getLine(0)?.translateToString(true), 'hello')
    t.dispose()
  })

  it('应答反映真实状态：备用屏、括号粘贴、鼠标、光标、同步输出', async () => {
    const t = new Terminal({ cols: 80, rows: 24, allowProposedApi: true })
    const out = collect(t)
    const ask = async (seq: string): Promise<string> => {
      out.length = 0
      assert.equal(await write(t, seq), 'done', seq)
      return out.join('')
    }
    assert.equal(await ask(`${E}[?1049$p`), `${E}[?1049;2$y`)
    await write(t, `${E}[?1049h`)
    assert.equal(t.buffer.active.type, 'alternate')
    assert.equal(await ask(`${E}[?1049$p`), `${E}[?1049;1$y`)
    await write(t, `${E}[?1049l`)
    assert.equal(await ask(`${E}[?1049$p`), `${E}[?1049;2$y`)

    assert.equal(await ask(`${E}[?2004$p`), `${E}[?2004;2$y`)
    await write(t, `${E}[?2004h`)
    assert.equal(await ask(`${E}[?2004$p`), `${E}[?2004;1$y`)

    assert.equal(await ask(`${E}[?1000$p`), `${E}[?1000;2$y`)
    await write(t, `${E}[?1000h`)
    assert.equal(await ask(`${E}[?1000$p`), `${E}[?1000;1$y`)

    assert.equal(await ask(`${E}[?25$p`), `${E}[?25;1$y`)
    await write(t, `${E}[?25l`)
    assert.equal(await ask(`${E}[?25$p`), `${E}[?25;2$y`)

    assert.equal(await ask(`${E}[?9999$p`), `${E}[?9999;0$y`)
    t.dispose()
  })

  it('整套查询 / 模式序列：每一条都不会让写入队列停摆', async () => {
    const stuck: string[] = []
    const seqs = corpus()
    for (const [name, seq] of seqs) {
      const t = new Terminal({ cols: 80, rows: 24, allowProposedApi: true })
      const first = await write(t, seq)
      const second = first === 'done' ? await write(t, 'x') : 'stuck'
      if (first !== 'done' || second !== 'done') stuck.push(name)
      t.dispose()
    }
    assert.deepEqual(stuck, [], `${stuck.length}/${seqs.length} 条序列让 xterm.js 写入队列停摆`)
  })
})
