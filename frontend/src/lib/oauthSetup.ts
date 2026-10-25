/**
 * OAuth 设置页（OAuthSettingsPanel.vue）的纯逻辑：状态卡文案、浏览器内自检、「按此客户端设置」预设、授权列表的人话描述。
 *
 * 与组件分开是为了能单元测试。服务端口径见 internal/oauth：
 *   - effective.go：EffectiveStatus（此刻到底有没有在服务、对外地址与来源、连接器地址）；
 *   - policy.go：批准页上要不要对客户端标黄警告（WarnUntrustedClient）——规则固定（官方 ChatGPT / Claude 的域名不提示，
 *     陌生域名与本机回环提示），没有开关，也从不拒绝授权（#238 起删除了「信任来源」四项）；
 *     真正的闸门只有总开关与自动注册（DCR）：关闭、限时窗口（默认）、常开三种方式，窗口之外 /oauth/register 返回 403
 *     （#239 起；窗口与登记清单的逻辑在 oauthWindow.ts）。
 */
import type { OAuthEffective, OAuthGrantRow, OAuthPolicy, TerminalModeCap } from '../api/client'
import { modeCapLabel } from './credentialPolicy'
import { relativeTime } from './terminal/format'

export type Tone = 'ok' | 'warn' | 'danger' | 'muted'

// ───────────────────────── 状态卡 ─────────────────────────

export interface SwitchState {
  kind: 'env_off' | 'on' | 'off'
  tone: Tone
  title: string
  detail: string
}

/** 总开关此刻真的生效了吗（以服务端 effective 为准，不看表单里未保存的改动）。 */
export function switchState(effective: OAuthEffective | null | undefined): SwitchState | null {
  if (!effective) return null
  if (effective.env_off) {
    return {
      kind: 'env_off',
      tone: 'danger',
      title: '已被环境变量关闭',
      detail:
        '这台 Hub 是带着环境变量 RELAYMESH_OAUTH=off 启动的，OAuth 整个不可用——在本页改开关也不会生效。去掉这个环境变量并重启 Hub 后，才能在这里设置。',
    }
  }
  if (effective.enabled) {
    return { kind: 'on', tone: 'ok', title: '已开启，正在对外服务', detail: 'ChatGPT / Claude 现在可以向这台 Hub 发起授权。' }
  }
  return {
    kind: 'off',
    tone: 'muted',
    title: '未开启（默认）',
    detail: 'OAuth 授权服务器目前关着，连接器连不上。在下面勾选「启用 OAuth 授权服务器」并保存后生效。',
  }
}

export interface BaseNote {
  tone: Tone
  label: string
  detail: string
}

/** 对外地址的来源说明；推导出来的、被环境变量盖掉的都标成警告。 */
export function publicBaseNote(effective: OAuthEffective | null | undefined): BaseNote | null {
  if (!effective) return null
  switch (effective.public_base_source) {
    case 'env':
      if (effective.settings_public_url_shadowed) {
        return {
          tone: 'warn',
          label: '来自环境变量 RELAYMESH_PUBLIC_URL（本页填的地址被盖掉了）',
          detail:
            '你在下面「对外地址」里填的值没有生效：环境变量的优先级更高。要么清空本页的值，要么去掉环境变量并重启 Hub。',
        }
      }
      return {
        tone: 'ok',
        label: '来自环境变量 RELAYMESH_PUBLIC_URL',
        detail: '优先级最高；要修改它，需要改环境变量并重启 Hub（本页的「对外地址」不起作用）。',
      }
    case 'settings':
      return { tone: 'ok', label: '来自本页的「对外地址」设置', detail: '' }
    default:
      return {
        tone: 'warn',
        label: '没有设置，按你现在访问页面的地址推导',
        detail:
          '推导出的地址只对你现在用的这条访问路径有效（局域网 IP、localhost 等），ChatGPT / Claude 在公网上访问不到它，而且请求头是可以被客户端影响的。' +
          '正式使用前请在下面填写「对外地址」，或设置环境变量 RELAYMESH_PUBLIC_URL。',
      }
  }
}

