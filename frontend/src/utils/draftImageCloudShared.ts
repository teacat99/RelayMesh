/**
 * 草稿图片云存储登记的页面级单例（登记逻辑与不变量见 draftImageCloud.ts）。
 *
 * 整个页面共用一份：多个输入区（反馈页、任务页）各建一份会互相看不到对方的上传状态，同一张图重复上传。
 * 登记变化通过订阅分发给当前挂载的输入区。
 */
import { draftImagesApi } from '../api/client'
import { createDraftImageCloud, type CloudImageState, type DraftImageCloud } from './draftImageCloud'

export type CloudChangeListener = (workflowId: string, key: string, state: CloudImageState | null) => void

const listeners = new Set<CloudChangeListener>()
let instance: DraftImageCloud | null = null

export function sharedDraftImageCloud(): { cloud: DraftImageCloud; subscribe: (listener: CloudChangeListener) => () => void } {
  if (!instance) {
    instance = createDraftImageCloud({
      api: draftImagesApi,
      onChange: (workflowId, key, state) => {
        for (const listener of Array.from(listeners)) {
          try {
            listener(workflowId, key, state)
          } catch (error) {
            console.warn('草稿图片云存储监听器异常：', error)
          }
        }
      },
      onError: (workflowId, key, error, info) => {
        // 上传放弃 / 删除失败只记日志：界面上由缩略图的状态标记与草稿同步提示承担告知，孤儿由服务端后台清理兜底
        if (info.op === 'put' && !info.final) return
        console.warn(`草稿图片云存储 ${info.op} 失败（${workflowId}/${key || '*'}）：`, error)
      },
    })
  }
  const cloud = instance
  return {
    cloud,
    subscribe(listener) {
      listeners.add(listener)
      return () => {
        listeners.delete(listener)
      }
    },
  }
}
