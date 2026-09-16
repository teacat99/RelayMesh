<script setup lang="ts">
import { onMounted, onUnmounted, ref, watch, computed } from 'vue'
import { usePreviewStore } from '@/stores/preview'
import { useImageLoader, formatBytes } from '@/composables/useImageLoader'
import { Loader2, AlertCircle, RotateCcw, X } from 'lucide-vue-next'

const previewStore = usePreviewStore()
const imageLoader = useImageLoader()

const scale = ref(1)
const posX = ref(0)
const posY = ref(0)
const isDragging = ref(false)
let startDragX = 0
let startDragY = 0

// 本地 DOM 真实就绪状态
const isDomImageLoaded = ref(false)
const isDomImageError = ref(false)

// 移动端多点触控双指缩放与平移记录
let initialPinchDistance = 0
let initialScale = 1
let initialTouchCenterX = 0
let initialTouchCenterY = 0

function resetTransform() {
  scale.value = 1
  posX.value = 0
  posY.value = 0
  isDomImageLoaded.value = false
  isDomImageError.value = false
}

// 提取当前大图的下载任务
const currentTask = computed(() => {
  const src = previewStore.currentImage?.src
  if (!src) return undefined
  return imageLoader.getTask(src)
})

// 计算是否最终就绪可展示
const isImageReady = computed(() => {
  const task = currentTask.value
  if (task && task.status === 'completed') {
    return true
  }
  return isDomImageLoaded.value
})

watch(() => previewStore.isImageOpen, (open) => {
  if (open) {
    resetTransform()
    const src = previewStore.currentImage?.src
    if (src && !src.startsWith('data:') && !src.startsWith('blob:')) {
      // 提权优先插队拉取
      imageLoader.prioritize(src)
    }
  }
})

// PC 鼠标滚轮平滑缩放
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

// 拖拽平移逻辑
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

// 移动端触摸
function handleTouchStart(e: TouchEvent) {
  if (e.touches.length === 1) {
    isDragging.value = true
    startDragX = e.touches[0].clientX - posX.value
    startDragY = e.touches[0].clientY - posY.value
  } else if (e.touches.length === 2) {
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

function handleDomLoad() {
  isDomImageLoaded.value = true
  isDomImageError.value = false
}

function handleDomError() {
  isDomImageError.value = true
}

function handleRetry() {
  isDomImageError.value = false
  const src = previewStore.currentImage?.src
  if (src) {
    imageLoader.retry(src)
  }
}

// 键盘 ESC 关闭
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
      class="fixed inset-0 z-[100] flex items-center justify-center bg-black/60 dark:bg-black/80 backdrop-blur-xs select-none touch-none overflow-hidden cursor-default"
      @click="previewStore.closeImagePreview()"
      @wheel="handleWheel"
    >
      <!-- 右上角快捷关闭按钮 -->
      <button
        type="button"
        class="absolute top-4 right-4 z-20 p-2 rounded-full bg-black/60 hover:bg-black/90 text-white/90 hover:text-white transition-all cursor-pointer shadow-lg"
        @click.stop="previewStore.closeImagePreview()"
        title="关闭预览 (Esc)"
      >
        <X class="w-5 h-5" />
      </button>

      <!-- 纯净图片渲染容器：阻止冒泡，支持无级缩放与拖拽 -->
      <div
        class="relative max-w-full max-h-full flex items-center justify-center p-3"
        @click.stop
      >
        <!-- 1. 流式下载中进度卡片 (消除浏览器原生破损图错觉) -->
        <div
          v-if="!isImageReady && !isDomImageError && currentTask?.status !== 'error'"
          class="p-6 rounded-md bg-card/90 border border-border backdrop-blur-md shadow-2xl flex flex-col items-center gap-3.5 z-10 select-none min-w-[260px]"
        >
          <div class="flex items-center gap-2 text-foreground font-mono text-xs font-semibold">
            <Loader2 class="w-4 h-4 text-primary animate-spin" />
            <span>正在加载高清大图...</span>
            <span class="text-primary font-bold">{{ currentTask?.percent || 0 }}%</span>
          </div>

          <!-- 精准进度条 -->
          <div class="w-56 h-2 rounded-full bg-muted overflow-hidden border border-border/80">
            <div
              class="h-full bg-primary transition-all duration-150 rounded-full"
              :style="{ width: `${currentTask?.percent || 0}%` }"
            ></div>
          </div>

          <!-- 数据量与提示 -->
          <div class="flex items-center justify-between w-full text-[11px] font-mono text-muted-foreground px-0.5">
            <span>{{ formatBytes(currentTask?.receivedBytes || 0) }}</span>
            <span>{{ formatBytes(currentTask?.totalBytes || 0) }}</span>
          </div>
        </div>

        <!-- 2. 加载失败兜底卡片 -->
        <div
          v-else-if="isDomImageError || currentTask?.status === 'error'"
          class="p-6 rounded-md bg-card/95 border border-destructive/50 shadow-2xl flex flex-col items-center gap-3 z-10 select-none min-w-[240px]"
        >
          <div class="flex items-center gap-2 text-destructive font-mono text-xs font-semibold">
            <AlertCircle class="w-4 h-4" />
            <span>图片传输中断或失败</span>
          </div>
          <p class="text-[11px] text-muted-foreground text-center">
            {{ currentTask?.error || '网络超时或链接不可用' }}
          </p>
          <button
            type="button"
            class="px-3 py-1.5 rounded-xs bg-primary text-primary-foreground hover:bg-primary/90 text-xs font-mono font-medium flex items-center gap-1.5 transition-all cursor-pointer mt-1"
            @click="handleRetry"
          >
            <RotateCcw class="w-3.5 h-3.5" />
            <span>重新加载</span>
          </button>
        </div>

        <!-- 3. 高清真实大图：就绪后平滑淡入，未就绪保持绝对隐藏，杜绝破损图 -->
        <img
          :src="imageLoader.getDisplaySrc(previewStore.currentImage.src)"
          :alt="previewStore.currentImage.alt || 'preview'"
          class="max-h-[92vh] max-w-[94vw] object-contain select-none transition-all duration-300 ease-out rounded-xs border border-border bg-card shadow-2xl cursor-default"
          :class="isImageReady ? 'opacity-100 scale-100' : 'opacity-0 scale-95 pointer-events-none absolute'"
          :style="{
            transform: isImageReady ? `translate3d(${posX}px, ${posY}px, 0) scale(${scale})` : undefined,
            willChange: 'transform'
          }"
          draggable="false"
          @mousedown="handleMouseDown"
          @touchstart="handleTouchStart"
          @touchmove="handleTouchMove"
          @touchend="handleTouchEnd"
          @load="handleDomLoad"
          @error="handleDomError"
        />
      </div>
    </div>
  </Transition>
</template>