/** 表单里是否有还没保存的改动（决定「保存」按钮旁的提示）。 */
export function policyDirty(saved: OAuthPolicy | null | undefined, draft: OAuthPolicy | null | undefined): boolean {
  if (!saved || !draft) return false
  const keys: (keyof OAuthPolicy)[] = [
    'enabled',
    'public_url',
    'allow_dcr',
    'access_token_ttl_minutes',
    'refresh_idle_days',
    'refresh_absolute_days',
  ]
  for (const k of keys) {
    const a = saved[k]
    const b = draft[k]
    if (typeof a === 'string' || typeof b === 'string') {
      if (String(a ?? '').trim() !== String(b ?? '').trim()) return true
    } else if (a !== b) return true
  }
  // dcr_always 是后来加的可选字段：旧后端没有它（undefined），与 false 是同一种状态，不能算成「改过」。
  if (Boolean(saved.dcr_always) !== Boolean(draft.dcr_always)) return true
  return false
}

// ───────────────────────── 接入向导：「按此客户端设置」 ─────────────────────────

export type ClientPresetId = 'chatgpt' | 'claude' | 'claude-code'
/** 向导能替你勾选的策略项。目前只有「自动注册」：其余的「信任来源」开关在 #238 里删除了（它们从不拒绝授权）。 */
export type PresetToggle = 'allow_dcr'

export const PRESET_TOGGLE_LABELS: Record<PresetToggle, string> = {
  allow_dcr: '自动注册',
}

/** 每个预设项到底起什么作用（按服务端实际行为写，见文件头）：「自动注册」是真正的闸门。 */
export const PRESET_TOGGLE_NOTES: Record<PresetToggle, string> = {
  allow_dcr: '必须开（选默认的「限时窗口」即可）：客户端靠它向 RelayMesh 自动注册，关闭就连不上',
}

export interface ClientStep {
  text: string
  /** 需要原样输入 / 粘贴的内容（地址或命令），界面里单独成块、可复制 */
  code?: string
}

export interface ClientPreset {
  id: ClientPresetId
  label: string
  blurb: string
  /** 「按此客户端设置」会勾上的策略项：只勾选、从不取消，也从不碰总开关与对外地址 */
  toggles: PresetToggle[]
  steps(connectorUrl: string): ClientStep[]
  note: string
}

const STATUS_STEP: ClientStep = {
  text: '先看上面的状态卡：开关是「已开启，正在对外服务」，并且对外地址是你的公网 https 地址（没有黄色警告）。',
}

/** 自动注册默认是限时窗口：添加连接器之前要先开窗口，否则客户端登记会被拒绝（403）。 */
const WINDOW_STEP: ClientStep = {
  text: '开启本页上面的「接入窗口」（默认 10 分钟）：自动注册平时是关着的，窗口没开着时，在客户端里添加连接器会被拒绝。如果你把「自动注册」设成了「常开」，这一步可以跳过。',
}

const APPROVE_STEP: ClientStep = {
  text: '它会把浏览器带到 RelayMesh 的授权页：登录后选择它能在哪些主机上执行命令、最高的执行模式，点「批准」（授予终端能力前要再输入一次密码）。',
}

const VERIFY_STEP: ClientStep = {
  text: '回到本页：批准之前它在「刚登记、还没授权」的清单里（不认识的可以删除登记），批准之后出现在「已授权的应用」里；不想让它继续使用，点「撤销」就立刻失效。',
}

