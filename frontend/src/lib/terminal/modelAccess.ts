// 模型 × 主机授权矩阵的纯逻辑（便于单元测试）。
//
// 权限模型只有一条：判定一个令牌能否使用某台主机，看的永远是令牌自己的主机名单（terminal_hosts）。
// 主机上的「新模型默认」只是预填规则——新增主机时写入现有令牌、新建令牌时预选——写成名单里的明确条目，
// 运行期间没有继承。所以这里所有的编辑都归结为「算出令牌新的 terminal_hosts」，由调用方整体保存。

import type { HostView } from '../../api/terminal'
import { hostTitle, shortCode } from './format'
import { supportsModel } from './hosts'

/** 单个令牌最多授权的主机数（与服务端 CredentialMaxTerminalHosts 一致）。 */
export const MAX_HOSTS_PER_TOKEN = 100

/** 通配符：令牌可使用全部主机（含以后新增的）。 */
export const ALL_HOSTS = '*'

export interface AccessHost {
  code: string
  title: string
  short: string
  online: boolean
  ssh: boolean
  /** G2：管理员允许模型使用这台主机。关闭时，即使令牌授权了它，模型也用不了。 */
  allowModel: boolean
  /** 新建令牌 / 新增主机时是否预先授予。 */
  defaultAllow: boolean
}

export interface AccessToken {
  id: number
  name: string
  active: boolean
  capability: boolean
  /** 令牌写的是「全部主机」。 */
  wildcard: boolean
  /** 带文件权限（无论是否同时带终端权限）。 */
  files: boolean
  /** 令牌自己的明确主机名单（不含通配符），包括当前不在矩阵里的（停用 / 已删除）。 */
  hosts: string[]
  expired: boolean
}

type CredLike = {
  id: number
  name: string
  is_active: boolean
  permissions: { terminal?: boolean; terminal_files?: boolean }
  terminal_hosts?: string[] | null
  capability?: boolean
  expires_at?: string | null
}

type HostLike = Pick<HostView, 'machine_code' | 'remark' | 'hostname' | 'status' | 'temporary' | 'online' | 'kind' | 'allow_model' | 'default_for_new_models'> &
  Partial<Pick<HostView, 'ssh'>>

/**
 * 矩阵里的列：已启用的持久主机（临时主机只活在内存里，不能写进令牌名单）。
 * SSH 主机（Hub 直连与经网关）都可以给模型用；只有登记时探测不出 POSIX 环境、只能当人工终端用的那种
 * （term_only）授权给令牌没有意义，不出现在矩阵里。
 */
export function accessHosts(hosts: readonly HostLike[]): AccessHost[] {
  return hosts
    .filter(h => h.status === 'enabled' && !h.temporary && supportsModel(h))
    .map(h => ({
      code: h.machine_code,
      title: hostTitle(h),
      short: shortCode(h.machine_code),
      online: !!h.online,
      ssh: h.kind === 'ssh',
      allowModel: !!h.allow_model,
      defaultAllow: h.default_for_new_models === 'allow',
    }))
    .sort((a, b) => a.title.localeCompare(b.title, 'zh-Hans-CN') || a.code.localeCompare(b.code))
}

export function hasTerminalCapability(c: Pick<CredLike, 'permissions'>): boolean {
  return !!(c.permissions?.terminal || c.permissions?.terminal_files)
}

/** 矩阵里的行：带终端 / 文件能力的数据库令牌；其余令牌只计数。 */
export function accessTokens(creds: readonly CredLike[], now: number = Date.now()): { rows: AccessToken[]; skipped: number } {
  const rows: AccessToken[] = []
  let skipped = 0
  for (const c of creds) {
    if (!hasTerminalCapability(c)) {
      skipped++
      continue
    }
    const list = c.terminal_hosts ?? []
    rows.push({
      id: c.id,
      name: c.name,
      active: c.is_active,
      capability: !!c.capability,
      wildcard: list.includes(ALL_HOSTS),
      files: !!c.permissions.terminal_files,
      hosts: list.filter(h => h !== ALL_HOSTS),
      expired: !!c.expires_at && Date.parse(c.expires_at) <= now,
    })
  }
  return { rows, skipped }
}

