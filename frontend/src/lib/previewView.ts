// 「预览视图」（#120）的开关规则：纯函数，便于单测；状态与存储在 composables/usePreviewView.ts。
//
// 预览视图是会话页里与经典视图并行的另一套会话列表。#165 起它是默认视图：没有明确选过经典的设备一律看预览视图，
// 经典视图保留，每台设备各自选择。
//   - 本机开关存在 localStorage，三态：'1' = 明确选了预览、'0' = 明确选了经典；没有值 / 旧值 / 乱值 = 没选过 = 默认（预览）。
//     所以「回到经典视图」要写 '0'（不能只清掉键，清掉等于回到默认）；
//   - 地址里的 ?view=preview / ?view=classic 可以临时覆盖：只管这一次打开的页面，不写进 localStorage
//     （方便分享链接与自动化测试，也不会因为点开一个链接就改了这台设备的习惯）；
//   - 地址参数优先于本机开关；手动点「试用预览视图 / 回到经典视图」之后，地址参数不再压着开关。

/** localStorage 里的键。 */
export const PREVIEW_VIEW_KEY = 'relaymesh_preview_view'

/** 旧版「聊天视图（预览）」的开关键：#165 删除那个视图之后不再有人读它，启动时清掉（留着只是占一条存储）。 */
export const LEGACY_CHAT_PREVIEW_KEY = 'relaymesh_chat_preview'

/** 没有明确选过时的默认：预览视图。 */
export const PREVIEW_VIEW_DEFAULT = true

/** 本机开关：'0' = 明确选了经典（关）、'1' = 明确选了预览（开）；没有值、旧值、乱值都按默认。 */
export function parseStoredFlag(raw: string | null | undefined): boolean {
  if (raw === '0') return false
  if (raw === '1') return true
  return PREVIEW_VIEW_DEFAULT
}

/** 写进 localStorage 的值：明确选了预览 '1'、明确选了经典 '0'（两种都写，不靠「没有值」表达经典）。 */
export function serializeFlag(enabled: boolean): string {
  return enabled ? '1' : '0'
}

/** 地址里的 ?view=：preview = 临时开，classic = 临时关，没有或别的值 = 不覆盖（null）。 */
export function parseViewParam(search: string): boolean | null {
  let value: string | null
  try {
    value = new URLSearchParams(search).get('view')
  } catch {
    return null
  }
  if (value === 'preview') return true
  if (value === 'classic') return false
  return null
}

/** 最终是否用预览视图：地址参数优先于本机开关。 */
export function resolvePreviewView(stored: boolean, override: boolean | null): boolean {
  return override ?? stored
}
