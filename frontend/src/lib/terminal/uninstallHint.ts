// 删除主机时的「设备端怎么办」：能同时卸载就提供勾选项，不能就如实说明原因并给出在设备上手动卸载的命令。
// 纯函数，便于单元测试。

import type { HostView, UninstallOutcome } from '../../api/terminal'

export function isWindowsHost(os: string): boolean {
  return /windows/i.test(os)
}

/** 在设备上手动卸载 Agent 的命令，以及它适用的前提。 */
export function manualUninstallCommand(os: string): { command: string; note: string } {
  if (isWindowsHost(os)) {
    return {
      command: 'relaymesh-agent uninstall --purge',
      note: '在「以管理员身份运行」的 PowerShell 中执行；当初用 --user 安装的，加上 --user（以最高权限安装的仍需管理员 PowerShell）。',
    }
  }
  return {
    command: 'sudo relaymesh-agent uninstall --purge',
    note: '当初用 --user 安装的，去掉 sudo 并加上 --user；已经是 root 的（没有 sudo 的容器 / 云主机）直接去掉 sudo。',
  }
}

export interface DeleteAgentNote {
  /** 可以提供「同时卸载设备端」勾选项。 */
  canUninstall: boolean
  /** 不能同时卸载时的原因（canUninstall 为 true 时为空）。 */
  reason: string
  /** 设备上手动卸载的命令；临时 Agent 没有安装过任何东西，为空。 */
  command: string
  note: string
}

/** 删除一台 Agent 主机时，对话框里关于设备端的说明。 */
export function deleteAgentNote(h: Pick<HostView, 'online' | 'os' | 'can_uninstall' | 'temporary'>): DeleteAgentNote {
  if (h.temporary) {
    return { canUninstall: false, reason: '这是临时运行的 Agent，没有安装过任何东西——在设备上关闭它的窗口或按 Ctrl+C 即可退出。', command: '', note: '' }
  }
  const manual = manualUninstallCommand(h.os)
  if (h.online && h.can_uninstall) return { canUninstall: true, reason: '', ...manual }
  const reason = h.online
    ? '这台设备上的 Agent 不支持远程卸载（版本较旧、不是以常驻服务运行，或所在系统暂不支持）。'
    : '这台设备当前离线，无法远程卸载。'
  return { canUninstall: false, reason, ...manual }
}

export interface OutcomeNotice {
  level: 'success' | 'warning'
  title: string
  description: string
}

/**
 * 删除完成后，按服务端返回的卸载结果给出的提示。没有请求卸载（outcome 为空）时返回 null，沿用默认的「已删除」。
 * 任何没有真正下发卸载的结果都是 warning 并带上手动命令——管理员不能被「已删除」误导成设备上已经清理干净。
 */
export function uninstallOutcomeNotice(outcome: UninstallOutcome | undefined, os: string): OutcomeNotice | null {
  if (!outcome) return null
  const { command } = manualUninstallCommand(os)
  const manual = `设备上的 Agent 仍在运行，会继续尝试连接。需要时在设备上执行：${command}`
  switch (outcome) {
    case 'requested':
      return {
        level: 'success',
        title: '已删除，并通知设备卸载 Agent',
        description: '卸载在设备上异步完成，几秒内 Agent 会停止；终端录像与命令输出仍保留在设备的数据目录里。',
      }
    case 'offline':
      return { level: 'warning', title: '已删除，但设备离线，没能卸载设备端', description: manual }
    case 'unsupported':
      return { level: 'warning', title: '已删除，但这台设备的 Agent 不支持远程卸载', description: manual }
    default:
      return { level: 'warning', title: '已删除，但卸载请求没能发出', description: manual }
  }
}
