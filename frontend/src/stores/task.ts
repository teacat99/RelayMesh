import { defineStore } from 'pinia'
import { ref } from 'vue'
import type { TaskSummary, TaskDetail, Report, TaskStage, Feedback } from '../api/types'
import { tasksApi } from '../api/client'

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

  async function fetchTasks(updatesOnly = false) {
    const reqSeq = ++fetchTasksSeq
    try {
      loading.value = true
      const res = await tasksApi.list({ updates_only: updatesOnly })
      if (reqSeq !== fetchTasksSeq) {
        return
      }
      tasks.value = res.tasks
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
    fetchTasks,
    fetchTaskDetail,
    createTask,
    updateStages,
    sendFeedback,
    ackReports
  }
})
