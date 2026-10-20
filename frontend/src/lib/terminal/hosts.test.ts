// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import type { HostView, SshView } from '../../api/terminal'
import {
  canHumanTerminal,
  canOpenSshTerminal,
  gatewayHosts,
  hostKeyFile,
  hostSummary,
  isGatewayHost,
  isSshHost,
  sameFingerprint,
  shortFingerprint,
  sshBadge,
  sshEndpoint,
  sshKindBadge,
  summaryText,
  supportsModel,
  sshTerminalRecorded,
  validateSshForm,
} from './hosts'

const view = (over: Partial<SshView> = {}): SshView => ({
  address: '10.0.0.5:22',
  user: 'ops',
  auth: 'password',
  key_type: 'ssh-ed25519',
  fingerprint: 'SHA256:abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG',
  has_secret: true,
  backend_enabled: true,
  ...over,
})

describe('主机类型', () => {
  it('缺失 kind 按 Agent 处理，只有 ssh 才是 SSH 主机', () => {
    assert.equal(isSshHost({ kind: 'ssh' }), true)
    assert.equal(isSshHost({ kind: 'agent' }), false)
    assert.equal(isSshHost({}), false)
    assert.equal(isSshHost(undefined), false)
    assert.equal(isSshHost(null), false)
  })

  it('本机 shell 的终端（以及记录、文件传输）只有装了 Agent 的主机才有；SSH 主机的终端见 canOpenSshTerminal', () => {
    assert.equal(canHumanTerminal({ kind: 'ssh' }), false)
    assert.equal(canHumanTerminal({ kind: 'agent' }), true)
    assert.equal(canHumanTerminal({}), true)
    assert.equal(canHumanTerminal(undefined), true)
  })
})

describe('SSH 展示文案', () => {
  it('连接摘要', () => {
    assert.equal(sshEndpoint({ user: 'ops', address: '10.0.0.5:22' }), 'ops@10.0.0.5:22')
    assert.equal(sshEndpoint({ address: 'h:2222' }), 'h:2222')
    assert.equal(sshEndpoint({ user: 'ops' }), '')
    assert.equal(sshEndpoint(null), '')
  })

  it('指纹缩写：只缩过长的 SHA256 指纹，其余原样', () => {
    const fp = 'SHA256:abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG'
    assert.equal(shortFingerprint(fp), 'SHA256:abcdefghij…BCDEFG')
    assert.equal(shortFingerprint('SHA256:short'), 'SHA256:short')
    assert.equal(shortFingerprint(''), '')
    assert.equal(shortFingerprint(undefined), '')
    assert.equal(shortFingerprint('x'.repeat(30)), `${'x'.repeat(12)}…${'x'.repeat(6)}`)
  })

  it('核对指纹用的主机公钥文件按密钥类型给出，未知类型不猜', () => {
    assert.equal(hostKeyFile('ssh-ed25519'), '/etc/ssh/ssh_host_ed25519_key.pub')
    assert.equal(hostKeyFile('ssh-rsa'), '/etc/ssh/ssh_host_rsa_key.pub')
    assert.equal(hostKeyFile('ecdsa-sha2-nistp256'), '/etc/ssh/ssh_host_ecdsa_key.pub')
    assert.equal(hostKeyFile('ssh-dss'), '')
    assert.equal(hostKeyFile(undefined), '')
  })

  it('指纹比对忽略首尾空白，区分大小写，空值永不相等', () => {
    assert.equal(sameFingerprint(' SHA256:Ab\n', 'SHA256:Ab'), true)
    assert.equal(sameFingerprint('SHA256:Ab', 'SHA256:ab'), false)
    assert.equal(sameFingerprint('', ''), false)
    assert.equal(sameFingerprint(undefined, undefined), false)
  })
})

describe('SSH 状态徽标', () => {
  it('后端未启用优先于一切', () => {
    const b = sshBadge({ online: true, ssh: view({ backend_enabled: false, key_state: 'mismatch' }) })
    assert.equal(b.tone, 'bad')
    assert.equal(b.text, '后端未启用')
  })

  it('密钥不一致是红色警告，并说明不会发送凭据', () => {
    const b = sshBadge({ online: false, ssh: view({ key_state: 'mismatch' }) })
    assert.equal(b.tone, 'bad')
    assert.match(b.title, /不会向它发送任何凭据/)
  })

  it('在线是绿色；离线带错误是黄色并保留原因', () => {
    assert.equal(sshBadge({ online: true, ssh: view() }).tone, 'ok')
    const w = sshBadge({ online: false, ssh: view({ error: '连接被拒绝' }) })
    assert.deepEqual([w.tone, w.text, w.title], ['warn', '连接失败', '连接被拒绝'])
    assert.equal(sshBadge({ online: false, ssh: view() }).text, '待检测')
  })

  it('没有 ssh 字段时退化为中性徽标', () => {
    assert.equal(sshBadge({ online: true }).tone, 'muted')
  })
})

