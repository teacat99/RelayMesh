// 已经下线的设置字段：清理逻辑。
//
// 「我的状态」（在线 / 暂离 / 托管）整体下线之后，浏览器缓存与服务端设置里仍可能留着它们的字段：
//   userPresence、statusStrategies、statusPresets.{away,autopilot}、flowPrompts.{away,autopilot}，
//   以及快捷预设版本留存里 status:away / status:autopilot 范围的基线、历史与留存条目。
// 载入与同步时把它们丢掉；发现服务端还带着，就在同步之后顺手存一次，让它们从服务端也消失。
// 纯函数，便于单元测试；调用点在 stores/settings.ts。
import { PRESET_SCOPES, normalizeVersions } from './presetVersions'

type Bag = Record<string, any>

const isObject = (v: unknown): v is Bag => !!v && typeof v === 'object' && !Array.isArray(v)

const scopeKnown = (scope: unknown) => typeof scope === 'string' && (PRESET_SCOPES as readonly string[]).includes(scope)
const keyKnown = (key: string) => scopeKnown(key.split('|')[0])

/** 设置里是否还留着下线功能的字段。 */
export function hasRetiredSettings(s: unknown): boolean {
  if (!isObject(s)) return false
  if ('userPresence' in s || 'statusStrategies' in s) return true
  // 预设与提示词原来按状态分组，现在只剩 online 一组
  for (const group of [s.statusPresets, s.flowPrompts]) {
    if (isObject(group) && Object.keys(group).some(k => k !== 'online')) return true
  }
  const v = s.presetVersions
  if (isObject(v)) {
    if (isObject(v.baseline) && Object.keys(v.baseline).some(k => !keyKnown(k))) return true
    if (isObject(v.history) && Object.keys(v.history).some(k => !keyKnown(k))) return true
    if (Array.isArray(v.archive) && v.archive.some(e => !isObject(e) || !scopeKnown(e.scope))) return true
  }
  return false
}

/** 丢掉下线功能的字段，其余原样返回（返回新对象，不改入参）。 */
export function dropRetiredSettings<T extends object>(s: T): T {
  const out = { ...s } as Bag
  delete out.userPresence
  delete out.statusStrategies
  if (isObject(out.statusPresets)) out.statusPresets = { online: out.statusPresets.online }
  if (isObject(out.flowPrompts)) out.flowPrompts = { online: out.flowPrompts.online }
  if (out.presetVersions) out.presetVersions = normalizeVersions(out.presetVersions)
  return out as T
}
