import { defineStore } from 'pinia'
import { ref } from 'vue'
import { asyncModeApi, asyncModeErrorMessage, type AuthProfileDraft } from '../api/asyncMode'
import { workItemsApi } from '../api/workItems'
import { registerConnectHook } from '../lib/connectHooks'
import { GATE_SHELVE_PROGRESS } from '../lib/wrapupGate'
import type { AsyncModeView, AuthProfile, AuthProfileLimits } from '../api/types'
import { useSessionStore } from './session'

// 授权档位（全局）与异步对话开关的写操作。
// 某个工作流的异步状态本身跟着收件箱状态一起拉取（session 仓库的 asyncMode），这里只负责：
//   - 档位表：设置页与工具栏的档位选择共用一份；
//   - 开关 / 选档位：提交后把服务端返回的状态直接写回 session 仓库，界面不必等下一次轮询。
// 服务端任何变化都会广播 workflow_mode_updated / auth_profiles_updated，其它标签页据此校准。

const DEFAULT_LIMITS: AuthProfileLimits = { max: 12, name_max: 24, rules_max: 240 }

export const useAsyncModeStore = defineStore('asyncMode', () => {
  const profiles = ref<AuthProfile[]>([])
  const defaultId = ref(0)
  const limits = ref<AuthProfileLimits>({ ...DEFAULT_LIMITS })
  const loaded = ref(false)
  const loading = ref(false)
  const error = ref('')
  const busy = ref(false)

  let loadSeq = 0

  async function loadProfiles(force = false) {
    if (loading.value) return
    if (loaded.value && !force) return
    const mine = ++loadSeq
    loading.value = true
    try {
      const res = await asyncModeApi.listProfiles()
      if (mine !== loadSeq) return
      profiles.value = res.items
      defaultId.value = res.default_id
      limits.value = res.limits
      error.value = ''
      loaded.value = true
    } catch (err) {
      if (mine !== loadSeq) return
      error.value = asyncModeErrorMessage(err, '无法加载授权档位')
    } finally {
      if (mine === loadSeq) loading.value = false
    }
  }

  async function guarded<T>(fn: () => Promise<T>): Promise<T> {
    if (busy.value) throw new Error('正在提交，请稍候')
    busy.value = true
    try {
      return await fn()
    } finally {
      busy.value = false
    }
  }

  function applyView(view: AsyncModeView) {
    useSessionStore().setAsyncMode(view)
  }

  /** 开关异步对话；开启时没有 hook 事件会抛出带原因的错误（409），由调用方 toast。 */
  async function setEnabled(workflowId: string, enabled: boolean): Promise<AsyncModeView> {
    return guarded(async () => {
      const view = await asyncModeApi.update(workflowId, { enabled })
      applyView(view)
      return view
    })
  }

  /** 严格收尾门开关（#232 D2）：默认开；关掉后回复窗口回到老口径。 */
  async function setStrictWrapup(workflowId: string, on: boolean): Promise<AsyncModeView> {
    return guarded(async () => {
      const view = await asyncModeApi.update(workflowId, { strict_wrapup: on })
      applyView(view)
      return view
    })
  }

  /** 「本次放行」：这一轮窗口不再被未完成任务挡住；失败（没有窗口 / 门已关）由调用方 toast 服务端给的原因。 */
  async function releaseGate(workflowId: string): Promise<AsyncModeView> {
    return guarded(async () => {
      const view = await asyncModeApi.releaseGate(workflowId)
      applyView(view)
      return view
    })
  }

  /**
   * 在收尾门清单里把一个任务置「暂缓」（你替智能体放下它）：进展里写明是你放下的、等你另行安排，
   * 这样服务端「暂缓要写缘由」的提醒不会再去打扰智能体。置完后重取本会话状态，清单立刻少一项。
   */
  async function shelveGateTask(workflowId: string, taskId: number): Promise<void> {
    return guarded(async () => {
      await workItemsApi.update(taskId, { state: 'shelved', progress: GATE_SHELVE_PROGRESS })
      await useSessionStore().fetchQueuedFeedbacks(workflowId, { urgent: true })
    })
  }

  async function selectProfile(workflowId: string, profileId: number): Promise<AsyncModeView> {
    return guarded(async () => {
      const view = await asyncModeApi.update(workflowId, { profile_id: profileId })
      applyView(view)
      return view
    })
  }

  async function createProfile(draft: AuthProfileDraft): Promise<AuthProfile> {
    return guarded(async () => {
      const item = await asyncModeApi.createProfile(draft)
      await loadProfiles(true)
      return item
    })
  }

  async function updateProfile(id: number, draft: AuthProfileDraft): Promise<AuthProfile> {
    return guarded(async () => {
      const item = await asyncModeApi.updateProfile(id, draft)
      await loadProfiles(true)
      return item
    })
  }

  async function setDefault(id: number): Promise<AuthProfile> {
    return guarded(async () => {
      const item = await asyncModeApi.setDefaultProfile(id)
      await loadProfiles(true)
      return item
    })
  }

  async function removeProfile(id: number): Promise<void> {
    return guarded(async () => {
      await asyncModeApi.deleteProfile(id)
      await loadProfiles(true)
    })
  }

  /** auth_profiles_updated：档位表变了，已经加载过的才重取（没打开过的页面不必为此发请求）。 */
  function onProfilesEvent() {
    if (loaded.value) void loadProfiles(true)
  }
  // 实时流（首次或重新）连上：连上之前 / 断线期间的 auth_profiles_updated 没人听见；已加载过的档位表重取一次（#243）。
  // 惰性仓库：用到了才会被创建，登记即「有旧数据要校准」，没用到的页面不会为此多拉代码块（lib/connectHooks.ts）
  registerConnectHook('asyncMode', onProfilesEvent)

  return {
    profiles,
    defaultId,
    limits,
    loaded,
    loading,
    error,
    busy,
    loadProfiles,
    setEnabled,
    setStrictWrapup,
    releaseGate,
    shelveGateTask,
    selectProfile,
    createProfile,
    updateProfile,
    setDefault,
    removeProfile,
    onProfilesEvent,
  }
})
