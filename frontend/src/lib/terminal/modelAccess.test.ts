// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import {
  ALL_HOSTS,
  MAX_HOSTS_PER_TOKEN,
  accessHosts,
  accessTokens,
  cellGranted,
  columnState,
  defaultSelection,
  grantSummary,
  hiddenGrants,
  overLimit,
  rowState,
  setRow,
  toggleCell,
  wouldGrant,
  wouldRevoke,
  type AccessToken,
} from './modelAccess'

type H = Parameters<typeof accessHosts>[0][number]

const host = (code: string, over: Partial<H> = {}): H => ({
  machine_code: code,
  remark: '',
  hostname: code.toLowerCase(),
  status: 'enabled',
  temporary: false,
  online: true,
  kind: 'agent',
  allow_model: true,
  default_for_new_models: 'deny',
  ...over,
})

const token = (over: Partial<AccessToken> = {}): AccessToken => ({
  id: 1,
  name: 't',
  active: true,
  capability: false,
  wildcard: false,
  files: false,
  hosts: [],
  expired: false,
  ...over,
})

describe('矩阵的列', () => {
  it('只含已启用的持久主机，临时 / 停用 / 被忽略的都不出现', () => {
    const cols = accessHosts([
      host('RM-A'),
      host('RM-TMP', { temporary: true }),
      host('RM-OFF', { status: 'disabled' }),
      host('RM-BLK', { status: 'blocked' }),
    ])
    assert.deepEqual(cols.map(c => c.code), ['RM-A'])
  })

  it('带出模型访问、默认态与类型，并按名称排序', () => {
    const cols = accessHosts([
      host('RM-2', { remark: '乙机', allow_model: false }),
      host('RM-1', { remark: '甲机', default_for_new_models: 'allow', kind: 'ssh' }),
    ])
    assert.deepEqual(cols.map(c => c.title), ['甲机', '乙机'])
    assert.equal(cols[0].defaultAllow, true)
    assert.equal(cols[0].ssh, true)
    assert.equal(cols[1].allowModel, false)
    assert.equal(cols[1].defaultAllow, false)
  })

  it('缺失默认态按不授予处理', () => {
    const cols = accessHosts([host('RM-A', { default_for_new_models: undefined })])
    assert.equal(cols[0].defaultAllow, false)
  })

  it('经网关接入的 SSH 主机也出现在模型授权矩阵里；只有探测不出 POSIX 环境（只能当人工终端用）的那种不出现', () => {
    const ssh = (gateway?: string, termOnly?: boolean) => ({
      address: '10.0.0.5:22', user: 'ops', auth: 'password', key_type: 'ssh-ed25519', fingerprint: 'SHA256:x',
      has_secret: !gateway, backend_enabled: true, gateway, cred_on_gateway: !!gateway, term_only: termOnly,
    })
    const cols = accessHosts([
      host('RM-DIRECT', { kind: 'ssh', ssh: ssh() }),
      host('RM-VIA', { kind: 'ssh', ssh: ssh('RM-GATE') }),
      host('RM-TERMONLY', { kind: 'ssh', ssh: ssh('RM-GATE', true) }),
      host('RM-A'),
    ])
    assert.deepEqual(cols.map(c => c.code).sort(), ['RM-A', 'RM-DIRECT', 'RM-VIA'])
  })
})

describe('矩阵的行', () => {
  const now = Date.parse('2026-10-03T00:00:00Z')

  it('只收带终端或文件能力的令牌，其余只计数', () => {
    const { rows, skipped } = accessTokens(
      [
        { id: 1, name: '终端', is_active: true, permissions: { terminal: true }, terminal_hosts: ['RM-A'] },
        { id: 2, name: '文件', is_active: true, permissions: { terminal_files: true }, terminal_hosts: [] },
        { id: 3, name: '只有反馈', is_active: true, permissions: {} },
      ],
      now,
    )
    assert.deepEqual(rows.map(r => r.name), ['终端', '文件'])
    assert.equal(rows[1].files, true)
    assert.equal(skipped, 1)
  })

  it('通配符单独成标记，明确名单里不含它', () => {
    const { rows } = accessTokens([{ id: 1, name: 'x', is_active: true, permissions: { terminal: true }, terminal_hosts: ['*'] }], now)
    assert.equal(rows[0].wildcard, true)
    assert.deepEqual(rows[0].hosts, [])
  })

  it('停用、能力 URL、已过期都被带出', () => {
    const { rows } = accessTokens(
      [
        { id: 1, name: 'a', is_active: false, permissions: { terminal: true }, capability: true, expires_at: '2026-10-01T00:00:00Z' },
        { id: 2, name: 'b', is_active: true, permissions: { terminal: true }, expires_at: '2027-01-01T00:00:00Z' },
        { id: 3, name: 'c', is_active: true, permissions: { terminal: true }, expires_at: null },
      ],
      now,
    )
    assert.equal(rows[0].active, false)
    assert.equal(rows[0].capability, true)
    assert.equal(rows[0].expired, true)
    assert.equal(rows[1].expired, false)
    assert.equal(rows[2].expired, false)
  })

  it('terminal_hosts 缺失或为 null 视为没有授权', () => {
    const { rows } = accessTokens(
      [
        { id: 1, name: 'a', is_active: true, permissions: { terminal: true } },
        { id: 2, name: 'b', is_active: true, permissions: { terminal: true }, terminal_hosts: null },
      ],
      now,
    )
    assert.deepEqual(rows.map(r => r.hosts), [[], []])
  })
})

