import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { wakeErrorMessage, wakePromptsApi, type WakeDefaults, type WakeKind, type WakeKindInfo, type WakeVarInfo } from '../api/wakePrompts'
import { WAKE_TEMPLATE_MAX_DEFAULT, wakeVarsFor } from '../lib/wakePrompts'

// 唤醒词模板的内置默认与变量表（#232 D3）：整个页面只取一次、各处共用（会话弹窗的「唤醒词」页签与设置页）。
// 内置默认随 Hub 版本变化、与会话无关，所以不需要事件刷新；取失败可以重试。

export const useWakePromptsStore = defineStore('wakePrompts', () => {
  const defaults = ref<WakeDefaults | null>(null)
  const error = ref('')
  let pending: Promise<void> | null = null

  const max = computed(() => defaults.value?.template_max || WAKE_TEMPLATE_MAX_DEFAULT)

  function ensure(force = false): Promise<void> {
    if (!force && defaults.value) return Promise.resolve()
    if (pending) return pending
    const p: Promise<void> = (async () => {
      try {
        defaults.value = await wakePromptsApi.defaults()
        error.value = ''
      } catch (err) {
        error.value = wakeErrorMessage(err, '无法读取唤醒词的内置默认')
      }
    })().finally(() => {
      if (pending === p) pending = null
    })
    pending = p
    return p
  }

  function info(kind: WakeKind): WakeKindInfo | undefined {
    return defaults.value?.kinds.find(k => k.kind === kind)
  }

  function varsFor(kind: WakeKind): WakeVarInfo[] {
    return wakeVarsFor(kind, defaults.value?.vars ?? [])
  }

  return { defaults, error, max, ensure, info, varsFor }
})
