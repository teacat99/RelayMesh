import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import type { MCPCredential, MCPPermissions } from '../api/client'
import {
  applyCapability,
  applyMode,
  CAPABILITY_DEFAULT_DAYS,
  capabilityUrl,
  defaultCapabilityExpiry,
  defaultPolicyForm,
  execMeshPreset,
  fileScopeSummary,
  formFromCredential,
  GOVERNANCE_OPTIONS,
  grantsWholeFs,
  hasWholeFs,
  isoToLocalInput,
  LAUNCHER_OPTIONS,
  localInputToIso,
  modeCapLabel,
  modeOf,
  MAX_ALLOWED_PATHS,
  pathIssues,
  pathIssueText,
  payloadFromForm,
  relayMeshPreset,
  ROOT_FIX_HINT,
  rootProblem,
  splitLines,
  splitList,
  TERMINAL_COMPAT_TOOLS,
  TERMINAL_NATIVE_TOOLS,
  TERMINAL_OPTIONS,
  terminalToolNames,
  tierLabel,
  tierOf,
  validatePolicyForm
} from './credentialPolicy'

function credential(overrides: Partial<MCPCredential> = {}): MCPCredential {
  return {
    id: 1,
    name: 'agent',
    token: 'rm-ab****cdef',
    host_name: '',
    is_active: true,
    permissions: relayMeshPreset(),
    note: '',
    created_at: '2026-10-01T00:00:00Z',
    updated_at: '2026-10-01T00:00:00Z',
    ...overrides
  }
}

describe('档位与模式', () => {
  it('新令牌默认预设是 RelayMesh：全部治理工具、不含终端', () => {
    const p = defaultPolicyForm().permissions
    assert.equal(p.terminal, false)
    assert.equal(p.terminal_files, false)
    assert.equal(modeOf(p), 'relaymesh')
    assert.equal(tierOf(p), 'relaymesh')
  })

  it('ExecMesh 预设只有终端、没有任何治理工具', () => {
    const p = execMeshPreset()
    assert.equal(modeOf(p), 'execmesh')
    assert.equal(tierOf(p), 'execmesh')
    assert.equal(p.feedback || p.sessions || p.system_info || p.skills || p.configure || p.execute, false)
  })

  it('治理子集或混合都是自定义模式；档位按权限推导', () => {
    assert.equal(modeOf({ ...relayMeshPreset(), execute: false }), 'custom')
    assert.equal(tierOf({ ...relayMeshPreset(), execute: false }), 'relaymesh')
    assert.equal(modeOf({ ...relayMeshPreset(), terminal: true }), 'custom')
    assert.equal(tierOf({ ...relayMeshPreset(), terminal: true }), 'hybrid')
    assert.equal(tierOf({ ...execMeshPreset(), terminal_files: true }), 'execmesh')
    assert.equal(modeOf({ ...execMeshPreset(), terminal_files: true }), 'custom')
    assert.equal(tierOf({ ...execMeshPreset(), terminal: false }), 'none')
  })

  it('档位文案', () => {
    assert.equal(tierLabel('relaymesh'), 'RelayMesh')
    assert.equal(tierLabel('execmesh'), 'ExecMesh')
    assert.equal(tierLabel('hybrid'), '混合')
    assert.equal(tierLabel(undefined), '')
  })

  it('切换预设只改权限位，保留访问控制配置', () => {
    const f = { ...defaultPolicyForm(), allowedCidrsText: '10.0.0.0/8', expiresAtLocal: '2030-01-01T00:00' }
    const exec = applyMode(f, 'execmesh')
    assert.equal(modeOf(exec.permissions), 'execmesh')
    assert.equal(exec.allowedCidrsText, '10.0.0.0/8')
    assert.equal(exec.expiresAtLocal, '2030-01-01T00:00')
    assert.equal(modeOf(applyMode(exec, 'relaymesh').permissions), 'relaymesh')
    assert.deepEqual(applyMode(exec, 'custom').permissions, exec.permissions)
  })
})

