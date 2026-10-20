// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import { HOLDER_RESTART_TIP } from './holderRestart'
import { coverageText } from './recordLines'

describe('HOLDER_RESTART_TIP', () => {
  it('讲清照抄命令不一定能用的几种情形：找不到命令、系统级安装、Windows', () => {
    assert.match(HOLDER_RESTART_TIP, /完整路径/)
    assert.match(HOLDER_RESTART_TIP, /~\/\.local\/bin/)
    assert.match(HOLDER_RESTART_TIP, /sudo/)
    assert.match(HOLDER_RESTART_TIP, /管理员身份运行/)
    assert.match(HOLDER_RESTART_TIP, /& '<完整路径>' holder restart/)
  })

  it('出现 holder restart 的界面提示都带上它（记录限高的覆盖说明）', () => {
    const c = coverageText({ online: 3, unsupported: 2 })
    assert.ok(c?.text.includes(HOLDER_RESTART_TIP), '覆盖说明里应带使用要点')
  })
})
