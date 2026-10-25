// 运行：pnpm --dir frontend run test:unit
// OAuth 设置页的纯逻辑（#234）：状态卡文案、浏览器内自检、「按此客户端设置」预设、授权列表的人话描述，
// 以及向导文字与 docs/oauth.md「快速开始」的一致性。
import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, it } from 'node:test'
import type { OAuthEffective, OAuthGrantRow, OAuthPolicy } from '../api/client'
import {
  applyClientPreset,
  checkStatusTone,
  CLIENT_PRESETS,
  formatLocalMinute,
  grantCreatedText,
  grantPermissionText,
  grantUsageText,
  policyDirty,
  PRESET_TOGGLE_LABELS,
  PRESET_TOGGLE_NOTES,
  presetById,
  presetMissing,
  publicBaseNote,
  runOAuthSelfCheck,
  selfCheckSummary,
  switchState,
  toneClasses,
  type FetchFn,
  type MiniInit,
  type MiniResponse,
} from './oauthSetup'

function policy(over: Partial<OAuthPolicy> = {}): OAuthPolicy {
  return {
    enabled: false,
    public_url: '',
    allow_dcr: false,
    access_token_ttl_minutes: 60,
    refresh_idle_days: 30,
    refresh_absolute_days: 90,
    ...over,
  }
}

function effective(over: Partial<OAuthEffective> = {}): OAuthEffective {
  const base = 'https://hub.example.com'
  return {
    env_off: false,
    enabled: true,
    public_base: base,
    public_base_source: 'settings',
    settings_public_url_shadowed: false,
    connector_url: `${base}/mcp`,
    resource_metadata_url: `${base}/.well-known/oauth-protected-resource/mcp`,
    authorization_server_metadata_url: `${base}/.well-known/oauth-authorization-server`,
    ...over,
  }
}

describe('switchState：总开关此刻真的生效了吗', () => {
  it('没有 effective（旧后端）时不给结论', () => {
    assert.equal(switchState(null), null)
    assert.equal(switchState(undefined), null)
  })
  it('被环境变量关闭：红色，并说明怎么办', () => {
    const s = switchState(effective({ env_off: true, enabled: false }))!
    assert.equal(s.kind, 'env_off')
    assert.equal(s.tone, 'danger')
    assert.match(s.detail, /RELAYMESH_OAUTH=off/)
    assert.match(s.detail, /重启/)
  })
  it('环境变量关闭优先于其它状态（即使策略写着开）', () => {
    assert.equal(switchState(effective({ env_off: true, enabled: true }))!.kind, 'env_off')
  })
  it('开启 / 未开启', () => {
    assert.equal(switchState(effective({ enabled: true }))!.kind, 'on')
    const off = switchState(effective({ enabled: false }))!
    assert.equal(off.kind, 'off')
    assert.match(off.detail, /启用 OAuth 授权服务器/)
  })
})

describe('publicBaseNote：对外地址的来源', () => {
  it('推导出来的：警告，并点明公网访问不到', () => {
    const n = publicBaseNote(effective({ public_base_source: 'derived' }))!
    assert.equal(n.tone, 'warn')
    assert.match(n.detail, /公网/)
    assert.match(n.detail, /RELAYMESH_PUBLIC_URL/)
  })
  it('来自设置页：正常', () => {
    assert.equal(publicBaseNote(effective({ public_base_source: 'settings' }))!.tone, 'ok')
  })
  it('来自环境变量：正常；设置页的值被盖掉时：警告', () => {
    assert.equal(publicBaseNote(effective({ public_base_source: 'env' }))!.tone, 'ok')
    const shadowed = publicBaseNote(effective({ public_base_source: 'env', settings_public_url_shadowed: true }))!
    assert.equal(shadowed.tone, 'warn')
    assert.match(shadowed.label, /被盖掉/)
  })
  it('没有 effective 时返回 null', () => {
    assert.equal(publicBaseNote(null), null)
  })
})

