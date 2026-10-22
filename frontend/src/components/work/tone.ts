// 任务清单用到的色调 → Tailwind 类名。Tailwind 需要在源码里看到完整类名，所以这里全部写成字面量。
import type { Tone } from '../../lib/workItems'

export interface ToneClasses {
  /** 小圆点 / 色块。 */
  dot: string
  /** 徽标：浅底 + 同色文字 + 细边。 */
  badge: string
  /** 列头文字。 */
  text: string
  /** 卡片左侧的强调线。 */
  accent: string
}

export const TONE_CLASSES: Record<Tone, ToneClasses> = {
  slate: {
    dot: 'bg-slate-400',
    badge: 'bg-slate-500/10 text-slate-600 dark:text-slate-300 border-slate-500/25',
    text: 'text-slate-600 dark:text-slate-300',
    accent: 'border-l-slate-400/70',
  },
  sky: {
    dot: 'bg-sky-500',
    badge: 'bg-sky-500/10 text-sky-600 dark:text-sky-300 border-sky-500/25',
    text: 'text-sky-600 dark:text-sky-300',
    accent: 'border-l-sky-500/80',
  },
  amber: {
    dot: 'bg-amber-500',
    badge: 'bg-amber-500/10 text-amber-700 dark:text-amber-300 border-amber-500/30',
    text: 'text-amber-700 dark:text-amber-300',
    accent: 'border-l-amber-500',
  },
  emerald: {
    dot: 'bg-emerald-500',
    badge: 'bg-emerald-500/10 text-emerald-700 dark:text-emerald-300 border-emerald-500/25',
    text: 'text-emerald-700 dark:text-emerald-300',
    accent: 'border-l-emerald-500/80',
  },
  violet: {
    dot: 'bg-violet-500',
    badge: 'bg-violet-500/10 text-violet-700 dark:text-violet-300 border-violet-500/25',
    text: 'text-violet-700 dark:text-violet-300',
    accent: 'border-l-violet-500/80',
  },
  rose: {
    dot: 'bg-rose-500',
    badge: 'bg-rose-500/10 text-rose-700 dark:text-rose-300 border-rose-500/25',
    text: 'text-rose-700 dark:text-rose-300',
    accent: 'border-l-rose-500/80',
  },
  zinc: {
    dot: 'bg-zinc-400',
    badge: 'bg-zinc-500/10 text-zinc-600 dark:text-zinc-300 border-zinc-500/25',
    text: 'text-zinc-600 dark:text-zinc-300',
    accent: 'border-l-zinc-400/70',
  },
}

/**
 * 阶段进度条各格的颜色：同一个色相、由浅到深，一眼能看出「走到哪一格」；与状态条（按状态着色）区分开。
 * 完整类名写成字面量，Tailwind 才扫得到。
 */
export const STAGE_SHADE: Record<'assess' | 'plan' | 'dev' | 'improve', string> = {
  assess: 'bg-violet-300 dark:bg-violet-400/60',
  plan: 'bg-violet-400 dark:bg-violet-400/80',
  dev: 'bg-violet-500',
  improve: 'bg-violet-700 dark:bg-violet-500',
}