describe('表单与载荷互转', () => {
  it('默认表单（RelayMesh）的载荷不含任何终端配置', () => {
    const payload = payloadFromForm(defaultPolicyForm())
    assert.deepEqual(payload.terminal_hosts, [])
    assert.equal(payload.terminal_mode_cap, '')
    assert.deepEqual(payload.terminal_limits, {})
    assert.equal(payload.expires_at, null)
    assert.deepEqual(payload.allowed_cidrs, [])
    assert.equal(payload.permissions?.terminal, false)
  })

  it('ExecMesh 表单：主机、模式上限、限额都进入载荷', () => {
    const f = {
      ...applyMode(defaultPolicyForm(), 'execmesh'),
      terminalHosts: ['host-a', 'host-b'],
      modeCap: 'approve' as const,
      maxConcurrent: '3',
      maxTimeoutSeconds: '600',
      allowedDirsText: '/srv/app\n/home/dev project\n/srv/app'
    }
    const payload = payloadFromForm(f)
    assert.deepEqual(payload.terminal_hosts, ['host-a', 'host-b'])
    assert.equal(payload.terminal_mode_cap, 'approve')
    assert.deepEqual(payload.terminal_limits, { max_concurrent: 3, max_timeout_seconds: 600, allowed_dirs: ['/srv/app', '/home/dev project'] })
  })

  it('全部主机时载荷是 ["*"]；空限额字段不会写出 0', () => {
    const f = { ...applyMode(defaultPolicyForm(), 'execmesh'), allHosts: true, terminalHosts: ['ignored'] }
    const payload = payloadFromForm(f)
    assert.deepEqual(payload.terminal_hosts, ['*'])
    assert.deepEqual(payload.terminal_limits, {})
  })

  it('关闭终端权限后残留的终端配置被清空', () => {
    const f = { ...defaultPolicyForm(), terminalHosts: ['host-a'], allHosts: true, modeCap: 'readonly' as const, maxConcurrent: '2' }
    const payload = payloadFromForm(f)
    assert.deepEqual(payload.terminal_hosts, [])
    assert.equal(payload.terminal_mode_cap, '')
    assert.deepEqual(payload.terminal_limits, {})
  })

  it('从凭据还原表单后再生成载荷应当等价（往返）', () => {
    const cred = credential({
      permissions: { ...execMeshPreset() },
      terminal_hosts: ['host-a', 'host-b'],
      terminal_mode_cap: 'auto',
      terminal_limits: { max_concurrent: 2, max_output_bytes: 1048576, allowed_dirs: ['/srv'] },
      expires_at: '2030-06-01T08:30:00Z',
      allowed_cidrs: ['10.0.0.0/8', '192.168.1.5']
    })
    const form = formFromCredential(cred)
    assert.equal(form.allHosts, false)
    assert.deepEqual(form.terminalHosts, ['host-a', 'host-b'])
    const payload = payloadFromForm(form)
    assert.deepEqual(payload.terminal_hosts, ['host-a', 'host-b'])
    assert.equal(payload.terminal_mode_cap, 'auto')
    assert.deepEqual(payload.terminal_limits, { max_concurrent: 2, max_output_bytes: 1048576, allowed_dirs: ['/srv'] })
    assert.deepEqual(payload.allowed_cidrs, ['10.0.0.0/8', '192.168.1.5'])
    assert.equal(payload.expires_at, new Date('2030-06-01T08:30:00Z').toISOString())
  })

  it('["*"] 还原为「全部主机」', () => {
    const form = formFromCredential(credential({ permissions: execMeshPreset(), terminal_hosts: ['*'] }))
    assert.equal(form.allHosts, true)
    assert.deepEqual(form.terminalHosts, [])
  })

  it('旧凭据（没有任何新字段）可还原且终端位为 false', () => {
    const legacy = credential({ permissions: { feedback: true, sessions: true, system_info: true, skills: true, configure: true, execute: true } })
    const form = formFromCredential(legacy)
    assert.equal(form.permissions.terminal, false)
    assert.equal(modeOf(form.permissions), 'relaymesh')
    assert.equal(form.expiresAtLocal, '')
    assert.equal(form.allowedCidrsText, '')
  })
})

