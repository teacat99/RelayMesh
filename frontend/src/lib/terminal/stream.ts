// 输出流的偏移记账：去重 / 截断补齐 / 确认（ack）批量化。纯逻辑，不依赖 DOM。

/**
 * 按偏移拼接输出流。Hub 每个输出帧都带「净荷首字节在会话输出中的累计偏移」，
 * 所以重连后即使服务端从更早的位置重放，也能精确去重；反过来如果历史已被环形缓冲覆盖，也能知道丢了多少字节。
 */
export class OutputAssembler {
  private next = 0
  private started = false

  /** ready 帧到达后调用：声明下一个期望的偏移（服务端的回放起点）。 */
  start(from: number): void {
    this.next = from
    this.started = true
  }

  /** 已经交给终端的累计偏移（= 重连时向服务端索要的 since）。 */
  get nextOffset(): number {
    return this.next
  }

  get isStarted(): boolean {
    return this.started
  }

  /**
   * 接收一个输出帧。
   *   - data 为 null：整帧都是重复数据，应丢弃；
   *   - gap > 0：帧与已收到的数据之间缺了 gap 个字节（历史被覆盖），data 仍应照常写入；
   *   - end：写入该数据后的累计偏移，用于确认。
   */
  push(offset: number, payload: Uint8Array): { data: Uint8Array | null; gap: number; end: number } {
    if (!this.started || payload.length === 0) return { data: null, gap: 0, end: this.next }
    const end = offset + payload.length
    if (end <= this.next) return { data: null, gap: 0, end: this.next }
    let data = payload
    let gap = 0
    if (offset < this.next) {
      data = payload.subarray(this.next - offset)
    } else if (offset > this.next) {
      gap = offset - this.next
    }
    this.next = end
    return { data, gap, end }
  }
}

/**
 * 确认批量化：每次终端渲染完一帧都发一条 ack 会把控制通道打满，
 * 所以累计到阈值才立即确认，其余交给一个很短的定时器兜底（保证低流量时窗口也能及时释放）。
 * Hub 的流控窗口是 256 KiB，阈值取它的八分之一。
 */
export class AckBatcher {
  private acked = 0
  private rendered = 0

  constructor(private readonly threshold = 32 * 1024) {}

  /** 新一轮附着开始：确认位置对齐到回放起点。 */
  reset(at: number): void {
    this.acked = at
    this.rendered = at
  }

  /** 终端渲染到了 offset；返回需要立即发送的 ack 偏移，或 null（稍后由 flush 兜底）。 */
  onRendered(offset: number): number | null {
    if (offset > this.rendered) this.rendered = offset
    if (this.rendered - this.acked >= this.threshold) return this.take()
    return null
  }

  /** 兜底：把尚未确认的进度全部确认。没有新进度时返回 null。 */
  flush(): number | null {
    return this.rendered > this.acked ? this.take() : null
  }

  get pending(): boolean {
    return this.rendered > this.acked
  }

  private take(): number {
    this.acked = this.rendered
    return this.acked
  }
}