export const CLIENT_PRESETS: ClientPreset[] = [
  {
    id: 'chatgpt',
    label: 'ChatGPT',
    blurb: '在 ChatGPT 里添加自定义连接器（MCP 服务器），用 OAuth 登录。',
    toggles: ['allow_dcr'],
    steps: url => [
      STATUS_STEP,
      WINDOW_STEP,
      { text: '在 ChatGPT 的设置里找到「连接器 / Connectors」，添加一个自定义连接器（MCP 服务器），把这个地址粘贴进去：', code: url },
      {
        text: '认证方式选 OAuth。不用填 Client ID 和 Secret：ChatGPT 会向 RelayMesh 自动注册（所以自动注册不能关，添加的这几分钟里接入窗口要开着）。',
      },
      APPROVE_STEP,
      VERIFY_STEP,
    ],
    note: 'ChatGPT 的菜单名称与入口会随版本变化，找不到时在设置里搜「连接器」或「MCP」；能不能添加自定义连接器取决于你的套餐与设置，以 ChatGPT 的界面提示为准。',
  },
  {
    id: 'claude',
    label: 'Claude（网页）',
    blurb: '在 claude.ai 里添加自定义连接器，点一下连接就跳到授权页。',
    toggles: ['allow_dcr'],
    steps: url => [
      STATUS_STEP,
      WINDOW_STEP,
      { text: '在 claude.ai 的设置里找到「连接器 / Connectors」，选择添加自定义连接器，名称随意，把这个地址粘贴进去：', code: url },
      { text: '点「连接」。Claude 同样会自动注册，不用填密钥；随后浏览器会跳到 RelayMesh 的授权页。' },
      APPROVE_STEP,
      VERIFY_STEP,
    ],
    note: 'Claude 的菜单名称与入口会随版本变化，找不到时在设置里搜「连接器」；能不能添加自定义连接器取决于你的套餐与设置，以 Claude 的界面提示为准。',
  },
  {
    id: 'claude-code',
    label: 'Claude Code',
    blurb: '在你自己电脑的终端里添加，授权时浏览器要和 Claude Code 在同一台电脑上。',
    toggles: ['allow_dcr'],
    steps: url => [
      STATUS_STEP,
      WINDOW_STEP,
      { text: '在运行 Claude Code 的那台电脑的终端里执行（把 relaymesh 换成你喜欢的名字）：', code: `claude mcp add --transport http relaymesh ${url}` },
      { text: '进入 Claude Code，输入 /mcp，选中 relaymesh，按提示选择认证（Authenticate），浏览器会打开 RelayMesh 的授权页。' },
      {
        text: '授权要在同一台电脑的浏览器里完成：Claude Code 会在本机开一个回环端口（127.0.0.1）接收授权结果，换一台电脑的浏览器收不到。',
      },
      APPROVE_STEP,
      VERIFY_STEP,
    ],
    note: '批准页会一直提示「重定向地址为本机回环地址」（同时还有一条「不在默认信任列表中」），这是正常的：它说明授权结果只会交给你本机上的程序。',
  },
]

export function presetById(id: ClientPresetId): ClientPreset {
  const p = CLIENT_PRESETS.find(x => x.id === id)
  if (!p) throw new Error(`未知的客户端预设: ${id}`)
  return p
}

/** 套用预设：只勾选该客户端需要的项，返回新对象；绝不改总开关与对外地址，也不取消任何已勾的项。 */
export function applyClientPreset(policy: OAuthPolicy, id: ClientPresetId): OAuthPolicy {
  const next: OAuthPolicy = { ...policy }
  for (const t of presetById(id).toggles) next[t] = true
  return next
}

/** 该客户端需要、但表单里现在还没勾上的项（用于「还差：…」提示）。 */
export function presetMissing(policy: OAuthPolicy | null | undefined, id: ClientPresetId): PresetToggle[] {
  if (!policy) return []
  return presetById(id).toggles.filter(t => !policy[t])
}

// ───────────────────────── 一键自检（在浏览器里发请求） ─────────────────────────

export type CheckStatus = 'ok' | 'warn' | 'fail' | 'info'

export interface SelfCheckItem {
  id: 'resource' | 'authserver' | 'mcp401' | 'scope'
  label: string
  status: CheckStatus
  detail: string
  hint?: string
}