describe('policyDirty：有没有未保存的改动', () => {
  it('相同为 false；空白差异不算改动', () => {
    assert.equal(policyDirty(policy(), policy()), false)
    assert.equal(policyDirty(policy({ public_url: 'https://a.example.com' }), policy({ public_url: ' https://a.example.com ' })), false)
    assert.equal(policyDirty(null, policy()), false)
  })
  it('任何一项策略改了都算', () => {
    const cases: Partial<OAuthPolicy>[] = [
      { enabled: true },
      { public_url: 'https://x.example.com' },
      { allow_dcr: true },
      { allow_dcr: true, dcr_always: true },
      { access_token_ttl_minutes: 120 },
      { refresh_idle_days: 7 },
      { refresh_absolute_days: 30 },
    ]
    for (const c of cases) assert.equal(policyDirty(policy(), policy(c)), true, JSON.stringify(c))
  })
  it('dcr_always 是后来加的可选字段：旧后端没有它（undefined）与 false 是同一种状态，不算改动（#239）', () => {
    const noField = policy({ allow_dcr: true })
    assert.equal('dcr_always' in noField, false)
    assert.equal(policyDirty(noField, policy({ allow_dcr: true, dcr_always: false })), false)
    assert.equal(policyDirty(policy({ allow_dcr: true, dcr_always: false }), noField), false)
    assert.equal(policyDirty(noField, policy({ allow_dcr: true, dcr_always: true })), true)
  })
  it('旧后端 / 旧缓存里残留的已删除字段（信任来源四项）不算改动，也不参与比较（#238）', () => {
    const legacy = { ...policy(), allow_chatgpt: true, allow_claude: true, allow_loopback: true, client_host_allowlist: ['x.example.com'] } as unknown as OAuthPolicy
    assert.equal(policyDirty(policy(), legacy), false)
  })
})

