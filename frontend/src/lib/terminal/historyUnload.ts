// 接续历史之后的「用完即还」：历史只在用户往上翻的时候占内存。
// 回到实时底部超过一阵子、或者整个面板 / 页面被切走很久，就该把接续进来的历史卸掉（回滚缓冲区回到基础大小）。
// 这里只有判断，不碰终端；卸载动作在 composables/useInlineHistory.ts。

/** 停在实时底部这么久，就认为用户看完了历史。留一点余量：刚回到底部敲一条命令又要往上翻的场景不会反复重读。 */
export const UNLOAD_AT_BOTTOM_MS = 30_000
/** 面板被切走 / 页面被隐藏这么久，不管停在哪里都卸载。 */
export const UNLOAD_AWAY_MS = 180_000

export interface ViewSample {
  /** 视口停在缓冲区最底部（跟着实时输出）。 */
  atBottom: boolean
  /** 面板不是当前标签，或浏览器页面被隐藏。 */
  away: boolean
}

/** 记录「停在底部多久」「离开多久」，到点时 sample 返回 true。 */
export class UnloadClock {
  private bottomSince: number | null = null
  private awaySince: number | null = null

  constructor(
    private readonly atBottomMs = UNLOAD_AT_BOTTOM_MS,
    private readonly awayMs = UNLOAD_AWAY_MS,
  ) {}

  /** 记录一次采样（now 为毫秒时间戳）；返回 true = 现在该卸载了。 */
  sample(now: number, v: ViewSample): boolean {
    this.bottomSince = v.atBottom ? (this.bottomSince ?? now) : null
    this.awaySince = v.away ? (this.awaySince ?? now) : null
    return (
      (this.bottomSince !== null && now - this.bottomSince >= this.atBottomMs) ||
      (this.awaySince !== null && now - this.awaySince >= this.awayMs)
    )
  }

  reset(): void {
    this.bottomSince = null
    this.awaySince = null
  }
}
