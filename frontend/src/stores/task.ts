import { defineStore } from 'pinia'
import { ref } from 'vue'
import type { TaskSummary, TaskDetail, Report, TaskStage, Feedback } from '../api/types'
import { tasksApi } from '../api/client'
import { TASK_PAGE_SIZE, collectAllPages } from '../lib/taskPaging'

export const useTaskStore = defineStore('task', () => {
  const tasks = ref<TaskSummary[]>([])
  const currentTask = ref<TaskDetail | null>(null)
  const reports = ref<Report[]>([])
  const feedbacks = ref<Feedback[]>([])
  const activeTab = ref<'dashboard' | 'conversation'>('dashboard')
  const loading = ref(false)

  let fetchTasksSeq = 0
  let refreshTimer: any = null

  function debouncedRefresh(taskId?: string) {
    if (refreshTimer) clearTimeout(refreshTimer)
    refreshTimer = setTimeout(() => {
      fetchTasks()
      const targetId = taskId || currentTask.value?.task_id
      if (targetId) {
        fetchTaskDetail(targetId)
      }
    }, 120)
  }

  /**
   * 实时流（首次或重新）连上：页面载入到连上之间、断线期间的 task_update / task_feedback / task_ack 没人听见也不会补发，
   * 把列表与打开着的任务重取一次（#243）。这个仓库还没被用过（没取过列表、也没打开过任务）就没有旧数据要校准。
   */
  function onConnected() {
    if (fetchTasksSeq === 0 && !currentTask.value) return
    debouncedRefresh()
  }

  async function fetchTasks(updatesOnly = false) {
    const reqSeq = ++fetchTasksSeq
    try {
      loading.value = true
      // 服务端一页最多 100 条、默认 20 条：以前只取第一页，工单一多（超过 20 个）侧栏就只显示其中一部分（#250）。
      // 这里一页页取完再一次写入；取的途中已经有更新的取数发出，就放弃这一轮（isStale），旧结果不会盖掉新的。
      const all = await collectAllPages<TaskSummary>(
        cursor => tasksApi.list({ updates_only: updatesOnly, limit: TASK_PAGE_SIZE, cursor: cursor || undefined }),
        { isStale: () => reqSeq !== fetchTasksSeq },
      )
      if (all === null) return
      tasks.value = all
    } catch (err) {
      console.error('Failed to list tasks', err)
    } finally {
      loading.value = false
    }
  }

  async function fetchTaskDetail(taskId: string) {
    try {
      loading.value = true
      const [taskRes, repRes, fbRes] = await Promise.all([
        tasksApi.get(taskId),
        tasksApi.getReports(taskId, { limit: 100 }),
        tasksApi.getFeedbacks(taskId, { limit: 100 })
      ])
      currentTask.value = taskRes.task
      reports.value = repRes.reports
      feedbacks.value = fbRes.feedbacks || []
    } catch (err) {
      console.error('Failed to fetch task detail', err)
    } finally {
      loading.value = false
    }
  }

  async function createTask(data: { task_id?: string; title?: string; mode?: string; stages?: TaskStage[]; segments?: any[]; wait_policy?: any }) {
    const res = await tasksApi.create(data)
    await fetchTasks()
    if (res.task?.task_id) {
      await fetchTaskDetail(res.task.task_id)
    }
    return res.task
  }

  async function updateStages(taskId: string, stages: TaskStage[], currentStageId?: string) {
    const res = await tasksApi.updateStages(taskId, {
      expected_revision: currentTask.value?.revision,
      current_stage_id: currentStageId,
      stages
    })
    await fetchTaskDetail(taskId)
    return res.result
  }

  async function sendFeedback(taskId: string, body: string, source: 'human' | 'commander' = 'human', references?: any[]) {
    const res = await tasksApi.sendFeedback(taskId, {
      body,
      source,
      expected_revision: currentTask.value?.revision,
      references
    })
    await fetchTaskDetail(taskId)
    return res.feedback
  }

  async function ackReports(taskId: string, throughSequence: number) {
    const res = await tasksApi.ackReports(taskId, throughSequence)
    await fetchTaskDetail(taskId)
    return res.summary
  }

  return {
    tasks,
    currentTask,
    reports,
    feedbacks,
    activeTab,
    loading,
    debouncedRefresh,
    onConnected,
    fetchTasks,
    fetchTaskDetail,
    createTask,
    updateStages,
    sendFeedback,
    ackReports
  }
})