describe('SSH 网关接入', () => {
  const GW = 'RM-GATE-0000-0001-AAAA'
  const viaGw = (over: Partial<SshView> = {}): SshView => view({ gateway: GW, gateway_ready: true, ...over })
  const sshHost = (ssh: SshView, over: Partial<HostView> = {}) => ({ kind: 'ssh', status: 'enabled', online: true, ssh, ...over })

  it('只有带 gateway 的 SSH 主机才是网关主机；直连的 SSH 主机与 Agent 主机都不是', () => {
    assert.equal(isGatewayHost(sshHost(viaGw())), true)
    assert.equal(isGatewayHost(sshHost(view())), false)
    assert.equal(isGatewayHost({ kind: 'agent', ssh: { gateway: GW } }), false)
    assert.equal(isGatewayHost({ kind: 'ssh', ssh: { gateway: '' } }), false)
    assert.equal(isGatewayHost(undefined), false)
  })

  it('打开人工终端要求：已启用、凭据还在网关上、密钥没不一致、网关就绪，缺一不可', () => {
    assert.equal(canOpenSshTerminal(sshHost(viaGw())), true)
    assert.equal(canOpenSshTerminal(sshHost(viaGw(), { status: 'disabled' })), false)
    assert.equal(canOpenSshTerminal(sshHost(viaGw({ cred_missing: true }))), false)
    // 凭据在网关上：Hub 没有副本（has_secret=false），也不需要 SSH 主密钥（backend_enabled=false），都不影响经网关打开终端
    assert.equal(canOpenSshTerminal(sshHost(viaGw({ has_secret: false, backend_enabled: false, cred_on_gateway: true }))), true)
    assert.equal(canOpenSshTerminal(sshHost(viaGw({ key_state: 'mismatch' }))), false)
    assert.equal(canOpenSshTerminal(sshHost(viaGw({ gateway_ready: false }))), false)
    assert.equal(canOpenSshTerminal(sshHost(viaGw({ gateway_ready: undefined }))), false)
    assert.equal(canOpenSshTerminal({ kind: 'agent', status: 'enabled' }), false)
    assert.equal(canOpenSshTerminal(null), false)
  })

  it('Hub 直连的 SSH 主机同样有人工终端：已启用、有 SSH 主密钥、保存着凭据、密钥没不一致，缺一不可', () => {
    assert.equal(canOpenSshTerminal(sshHost(view())), true)
    assert.equal(canOpenSshTerminal(sshHost(view(), { status: 'disabled' })), false)
    assert.equal(canOpenSshTerminal(sshHost(view({ backend_enabled: false }))), false, '服务端没有 SSH 主密钥，Hub 无法解开凭据')
    assert.equal(canOpenSshTerminal(sshHost(view({ has_secret: false }))), false, 'Hub 上没有保存凭据')
    assert.equal(canOpenSshTerminal(sshHost(view({ key_state: 'mismatch' }))), false)
    // 网关才有的字段对直连没有意义：不能因为 gateway_ready 缺失就当作不可用
    assert.equal(canOpenSshTerminal(sshHost(view({ gateway_ready: undefined }))), true)
    assert.equal(canOpenSshTerminal(sshHost(view({ cred_missing: true }))), true, 'cred_missing 只描述网关上的凭据')
  })

  it('终端会话是否被记录：只有经网关的会话有记录（才谈得上无痕），Hub 直连的不记录', () => {
    assert.equal(sshTerminalRecorded(sshHost(viaGw())), true)
    assert.equal(sshTerminalRecorded(sshHost(view())), false)
    assert.equal(sshTerminalRecorded({ kind: 'agent' }), false)
    assert.equal(sshTerminalRecorded(null), false)
  })

  it('网关候选只含网关能力在线的 Agent（Agent 默认开启；--no-ssh-gateway 关闭的、旧版本的、离线 / 停用 / 临时的都不算）', () => {
    const agent = (over: Partial<HostView> = {}) =>
      ({ machine_code: 'A', kind: 'agent', status: 'enabled', online: true, temporary: false, holder: { state: 'ready', persistent: true, caps: ['exec', 'ssh'] }, ...over }) as HostView
    const plain = agent({ machine_code: 'P', holder: { state: 'ready', persistent: true, caps: ['exec'] } })
    const old = agent({ machine_code: 'O', holder: { state: 'ready', persistent: true } })
    const offline = agent({ machine_code: 'F', online: false })
    const disabled = agent({ machine_code: 'D', status: 'disabled' })
    const temp = agent({ machine_code: 'T', temporary: true })
    const down = agent({ machine_code: 'U', holder: { state: 'unavailable', persistent: true, caps: ['ssh'] } })
    const ssh = { ...agent({ machine_code: 'S' }), kind: 'ssh' } as HostView
    const good = agent({ machine_code: 'G' })
    assert.deepEqual(gatewayHosts([plain, old, offline, disabled, temp, down, ssh]).map(h => h.machine_code), [])
    assert.deepEqual(gatewayHosts([plain, good, old]).map(h => h.machine_code), ['G'])
  })

  it('类型徽标：两条路径能力相同，只在说明里区分谁去连、会话与凭据放在哪', () => {
    assert.equal(sshKindBadge(sshHost(view())).text, 'SSH')
    assert.match(sshKindBadge(sshHost(view())).title, /Hub 直连/)
    assert.match(sshKindBadge(sshHost(view())).title, /有人工终端，模型获得授权后也可以执行命令/)
    assert.match(sshKindBadge(sshHost(view())).title, /不做记录，Hub 重启时会结束/)
    assert.doesNotMatch(sshKindBadge(sshHost(view())).title, /只供模型|没有人工终端/)
    assert.equal(sshKindBadge(sshHost(viaGw())).text, 'SSH · 网关')
    assert.match(sshKindBadge(sshHost(viaGw())).title, /有人工终端，模型获得授权后也可以执行命令/)
    assert.match(sshKindBadge(sshHost(viaGw())).title, /凭据只保存在网关上/)
    // 探测不出 POSIX 环境的网关主机只能当终端用，徽标说清楚
    assert.match(sshKindBadge(sshHost(viaGw({ term_only: true }))).title, /只能当人工终端用/)
  })

  it('状态徽标：网关不可用时不把责任算到目标主机头上；网关就绪后才看目标', () => {
    const down = sshBadge({ online: false, ssh: viaGw({ gateway_ready: false, error: '连接被拒绝' }) })
    assert.deepEqual([down.tone, down.text], ['warn', '网关不可用'])
    assert.match(down.title, /网关/)
    assert.equal(sshBadge({ online: true, ssh: viaGw() }).text, '可连接')
    assert.equal(sshBadge({ online: false, ssh: viaGw({ error: '连接被拒绝' }) }).text, '连接失败')
    assert.equal(sshBadge({ online: false, ssh: viaGw() }).text, '待检测')
    // 更严重的问题优先
    assert.equal(sshBadge({ online: false, ssh: viaGw({ gateway_ready: false, key_state: 'mismatch' }) }).text, '主机密钥不一致')
    // Hub 直连不受影响
    assert.equal(sshBadge({ online: true, ssh: view() }).text, '可连接')
  })

  it('经网关的主机不依赖 Hub 的 SSH 主密钥：后端未启用只对 Hub 直连生效', () => {
    // 凭据在网关上：Hub 没有主密钥（backend_enabled=false）时它照常可用
    const ok = sshBadge({ online: true, ssh: viaGw({ backend_enabled: false, has_secret: false, cred_on_gateway: true }) })
    assert.deepEqual([ok.tone, ok.text], ['ok', '可连接'])
    // Hub 直连没有主密钥才是「后端未启用」
    assert.equal(sshBadge({ online: true, ssh: view({ backend_enabled: false }) }).text, '后端未启用')
  })

  it('网关上没有凭据时给出「凭据缺失」徽标，排在网关不可用之后、可连接之前', () => {
    const b = sshBadge({ online: false, ssh: viaGw({ cred_missing: true, cred_on_gateway: true, has_secret: false }) })
    assert.deepEqual([b.tone, b.text], ['warn', '凭据缺失'])
    assert.match(b.title, /重新填写/)
    // 网关本身不可用时先说网关的问题（凭据是否还在无从判断）
    assert.equal(sshBadge({ online: false, ssh: viaGw({ cred_missing: true, gateway_ready: false }) }).text, '网关不可用')
    // 密钥不一致更严重
    assert.equal(sshBadge({ online: false, ssh: viaGw({ cred_missing: true, key_state: 'mismatch' }) }).text, '主机密钥不一致')
  })

  it('摘要：经网关的主机说清走哪台网关，并且像 Hub 直连一样显示模型片段', () => {
    const h = { ...sshHost(viaGw()), allow_model: true, exec_mode: 'auto' }
    const text = summaryText(hostSummary(h, { modelEnabled: true, gatewayTitle: c => (c === GW ? '家里网关' : c) }))
    assert.match(text, /经网关 家里网关/)
    assert.match(text, /模型可用/)
    // 没有名字映射时退化为缩写的机器码
    assert.match(summaryText(hostSummary(h, { modelEnabled: false })), /经网关 RM-GATE-…-AAAA/)
    assert.doesNotMatch(summaryText(hostSummary(h, { modelEnabled: false })), /模型/)
    // Hub 直连的主机照旧显示模型片段，且没有「经网关」
    const direct = summaryText(hostSummary({ ...sshHost(view()), allow_model: true, exec_mode: 'auto' }, { modelEnabled: true }))
    assert.match(direct, /模型可用/)
    assert.doesNotMatch(direct, /经网关/)
  })

  it('摘要：凭据的状态按它所在的位置判断（Hub 直连看 has_secret，经网关看 cred_missing）', () => {
    const off = { modelEnabled: false }
    // 经网关：Hub 没有副本（has_secret=false）是正常的，不能报「凭据不可用」
    const fine = summaryText(hostSummary(sshHost(viaGw({ has_secret: false, cred_on_gateway: true })), off))
    assert.doesNotMatch(fine, /凭据/)
    const missing = summaryText(hostSummary(sshHost(viaGw({ has_secret: false, cred_on_gateway: true, cred_missing: true })), off))
    assert.match(missing, /凭据缺失，需重新填写/)
    // Hub 直连：没有保存凭据才报
    assert.match(summaryText(hostSummary(sshHost(view({ has_secret: false })), off)), /凭据不可用，需重新填写/)
    assert.doesNotMatch(summaryText(hostSummary(sshHost(view()), off)), /凭据/)
  })

  it('摘要：Hub 直连的主机标明「Hub 直连」，会话由 Hub 持有所以直接列出运行中的会话；经网关的会话属于网关主机，不在这里计数', () => {
    const off = { modelEnabled: false }
    const sessions = [{ sid: 'a', state: 'running' }, { sid: 'b', state: 'exited' }, { sid: 'c', state: 'running' }] as never
    const direct = summaryText(hostSummary({ ...sshHost(view()), sessions }, off))
    assert.match(direct, /Hub 直连/)
    assert.match(direct, /2 个会话运行中/)
    const viaGateway = summaryText(hostSummary({ ...sshHost(viaGw()), sessions }, off))
    assert.doesNotMatch(viaGateway, /Hub 直连|会话运行中/)
  })

  it('摘要：网关本机一票否决与「仅人工终端」如实说明', () => {
    const on = { modelEnabled: true }
    const vetoed = hostSummary({ ...sshHost(viaGw({ gateway_denied: true })), allow_model: true }, on)
    const v = vetoed.find(p => p.text === '网关已一票否决模型')
    assert.equal(v?.tone, 'warn')
    assert.match(v?.title ?? '', /--deny-model-exec/)
    assert.doesNotMatch(summaryText(vetoed), /模型可用/)

    const termOnly = summaryText(hostSummary({ ...sshHost(viaGw({ term_only: true })), allow_model: true }, on))
    assert.match(termOnly, /仅人工终端/)
    assert.doesNotMatch(termOnly, /模型可用|不允许模型/)
  })

  it('supportsModel：只有探测不出 POSIX 环境的网关主机不能给模型用', () => {
    assert.equal(supportsModel(sshHost(viaGw())), true)
    assert.equal(supportsModel(sshHost(view())), true)
    assert.equal(supportsModel({ kind: 'agent' }), true)
    assert.equal(supportsModel(sshHost(viaGw({ term_only: true }))), false)
    // term_only 只对网关主机有意义
    assert.equal(supportsModel(sshHost(view({ term_only: true }))), true)
    assert.equal(supportsModel(undefined), true)
  })
})

