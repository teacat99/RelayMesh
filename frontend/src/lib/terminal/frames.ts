// 浏览器 ↔ Hub 终端 WebSocket 的线协议（与 internal/termproto 的 Browser* 帧一一对应）。
//
// 文本帧为 JSON 控制帧，二进制帧承载终端字节：
//   Hub → 浏览器  [0x01][offset u64 BE][payload]   会话输出，offset 是 payload 首字节在会话输出流中的累计偏移
//   浏览器 → Hub  [0x02][payload ≤ 16 KiB]          键盘输入 / 粘贴
// 这里只放纯函数（不依赖 DOM / Vue），便于单元测试。

export const SUBPROTOCOL = 'relaymesh.term.v1'
export const TICKET_PREFIX = 'tk.'

export const FRAME_OUTPUT = 0x01
export const FRAME_INPUT = 0x02

/** 单个输入帧的净荷上限（对应 termproto.MaxInputFrame）。 */
export const MAX_INPUT_PAYLOAD = 16 * 1024

/** 应用层关闭码（对应 termproto.Close*）。 */
export const CLOSE = {
  invalidHandshake: 4401,
  gateClosed: 4403,
  notFound: 4404,
  disabled: 4409,
  sessionEnded: 4410,
  stalled: 4412,
  superseded: 4413,
  /** 会话有记录时，附着方持续跟不上输出：服务端断开并要求重新附着，跳到最新输出。 */
  lagging: 4414,
  rateLimited: 4429,
} as const

export interface ReadyFrame {
  t: 'ready'
  sid: string
  base: number
  end: number
  from: number
  truncated: boolean
  /** 附着起点处需要先写给终端的模式恢复序列（仅全新附着才有）。 */
  prefix: Uint8Array
  rows: number
  cols: number
  state: string
  exitCode: number | null
}

export interface ExitFrame {
  t: 'exit'
  code: number | null
  signal: string
}

export interface ClosedFrame {
  t: 'closed'
  reason: string
}

/** 服务端告知「你跟不上输出了 / 追上了」：on 为真时界面应盖上「正在追赶」遮罩，持续下去服务端会以 4414 断开并要求重新附着。 */
export interface LagFrame {
  t: 'lag'
  on: boolean
}

/** 服务端告知「你跟不上输出了 / 追上了」：on 为真时界面应盖上「正在追赶」遮罩，持续下去服务端会以 4414 断开并要求重新附着。 */
export interface LagFrame {
  t: 'lag'
  on: boolean
}

export type ControlFrame = ReadyFrame | ExitFrame | ClosedFrame | LagFrame | LagFrame

const encoder = new TextEncoder()

function isUint(v: unknown): v is number {
  return typeof v === 'number' && Number.isFinite(v) && v >= 0 && Number.isSafeInteger(v)
}

/** 解码标准 base64（Hub 用它携带 prefix）；非法输入返回 null，而不是抛错。 */
export function decodeBase64(s: string): Uint8Array | null {
  try {
    const bin = atob(s)
    const out = new Uint8Array(bin.length)
    for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i)
    return out
  } catch {
    return null
  }
}

/** 解析文本控制帧；格式不对或类型未知一律返回 null（向前兼容：忽略不认识的帧）。 */
export function parseControl(text: string): ControlFrame | null {
  let raw: unknown
  try {
    raw = JSON.parse(text)
  } catch {
    return null
  }
  if (!raw || typeof raw !== 'object') return null
  const o = raw as Record<string, unknown>
  switch (o.t) {
    case 'ready': {
      if (typeof o.sid !== 'string' || !isUint(o.base) || !isUint(o.end) || !isUint(o.from)) return null
      let prefix = new Uint8Array(0)
      if (typeof o.prefix === 'string' && o.prefix !== '') {
        const dec = decodeBase64(o.prefix)
        if (!dec) return null
        prefix = dec
      }
      return {
        t: 'ready',
        sid: o.sid,
        base: o.base,
        end: o.end,
        from: o.from,
        truncated: o.truncated === true,
        prefix,
        rows: isUint(o.rows) ? o.rows : 0,
        cols: isUint(o.cols) ? o.cols : 0,
        state: typeof o.state === 'string' ? o.state : '',
        exitCode: typeof o.exit_code === 'number' ? o.exit_code : null,
      }
    }
    case 'exit':
      return {
        t: 'exit',
        code: typeof o.code === 'number' ? o.code : null,
        signal: typeof o.signal === 'string' ? o.signal : '',
      }
    case 'closed':
      return { t: 'closed', reason: typeof o.reason === 'string' ? o.reason : '' }
    case 'lag':
      return { t: 'lag', on: o.on === true }
    default:
      return null
  }
}

/** 解码输出二进制帧；长度不足、类型不符或偏移超出安全整数范围返回 null。 */
export function decodeOutput(buf: ArrayBuffer | Uint8Array): { offset: number; payload: Uint8Array } | null {
  const bytes = buf instanceof Uint8Array ? buf : new Uint8Array(buf)
  if (bytes.length < 9 || bytes[0] !== FRAME_OUTPUT) return null
  const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength)
  const offset = view.getBigUint64(1, false)
  if (offset > BigInt(Number.MAX_SAFE_INTEGER)) return null
  return { offset: Number(offset), payload: bytes.subarray(9) }
}

/** 把原始字节切成若干输入帧（每帧净荷不超过 MAX_INPUT_PAYLOAD）。 */
export function encodeInputBytes(data: Uint8Array): Uint8Array[] {
  const frames: Uint8Array[] = []
  let pos = 0
  while (pos < data.length) {
    let end = Math.min(pos + MAX_INPUT_PAYLOAD, data.length)
    // 尽量不在 UTF-8 多字节序列中间切开：回退到字符边界（续字节形如 10xxxxxx）。
    if (end < data.length) {
      let back = end
      while (back > pos && (data[back] & 0xc0) === 0x80) back--
      if (back > pos) end = back
    }
    const frame = new Uint8Array(1 + (end - pos))
    frame[0] = FRAME_INPUT
    frame.set(data.subarray(pos, end), 1)
    frames.push(frame)
    pos = end
  }
  return frames
}

/** 把键盘 / 粘贴文本按 UTF-8 编码并切成输入帧。 */
export function encodeInput(text: string): Uint8Array[] {
  return encodeInputBytes(encoder.encode(text))
}

/**
 * xterm 的 onBinary 给出的是「每个字符代表一个字节」的 Latin-1 字符串（鼠标上报等场景），
 * 不能按 UTF-8 重新编码，否则 0x80 以上的字节会被放大成两个字节。
 */
export function encodeBinaryString(data: string): Uint8Array[] {
  const bytes = new Uint8Array(data.length)
  for (let i = 0; i < data.length; i++) bytes[i] = data.charCodeAt(i) & 0xff
  return encodeInputBytes(bytes)
}

export function ackFrame(offset: number): string {
  return JSON.stringify({ t: 'ack', offset })
}

export function resizeFrame(rows: number, cols: number): string {
  return JSON.stringify({ t: 'resize', rows: clampDim(rows), cols: clampDim(cols) })
}

/** 终端尺寸必须是 [1, 1000] 内的整数（与 termproto.ClampSize 一致，服务端会再校验一次）。 */
export function clampDim(n: number): number {
  if (!Number.isFinite(n)) return 1
  return Math.min(1000, Math.max(1, Math.floor(n)))
}
