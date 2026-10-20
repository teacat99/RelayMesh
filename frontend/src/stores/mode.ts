import { defineStore } from 'pinia'
import { computed, reactive } from 'vue'
import { EXEC_HOME, RELAY_HOME, isRememberable, modeOfPath, type AppMode } from '../lib/mode'
import { useExecRunsStore } from './execRuns'
import { useTerminalStore } from './terminal'

/**
 * 工作模式（RelayMesh / ExecMesh）。
 *
 * 两个模式各自记住上次停留的位置：从 ExecMesh 切回 RelayMesh 回到首页（所选会话由会话状态自己保存），
 * 再切到 ExecMesh 时回到离开时的终端或线程，而不是总回到起点。
 */
export const useModeStore = defineStore('mode', () => {
  const terminal = useTerminalStore()
  const execRuns = useExecRunsStore()

  const last = reactive<Record<AppMode, string>>({ relay: RELAY_HOME, exec: EXEC_HOME })

  /** 路由切换后调用，记下该模式的停留位置。 */
  function remember(fullPath: string) {
    if (!isRememberable(fullPath)) return
    last[modeOfPath(fullPath)] = fullPath
  }

  function targetOf(mode: AppMode): string {
    return last[mode]
  }

  /** ExecMesh 里正在运行的模型命令数（不含待批准）；人工会话不计入，否则只要开着终端就一直亮。 */
  const modelRunning = computed(() => execRuns.running)

  /** 切换器上 ExecMesh 一侧的徽标：待你处理的事项数（待启用主机 + 待批准的模型命令）。 */
  const attention = computed(() => terminal.pending.length + execRuns.awaiting)

  return { last, remember, targetOf, modelRunning, attention }
})