describe('客户端预设：「按此客户端设置」', () => {
  it('只勾选该客户端需要的项，绝不碰总开关与对外地址', () => {
    for (const p of CLIENT_PRESETS) {
      const before = policy({ enabled: false, public_url: 'https://keep.example.com' })
      const after = applyClientPreset(before, p.id)
      assert.equal(after.enabled, false, `${p.id} 不能自动打开总开关`)
      assert.equal(after.public_url, 'https://keep.example.com', `${p.id} 不能改对外地址`)
      for (const t of p.toggles) assert.equal(after[t], true, `${p.id} 应勾选 ${t}`)
    }
  })
  it('不修改入参，也不改动其他项（已有的 TTL、对外地址原样保留）', () => {
    const before = policy({ enabled: true, access_token_ttl_minutes: 240, public_url: 'https://keep.example.com' })
    const snapshot = JSON.stringify(before)
    const after = applyClientPreset(before, 'chatgpt')
    assert.equal(JSON.stringify(before), snapshot)
    assert.notEqual(after, before)
    assert.equal(after.allow_dcr, true)
    assert.equal(after.enabled, true)
    assert.equal(after.access_token_ttl_minutes, 240)
    assert.equal(after.public_url, 'https://keep.example.com')
  })
  it('已经勾上的项不会被取消（再套用一次结果不变）', () => {
    const on = policy({ allow_dcr: true })
    assert.deepEqual(applyClientPreset(on, 'claude'), on)
  })
  it('三个客户端目前都只需要「自动注册」（#238 删除信任来源开关之后）', () => {
    assert.deepEqual(presetById('chatgpt').toggles, ['allow_dcr'])
    assert.deepEqual(presetById('claude').toggles, ['allow_dcr'])
    assert.deepEqual(presetById('claude-code').toggles, ['allow_dcr'])
    assert.deepEqual(CLIENT_PRESETS.map(p => p.id), ['chatgpt', 'claude', 'claude-code'])
  })
  it('presetMissing 列出还没勾上的项，套用之后为空', () => {
    assert.deepEqual(presetMissing(policy(), 'chatgpt'), ['allow_dcr'])
    assert.deepEqual(presetMissing(policy({ allow_dcr: true }), 'chatgpt'), [])
    assert.deepEqual(presetMissing(applyClientPreset(policy(), 'claude'), 'claude'), [])
    assert.deepEqual(presetMissing(null, 'claude'), [])
  })
  it('步骤文字按纯文本显示，不能带 markdown 的星号；每个预设项都有标签和说明', () => {
    for (const p of CLIENT_PRESETS) {
      for (const s of p.steps('https://hub.example.com/mcp')) assert.equal(s.text.includes('**'), false, `${p.id}: ${s.text}`)
      for (const t of p.toggles) {
        assert.ok(PRESET_TOGGLE_LABELS[t].length > 0)
        assert.ok(PRESET_TOGGLE_NOTES[t].length > 0)
      }
    }
  })
  it('预设项说明如实：自动注册是真正的闸门，必须开', () => {
    assert.match(PRESET_TOGGLE_NOTES.allow_dcr, /必须开/)
    assert.deepEqual(Object.keys(PRESET_TOGGLE_NOTES), ['allow_dcr'], '信任来源开关已删除，不应再有别的预设项')
  })
  it('三个客户端的步骤里都有「先开接入窗口」，并且排在「去客户端添加」之前（#239：自动注册默认是限时窗口）', () => {
    const url = 'https://hub.example.com/mcp'
    for (const p of CLIENT_PRESETS) {
      const steps = p.steps(url)
      const win = steps.findIndex(s => s.text.includes('接入窗口'))
      const add = steps.findIndex(s => s.code !== undefined)
      assert.ok(win >= 0, `${p.id} 的步骤里要提到接入窗口`)
      assert.ok(win < add, `${p.id}：开窗口要排在添加连接器之前`)
    }
  })
  it('验证那一步指向新的两块：批准前看「刚登记」、批准后看「已授权」', () => {
    for (const p of CLIENT_PRESETS) {
      const last = p.steps('https://hub.example.com/mcp').at(-1)!
      assert.match(last.text, /刚登记、还没授权/)
      assert.match(last.text, /已授权的应用/)
    }
  })
  it('步骤里把连接器地址原样给出，Claude Code 的命令完整', () => {
    const url = 'https://hub.example.com/mcp'
    for (const p of CLIENT_PRESETS) {
      const steps = p.steps(url)
      assert.ok(steps.length >= 4, `${p.id} 至少 4 步`)
      assert.ok(steps.every(s => s.text.trim().length > 0))
      assert.ok(steps.some(s => s.code?.includes(url)), `${p.id} 的步骤里要出现连接器地址`)
    }
    const cmd = presetById('claude-code').steps(url).find(s => s.code?.startsWith('claude mcp add'))
    assert.equal(cmd?.code, 'claude mcp add --transport http relaymesh https://hub.example.com/mcp')
  })
})

interface Recorded {
  url: string
  init?: MiniInit
}

function fakeResponse(status: number, body: string, headers: Record<string, string> = {}): MiniResponse {
  const lower: Record<string, string> = {}
  for (const [k, v] of Object.entries(headers)) lower[k.toLowerCase()] = v
  return { status, headers: { get: (n: string) => lower[n.toLowerCase()] ?? null }, text: async () => body }
}

/** 按路径给每个端点配一个应答（或抛错），并记录收到的请求。 */
function fakeFetch(routes: Record<string, MiniResponse | Error>, log: Recorded[] = []): FetchFn {
  return async (url, init) => {
    log.push({ url, init })
    const path = url.replace(/^https?:\/\/[^/]+/, '')
    const r = routes[path]
    if (!r) return fakeResponse(404, '{"error":"not_found"}')
    if (r instanceof Error) throw r
    return r
  }
}

const ORIGIN = 'https://hub.example.com'
const goodRoutes = (e: OAuthEffective): Record<string, MiniResponse> => ({
  '/.well-known/oauth-protected-resource/mcp': fakeResponse(200, JSON.stringify({ resource: e.connector_url, authorization_servers: [e.public_base] })),
  '/.well-known/oauth-authorization-server': fakeResponse(
    200,
    JSON.stringify({ issuer: e.public_base, authorization_endpoint: `${e.public_base}/oauth/authorize`, token_endpoint: `${e.public_base}/oauth/token` }),
  ),
  '/mcp': fakeResponse(401, '{"error":"unauthorized"}', { 'WWW-Authenticate': `Bearer resource_metadata="${e.resource_metadata_url}"` }),
})