describe('校验', () => {
  it('没有任何工具权限时不通过', () => {
    const f = { ...defaultPolicyForm(), permissions: { ...relayMeshPreset(), feedback: false, sessions: false, system_info: false, skills: false, configure: false, execute: false } }
    assert.match(validatePolicyForm(f), /至少选择/)
  })

  it('开启终端权限但没选主机时不通过；选主机或全部主机后通过', () => {
    const base = applyMode(defaultPolicyForm(), 'execmesh')
    assert.match(validatePolicyForm(base), /主机/)
    assert.equal(validatePolicyForm({ ...base, terminalHosts: ['host-a'] }), '')
    assert.equal(validatePolicyForm({ ...base, allHosts: true }), '')
  })

  it('限额必须是非负整数，留空表示不限', () => {
    const base = { ...applyMode(defaultPolicyForm(), 'execmesh'), allHosts: true }
    assert.equal(validatePolicyForm({ ...base, maxConcurrent: '' }), '')
    assert.equal(validatePolicyForm({ ...base, maxConcurrent: '0' }), '')
    assert.match(validatePolicyForm({ ...base, maxConcurrent: '-1' }), /最大并发/)
    assert.match(validatePolicyForm({ ...base, maxWaitSeconds: '1.5' }), /等待上限/)
    assert.match(validatePolicyForm({ ...base, maxOutputBytes: 'abc' }), /输出上限/)
  })

  it('RelayMesh 档不需要主机', () => {
    assert.equal(validatePolicyForm(defaultPolicyForm()), '')
  })
})

describe('辅助函数', () => {
  it('列表拆分与去重', () => {
    assert.deepEqual(splitList('10.0.0.0/8, 192.168.1.5\n10.0.0.0/8;fd00::/8  '), ['10.0.0.0/8', '192.168.1.5', 'fd00::/8'])
    assert.deepEqual(splitList(''), [])
    assert.deepEqual(splitLines(' /a b \n\n/c\n/a b'), ['/a b', '/c'])
  })

  it('本地时间输入与 ISO 往返；非法值返回空', () => {
    const iso = '2030-06-01T08:30:00.000Z'
    const local = isoToLocalInput(iso)
    assert.match(local, /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/)
    assert.equal(localInputToIso(local), new Date(local).toISOString())
    assert.equal(isoToLocalInput(null), '')
    assert.equal(isoToLocalInput('garbage'), '')
    assert.equal(localInputToIso(''), null)
    assert.equal(localInputToIso('garbage'), null)
  })

  it('模式上限文案', () => {
    assert.equal(modeCapLabel(''), '跟随主机')
    assert.equal(modeCapLabel('approve'), '需人工批准')
    assert.equal(modeCapLabel(undefined), '跟随主机')
  })
})

describe('ExecMesh 兼容命名', () => {
  it('预设默认不开启，且不影响模式与档位判断', () => {
    assert.equal(relayMeshPreset().terminal_compat, false)
    assert.equal(execMeshPreset().terminal_compat, false)
    const compat = { ...execMeshPreset(), terminal_compat: true }
    assert.equal(modeOf(compat), 'execmesh')
    assert.equal(tierOf(compat), 'execmesh')
    assert.equal(tierOf({ ...relayMeshPreset(), terminal: true, terminal_compat: true }), 'hybrid')
  })

  it('工具名清单随开关切换；没有终端执行权限时永远是原生名字', () => {
    assert.deepEqual([...terminalToolNames(execMeshPreset())], [...TERMINAL_NATIVE_TOOLS])
    assert.deepEqual([...terminalToolNames({ ...execMeshPreset(), terminal_compat: true })], [...TERMINAL_COMPAT_TOOLS])
    assert.deepEqual([...terminalToolNames({ ...relayMeshPreset(), terminal_compat: true })], [...TERMINAL_NATIVE_TOOLS])
    assert.ok(TERMINAL_COMPAT_TOOLS.includes('exec') && TERMINAL_COMPAT_TOOLS.includes('job_status'))
  })

  it('读取已有令牌：没有终端权限时不带兼容开关', () => {
    const withCompat = formFromCredential(credential({ permissions: { ...execMeshPreset(), terminal_compat: true }, terminal_hosts: ['*'] }))
    assert.equal(withCompat.permissions.terminal_compat, true)
    const stale = formFromCredential(credential({ permissions: { ...relayMeshPreset(), terminal_compat: true } }))
    assert.equal(stale.permissions.terminal_compat, false)
  })

  it('提交时：兼容开关只在有终端执行权限时发送', () => {
    const on = { ...defaultPolicyForm(), permissions: { ...execMeshPreset(), terminal_compat: true }, allHosts: true }
    assert.equal(payloadFromForm(on).permissions?.terminal_compat, true)
    const off = { ...defaultPolicyForm(), permissions: { ...relayMeshPreset(), terminal_compat: true } }
    assert.equal(payloadFromForm(off).permissions?.terminal_compat, false)
  })

  it('切换预设：保留已选的兼容命名，离开终端档则清掉', () => {
    const form = { ...defaultPolicyForm(), permissions: { ...relayMeshPreset(), terminal: true, terminal_compat: true } }
    assert.equal(applyMode(form, 'execmesh').permissions.terminal_compat, true)
    assert.equal(applyMode(form, 'relaymesh').permissions.terminal_compat, false)
    assert.equal(applyMode(defaultPolicyForm(), 'execmesh').permissions.terminal_compat, false)
  })
})

