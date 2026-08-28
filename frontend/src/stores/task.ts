import { defineStore } from 'pinia'
import { ref } from 'vue'
import type { TaskSummary, TaskDetail, Report } from '../api/types'
import { tasksApi } from '../api/client'

export const useTaskStore = defineStore('task', () => {
  const tasks = ref<TaskSummary[]>([])
  const currentTask = ref<TaskDetail | null>(null)
  const reports = ref<Report[]>([])
  const loading = ref(false)

  async function fetchTasks(updatesOnly = false) {
    try {
      loading.value = true
      const res = await tasksApi.list({ updates_only: updatesOnly })
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
      const [taskRes, repRes] = await Promise.all([
        tasksApi.get(taskId),
        tasksApi.getReports(taskId)
      ])
      currentTask.value = taskRes.task
      reports.value = repRes.reports
    } catch (err) {
      console.error('Failed to fetch task detail', err)
    } finally {
      loading.value = false
    }
  }

  async function sendFeedback(taskId: string, body: string, references?: any[]) {
    const res = await tasksApi.sendFeedback(taskId, {
      body,
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
    loading,
    fetchTasks,
    fetchTaskDetail,
    sendFeedback,
    ackReports
  }
})
