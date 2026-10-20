import { defineStore } from 'pinia'
import { ref } from 'vue'
import { terminalApi, toApiError, type LauncherRunPlan, type SessionLaunchersView } from '../api/terminal'
import { sessionLaunchersPollDelay } from '../lib/terminal/sessionLaunchers'
import { useTerminalStore } from './terminal'

// 会话页底部「运行」按钮与启动器「本项目」标签的数据（#140）：每个工作流一份「这个会话对应的续聊预设」视图。
// 视图由服务端按「主机 + 目录 + 跟的是这个会话」选出、并按提交运行时同一份检查算好此刻能不能运行；
// 这里只负责读它、按节奏刷新、提交运行。按钮（输入区）与启动器（页头弹窗）共用同一份，所以放在 store 里。
// 旧版 Hub 没有这个接口（404）：记下来，按钮不再出现、也不再请求。
export const useSessionLaunchersStore = defineStore('sessionLaunchers', () => {
  const views = ref<Record<string, SessionLaunchersView | null>>({})
  /** 工作流 → 正在提交运行的预设编号（提交要先解锁、再等服务端接收，期间不允许再点）。 */
  const submitting = ref<Record<string, string>>({})
  const unsupported = ref(false)

  const seq: Record<string, number> = {}

  function setView(wf: string, v: SessionLaunchersView | null) {
    views.value = { ...views.value, [wf]: v }
  }

  /** 读一次视图。过期的响应（又请求过一次）会被丢掉；网络抖动保留上一次的结果。 */
  async function refresh(wf: string): Promise<void> {
    if (unsupported.value || !wf) return
    const n = (seq[wf] = (seq[wf] ?? 0) + 1)
    try {
      const r = await terminalApi.sessionLaunchers(wf)
      if (seq[wf] === n) setView(wf, r.session)
    } catch (e) {
      // 404：服务端还不认识这个接口，之后不用再问。
      if (toApiError(e).status === 404) unsupported.value = true
    }
  }

  // 定时刷新：按钮在输入区常驻，启动器开着时也要新鲜。有运行在进行时勤一点，否则慢一点；页面在后台时不请求。
  const pollUsers: Record<string, number> = {}
  const pollTimers: Record<string, ReturnType<typeof setTimeout>> = {}

  function schedule(wf: string) {
    if (!pollUsers[wf]) return
    pollTimers[wf] = setTimeout(async () => {
      delete pollTimers[wf]
      if (!pollUsers[wf]) return
      if (!document.hidden) await refresh(wf)
      schedule(wf)
    }, sessionLaunchersPollDelay(views.value[wf]))
  }

  /** 开始定时读取这个工作流的视图（可重入，按次数计）。 */
  function startPolling(wf: string) {
    if (!wf) return
    pollUsers[wf] = (pollUsers[wf] ?? 0) + 1
    if (pollUsers[wf] > 1) return
    void refresh(wf)
    schedule(wf)
  }

  function stopPolling(wf: string) {
    if (!pollUsers[wf]) return
    pollUsers[wf] -= 1
    if (pollUsers[wf] > 0) return
    delete pollUsers[wf]
    if (pollTimers[wf]) clearTimeout(pollTimers[wf])
    delete pollTimers[wf]
  }

  /**
   * 运行一个对应的续聊预设：服务端直接提交（需要刚刚解锁——它会让主机运行命令），浏览器什么都不用执行。
   * 用户取消解锁时抛 UnlockCancelled；前置条件不满足（主机离线、对话还在运行、上一次还没结束……）抛带可读原因的错误。
   */
  async function run(wf: string, presetId: string): Promise<LauncherRunPlan> {
    submitting.value = { ...submitting.value, [wf]: presetId }
    try {
      const plan = await useTerminalStore().runLauncher(presetId)
      await refresh(wf)
      // 刚提交的运行立刻让按钮收起来：读到的视图里还没有它（极短的竞态）时，先按计划把这个预设标成「忙」。
      const v = views.value[wf]
      const p = v?.presets.find(x => x.id === presetId)
      if (v && p && p.last_run?.run_id !== plan.run_id) {
        const active = plan.run_state === 'running' || plan.run_state === 'awaiting_approval'
        setView(wf, {
          ...v,
          presets: v.presets.map(x =>
            x.id === presetId
              ? { ...x, can_run: false, busy: active, last_run: { run_id: plan.run_id, state: plan.run_state, active, created_at: new Date().toISOString() } }
              : x,
          ),
        })
      }
      return plan
    } finally {
      const { [wf]: _done, ...rest } = submitting.value
      submitting.value = rest
    }
  }

  return { views, submitting, unsupported, refresh, startPolling, stopPolling, run }
})
