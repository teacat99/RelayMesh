#!/usr/bin/env node
// 前端产物体积报告（#186）：改依赖或拆包之前之后各跑一次，用同一张表对比。
//
//   node scripts/bundle-report.mjs                         # 构建到临时目录（不动 web/dist），打印三张表
//   node scripts/bundle-report.mjs --json /tmp/before.json # 同时存一份机器可读的结果
//   node scripts/bundle-report.mjs --compare /tmp/before.json   # 与之前存的结果逐块对比
//   node scripts/bundle-report.mjs --root /tmp/other/frontend   # 分析另一份检出（要能解析到 node_modules，见 README 的「基线」做法）
//   选项：--no-brotli（省掉 brotli 11 级压缩，快很多，brotli 列留空）、--top <n>（块表只列前 n 个，默认 30）
//
// 三张表：
//   1. 汇总：块数、「首屏同步」合计（入口脚本 + 它静态引用的全部块 + 这些块带的样式与字体 + index.html）——这才是首访必须下载的量；
//   2. 块：每个块的原始 / gzip / brotli 大小、是否在首屏同步集合里；
//   3. 入口脚本构成：把入口里的模块按来源（src 下的目录 / npm 包）归并，按「渲染后体积」折算到入口最终大小（压缩前的渲染体积按比例缩放，是估算）。
// 压缩口径：gzip 级别 6（Node 默认）、brotli 质量 11；服务端现在是动态 gzip 级别 4（internal/api/gzip.go），所以线上实际 gzip 会略大一点，对比时同口径即可。
// 构建本身与 `pnpm build` 一样用 vite.config.ts（含 manualChunks 与 build.target），只是 outDir 换成临时目录、多挂了一个只读的收集插件。
import { build } from 'vite'
import { brotliCompressSync, constants as zc, gzipSync } from 'node:zlib'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const here = path.dirname(fileURLToPath(import.meta.url))

function parseArgs(argv) {
  const opts = { root: path.resolve(here, '..'), json: '', compare: '', brotli: true, top: 30 }
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i]
    if (a === '--json') opts.json = argv[++i]
    else if (a === '--compare') opts.compare = argv[++i]
    else if (a === '--root') opts.root = path.resolve(argv[++i])
    else if (a === '--no-brotli') opts.brotli = false
    else if (a === '--top') opts.top = Number(argv[++i])
    else if (a === '-h' || a === '--help') {
      console.log(fs.readFileSync(fileURLToPath(import.meta.url), 'utf8').split('\n').slice(1, 17).map(l => l.replace(/^\/\/ ?/, '')).join('\n'))
      process.exit(0)
    } else {
      console.error(`未知参数：${a}（--help 看用法）`)
      process.exit(2)
    }
  }
  return opts
}

const kb = n => (n == null ? '—' : (n / 1024).toFixed(1))

/** 去掉文件名里的 8 位内容哈希，得到跨构建稳定的名字（assets/index-VCZ8MsA4.js → assets/index.js）。 */
function stableKey(file) {
  return file.replace(/-[A-Za-z0-9_-]{8}(\.[A-Za-z0-9]+)$/, '$1')
}

/** 模块归并到来源：npm 包（取最后一个 node_modules 之后的包名）或 src 下的目录（components 取两层）。 */
function groupOf(id, srcRoot) {
  const norm = id.replace(/\\/g, '/')
  if (norm.startsWith('\0') || !norm.startsWith('/')) return '(构建辅助)'
  const nm = norm.lastIndexOf('/node_modules/')
  if (nm >= 0) {
    const parts = norm.slice(nm + '/node_modules/'.length).split('/')
    return 'npm:' + (parts[0].startsWith('@') ? `${parts[0]}/${parts[1]}` : parts[0])
  }
  if (norm.startsWith(srcRoot + '/')) {
    const parts = norm.slice(srcRoot.length + 1).split('/')
    if (parts.length === 1) return 'src(根)'
    return parts[0] === 'components' && parts.length > 2 ? `src/components/${parts[1]}` : `src/${parts[0]}`
  }
  return '(其它)'
}