/** 这个单元格对应的授权是否成立（通配符对所有主机成立）。 */
export function cellGranted(t: AccessToken, code: string): boolean {
  return t.wildcard || t.hosts.includes(code)
}

/** 名单里有、但当前不在矩阵里的主机数（已停用 / 已删除 / 临时），提示给管理员，避免它们成为看不见的授权。 */
export function hiddenGrants(t: AccessToken, visible: readonly string[]): number {
  if (t.wildcard) return 0
  const known = new Set(visible)
  return t.hosts.filter(h => !known.has(h)).length
}

/**
 * 勾选 / 取消一个单元格后令牌新的主机名单。
 * - 通配令牌取消某一格：展开成「当前矩阵里的全部主机」去掉这一台（以后新增的主机不再自动可用，调用方应当提示）；
 * - 通配令牌再勾选：什么也不变；
 * - 其他：保留名单里矩阵之外的条目，只增删这一台。
 */
export function toggleCell(t: AccessToken, code: string, on: boolean, visible: readonly string[]): string[] {
  if (t.wildcard) return on ? [ALL_HOSTS] : visible.filter(h => h !== code)
  const set = new Set(t.hosts)
  if (on) set.add(code)
  else set.delete(code)
  return [...set]
}

/** 整行全选 / 清空后令牌新的主机名单：清空只清矩阵里看得见的列，矩阵之外的条目保留（它们对管理员不可见，不该被顺手抹掉）。 */
export function setRow(t: AccessToken, visible: readonly string[], on: boolean): string[] {
  if (t.wildcard) return on ? [ALL_HOSTS] : []
  if (on) return [...new Set([...t.hosts, ...visible])]
  const vis = new Set(visible)
  return t.hosts.filter(h => !vis.has(h))
}

export type CheckState = 'all' | 'some' | 'none'

/** 行的勾选状态（只看矩阵里的列）。 */
export function rowState(t: AccessToken, visible: readonly string[]): CheckState {
  if (visible.length === 0) return 'none'
  if (t.wildcard) return 'all'
  const n = visible.filter(c => t.hosts.includes(c)).length
  return n === 0 ? 'none' : n === visible.length ? 'all' : 'some'
}

/** 列的勾选状态：通配令牌本来就包含每一台，不计入「需要被改动」的集合，但算作已授权。 */
export function columnState(rows: readonly AccessToken[], code: string): CheckState {
  if (rows.length === 0) return 'none'
  const n = rows.filter(t => cellGranted(t, code)).length
  return n === 0 ? 'none' : n === rows.length ? 'all' : 'some'
}

/** 把一台主机授予「全部现有令牌」时会被改动的令牌（与服务端 credentialTakesDefaultGrant 同一规则：能力 URL 令牌与通配令牌不动）。 */
export function wouldGrant(rows: readonly AccessToken[], code: string): AccessToken[] {
  return rows.filter(t => !t.capability && !t.wildcard && !t.hosts.includes(code))
}

/** 把一台主机从全部令牌收回时会被改动的令牌（通配令牌不能只收回一台，不在其中）。 */
export function wouldRevoke(rows: readonly AccessToken[], code: string): AccessToken[] {
  return rows.filter(t => !t.wildcard && t.hosts.includes(code))
}

/** 名单是否超过服务端上限。 */
export function overLimit(list: readonly string[]): boolean {
  return list.filter(h => h !== ALL_HOSTS).length > MAX_HOSTS_PER_TOKEN
}

/** 「将授予 N 个模型」的预览文案：列出前几个名字，其余折叠。 */
export function grantSummary(names: readonly string[], limit = 4): string {
  if (names.length === 0) return '没有需要授予的模型令牌'
  const shown = names.slice(0, limit).join('、')
  return names.length > limit ? `将授予 ${names.length} 个模型令牌：${shown} 等` : `将授予 ${names.length} 个模型令牌：${shown}`
}

/** 新建令牌 / 给已有令牌新开终端权限时按主机默认态预选的主机；能力 URL 令牌要求逐台授权，从不预选。 */
export function defaultSelection(hosts: readonly HostLike[], capability: boolean): string[] {
  if (capability) return []
  return hosts.filter(h => h.status === 'enabled' && !h.temporary && h.default_for_new_models === 'allow').map(h => h.machine_code)
}