describe('单元格的编辑', () => {
  const visible = ['RM-A', 'RM-B', 'RM-C']

  it('通配令牌对每一台都算已授权', () => {
    const t = token({ wildcard: true })
    assert.equal(cellGranted(t, 'RM-A'), true)
    assert.equal(cellGranted(t, 'RM-ZZZ'), true)
  })

  it('勾选与取消只动这一台，保留矩阵之外的条目', () => {
    const t = token({ hosts: ['RM-A', 'RM-OLD'] })
    assert.deepEqual(toggleCell(t, 'RM-B', true, visible).sort(), ['RM-A', 'RM-B', 'RM-OLD'])
    assert.deepEqual(toggleCell(t, 'RM-A', false, visible), ['RM-OLD'])
  })

  it('重复勾选不产生重复条目', () => {
    const t = token({ hosts: ['RM-A'] })
    assert.deepEqual(toggleCell(t, 'RM-A', true, visible), ['RM-A'])
  })

  it('通配令牌取消一格：展开成矩阵里其余各台；再勾选保持通配', () => {
    const t = token({ wildcard: true })
    assert.deepEqual(toggleCell(t, 'RM-B', false, visible), ['RM-A', 'RM-C'])
    assert.deepEqual(toggleCell(t, 'RM-B', true, visible), [ALL_HOSTS])
  })
})

describe('整行', () => {
  const visible = ['RM-A', 'RM-B']

  it('全选是并集，保留矩阵之外的条目', () => {
    const t = token({ hosts: ['RM-OLD'] })
    assert.deepEqual(setRow(t, visible, true).sort(), ['RM-A', 'RM-B', 'RM-OLD'])
  })

  it('清空只清看得见的列，看不见的授权不被顺手抹掉', () => {
    const t = token({ hosts: ['RM-A', 'RM-OLD'] })
    assert.deepEqual(setRow(t, visible, false), ['RM-OLD'])
  })

  it('通配令牌：全选不变，清空得到空名单', () => {
    const t = token({ wildcard: true })
    assert.deepEqual(setRow(t, visible, true), [ALL_HOSTS])
    assert.deepEqual(setRow(t, visible, false), [])
  })

  it('行状态按矩阵里的列判定', () => {
    assert.equal(rowState(token({ hosts: [] }), visible), 'none')
    assert.equal(rowState(token({ hosts: ['RM-A'] }), visible), 'some')
    assert.equal(rowState(token({ hosts: ['RM-A', 'RM-B', 'RM-OLD'] }), visible), 'all')
    assert.equal(rowState(token({ wildcard: true }), visible), 'all')
    assert.equal(rowState(token({ hosts: ['RM-OLD'] }), visible), 'none')
    assert.equal(rowState(token({ hosts: ['RM-A'] }), []), 'none')
  })

  it('统计矩阵之外的授权数，通配令牌没有', () => {
    assert.equal(hiddenGrants(token({ hosts: ['RM-A', 'RM-OLD', 'RM-GONE'] }), visible), 2)
    assert.equal(hiddenGrants(token({ wildcard: true }), visible), 0)
  })
})

describe('整列', () => {
  const rows = [
    token({ id: 1, name: '甲', hosts: ['RM-A'] }),
    token({ id: 2, name: '乙', hosts: [] }),
    token({ id: 3, name: '丙', wildcard: true }),
    token({ id: 4, name: '丁', capability: true, hosts: ['RM-X'] }),
  ]

  it('列状态：通配令牌算已授权', () => {
    assert.equal(columnState(rows, 'RM-A'), 'some')
    assert.equal(columnState([rows[2]], 'RM-A'), 'all')
    assert.equal(columnState([rows[1]], 'RM-A'), 'none')
    assert.equal(columnState([], 'RM-A'), 'none')
  })

  it('授予现有令牌：跳过通配、能力 URL 与已含该主机的令牌（与服务端规则一致）', () => {
    assert.deepEqual(wouldGrant(rows, 'RM-A').map(t => t.name), ['乙'])
    assert.deepEqual(wouldGrant(rows, 'RM-Z').map(t => t.name), ['甲', '乙'])
  })

  it('收回：只涉及明确含有该主机的非通配令牌', () => {
    assert.deepEqual(wouldRevoke(rows, 'RM-A').map(t => t.name), ['甲'])
    assert.deepEqual(wouldRevoke(rows, 'RM-X').map(t => t.name), ['丁'])
  })
})

describe('上限、摘要与预选', () => {
  it('名单上限与服务端一致，通配符不计数', () => {
    assert.equal(MAX_HOSTS_PER_TOKEN, 100)
    assert.equal(overLimit(Array.from({ length: 100 }, (_, i) => `RM-${i}`)), false)
    assert.equal(overLimit(Array.from({ length: 101 }, (_, i) => `RM-${i}`)), true)
    assert.equal(overLimit([ALL_HOSTS]), false)
  })

  it('授予预览：数量、名称与折叠', () => {
    assert.equal(grantSummary([]), '没有需要授予的模型令牌')
    assert.equal(grantSummary(['甲', '乙']), '将授予 2 个模型令牌：甲、乙')
    assert.equal(grantSummary(['1', '2', '3', '4', '5', '6']), '将授予 6 个模型令牌：1、2、3、4 等')
  })

  it('新建令牌预选：只含已启用、持久且默认授予的主机；能力 URL 令牌从不预选', () => {
    const hosts = [
      host('RM-A', { default_for_new_models: 'allow' }),
      host('RM-B', { default_for_new_models: 'deny' }),
      host('RM-C', { default_for_new_models: 'allow', status: 'disabled' }),
      host('RM-D', { default_for_new_models: 'allow', temporary: true }),
      host('RM-E', { default_for_new_models: 'allow' }),
    ]
    assert.deepEqual(defaultSelection(hosts, false), ['RM-A', 'RM-E'])
    assert.deepEqual(defaultSelection(hosts, true), [])
  })
})
