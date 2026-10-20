import { DEFAULT_PHASE_TEMPLATE } from './defaultPrompts.generated'

// 工作流阶段（v1.7 起为三段：讨论 / 方案 / 开发）。
//
// 旧五段式的阶段 id 仍然被服务端接受并映射：assess → discuss，verify / done → dev。
// 服务端负责迁移存量数据；这里只处理浏览器本地缓存（localStorage）里可能残留的旧模板，
// 避免设置页在同步到服务端之前先闪出旧的五个阶段。

export interface PhaseLike {
  id: string
  label: string
  description?: string
  prompt?: string
}

export const LEGACY_PHASE_ALIAS: Readonly<Record<string, string>> = {
  assess: 'discuss',
  verify: 'dev',
  done: 'dev',
}

export function isLegacyPhaseId(id: string): boolean {
  return Object.prototype.hasOwnProperty.call(LEGACY_PHASE_ALIAS, id.trim())
}

export function canonicalPhaseId(id: string): string {
  const trimmed = id.trim()
  return Object.prototype.hasOwnProperty.call(LEGACY_PHASE_ALIAS, trimmed) ? LEGACY_PHASE_ALIAS[trimmed] : trimmed
}

/**
 * 把本地缓存里的阶段模板收敛为三段式：
 * - 空 / 缺失 → 出厂三段；
 * - 不含旧 id → 原样返回（用户改过的文案、自建的阶段都不动）；
 * - 含旧 id（五段式缓存）→ 换成出厂三段，用户自建的阶段接在后面。
 *   用户在旧五段上改过的文案以服务端迁移后的版本为准（同步时会覆盖本地）。
 */
export function normalizePhaseTemplate<T extends PhaseLike>(items: T[] | null | undefined): Array<T | PhaseLike> {
  const defaults: PhaseLike[] = DEFAULT_PHASE_TEMPLATE.map(p => ({ ...p }))
  if (!items || items.length === 0) return defaults
  if (!items.some(it => isLegacyPhaseId(it.id))) return items
  const builtin = new Set<string>([...Object.keys(LEGACY_PHASE_ALIAS), ...DEFAULT_PHASE_TEMPLATE.map(p => p.id)])
  return [...defaults, ...items.filter(it => !builtin.has(it.id))]
}
