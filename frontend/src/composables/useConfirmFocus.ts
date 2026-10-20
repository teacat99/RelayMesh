// 确认卡的「被点名」高亮：工具栏的「待答复」徽标点一下，会把要看的编号放进 store.focusRef，
// 对应的那张卡滚到视口中央并亮一下边框（2.5 秒后熄灭，并把 focusRef 清掉，下次再点同一个编号还能触发）。
//
// 经典的确认时间线卡（confirms/ConfirmTimelineCard）与预览视图里待处理确认项的统一消息卡共用这一份。
// 卡可能是在 focusRef 已经设好之后才挂载的（比如它落在还没展开的更早轮次里，列表先把窗口撑开），所以 watch 是 immediate 的；
// 这种卡在 setup 里还没有根节点、滚不了，所以点名时没有根节点就记下来，挂载好了再滚一次
// （否则只亮外框、不滚动：视口停在底部时点名更早轮次里的卡，卡会留在几千像素之外，滚动位置纹丝不动）。
import { onMounted, ref, toValue, watch, type MaybeRefOrGetter, type Ref } from 'vue'
import { useConfirmStore } from '../stores/confirms'

/** 高亮持续的时间（毫秒）。 */
export const CONFIRM_FOCUS_MS = 2500

/**
 * @param itemRef 这张卡的编号（如 Q5），可以是 getter
 * @param rootEl 卡的定位容器，被点名时滚到它
 * @returns focused：高亮是否亮着
 */
export function useConfirmFocus(itemRef: MaybeRefOrGetter<string>, rootEl: Ref<HTMLElement | null>): Ref<boolean> {
  const store = useConfirmStore()
  const focused = ref(false)
  let focusTimer: ReturnType<typeof setTimeout> | null = null
  // 点名时这张卡还没有根节点（它是点名之后才挂载的）：等挂载好再滚
  let scrollOnMount = false
  const reveal = () => rootEl.value?.scrollIntoView({ behavior: 'smooth', block: 'center' })
  watch(
    () => store.focusRef,
    focusRef => {
      if (!focusRef || focusRef !== toValue(itemRef)) return
      if (rootEl.value) reveal()
      else scrollOnMount = true
      focused.value = true
      if (focusTimer) clearTimeout(focusTimer)
      focusTimer = setTimeout(() => {
        focused.value = false
        if (store.focusRef === toValue(itemRef)) store.focusRef = ''
      }, CONFIRM_FOCUS_MS)
    },
    { immediate: true },
  )
  onMounted(() => {
    if (!scrollOnMount) return
    scrollOnMount = false
    // 这次点名还没过期才滚（过期的话 focusRef 已被清掉，用户早该离开这个位置了）
    if (store.focusRef === toValue(itemRef)) reveal()
  })
  return focused
}
