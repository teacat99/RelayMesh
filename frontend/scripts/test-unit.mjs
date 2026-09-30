#!/usr/bin/env node
// 前端纯逻辑单元测试：用 vite 自带的 esbuild 把 src/**/*.test.ts 打包到临时目录，再交给内置的 node:test 运行。
// 不引入额外依赖；测试文件只允许依赖纯 TS 模块（不含 Vue / DOM）。
import { spawnSync } from 'node:child_process'
import { mkdtempSync, readdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
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

const outdir = mkdtempSync(join(tmpdir(), 'relaymesh-unit-'))
try {
  const esbuildBin = join(root, 'node_modules', '.bin', process.platform === 'win32' ? 'esbuild.cmd' : 'esbuild')
  const build = spawnSync(
    esbuildBin,
    [...entries, '--bundle', '--platform=node', '--format=esm', `--outdir=${outdir}`, '--out-extension:.js=.mjs', '--log-level=warning'],
    { stdio: 'inherit' }
  )
  if (build.status !== 0) process.exit(build.status ?? 1)

  const bundles = readdirSync(outdir)
    .filter(name => name.endsWith('.test.mjs'))
    .map(name => join(outdir, name))
  const run = spawnSync(process.execPath, ['--test', ...bundles], { stdio: 'inherit' })
  process.exitCode = run.status ?? 1
} finally {
  rmSync(outdir, { recursive: true, force: true })
}
