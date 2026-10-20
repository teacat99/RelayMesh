// 聊天里点开引用卡片后的右侧预览：当前打开哪篇文档 + 摘要缓存（卡片和预览共用）。
import { defineStore } from 'pinia'
import { ref } from 'vue'
import { artifactsApi, type ArtifactSummary } from '../api/artifacts'

export type ArtifactMeta = ArtifactSummary | 'missing'

export const useArtifactPreviewStore = defineStore('artifactPreview', () => {
  const docId = ref(0)
  const meta = ref<Record<number, ArtifactMeta>>({})
  const inflight = new Map<number, Promise<void>>()

  function load(id: number, force = false): Promise<void> {
    if (!force && meta.value[id]) return Promise.resolve()
    const running = inflight.get(id)
    if (running) return running
    const p = artifactsApi
      .summary(id)
      .then(s => {
        meta.value = { ...meta.value, [id]: s }
      })
      .catch(() => {
        meta.value = { ...meta.value, [id]: 'missing' }
      })
      .finally(() => inflight.delete(id))
    inflight.set(id, p)
    return p
  }

  function open(id: number) {
    docId.value = id
    void load(id, true)
  }
  function close() {
    docId.value = 0
  }

  return { docId, meta, load, open, close }
})