export interface MiniResponse {
  status: number
  headers: { get(name: string): string | null }
  text(): Promise<string>
}

export interface MiniInit {
  method?: string
  headers?: Record<string, string>
  body?: string
  credentials?: 'omit' | 'same-origin' | 'include'
}

export type FetchFn = (url: string, init?: MiniInit) => Promise<MiniResponse>

interface Probe {
  res?: MiniResponse
  json?: Record<string, unknown>
  error?: string
}

async function probe(fetchFn: FetchFn, url: string, init?: MiniInit): Promise<Probe> {
  let res: MiniResponse
  try {
    res = await fetchFn(url, init)
  } catch (e) {
    return { error: e instanceof Error ? e.message : String(e) }
  }
  let json: Record<string, unknown> | undefined
  try {
    const parsed: unknown = JSON.parse(await res.text())
    if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) json = parsed as Record<string, unknown>
  } catch {
    /* 不是 JSON（例如被反向代理 / 前端页面接管）：json 保持 undefined */
  }
  return { res, json }
}

const NETWORK_HINT = '浏览器连不上这个地址（网络、证书或反向代理的问题）。'

/** 404 时按「开关没开 / 被环境变量关闭 / 开着却找不到」给出可行动的原因。 */
function notFoundReason(effective: OAuthEffective): { detail: string; hint: string } {
  if (effective.env_off) {
    return { detail: '返回 404：OAuth 被环境变量 RELAYMESH_OAUTH=off 关闭了', hint: '去掉这个环境变量并重启 Hub。' }
  }
  if (!effective.enabled) {
    return { detail: '返回 404：总开关还没开', hint: '在下面勾选「启用 OAuth 授权服务器」并保存，再重新自检。' }
  }
  return {
    detail: '返回 404：总开关是开着的，却找不到这个端点',
    hint: '多半是反向代理没有把 /.well-known/ 与 /oauth/ 转发给 RelayMesh。',
  }
}

function metadataItem(
  id: 'resource' | 'authserver',
  label: string,
  p: Probe,
  effective: OAuthEffective,
  required: string[],
  mismatch: (json: Record<string, unknown>) => SelfCheckItem | null,
): SelfCheckItem {
  if (p.error) return { id, label, status: 'fail', detail: `请求没有成功：${p.error}`, hint: NETWORK_HINT }
  const res = p.res as MiniResponse
  if (res.status === 404) return { id, label, status: 'fail', ...notFoundReason(effective) }
  if (res.status !== 200) {
    return { id, label, status: 'fail', detail: `返回 ${res.status}（预期 200）`, hint: '检查反向代理是否拦截了 /.well-known/ 路径。' }
  }
  if (!p.json) {
    return {
      id,
      label,
      status: 'fail',
      detail: '返回 200，但内容不是预期的 JSON（像是一个网页）',
      hint: '反向代理可能把 /.well-known/ 路径转给了前端页面；需要把 /.well-known/ 与 /oauth/ 都转发给 RelayMesh。',
    }
  }
  const missing = required.filter(k => p.json![k] === undefined)
  if (missing.length) {
    return { id, label, status: 'fail', detail: `返回 200，但缺少字段：${missing.join('、')}`, hint: '元数据不完整，客户端无法据此发现授权服务器。' }
  }
  return mismatch(p.json) ?? { id, label, status: 'ok', detail: '返回 200，内容完整' }
}

/**
 * 在浏览器里对「当前打开本页的地址」做三项检查（正是 docs/oauth.md 检查清单第 5 步，用按钮代替手工）：
 * 两个元数据端点返回 200 且字段齐全；不带令牌的 POST /mcp 返回 401 并带 WWW-Authenticate。
 * 检查的是这条访问路径，不能证明公网可达——最后一条 info 会如实说明。
 */
