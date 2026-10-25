import { computed } from 'vue'
import { useRoute } from 'vue-router'
import { deepLinkTarget, isDeepLinkPending } from '../lib/notifyBus'

/**
 * 地址上还没处理完的深链接（?wf=… / ?s=…）指向哪个工作流；没有深链、已经处理完（useNotifyRuntime 选中目标后会把参数去掉）、
 * 或只带了会话编号（工作流要等简略会话列表回来才知道）时是空串。
 *
 * 用途：深链接冷打开时目标工作流要等简略会话列表回来才解析出来，那之前会话页的「当前工作流」是空的，
 * 外壳件的占位（lib/shellHints.ts，#246）靠它在第一帧就能按目标工作流的记忆画出来。
 */
export function usePendingDeepLinkWf() {
  const route = useRoute()
  return computed(() => (isDeepLinkPending(route.name, route.query) ? deepLinkTarget(route.query).wf : ''))
}
