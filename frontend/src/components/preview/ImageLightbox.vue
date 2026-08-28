<script setup lang="ts">
import { onMounted, onUnmounted, ref, watch } from 'vue'
import { usePreviewStore } from '@/stores/preview'

const previewStore = usePreviewStore()

const scale = ref(1)
const posX = ref(0)
const posY = ref(0)
const isDragging = ref(false)
let startDragX = 0
let startDragY = 0

// 移动端多点触控双指缩放与平移记录
let initialPinchDistance = 0
let initialScale = 1
let initialTouchCenterX = 0
let initialTouchCenterY = 0

function resetTransform() {
  scale.value = 1
  posX.value = 0
  posY.value = 0
}

watch(() => previewStore.isImageOpen, (open) => {
  if (open) {
    resetTransform()
  }
})

// PC 鼠标滚轮平滑缩放（以鼠标当前位置为锚点或中心缩放）
function handleWheel(e: WheelEvent) {
  e.preventDefault()
  const delta = e.deltaY < 0 ? 0.18 : -0.18
  const newScale = Math.min(5, Math.max(0.4, Number((scale.value + delta).toFixed(2))))
  scale.value = newScale
  if (scale.value <= 1) {
    posX.value = 0
    posY.value = 0
  }
}

// 拖拽平移逻辑 (仅在放大后允许拖拽，或自由微移)
function handleMouseDown(e: MouseEvent) {
  if (e.button !== 0) return // 仅左键
  isDragging.value = true
  startDragX = e.clientX - posX.value
  startDragY = e.clientY - posY.value
  window.addEventListener('mousemove', handleMouseMove)
  window.addEventListener('mouseup', handleMouseUp)
}

function handleMouseMove(e: MouseEvent) {
  if (!isDragging.value) return
  posX.value = e.clientX - startDragX
  posY.value = e.clientY - startDragY
}

function handleMouseUp() {
  isDragging.value = false
  window.removeEventListener('mousemove', handleMouseMove)
  window.removeEventListener('mouseup', handleMouseUp)
}

// 移动端 Touch 触摸事件：单指拖拽 + 双指捏合平滑缩放
function handleTouchStart(e: TouchEvent) {
  if (e.touches.length === 1) {
    // 单指拖拽
    isDragging.value = true
    startDragX = e.touches[0].clientX - posX.value
    startDragY = e.touches[0].clientY - posY.value
  } else if (e.touches.length === 2) {
    // 双指捏合
    isDragging.value = false
    const t1 = e.touches[0]
    const t2 = e.touches[1]
    initialPinchDistance = Math.hypot(t2.clientX - t1.clientX, t2.clientY - t1.clientY)
    initialScale = scale.value
    initialTouchCenterX = (t1.clientX + t2.clientX) / 2
    initialTouchCenterY = (t1.clientY + t2.clientY) / 2
  }
}

function handleTouchMove(e: TouchEvent) {
  if (e.touches.length === 1 && isDragging.value) {
    e.preventDefault()
    posX.value = e.touches[0].clientX - startDragX
    posY.value = e.touches[0].clientY - startDragY
  } else if (e.touches.length === 2) {
    e.preventDefault()
    const t1 = e.touches[0]
    const t2 = e.touches[1]
    const dist = Math.hypot(t2.clientX - t1.clientX, t2.clientY - t1.clientY)
    if (initialPinchDistance > 0) {
      const pinchRatio = dist / initialPinchDistance
      scale.value = Math.min(5, Math.max(0.4, Number((initialScale * pinchRatio).toFixed(2))))
    }
  }
}

function handleTouchEnd(e: TouchEvent) {
  if (e.touches.length === 0) {
    isDragging.value = false
    initialPinchDistance = 0
  }
}

// 键盘与外部关闭
function handleKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape' && previewStore.isImageOpen) {
    previewStore.closeImagePreview()
  }
}

onMounted(() => {
  window.addEventListener('keydown', handleKeydown)
})

onUnmounted(() => {
  window.removeEventListener('keydown', handleKeydown)
})
</script>

<template>
  <Transition
    enter-active-class="transition duration-150 ease-out"
    enter-from-class="opacity-0"
    enter-to-class="opacity-100"
    leave-active-class="transition duration-100 ease-in"
    leave-from-class="opacity-100"
    leave-to-class="opacity-0"
  >
    <!-- 全屏极净低模糊通透背景，点击任意外部区域瞬间关闭 -->
    <div
      v-if="previewStore.isImageOpen && previewStore.currentImage"
      class="fixed inset-0 z-[100] flex items-center justify-center bg-black/10 dark:bg-black/25 backdrop-blur-[1px] select-none touch-none overflow-hidden cursor-default"
      @click="previewStore.closeImagePreview()"
      @wheel="handleWheel"
    >
      <!-- 纯净图片渲染容器：阻止冒泡，支持无级缩放与拖拽 -->
      <div
        class="relative max-w-full max-h-full flex items-center justify-center p-3"
        @click.stop
      >
        <img
          :src="previewStore.currentImage.src"
          :alt="previewStore.currentImage.alt || 'preview'"
          class="max-h-[92vh] max-w-[94vw] object-contain select-none transition-transform duration-75 ease-out rounded-xs border border-border bg-card shadow-2xl cursor-default"
          :style="{
            transform: `translate3d(${posX}px, ${posY}px, 0) scale(${scale})`,
            willChange: 'transform'
          }"
          draggable="false"
          @mousedown="handleMouseDown"
          @touchstart="handleTouchStart"
          @touchmove="handleTouchMove"
          @touchend="handleTouchEnd"
        />
      </div>
    </div>
  </Transition>
</template>
