import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { toast } from 'vue-sonner'
import { notifyApi, notifyErrorMessage, type PrefsResult } from '../api/notify'
import {
  boardStaleMs as boardStaleMsOf,
  describePolicy,
  emptyPrefs,
  isMuted,
  muteUntilISO,
  policyOf,
  type ConvPolicy,
  type NotifyPrefs,
  type PreviewMode,
} from '../lib/notifyPrefs'

/**
 * 通知偏好（服务端保存，跨设备共享）：免打扰、ntfy 预览模式、「指派久无人接」的提醒开关与阈值、各会话的提醒策略。
 * 「这条消息该不该打扰你」由服务端裁决；这里只负责让界面读写这份偏好，并在别处（别的标签页、别的设备）
 * 改动时通过 SSE notify_prefs_updated 同步过来。
 */
export const useNotifyPrefsStore = defineStore('notifyPrefs', () => {
  const prefs = ref<NotifyPrefs>(emptyPrefs())
  const loaded = ref(false)
  const saving = ref(false)
  /** 服务端时间 − 本机时间（毫秒）：静音到期的判断以服务端时钟为准。 */
  let skewMs = 0
  let loadSeq = 0
  let reloadTimer: number | null = null

  const dnd = computed(() => prefs.value.dnd)
  const ntfyPreview = computed(() => prefs.value.ntfy_preview)
  const boardStaleOff = computed(() => prefs.value.board_stale_off)
  /** 生效的「久无人接」阈值（毫秒）：任务行的「已等」标红读它；由服务端算好，与回执头和提醒同一个数。 */
  const boardStaleMs = computed(() => boardStaleMsOf(prefs.value))
  /** 同一个阈值，以分钟计（任务行标红时的悬停说明用它写「超过 N 小时」）。 */
  const boardStaleAfterMinutes = computed(() => prefs.value.board_stale_after_minutes)

  function serverNow(): number {
    return Date.now() + skewMs
  }

  function apply(res: PrefsResult) {
    prefs.value = res.prefs
    skewMs = res.skewMs
    loaded.value = true
  }

  async function load() {
    const seq = ++loadSeq
    try {
      const res = await notifyApi.getPrefs()
      if (seq === loadSeq) apply(res) // 之后又发起了修改或更新的读取：丢弃这份可能已经过时的响应
    } catch (err) {
      console.warn('[notify] 读取通知偏好失败', notifyErrorMessage(err))
    }
  }

  /** 先在界面上生效，再请求服务端；失败则回滚并提示。 */
  async function mutate(optimistic: () => void, request: () => Promise<PrefsResult>, failure: string): Promise<boolean> {
    const before = prefs.value
    loadSeq++ // 让在途的读取作废，免得它的旧数据盖掉这次修改
    optimistic()
    saving.value = true
    try {
      apply(await request())
      return true
    } catch (err) {
      prefs.value = before
      toast.error(`${failure}：${notifyErrorMessage(err)}`)
      return false
    } finally {
      saving.value = false
    }
  }

  function setDnd(on: boolean) {
    return mutate(
      () => (prefs.value = { ...prefs.value, dnd: on }),
      () => notifyApi.updatePrefs({ dnd: on }),
      '设置免打扰失败',
    )
  }

  function toggleDnd() {
    return setDnd(!prefs.value.dnd)
  }

  function setNtfyPreview(mode: PreviewMode) {
    return mutate(
      () => (prefs.value = { ...prefs.value, ntfy_preview: mode }),
      () => notifyApi.updatePrefs({ ntfy_preview: mode }),
      '设置推送预览失败',
    )
  }

  /** 打开 / 关掉「指派久无人接」的提醒；只管提醒，不影响阈值与任务行上的标红。 */
  function setBoardStaleOff(off: boolean) {
    return mutate(
      () => (prefs.value = { ...prefs.value, board_stale_off: off }),
      () => notifyApi.updatePrefs({ board_stale_off: off }),
      '设置久无人接提醒失败',
    )
  }

  /** 设置「久无人接」的阈值（分钟，30~1440）；0 = 恢复出厂默认。先在界面上生效（任务行立刻按新阈值标红），失败则回滚。 */
  function setBoardStaleMinutes(minutes: number) {
    return mutate(
      () =>
        (prefs.value = {
          ...prefs.value,
          board_stale_minutes: minutes,
          board_stale_after_minutes: minutes === 0 ? prefs.value.board_stale_default_minutes : minutes,
        }),
      () => notifyApi.updatePrefs({ board_stale_minutes: minutes }),
      '设置久无人接的时长失败',
    )
  }

  /** 设置某个会话的提醒策略；mutedForMs 只对 muted 有意义（null / 不传 = 一直静音）。 */
  function setConversationPolicy(conversation: string, policy: ConvPolicy, mutedForMs: number | null = null) {
    const until = policy === 'muted' ? muteUntilISO(mutedForMs, serverNow()) : undefined
    return mutate(
      () => {
        const conversations = { ...prefs.value.conversations }
        conversations[conversation] = until ? { policy, muted_until: until } : { policy }
        prefs.value = { ...prefs.value, conversations }
      },
      () => notifyApi.setConversation(conversation, policy, until),
      '设置会话提醒失败',
    )
  }

  /** 恢复某个会话的默认提醒策略。 */
  function resetConversation(conversation: string) {
    return mutate(
      () => {
        const conversations = { ...prefs.value.conversations }
        delete conversations[conversation]
        prefs.value = { ...prefs.value, conversations }
      },
      () => notifyApi.clearConversation(conversation),
      '恢复会话提醒失败',
    )
  }

  function policy(conversation: string): ConvPolicy {
    return policyOf(prefs.value, conversation, serverNow())
  }

  function muted(conversation: string): boolean {
    return isMuted(prefs.value, conversation, serverNow())
  }

  function describe(conversation: string): string {
    return describePolicy(prefs.value, conversation, serverNow())
  }

  /** SSE 连上（首次或重连）：取一次最新偏好。 */
  function onConnected() {
    void load()
  }

  /** SSE notify_prefs_updated：别处改了偏好，稍等片刻（合并连续的更新）重新取。 */
  function onServerEvent() {
    if (reloadTimer !== null) window.clearTimeout(reloadTimer)
    reloadTimer = window.setTimeout(() => {
      reloadTimer = null
      void load()
    }, 150)
  }

  return {
    prefs,
    loaded,
    saving,
    dnd,
    ntfyPreview,
    boardStaleOff,
    boardStaleMs,
    boardStaleAfterMinutes,
    setDnd,
    toggleDnd,
    setNtfyPreview,
    setBoardStaleOff,
    setBoardStaleMinutes,
    setConversationPolicy,
    resetConversation,
    policy,
    muted,
    describe,
    load,
    onConnected,
    onServerEvent,
  }
})
