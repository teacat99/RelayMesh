// 项目页的页签（#156 批 3）：对外三个——知识库 / 模块 / 管理；「管理」里再分四个分区——设置 / 迁移 / 授权 / 审计。
//
// 地址里仍然用 ?tab=<取值>，取值保持原来的 kb | modules | module | settings | migration | grants | audit，
// 所以所有旧链接（确认卡、会话页头、文档页的「去迁移」、书签）一个都不用改：
// settings / migration / grants / audit 统归「管理」页签，并指明落在哪个分区；`?tab=manage` 是手输的写法，落在默认分区（设置）。

export const MANAGE_SECTIONS = [
  { id: 'settings', label: '设置', hint: '项目名称与说明、权限档位、归入的目录' },
  { id: 'migration', label: '迁移', hint: '把还留在工作流里的老文档并入项目知识库' },
  { id: 'grants', label: '授权', hint: '给 Agent 身份授权，或撤销授权' },
  { id: 'audit', label: '审计', hint: '看谁在什么时候对项目做了什么' },
] as const

export type ManageSection = (typeof MANAGE_SECTIONS)[number]['id']
/** 页签；`module` 是「模块」页签下的单个模块页（地址 ?tab=module&m=<路径>），高亮仍是「模块」。 */
export type ProjectTab = 'kb' | 'modules' | 'module' | 'manage'

export const DEFAULT_MANAGE_SECTION: ManageSection = 'settings'

export function isManageSection(v: unknown): v is ManageSection {
  return MANAGE_SECTIONS.some(s => s.id === v)
}

/** 把地址里的 ?tab= 解析成「哪个页签 + （管理页签里）哪个分区」。认不得的取值一律回到知识库。 */
export function resolveProjectTab(raw: unknown): { tab: ProjectTab; section: ManageSection } {
  const t = Array.isArray(raw) ? raw[0] : raw
  if (t === 'modules' || t === 'module') return { tab: t, section: DEFAULT_MANAGE_SECTION }
  if (t === 'manage') return { tab: 'manage', section: DEFAULT_MANAGE_SECTION }
  if (isManageSection(t)) return { tab: 'manage', section: t }
  return { tab: 'kb', section: DEFAULT_MANAGE_SECTION }
}

/** 键盘在分区之间移动：上 / 左 = 前一个，下 / 右 = 后一个，Home / End 跳首尾；到头不循环。返回 null 表示这个键不管。 */
export function moveManageSection(cur: ManageSection, key: string): ManageSection | null {
  const i = MANAGE_SECTIONS.findIndex(s => s.id === cur)
  if (i < 0) return null
  let next = i
  if (key === 'ArrowUp' || key === 'ArrowLeft') next = Math.max(0, i - 1)
  else if (key === 'ArrowDown' || key === 'ArrowRight') next = Math.min(MANAGE_SECTIONS.length - 1, i + 1)
  else if (key === 'Home') next = 0
  else if (key === 'End') next = MANAGE_SECTIONS.length - 1
  else return null
  return MANAGE_SECTIONS[next].id
}
