// 本机浏览器草稿的 Vue 封装（#144）：把「某个输入区的当前内容」和「它所属的作用域」交给它，
// 它负责：作用域变化时存旧的、取新的并回填；内容变化时防抖落盘；卸载时落盘。
// 规则与边界见 lib/localDraft.ts（只存本机、明文——严禁用在口令 / 令牌 / 凭据类输入上）。
import { onBeforeUnmount, ref, watch } from 'vue'
import { DraftController, browserDraftStorage } from '../lib/localDraft'

export interface LocalDraftOptions<T> {
  /** 当前作用域（见 draftScopes）；null = 暂时没有（比如数据还没加载出来），这期间不存不取。 */
  scope: () => string | null
  /** 当前输入区内容的快照：必须读到所有要保存的响应式字段，并返回新对象。 */
  read: () => T
  /** 回填：传入草稿，或 null（这个作用域没有草稿，调用方应把输入区重置成初始状态）。 */
  apply: (saved: T | null) => void
  /** 空内容不落盘，并会把已有草稿删掉。 */
  isEmpty: (payload: T) => boolean
  validate?: (d: unknown) => d is T
  maxChars?: number
  debounceMs?: number
}

export function useLocalDraft<T>(opts: LocalDraftOptions<T>) {
  const ctl = new DraftController<T>({
    store: browserDraftStorage(),
    isEmpty: opts.isEmpty,
    validate: opts.validate,
    maxChars: opts.maxChars,
    debounceMs: opts.debounceMs,
  })
  /** 当前内容是从草稿恢复出来的（用户一动手改就变回 false），用来显示「已恢复…」的提示。 */
  const restored = ref(false)
  let restoredJson = ''

  // 同步触发：作用域一变就立刻「存旧的、取新的」，赶在别的响应式逻辑动输入区之前，
  // 否则旧作用域的输入区被重置成空时，空内容会按旧作用域写盘、把草稿冲掉。
  watch(
    opts.scope,
    scope => {
      const saved = ctl.switchTo(scope)
      restored.value = saved !== null
      restoredJson = saved !== null ? JSON.stringify(saved) : ''
      opts.apply(saved)
    },
    { immediate: true, flush: 'sync' },
  )

  watch(opts.read, payload => {
    if (restored.value && JSON.stringify(payload) !== restoredJson) restored.value = false
    ctl.update(payload)
  })

  onBeforeUnmount(() => ctl.dispose())

  /** 提交成功：删掉草稿（输入区由调用方自己重置）。 */
  function clear() {
    ctl.clear()
    restored.value = false
  }

  /** 用户点「丢弃」：删掉草稿，并把输入区重置成初始状态。 */
  function discard() {
    clear()
    opts.apply(null)
  }

  return { restored, clear, discard }
}
