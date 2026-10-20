/** 与 internal/docs 一致的标题锚点 slug（CJK 保留）。 */
export function slugHeadingAnchor(text: string): string {
  const used = new Map<string, number>()
  return slugOnce(text.trim(), used)
}

function slugOnce(text: string, used: Map<string, number>): string {
  let b = ''
  let dash = false
  for (const char of text) {
    const code = char.codePointAt(0) ?? 0
    const isCJK =
      (code >= 0x4e00 && code <= 0x9fff) ||
      (code >= 0x3040 && code <= 0x30ff) ||
      (code >= 0xac00 && code <= 0xd7af)
    if (isCJK || /[a-zA-Z0-9]/.test(char)) {
      b += char.toLowerCase()
      dash = false
    } else if (/\s|[-_]/.test(char)) {
      if (!dash && b.length > 0) {
        b += '-'
        dash = true
      }
    }
  }
  let s = b.replace(/^-+|-+$/g, '')
  if (!s) s = 'section'
  const n = (used.get(s) ?? 0) + 1
  used.set(s, n)
  return n > 1 ? `${s}-${n}` : s
}

export interface DocHeading {
  level: number
  text: string
  anchor: string
}

const headingRe = /^(#{1,6})\s+(.+?)\s*$/

/** 解析标题（跳过围栏代码块），用于 TOC。 */
export function parseDocHeadings(body: string): DocHeading[] {
  const lines = body.split('\n')
  let inFence = false
  const out: DocHeading[] = []
  const used = new Map<string, number>()
  for (const line of lines) {
    const trim = line.trim()
    if (trim.startsWith('```')) {
      inFence = !inFence
      continue
    }
    if (inFence) continue
    const m = headingRe.exec(line)
    if (!m) continue
    const text = m[2].trim()
    const base = slugOnce(text, used)
    out.push({ level: m[1].length, text, anchor: base })
  }
  return out
}

