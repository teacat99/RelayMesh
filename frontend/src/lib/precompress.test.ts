// 运行：pnpm --dir frontend run test:unit
// 构建时 Brotli 预压缩（build.precompress.ts，#186）：哪些文件会被压、压完能否原样解开、没收益 / 太小的不留 .br、
// 旧构建残留的 .br 会被清掉。服务端按 .br 直接发给浏览器，文件一旦损坏整页白屏，所以「解压回来逐字节一致」是硬要求。
import assert from 'node:assert/strict'
import { randomFillSync } from 'node:crypto'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterEach, beforeEach, describe, it } from 'node:test'
import { brotliDecompressSync } from 'node:zlib'
import { PRECOMPRESS_MIN_BYTES, brotliBest, precompressAssets } from '../../build.precompress'

let dir = ''
beforeEach(() => {
  dir = mkdtempSync(join(tmpdir(), 'rm-precompress-'))
  mkdirSync(join(dir, 'assets'))
})
afterEach(() => rmSync(dir, { recursive: true, force: true }))

const put = (name: string, data: string | Buffer) => writeFileSync(join(dir, 'assets', name), data)
const has = (name: string) => existsSync(join(dir, 'assets', name))
const text = (n: number) => 'export const x = "relaymesh lazy chunk";\n'.repeat(n)

describe('precompressAssets', () => {
  it('为带哈希的 js / css 各写一个 .br，解压回来与原文件逐字节一致', () => {
    const js = text(400)
    const css = '.a{color:red}\n'.repeat(500)
    put('index-AbCd1234.js', js)
    put('index-AbCd1234.css', css)
    const out = precompressAssets(dir)
    assert.deepEqual(out.map(e => e.file), ['assets/index-AbCd1234.css', 'assets/index-AbCd1234.js'])
    assert.equal(brotliDecompressSync(readFileSync(join(dir, 'assets', 'index-AbCd1234.js.br'))).toString(), js)
    assert.equal(brotliDecompressSync(readFileSync(join(dir, 'assets', 'index-AbCd1234.css.br'))).toString(), css)
    for (const e of out) {
      assert.equal(e.skipped, undefined)
      assert.ok(e.br > 0 && e.br < e.raw * 0.9, `${e.file} 应明显变小: ${e.raw} → ${e.br}`)
    }
  })

  it('不碰 js / css 以外的文件（字体、图片已经压过）', () => {
    put('font-AbCd1234.woff2', text(400))
    put('logo-AbCd1234.svg', text(400))
    put('data-AbCd1234.json', text(400))
    assert.deepEqual(precompressAssets(dir), [])
    assert.deepEqual(readdirSync(join(dir, 'assets')).sort(), ['data-AbCd1234.json', 'font-AbCd1234.woff2', 'logo-AbCd1234.svg'])
  })

  it('小于下限的文件不压，也不留 .br（含上一次构建遗留的同名 .br）', () => {
    put('tiny-AbCd1234.js', 'x'.repeat(PRECOMPRESS_MIN_BYTES - 1))
    put('tiny-AbCd1234.js.br', 'stale')
    const out = precompressAssets(dir)
    assert.deepEqual(out, [{ file: 'assets/tiny-AbCd1234.js', raw: PRECOMPRESS_MIN_BYTES - 1, br: 0, skipped: 'small' }])
    assert.equal(has('tiny-AbCd1234.js.br'), false)
  })

  it('刚好等于下限的文件会压', () => {
    put('edge-AbCd1234.js', 'a'.repeat(PRECOMPRESS_MIN_BYTES))
    const out = precompressAssets(dir)
    assert.equal(out[0].skipped, undefined)
    assert.ok(has('edge-AbCd1234.js.br'))
  })

  it('压不出收益的文件（随机数据）不留 .br，走原文件', () => {
    const noise = Buffer.alloc(8192)
    randomFillSync(noise)
    put('noise-AbCd1234.js', noise)
    const out = precompressAssets(dir)
    assert.equal(out[0].skipped, 'no-gain')
    assert.equal(has('noise-AbCd1234.js.br'), false)
  })

  it('原文件已不存在的旧 .br 会被清掉，仍然存在的会被覆盖', () => {
    put('gone-AbCd1234.js.br', 'stale') // 对应的 gone-AbCd1234.js 不在了
    put('keep-AbCd1234.js', text(300))
    put('keep-AbCd1234.js.br', 'stale')
    precompressAssets(dir)
    assert.equal(has('gone-AbCd1234.js.br'), false)
    assert.equal(brotliDecompressSync(readFileSync(join(dir, 'assets', 'keep-AbCd1234.js.br'))).toString(), text(300))
  })

  it('重复运行结果一致（幂等）', () => {
    put('a-AbCd1234.js', text(300))
    precompressAssets(dir)
    const first = readFileSync(join(dir, 'assets', 'a-AbCd1234.js.br'))
    precompressAssets(dir)
    assert.ok(first.equals(readFileSync(join(dir, 'assets', 'a-AbCd1234.js.br'))))
  })

  it('没有 assets 目录时什么也不做', () => {
    rmSync(join(dir, 'assets'), { recursive: true })
    assert.deepEqual(precompressAssets(dir), [])
  })
})

describe('brotliBest', () => {
  it('输出是标准 Brotli 流：默认参数的解码器能解开', () => {
    const data = Buffer.from(text(200))
    const packed = brotliBest(data)
    assert.ok(packed.length < data.length / 10)
    assert.ok(brotliDecompressSync(packed).equals(data))
  })
})
