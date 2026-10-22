// 设置窗口左侧「分类导航」用到的纯函数（便于单元测试；分类本身的数据在 settingsRegistry.ts）。
//
// 导航分两组：当前模块自己的分类在前，两个模块通用的分类在后；顶部是「RelayMesh | ExecMesh」分段切换。
// 窄屏没有常驻的左栏，改用「当前分类」选择条，分组与顺序与左栏完全一致。

import { PROJECTS, PROJECT_ORDER, type AppMode } from './mode'
import { jumpLabel, visibleCategories, type SettingsCategory, type SettingsCategoryId } from './settingsRegistry'

export interface SettingsNavGroup {
  key: 'own' | 'common'
  /** 分组小标题。 */
  title: string
  items: SettingsCategory[]
}

/** 当前模块的导航分组：本模块的分类 → 通用分类（没有分类的组不出现）。 */
export function navGroups(module: AppMode): SettingsNavGroup[] {
  const v = visibleCategories(module)
  const groups: SettingsNavGroup[] = [
    { key: 'own', title: `${PROJECTS[module].name} 设置`, items: v.own },
    { key: 'common', title: '通用设置', items: v.common },
  ]
  return groups.filter(g => g.items.length > 0)
}

/** 导航里从上到下的全部分类 id（键盘上下键按这个顺序走）。 */
export function navCategoryIds(module: AppMode): SettingsCategoryId[] {
  return navGroups(module).flatMap(g => g.items.map(c => c.id))
}

/** 导航键：竖向列表，上下移动，Home / End 跳到首尾。 */
export const NAV_KEYS = ['ArrowDown', 'ArrowUp', 'Home', 'End'] as const

/**
 * 按键之后应当落在哪个分类：
 * - ArrowDown / ArrowUp 在首尾处循环（与 WAI-ARIA 的标签页键盘约定一致）；
 * - Home / End 到第一项 / 最后一项；
 * - 其他键返回 null（调用方不拦截，保持浏览器原有行为）；
 * - 当前分类不在导航里（例如刚跨模块）时，向下 / Home 落在第一项，向上 / End 落在最后一项。
 */
export function moveCategory(module: AppMode, current: string, key: string): SettingsCategoryId | null {
  const ids = navCategoryIds(module)
  if (ids.length === 0) return null
  const at = ids.indexOf(current as SettingsCategoryId)
  switch (key) {
    case 'Home':
      return ids[0]
    case 'End':
      return ids[ids.length - 1]
    case 'ArrowDown':
      return at < 0 ? ids[0] : ids[(at + 1) % ids.length]
    case 'ArrowUp':
      return at < 0 ? ids[ids.length - 1] : ids[(at - 1 + ids.length) % ids.length]
    default:
      return null
  }
}

export interface ModuleSegment {
  id: AppMode
  name: string
  /** 是不是正在看的这个模块。 */
  current: boolean
  /** 悬停说明 / 无障碍名称。 */
  title: string
}

/** 顶部分段切换：两个模块按固定顺序排列，当前的高亮、另一个是跳转按钮。 */
export function moduleSegments(module: AppMode): ModuleSegment[] {
  return PROJECT_ORDER.map(id => ({
    id,
    name: PROJECTS[id].name,
    current: id === module,
    title: id === module ? `现在看的是 ${PROJECTS[id].name} 的设置` : jumpLabel(module),
  }))
}
