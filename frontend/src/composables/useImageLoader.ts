import { reactive, ref } from 'vue'

export interface ImageLoadTask {
  url: string
  status: 'pending' | 'loading' | 'completed' | 'error'
  receivedBytes: number
  totalBytes: number
  percent: number
  blobUrl?: string
  error?: string
}

// 全局单例响应式任务映射表，实现缩略图与 Lightbox 弹窗进度共享
const taskMap = reactive<Map<string, ImageLoadTask>>(new Map())

// 排队队列
const queue = ref<string[]>([])
let activeCount = 0
const MAX_CONCURRENT = 1 // 严格串行按阅读顺序单并发下载，为小带宽保驾护航

/**
 * 格式化字节数 (例: 1.25 MB 或 850 KB)
 */
export function formatBytes(bytes: number): string {
  if (!bytes || bytes <= 0) return '0 KB'
  if (bytes < 1024 * 1024) {
    return `${Math.round(bytes / 1024)} KB`
  }
  return `${(bytes / (1024 * 1024)).toFixed(2)} MB`
}

/**
 * 处理下一个待处理队列任务
 */
function processNextQueueItem() {
  if (activeCount >= MAX_CONCURRENT || queue.value.length === 0) {
    return
  }

  const nextUrl = queue.value.shift()
  if (!nextUrl) return

  const task = taskMap.get(nextUrl)
  if (!task || task.status === 'completed' || task.status === 'loading') {
    processNextQueueItem()
    return
  }

  executeStreamLoad(nextUrl)
}

/**
 * 实际发起 fetch 流式下载并监听 chunks 进度
 */
async function executeStreamLoad(url: string) {
  let task = taskMap.get(url)
  if (!task) {
    task = reactive<ImageLoadTask>({
      url,
      status: 'pending',
      receivedBytes: 0,
      totalBytes: 0,
      percent: 0
    })
    taskMap.set(url, task)
  }

  // Base64 或已经生成的 Blob 直接秒成
  if (url.startsWith('data:') || url.startsWith('blob:')) {
    task.status = 'completed'
    task.blobUrl = url
    task.percent = 100
    processNextQueueItem()
    return
  }

  task.status = 'loading'
  task.percent = 0
  task.receivedBytes = 0
  task.error = undefined
  activeCount++

  try {
    const headers: Record<string, string> = {}
    const storedToken = localStorage.getItem('relaymesh_token')
    if (storedToken) {
      headers['Authorization'] = `Bearer ${storedToken}`
    }

    const response = await fetch(url, {
      credentials: 'same-origin',
      headers
    })
    if (!response.ok) {
      // 若遇到鉴权失败或特定错误，优雅回退为完成，交由浏览器原生 img 标签携带 Cookie / 内部会话渲染
      console.warn(`[ImageLoader] Fetch returned ${response.status}, falling back to native img rendering: ${url}`)
      task.status = 'completed'
      task.blobUrl = url
      task.percent = 100
      return
    }

    const contentLength = response.headers.get('content-length')
    const totalBytes = contentLength ? parseInt(contentLength, 10) : 0
    task.totalBytes = totalBytes

    const contentType = response.headers.get('content-type') || 'image/png'

    if (!response.body) {
      // 降级兜底处理
      const blob = await response.blob()
      task.blobUrl = URL.createObjectURL(blob)
      task.receivedBytes = blob.size
      task.totalBytes = blob.size
      task.percent = 100
      task.status = 'completed'
      return
    }

    const reader = response.body.getReader()
    const chunks: Uint8Array[] = []
    let received = 0

    while (true) {
      const { done, value } = await reader.read()
      if (done) break

      if (value) {
        chunks.push(value)
        received += value.length
        task.receivedBytes = received

        if (totalBytes > 0) {
          task.percent = Math.min(99, Math.round((received / totalBytes) * 100))
        } else {
          // 未知总大小时基于经验步进模拟进度
          task.percent = Math.min(90, Math.round(received / (256 * 1024) * 10))
        }
      }
    }

    // 下载组装 Blob 并生成临时本地 URL
    const finalBlob = new Blob(chunks, { type: contentType })
    task.blobUrl = URL.createObjectURL(finalBlob)
    task.receivedBytes = finalBlob.size
    task.totalBytes = finalBlob.size
    task.percent = 100
    task.status = 'completed'
  } catch (err: any) {
    console.warn(`[ImageLoader] Failed to stream image: ${url}`, err)
    task.status = 'error'
    task.error = err?.message || '下载失败'
  } finally {
    activeCount = Math.max(0, activeCount - 1)
    processNextQueueItem()
  }
}

export function useImageLoader() {
  /**
   * 获取指定 URL 的任务状态
   */
  function getTask(url?: string): ImageLoadTask | undefined {
    if (!url) return undefined
    return taskMap.get(url)
  }

  /**
   * 获取最终展示地址 (已完成则为 BlobURL，否则返回原始 URL)
   */
  function getDisplaySrc(url?: string): string {
    if (!url) return ''
    const task = taskMap.get(url)
    if (task && task.status === 'completed' && task.blobUrl) {
      return task.blobUrl
    }
    return url
  }

  /**
   * 将图片加入按序阅读队列
   */
  function enqueue(url?: string) {
    if (!url) return
    let task = taskMap.get(url)
    if (!task) {
      task = reactive<ImageLoadTask>({
        url,
        status: 'pending',
        receivedBytes: 0,
        totalBytes: 0,
        percent: 0
      })
      taskMap.set(url, task)
    }

    if (task.status === 'completed' || task.status === 'loading') {
      return
    }

    if (!queue.value.includes(url)) {
      queue.value.push(url)
      processNextQueueItem()
    }
  }

  /**
   * 用户主动点击：提权至最高优先级，立刻插队到队列最前列加载
   */
  function prioritize(url?: string) {
    if (!url) return
    let task = taskMap.get(url)
    if (!task) {
      task = reactive<ImageLoadTask>({
        url,
        status: 'pending',
        receivedBytes: 0,
        totalBytes: 0,
        percent: 0
      })
      taskMap.set(url, task)
    }

    if (task.status === 'completed' || task.status === 'loading') {
      return
    }

    // 从原队列剔除并插入到头部最前列
    queue.value = queue.value.filter(u => u !== url)
    queue.value.unshift(url)

    // 若当前无活动任务立即调度
    if (activeCount < MAX_CONCURRENT) {
      processNextQueueItem()
    }
  }

  /**
   * 失败重试
   */
  function retry(url?: string) {
    if (!url) return
    const task = taskMap.get(url)
    if (task) {
      task.status = 'pending'
      task.error = undefined
    }
    prioritize(url)
  }

  return {
    getTask,
    getDisplaySrc,
    enqueue,
    prioritize,
    retry,
    formatBytes
  }
}
