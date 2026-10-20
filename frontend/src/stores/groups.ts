import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { chatErrorMessage } from '../api/conversations'
import { groupsApi, type GroupCandidate, type GroupSummary } from '../api/groups'
import { groupTotals } from '../lib/group'

// 群聊列表（侧栏与群聊首页用）：以服务端为准，任何群的变化靠 conversation_updated 触发防抖后重取。
// 同一时刻只维护一份摘要列表；晚到的旧响应按请求序号丢弃。

export const useGroupStore = defineStore('groups', () => {
  const list = ref<GroupSummary[]>([])
  const loaded = ref(false)
  const loading = ref(false)
  const error = ref('')
  const candidates = ref<GroupCandidate[]>([])
  const candidatesLoading = ref(false)
  /** 群聊首页是否同时列出已归档的群（侧栏永远不列）。 */
  const includeArchived = ref(false)

  let seq = 0
  let timer: ReturnType<typeof setTimeout> | null = null

  const totals = computed(() => groupTotals(list.value))

  async function load() {
    const mine = ++seq
    loading.value = true
    try {
      const res = await groupsApi.list(includeArchived.value)
      if (mine !== seq) return
      list.value = res
      error.value = ''
      loaded.value = true
    } catch (e) {
      if (mine === seq) error.value = chatErrorMessage(e, '群聊列表加载失败')
    } finally {
      if (mine === seq) loading.value = false
    }
  }

  function schedule(delay = 400) {
    if (timer) clearTimeout(timer)
    timer = setTimeout(() => {
      timer = null
      void load()
    }, delay)
  }

  /** SSE conversation_updated：带 group_id 的事件说明某个群变了。 */
  function onServerEvent(payload: { group_id?: string } | null) {
    if (payload?.group_id && loaded.value) schedule()
  }

  async function loadCandidates() {
    candidatesLoading.value = true
    try {
      candidates.value = await groupsApi.candidates()
    } finally {
      candidatesLoading.value = false
    }
  }

  async function create(title: string, workflowIds: string[], projectId?: string): Promise<string> {
    const g = await groupsApi.create({ title, workflow_ids: workflowIds, project_id: projectId || undefined })
    await load()
    return g.id
  }

  function unreadOf(id: string): number {
    return list.value.find(g => g.id === id)?.unread ?? 0
  }

  async function setIncludeArchived(v: boolean) {
    includeArchived.value = v
    await load()
  }

  return { list, includeArchived, setIncludeArchived, loaded, loading, error, candidates, candidatesLoading, totals, load, schedule, onServerEvent, loadCandidates, create, unreadOf }
})
