import { defineStore } from 'pinia'
import { computed, ref, shallowRef } from 'vue'
import { toast } from 'vue-sonner'
import type { RelayRequest } from '../lib/crossNotice'

/** 最近一条新反馈请求；seq 单调递增，同一条请求再次到达也能被监听到。 */
export interface RelayRequestEvent extends RelayRequest {
  seq: number
}

/** 一条系统通知的参数（由 stores/notifyBus.ts 组好文案后交来）。 */
export interface SystemNotice {
  title: string
  body: string
  /** 相同标签的系统通知互相替换（同一会话只留一条）。 */
  tag: string
  /** 点击通知时执行；调用前已经把窗口带到前台并关闭了通知。 */
  onClick?: () => void
}

const SOUND_KEY = 'relaymesh_sound'
const DESKTOP_KEY = 'relaymesh_desktop_notify'

const notificationSupported = typeof window !== 'undefined' && 'Notification' in window

/**
 * 这台设备上「怎么呈现通知」的本机设置与呈现手段：提示音、系统通知的开关与权限。
 * 它们是本机偏好（localStorage），不跟账号走——同一个账号在手机上和电脑上想要的提醒方式不一样。
 * 「值不值得通知你」由服务端的统一通知闸裁决（见 stores/notifyBus.ts 与 stores/notifyPrefs.ts）。
 */
export const useNotifyStore = defineStore('notify', () => {
  const soundEnabled = ref(localStorage.getItem(SOUND_KEY) !== 'false')
  const desktopEnabled = ref(localStorage.getItem(DESKTOP_KEY) === 'true')
  /** 浏览器的系统通知权限；浏览器不支持时为 'unsupported'。只在用户点开关时才会去请求。 */
  const permission = ref<NotificationPermission | 'unsupported'>(notificationSupported ? Notification.permission : 'unsupported')
  /** 系统通知此刻能不能弹：本机开关已开、浏览器支持、权限已授予。 */
  const desktopReady = computed(() => desktopEnabled.value && permission.value === 'granted')
  /**
   * 新反馈请求的「事件」。应用内的消息（人在 ExecMesh 里时的跨项目提醒）
   * 需要路由与会话仓库，由 composables/useCrossModeNotice.ts 监听这个值后完成。
   */
  const lastRequest = shallowRef<RelayRequestEvent | null>(null)
  let requestSeq = 0

  function persist(key: string, on: boolean) {
    try {
      localStorage.setItem(key, on ? 'true' : 'false')
    } catch {
      // 隐私模式等写不进去：本次会话内仍然有效
    }
  }

  function toggleSound() {
    soundEnabled.value = !soundEnabled.value
    persist(SOUND_KEY, soundEnabled.value)
  }

  function refreshPermission() {
    if (notificationSupported) permission.value = Notification.permission
  }

  /** 开 / 关系统通知。开启时才向浏览器请求权限（必须由用户点击触发）；被屏蔽时告诉用户去哪里改。 */
  function toggleDesktop() {
    if (desktopEnabled.value) {
      desktopEnabled.value = false
      persist(DESKTOP_KEY, false)
      return
    }
    if (!notificationSupported) {
      toast.info('此浏览器不支持系统通知', { description: '提醒会以页面内的提示和提示音呈现。' })
      return
    }
    void Promise.resolve(Notification.requestPermission()).then(result => {
      permission.value = result
      if (result === 'granted') {
        desktopEnabled.value = true
        persist(DESKTOP_KEY, true)
      } else if (result === 'denied') {
        toast.info('浏览器已屏蔽本站的通知', { description: '请点地址栏左侧的站点设置，把「通知」改为允许后再开启。' })
      }
    })
  }

  // 另一个标签页改了本机设置：同步过来。多标签页里负责响铃与弹系统通知的只有 leader，
  // 它不一定是用户刚点开关的那个标签页，所以两边必须看到同一份设置。
  if (typeof window !== 'undefined') {
    window.addEventListener('storage', e => {
      if (e.key === SOUND_KEY) soundEnabled.value = e.newValue !== 'false'
      else if (e.key === DESKTOP_KEY) desktopEnabled.value = e.newValue === 'true'
    })
  }

  function playAlert() {
    if (!soundEnabled.value) return
    try {
      const ctx = new (window.AudioContext || (window as any).webkitAudioContext)()
      const osc = ctx.createOscillator()
      const gain = ctx.createGain()
      osc.type = 'sine'
      osc.frequency.setValueAtTime(587.33, ctx.currentTime) // D5
      osc.frequency.exponentialRampToValueAtTime(880, ctx.currentTime + 0.15) // A5
      gain.gain.setValueAtTime(0.15, ctx.currentTime)
      gain.gain.exponentialRampToValueAtTime(0.01, ctx.currentTime + 0.3)
      osc.connect(gain)
      gain.connect(ctx.destination)
      osc.start()
      osc.stop(ctx.currentTime + 0.3)
    } catch {
      // AudioContext not allowed or not supported
    }
  }

  /**
   * 弹一条系统通知。成功返回通知对象，不可用或失败（如部分手机浏览器不允许直接构造通知）返回 null，
   * 调用方据此回退到页内提示。
   */
  function showSystem(notice: SystemNotice): Notification | null {
    if (!desktopReady.value || !notificationSupported) return null
    try {
      // renotify 让「同标签的新通知」重新提醒一次（否则只是悄悄替换内容）；它不在标准的类型定义里
      const options: NotificationOptions & { renotify?: boolean } = {
        body: notice.body,
        icon: '/favicon.svg',
        tag: notice.tag,
        renotify: true,
      }
      const n = new Notification(notice.title, options)
      n.onclick = () => {
        try {
          window.focus()
        } catch {
          // 忽略
        }
        n.close()
        notice.onClick?.()
      }
      return n
    } catch {
      return null
    }
  }

  /** 发出一条「新反馈请求」事件，驱动人在 ExecMesh 时的跨项目提醒（见 useCrossModeNotice）。 */
  function emitRequest(request: RelayRequest) {
    lastRequest.value = { ...request, seq: ++requestSeq }
  }

  return {
    soundEnabled,
    desktopEnabled,
    desktopReady,
    permission,
    lastRequest,
    toggleSound,
    toggleDesktop,
    refreshPermission,
    playAlert,
    showSystem,
    emitRequest,
  }
})