export async function runOAuthSelfCheck(fetchFn: FetchFn, origin: string, effective: OAuthEffective): Promise<SelfCheckItem[]> {
  const base = origin.replace(/\/+$/, '')
  const items: SelfCheckItem[] = []

  const resource = await probe(fetchFn, `${base}/.well-known/oauth-protected-resource/mcp`, { credentials: 'omit' })
  items.push(
    metadataItem('resource', '资源元数据（/.well-known/oauth-protected-resource/mcp）', resource, effective, ['resource', 'authorization_servers'], json => {
      const got = String(json.resource)
      if (got !== effective.connector_url) {
        return {
          id: 'resource',
          label: '资源元数据（/.well-known/oauth-protected-resource/mcp）',
          status: 'warn',
          detail: `返回 200，但其中的 resource 是 ${got}，与上面的连接器地址 ${effective.connector_url} 不一致`,
          hint: '对外地址在两次请求之间变了，或反向代理改写了 Host / X-Forwarded-*；刷新本页后重试。',
        }
      }
      return null
    }),
  )

  const authServer = await probe(fetchFn, `${base}/.well-known/oauth-authorization-server`, { credentials: 'omit' })
  items.push(
    metadataItem('authserver', '授权服务器元数据（/.well-known/oauth-authorization-server）', authServer, effective, ['issuer', 'authorization_endpoint', 'token_endpoint'], json => {
      const got = String(json.issuer)
      if (got !== effective.public_base) {
        return {
          id: 'authserver',
          label: '授权服务器元数据（/.well-known/oauth-authorization-server）',
          status: 'warn',
          detail: `返回 200，但其中的 issuer 是 ${got}，与上面的对外地址 ${effective.public_base} 不一致`,
          hint: '对外地址在两次请求之间变了，或反向代理改写了 Host / X-Forwarded-*；刷新本页后重试。',
        }
      }
      return null
    }),
  )

  const mcp = await probe(fetchFn, `${base}/mcp`, {
    method: 'POST',
    credentials: 'omit',
    headers: { 'content-type': 'application/json', accept: 'application/json' },
    // ping 没有任何副作用：万一 /mcp 不需要令牌，它也只会得到一个空的应答。
    body: JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'ping' }),
  })
  const mcpLabel = '未带令牌访问 /mcp（应当 401 并带 WWW-Authenticate）'
  if (mcp.error) {
    items.push({ id: 'mcp401', label: mcpLabel, status: 'fail', detail: `请求没有成功：${mcp.error}`, hint: NETWORK_HINT })
  } else {
    const res = mcp.res as MiniResponse
    const challenge = res.headers.get('www-authenticate') ?? ''
    if (res.status === 401 && /resource_metadata\s*=/i.test(challenge)) {
      items.push({ id: 'mcp401', label: mcpLabel, status: 'ok', detail: '返回 401，并带 WWW-Authenticate（客户端据此找到授权服务器）' })
    } else if (res.status === 401) {
      items.push({
        id: 'mcp401',
        label: mcpLabel,
        status: 'fail',
        detail: '返回 401，但没有 WWW-Authenticate',
        hint: effective.enabled
          ? '反向代理可能去掉了这个响应头；检查代理是否把所有响应头原样转发。'
          : '总开关没开（或被环境变量关闭）时 Hub 不会带这个头——先把总开关打开。',
      })
    } else if (res.status === 200) {
      items.push({
        id: 'mcp401',
        label: mcpLabel,
        status: 'fail',
        detail: '返回 200：不带令牌也能访问 /mcp',
        hint: 'Hub 里还没有任何 MCP 令牌，/mcp 对任何人开放，OAuth 无从谈起。先在下方的「MCP 凭据管理」里创建一个令牌，再回来自检。',
      })
    } else {
      items.push({ id: 'mcp401', label: mcpLabel, status: 'fail', detail: `返回 ${res.status}（预期 401）`, hint: '检查反向代理是否拦截了 /mcp。' })
    }
  }

  const sameOrigin = base === effective.public_base
  items.push({
    id: 'scope',
    label: '这次检查能说明什么',
    status: 'info',
    detail: sameOrigin
      ? `检查是从当前页面的地址（${base}）发出的；如果你是在局域网里打开本页，这不代表公网可达。`
      : `检查是从你现在打开本页的地址（${base}）发出的，只能证明这条访问路径是通的。ChatGPT / Claude 访问的是 ${effective.public_base}：它能否从公网访问、并转发到这台 Hub，请在外网（例如用手机流量）再确认一次。`,
  })
  return items
}