describe('能力 URL 凭据', () => {
  const host = 'RM-AAAA-BBBB-CCCC-DDDD'
  const now = new Date('2026-10-01T08:00:00Z')

  it('默认表单不是能力凭据，载荷里也不带该字段', () => {
    const f = defaultPolicyForm()
    assert.equal(f.capability, false)
    assert.equal('capability' in payloadFromForm(f, { creating: true }), false)
  })

  it('打开：收窄为 ExecMesh 档、取消全部主机、补默认到期时间；已填的到期时间与来源限制保留', () => {
    const start = {
      ...defaultPolicyForm(),
      permissions: { ...relayMeshPreset(), terminal: true, terminal_compat: true },
      allHosts: true,
      allowedCidrsText: '10.0.0.0/8',
      modeCap: 'approve' as const
    }
    const f = applyCapability(start, true, now)
    assert.equal(f.capability, true)
    assert.equal(modeOf(f.permissions), 'execmesh')
    assert.equal(f.permissions.terminal_compat, true, '兼容命名的选择应保留')
    assert.equal(f.allHosts, false)
    assert.equal(f.allowedCidrsText, '10.0.0.0/8')
    assert.equal(f.modeCap, 'approve')
    const diffDays = (new Date(f.expiresAtLocal).getTime() - now.getTime()) / 86_400_000
    assert.ok(Math.abs(diffDays - CAPABILITY_DEFAULT_DAYS) < 1, `默认有效期应约 ${CAPABILITY_DEFAULT_DAYS} 天，实际 ${diffDays}`)

    const kept = applyCapability({ ...start, expiresAtLocal: '2027-01-02T03:04' }, true, now)
    assert.equal(kept.expiresAtLocal, '2027-01-02T03:04')
  })

  it('关闭只取消标记，不动其余配置', () => {
    const on = applyCapability({ ...defaultPolicyForm(), terminalHosts: [host] }, true, now)
    const off = applyCapability(on, false, now)
    assert.equal(off.capability, false)
    assert.deepEqual(off.terminalHosts, [host])
    assert.equal(off.expiresAtLocal, on.expiresAtLocal)
  })

  it('默认到期时间是 datetime-local 格式', () => {
    assert.match(defaultCapabilityExpiry(now), /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/)
    const later = defaultCapabilityExpiry(now, 90)
    assert.ok(new Date(later).getTime() > new Date(defaultCapabilityExpiry(now, 30)).getTime())
  })

  it('校验：必须是 ExecMesh 档、不能全部主机、必须逐台选主机', () => {
    const ok = { ...applyCapability(defaultPolicyForm(), true, now), terminalHosts: [host] }
    assert.equal(validatePolicyForm(ok), '')

    assert.match(validatePolicyForm({ ...ok, terminalHosts: [] }), /逐台/)
    assert.match(validatePolicyForm({ ...ok, allHosts: true }), /全部主机/)
    assert.match(validatePolicyForm({ ...ok, permissions: { ...ok.permissions, feedback: true } }), /ExecMesh 档/)
    assert.match(validatePolicyForm({ ...ok, permissions: { ...ok.permissions, terminal: false, terminal_files: true } }), /ExecMesh 档/)
    // 同样的配置不开能力开关时，全部主机是允许的。
    assert.equal(validatePolicyForm({ ...ok, capability: false, allHosts: true }), '')
  })

  it('载荷：只在创建时带 capability，更新时不带（凭据类型不可更改）', () => {
    const f = { ...applyCapability(defaultPolicyForm(), true, now), terminalHosts: [host] }
    const create = payloadFromForm(f, { creating: true })
    assert.equal(create.capability, true)
    assert.deepEqual(create.terminal_hosts, [host])
    assert.equal(create.permissions?.terminal, true)
    assert.equal(create.permissions?.feedback, false)
    assert.equal('capability' in payloadFromForm(f), false)
    assert.equal('capability' in payloadFromForm(f, { creating: false }), false)
    // 非能力凭据创建时同样不带。
    assert.equal('capability' in payloadFromForm({ ...f, capability: false }, { creating: true }), false)
  })

  it('读取已有凭据：带上能力标记', () => {
    const cap = formFromCredential(credential({ capability: true, permissions: execMeshPreset(), terminal_hosts: [host] }))
    assert.equal(cap.capability, true)
    assert.deepEqual(cap.terminalHosts, [host])
    assert.equal(formFromCredential(credential()).capability, false)
  })

  it('能力网址由页面源与服务端返回的路径拼成', () => {
    assert.equal(capabilityUrl('http://10.0.0.5:18775', '/mcp/c/rmc-abc'), 'http://10.0.0.5:18775/mcp/c/rmc-abc')
    assert.equal(capabilityUrl('https://relay.example.com/', '/mcp/c/rmc-abc'), 'https://relay.example.com/mcp/c/rmc-abc')
    assert.equal(capabilityUrl('http://h:1', 'mcp/c/x'), 'http://h:1/mcp/c/x')
  })
})

