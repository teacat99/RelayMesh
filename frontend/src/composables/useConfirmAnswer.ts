// 确认卡（智能体的提问 / 计划确认）上的作答动作：提交、采用推荐、忽略，以及它们共用的「提交中」与本机草稿作用域。
//
// 经典的确认卡（confirms/ConfirmCard）与预览视图里待处理确认项的统一消息卡共用这一份，
// 两处入口的行为与文案只写一遍、不会各改各的：
// - 作答走 store（单条接口，服务端同事务写入收件箱）；失败时卡片保持原样并提示原因，不会出现「界面已答复、智能体收不到」；
// - 选择、备注与自定义答复存本机草稿（draftScope）：切走再回来、刷新都还在，答复 / 采用推荐 / 忽略成功后清掉；
// - 忽略是两段式：第一击只是把 confirmDismiss 置真（界面换成「确认忽略 / 算了」），第二击才真的调接口。
import { computed, ref, toValue, type MaybeRefOrGetter } from 'vue'
import { toast } from 'vue-sonner'
import { confirmErrorMessage, type ConfirmItem } from '../api/confirms'
import { useConfirmStore } from '../stores/confirms'
import { answerText, optionLabel, recommendedIds } from '../lib/confirms'
import { clearLocalDraft, draftScopes } from '../lib/localDraft'

export function useConfirmAnswer(item: MaybeRefOrGetter<ConfirmItem>, workflowId: MaybeRefOrGetter<string>) {
  const store = useConfirmStore()

  const busy = computed(() => store.busy.has(toValue(item).ref) || store.busy.has('*'))
  const draftScope = computed(() => draftScopes.confirm(toValue(workflowId), toValue(item).ref))
  /** 忽略的第一击之后为真：界面换成「确认忽略 / 算了」；不计时，点「算了」（界面把它置回假）或确认后才复位。 */
  const confirmDismiss = ref(false)

  // custom：用户在「其他（自定义）」里写的答复（#183）；预设选项都不合适时靠它作答，没选中就是空串。
  async function submit(options: string[], note: string, custom = '') {
    const scope = draftScope.value
    try {
      const done = await store.answer(toValue(item).ref, options, note || undefined, custom || undefined)
      clearLocalDraft(scope)
      toast.success(`已答复 ${done.ref}`, { description: answerText(done, 80) || undefined, duration: 2500 })
    } catch (err) {
      toast.error(confirmErrorMessage(err))
    }
  }

  async function adopt() {
    const ids = recommendedIds(toValue(item).options, toValue(item).multi)
    if (!ids.length) return
    const scope = draftScope.value
    try {
      const done = await store.answer(toValue(item).ref, ids)
      clearLocalDraft(scope)
      toast.success(`已采用推荐 ${done.ref}`, { description: ids.map(id => optionLabel(toValue(item).options, id)).join('、'), duration: 2500 })
    } catch (err) {
      toast.error(confirmErrorMessage(err))
    }
  }

  async function dismiss() {
    if (!confirmDismiss.value) {
      confirmDismiss.value = true
      return
    }
    confirmDismiss.value = false
    const scope = draftScope.value
    try {
      const done = await store.dismiss(toValue(item).ref)
      clearLocalDraft(scope)
      toast.success(`已忽略 ${done.ref}`, { description: '会告诉智能体不用再等它', duration: 2500 })
    } catch (err) {
      toast.error(confirmErrorMessage(err))
    }
  }

  return { busy, draftScope, confirmDismiss, submit, adopt, dismiss }
}
