// 终端限高（按行数）：每个会话在主机上的记录至少保留最近这么多行输出，更早的整段滚动丢弃。
// 纯函数：设置页的输入解析、文案与「是否需要二次确认」的判断。

import { HOLDER_RESTART_TIP } from './holderRestart'

/** 设置里的「常用行数」快捷值：只是常见的量级，不是推荐值（推荐值要结合各工具的实际输出体量另行评估）。 */
export const RECORD_LINES_PRESETS = [20_000, 50_000, 100_000, 200_000, 500_000] as const
/** 第一次设置时填入的行数（暂定）。 */
export const RECORD_LINES_DEFAULT = 100_000

/** 千分位分隔的行数文字（100000 → 100,000）。 */
export function fmtLines(n: number): string {
  return Math.trunc(n).toLocaleString('en-US')
}

/** 解析输入框里的行数：允许千分位逗号与空格，必须是落在范围内的整数。 */
export function parseRecordLines(
  raw: string,
  limits: { min: number; max: number },
): { ok: true; value: number } | { ok: false; message: string } {
  const text = raw.replace(/[,\s]/g, '')
  if (!/^\d+$/.test(text)) return { ok: false, message: '请输入整数' }
  const value = Number(text)
  if (!Number.isSafeInteger(value) || value < limits.min || value > limits.max) {
    return { ok: false, message: `应在 ${fmtLines(limits.min)}–${fmtLines(limits.max)} 之间` }
  }
  return { ok: true, value }
}

/**
 * 这次修改会不会让主机丢弃更多内容：从「不限制」改成有上限，或把上限调小，都会立刻丢掉运行中会话里更早的记录，
 * 而且不可恢复，需要先让用户确认；放宽 / 取消上限不会丢任何东西。
 */
export function tightensRecordLines(current: number, next: number): boolean {
  if (next <= 0) return false
  return current <= 0 || next < current
}

export interface RecordLinesCoverageView {
  online: number
  unsupported: number
}

/** 设了上限之后，对在线主机的覆盖情况说明；没有可说的内容时返回 null。 */
export function coverageText(c: RecordLinesCoverageView | undefined | null): { text: string; warn: boolean } | null {
  if (!c) return null
  if (c.online === 0) return { text: '目前没有在线主机；各主机上线时会收到这个设置。', warn: false }
  if (c.unsupported <= 0) return { text: `已下发到 ${c.online} 台在线主机；离线的主机上线时生效。`, warn: false }
  const supported = c.online - c.unsupported
  return {
    text:
      `在线的 ${c.online} 台主机里，${c.unsupported} 台的会话守护进程还不支持按行数限高` +
      (supported > 0 ? `（另外 ${supported} 台已生效）` : '') +
      '：升级 Agent 后，需要在该主机上重启会话守护进程（relaymesh-agent holder restart）才会生效，期间它们仍只按字节配额滚动。' +
      HOLDER_RESTART_TIP,
    warn: true,
  }
}