function collector(sink, withBrotli) {
  const gz = buf => gzipSync(buf, { level: 6 }).length
  const br = buf =>
    withBrotli ? brotliCompressSync(buf, { params: { [zc.BROTLI_PARAM_QUALITY]: 11, [zc.BROTLI_PARAM_SIZE_HINT]: buf.length } }).length : null
  return {
    name: 'rm-bundle-report',
    apply: 'build',
    enforce: 'post',
    generateBundle(_opts, bundle) {
      for (const [file, item] of Object.entries(bundle)) {
        if (item.type === 'chunk') {
          const buf = Buffer.from(item.code)
          sink.push({
            file,
            kind: 'chunk',
            name: item.name,
            isEntry: item.isEntry,
            isDynamicEntry: item.isDynamicEntry,
            raw: buf.length,
            gzip: gz(buf),
            brotli: br(buf),
            imports: [...item.imports],
            dynamicImports: [...item.dynamicImports],
            css: [...(item.viteMetadata?.importedCss ?? [])],
            assets: [...(item.viteMetadata?.importedAssets ?? [])],
            modules: Object.entries(item.modules).map(([id, m]) => ({ id, rendered: m.renderedLength })),
          })
        } else {
          const buf = Buffer.from(item.source)
          sink.push({ file, kind: 'asset', raw: buf.length, gzip: gz(buf), brotli: br(buf) })
        }
      }
    },
  }
}

function analyze(items, srcRoot) {
  const byFile = new Map(items.map(i => [i.file, i]))
  const entry = items.find(i => i.kind === 'chunk' && i.isEntry)
  if (!entry) throw new Error('没有找到入口块（isEntry）')

  // 首屏同步集合：入口 + 静态 imports 的传递闭包
  const initial = new Set()
  const walk = f => {
    if (initial.has(f)) return
    initial.add(f)
    for (const dep of byFile.get(f)?.imports ?? []) walk(dep)
  }
  walk(entry.file)

  const initialFiles = new Set(initial)
  for (const f of initial) {
    const c = byFile.get(f)
    for (const x of [...(c?.css ?? []), ...(c?.assets ?? [])]) initialFiles.add(x)
  }
  if (byFile.has('index.html')) initialFiles.add('index.html')

  // 稳定名：同名（例如两个 index）加序号
  const seen = new Map()
  for (const it of items) {
    let key = stableKey(it.file)
    const n = (seen.get(key) ?? 0) + 1
    seen.set(key, n)
    if (n > 1) key += `#${n}`
    it.key = key
    it.initial = initialFiles.has(it.file)
  }

  const sum = (arr, f) => arr.reduce((s, i) => s + (i[f] ?? 0), 0)
  const hasBr = items.every(i => i.brotli != null)
  const initialItems = items.filter(i => i.initial)
  const js = initialItems.filter(i => i.kind === 'chunk')
  const css = initialItems.filter(i => i.file.endsWith('.css'))
  const other = initialItems.filter(i => i.kind === 'asset' && !i.file.endsWith('.css'))
  const part = list => ({ n: list.length, raw: sum(list, 'raw'), gzip: sum(list, 'gzip'), brotli: hasBr ? sum(list, 'brotli') : null })
  const summary = {
    chunks: items.filter(i => i.kind === 'chunk').length,
    totalRaw: sum(items, 'raw'),
    totalGzip: sum(items, 'gzip'),
    entry: { file: entry.file, raw: entry.raw, gzip: entry.gzip, brotli: entry.brotli },
    initialJs: part(js),
    initialCss: part(css),
    initialOther: part(other),
    initialAll: part(initialItems),
  }

  // 入口构成
  const groups = new Map()
  const renderedTotal = sum(entry.modules, 'rendered') || 1
  for (const m of entry.modules) {
    const g = groupOf(m.id, srcRoot)
    const cur = groups.get(g) ?? { rendered: 0, modules: 0 }
    cur.rendered += m.rendered
    cur.modules += 1
    groups.set(g, cur)
  }
  const entryGroups = [...groups.entries()]
    .map(([group, v]) => ({
      group,
      modules: v.modules,
      share: v.rendered / renderedTotal,
      estRaw: Math.round((v.rendered / renderedTotal) * entry.raw),
      estGzip: Math.round((v.rendered / renderedTotal) * entry.gzip),
    }))
    .sort((a, b) => b.share - a.share)

  // 重复模块：同一个模块（有渲染体积）出现在多个块里
  const where = new Map()
  for (const c of items.filter(i => i.kind === 'chunk')) {
    for (const m of c.modules) {
      if (m.rendered <= 0) continue
      const list = where.get(m.id) ?? []
      list.push({ key: c.key, rendered: m.rendered })
      where.set(m.id, list)
    }
  }
  const duplicates = [...where.entries()]
    .filter(([, l]) => l.length > 1)
    .map(([id, l]) => ({ id: path.relative(srcRoot, id), chunks: l.map(x => x.key), rendered: l[0].rendered }))
    .sort((a, b) => b.rendered * b.chunks.length - a.rendered * a.chunks.length)

  return { summary, entryGroups, duplicates, chunks: items.map(({ modules, ...rest }) => rest) }
}

