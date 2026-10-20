// 复制文本：优先用 Clipboard API（仅安全上下文可用），
// 通过 http://局域网IP 访问时退回到 execCommand('copy')，保证两种访问方式都能复制。

export async function copyText(text: string): Promise<boolean> {
  try {
    if (typeof navigator !== 'undefined' && navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(text)
      return true
    }
  } catch {
    /* 被权限策略拒绝：走回退方案 */
  }
  try {
    const ta = document.createElement('textarea')
    ta.value = text
    ta.setAttribute('readonly', '')
    ta.style.cssText = 'position:fixed;top:0;left:0;width:1px;height:1px;opacity:0;pointer-events:none'
    document.body.appendChild(ta)
    const prevFocus = document.activeElement as HTMLElement | null
    ta.select()
    const ok = document.execCommand('copy')
    document.body.removeChild(ta)
    prevFocus?.focus?.()
    return ok
  } catch {
    return false
  }
}

/** 读取剪贴板文本；不可用或被拒绝时返回 null（调用方提示用户改用 Ctrl+Shift+V）。 */
export async function readClipboardText(): Promise<string | null> {
  try {
    if (typeof navigator !== 'undefined' && navigator.clipboard?.readText && window.isSecureContext) {
      return await navigator.clipboard.readText()
    }
  } catch {
    /* 用户拒绝授权 */
  }
  return null
}