describe('SSH 表单校验', () => {
  const base = { address: '10.0.0.5', user: 'ops', auth: 'password' as const, password: 'x', privateKey: '' }

  it('通过的输入返回空串', () => {
    assert.equal(validateSshForm(base), '')
    assert.equal(validateSshForm({ ...base, address: 'host.example.com:2222' }), '')
    assert.equal(validateSshForm({ ...base, address: '[::1]:22' }), '')
    assert.equal(validateSshForm({ ...base, auth: 'key', password: '', privateKey: '-----BEGIN OPENSSH PRIVATE KEY-----' }), '')
  })

  it('地址：必填、不能夹带用户名 / 路径 / 空白', () => {
    assert.match(validateSshForm({ ...base, address: '  ' }), /主机地址/)
    for (const bad of ['ops@host', 'host/path', 'a b', 'host?x', 'host#x', 'ho%st', 'a\\b']) {
      assert.match(validateSshForm({ ...base, address: bad }), /主机地址只能是/, bad)
    }
  })

  it('用户名与服务端规则一致', () => {
    for (const bad of ['', 'a b', 'a/b', 'a;b', 'x'.repeat(65), '用户']) {
      assert.match(validateSshForm({ ...base, user: bad }), /用户名无效/, bad)
    }
    for (const ok of ['root', 'svc-1', 'a.b_c', 'DOMAIN$']) assert.equal(validateSshForm({ ...base, user: ok }), '', ok)
  })

  it('凭据：口令 / 私钥按认证方式必填，私钥有大小上限；编辑时凭据可以留空', () => {
    assert.match(validateSshForm({ ...base, password: '' }), /口令/)
    assert.match(validateSshForm({ ...base, auth: 'key', password: '', privateKey: ' ' }), /私钥/)
    assert.match(validateSshForm({ ...base, auth: 'key', privateKey: 'k'.repeat((16 << 10) + 1) }), /过大/)
    assert.equal(validateSshForm({ ...base, password: '' }, { secretRequired: false }), '')
    assert.equal(validateSshForm({ ...base, auth: 'key', password: '', privateKey: '' }, { secretRequired: false }), '')
  })
})