const byId = <T extends { id: string }>(items: T[], id: string): T => items.find(i => i.id === id)!

describe('runOAuthSelfCheck：浏览器里的一键自检', () => {
  it('配置正常：三项通过 + 一条说明；请求不带 Cookie、不带令牌', async () => {
    const e = effective()
    const log: Recorded[] = []
    const items = await runOAuthSelfCheck(fakeFetch(goodRoutes(e), log), ORIGIN, e)
    assert.deepEqual(items.map(i => i.status), ['ok', 'ok', 'ok', 'info'])
    assert.equal(selfCheckSummary(items).tone, 'ok')
    // 模拟外部客户端：不能带上本页的登录 Cookie，更不能带任何 Authorization。
    assert.equal(log.length, 3)
    for (const r of log) {
      assert.equal(r.init?.credentials, 'omit')
      assert.equal(Object.keys(r.init?.headers ?? {}).some(k => k.toLowerCase() === 'authorization'), false)
    }
    // 注意：受保护资源元数据的路径 /.well-known/oauth-protected-resource/mcp 也以 /mcp 结尾，要按完整路径匹配。
    const post = log.find(r => new URL(r.url).pathname === '/mcp')!
    assert.equal(post.init?.method, 'POST')
    assert.deepEqual(JSON.parse(post.init?.body ?? '{}'), { jsonrpc: '2.0', id: 1, method: 'ping' })
  })

  it('总开关没开：两个元数据 404 给出「总开关还没开」，/mcp 无挑战头给出可行动的提示', async () => {
    const e = effective({ enabled: false })
    const routes = goodRoutes(e)
    delete (routes as Record<string, MiniResponse>)['/.well-known/oauth-protected-resource/mcp']
    delete (routes as Record<string, MiniResponse>)['/.well-known/oauth-authorization-server']
    routes['/mcp'] = fakeResponse(401, '{}') // 没有 WWW-Authenticate
    const items = await runOAuthSelfCheck(fakeFetch(routes), ORIGIN, e)
    assert.equal(byId(items, 'resource').status, 'fail')
    assert.match(byId(items, 'resource').detail, /总开关还没开/)
    assert.match(byId(items, 'authserver').detail, /总开关还没开/)
    assert.match(byId(items, 'mcp401').hint ?? '', /总开关/)
    assert.equal(selfCheckSummary(items).tone, 'danger')
  })

  it('被环境变量关闭：404 的原因指向 RELAYMESH_OAUTH=off', async () => {
    const e = effective({ env_off: true, enabled: false })
    const items = await runOAuthSelfCheck(fakeFetch({}), ORIGIN, e)
    assert.match(byId(items, 'resource').detail, /RELAYMESH_OAUTH=off/)
    assert.match(byId(items, 'authserver').hint ?? '', /重启/)
  })

  it('总开关开着却 404：指向反向代理没转发 /.well-known/', async () => {
    const e = effective({ enabled: true })
    const items = await runOAuthSelfCheck(fakeFetch({}), ORIGIN, e)
    assert.match(byId(items, 'resource').hint ?? '', /反向代理/)
  })

  it('/mcp 不带令牌也返回 200：指出「没有 MCP 令牌，OAuth 无从谈起」', async () => {
    const e = effective()
    const routes = goodRoutes(e)
    routes['/mcp'] = fakeResponse(200, '{"jsonrpc":"2.0","id":1,"result":{}}')
    const items = await runOAuthSelfCheck(fakeFetch(routes), ORIGIN, e)
    const mcp = byId(items, 'mcp401')
    assert.equal(mcp.status, 'fail')
    assert.match(mcp.detail, /不带令牌也能访问/)
    assert.match(mcp.hint ?? '', /MCP 令牌/)
  })

  it('401 但没有 WWW-Authenticate，且总开关开着：怀疑反向代理去掉了响应头', async () => {
    const e = effective({ enabled: true })
    const routes = goodRoutes(e)
    routes['/mcp'] = fakeResponse(401, '{}')
    const mcp = byId(await runOAuthSelfCheck(fakeFetch(routes), ORIGIN, e), 'mcp401')
    assert.equal(mcp.status, 'fail')
    assert.match(mcp.hint ?? '', /响应头/)
  })

  it('元数据端点被前端页面接管（返回 200 的 HTML）：指出不是 JSON', async () => {
    const e = effective()
    const routes = goodRoutes(e)
    routes['/.well-known/oauth-authorization-server'] = fakeResponse(200, '<!doctype html><html></html>')
    const item = byId(await runOAuthSelfCheck(fakeFetch(routes), ORIGIN, e), 'authserver')
    assert.equal(item.status, 'fail')
    assert.match(item.detail, /不是预期的 JSON/)
  })

  it('字段缺失：列出缺的字段', async () => {
    const e = effective()
    const routes = goodRoutes(e)
    routes['/.well-known/oauth-authorization-server'] = fakeResponse(200, JSON.stringify({ issuer: e.public_base }))
    const item = byId(await runOAuthSelfCheck(fakeFetch(routes), ORIGIN, e), 'authserver')
    assert.equal(item.status, 'fail')
    assert.match(item.detail, /authorization_endpoint/)
    assert.match(item.detail, /token_endpoint/)
  })

  it('issuer / resource 与上面显示的地址不一致：给警告而不是失败', async () => {
    const e = effective()
    const routes = goodRoutes(e)
    routes['/.well-known/oauth-protected-resource/mcp'] = fakeResponse(200, JSON.stringify({ resource: 'https://other.example.com/mcp', authorization_servers: [] }))
    routes['/.well-known/oauth-authorization-server'] = fakeResponse(
      200,
      JSON.stringify({ issuer: 'https://other.example.com', authorization_endpoint: 'x', token_endpoint: 'y' }),
    )
    const items = await runOAuthSelfCheck(fakeFetch(routes), ORIGIN, e)
    assert.equal(byId(items, 'resource').status, 'warn')
    assert.equal(byId(items, 'authserver').status, 'warn')
    assert.equal(selfCheckSummary(items).tone, 'warn')
  })

  it('网络错误：每项都说明请求没有成功', async () => {
    const e = effective()
    const boom = new Error('Failed to fetch')
    const items = await runOAuthSelfCheck(fakeFetch({ '/.well-known/oauth-protected-resource/mcp': boom, '/.well-known/oauth-authorization-server': boom, '/mcp': boom }), ORIGIN, e)
    for (const id of ['resource', 'authserver', 'mcp401']) {
      assert.equal(byId(items, id).status, 'fail')
      assert.match(byId(items, id).detail, /请求没有成功：Failed to fetch/)
    }
  })

  it('说明条：从别的地址打开页面时，如实说只证明了这条路径，并点名公网地址', async () => {
    const e = effective()
    const items = await runOAuthSelfCheck(fakeFetch(goodRoutes(e)), 'http://192.168.1.5:18775', e)
    const scope = byId(items, 'scope')
    assert.equal(scope.status, 'info')
    assert.match(scope.detail, /只能证明这条访问路径是通的/)
    assert.match(scope.detail, /https:\/\/hub\.example\.com/)
  })

  it('origin 末尾的斜杠不会拼出双斜杠', async () => {
    const e = effective()
    const log: Recorded[] = []
    await runOAuthSelfCheck(fakeFetch(goodRoutes(e), log), `${ORIGIN}/`, e)
    assert.ok(log.every(r => !r.url.replace('https://', '').includes('//')))
  })
})

