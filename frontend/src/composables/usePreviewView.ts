import { computed, ref } from 'vue'
import {
  LEGACY_CHAT_PREVIEW_KEY,
  PREVIEW_VIEW_DEFAULT,
  PREVIEW_VIEW_KEY,
  parseStoredFlag,
  parseViewParam,
  resolvePreviewView,
  serializeFlag,
} from '../lib/previewView'

// 「预览视图」（#120）的开关：每台设备各自选择，#165 起默认开（没有明确选过经典的设备看预览视图）。
// 规则见 lib/previewView.ts：本机开关存 localStorage（'1' 预览 / '0' 经典，没有值 = 默认）；
// 地址里的 ?view=preview|classic 只管这一次打开的页面，不写存储。

function readStored(): boolean {
  try {
    return parseStoredFlag(localStorage.getItem(PREVIEW_VIEW_KEY))
  } catch {
    return PREVIEW_VIEW_DEFAULT
  }
}

/** 地址参数在页面载入时读一次：路由跳转把参数从地址栏里拿掉之后，这一页仍按它显示。 */
function readOverride(): boolean | null {
  if (typeof window === 'undefined') return null
  try {
    return parseViewParam(window.location.search)
  } catch {
    return null
  }
}

/** 旧版「聊天视图（预览）」已经删除（#165）：把它留在存储里的开关键清掉。 */
function dropLegacyChatPreviewKey() {
  try {
    localStorage.removeItem(LEGACY_CHAT_PREVIEW_KEY)
  } catch {
    // 存储不可用 / 非浏览器环境（单元测试）：没有可清的
  }
}
dropLegacyChatPreviewKey()

const stored = ref(readStored())
const override = ref<boolean | null>(readOverride())
const enabled = computed(() => resolvePreviewView(stored.value, override.value))

function set(value: boolean) {
  override.value = null // 手动切换以后，地址参数不再压着开关
  stored.value = value
  try {
    localStorage.setItem(PREVIEW_VIEW_KEY, serializeFlag(value))
  } catch {
    // 存储不可用时只在本页面生效
  }
}

/** 别的标签页改了开关：跟着变（地址参数覆盖的页面不受影响）；整个存储被清空（key 为 null）时回到默认。 */
if (typeof window !== 'undefined') {
  window.addEventListener('storage', e => {
    if (e.key === PREVIEW_VIEW_KEY || e.key === null) stored.value = parseStoredFlag(e.key === null ? null : e.newValue)
  })
}

export function usePreviewView() {
  return { enabled, set, toggle: () => set(!enabled.value) }
}
