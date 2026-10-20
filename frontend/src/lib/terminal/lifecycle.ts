// 会话生命周期的展示逻辑（纯函数，便于单元测试）。
//
// 人工终端默认「空闲不回收」：没人连接也不回收，只有你终止它、进程自己退出、主机重启等不可抗力才会结束。
// 也可以选择「空闲回收」：连续这么久既没有人连接、也没有任何输入，守护进程才会回收它（输出不算交互）。
//
// 命名说明：这里管的是「没人用了要不要收走」（会话策略，可改）；与主机页上的「重启不中断」（Agent 升级 / 重启 / 崩溃时
// 会话还在不在，见 format.ts 的 holderBadge）是两件独立的事，名字刻意拉开，避免都叫「持久 / 永久」而被误当成一回事。
// 强制执行者是主机上的守护进程，所以服务端或浏览器离线时计时照常进行。

export interface IdleOption {
  /** 空闲回收秒数；0 = 空闲不回收。 */
  value: number
  label: string
  /** 徽标上的简短写法。 */
  short: string
}

const HOUR = 3600
const DAY = 24 * HOUR

/** 界面提供的取值（与服务端允许的范围一致：0，或 1 小时 ~ 30 天）。 */
export const IDLE_OPTIONS: IdleOption[] = [
  { value: 0, label: '空闲不回收', short: '不回收' },
  { value: HOUR, label: '空闲 1 小时后回收', short: '1 小时' },
  { value: DAY, label: '空闲 1 天后回收', short: '1 天' },
  { value: 7 * DAY, label: '空闲 7 天后回收', short: '7 天' },
  { value: 30 * DAY, label: '空闲 30 天后回收', short: '30 天' },
]

/** 秒数 → 「1 小时」「3 天」「90 分钟」；能整除的取最大单位。 */
export function formatIdle(sec: number): string {
  const s = Math.max(0, Math.round(sec))
  if (s >= DAY && s % DAY === 0) return `${s / DAY} 天`
  if (s >= HOUR && s % HOUR === 0) return `${s / HOUR} 小时`
  if (s >= 60 && s % 60 === 0) return `${s / 60} 分钟`
  return `${s} 秒`
}

/** 「空闲不回收」或「空闲 1 天后回收」。 */
export function idleSummary(sec: number): string {
  return sec > 0 ? `空闲 ${formatIdle(sec)}后回收` : '空闲不回收'
}

/** 取值下拉框里的一项：`inherit` 表示沿用主机默认。 */
export type IdleChoice = 'inherit' | number

export interface LifecycleSource {
  state?: string
  idle?: number
  idle_own?: boolean
  attached?: number
  active?: number
}

/** 会话当前选中的下拉项。 */
export function idleChoiceOf(s: LifecycleSource): IdleChoice {
  return s.idle_own ? (s.idle ?? 0) : 'inherit'
}

export interface LifecycleBadge {
  text: string
  /** ok：空闲不回收；info：设置了空闲回收。 */
  tone: 'ok' | 'info'
  title: string
}

/**
 * 如果从现在起一直没有交互，会话将在哪个时刻（Unix 秒）被回收。
 * 空闲不回收、已退出、或者此刻有人连接着（计时尚未开始）时返回 null。
 */
export function reclaimAt(s: LifecycleSource): number | null {
  if (s.state === 'exited') return null
  const idle = s.idle ?? 0
  if (idle <= 0 || (s.attached ?? 0) > 0) return null
  const active = s.active ?? 0
  return active > 0 ? active + idle : null
}

/** 距离被回收还剩多久的口语化描述（只在设置了空闲回收、且没人连接时有意义）。 */
export function reclaimEta(s: LifecycleSource, nowSec: number): string {
  const at = reclaimAt(s)
  if (at === null) return ''
  const left = at - nowSec
  if (left <= 0) return '随时会被回收'
  if (left < 90) return '不到 2 分钟后回收'
  if (left < 2 * HOUR) return `约 ${Math.round(left / 60)} 分钟后回收`
  if (left < 2 * DAY) return `约 ${Math.round(left / HOUR)} 小时后回收`
  return `约 ${Math.round(left / DAY)} 天后回收`
}

/** 页面顶部的生命周期徽标；已退出的会话没有生命周期。 */
export function lifecycleBadge(s: LifecycleSource, nowSec: number = Math.floor(Date.now() / 1000)): LifecycleBadge | null {
  if (s.state === 'exited') return null
  const idle = s.idle ?? 0
  const origin = s.idle_own ? '（这个会话单独设置）' : '（沿用这台主机的默认设置）'
  if (idle <= 0) {
    return {
      text: '空闲不回收',
      tone: 'ok',
      title: `没人连接也不会被回收：只有你终止它、里面的程序退出，或主机重启等不可抗力才会结束。${origin}`,
    }
  }
  const eta = reclaimEta(s, nowSec)
  return {
    text: `空闲 ${formatIdle(idle)}回收`,
    tone: 'info',
    title: `连续 ${formatIdle(idle)}既没有人连接、也没有输入，会话就会被回收（程序的输出不算交互）。${origin}${eta ? `\n现在${eta}。` : '\n现在有人连接着，计时尚未开始。'}`,
  }
}

/** 因不可抗力结束的原因 → 给用户的说明；不属于不可抗力则返回 null。 */
export function forceMajeureNote(reason?: string): string | null {
  switch (reason) {
    case 'host_reboot':
      return '主机重启了，里面的程序随之结束。'
    case 'holder_stopped':
      return '会话守护进程被停止了，里面的程序随之结束。'
    case 'holder_exit':
      return '会话守护进程意外退出了，里面的程序随之结束。'
    case 'gone':
      return '主机报告这个会话已经不存在（原因未知）。'
    default:
      return null
  }
}

/** 这个结束原因是否适合提供「在原目录重建会话」。 */
export function canRebuild(reason?: string): boolean {
  return forceMajeureNote(reason) !== null
}

/** 把结束的原因说成人话（用于已结束会话的页面横幅）。 */
export function endedNote(reason?: string, exitCode?: number): string {
  const fm = forceMajeureNote(reason)
  if (fm) return fm
  switch (reason) {
    case 'idle_ttl':
      return '按生命周期设置，因长时间没有人使用而被回收。'
    case 'killed':
      return '会话被终止。'
    case 'policy':
      return '终端总闸被关闭，会话被终止。'
    case 'exit':
      return typeof exitCode === 'number' && exitCode !== 0 ? `里面的程序已退出（退出码 ${exitCode}）。` : '里面的程序已退出。'
    default:
      return '会话已结束。'
  }
}
