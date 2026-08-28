import { defineStore } from 'pinia'
import { ref } from 'vue'

export interface PreviewImageInfo {
  src: string
  alt?: string
  title?: string
}

export const usePreviewStore = defineStore('preview', () => {
  // Pure Image Lightbox state
  const isImageOpen = ref(false)
  const currentImage = ref<PreviewImageInfo | null>(null)

  function openImagePreview(info: PreviewImageInfo) {
    currentImage.value = info
    isImageOpen.value = true
  }

  function closeImagePreview() {
    isImageOpen.value = false
    currentImage.value = null
  }

  return {
    isImageOpen,
    currentImage,
    openImagePreview,
    closeImagePreview
  }
})
