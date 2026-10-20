// HTML Artifact 与宿主页之间的 postMessage 桥（纯函数部分，便于单测）。
// 约束（设计稿 06 §6）：宿主校验 event.source === iframe.contentWindow；只接受白名单消息；
// 数据操作按「查看者」身份代为执行；不开放聊天发送；写入有频率上限。

export type BridgeRequest =
  | { kind: 'data.get'; id: number; key: string }
  | { kind: 'data.set'; id: number; key: string; value: unknown; expected?: number }
  | { kind: 'data.list'; id: number }
  | { kind: 'data.delete'; id: number; key: string }
  | { kind: 'comment'; id: number; anchor: string; text: string }
  | { kind: 'resize'; height: number }

const KEY_RE = /^[A-Za-z0-9_.:-]{1,128}$/

export const MIN_FRAME_HEIGHT = 160
export const MAX_FRAME_HEIGHT = 5000

/** 解析来自 iframe 的消息；不合规一律返回 null（直接丢弃，不回复）。 */
export function parseBridgeMessage(data: unknown): BridgeRequest | null {
  if (!data || typeof data !== 'object') return null
  const d = data as Record<string, unknown>
  if (d.rm !== 1 || typeof d.type !== 'string') return null
  if (d.type === 'resize') {
    const h = Number(d.height)
    return Number.isFinite(h) ? { kind: 'resize', height: clampFrameHeight(h) } : null
  }
  const id = Number(d.id)
  if (!Number.isInteger(id) || id <= 0) return null
  switch (d.type) {
    case 'data.list':
      return { kind: 'data.list', id }
    case 'data.get':
    case 'data.delete':
      return typeof d.key === 'string' && KEY_RE.test(d.key) ? { kind: d.type, id, key: d.key } : null
    case 'data.set': {
      if (typeof d.key !== 'string' || !KEY_RE.test(d.key)) return null
      const expected = d.expected === undefined || d.expected === null ? undefined : Number(d.expected)
      if (expected !== undefined && !Number.isInteger(expected)) return null
      return { kind: 'data.set', id, key: d.key, value: d.value, expected }
    }
    case 'comment': {
      // 桥脚本里 comment(text, anchor) 把 anchor 放在 key、文本放在 value
      const text = typeof d.value === 'string' ? d.value.trim() : ''
      const anchor = typeof d.key === 'string' ? d.key.slice(0, 128) : ''
      return text ? { kind: 'comment', id, anchor, text } : null
    }
  }
  return null
}

export function clampFrameHeight(h: number): number {
  return Math.min(MAX_FRAME_HEIGHT, Math.max(MIN_FRAME_HEIGHT, Math.ceil(h)))
}

/** 滑动窗口限频：窗口内最多 limit 次，防止页面脚本循环狂写。 */
export class WriteLimiter {
  private stamps: number[] = []
  constructor(
    private readonly limit = 20,
    private readonly windowMs = 1000,
  ) {}
  allow(now = Date.now()): boolean {
    this.stamps = this.stamps.filter(t => now - t < this.windowMs)
    if (this.stamps.length >= this.limit) return false
    this.stamps.push(now)
    return true
  }
}

/** 宿主回给 iframe 的应答。 */
export function bridgeReply(id: number, ok: boolean, payload: unknown): Record<string, unknown> {
  return ok ? { rm: 1, id, ok: true, value: payload } : { rm: 1, id, ok: false, error: String(payload ?? 'failed') }
}

/** 把 REST 返回的数据项交给页面：value 还原成 JSON 值。 */
export function dataItemForPage(item: { key: string; value: string; version: number }): { key: string; value: unknown; version: number } {
  let value: unknown = null
  try {
    value = JSON.parse(item.value)
  } catch {
    value = null
  }
  return { key: item.key, value, version: item.version }
}
