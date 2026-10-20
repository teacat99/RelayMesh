import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { projectErrorMessage, projectsApi, type Project, type ProjectDetail, type ProjectSuggestion } from '../api/projects'
import { invalidateOwnerProjects } from '../lib/docNavClient'
import { useAgentStateStore } from './agentState'

// 项目（#114）：列表、建议、当前项目详情。以服务端为准；改动后各页面直接调 reload*。
// 晚到的旧响应按请求序号丢弃（和 groups 同一套做法）。
// 项目数据重新加载就意味着可能变过（归入 / 移出目录、归档），顺手作废「工作流属于哪个项目」的缓存（#131，文档跳转据此选落点）。

export const useProjectStore = defineStore('projects', () => {
  const list = ref<Project[]>([])
  const suggestions = ref<ProjectSuggestion[]>([])
  const detail = ref<ProjectDetail | null>(null)
  const loading = ref(false)
  const detailLoading = ref(false)
  const error = ref('')
  const loaded = ref(false)
  let listSeq = 0
  let detailSeq = 0

  const identities = computed(() => useAgentStateStore().identities)
  /** 身份编号 → 显示名；认不出的给「身份#N」。 */
  function identityLabel(id: number | undefined | null): string {
    if (!id) return '未指定'
    const it = identities.value.find(i => i.id === id)
    return it?.label || `身份#${id}`
  }

  async function loadList() {
    const mine = ++listSeq
    invalidateOwnerProjects()
    loading.value = true
    try {
      const [l, s] = await Promise.all([projectsApi.list(), projectsApi.suggestions()])
      if (mine !== listSeq) return
      list.value = l
      suggestions.value = s
      error.value = ''
      loaded.value = true
    } catch (e) {
      if (mine === listSeq) error.value = projectErrorMessage(e, '项目列表加载失败')
    } finally {
      if (mine === listSeq) loading.value = false
    }
  }

  async function loadDetail(id: number) {
    const mine = ++detailSeq
    invalidateOwnerProjects()
    detailLoading.value = true
    try {
      const d = await projectsApi.get(id)
      if (mine !== detailSeq) return
      detail.value = d
      error.value = ''
    } catch (e) {
      if (mine === detailSeq) {
        detail.value = null
        error.value = projectErrorMessage(e, '项目加载失败')
      }
    } finally {
      if (mine === detailSeq) detailLoading.value = false
    }
  }

  function clearDetail() {
    detailSeq++
    detail.value = null
  }

  return { list, suggestions, detail, loading, detailLoading, error, loaded, identities, identityLabel, loadList, loadDetail, clearDetail }
})
