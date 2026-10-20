#!/usr/bin/env node
// 前端纯逻辑单元测试：用 vite 自带的 esbuild 把 src/**/*.test.ts 打包到临时目录，再交给内置的 node:test 运行。
// 测试文件只允许依赖纯 TS 模块（不含 Vue 组件）；需要 DOM 的（如 Markdown 净化）用 jsdom 注入，jsdom 不参与打包；需要重新压缩依赖的构建回归测试直接调用 esbuild，esbuild 同样不参与打包。
import { spawnSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, readdirSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = resolve(fileURLToPath(new URL('..', import.meta.url)))

function collectTests(dir) {
  const found = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name)
    if (entry.isDirectory()) found.push(...collectTests(full))
    else if (entry.name.endsWith('.test.ts')) found.push(full)
  }
  return found
}

const entries = collectTests(join(root, 'src'))
if (entries.length === 0) {
  console.log('没有找到 *.test.ts，跳过')
  process.exit(0)
}

// 产物放在 node_modules/.cache 下，这样外部依赖（jsdom）能沿目录向上解析到 frontend/node_modules
const cacheBase = join(root, 'node_modules', '.cache')
mkdirSync(cacheBase, { recursive: true })
const outdir = mkdtempSync(join(cacheBase, 'relaymesh-unit-'))
try {
  const esbuildBin = join(root, 'node_modules', '.bin', process.platform === 'win32' ? 'esbuild.cmd' : 'esbuild')
  const build = spawnSync(
    esbuildBin,
    [...entries, '--bundle', '--platform=node', '--format=esm', '--external:jsdom', '--external:esbuild', `--outdir=${outdir}`, '--out-extension:.js=.mjs', '--log-level=warning'],
    { stdio: 'inherit' }
  )
  if (build.error) {
    console.error(`无法启动 esbuild（${esbuildBin}）：${build.error.message}。请先执行 pnpm --dir frontend install`)
    process.exit(1)
  }
  if (build.status !== 0) process.exit(build.status ?? 1)

  // 测试分散在多个目录时 esbuild 会保留相对目录结构，因此递归收集产物
  const collectBundles = dir =>
    readdirSync(dir, { withFileTypes: true }).flatMap(entry => {
      const full = join(dir, entry.name)
      if (entry.isDirectory()) return collectBundles(full)
      return entry.name.endsWith('.test.mjs') ? [full] : []
    })
  const bundles = collectBundles(outdir)
  if (bundles.length === 0) {
    console.error('esbuild 没有产出任何 *.test.mjs，请检查测试入口')
    process.exit(1)
  }
  const run = spawnSync(process.execPath, ['--test', ...bundles], { stdio: 'inherit' })
  process.exitCode = run.status ?? 1
} finally {
  rmSync(outdir, { recursive: true, force: true })
}