export interface CheckSummary {
  tone: Tone
  text: string
}

export function selfCheckSummary(items: SelfCheckItem[]): CheckSummary {
  const fail = items.filter(i => i.status === 'fail').length
  const warn = items.filter(i => i.status === 'warn').length
  if (fail) return { tone: 'danger', text: `有 ${fail} 项没通过，按每项下面的提示处理后重新自检` }
  if (warn) return { tone: 'warn', text: '检查通过，但有需要留意的地方' }
  return { tone: 'ok', text: '三项检查全部通过' }
}

// ───────────────────────── 已授权的应用 ─────────────────────────

function pad2(n: number): string {
  return String(n).padStart(2, '0')
}

/** 本地时区的「2026-10-22 12:30」；无法解析时返回「—」。 */
export function formatLocalMinute(iso: string | null | undefined): string {
  if (!iso) return '—'
  const t = new Date(iso)
  if (Number.isNaN(t.getTime())) return '—'
  return `${t.getFullYear()}-${pad2(t.getMonth() + 1)}-${pad2(t.getDate())} ${pad2(t.getHours())}:${pad2(t.getMinutes())}`
}

/** 「可在 host1、host2 上执行命令 · 执行模式上限：自动执行」 */
export function grantPermissionText(g: Pick<OAuthGrantRow, 'terminal_hosts' | 'terminal_mode_cap'>): string {
  const hosts = (g.terminal_hosts ?? []).filter(Boolean)
  const where = hosts.includes('*') ? '可在全部主机上执行命令' : hosts.length ? `可在 ${hosts.join('、')} 上执行命令` : '没有授予任何主机的执行权限'
  return `${where} · 执行模式上限：${modeCapLabel((g.terminal_mode_cap ?? '') as TerminalModeCap)}`
}

export function grantCreatedText(g: Pick<OAuthGrantRow, 'created_at'>): string {
  return `授权于 ${formatLocalMinute(g.created_at)}`
}

/** 「最近使用 3 小时前 · 来自 203.0.113.9」或「尚未使用」。 */
export function grantUsageText(g: Pick<OAuthGrantRow, 'last_used_at' | 'last_used_ip'>, nowMs: number = Date.now()): string {
  if (!g.last_used_at) return '尚未使用'
  const when = relativeTime(g.last_used_at, nowMs)
  const ip = (g.last_used_ip ?? '').trim()
  return ip ? `最近使用 ${when} · 来自 ${ip}` : `最近使用 ${when}`
}

// ───────────────────────── 展示辅助 ─────────────────────────

/** 状态色块的 Tailwind 类（与 NtfySettings 里测试结果块同一套配色；warn 用琥珀色）。 */
export function toneClasses(tone: Tone): string {
  switch (tone) {
    case 'ok':
      return 'bg-emerald-500/10 border-emerald-500/30 text-emerald-600 dark:text-emerald-400'
    case 'warn':
      return 'bg-amber-500/10 border-amber-500/30 text-amber-600 dark:text-amber-400'
    case 'danger':
      return 'bg-destructive/10 border-destructive/30 text-destructive'
    default:
      return 'bg-muted/40 border-border/60 text-muted-foreground'
  }
}

export function checkStatusTone(s: CheckStatus): Tone {
  switch (s) {
    case 'ok':
      return 'ok'
    case 'warn':
      return 'warn'
    case 'fail':
      return 'danger'
    default:
      return 'muted'
  }
}
