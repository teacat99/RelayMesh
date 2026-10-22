// 一个会话（工作流）的提醒策略状态：页头的「提醒」菜单（PC）与手机「⋯」菜单里的提醒条目共用（#115、#156 批 1）。
// 偏好存在服务端，浏览器与手机推送一起生效；这里只算展示用的派生状态。
import { computed, type MaybeRefOrGetter, toValue } from 'vue'
import { mutedUntilMs, workflowConversation } from '../lib/notifyPrefs'
import { useNotifyPrefsStore } from '../stores/notifyPrefs'

export function useConversationNotify(workflowId: MaybeRefOrGetter<string>) {
  const prefs = useNotifyPrefsStore()
  const conversation = computed(() => workflowConversation(toValue(workflowId)))
  const muted = computed(() => prefs.muted(conversation.value))
  const note = computed(() => prefs.describe(conversation.value))
  /** 「一直静音」才打勾；有到期时间的静音不勾任何预设（说明文字里写着到什么时候）。 */
  const mutedForever = computed(() => muted.value && mutedUntilMs(prefs.prefs, conversation.value) === null)
  const title = computed(() => (muted.value ? `这个会话已静音：${note.value}` : '这个会话的提醒：全部（点击设置静音）'))
  return { prefs, conversation, muted, note, mutedForever, title }
}
