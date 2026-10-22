import { defineStore } from 'pinia'
import { ref } from 'vue'
import { projectDocsApi, type DocDetail, type ProjectDoc, type DocWorkflowStat } from '../api/projectDocs'
import { confirmsApi } from '../api/confirms'

/** 后台刷新（服务端事件触发、没有人在等结果）失败时什么也不做：失败不该变成未处理的 Promise 拒绝。 */
const ignoreBackgroundError = () => {}

export const useProjectDocStore = defineStore('projectDocs', () => {
  const items = ref<ProjectDoc[]>([])
  const workflows = ref<DocWorkflowStat[]>([])
  const detail = ref<DocDetail | null>(null)
  const loading = ref(false)
  let workflowId = ''
  // 每打开一篇新文档（非 silent 的 loadDetail）加一：晚到的旧响应不能盖掉后打开的那篇
  // （切得快会出现地址是乙、内容是甲）；silent 的后台校准只在期间没切走时才生效。
  let navSeq = 0
  let debounce: ReturnType<typeof setTimeout> | null = null
  let confirmDebounce: ReturnType<typeof setTimeout> | null = null

  async function loadList(wf: string, params?: Record<string, string>) {
    loading.value = true
    try {
      workflowId = wf
      const res = await projectDocsApi.list(wf, params)
      items.value = res.items
      workflows.value = res.workflows ?? []
    } finally {
      loading.value = false
    }
  }

  /** silent=true 用于后台校准：不置 loading（否则正文会闪一下骨架）。 */
  async function loadDetail(wf: string, refKey: string, silent = false) {
    const nav = silent ? navSeq : ++navSeq
    if (!silent) loading.value = true
    try {
      const d = await projectDocsApi.get(wf, refKey)
      if (nav !== navSeq) return
      detail.value = d
    } finally {
      if (!silent && nav === navSeq) loading.value = false
    }
  }

  /**
   * 服务端文档有变化：目录跟着「正在看的工作流」（loadList 设置的那个）刷新，打开着的文档跟着「它自己的工作流」刷新——
   * 项目里的文档页没有按工作流的目录，打开的文档也可能来自项目里别的工作流，两者不一定是同一个。
   */
  function onServerEvent(payload?: { workflow_id?: string }) {
    const listWf = workflowId
    const openWf = detail.value?.doc.workflow_id || ''
    if (!listWf && !openWf) return
    const hit = (wf: string) => !!wf && (!payload?.workflow_id || payload.workflow_id === wf)
    if (!hit(listWf) && !hit(openWf)) return
    if (debounce) clearTimeout(debounce)
    debounce = setTimeout(() => {
      // 后台校准：网络瞬断时失败不打扰（不留未处理的 Promise 拒绝），下一次服务端事件会再刷新
      if (hit(listWf)) loadList(listWf).catch(ignoreBackgroundError)
      const cur = detail.value?.doc
      if (cur && hit(cur.workflow_id)) loadDetail(cur.workflow_id, cur.path, true).catch(ignoreBackgroundError)
    }, 400)
  }

  /**
   * 确认项有变化（有人作答 / 忽略、文档保存同步出新的决策项）：只需要重取当前打开的文档，
   * 决策卡的状态与答复随详情一起来；没打开文档、或事件属于别的工作流时什么也不做。
   */
  function onConfirmEvent(payload?: { workflow_id?: string }) {
    const open = detail.value?.doc
    if (!open) return
    if (payload?.workflow_id && payload.workflow_id !== open.workflow_id) return
    if (confirmDebounce) clearTimeout(confirmDebounce)
    confirmDebounce = setTimeout(() => {
      confirmDebounce = null
      const cur = detail.value?.doc
      if (cur) loadDetail(cur.workflow_id, cur.path, true).catch(ignoreBackgroundError)
    }, 250)
  }

  /** 在文档里给一张决策卡作答（对应确认项 Qn）；成功后立刻重取当前文档，卡片马上变成「已作答」。 */
  async function answerDecision(wf: string, ref: string, options: string[], note?: string, custom?: string) {
    const item = await confirmsApi.answer(wf, ref, options, note, custom)
    await refreshOpenDoc(wf)
    return item
  }

  /** 一键采用推荐（整批原子）；只传 L0 / L1 且有推荐选项的决策卡的编号。 */
  async function adoptDecisions(wf: string, refs: string[]) {
    const res = await confirmsApi.answerBatch(wf, refs.map(ref => ({ ref })), true)
    await refreshOpenDoc(wf)
    return res.items
  }

  async function refreshOpenDoc(wf: string) {
    const cur = detail.value?.doc
    if (cur && wf === cur.workflow_id) await loadDetail(wf, cur.path, true)
  }

  /** 仅使用服务端聚合统计；无统计时不显示角标（不用当前列表长度冒充全局计数）。 */
  function docCount(wf: string): number | null {
    const stat = workflows.value.find(w => w.workflow_id === wf)
    if (!stat || stat.total <= 0) return null
    return stat.total
  }

  function clearDetail() {
    navSeq++ // 还在路上的加载不能把刚清掉的文档又放回来
    loading.value = false
    detail.value = null
  }

  return {
    items,
    workflows,
    detail,
    loading,
    loadList,
    loadDetail,
    onServerEvent,
    onConfirmEvent,
    answerDecision,
    adoptDecisions,
    docCount,
    clearDetail,
  }
})
