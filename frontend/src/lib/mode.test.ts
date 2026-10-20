// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import {
  EXEC_HOSTS_PATH,
  PROJECTS,
  PROJECT_ORDER,
  badgeNumber,
  execSessionPath,
  execThreadPath,
  isRememberable,
  modeOfPath,
  otherProject,
  projectAlert,
  switcherLabel,
  type ProjectCounts,
} from './mode'

describe('工作模式与路径', () => {
  it('/exec 及其子路径属于 ExecMesh，其余都属于 RelayMesh', () => {
    for (const p of ['/exec', '/exec/', '/exec/t/abc', '/exec/t/abc?view=record', '/exec/hosts', '/exec/r/key?x=1', '/exec?foo=1', '/exec#top']) {
      assert.equal(modeOfPath(p), 'exec', p)
    }
    for (const p of ['/', '/settings', '/archive', '/terminal', '/execution', '/executor/x', '/exe', '/e/xec']) {
      assert.equal(modeOfPath(p), 'relay', p)
    }
  })

  it('RelayMesh 只记首页；ExecMesh 记任意位置', () => {
    assert.equal(isRememberable('/'), true)
    assert.equal(isRememberable('/settings'), false)
    assert.equal(isRememberable('/archive'), false)
    assert.equal(isRememberable('/exec'), true)
    assert.equal(isRememberable('/exec/t/abc'), true)
    assert.equal(isRememberable('/exec/hosts'), true)
  })

  it('会话与线程的路径会转义特殊字符', () => {
    assert.equal(execSessionPath('abc123'), '/exec/t/abc123')
    assert.equal(execSessionPath('a/b c'), '/exec/t/a%2Fb%20c')
    assert.equal(execSessionPath('abc123', true), '/exec/t/abc123?view=record')
    assert.equal(execThreadPath('thr-1'), '/exec/r/thr-1')
    assert.equal(EXEC_HOSTS_PATH, '/exec/hosts')
  })
})

describe('项目切换器', () => {
  const quiet: ProjectCounts = { relayWaiting: 0, execAttention: 0, modelRunning: 0 }

  it('两个项目各有名称与说明，顺序固定为 RelayMesh、ExecMesh', () => {
    assert.deepEqual(PROJECT_ORDER, ['relay', 'exec'])
    assert.equal(PROJECTS.relay.name, 'RelayMesh')
    assert.equal(PROJECTS.exec.name, 'ExecMesh')
    for (const id of PROJECT_ORDER) {
      assert.equal(PROJECTS[id].id, id)
      assert.ok(PROJECTS[id].subtitle.length > 0 && PROJECTS[id].tagline.length > 0)
    }
    assert.equal(otherProject('relay'), 'exec')
    assert.equal(otherProject('exec'), 'relay')
  })

  it('只提醒「另一个项目」：当前所在项目一律不提醒', () => {
    const busy: ProjectCounts = { relayWaiting: 3, execAttention: 2, modelRunning: 1 }
    assert.deepEqual(projectAlert('relay', 'relay', busy), { attention: 0, running: false, text: '' })
    assert.deepEqual(projectAlert('exec', 'exec', busy), { attention: 0, running: false, text: '' })
  })

  it('在 RelayMesh 里提醒 ExecMesh：待处理数与模型运行中', () => {
    const a = projectAlert('exec', 'relay', { relayWaiting: 5, execAttention: 2, modelRunning: 1 })
    assert.equal(a.attention, 2)
    assert.equal(a.running, true)
    assert.equal(a.text, '2 项待处理（待启用主机或待批准的命令），1 个模型正在运行')
    assert.deepEqual(projectAlert('exec', 'relay', { ...quiet, modelRunning: 2 }), { attention: 0, running: true, text: '2 个模型正在运行' })
    assert.deepEqual(projectAlert('exec', 'relay', quiet), { attention: 0, running: false, text: '' })
  })

  it('在 ExecMesh 里提醒 RelayMesh：等待反馈的会话数（运行中指示只属于 ExecMesh）', () => {
    assert.deepEqual(projectAlert('relay', 'exec', { relayWaiting: 4, execAttention: 9, modelRunning: 9 }), {
      attention: 4,
      running: false,
      text: '4 个会话等待反馈',
    })
    assert.deepEqual(projectAlert('relay', 'exec', quiet), { attention: 0, running: false, text: '' })
  })

  it('徽标数字封顶 99+', () => {
    assert.equal(badgeNumber(0), '0')
    assert.equal(badgeNumber(99), '99')
    assert.equal(badgeNumber(100), '99+')
  })

  it('按钮的无障碍名称带上当前项目；另一个项目有提醒时一并说明', () => {
    assert.equal(switcherLabel('relay', quiet), '切换项目，当前是 RelayMesh')
    assert.equal(switcherLabel('relay', { relayWaiting: 7, execAttention: 2, modelRunning: 0 }), '切换项目，当前是 RelayMesh（ExecMesh：2 项待处理（待启用主机或待批准的命令））')
    assert.equal(switcherLabel('exec', { relayWaiting: 3, execAttention: 0, modelRunning: 1 }), '切换项目，当前是 ExecMesh（RelayMesh：3 个会话等待反馈）')
  })
})
