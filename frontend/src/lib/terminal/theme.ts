// xterm 配色：跟随应用明暗主题（背景与页面底色一致，前景 / ANSI 色按各自背景调过对比度）。

import type { ITheme } from '@xterm/xterm'

export const TERM_FONT_FAMILY =
  'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", "DejaVu Sans Mono", "Noto Sans Mono CJK SC", "Microsoft YaHei UI", monospace'

const dark: ITheme = {
  background: '#111113',
  foreground: '#d4d4d8',
  cursor: '#e4e4e7',
  cursorAccent: '#111113',
  selectionBackground: 'rgba(161, 161, 170, 0.35)',
  selectionInactiveBackground: 'rgba(161, 161, 170, 0.2)',
  black: '#3f3f46',
  red: '#f87171',
  green: '#4ade80',
  yellow: '#facc15',
  blue: '#60a5fa',
  magenta: '#e879f9',
  cyan: '#22d3ee',
  white: '#d4d4d8',
  brightBlack: '#71717a',
  brightRed: '#fca5a5',
  brightGreen: '#86efac',
  brightYellow: '#fde047',
  brightBlue: '#93c5fd',
  brightMagenta: '#f0abfc',
  brightCyan: '#67e8f9',
  brightWhite: '#fafafa',
}

const light: ITheme = {
  background: '#ffffff',
  foreground: '#27272a',
  cursor: '#27272a',
  cursorAccent: '#ffffff',
  selectionBackground: 'rgba(82, 82, 91, 0.25)',
  selectionInactiveBackground: 'rgba(82, 82, 91, 0.15)',
  black: '#27272a',
  red: '#b91c1c',
  green: '#15803d',
  yellow: '#a16207',
  blue: '#1d4ed8',
  magenta: '#a21caf',
  cyan: '#0e7490',
  white: '#52525b',
  brightBlack: '#71717a',
  brightRed: '#dc2626',
  brightGreen: '#16a34a',
  brightYellow: '#b45309',
  brightBlue: '#2563eb',
  brightMagenta: '#c026d3',
  brightCyan: '#0891b2',
  brightWhite: '#18181b',
}

export function terminalTheme(isDark: boolean): ITheme {
  return { ...(isDark ? dark : light) }
}