describe('终端文件权限与允许目录', () => {
  const files = (over: Partial<ReturnType<typeof defaultPolicyForm>> = {}) => ({
    ...applyMode(defaultPolicyForm(), 'execmesh'),
    permissions: { ...execMeshPreset(), terminal_files: true },
    allHosts: true,
    allowedPathsText: '/srv/data\n~/work',
    ...over
  })

  it('权限页提供「终端文件」开关；只开文件时还会带上只读的 terminal_targets', () => {
    const opt = TERMINAL_OPTIONS.find(o => o.key === 'terminal_files')
    assert.ok(opt)
    assert.equal(opt.tools, 'terminal_targets, terminal_files')
  })

  it('带文件权限时：必须填写允许目录，且逐条通过校验', () => {
    assert.equal(validatePolicyForm(files()), '')
    assert.match(validatePolicyForm(files({ allowedPathsText: '' })), /允许访问的目录/)
    assert.match(validatePolicyForm(files({ allowedPathsText: '  \n \n' })), /允许访问的目录/)
    assert.match(validatePolicyForm(files({ allowedPathsText: '/srv/data\nrelative/path' })), /relative\/path.*绝对路径/)
    assert.match(validatePolicyForm(files({ allowedPathsText: '/' })), /根目录/)
    const tooMany = Array.from({ length: MAX_ALLOWED_PATHS + 1 }, (_, i) => `/srv/d${i}`).join('\n')
    assert.match(validatePolicyForm(files({ allowedPathsText: tooMany })), new RegExp(`最多 ${MAX_ALLOWED_PATHS} 个`))
    // 没有文件权限时，允许目录是无关的残留，不参与校验。
    assert.equal(validatePolicyForm({ ...files({ allowedPathsText: '' }), permissions: execMeshPreset() }), '')
  })

  it('载荷：允许目录去重后进入 terminal_limits.allowed_paths；没有文件权限时不发送', () => {
    const payload = payloadFromForm(files({ allowedPathsText: ' /srv/data \n~/work\n/srv/data\nC:\\Users\\me\\p ' }))
    assert.deepEqual(payload.terminal_limits, { allowed_paths: ['/srv/data', '~/work', 'C:\\Users\\me\\p'] })
    assert.equal(payload.permissions?.terminal_files, true)

    const noFiles = payloadFromForm({ ...files(), permissions: execMeshPreset() })
    assert.equal('allowed_paths' in (noFiles.terminal_limits ?? {}), false)
    // 完全没有终端权限：整个终端配置清空。
    assert.deepEqual(payloadFromForm({ ...defaultPolicyForm(), allowedPathsText: '/srv' }).terminal_limits, {})
  })

  it('读取已有凭据：允许目录还原为多行文本，往返等价', () => {
    const cred = credential({
      permissions: { ...execMeshPreset(), terminal_files: true },
      terminal_hosts: ['*'],
      terminal_limits: { max_concurrent: 2, allowed_dirs: ['/srv'], allowed_paths: ['/srv/data', '~/work'] }
    })
    const form = formFromCredential(cred)
    assert.equal(form.allowedPathsText, '/srv/data\n~/work')
    assert.equal(form.allowedDirsText, '/srv')
    assert.deepEqual(payloadFromForm(form).terminal_limits, { max_concurrent: 2, allowed_dirs: ['/srv'], allowed_paths: ['/srv/data', '~/work'] })
    assert.equal(formFromCredential(credential()).allowedPathsText, '')
  })

  it('rootProblem 与服务端 RootProblem 一致', () => {
    for (const ok of ['/srv/data', '/a', '~', '~/work', '~\\work', 'C:\\Users\\dev', 'C:/Users/dev', 'd:\\x', '/srv/a b', '/日本語/目录', '/a/./b']) {
      assert.equal(rootProblem(ok), '', `${ok} 应合法`)
    }
    const bad: [string, RegExp][] = [
      ['', /不能为空/],
      ['/', /根目录/],
      ['//', /UNC/],
      ['/./', /根目录/],
      ['C:\\', /盘符根目录/],
      ['C:/', /盘符根目录/],
      ['C:', /绝对路径/],
      ['relative', /绝对路径/],
      ['./x', /绝对路径/],
      ['../x', /\.\./],
      ['/a/../b', /\.\./],
      ['/a\\..\\b', /\.\./],
      ['~user/x', /~用户名/],
      ['\\\\srv\\share\\x', /UNC/],
      ['//srv/x', /UNC/],
      ['/srv/a\nb', /非法字符/],
      ['/srv/a\u0000b', /非法字符/],
      ['/srv/a\u0085b', /非法字符/],
      ['/srv/\ud800x', /非法字符/],
      ['C:\\a:b', /冒号/],
      ['C:\\dir.\\x', /点或空格/],
      ['C:\\dir \\x', /点或空格/],
      ['/' + 'a'.repeat(1030), /过长/]
    ]
    for (const [input, re] of bad) {
      assert.match(rootProblem(input), re, `${JSON.stringify(input)} 应被拒绝`)
    }
  })

  it('pathIssues：逐行列出不合法的目录，合法行不出现；空行与重复行被忽略', () => {
    assert.deepEqual(pathIssues(''), [])
    assert.deepEqual(pathIssues('/srv/data\n~/work\n\n  \n/srv/data'), [])
    const got = pathIssues('/srv/data\n/\nrelative\n/\nC:\\')
    assert.deepEqual(
      got.map(i => i.path),
      ['/', 'relative', 'C:\\'],
      '重复的「/」只报一次，且保持输入顺序'
    )
    assert.match(got[0].why, /根目录/)
    assert.match(got[2].why, /盘符根目录/)
  })

  it('整盘写法的提示里带有改法；其它问题不带', () => {
    const [root] = pathIssues('/')
    const text = pathIssueText(root)
    assert.match(text, /不能把文件系统根目录作为允许目录/)
    assert.ok(text.includes(ROOT_FIX_HINT), '应附上改法')
    assert.match(text, /~\//)
    assert.match(pathIssueText(pathIssues('C:\\')[0]), /盘符根目录.*~\//)
    assert.ok(!pathIssueText(pathIssues('relative')[0]).includes(ROOT_FIX_HINT), '相对路径不是整盘写法，不需要改法')
  })

  it('图 2 的场景：全部工具权限 + 全部主机 + 允许目录填 /，校验必须给出看得懂的原因', () => {
    const full = {
      ...defaultPolicyForm(),
      permissions: { ...relayMeshPreset(), ...execMeshPreset(), terminal: true, terminal_files: true },
      allHosts: true,
      allowedPathsText: '/'
    }
    const msg = validatePolicyForm(full)
    assert.match(msg, /允许访问的目录「\/」不合法/)
    assert.match(msg, /根目录/)
    assert.match(msg, /~\//, '要告诉用户改成什么')
    // 改成合法目录后通过。
    assert.equal(validatePolicyForm({ ...full, allowedPathsText: '~/' }), '')
  })
})

describe('允许整个文件系统（高风险开关）', () => {
  const files = (over: Partial<ReturnType<typeof defaultPolicyForm>> = {}) => ({
    ...applyMode(defaultPolicyForm(), 'execmesh'),
    permissions: { ...execMeshPreset(), terminal_files: true },
    allHosts: true,
    allowedPathsText: '~/work',
    ...over
  })
  const wholeFsCredential = (over: Partial<MCPCredential> = {}) =>
    credential({
      permissions: { ...execMeshPreset(), terminal_files: true },
      terminal_hosts: ['*'],
      terminal_limits: { allowed_paths: ['~/work'], allow_whole_fs: true },
      ...over
    })

  it('默认关闭；读取已有凭据时还原开关', () => {
    assert.equal(defaultPolicyForm().allowWholeFs, false)
    assert.equal(formFromCredential(credential()).allowWholeFs, false)
    assert.equal(formFromCredential(wholeFsCredential()).allowWholeFs, true)
    // 老凭据（没有这个字段）当作关闭。
    assert.equal(formFromCredential(wholeFsCredential({ terminal_limits: { allowed_paths: ['~/work'] } })).allowWholeFs, false)
  })

  it('开关打开时不再要求填写目录；开关关闭、目录为空时仍然要求，且提示里指向这个开关', () => {
    assert.equal(validatePolicyForm(files({ allowedPathsText: '', allowWholeFs: true })), '')
    const msg = validatePolicyForm(files({ allowedPathsText: '', allowWholeFs: false }))
    assert.match(msg, /允许访问的目录/)
    assert.match(msg, /允许整个文件系统/)
  })

  it('开关只能由专门的开关打开：目录框里的 / 在开关打开时同样被拒绝（防手误）', () => {
    assert.match(validatePolicyForm(files({ allowedPathsText: '/', allowWholeFs: true })), /根目录/)
    assert.match(validatePolicyForm(files({ allowedPathsText: '/', allowWholeFs: false })), /允许整个文件系统.*开关/)
    assert.match(validatePolicyForm(files({ allowedPathsText: 'C:\\', allowWholeFs: true })), /盘符根目录/)
    // 开关打开时被保留的目录也要合法，否则服务端会拒绝整次保存。
    assert.match(validatePolicyForm(files({ allowedPathsText: '~/work\nrelative', allowWholeFs: true })), /relative.*绝对路径/)
  })

  it('载荷：带文件权限且打开开关才发送 allow_whole_fs；目录原样保留；没有文件权限时都不发送', () => {
    assert.deepEqual(payloadFromForm(files({ allowWholeFs: true })).terminal_limits, { allowed_paths: ['~/work'], allow_whole_fs: true })
    assert.deepEqual(payloadFromForm(files({ allowWholeFs: true, allowedPathsText: '' })).terminal_limits, { allow_whole_fs: true })
    assert.deepEqual(payloadFromForm(files({ allowWholeFs: false })).terminal_limits, { allowed_paths: ['~/work'] })
    const noFiles = payloadFromForm({ ...files({ allowWholeFs: true }), permissions: execMeshPreset() })
    assert.equal('allow_whole_fs' in (noFiles.terminal_limits ?? {}), false)
    assert.equal('allowed_paths' in (noFiles.terminal_limits ?? {}), false)
    assert.deepEqual(payloadFromForm({ ...defaultPolicyForm(), allowWholeFs: true }).terminal_limits, {})
  })

  it('往返：打开开关的凭据读入表单再发出，开关与目录都不丢', () => {
    const form = formFromCredential(wholeFsCredential())
    assert.deepEqual(payloadFromForm(form).terminal_limits, { allowed_paths: ['~/work'], allow_whole_fs: true })
  })

  it('grantsWholeFs：只有「从无效变成有效」才算授予（需要重新验证密码）', () => {
    const on = files({ allowWholeFs: true })
    const off = files({ allowWholeFs: false })
    assert.equal(grantsWholeFs(null, on), true, '新建时直接带开关')
    assert.equal(grantsWholeFs(null, off), false)
    assert.equal(grantsWholeFs(credential({ permissions: { ...execMeshPreset(), terminal_files: true }, terminal_limits: { allowed_paths: ['~/work'] } }), on), true, '关 → 开')
    assert.equal(grantsWholeFs(wholeFsCredential(), on), false, '开 → 开：只是改别的字段，不需要')
    assert.equal(grantsWholeFs(wholeFsCredential(), off), false, '开 → 关：刹车，不需要')
    // 绕法：保存的凭据里开关还留着、但文件权限是关的（无效）；这时只把文件权限重新打开，同样是「授予」。
    const dormant = wholeFsCredential({ permissions: execMeshPreset() })
    assert.equal(grantsWholeFs(dormant, on), true, '休眠的开关被重新激活')
    // 表单里开关虽开、但文件权限没有开：不生效，也就不算授予。
    assert.equal(grantsWholeFs(null, { ...on, permissions: execMeshPreset() }), false)
  })

  it('hasWholeFs / fileScopeSummary：只有同时带文件权限才算对整个文件系统开放', () => {
    assert.equal(hasWholeFs(wholeFsCredential()), true)
    assert.equal(hasWholeFs(wholeFsCredential({ permissions: execMeshPreset() })), false)
    assert.equal(hasWholeFs(credential()), false)
    assert.equal(fileScopeSummary(wholeFsCredential()), '文件：整个文件系统')
    assert.equal(fileScopeSummary(credential({ permissions: { ...execMeshPreset(), terminal_files: true }, terminal_limits: { allowed_paths: ['/a', '/b'] } })), '文件目录 2 个')
    assert.equal(fileScopeSummary(credential({ permissions: execMeshPreset() })), '')
  })
})

describe('运行预设权限（#179）', () => {
  const withLauncher = (over: Partial<MCPPermissions> = {}): MCPPermissions => ({ ...relayMeshPreset(), launcher: true, ...over })

  it('默认关闭：新令牌与两个档位预设都不含它，权限选项里单独成组', () => {
    assert.equal(defaultPolicyForm().permissions.launcher, false)
    assert.equal(relayMeshPreset().launcher, false)
    assert.equal(execMeshPreset().launcher, false)
    assert.deepEqual(LAUNCHER_OPTIONS.map(o => [o.key, o.tools]), [['launcher', 'preset_run']])
    assert.ok(!GOVERNANCE_OPTIONS.some(o => o.key === 'launcher'), '它不是治理类工具权限')
    assert.ok(!TERMINAL_OPTIONS.some(o => o.key === 'launcher'), '它也不是终端工具权限')
  })

  it('带它的权限是自定义模式；档位仍按治理 / 终端工具推导', () => {
    assert.equal(modeOf(relayMeshPreset()), 'relaymesh')
    assert.equal(modeOf(withLauncher()), 'custom')
    assert.equal(tierOf(withLauncher()), 'relaymesh')
    assert.equal(tierOf(withLauncher({ terminal: true })), 'hybrid')
    // 只有「运行预设」本身不构成任何档位。
    assert.equal(tierOf({ ...relayMeshPreset(), feedback: false, sessions: false, system_info: false, skills: false, configure: false, execute: false, launcher: true }), 'none')
  })

  it('切换到预设档位会清掉它；选自定义时保留', () => {
    const f = { ...defaultPolicyForm(), permissions: withLauncher() }
    assert.equal(applyMode(f, 'relaymesh').permissions.launcher, false)
    assert.equal(applyMode(f, 'execmesh').permissions.launcher, false)
    assert.equal(applyMode(f, 'custom').permissions.launcher, true)
    // 能力 URL 会先收窄为 ExecMesh 档：它不可能带着「运行预设」进入能力凭据。
    assert.equal(applyCapability(f, true).permissions.launcher, false)
  })

  it('读取已有凭据：旧凭据没有这个字段时为 false，带上则还原；往返等价', () => {
    assert.equal(formFromCredential(credential()).permissions.launcher, false)
    const c = credential({ permissions: withLauncher() })
    const f = formFromCredential(c)
    assert.equal(f.permissions.launcher, true)
    const sent = payloadFromForm(f).permissions as MCPPermissions
    assert.equal(sent.launcher, true)
    assert.deepEqual(formFromCredential(credential({ permissions: sent })).permissions, f.permissions)
  })

  it('载荷：只带「运行预设」时不发送任何终端配置', () => {
    const payload = payloadFromForm({ ...defaultPolicyForm(), permissions: withLauncher() })
    assert.equal(payload.permissions?.launcher, true)
    assert.deepEqual(payload.terminal_hosts, [])
    assert.equal(payload.terminal_mode_cap, '')
    assert.deepEqual(payload.terminal_limits, {})
  })

  it('校验：必须同时开治理类工具；能力 URL 凭据不能带它', () => {
    assert.equal(validatePolicyForm({ ...defaultPolicyForm(), permissions: withLauncher() }), '')
    const execOnly = { ...applyMode(defaultPolicyForm(), 'execmesh'), allHosts: true }
    assert.match(validatePolicyForm({ ...execOnly, permissions: { ...execOnly.permissions, launcher: true } }), /运行预设.*治理类工具/)
    const cap = applyCapability(defaultPolicyForm(), true)
    const capWithLauncher = { ...cap, terminalHosts: ['host-a'], permissions: { ...cap.permissions, launcher: true } }
    assert.match(validatePolicyForm(capWithLauncher), /能力 URL.*运行预设/)
  })
})
