// 设置页的分类注册表（纯数据 + 纯函数，便于单元测试）。
//
// 每个分类都有明确的归属：
//   - common：两个模块都用得到的（外观与通知、MCP 令牌与安全、备份导出、关于）；
//   - relay ：只属于 RelayMesh（会话与交付）；
//   - exec  ：只属于 ExecMesh（终端与模型执行）。
// 设置窗口只显示「当前模块的分类 + 通用分类」，另一个模块的分类通过窗口里的「RelayMesh | ExecMesh」分段切换进入。

import { PROJECTS, otherProject, type AppMode } from './mode'

export type SettingsOwner = 'common' | AppMode

export type SettingsCategoryId =
  | 'appearance' | 'security' | 'oauth' | 'backup' | 'about'
  | 'waiting' | 'prompts' | 'speech' | 'presets' | 'authProfiles' | 'sessionPrompts' | 'sessionData'
  | 'termPrefs' | 'modelAccess' | 'retention'

export interface SettingsCategory {
  id: SettingsCategoryId
  label: string
  owner: SettingsOwner
  /** 给无障碍名称 / 悬停提示用的一句话说明。 */
  hint: string
}

export const SETTINGS_CATEGORIES: readonly SettingsCategory[] = [
  // ---- RelayMesh ----
  { id: 'waiting', label: '等待策略', owner: 'relay', hint: '智能体等待你反馈时的超时与轮询节奏、追加指令的提醒方式，以及分支会话请示的定夺与转交' },
  { id: 'prompts', label: '提示词流转', owner: 'relay', hint: '发给智能体的服务指引与各场景的回执提示词' },
  { id: 'speech', label: '语音 ASR', owner: 'relay', hint: '语音输入的识别服务与语言' },
  { id: 'presets', label: '快捷标签预设', owner: 'relay', hint: '反馈输入框上方的快捷语，以及工作流阶段模板' },
  { id: 'authProfiles', label: '授权档位', owner: 'relay', hint: '异步对话模式下，智能体不必先问就能直接处理哪些事' },
  { id: 'sessionPrompts', label: '会话提示词', owner: 'relay', hint: '常驻规则的内置模板（主线 / 分支）：会话输入区的「常驻」里一点套用，新建会话时也能选' },
  { id: 'sessionData', label: '会话数据与图片', owner: 'relay', hint: '本地草稿缓存与图片下载限速' },
  // ---- ExecMesh ----
  { id: 'termPrefs', label: '终端偏好', owner: 'exec', hint: '终端字号、多行粘贴确认与同时保持连接的会话数' },
  { id: 'modelAccess', label: '模型执行与权限', owner: 'exec', hint: '哪些模型可以使用哪些主机，以及新主机、新模型的默认授权' },
  { id: 'retention', label: '记录与保留', owner: 'exec', hint: '终端记录与模型运行记录的保留策略' },
  // ---- 通用 ----
  { id: 'appearance', label: '外观与通知', owner: 'common', hint: '主题、声音与桌面通知、手机推送（ntfy，可按消息类型设优先级、打开异常提醒）与界面动效' },
  { id: 'security', label: 'MCP 与安全', owner: 'common', hint: '访问认证、IP 防护与 MCP 令牌管理' },
  { id: 'oauth', label: 'OAuth 授权', owner: 'common', hint: 'ChatGPT / Claude 连接器的授权：接入窗口、刚登记的客户端、已授权的应用与授权服务器设置' },
  { id: 'backup', label: '备份与导出', owner: 'common', hint: '导出全部配置' },
  { id: 'about', label: '关于', owner: 'common', hint: '版本与项目信息' },
]

const BY_ID: Readonly<Record<string, SettingsCategory>> = Object.fromEntries(SETTINGS_CATEGORIES.map(c => [c.id, c]))

export function categoryById(id: string): SettingsCategory | undefined {
  return Object.prototype.hasOwnProperty.call(BY_ID, id) ? BY_ID[id] : undefined
}

/** 某个归属下的全部分类（保持注册顺序）。 */
export function categoriesOwnedBy(owner: SettingsOwner): SettingsCategory[] {
  return SETTINGS_CATEGORIES.filter(c => c.owner === owner)
}

/** 在 module 的设置窗口里能看到的分类：模块自己的在前，通用的在后。 */
export function visibleCategories(module: AppMode): { own: SettingsCategory[]; common: SettingsCategory[] } {
  return { own: categoriesOwnedBy(module), common: categoriesOwnedBy('common') }
}

export function firstCategory(module: AppMode): SettingsCategoryId {
  return visibleCategories(module).own[0].id
}

/**
 * 把想要的分类 id 落到 module 的窗口里一个确实存在的分类：
 * 通用分类与本模块的分类原样返回；另一个模块的分类、未知 id 或空值回到本模块的第一个分类。
 */
export function resolveCategory(module: AppMode, wanted: string | null | undefined): SettingsCategoryId {
  const c = wanted ? categoryById(wanted) : undefined
  if (c && (c.owner === 'common' || c.owner === module)) return c.id
  return firstCategory(module)
}

export function settingsTitle(module: AppMode): string {
  return `设置 · ${PROJECTS[module].name}`
}

/** 分段切换里「去对方模块」那一段的说明（悬停提示与无障碍名称）。 */
export function jumpLabel(module: AppMode): string {
  return `去 ${PROJECTS[otherProject(module)].name} 的设置`
}
