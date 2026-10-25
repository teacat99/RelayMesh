// 生产构建的 Brotli 预压缩（#186，vite.config.ts 与单元测试共用）。
//
// 为什么在构建时压：服务端原来对前端脚本 / 样式做的是请求时的动态 gzip（级别 4，见 internal/api/gzip.go），
// 首屏要下载的 4 个脚本约 419 KB gzip（体积报告口径是级别 6；服务端实际级别 4，会略大一点），同样内容用 Brotli 级别 11 只有约 339 KB（-19%）；
// 样式 25.5 → 19.6 KB。合计少下约 86 KB，生产带宽约 1～3 Mbps，首访因此快约 0.2～0.7 秒（量化见 #186 的决策记录）。
// 级别 11 压一次要几秒，只适合构建时做、随产物一起提交，运行时由 internal/api/precompressed.go 按 Accept-Encoding 直接发 .br（不占 CPU）。
//
// 只处理 outDir/assets 下的 .js / .css（Vite 输出的带哈希产物，缓存一年且 immutable）：字体已经是 woff2、index.html 很小，不值得。
// 每个产物旁写一个同名加 .br 的文件（index-Abc.js → index-Abc.js.br）；压完立刻解压回来逐字节比对，不一致直接让构建失败，
// 免得把坏文件随 web/dist 提交出去（浏览器收到损坏的 br 会整页白屏）。
//
// 只依赖 node 内置模块：单元测试（src/lib/precompress.test.ts）把本文件一起打包，不拖进 vite。
import { readdirSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { brotliCompressSync, brotliDecompressSync, constants } from 'node:zlib'

/** 小于它的产物不压：省下的字节抵不过多一个文件与一次内容协商（内嵌进二进制也要占空间）。internal/api 与 web/dist_test.go 的假设与此一致。 */
export const PRECOMPRESS_MIN_BYTES = 1024
/** 压缩后必须比原文件至少小这个比例才留下 .br（没压出明显收益的文件，直接走原文件 / gzip 更省事）。 */
export const PRECOMPRESS_MIN_GAIN = 0.1
/** 值得预压缩的文件：只看扩展名。 */
export const PRECOMPRESS_EXT = /\.(js|css)$/

export interface PrecompressEntry {
  /** 相对 outDir 的路径（正斜杠），如 assets/index-Abc.js */
  file: string
  raw: number
  br: number
  /** 没留下 .br 的原因（留下了就没有这个字段） */
  skipped?: 'small' | 'no-gain'
}

/** 对一段内容做 Brotli 压缩（级别 11、文本模式、最大 24 位窗口，供浏览器标准解码器解开）。 */
export function brotliBest(data: Buffer): Buffer {
  return brotliCompressSync(data, {
    params: {
      [constants.BROTLI_PARAM_QUALITY]: constants.BROTLI_MAX_QUALITY,
      [constants.BROTLI_PARAM_MODE]: constants.BROTLI_MODE_TEXT,
      [constants.BROTLI_PARAM_LGWIN]: constants.BROTLI_MAX_WINDOW_BITS,
      [constants.BROTLI_PARAM_SIZE_HINT]: data.length,
    },
  })
}

/**
 * 给 outDir/assets 下的 .js / .css 各写一个同目录的 .br，返回每个文件的处理结果（按文件名排序）。
 * 同名 .br 会被覆盖；原文件已不存在的旧 .br（上一次构建的残留）会被删掉；解压比对不一致时抛错。
 */
export function precompressAssets(outDir: string): PrecompressEntry[] {
  const assets = join(outDir, 'assets')
  let names: string[]
  try {
    names = readdirSync(assets).sort()
  } catch {
    return [] // 没有 assets 目录（例如只复制了 public/）：没有什么要压的
  }
  const present = new Set(names)

  // 残留清理：x.js.br 的原文件 x.js 不在了，说明是旧构建留下的
  for (const name of names) {
    if (name.endsWith('.br') && !present.has(name.slice(0, -3))) rmSync(join(assets, name), { force: true })
  }

  const out: PrecompressEntry[] = []
  for (const name of names) {
    if (!PRECOMPRESS_EXT.test(name)) continue
    const full = join(assets, name)
    if (!statSync(full).isFile()) continue
    const raw = readFileSync(full)
    const file = `assets/${name}`
    if (raw.length < PRECOMPRESS_MIN_BYTES) {
      rmSync(`${full}.br`, { force: true })
      out.push({ file, raw: raw.length, br: 0, skipped: 'small' })
      continue
    }
    const packed = brotliBest(raw)
    if (packed.length > raw.length * (1 - PRECOMPRESS_MIN_GAIN)) {
      rmSync(`${full}.br`, { force: true })
      out.push({ file, raw: raw.length, br: packed.length, skipped: 'no-gain' })
      continue
    }
    if (!brotliDecompressSync(packed).equals(raw)) {
      throw new Error(`Brotli 预压缩自检失败：${file} 解压后与原文件不一致`)
    }
    writeFileSync(`${full}.br`, packed)
    out.push({ file, raw: raw.length, br: packed.length })
  }
  return out
}
