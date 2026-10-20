// 回放期间压制终端自动生成的应答。
//
// 附着（页面刷新、断线重连）时 Hub 会把缓冲里的历史输出重放给终端。xterm.js 解析这些数据时，
// 会把其中的查询（设备属性 DA、光标位置 CPR、颜色 OSC 10/11、模式 DECRQM……）当成新的查询照常应答，
// 应答经 onData 写回会话输入。可发起查询的进程（vim、fish、tmux 的启动探测……）早就不在等了，
// 这些迟到的应答落到 shell 提示符上就是 `^[[?1;2c`、`0;276;0c` 一类的乱码。
// 所以解析回放数据的这段窗口内，终端自己产生的应答必须丢弃；实时输出里的查询则照常应答。
//
// 窗口 = 从写入一段回放数据，到终端报告这段数据解析完毕（xterm.js 的写回调）。
// 为防止解析异常时回调永远不来、把用户输入一直压住，设了「无进展超时」：超过 maxStallMs 没有任何回放数据
// 被解析完成就自动解除压制。

export class ReplayMute {
  private inFlight = 0
  private until = 0

  constructor(
    private readonly now: () => number = Date.now,
    private readonly maxStallMs = 3000,
  ) {}

  /** 当前是否应当丢弃终端自动生成的应答。 */
  get active(): boolean {
    return this.inFlight > 0 && this.now() < this.until
  }

  /**
   * 包装一次 term.write 的完成回调。replay 为 false 时原样返回；
   * 为 true 时立刻开始压制，并在回调触发（该段数据解析完毕）后解除。
   */
  wrap(replay: boolean, done: () => void): () => void {
    if (!replay) return done
    this.inFlight++
    this.until = this.now() + this.maxStallMs
    let finished = false
    return () => {
      if (!finished) {
        finished = true
        if (this.inFlight > 0) this.inFlight--
        // 还有回放数据在排队解析：这次完成算一次「进展」，延长无进展超时。
        if (this.inFlight > 0) this.until = this.now() + this.maxStallMs
      }
      done()
    }
  }
}
