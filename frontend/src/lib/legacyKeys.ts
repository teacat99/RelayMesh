// 已经删掉的功能留在浏览器本机存储里的开关键：启动时清一次，免得一直留在用户的设备上。
//   - relaymesh_chat_preview：旧版「聊天视图（预览）」的开关（#165 删除）；
//   - relaymesh_preview_view：「预览视图」的本机开关（'1' 预览 / '0' 经典，没有值 = 默认预览）。#166 删掉经典视图以后不再读取，
//     以前明确选过经典（'0'）的设备从此直接看预览视图，这里把键清掉，不弹任何提示。
// 键名固定，别改：改了就清不掉老设备上的那一份。
export const LEGACY_STORAGE_KEYS = ['relaymesh_chat_preview', 'relaymesh_preview_view'] as const

type RemovableStorage = Pick<Storage, 'removeItem'>

/** 把上面这些键从存储里清掉；存储不可用（隐私模式、非浏览器环境、单元测试）时什么也不做，不抛错。可传入假存储便于测试。 */
export function dropLegacyStorageKeys(storage?: RemovableStorage): void {
  try {
    const target = storage ?? (typeof localStorage === 'undefined' ? undefined : localStorage)
    if (!target) return
    for (const key of LEGACY_STORAGE_KEYS) target.removeItem(key)
  } catch {
    // 存储不可用：没有可清的
  }
}
