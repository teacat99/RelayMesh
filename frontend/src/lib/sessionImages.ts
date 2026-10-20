// 会话 / 追加里的图片（#120 · D-273 阶段 C；只给预览视图用，经典列表有自己的一份同名函数，没动）。
//
// 三个纯函数：取稳定的 key、取占位比例、取可以直接放进 <img src> 的地址；外加一份「已加载完」的记录。
// 「已加载」记录放在模块级（跨组件实例共享）：列表重新渲染、卡片重新挂载后，已经加载过的缩略图不会再闪一次骨架。
import { ref } from 'vue'
import type { SessionImage } from '../api/types'

/** 一张图在列表里的稳定 key：优先内容哈希，其次直链，最后退回序号；prefix 区分来源（会话号 / inbox-<id>）。 */
export function getImageKey(img: SessionImage | null | undefined, index: number, prefix: string): string {
  if (!img) return `${prefix}_${index}`
  if (img.hash) return `${prefix}_${img.hash}`
  if (img.url) return `${prefix}_${img.url}`
  return `${prefix}_${index}`
}

/** 骨架占位的宽高比：图片自带尺寸就用它（加载前后高度不变，没有跳动），否则 16 / 10。 */
export function getImageStyle(img: SessionImage | null | undefined): { aspectRatio: string } {
  if (img && img.width && img.height && img.width > 0 && img.height > 0) {
    return { aspectRatio: `${img.width} / ${img.height}` }
  }
  return { aspectRatio: '16 / 10' }
}

/** 图片的可用地址：直链优先；内联数据（data: / http(s) / 裸 base64）次之；都没有返回空串。 */
export function getImageUrl(img: SessionImage | null | undefined): string {
  if (!img) return ''
  if (img.url) return img.url
  if (img.data) {
    if (img.data.startsWith('data:') || img.data.startsWith('http://') || img.data.startsWith('https://')) {
      return img.data
    }
    const format = img.format || 'png'
    return `data:image/${format};base64,${img.data}`
  }
  return ''
}

const loadedImageKeys = ref<Set<string>>(new Set())

export function isImageLoaded(key: string): boolean {
  return loadedImageKeys.value.has(key)
}

export function markImageLoaded(key: string): void {
  loadedImageKeys.value.add(key)
}
