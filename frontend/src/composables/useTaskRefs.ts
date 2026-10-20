import { computed } from 'vue'
import { toast } from 'vue-sonner'
import { useWorkItemStore } from '../stores/workItems'

/**
 * 正文里的 #N → 任务详情弹窗：识别与装饰见 lib/taskRefs.ts，这里提供「哪些编号存在」与「点开」。
 *
 * 编号只在会话页当前的工作流里解析（任务 store 的 feed 就是这个工作流的全部任务）：
 * workflowId 为空、不是会话页当前的工作流、或任务还没加载完时，什么都不链接，保持原样。
 */
export function useTaskRefs(workflowId: () => string | undefined) {
  const store = useWorkItemStore()

  const active = computed(() => {
    const id = workflowId()
    return !!id && store.feed.workflowId === id && store.feed.loaded
  })
  /** 编号集合变化的信号（字符串）：没有可链接的任务时为空串；只有编号集合真的变了它才变。 */
  const signature = computed(() => (active.value ? store.feedSeqSignature : ''))
  const has = (seq: number) => active.value && store.feedSeqSet.has(seq)

  async function open(seq: number) {
    const id = workflowId()
    if (!id) return
    if (!(await store.openBySeq(id, seq))) toast.info(`找不到任务 #${seq}，它可能已被删除`)
  }

  return { active, signature, has, open }
}
