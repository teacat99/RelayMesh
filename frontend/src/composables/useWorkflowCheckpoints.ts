import { ref, watch, type Ref } from 'vue'
import { workflowCheckpointsApi } from '../api/client'
import type { CheckpointView } from '../lib/anchor'
import { useSessionStore } from '../stores/session'
import { whenSecondaryOpen } from '../lib/secondaryGate'

/**
 * 某个工作流的恢复胶囊（只读）。
 *
 * - 工作流切换时重新拉取；MCP 保存新胶囊时由 SSE `checkpoint_saved` 事件触发刷新；
 * - 用递增序号丢弃过期响应，避免快速切换工作流时旧响应覆盖新数据；
 * - 拉取失败静默保留上一次的数据——胶囊标记只是时间线上的辅助信息，不应打断主流程。
 */
export function useWorkflowCheckpoints(workflowId: Ref<string | undefined | null>) {
  const sessionStore = useSessionStore()
  const checkpoints = ref<CheckpointView[]>([])
  let seq = 0

  async function load() {
    const id = workflowId.value
    const mine = ++seq
    if (!id) {
      checkpoints.value = []
      return
    }
    try {
      const list = await workflowCheckpointsApi.list(id)
      if (mine === seq) checkpoints.value = list
    } catch {
      // 保留上一次的数据
    }
  }

  // 次要请求让路（#169 第三期，lib/secondaryGate.ts）：整表在途时先等一等，放行后再取。
  // load 在放行那一刻才读工作流编号，所以等的这段时间里又换了工作流也只发一次、取的是最新的；栅栏没立着时同步执行，与以前一样。
  //
  // 会话页还没显示（深链接解析完之前，列表挂着的是占位的默认会话）时不取：占位会话马上会被目标取代，为它取一遍是白发的请求，
  // 低带宽下还会抢首屏的带宽；解析完、会话页显示出来（timelineShown 变真）时再取目标工作流的。
  let deferred = false
  watch([workflowId, () => sessionStore.timelineShown], ([next, shown], old) => {
    if (next !== old?.[0]) checkpoints.value = []
    if (!shown || deferred) return
    deferred = true
    whenSecondaryOpen(() => {
      deferred = false
      void load()
    })
  }, { immediate: true })

  watch(() => sessionStore.lastCheckpointEvent, (event) => {
    if (event && event.workflow_id === workflowId.value) void load()
  })

  return { checkpoints, reload: load }
}