function printReport(r, top) {
  const s = r.summary
  const row = p => `${p.n} 个文件：原始 ${kb(p.raw)} KB / gzip ${kb(p.gzip)} KB / brotli ${kb(p.brotli)} KB`
  console.log('## 汇总\n')
  console.log(`- 块 ${s.chunks} 个；全部产物原始 ${kb(s.totalRaw)} KB / gzip ${kb(s.totalGzip)} KB`)
  console.log(`- 入口脚本 \`${s.entry.file}\`：原始 ${kb(s.entry.raw)} KB / gzip ${kb(s.entry.gzip)} KB / brotli ${kb(s.entry.brotli)} KB`)
  console.log(`- **首屏同步合计（首访必须下载）**：${row(s.initialAll)}`)
  console.log(`  - 脚本：${row(s.initialJs)}`)
  console.log(`  - 样式：${row(s.initialCss)}`)
  console.log(`  - 其它（字体、index.html）：${row(s.initialOther)}`)

  console.log(`\n## 块（按 gzip 降序，前 ${top} 个；「首屏」= 在首屏同步集合里）\n`)
  console.log('| 块 | 原始 KB | gzip KB | brotli KB | 首屏 |')
  console.log('|---|---:|---:|---:|:---:|')
  for (const c of [...r.chunks].sort((a, b) => b.gzip - a.gzip).slice(0, top)) {
    console.log(`| ${c.key} | ${kb(c.raw)} | ${kb(c.gzip)} | ${kb(c.brotli)} | ${c.initial ? '是' : '—'} |`)
  }

  console.log('\n## 入口脚本构成（按来源，估算值）\n')
  console.log('| 来源 | 模块数 | 占比 | 估算原始 KB | 估算 gzip KB |')
  console.log('|---|---:|---:|---:|---:|')
  for (const g of r.entryGroups.slice(0, 25)) {
    console.log(`| ${g.group} | ${g.modules} | ${(g.share * 100).toFixed(1)}% | ${kb(g.estRaw)} | ${kb(g.estGzip)} |`)
  }

  if (r.duplicates.length) {
    console.log(`\n## 重复模块（同一个模块出现在多个块里；共 ${r.duplicates.length} 个，列前 10）\n`)
    for (const d of r.duplicates.slice(0, 10)) console.log(`- ${d.id}（${kb(d.rendered)} KB 渲染体积）：${d.chunks.join('、')}`)
  } else {
    console.log('\n## 重复模块\n\n没有。')
  }
}

