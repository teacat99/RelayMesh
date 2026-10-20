<script setup lang="ts">
// 附件缩略图（#120 · D-273 阶段 C，只给预览视图用）：您的回复与追加共用，从列表里两份一样的模板抽出来。
//
// 行为与阶段 1 的版本完全一致：固定宽高比骨架（加载前后高度不变）、串行下载队列里的百分比 / 已传大小、下载失败可点重试、
// 就绪后淡入、点击放大预览；底部一体化标题栏，左边编号、右边文件名；底色统一 bg-muted。
// 图片的下载由 imageLoader 带鉴权串行进行，入队在列表里（按阅读顺序）；这里只负责展示与「点到谁优先下载谁」。
import { AlertCircle, Loader2 } from 'lucide-vue-next'
import type { SessionImage } from '../../../api/types'
import { formatBytes, useImageLoader } from '../../../composables/useImageLoader'
import { getImageKey, getImageStyle, getImageUrl, isImageLoaded, markImageLoaded } from '../../../lib/sessionImages'
import { usePreviewStore } from '../../../stores/preview'

const props = defineProps<{
  images?: SessionImage[] | null
  /** 区分来源的 key 前缀（会话号 / inbox-<id>）：同一张图出现在两处时各记各的「已加载」 */
  keyPrefix: string
  /** 放大预览时，没有文件名的图片用它命名（后面补 -序号），例如「第 3 轮用户附件」 */
  fallbackName: string
}>()

const imageLoader = useImageLoader()
const previewStore = usePreviewStore()

function open(img: SessionImage, index: number) {
  const url = getImageUrl(img)
  imageLoader.prioritize(url)
  previewStore.openImagePreview({
    src: url,
    alt: img.name || `${props.fallbackName}-${index + 1}`,
    width: img.width,
    height: img.height
  })
}
</script>

<template>
  <div v-if="images && images.length > 0" class="flex max-w-full flex-wrap gap-2 overflow-hidden pt-2">
    <div
      v-for="(img, imgIdx) in images"
      :key="img.hash || imgIdx"
      class="group relative h-24 max-w-full shrink-0 cursor-pointer overflow-hidden rounded-xs border border-border/80 bg-muted shadow-2xs transition-all hover:border-primary/60 sm:h-36"
      data-testid="attachment-thumb"
      :style="getImageStyle(img)"
      @click.stop="open(img, imgIdx)"
    >
      <!-- 骨架占位防抖与实时流式下载进度条（CLS = 0） -->
      <div
        v-if="!isImageLoaded(getImageKey(img, imgIdx, keyPrefix))"
        class="absolute inset-0 z-5 flex select-none flex-col items-center justify-center bg-muted/70 p-2"
      >
        <!-- 正在流式下载中：展示百分比与已传大小 -->
        <div
          v-if="imageLoader.getTask(getImageUrl(img))?.status === 'loading'"
          class="flex w-full max-w-[130px] flex-col items-center gap-1.5"
        >
          <div class="flex w-full items-center justify-between font-mono text-[10px] font-semibold text-muted-foreground">
            <span class="flex items-center gap-1 text-primary">
              <Loader2 class="size-3 animate-spin" />
              {{ imageLoader.getTask(getImageUrl(img))?.percent }}%
            </span>
            <span>{{ formatBytes(imageLoader.getTask(getImageUrl(img))?.receivedBytes || 0) }}</span>
          </div>
          <div class="h-1.5 w-full overflow-hidden rounded-full border border-border/60 bg-muted/90">
            <div
              class="h-full rounded-full bg-primary transition-all duration-150"
              :style="{ width: `${imageLoader.getTask(getImageUrl(img))?.percent || 0}%` }"
            ></div>
          </div>
        </div>

        <!-- 下载失败重试 -->
        <div
          v-else-if="imageLoader.getTask(getImageUrl(img))?.status === 'error'"
          class="flex cursor-pointer flex-col items-center gap-1 font-mono text-[10px] text-destructive hover:underline"
          title="点击重试下载"
          @click.stop="imageLoader.retry(getImageUrl(img))"
        >
          <AlertCircle class="size-3.5" />
          <span>重试</span>
        </div>

        <!-- 队列排队等待中 -->
        <div v-else class="flex animate-pulse items-center gap-1 rounded-xs border border-muted-foreground/20 px-2 py-1 text-muted-foreground/60">
          <span class="font-mono text-[9px]">排队加载</span>
        </div>
      </div>

      <!-- 实际图片：懒加载 + 异步解码 + 就绪平滑淡入 -->
      <img
        :src="imageLoader.getDisplaySrc(getImageUrl(img))"
        loading="lazy"
        decoding="async"
        class="pointer-events-none block size-full object-contain transition-opacity duration-300"
        :class="isImageLoaded(getImageKey(img, imgIdx, keyPrefix)) ? 'opacity-100' : 'opacity-0'"
        :alt="img.name || `attachment-${imgIdx + 1}`"
        @load="markImageLoaded(getImageKey(img, imgIdx, keyPrefix))"
      />
      <!-- 底部一体化分区标题栏：左侧嵌入编号，右侧嵌入文件名 -->
      <div class="pointer-events-none absolute inset-x-0 bottom-0 z-10 flex h-4.5 select-none items-stretch overflow-hidden bg-black/80 font-mono text-[9px] text-white backdrop-blur-xs">
        <div class="flex shrink-0 items-center justify-center border-r border-white/20 bg-primary px-1.5 font-bold text-primary-foreground">
          #{{ imgIdx + 1 }}
        </div>
        <div class="flex-1 self-center truncate px-1.5 text-center text-white/90">
          {{ img.name || `img-${imgIdx + 1}` }}
        </div>
      </div>
    </div>
  </div>
</template>
