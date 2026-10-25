import { defineStore } from 'pinia'
import { ref } from 'vue'
import { registerConnectHook } from '../lib/connectHooks'
import {
  promptsErrorMessage,
  workflowPromptsApi,
  type WakeTemplatePatch,
  type WorkflowPromptsView,
} from '../api/workflowPrompts'

// 会话级常驻提示词（#232）：按工作流缓存服务端的设置，输入区的「常驻」小标签与弹窗共用。
// 服务端任何变化（你在另一个标签页改了、或智能体第一次读到新版本）都会广播 workflow_prompts_updated，
// 这里只对「已经加载过」的工作流重取，没打开过的不为此发请求。

export const useWorkflowPromptsStore = defineStore('workflowPrompts', () => {
  const byWorkflow = ref<Record<string, WorkflowPromptsView>>({})
  const errors = ref<Record<string, string>>({})
  const busy = ref(false)
  /**
   * 常驻规则弹窗此刻开着的是哪个工作流（空串 = 没开）。弹窗只挂在输入区的小标签里，
   * 手机页头的「⋯」菜单（小标签在工具条最右边、要横向滑才看得到）通过这里打开同一个弹窗。
   */
  const dialogFor = ref('')

  function openDialog(workflowId: string) {
    if (workflowId) dialogFor.value = workflowId
  }

  function closeDialog() {
    dialogFor.value = ''
  }

  /** 每个工作流自己的请求序号：慢响应不能覆盖更新的结果（保存 / 回滚也会让在途的读取作废）。 */
  const seqs = new Map<string, number>()
  /** 同一个工作流同时只有一个在途的读取：小标签与弹窗一起挂载时不会发两次请求。 */
  const pending = new Map<string, Promise<void>>()

  function view(workflowId: string): WorkflowPromptsView | null {
    return byWorkflow.value[workflowId] ?? null
  }

  function bump(workflowId: string): number {
    const next = (seqs.get(workflowId) ?? 0) + 1
    seqs.set(workflowId, next)
    return next
  }

  function apply(v: WorkflowPromptsView) {
    byWorkflow.value = { ...byWorkflow.value, [v.workflow_id]: v }
    if (errors.value[v.workflow_id]) {
      const next = { ...errors.value }
      delete next[v.workflow_id]
      errors.value = next
    }
  }

  function load(workflowId: string, force = false): Promise<void> {
    if (!workflowId) return Promise.resolve()
    if (!force) {
      if (byWorkflow.value[workflowId]) return Promise.resolve()
      const inflight = pending.get(workflowId)
      if (inflight) return inflight
    }
    const mine = bump(workflowId)
    const p: Promise<void> = (async () => {
      try {
        const v = await workflowPromptsApi.get(workflowId)
        if (seqs.get(workflowId) !== mine) return
        apply(v)
      } catch (err) {
        if (seqs.get(workflowId) !== mine) return
        errors.value = { ...errors.value, [workflowId]: promptsErrorMessage(err, '无法读取常驻提示词') }
      }
    })().finally(() => {
      if (pending.get(workflowId) === p) pending.delete(workflowId)
    })
    pending.set(workflowId, p)
    return p
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

  /** 保存（空串 = 清除）。成功后直接用服务端返回的状态，不等下一次广播。 */
  async function save(workflowId: string, text: string): Promise<{ changed: boolean; view: WorkflowPromptsView }> {
    return guarded(async () => {
      const res = await workflowPromptsApi.save(workflowId, text)
      bump(workflowId)
      apply(res.prompts)
      return { changed: res.changed, view: res.prompts }
    })
  }

  /** 保存这个会话的唤醒词模板（续聊 / 新开对话；空串 = 清除）。不涨常驻规则的版本。 */
  async function saveWake(workflowId: string, patch: WakeTemplatePatch): Promise<{ changed: boolean; view: WorkflowPromptsView }> {
    return guarded(async () => {
      const res = await workflowPromptsApi.saveWake(workflowId, patch)
      bump(workflowId)
      apply(res.prompts)
      return { changed: res.changed, view: res.prompts }
    })
  }

  async function restore(workflowId: string, revisionId: number): Promise<{ changed: boolean; view: WorkflowPromptsView }> {
    return guarded(async () => {
      const res = await workflowPromptsApi.restore(workflowId, revisionId)
      bump(workflowId)
      apply(res.prompts)
      return { changed: res.changed, view: res.prompts }
    })
  }

  /** workflow_prompts_updated：只对已经加载过的工作流重取。 */
  function onEvent(workflowId?: string) {
    if (workflowId && byWorkflow.value[workflowId]) void load(workflowId, true)
  }

  /** 实时流（首次或重新）连上：连上之前 / 断线期间的 workflow_prompts_updated 没人听见，已加载过的工作流各重取一次（#243）。 */
  function onConnected() {
    for (const id of Object.keys(byWorkflow.value)) void load(id, true)
  }
  // 惰性仓库：用到了才会被创建，所以「已登记」就等于「有旧数据要校准」；没用到的页面不会为此多拉代码块（lib/connectHooks.ts）
  registerConnectHook('workflowPrompts', onConnected)

  return { byWorkflow, errors, busy, dialogFor, openDialog, closeDialog, view, load, save, saveWake, restore, onEvent }
})
