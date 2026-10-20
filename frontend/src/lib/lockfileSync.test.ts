// 运行：pnpm --dir frontend run test:unit
// package.json 与 package-lock.json 一致性的防线（#164 rc.1 热更时踩到）。
//   本机与公开库 CI 用 pnpm（pnpm-lock.yaml，`install --frozen-lockfile` 自己会校验），而 Docker 镜像（Dockerfile.multistage，
//   公开库的 docker-publish.yml 同款）用 `npm ci`（package-lock.json）。只升级了 package.json 与 pnpm-lock.yaml、漏掉
//   package-lock.json 时，单测、类型检查与本机构建全是绿的，却会在镜像构建的第一步以
//   「lock file's axios@1.19.0 does not satisfy axios@1.20.0」失败——热更与正式发布都会被挡住。
//   这里守住最容易漏的两处：
//   1. package.json 里每个依赖的版本范围，必须与 package-lock.json 根节点记录的范围逐项相同（npm 重新生成锁文件时会同步这一段）；
//   2. 每个依赖在锁里都有已安装条目，且已安装版本不低于范围的下限（`^1.20.0` 的锁里装着 1.19.0 就是漏了）。
// 修复办法：在 node:20-alpine（与镜像里同一个 npm）里重新生成：
//   docker run --rm -v "$PWD/frontend:/w" -w /w node:20-alpine npm install --package-lock-only --ignore-scripts --registry https://registry.npmmirror.com/
// （registry 与现有锁里 resolved 的主机保持一致）；之后用 scripts 里的 Docker 构建或 deploy-direct.sh 的第 2 步确认 `npm ci` 通过。
import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, it } from 'node:test'

function frontendDir(): string {
  for (const base of [process.cwd(), join(process.cwd(), 'frontend')]) {
    if (existsSync(join(base, 'package.json')) && existsSync(join(base, 'package-lock.json'))) return base
  }
  throw new Error('找不到 frontend 目录，请在仓库根目录或 frontend 目录下运行测试')
}

type Deps = Record<string, string>
interface PackageJson {
  dependencies?: Deps
  devDependencies?: Deps
}
interface LockEntry extends PackageJson {
  version?: string
}
interface PackageLock {
  lockfileVersion?: number
  packages?: Record<string, LockEntry>
}

const readJson = <T>(name: string): T => JSON.parse(readFileSync(join(frontendDir(), name), 'utf8')) as T

/** 把 `1.20.0` / `1.20.0-rc.1` 取出前三段数字；取不到返回 null。 */
function parseVersion(text: string): [number, number, number] | null {
  const m = /(\d+)\.(\d+)\.(\d+)/.exec(text)
  return m ? [Number(m[1]), Number(m[2]), Number(m[3])] : null
}

/** a 是否不低于 b（逐段比较）。 */
function atLeast(a: [number, number, number], b: [number, number, number]): boolean {
  for (let i = 0; i < 3; i++) {
    if (a[i] !== b[i]) return a[i] > b[i]
  }
  return true
}

describe('package.json 与 package-lock.json 一致（Docker 构建用 npm ci）', () => {
  const pkg = readJson<PackageJson>('package.json')
  const lock = readJson<PackageLock>('package-lock.json')
  const root = lock.packages?.['']

  it('锁文件有根节点（lockfileVersion 2 以上）', () => {
    assert.ok(root, 'package-lock.json 里没有根节点 packages[""]')
    assert.ok((lock.lockfileVersion ?? 0) >= 2, `lockfileVersion=${lock.lockfileVersion}`)
  })

  for (const field of ['dependencies', 'devDependencies'] as const) {
    it(`${field}：版本范围与锁文件根节点逐项相同`, () => {
      assert.deepEqual(
        root?.[field] ?? {},
        pkg[field] ?? {},
        `package.json 的 ${field} 与 package-lock.json 根节点不一致：只改了 package.json / pnpm-lock.yaml、没有重新生成 package-lock.json？（修复办法见本文件头部注释）`
      )
    })
  }

  it('每个依赖在锁里都有已安装条目，且已安装版本不低于范围的下限', () => {
    const wanted: Deps = { ...pkg.dependencies, ...pkg.devDependencies }
    assert.ok(Object.keys(wanted).length > 0, 'package.json 里没有依赖，测试没有意义')
    for (const [name, range] of Object.entries(wanted)) {
      const entry = lock.packages?.[`node_modules/${name}`]
      assert.ok(entry?.version, `${name} 在 package-lock.json 里没有已安装条目`)
      const floor = parseVersion(range)
      const have = parseVersion(entry.version ?? '')
      if (!floor || !have) continue // 范围写成标签 / 链接等形式：不在这里判断
      assert.ok(atLeast(have, floor), `${name}：package.json 要求 ${range}，package-lock.json 里装的是 ${entry.version}`)
    }
  })

  it('比较函数本身：逐段比较、patch 位也算', () => {
    assert.equal(atLeast([1, 20, 0], [1, 20, 0]), true)
    assert.equal(atLeast([1, 19, 9], [1, 20, 0]), false)
    assert.equal(atLeast([3, 5, 44], [3, 5, 41]), true)
    assert.equal(atLeast([3, 5, 41], [3, 5, 44]), false)
    assert.deepEqual(parseVersion('^1.20.0'), [1, 20, 0])
    assert.equal(parseVersion('latest'), null)
  })
})
