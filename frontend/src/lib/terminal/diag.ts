// 终端连接的诊断事件环（#101）：只记「发生了什么、什么时候、多大」，绝不记终端内容、令牌、命令。
//
// 用途：「终端忽然滚屏 / 重新加载」这类问题事后很难复盘——是重连了？尺寸变了？被服务端判定跟不上输出？WebGL 丢了？
// 事件环常驻内存（每个窗格 200 条），右键菜单里一键复制成文字，贴给开发者就够定位。

export type DiagValue = string | number | boolean

export interface DiagEvent {
  /** 事件发生的时间（毫秒时间戳）。 */
  t: number
  /** 事件名（短英文词，如 connect / ready / close / retry / resize / lag / seed / webgl-lost）。 */
  k: string
  d?: Record<string, DiagValue>
}

const DEFAULT_CAP = 200

/** 字符串值只留可打印的短文本，防止把别的东西（比如服务端给的原因文字里夹带的内容）原样写进诊断。 */
function clean(v: DiagValue): DiagValue {
  if (typeof v !== 'string') return v
  return v.replace(/[^\x20-\x7e\u4e00-\u9fff（）：，。、；]/g, '·').slice(0, 80)
}

export class DiagLog {
  private buf: DiagEvent[] = []

  constructor(
    private readonly cap: number = DEFAULT_CAP,
    private readonly now: () => number = Date.now,
  ) {}

  add(k: string, d?: Record<string, DiagValue>): void {
    let data: Record<string, DiagValue> | undefined
    if (d) {
      data = {}
      for (const key of Object.keys(d)) data[key] = clean(d[key])
    }
    this.buf.push({ t: this.now(), k, d: data })
    if (this.buf.length > this.cap) this.buf.splice(0, this.buf.length - this.cap)
  }

  get size(): number {
    return this.buf.length
  }

  events(): readonly DiagEvent[] {
    return this.buf
  }

  /**
   * 渲染成可复制的文字：头部是调用方给的环境信息（版本、窗格尺寸、会话短 id 等），下面是相对时间的事件列表（最新的在最后）。
   * 相对时间以第一条事件为零点，另给最后一条事件距现在多久，方便对上「用户说的那一刻」。
   */
  text(header: Record<string, DiagValue> = {}, nowMs: number = this.now()): string {
    const lines = ['# RelayMesh 终端连接诊断（不含终端内容）']
    for (const key of Object.keys(header)) lines.push(`${key}: ${clean(header[key])}`)
    lines.push(`events: ${this.buf.length}`)
    if (this.buf.length === 0) return lines.join('\n') + '\n'
    const t0 = this.buf[0].t
    lines.push(`last event: ${((nowMs - this.buf[this.buf.length - 1].t) / 1000).toFixed(1)}s ago`)
    lines.push('')
    for (const e of this.buf) {
      const rel = ((e.t - t0) / 1000).toFixed(3).padStart(9)
      const extra = e.d
        ? ' ' +
          Object.keys(e.d)
            .map(key => `${key}=${e.d![key]}`)
            .join(' ')
        : ''
      lines.push(`+${rel}s ${e.k}${extra}`)
    }
    return lines.join('\n') + '\n'
  }
}
