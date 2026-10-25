import { ref } from 'vue'

// 预览视图消息搜索面板的开关（#120 阶段 E-c1）。
// 入口在会话区上方的小字行 / 手机宽度的芯片条（MessageSearchLink），面板挂在预览列表里（PreviewMessageList），
// 两处隔得远、拿不到彼此的引用，所以用一个模块级单例传话（与 useSessionJump 同一个思路）。
const open = ref(false)

export function usePreviewSearch() {
  return {
    open,
    toggle() {
      open.value = !open.value
    },
    close() {
      open.value = false
    },
  }
}