describe('主机卡片的收起摘要', () => {
  const NOW = Date.parse('2026-10-02T12:00:00Z')
  const on = { modelEnabled: true, nowMs: NOW }
  const off = { modelEnabled: false, nowMs: NOW }
  const agent = {
    kind: 'agent',
    machine_code: 'RM-9OJH-AAAA-BBBB-1589',
    online: true,
    os: 'windows',
    arch: 'amd64',
    privilege: 'admin',
    sessions: [],
  }

  it('在线的 Agent 主机：平台、权限、默认生命周期、记录开关，一目了然', () => {
    assert.equal(summaryText(hostSummary(agent, off)), 'windows · amd64 · 管理员（可提权） · 空闲不回收 · 终端记录开启')
  })

  it('有会话在运行时放在最前面，只数 running 的', () => {
    const sessions = [
      { sid: 'a', rows: 24, cols: 80, base: 0, end: 0, attached: 0, state: 'running' },
      { sid: 'b', rows: 24, cols: 80, base: 0, end: 0, attached: 0, state: 'exited' },
      { sid: 'c', rows: 24, cols: 80, base: 0, end: 0, attached: 1, state: 'running' },
    ]
    const parts = hostSummary({ ...agent, sessions }, off)
    assert.equal(parts[0].text, '2 个会话运行中')
    assert.equal(parts[0].tone, 'ok')
  })

  it('离线：说明最后在线时间，不再数会话', () => {
    const lastSeen = new Date(NOW - 3 * 3600 * 1000).toISOString()
    const parts = hostSummary({ ...agent, online: false, last_seen_at: lastSeen }, off)
    assert.equal(parts[0].text, '离线，最后在线 3 小时前')
    assert.equal(hostSummary({ ...agent, online: false }, off)[0].text, '离线')
    assert.ok(!summaryText(parts).includes('运行中'))
  })

  it('默认生命周期与终端记录如实反映（服务端关闭优先于单机关闭）', () => {
    assert.match(summaryText(hostSummary({ ...agent, idle_sec: 7 * 86400 }, off)), /空闲 7 天后回收/)
    assert.match(summaryText(hostSummary({ ...agent, rec_off: true }, off)), /终端记录已关闭/)
    assert.match(summaryText(hostSummary({ ...agent, rec_off: true, rec_server_off: true }, off)), /终端记录被服务端关闭/)
  })

  it('同名主机在最前面带上缩写机器码，收起后也分得清', () => {
    const [first] = hostSummary({ ...agent, duplicate_name: true }, off)
    assert.equal(first.text, 'RM-9OJH-…-1589')
    assert.equal(first.mono, true)
    assert.equal(first.tone, 'warn')
    assert.ok(!summaryText(hostSummary(agent, off)).includes('RM-'))
  })

  it('有会话已不存在时给出醒目的告警片段', () => {
    const parts = hostSummary({ ...agent, holder: { state: 'ready', persistent: true, lost: [{ sid: 'x', reason: 'killed' }, { sid: 'y', reason: 'killed' }] as never } }, off)
    const lost = parts.find(p => p.text.includes('已不存在'))
    assert.equal(lost?.text, '2 个会话已不存在')
    assert.equal(lost?.tone, 'warn')
  })

  it('服务端没启用模型执行时，摘要里不出现模型相关内容', () => {
    const h = { ...agent, allow_model: true, model_running: 2 }
    assert.ok(!summaryText(hostSummary(h, off)).includes('模型'))
  })

  it('模型访问：不允许 / 可用（带执行模式）/ 暂停 / 进行中 / 本机否决 / Agent 不支持', () => {
    assert.match(summaryText(hostSummary(agent, on)), /不允许模型使用$/)
    assert.match(summaryText(hostSummary({ ...agent, allow_model: true }, on)), /模型可用（自动）$/)
    assert.match(summaryText(hostSummary({ ...agent, allow_model: true, exec_mode: 'approve' }, on)), /模型可用（需批准）$/)
    assert.match(summaryText(hostSummary({ ...agent, allow_model: true, exec_mode: 'readonly' }, on)), /模型可用（只读）$/)
    assert.match(summaryText(hostSummary({ ...agent, allow_model: true, exec_mode: 'auto' }, on)), /模型可用（自动）$/)

    const paused = hostSummary({ ...agent, allow_model: true, model_paused: true }, on)
    assert.equal(paused[paused.length - 1].text, '模型已暂停')
    assert.equal(paused[paused.length - 1].tone, 'warn')

    const running = hostSummary({ ...agent, allow_model: true, model_running: 3 }, on)
    assert.equal(running[running.length - 1].text, '3 条模型命令进行中')

    const veto = hostSummary({ ...agent, allow_model: true, holder: { state: 'ready', persistent: true, deny_exec: true } }, on)
    assert.equal(veto[veto.length - 1].text, '本机已一票否决模型')
    assert.ok(!summaryText(veto).includes('模型可用'))

    const old = hostSummary({ ...agent, allow_model: true, holder: { state: 'ready', persistent: true } }, on)
    assert.equal(old[old.length - 1].text, 'Agent 不支持模型执行')
    assert.equal(old[old.length - 1].tone, 'warn')

    const supported = hostSummary({ ...agent, allow_model: true, holder: { state: 'ready', persistent: true, exec: {} } }, on)
    assert.ok(!summaryText(supported).includes('不支持'))
  })

  it('临时启用的主机不支持模型访问，摘要里不提', () => {
    assert.ok(!summaryText(hostSummary({ ...agent, temporary: true, allow_model: true }, on)).includes('模型'))
  })

  it('SSH 主机：连接信息、认证方式，凭据缺失时告警；没有生命周期与记录', () => {
    const ssh = { kind: 'ssh', online: true, os: 'linux', arch: 'amd64', ssh: view({ user: 'ops', address: '10.0.0.5:22', auth: 'key' }) }
    assert.equal(summaryText(hostSummary(ssh, off)), 'ops@10.0.0.5:22 · 私钥认证 · linux · amd64 · Hub 直连')
    const noSecret = hostSummary({ ...ssh, ssh: view({ has_secret: false }) }, off)
    assert.equal(noSecret[noSecret.length - 1].text, '凭据不可用，需重新填写')
    assert.equal(noSecret[noSecret.length - 1].tone, 'warn')
    const text = summaryText(hostSummary(ssh, on))
    assert.ok(!text.includes('终端记录') && !text.includes('空闲'))
    assert.match(text, /不允许模型使用$/)
    assert.ok(!summaryText(hostSummary({ ...ssh, allow_model: true, holder: { state: 'ready', persistent: true } }, on)).includes('不支持'), 'SSH 主机不存在 Agent 版本问题')
  })
})