function printCompare(now, before) {
  const delta = (a, b) => {
    if (a == null || b == null) return '—'
    const d = a - b
    return `${d >= 0 ? '+' : ''}${kb(d)}`
  }
  console.log(`\n## 与基线对比（基线生成于 ${before.generatedAt ?? '未知'}）\n`)
  console.log('| 指标 | 基线 KB | 现在 KB | 变化 KB |')
  console.log('|---|---:|---:|---:|')
  const rows = [
    ['入口脚本 gzip', before.summary.entry.gzip, now.summary.entry.gzip],
    ['入口脚本 brotli', before.summary.entry.brotli, now.summary.entry.brotli],
    ['首屏同步合计 gzip', before.summary.initialAll.gzip, now.summary.initialAll.gzip],
    ['首屏同步合计 brotli', before.summary.initialAll.brotli, now.summary.initialAll.brotli],
    ['首屏同步脚本 gzip', before.summary.initialJs.gzip, now.summary.initialJs.gzip],
    ['全部产物 gzip', before.summary.totalGzip, now.summary.totalGzip],
  ]
  for (const [name, a, b] of rows) console.log(`| ${name} | ${kb(a)} | ${kb(b)} | ${delta(b, a)} |`)
  console.log(`\n块数：${before.summary.chunks} → ${now.summary.chunks}`)

  const beforeMap = new Map(before.chunks.map(c => [c.key, c]))
  const nowMap = new Map(now.chunks.map(c => [c.key, c]))
  const lines = []
  for (const [key, c] of nowMap) {
    const b = beforeMap.get(key)
    if (!b) lines.push({ key, tag: '新增', gzip: c.gzip, d: c.gzip })
    else if (Math.abs(c.gzip - b.gzip) >= 512) lines.push({ key, tag: '变化', gzip: c.gzip, d: c.gzip - b.gzip, from: b.gzip })
  }
  for (const [key, b] of beforeMap) if (!nowMap.has(key)) lines.push({ key, tag: '消失', gzip: 0, d: -b.gzip, from: b.gzip })
  lines.sort((a, b) => Math.abs(b.d) - Math.abs(a.d))
  console.log('\n逐块 gzip 变化（≥0.5 KB 才列）：\n')
  console.log('| 块 | 状态 | 基线 KB | 现在 KB | 变化 KB |')
  console.log('|---|---|---:|---:|---:|')
  for (const l of lines) console.log(`| ${l.key} | ${l.tag} | ${kb(l.from ?? null)} | ${kb(l.tag === '消失' ? null : l.gzip)} | ${l.d >= 0 ? '+' : ''}${kb(l.d)} |`)
}

async function main() {
  const opts = parseArgs(process.argv.slice(2))
  const configFile = path.join(opts.root, 'vite.config.ts')
  if (!fs.existsSync(configFile)) {
    console.error(`找不到 ${configFile}：--root 要指向 frontend 目录`)
    process.exit(2)
  }
  const outDir = fs.mkdtempSync(path.join(os.tmpdir(), 'rm-bundle-report-'))
  const items = []
  const t0 = Date.now()
  // 构建配置里的 Brotli 预压缩插件（build.precompress.ts）在这里没有用：本脚本自己算 brotli，产物也不落盘
  process.env.RM_SKIP_PRECOMPRESS = '1'
  try {
    await build({
      root: opts.root,
      configFile,
      logLevel: 'error',
      build: { outDir, emptyOutDir: true, reportCompressedSize: false, write: true },
      plugins: [collector(items, opts.brotli)],
    })
  } finally {
    fs.rmSync(outDir, { recursive: true, force: true })
  }
  const result = analyze(items, path.join(opts.root, 'src'))
  result.generatedAt = new Date().toISOString()
  result.root = opts.root
  console.log(`# 前端产物体积报告  root=${opts.root}  构建+压缩 ${((Date.now() - t0) / 1000).toFixed(0)} s\n`)
  printReport(result, opts.top)
  if (opts.json) {
    fs.writeFileSync(opts.json, JSON.stringify(result, null, 1))
    console.log(`\n结果已存：${opts.json}`)
  }
  if (opts.compare) printCompare(result, JSON.parse(fs.readFileSync(opts.compare, 'utf8')))
}

main().catch(err => {
  console.error(err)
  process.exit(1)
})