describe('已授权的应用：人话描述', () => {
  const grant = (over: Partial<OAuthGrantRow> = {}): OAuthGrantRow => ({
    id: 1,
    client_name: 'ChatGPT',
    terminal_hosts: ['host-a', 'host-b'],
    terminal_mode_cap: 'approve',
    revoked: false,
    last_used_at: null,
    last_used_ip: '',
    created_at: '2026-10-22T04:30:00Z',
    ...over,
  })

  it('权限：主机列表 / 全部主机 / 没有主机，并带模式上限', () => {
    assert.equal(grantPermissionText(grant()), '可在 host-a、host-b 上执行命令 · 执行模式上限：需人工批准')
    assert.equal(grantPermissionText(grant({ terminal_hosts: ['*'], terminal_mode_cap: 'auto' })), '可在全部主机上执行命令 · 执行模式上限：自动执行')
    assert.equal(grantPermissionText(grant({ terminal_hosts: [], terminal_mode_cap: '' })), '没有授予任何主机的执行权限 · 执行模式上限：跟随主机')
    assert.equal(grantPermissionText(grant({ terminal_hosts: undefined, terminal_mode_cap: 'readonly' })), '没有授予任何主机的执行权限 · 执行模式上限：只读')
  })

  it('使用情况：尚未使用 / 带 IP / 不带 IP', () => {
    const now = Date.parse('2026-10-22T08:30:00Z')
    assert.equal(grantUsageText(grant(), now), '尚未使用')
    assert.equal(grantUsageText(grant({ last_used_at: '2026-10-22T05:30:00Z', last_used_ip: '203.0.113.9' }), now), '最近使用 3 小时前 · 来自 203.0.113.9')
    assert.equal(grantUsageText(grant({ last_used_at: '2026-10-22T08:29:30Z' }), now), '最近使用 30 秒前')
  })

  it('时间：本地时区的「年-月-日 时:分」，无法解析时给「—」', () => {
    const iso = '2026-10-22T04:30:00Z'
    const d = new Date(iso)
    const p = (n: number) => String(n).padStart(2, '0')
    const want = `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
    assert.equal(formatLocalMinute(iso), want)
    assert.equal(grantCreatedText(grant()), `授权于 ${want}`)
    assert.equal(formatLocalMinute('不是时间'), '—')
    assert.equal(formatLocalMinute(undefined), '—')
  })
})

describe('展示辅助：状态色', () => {
  it('四种色调各给出不同的类，警告用琥珀色、错误用 destructive', () => {
    const all = (['ok', 'warn', 'danger', 'muted'] as const).map(toneClasses)
    assert.equal(new Set(all).size, 4)
    assert.match(toneClasses('ok'), /emerald/)
    assert.match(toneClasses('warn'), /amber/)
    assert.match(toneClasses('danger'), /destructive/)
  })
  it('自检结果的状态映射到色调：fail → danger，info → muted', () => {
    assert.equal(checkStatusTone('ok'), 'ok')
    assert.equal(checkStatusTone('warn'), 'warn')
    assert.equal(checkStatusTone('fail'), 'danger')
    assert.equal(checkStatusTone('info'), 'muted')
  })
})

describe('向导文字与 docs/oauth.md「快速开始」一致', () => {
  function readDoc(): string {
    for (const base of [process.cwd(), join(process.cwd(), 'frontend')]) {
      const p = join(base, '..', 'docs', 'oauth.md')
      if (existsSync(join(base, 'package.json')) && existsSync(p)) return readFileSync(p, 'utf8')
    }
    throw new Error('找不到 docs/oauth.md，请在仓库根目录或 frontend 目录下运行测试')
  }

  it('文档里有「快速开始」，并包含 Claude Code 的命令、三个客户端与连接器地址的写法', () => {
    const doc = readDoc()
    assert.match(doc, /^## 快速开始/m)
    assert.ok(doc.includes('claude mcp add --transport http'), '文档应给出与页面相同的 Claude Code 命令')
    for (const name of ['ChatGPT', 'Claude（网页）', 'Claude Code']) assert.ok(doc.includes(name), `文档里应有「${name}」`)
    assert.ok(doc.includes('/mcp'), '文档应说明连接器地址是 <对外地址>/mcp')
    assert.ok(doc.includes('一键自检'), '文档应提到页面上的一键自检')
  })

  it('文档不再声称有页面上不存在的「未设置对外地址」提示', () => {
    assert.equal(readDoc().includes('Web 会提示「未设置对外地址」'), false)
  })
})
