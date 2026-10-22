import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { describe, it } from 'node:test'
import { classifyLauncherExec } from './launcherExec'

// 夹具放在 internal/store/testdata（与 Go 侧同一份）：公开库同步只带 cmd / internal / frontend / web / docs，根目录的 testdata 带不过去。
const casesPath = join(resolve(process.cwd(), '..'), 'internal/store/testdata/launcher_exec_cases.json')
const cases = JSON.parse(readFileSync(casesPath, 'utf8')) as { name: string; command: string; ok: boolean; exec?: string }[]

describe('classifyLauncherExec（与 internal/store/testdata/launcher_exec_cases.json 一致）', () => {
  for (const c of cases) {
    it(c.name, () => {
      const r = classifyLauncherExec(c.command)
      if (c.ok) {
        assert.equal(r.ok, true)
        if (c.exec !== undefined) assert.equal(r.ok && r.exec, c.exec)
      } else {
        assert.equal(r.ok, false)
      }
    })
  }
})
