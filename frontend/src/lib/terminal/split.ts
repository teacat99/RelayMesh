// 桌面并排（左右两个窗格）的纯逻辑。状态保存在 stores/exec.ts，视图只负责渲染。

export const SPLIT_RATIO_MIN = 0.25
export const SPLIT_RATIO_MAX = 0.75
export const SPLIT_RATIO_DEFAULT = 0.5

/** 左窗格占比：非数字回默认值，夹到 25%~75%（再窄的终端没法用）。 */
export function clampSplitRatio(n: unknown): number {
  const v = typeof n === 'number' ? n : typeof n === 'string' && n.trim() !== '' ? Number(n) : NaN
  if (!Number.isFinite(v)) return SPLIT_RATIO_DEFAULT
  return Math.min(SPLIT_RATIO_MAX, Math.max(SPLIT_RATIO_MIN, v))
}

/** 由指针位置换算占比（container 为分隔区域的左边界与宽度）。 */
export function ratioFromPointer(clientX: number, left: number, width: number): number {
  if (!(width > 0)) return SPLIT_RATIO_DEFAULT
  return clampSplitRatio((clientX - left) / width)
}

export interface SplitTab {
  key: string
  replay?: boolean
}

/**
 * 打开并排时，右窗格选谁：最近使用过的另一个终端标签（live 集合顺序），
 * 其次是任意另一个终端标签。回放标签不参与并排。没有可选的就返回空串。
 */
export function pickSecondary(tabs: readonly SplitTab[], primary: string, mru: readonly string[]): string {
  const eligible = tabs.filter(t => !t.replay && t.key !== primary)
  if (eligible.length === 0) return ''
  const ok = new Set(eligible.map(t => t.key))
  for (const k of mru) if (ok.has(k)) return k
  return eligible[eligible.length - 1].key
}

export interface SplitState {
  on: boolean
  secondary: string
}

export const SPLIT_OFF: SplitState = { on: false, secondary: '' }

/**
 * 主窗格改变之后，并排应该怎么调整。terminalKeys 是当前所有「终端」标签（不含回放标签）。
 * - 右窗格的标签已经不存在：退出并排；
 * - 点中的正是右窗格的会话：两边互换（原来的主窗格去右边），不会出现同一个会话占两个窗格；
 * - 其余情况原样保留。
 */
export function afterPrimaryChange(state: SplitState, prevPrimary: string, nextPrimary: string, terminalKeys: ReadonlySet<string>): SplitState {
  if (!state.on) return state
  if (!state.secondary || !terminalKeys.has(state.secondary)) return SPLIT_OFF
  if (nextPrimary !== state.secondary) return state
  if (prevPrimary && prevPrimary !== nextPrimary && terminalKeys.has(prevPrimary)) return { on: true, secondary: prevPrimary }
  return SPLIT_OFF
}
