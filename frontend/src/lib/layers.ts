// 全站层级表（z-index）。凡是会浮在别的界面之上的东西，层级都从这里取，不要在组件里手写数字。
//
// 为什么要集中：几个弹窗的层级一样时，谁盖住谁取决于它们的 DOM 先后——而 DOM 先后又取决于
// 组件是在应用启动时就挂载（例如全局的「验证身份」弹窗）还是打开时才挂载（例如「接入主机」），
// 与「谁后打开」无关。结果就是后打开的验证框被先打开的弹窗压在下面，密码框点不到。
// 用显式的层级而不是 DOM 先后，行为才是确定的。
//
// 约定（数字越大越靠上）：
//   sheet      50   侧边抽屉（审计日志、工作表）、移动端侧边栏
//   dialog     60   普通弹窗
//   dialogTop  70   叠在别的弹窗之上的确认框（确认删除、结束会话……）
//   dialogVerify 75 验证身份框。确认框里的操作本身也可能要求再验证一次（确认删除主机 → 再输一次登录密码），
//                   验证框是确认之后才弹出的，必须压在确认框之上；与确认框同层时，确认框的遮罩会把它盖住（密码框点不到）
//   popper     80   气泡类：提示、浮层、下拉菜单、选择器——它们常开在弹窗里，必须高于弹窗
//   session    90   掉线后的登录框：任何界面都不能挡住它
//   lightbox  100   图片预览
// 消息提示（toast）由 vue-sonner 自带极高层级，始终在最上面，不在此表内。
//
// Tailwind 需要在源码里看到完整的类名，所以这里写字面量，不要用模板字符串拼接。

export const LAYER = {
  sheet: 50,
  dialog: 60,
  dialogTop: 70,
  dialogVerify: 75,
  popper: 80,
  session: 90,
  lightbox: 100,
} as const

export type LayerName = keyof typeof LAYER

export const LAYER_CLASS: Record<LayerName, string> = {
  sheet: 'z-50',
  dialog: 'z-[60]',
  dialogTop: 'z-[70]',
  dialogVerify: 'z-[75]',
  popper: 'z-[80]',
  session: 'z-[90]',
  lightbox: 'z-[100]',
}

/** 弹窗的三档：普通弹窗；叠在别的弹窗之上的确认框（top）；压在确认框之上的验证身份框（verify）。 */
export type DialogLayer = 'base' | 'top' | 'verify'

export function dialogLayerClass(layer: DialogLayer | undefined): string {
  switch (layer) {
    case 'verify':
      return LAYER_CLASS.dialogVerify
    case 'top':
      return LAYER_CLASS.dialogTop
    default:
      return LAYER_CLASS.dialog
  }
}
