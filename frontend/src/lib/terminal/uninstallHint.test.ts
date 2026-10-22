import { describe, it } from 'node:test'
import assert from 'node:assert/strict'
import { deleteAgentNote, isWindowsHost, manualUninstallCommand, uninstallOutcomeNotice } from './uninstallHint'

describe('uninstallHint', () => {
  it('按系统给出手动卸载命令：Windows 不带 sudo 并说明管理员 PowerShell', () => {
    assert.equal(isWindowsHost('windows'), true)
    assert.equal(isWindowsHost('Windows 11 Pro'), true)
    assert.equal(isWindowsHost('Ubuntu 24.04'), false)
    const win = manualUninstallCommand('windows')
    assert.equal(win.command, 'relaymesh-agent uninstall --purge')
    assert.ok(win.note.includes('管理员'))
    const linux = manualUninstallCommand('Ubuntu 24.04')
    assert.equal(linux.command, 'sudo relaymesh-agent uninstall --purge')
    assert.ok(linux.note.includes('--user'))
  })

  it('在线且声明了能力：可以勾选「同时卸载」，同时仍带手动命令作为兜底', () => {
    const n = deleteAgentNote({ online: true, os: 'Ubuntu', can_uninstall: true, temporary: false })
    assert.equal(n.canUninstall, true)
    assert.equal(n.reason, '')
    assert.ok(n.command.length > 0)
  })

  it('不能同时卸载时如实给出原因：离线、旧版本 / 非常驻服务、临时 Agent', () => {
    const offline = deleteAgentNote({ online: false, os: 'windows', can_uninstall: true, temporary: false })
    assert.equal(offline.canUninstall, false, '离线时即使上次声明过能力也不能提供')
    assert.ok(offline.reason.includes('离线'))
    assert.ok(offline.command.startsWith('relaymesh-agent'))

    const old = deleteAgentNote({ online: true, os: 'Ubuntu', temporary: false })
    assert.equal(old.canUninstall, false)
    assert.ok(old.reason.includes('不支持远程卸载'))

    const temp = deleteAgentNote({ online: true, os: 'windows', can_uninstall: true, temporary: true })
    assert.equal(temp.canUninstall, false, '临时 Agent 没有安装过任何东西')
    assert.equal(temp.command, '')
    assert.ok(temp.reason.includes('临时'))
  })

  it('卸载结果提示：只有真正下发了才是成功；其余都是警告并带上手动命令', () => {
    assert.equal(uninstallOutcomeNotice(undefined, 'Ubuntu'), null)

    const ok = uninstallOutcomeNotice('requested', 'Ubuntu')
    assert.equal(ok?.level, 'success')
    assert.ok(ok?.description.includes('保留'), '应说明录像与命令输出仍保留在设备上')

    for (const o of ['offline', 'unsupported', 'failed'] as const) {
      const n = uninstallOutcomeNotice(o, 'Ubuntu')
      assert.equal(n?.level, 'warning', o)
      assert.ok(n?.title.startsWith('已删除，但'), o)
      assert.ok(n?.description.includes('sudo relaymesh-agent uninstall --purge'), o)
    }
    const win = uninstallOutcomeNotice('offline', 'windows')
    assert.ok(win?.description.includes('relaymesh-agent uninstall --purge') && !win.description.includes('sudo'))
  })
})
