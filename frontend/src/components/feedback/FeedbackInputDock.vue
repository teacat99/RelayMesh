<script setup lang="ts">
import { ref, computed, onMounted, onBeforeUnmount, onUnmounted, watch, nextTick, h } from 'vue'
import { useMediaQuery, useResizeObserver } from '@vueuse/core'
import { appendDescription, appendPlaceholder } from '../../lib/inbox'
import { useSessionStore } from '../../stores/session'
import { usePresence } from '../../composables/usePresence'
import { useSettingsStore } from '../../stores/settings'
import ImageUploader from '../ImageUploader.vue'
import ViewTrialLinks from './ViewTrialLinks.vue'
import Button from '../ui/button/Button.vue'
import {
  Send,
  SlidersHorizontal,
  ArrowDown,
  ImagePlus,
  RotateCcw,
  MoveVertical,
  Mic,
  MicOff,
  ChevronDown,
  FileText,
  ChevronLeft,
  ChevronRight,
  Plus,
  Trash2,
  X,
  CheckSquare,
  Square,
  MessageSquareQuote,
  ImageOff,
  CloudUpload,
  CloudOff
} from 'lucide-vue-next'
import QuickPresetEditDialog from './QuickPresetEditDialog.vue'
import QuoteChips from './QuoteChips.vue'
import PhaseSlider from './PhaseSlider.vue'
import SessionExecChip from './SessionExecChip.vue'
import ReplyWindowChip from './ReplyWindowChip.vue'
import AgentStatusChip from './AgentStatusChip.vue'
import SessionQuickRun from './SessionQuickRun.vue'
import SessionEnableAgent from './SessionEnableAgent.vue'
import InboxStatusChip from './InboxStatusChip.vue'
import AuthProfileChip from './AuthProfileChip.vue'
import ConfirmPendingChip from '../confirms/ConfirmPendingChip.vue'
import type { QuickPresetItem } from '../../stores/settings'
import type { SessionImage } from '../../api/types'
import { draftsApi } from '../../api/client'
import {
  computeImageKey,
  createDraftPersister,
  draftSignature,
  draftStateHash,
  ensureImageKeys,
  hasUnloadedImages,
  isImageUnloaded
} from '../../utils/draftSync'
import { hydrateDraftImages } from '../../utils/draftImages'
import { sharedDraftImages } from '../../utils/draftImageStore'
import { sharedDraftImageCloud } from '../../utils/draftImageCloudShared'
import { imageKeysOf, serializeDraftForServer, unreferencedKeys, type CloudImageState } from '../../utils/draftImageCloud'
import { contentSignature, decideAdopt, isBlankUnsynced, parseServerTime } from '../../utils/draftAdopt'
import { insertImageRef, moveMapper, remapImageRefs, removalMapper } from '../../utils/imageRefs'
import { insertQuoteRef } from '../../lib/replyRef'
import { useReplyQuoteStore } from '../../stores/replyQuote'
import { isTouchPrimaryInput, moveCaretToEnd } from '../../utils/textareaCaret'
import { VoiceRecorderStreamer } from '../../utils/voiceStream'
import { usePreviewStore } from '../../stores/preview'
import { showPhaseSlider } from '../../lib/asyncMode'
import { toast, toastDestructive } from '@/components/ui/sonner'

const props = withDefaults(defineProps<{
  isScrolledUp?: boolean
  isSubmitting?: boolean
  placeholder?: string
  buttonText?: string
  workflowId?: string
}>(), {
  isScrolledUp: false,
  isSubmitting: false,
  placeholder: '',
  buttonText: '',
  workflowId: ''
})

const emit = defineEmits<{
  (e: 'submit', data: { text: string; presets: string[]; images: SessionImage[] }): void
  (e: 'scroll-to-bottom'): void
  (e: 'open-settings'): void
  (e: 'resize-delta', delta: number): void
}>()

const sessionStore = useSessionStore()
const settingsStore = useSettingsStore()
const previewStore = usePreviewStore()

// Input State
const responseText = ref('')
const images = ref<SessionImage[]>([])
const selectedPresets = ref<string[]>([])
const fileInputRef = ref<HTMLInputElement | null>(null)

// 响应式绑定工作流 ID（以传入的 props.workflowId 或用户当前选中的 selectedSession 为真源）
const currentBindingWorkflowId = computed(() => {
  if (props.workflowId) return props.workflowId
  return sessionStore.selectedSession?.workflow_id || sessionStore.selectedSession?.session_id || sessionStore.currentSession?.workflow_id || sessionStore.currentSession?.session_id || 'default'
})

// 当前会话（执行状态块 SessionExecChip 自己读取会话与计时，这里只需要知道有没有会话）
const activeSession = computed(() => sessionStore.selectedSession || sessionStore.currentSession)

const phaseSliderRef = ref<InstanceType<typeof PhaseSlider> | null>(null)
// 异步对话真正生效（开关开着 且 hook 在线）时隐藏阶段滑块、显示授权档位；降级回同步时滑块自动回来
const phaseSliderVisible = computed(() => showPhaseSlider(sessionStore.asyncMode))
const currentWorkflowId = computed(() => {
  return activeSession.value?.workflow_id || activeSession.value?.session_id || sessionStore.currentSession?.workflow_id || sessionStore.currentSession?.session_id || sessionStore.selectedSession?.workflow_id || sessionStore.selectedSession?.session_id || ''
})

// Feedback input panel resizing state with LocalStorage persistence
const DOCK_HEIGHT_STORAGE_KEY = 'relaymesh_input_dock_height'
// 手机宽度的默认高度矮一些：160px 在 700px 高的真机视口里占去 23%，会话主区被挤到不足一半（#155）；
// 拖动调过高度的人仍按本机记忆的值，这里只影响没调过的默认。
const PHONE_MEDIA = '(max-width: 767.98px)'
const DEFAULT_INPUT_HEIGHT = typeof window !== 'undefined' && window.matchMedia?.(PHONE_MEDIA).matches ? 128 : 160
const MIN_INPUT_HEIGHT = 100
const MAX_INPUT_HEIGHT = 1000
const inputDockHeight = ref(DEFAULT_INPUT_HEIGHT)
const isDraggingResize = ref(false)
let dragStartY = 0
let dragStartHeight = 0
let lastEmittedHeight = DEFAULT_INPUT_HEIGHT
// 用户拖出来 / 本机记住的「想要的高度」。输入栏的实际高度（inputDockHeight）是它夹在当前视口允许范围内的结果：
// 视口临时变矮（手机弹出输入法、浏览器工具栏伸缩）时只缩小实际高度，不改写本机记忆；视口恢复后自动回到想要的高度（#198）。
let preferredDockHeight = DEFAULT_INPUT_HEIGHT

function getMaxAllowedDockHeight(): number {
  if (typeof window === 'undefined') return MAX_INPUT_HEIGHT
  // 在移动端或小视口屏幕（高度小于 650px），输入栏最大高度限制为视口的 80%，避免完全遮蔽对话视口
  if (window.innerHeight < 650) {
    return Math.max(MIN_INPUT_HEIGHT, Math.floor(window.innerHeight * 0.8))
  }
  // 在普通屏幕上，最高不超过视口减去顶部 Header (60px) 和最小消息阅读区 (100px)，同时上限为 MAX_INPUT_HEIGHT (1000px)
  const dynamicMax = Math.max(MIN_INPUT_HEIGHT, window.innerHeight - 160)
  return Math.min(MAX_INPUT_HEIGHT, dynamicMax)
}

function loadSavedDockHeight() {
  const maxAllowed = getMaxAllowedDockHeight()
  try {
    const saved = localStorage.getItem(DOCK_HEIGHT_STORAGE_KEY)
    if (saved) {
      const h = parseInt(saved, 10)
      if (!isNaN(h)) {
        preferredDockHeight = Math.max(MIN_INPUT_HEIGHT, Math.min(MAX_INPUT_HEIGHT, h))
        inputDockHeight.value = Math.max(MIN_INPUT_HEIGHT, Math.min(maxAllowed, preferredDockHeight))
        lastEmittedHeight = inputDockHeight.value
      }
    } else {
      preferredDockHeight = DEFAULT_INPUT_HEIGHT
      inputDockHeight.value = Math.min(DEFAULT_INPUT_HEIGHT, maxAllowed)
      lastEmittedHeight = inputDockHeight.value
    }
  } catch (_) {}
  document.documentElement.style.setProperty('--input-dock-height', `${inputDockHeight.value}px`)
}

function saveDockHeight(h: number) {
  try {
    localStorage.setItem(DOCK_HEIGHT_STORAGE_KEY, String(h))
    document.documentElement.style.setProperty('--input-dock-height', `${h}px`)
  } catch (_) {}
}

function startResizeDrag(e: PointerEvent) {
  isDraggingResize.value = true
  dragStartY = e.clientY
  dragStartHeight = inputDockHeight.value
  lastEmittedHeight = dragStartHeight

  const target = e.currentTarget as HTMLElement
  if (target && target.setPointerCapture) {
    try {
      target.setPointerCapture(e.pointerId)
    } catch (_) {}
  }

  window.addEventListener('pointermove', handleResizePointerMove)
  window.addEventListener('pointerup', handleResizePointerUp)
  window.addEventListener('pointercancel', handleResizePointerUp)
  document.body.style.userSelect = 'none'
  document.body.style.cursor = 'row-resize'
}

function handleResizePointerMove(e: PointerEvent) {
  if (!isDraggingResize.value) return
  const deltaFromStart = dragStartY - e.clientY
  const maxAllowed = getMaxAllowedDockHeight()
  const newH = Math.min(maxAllowed, Math.max(MIN_INPUT_HEIGHT, dragStartHeight + deltaFromStart))
  const delta = newH - lastEmittedHeight
  if (delta !== 0) {
    inputDockHeight.value = newH
    lastEmittedHeight = newH
    emit('resize-delta', delta)
  }
}

function handleResizePointerUp(e: PointerEvent) {
  if (!isDraggingResize.value) return
  isDraggingResize.value = false
  preferredDockHeight = inputDockHeight.value
  saveDockHeight(inputDockHeight.value)

  const target = e.currentTarget as HTMLElement
  if (target && target.releasePointerCapture) {
    try {
      target.releasePointerCapture(e.pointerId)
    } catch (_) {}
  }

  window.removeEventListener('pointermove', handleResizePointerMove)
  window.removeEventListener('pointerup', handleResizePointerUp)
  window.removeEventListener('pointercancel', handleResizePointerUp)
  document.body.style.userSelect = ''
  document.body.style.cursor = ''
}

function resetInputHeight() {
  const delta = DEFAULT_INPUT_HEIGHT - inputDockHeight.value
  inputDockHeight.value = DEFAULT_INPUT_HEIGHT
  lastEmittedHeight = DEFAULT_INPUT_HEIGHT
  preferredDockHeight = DEFAULT_INPUT_HEIGHT
  saveDockHeight(DEFAULT_INPUT_HEIGHT)
  if (delta !== 0) {
    emit('resize-delta', delta)
  }
}

// ==========================================
// 多草稿箱体系 (最多5个草稿，支持 Ctrl+←/→ 平移动画切换)
// ==========================================
export interface DraftSlot {
  id: string
  text: string
  presets: string[]
  images: SessionImage[]
  updated_at: number
}

export interface MultiDraftState {
  activeIndex: number
  drafts: DraftSlot[]
}

let isResetting = false
const slideDirection = ref<'slide-left' | 'slide-right'>('slide-left')

const multiDrafts = ref<MultiDraftState>({
  activeIndex: 0,
  drafts: [
    { id: '1', text: '', presets: [], images: [], updated_at: Date.now() }
  ]
})

function getDraftStorageKey(workflowId: string): string {
  return `relaymesh_multidrafts_wf_${workflowId}`
}

// 「这份内容已经同步到服务端」的标记键：值是内容哈希，载入时若与本地内容一致就不必再上传一遍
function getDraftSyncedMarkKey(workflowId: string): string {
  return `relaymesh_multidrafts_synced_wf_${workflowId}`
}

// 上一次与服务端同步（上传成功 / 采纳）时服务端记录的草稿更新时间（毫秒）：据此判断服务端那份是不是被另一台设备更新过（utils/draftAdopt.ts）
function getDraftSyncedAtKey(workflowId: string): string {
  return `relaymesh_multidrafts_synced_at_wf_${workflowId}`
}

// 输入区（responseText / selectedPresets / images）与 multiDrafts 当前所属的工作流。
// 只在 loadDrafts 里更新，所有落盘都以它为准，不再在保存时用响应式状态临时推导：
// 切换工作流的瞬间，推导出来的已经是新工作流，而输入区里还是旧工作流的内容，旧实现因此会把旧草稿写进新工作流的存储键。
let boundWorkflowId = ''
let loadSeq = 0

// 本地大图仓库（IndexedDB）：localStorage 放不下的大图正文存在这里，刷新后秒级取回；没有 IndexedDB 时为 null（只靠服务端草稿兜底）。
// 详见 utils/draftImages.ts：刷新后图片「变空图」并在下一次敲字时覆盖服务端副本，正是旧实现丢图的真实链路。
const { store: draftImageStore, sync: draftImageSync } = sharedDraftImages()

// 草稿图片云存储：图片一加入草稿就上传到服务器（以内容键为身份、幂等），草稿 JSON 里只留引用，
// 提交时服务端直接复用，草稿销毁时删除；换设备 / 清了浏览器数据也能从云端取回（详见 utils/draftImageCloud.ts）。
// 登记是页面级单例，这里只订阅它的变化。
const { cloud: draftImageCloud, subscribe: subscribeDraftImageCloud } = sharedDraftImageCloud()
// 登记变化时递增，驱动缩略图上的上传状态重绘
const cloudVersion = ref(0)
let unsubscribeDraftImageCloud: (() => void) | null = null

// 同步失败要让用户看得见：旧实现只有一行 console.warn，用户无从得知服务端那份草稿其实没有更新。
// 只在「由好变坏」时提示一次，之后按 15s → 30s → … → 2min 自动重试，恢复时再提示一次。
let remoteFailureNotified = false
let localFailureNotified = false
let remoteRetryTimer: ReturnType<typeof setTimeout> | null = null
let remoteRetryDelay = 15_000

function scheduleRemoteRetry(wId: string) {
  if (remoteRetryTimer !== null) return
  remoteRetryTimer = setTimeout(() => {
    remoteRetryTimer = null
    draftPersister.flush(wId)
  }, remoteRetryDelay)
  remoteRetryDelay = Math.min(remoteRetryDelay * 2, 120_000)
}

// 落盘引擎（去抖 + 内容签名跳过未变化 + 上传串行，详见 utils/draftSync.ts）
const draftPersister = createDraftPersister({
  writeLocal: (wId, json) => localStorage.setItem(getDraftStorageKey(wId), json),
  upload: (wId, activeIndex, json) => draftsApi.save(wId, activeIndex, json),
  // 已经上传到云端图片存储的图片，草稿 JSON 里只留引用，不再内联几 MB 的 Base64（没传成功的继续内联，保证降级可用）
  serializeRemote: serializeDraftForServer,
  // 大图正文放进本地大图仓库；仓库自己的失败只记日志，不影响草稿本身
  afterLocal: (wId, state) => {
    void draftImageSync?.afterLocal(wId, state).catch(() => {})
  },
  // INV-D5：还有图片正文没载入就绝不上传，否则空壳会把服务端完整的图片覆盖掉；
  // 图片还在上传到云端时也先等一等（上传完成会重新评估），这样草稿 JSON 一次就能带上引用，而不是先把正文内联传一遍
  canUpload: (state, wId) => !hasUnloadedImages(state) && !draftImageCloud.hasUploading(wId, state),
  onUploaded: (wId, hash, result) => {
    try {
      localStorage.setItem(getDraftSyncedMarkKey(wId), hash)
      // 记下服务端为这份草稿记录的更新时间：下次载入时据此判断服务端是否被别的设备更新过
      const at = parseServerTime((result as { draft?: { updated_at?: string } } | undefined)?.draft?.updated_at)
      if (at !== null) localStorage.setItem(getDraftSyncedAtKey(wId), String(at))
    } catch (_) {}
    remoteRetryDelay = 15_000
    if (remoteFailureNotified) {
      remoteFailureNotified = false
      toast.success('草稿已重新同步到服务器')
    }
  },
  onError: (kind, wId, err) => {
    if (kind === 'remote') {
      console.warn(`Failed to sync draft to server db (${wId}):`, err)
      if (!remoteFailureNotified) {
        remoteFailureNotified = true
        toast.warning('草稿暂时没能同步到服务器', {
          description: '内容仍保存在本机，网络恢复后会自动重试。'
        })
      }
      scheduleRemoteRetry(wId)
      return
    }
    console.warn(`Failed to write draft to local storage (${wId}):`, err)
    if (!localFailureNotified) {
      localFailureNotified = true
      toast.warning('本机草稿缓存写入失败', {
        description: '浏览器存储空间可能已满；草稿仍会同步到服务器，请在刷新页面前确认没有「同步失败」的提示。'
      })
    }
  },
})

function createEmptyDrafts(): MultiDraftState {
  return {
    activeIndex: 0,
    drafts: [{ id: '1', text: '', presets: [], images: [], updated_at: Date.now() }]
  }
}

function parseStoredDrafts(raw: string | null | undefined): MultiDraftState | null {
  if (!raw) return null
  try {
    const data = JSON.parse(raw)
    if (data && Array.isArray(data.drafts) && data.drafts.length > 0) {
      const idx = data.activeIndex
      return {
        activeIndex: typeof idx === 'number' && idx >= 0 && idx < data.drafts.length ? idx : 0,
        drafts: data.drafts
      }
    }
  } catch (_) {}
  return null
}

// 把输入区里的内容写回当前草稿卡槽（只是内存赋值，不做任何序列化）
function syncActiveSlotFromInputs() {
  const slot = multiDrafts.value.drafts[multiDrafts.value.activeIndex]
  if (!slot) return
  slot.text = responseText.value
  slot.presets = selectedPresets.value
  slot.images = images.value
  slot.updated_at = Date.now()
}

// 落盘引擎冲刷时读取内容的入口：只有输入区仍然属于 wId 时才返回，否则返回 null 让本次冲刷作废，绝不写错地方
function readBoundDrafts(wId: string): MultiDraftState | null {
  if (boundWorkflowId !== wId) return null
  syncActiveSlotFromInputs()
  // 旧草稿里的图片没有内容键（新加入的图片在加入时已经有了），补上之后才能进本地大图仓库、也才能被可靠地认出来
  ensureImageKeys(multiDrafts.value)
  return multiDrafts.value
}

function saveDrafts() {
  if (isResetting) return
  const wId = boundWorkflowId
  if (!wId) return
  syncActiveSlotFromInputs()
  // 只登记「有变化」；本地写入 300ms 去抖、服务端 1.5s 去抖，内容没变化则都不执行
  draftPersister.touch(wId, () => readBoundDrafts(wId))
}

// 显式操作（重置、删除、撤销删除）以及离开页面时需要立即落盘
function flushDrafts(wId: string = boundWorkflowId) {
  if (!wId) return
  draftPersister.touch(wId, () => readBoundDrafts(wId))
  draftPersister.flush(wId)
}

// ==========================================
// 草稿图片云存储：登记同步 / 上传 / 释放
// ==========================================

// 云端登记变化 → 同步到内存里图片的 synced 标记（草稿 JSON 据此决定带引用还是内联正文）
function applyCloudState(wId: string, key: string, state: CloudImageState | null) {
  if (wId !== boundWorkflowId) return
  const uploaded = state === 'uploaded'
  const mark = (img: SessionImage) => {
    if (img.key === key && !!img.synced !== uploaded) img.synced = uploaded
  }
  for (const slot of multiDrafts.value.drafts) for (const img of slot.images || []) mark(img)
  for (const img of images.value) mark(img)
  cloudVersion.value++
  // 上传结束（成功或放弃）后重新评估草稿 JSON 的上传：此前它被「还有图片在上传」的闸门挡着
  if (state !== 'uploading') saveDrafts()
}

// 按云端登记重新核对内存里每张图片的 synced 标记（载入、撤销删除、采纳别处的草稿之后调用）
function reconcileSyncedFlags() {
  const wId = boundWorkflowId
  if (!wId) return
  const sync = (img: SessionImage) => {
    const uploaded = !!img.key && draftImageCloud.isUploaded(wId, img.key)
    if (!!img.synced !== uploaded) img.synced = uploaded
  }
  for (const slot of multiDrafts.value.drafts) for (const img of slot.images || []) sync(img)
  for (const img of images.value) sync(img)
  cloudVersion.value++
}

// 已经读过云端图片键的工作流：读完之前不上传，否则刷新页面 / 切换工作流时会把云端早就有的图片再传一遍
let cloudReadyWorkflow = ''

// 确保草稿里所有已载入正文的图片都在云端（幂等：已上传 / 正在上传的不会重复传）
function ensureCloudImages() {
  const wId = boundWorkflowId
  if (!wId || cloudReadyWorkflow !== wId) return
  syncActiveSlotFromInputs()
  ensureImageKeys(multiDrafts.value)
  void draftImageCloud.ensureAll(wId, multiDrafts.value)
}

// 缩略图上的云端状态：只有「上传中」和「没传上去」需要让用户看见
function cloudBadge(img: SessionImage): 'uploading' | 'failed' | null {
  void cloudVersion.value
  if (!img.key || !img.data || img.lost) return null
  const state = draftImageCloud.state(boundWorkflowId, img.key)
  return state === 'uploading' || state === 'failed' ? state : null
}

function retryCloudUpload(img: SessionImage) {
  if (boundWorkflowId) void draftImageCloud.ensure(boundWorkflowId, img)
}

// 草稿不再引用的云端图片 → 删除。删之前核对一次引用：同一张图可能还在别的卡槽里
function releaseUnreferenced(wId: string, candidateKeys: string[]) {
  if (!wId || candidateKeys.length === 0) return
  let state: MultiDraftState | null = null
  if (wId === boundWorkflowId) {
    syncActiveSlotFromInputs()
    state = multiDrafts.value
  } else {
    try {
      state = parseStoredDrafts(localStorage.getItem(getDraftStorageKey(wId)))
    } catch (_) {}
  }
  // 读不到那个工作流的草稿就不删：宁可留给服务端的后台清理，也不能误删仍被引用的图片
  if (!state) return
  const keys = unreferencedKeys(candidateKeys, state)
  if (keys.length > 0) void draftImageCloud.release(wId, keys)
}

// 延迟释放：删除草稿要留出撤销窗口，提交之后输入区被清空而图片还要等服务端消耗，都不能立刻删
const pendingReleases = new Map<string, { wId: string; key: string; timer: ReturnType<typeof setTimeout> }>()

function scheduleRelease(wId: string, keys: string[], delayMs: number) {
  for (const key of keys) {
    const id = `${wId}\u0001${key}`
    const previous = pendingReleases.get(id)
    if (previous) clearTimeout(previous.timer)
    const timer = setTimeout(() => {
      pendingReleases.delete(id)
      releaseUnreferenced(wId, [key])
    }, delayMs)
    pendingReleases.set(id, { wId, key, timer })
  }
}

function cancelScheduledRelease(wId: string, keys: string[]) {
  for (const key of keys) {
    const id = `${wId}\u0001${key}`
    const previous = pendingReleases.get(id)
    if (previous) {
      clearTimeout(previous.timer)
      pendingReleases.delete(id)
    }
  }
}

// 组件卸载时不再等：此刻输入区的状态仍然有效，核对引用后立即释放
function runPendingReleasesNow() {
  const all = Array.from(pendingReleases.values())
  pendingReleases.clear()
  for (const { wId, key, timer } of all) {
    clearTimeout(timer)
    releaseUnreferenced(wId, [key])
  }
}

// 草稿被删除后留给「撤销」的时间；提交后图片等服务端消耗的时间
const DRAFT_DELETE_UNDO_MS = 8_000
const SUBMITTED_IMAGE_GRACE_MS = 60_000

// 补回「正文缺失」的图片：刷新后本地副本里的大图只是空壳（localStorage 放不下），
// 依次从本地大图仓库、云端草稿图片、服务端草稿取回；都没有才确认丢失，并明确提示，而不是静默变成空图。
// 图片没补齐之前，上传被闸门挡住（INV-D5），否则下一次敲字就会用空壳覆盖服务端完整的图片。
// remoteInSync：服务端那份与当前内容完全一致（刚采纳了服务端草稿），补回正文之后无需再传一遍。
async function hydrateImagesFor(wId: string, seq: number, prefetched?: MultiDraftState | null, remoteInSync = false) {
  const isStale = () => seq !== loadSeq || boundWorkflowId !== wId
  if (!hasUnloadedImages(multiDrafts.value)) return

  const result = await hydrateDraftImages(multiDrafts.value, {
    workflowId: wId,
    store: draftImageStore,
    fetchRemote: async () => {
      if (prefetched !== undefined) return prefetched
      const res = await draftsApi.get(wId)
      return parseStoredDrafts(res?.draft?.drafts_json)
    },
    // 云端草稿图片存储：换设备 / 浏览器清了本地数据时，图片靠它找回
    fetchCloud: key => draftImageCloud.fetch(wId, key),
    isStale,
    // 云端 / 服务端兜底补回的大图也写进本地仓库，下次刷新就不用再下载
    onRecovered: img => {
      // 从云端取回的图片证明它在服务器上，登记为已上传（草稿 JSON 只需带引用）
      if (img.synced && img.key) draftImageCloud.markUploaded(wId, img.key)
      void draftImageSync?.stash(wId, img, { force: true }).catch(() => {})
    },
  })
  if (result.stale || isStale()) return
  // 补回的图片可能不在云端（旧草稿把正文内联在草稿 JSON 里）：核对标记并补传
  reconcileSyncedFlags()
  ensureCloudImages()

  if (result.hydrated > 0 || result.lost > 0) {
    // 载入后的内容若与上次上传的一致就不必再传；不一致（例如载入期间用户改了文字）由下面的冲刷补传
    let marker: string | null = null
    try {
      marker = localStorage.getItem(getDraftSyncedMarkKey(wId))
    } catch (_) {}
    syncActiveSlotFromInputs()
    if (remoteInSync) {
      draftPersister.markPersisted(wId, multiDrafts.value, { local: false, remote: true })
    } else {
      draftPersister.markPersisted(wId, multiDrafts.value, { local: false, remoteSyncedHash: marker })
    }
    flushDrafts(wId)
  }
  if (result.lost > 0) {
    toast.warning(`有 ${result.lost} 张草稿图片已丢失`, {
      description: '本机和服务器都没有找到这些图片的内容，缩略图已标红，请重新粘贴或直接移除。',
      duration: 10_000
    })
  }
  if (result.unavailable > 0) {
    toast.warning('草稿图片暂时无法载入', {
      description: '没能连上服务器取回图片。已保留占位，并暂停同步草稿，避免覆盖服务器上的副本。',
      duration: 10_000,
      action: {
        label: '重试',
        onClick: () => {
          if (boundWorkflowId === wId) void hydrateImagesFor(wId, loadSeq)
        }
      }
    })
  }
}

// 记录「本机内容已与服务端一致」：同步标记（内容哈希）+ 服务端当时记录的更新时间。
// 下次载入时据此判断本机有没有未同步的改动、服务端是否被别的设备更新过（utils/draftAdopt.ts）。
function recordSynced(wId: string, serverAt: number | null) {
  syncActiveSlotFromInputs()
  ensureImageKeys(multiDrafts.value)
  try {
    localStorage.setItem(getDraftSyncedMarkKey(wId), draftStateHash(multiDrafts.value))
    if (serverAt !== null) localStorage.setItem(getDraftSyncedAtKey(wId), String(serverAt))
  } catch (_) {}
}

// 看服务端有没有「另一台设备写的更新草稿」。最后写入者胜，但绝不覆盖本机尚未同步的改动（决策规则见 utils/draftAdopt.ts）：
// - 本机没有记录 → 采纳服务端草稿；
// - 本机自上次同步以来没有改动、服务端更新且内容不同 → 采纳；
// - 本机有未同步的改动 → 保留本机（之后的上传以本机为准）。
async function adoptNewerRemoteDraft(wId: string, seq: number, hasLocalRecord: boolean): Promise<void> {
  const isStale = () => seq !== loadSeq || boundWorkflowId !== wId
  syncActiveSlotFromInputs()
  const signatureBefore = draftSignature(multiDrafts.value)
  let res: Awaited<ReturnType<typeof draftsApi.get>> | null = null
  try {
    res = await draftsApi.get(wId)
  } catch (err) {
    console.warn('Failed to fetch draft from server db:', err)
    return
  }
  // 等待期间已经切走：这份服务端草稿属于别的工作流的旧绑定，直接丢弃
  if (isStale()) return
  syncActiveSlotFromInputs()
  // 等待期间用户已经在输入：以用户输入为准，不用服务端内容覆盖
  const touched = draftSignature(multiDrafts.value) !== signatureBefore

  const row = res?.draft
  const serverState = row?.drafts_json ? parseStoredDrafts(row.drafts_json) : null
  const serverAt = serverState ? (parseServerTime(row?.updated_at) ?? 0) : null
  if (serverState) ensureImageKeys(serverState)
  ensureImageKeys(multiDrafts.value)

  let marker: string | null = null
  let syncedAt: number | null = null
  try {
    marker = localStorage.getItem(getDraftSyncedMarkKey(wId))
    const raw = localStorage.getItem(getDraftSyncedAtKey(wId))
    const parsed = raw === null ? NaN : Number(raw)
    syncedAt = Number.isFinite(parsed) ? parsed : null
  } catch (_) {}

  const decision = decideAdopt({
    serverUpdatedAt: serverAt,
    syncedAt,
    localClean: marker !== null && marker === draftStateHash(multiDrafts.value),
    noLocalRecord: !hasLocalRecord,
    sameContent: !!serverState && contentSignature(serverState) === contentSignature(multiDrafts.value),
    blankUnsynced: isBlankUnsynced(multiDrafts.value, marker),
    touched,
  })
  if (decision.action === 'adopt' && serverState) {
    multiDrafts.value = serverState
    const cur = serverState.drafts[serverState.activeIndex] || serverState.drafts[0]
    responseText.value = cur.text || ''
    selectedPresets.value = cur.presets || []
    images.value = cur.images || []
    draftPersister.markPersisted(wId, multiDrafts.value, { local: false, remote: true })
    flushDrafts(wId)
    // 采纳下来的图片可能只是引用（正文在云端）或旧版本留下的空壳：补回正文；补回之后内容哈希才是最终值，所以最后再记录同步标记
    await hydrateImagesFor(wId, seq, serverState, true)
    if (isStale()) return
    recordSynced(wId, serverAt)
    if (hasLocalRecord) {
      toast.info('已载入另一台设备上更新的草稿', {
        description: '这个工作流的草稿在别处被修改过，本机没有未同步的改动，所以已自动更新。'
      })
    }
    return
  }
  if (decision.reason === 'same_content' && serverAt !== null) {
    // 内容一致：不用传，只刷新同步标记，下次载入就能认出「本机没有未同步的改动」
    draftPersister.markPersisted(wId, multiDrafts.value, { local: false, remote: true })
    recordSynced(wId, serverAt)
  }
}

// 载入之后的云端对账：云端已有哪些图片 → 补回正文缺失的图片 → 服务端是否有更新的草稿 → 补传云端还没有的图片。
async function reconcileAfterLoad(wId: string, seq: number, hasLocalRecord: boolean): Promise<void> {
  const isStale = () => seq !== loadSeq || boundWorkflowId !== wId
  try {
    // 读一次云端已有的图片键：已经在云端的不再重复上传（读取失败不致命，ensure 是幂等的）
    await draftImageCloud.loadRemoteKeys(wId)
    if (isStale()) return
    cloudReadyWorkflow = wId
    reconcileSyncedFlags()
    await hydrateImagesFor(wId, seq)
    if (isStale()) return
    await adoptNewerRemoteDraft(wId, seq, hasLocalRecord)
    if (isStale()) return
    reconcileSyncedFlags()
    ensureCloudImages()
  } catch (err) {
    console.warn('Draft reconcile failed:', err)
  }
}

// 切回页面时再看一眼服务端：另一台设备上写的草稿能在这里自动出现，而不必等到刷新页面
let lastRemoteCheckAt = 0
const REMOTE_RECHECK_MIN_INTERVAL_MS = 20_000

function recheckRemoteDraft() {
  const wId = boundWorkflowId
  if (!wId || props.isSubmitting || isResetting) return
  if (Date.now() - lastRemoteCheckAt < REMOTE_RECHECK_MIN_INTERVAL_MS) return
  // 本机有待落盘 / 待上传的改动，或图片还没载入完：不是对账的时候
  if (draftPersister.hasPending(wId) || hasUnloadedImages(multiDrafts.value)) return
  lastRemoteCheckAt = Date.now()
  void adoptNewerRemoteDraft(wId, loadSeq, true).then(() => {
    if (wId === boundWorkflowId) ensureCloudImages()
  })
}

async function loadDrafts(forceWorkflowId?: string) {
  const wId = forceWorkflowId || currentBindingWorkflowId.value
  const wKey = getDraftStorageKey(wId)
  const seq = ++loadSeq
  // 先切换绑定：此后所有保存的目标都是新工作流
  boundWorkflowId = wId

  // 彻底清除旧版本遗留的全局污染草稿键，避免跨会话乱恢复（载入时清理一次即可，不必在每次按键时做）
  try {
    localStorage.removeItem('relaymesh_active_draft_content')
    localStorage.removeItem('relaymesh_draft_global')
  } catch (_) {}

  // 1. 本地快速恢复 (0 延迟即时呈现)
  let loaded: MultiDraftState | null = null
  let migratedFromLegacy = false
  try {
    loaded = parseStoredDrafts(localStorage.getItem(wKey))
    if (!loaded) {
      // 检查旧单草稿格式
      const oldKey = `relaymesh_draft_wf_${wId}`
      const oldRaw = localStorage.getItem(oldKey)
      if (oldRaw) {
        migratedFromLegacy = true
        const oldData = JSON.parse(oldRaw)
        loaded = {
          activeIndex: 0,
          drafts: [
            {
              id: '1',
              text: oldData.text || '',
              presets: Array.isArray(oldData.presets) ? oldData.presets : [],
              images: Array.isArray(oldData.images) ? oldData.images : [],
              updated_at: oldData.updated_at || Date.now()
            }
          ]
        }
        localStorage.removeItem(oldKey)
      }
    }
  } catch (_) {}
  const restoredLocally = loaded !== null

  // 无草稿（或记录已损坏）时生成 1 个干净空草稿；无论如何都要整体替换，绝不能让上一个工作流的内容残留在输入区
  multiDrafts.value = loaded || createEmptyDrafts()
  const current = multiDrafts.value.drafts[multiDrafts.value.activeIndex] || multiDrafts.value.drafts[0]
  responseText.value = current.text || ''
  selectedPresets.value = current.presets || []
  images.value = current.images || []

  if (restoredLocally) {
    // 本地内容就是刚读到的内容，无需重复写；服务端是否已一致取决于上次上传成功后留下的标记
    let marker: string | null = null
    try {
      marker = localStorage.getItem(getDraftSyncedMarkKey(wId))
    } catch (_) {}
    // 旧格式迁移得来的内容还不在新键里（旧键已删除），必须立刻落盘，否则在下一次编辑之前刷新就会丢草稿。
    // 从未同步过的空白草稿（打开页面 / 刷新时自动落下的空壳）不是用户的改动：按「服务端已一致」登记，
    // 否则它会被当作未同步的本机改动，下一次上传就用空白覆盖掉另一台设备写在服务端的草稿。
    draftPersister.markPersisted(wId, multiDrafts.value, {
      local: !migratedFromLegacy,
      remote: isBlankUnsynced(multiDrafts.value, marker),
      remoteSyncedHash: marker,
    })
    if (migratedFromLegacy) flushDrafts(wId)
    // 后续对账（不阻塞首屏）：云端图片登记 → 补回空壳图片的正文（缩略图先显示「载入中」）
    // → 服务端是否有另一台设备写的更新草稿 → 补传云端还没有的图片
    void reconcileAfterLoad(wId, seq, true)
    return
  }

  // 空白草稿不需要往服务端传，但本地存储里还没有它，稍后需要落一份
  draftPersister.markPersisted(wId, multiDrafts.value, { local: false, remote: true })
  // 本机没有这个工作流的记录（全新设备 / 清过浏览器数据）：从服务端取草稿；等待期间用户已经开始输入则以用户输入为准
  await reconcileAfterLoad(wId, seq, false)
  if (seq !== loadSeq || boundWorkflowId !== wId) return
  // 落一份本地记录（服务端有草稿则是它，没有则是空白）
  flushDrafts(wId)
}

function switchDraft(targetIndex: number) {
  if (targetIndex < 0 || targetIndex >= multiDrafts.value.drafts.length || targetIndex === multiDrafts.value.activeIndex) {
    return
  }
  const curIdx = multiDrafts.value.activeIndex
  if (multiDrafts.value.drafts[curIdx]) {
    multiDrafts.value.drafts[curIdx].text = responseText.value
    multiDrafts.value.drafts[curIdx].presets = selectedPresets.value
    multiDrafts.value.drafts[curIdx].images = images.value
    multiDrafts.value.drafts[curIdx].updated_at = Date.now()
  }

  slideDirection.value = targetIndex > curIdx ? 'slide-left' : 'slide-right'
  multiDrafts.value.activeIndex = targetIndex

  const target = multiDrafts.value.drafts[targetIndex]
  responseText.value = target.text || ''
  selectedPresets.value = target.presets || []
  images.value = target.images || []

  saveDrafts()
}

function createNewDraft() {
  const curIdx = multiDrafts.value.activeIndex
  if (multiDrafts.value.drafts[curIdx]) {
    multiDrafts.value.drafts[curIdx].text = responseText.value
    multiDrafts.value.drafts[curIdx].presets = selectedPresets.value
    multiDrafts.value.drafts[curIdx].images = images.value
    multiDrafts.value.drafts[curIdx].updated_at = Date.now()
  }

  const newId = String(Date.now())
  multiDrafts.value.drafts.push({
    id: newId,
    text: '',
    presets: [],
    images: [],
    updated_at: Date.now()
  })

  switchDraft(multiDrafts.value.drafts.length - 1)
  toast.success(`已新建草稿 (第 ${multiDrafts.value.activeIndex + 1}/${multiDrafts.value.drafts.length} 个)`)
}

// 草稿可逆快照备份（Demand 5: 高风险操作通知提供撤销操作，保留 7000ms 快照）
let deletedDraftSnapshot: {
  draft: DraftSlot
  index: number
  totalBefore: number
  workflowId: string
} | null = null

function restoreDraftSnapshot() {
  if (!deletedDraftSnapshot) return
  const { draft, index, totalBefore, workflowId } = deletedDraftSnapshot
  // 输入区已经切到别的工作流：撤销会把草稿插进错误的工作流，宁可放弃也不能串草稿
  if (workflowId !== boundWorkflowId) {
    deletedDraftSnapshot = null
    toast.warning('该草稿所属的工作流已切换，无法撤销删除')
    return
  }

  if (totalBefore <= 1) {
    multiDrafts.value.drafts[0] = { ...draft }
    multiDrafts.value.activeIndex = 0
  } else {
    // 恢复插入到原本的位置
    const targetIdx = Math.min(multiDrafts.value.drafts.length, Math.max(0, index))
    multiDrafts.value.drafts.splice(targetIdx, 0, { ...draft })
    multiDrafts.value.activeIndex = targetIdx
  }

  responseText.value = draft.text || ''
  selectedPresets.value = draft.presets || []
  images.value = draft.images || []
  // 撤销：取消尚未执行的云端释放；已经释放掉的图片按云端登记重新核对标记，并把它们补传回去
  cancelScheduledRelease(workflowId, imageKeysOf(draft.images))
  reconcileSyncedFlags()
  ensureCloudImages()
  saveDrafts()
  flushDrafts()
  deletedDraftSnapshot = null
  toast.success('已成功撤销并恢复草稿')
}

function deleteCurrentDraft() {
  const wId = boundWorkflowId
  const curIdx = multiDrafts.value.activeIndex
  const currentDraft = multiDrafts.value.drafts[curIdx]
  // 被销毁的草稿里的图片：云端那份也要删，但要留出「撤销」的窗口
  const deletedKeys = imageKeysOf(images.value)

  // 保存当前草稿快照
  if (currentDraft) {
    deletedDraftSnapshot = {
      draft: {
        id: currentDraft.id,
        text: responseText.value || currentDraft.text || '',
        presets: [...(selectedPresets.value || currentDraft.presets || [])],
        images: [...(images.value || currentDraft.images || [])],
        updated_at: Date.now()
      },
      index: curIdx,
      totalBefore: multiDrafts.value.drafts.length,
      workflowId: wId
    }
  }

  if (multiDrafts.value.drafts.length > 1) {
    multiDrafts.value.drafts.splice(curIdx, 1)
    const nextIdx = Math.max(0, curIdx - 1)
    multiDrafts.value.activeIndex = nextIdx
    const target = multiDrafts.value.drafts[nextIdx]
    responseText.value = target.text || ''
    selectedPresets.value = target.presets || []
    images.value = target.images || []
    saveDrafts()
    toastDestructive('已删除该草稿', () => restoreDraftSnapshot())
  } else {
    responseText.value = ''
    selectedPresets.value = []
    images.value = []
    multiDrafts.value.drafts[0] = {
      id: '1',
      text: '',
      presets: [],
      images: [],
      updated_at: Date.now()
    }
    saveDrafts()
    toastDestructive('已清空当前草稿', () => restoreDraftSnapshot())
  }
  // 删除是显式操作，立即落盘
  flushDrafts(wId)
  // 撤销窗口过后，草稿不再引用的图片从云端删除（期间被撤销 / 重新粘贴同一张图则不会被删）
  scheduleRelease(wId, deletedKeys, DRAFT_DELETE_UNDO_MS)
}

// 重置输入区（提交之后清空当前草稿）。被重置的永远是输入区当前所属的工作流，落盘也以它为准；
// 参数只为兼容旧调用保留——调用方传入的可能不是草稿所属的键（例如任务视图传的是任务 ID）。
function resetForm(_targetWorkflowId?: string) {
  isResetting = true
  // 被清空的草稿里的图片：提交时服务端会按引用复用并消耗它们；没有走引用的提交（例如任务视图）留下的由这里兜底清理
  const submittedKeys = imageKeysOf(images.value)
  if (multiDrafts.value.drafts.length > 1) {
    const curIdx = multiDrafts.value.activeIndex
    multiDrafts.value.drafts.splice(curIdx, 1)
    const nextIdx = Math.max(0, curIdx - 1)
    multiDrafts.value.activeIndex = nextIdx
    const target = multiDrafts.value.drafts[nextIdx]
    responseText.value = target.text || ''
    selectedPresets.value = target.presets || []
    images.value = target.images || []
  } else {
    responseText.value = ''
    selectedPresets.value = []
    images.value = []
    multiDrafts.value.drafts[0] = {
      id: '1',
      text: '',
      presets: [],
      images: [],
      updated_at: Date.now()
    }
  }

  // 本地与服务端立即同步（提交后的空白状态必须马上落盘，不能等去抖）
  flushDrafts()
  // 提交请求紧跟在清空之后发出，服务端要按引用取走这些图片：留出宽限期再核对引用并清理，不能立刻删
  scheduleRelease(boundWorkflowId, submittedKeys, SUBMITTED_IMAGE_GRACE_MS)

  setTimeout(() => {
    isResetting = false
  }, 100)
}

function getImageUrl(img: SessionImage | null | undefined): string {
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

function loadSpecificContent(data: { text: string; presets?: string[]; images?: SessionImage[] }) {
  // 检查当前输入区是否已有内容
  const hasCurrentContent = (responseText.value && responseText.value.trim() !== '') ||
                            (selectedPresets.value && selectedPresets.value.length > 0) ||
                            (images.value && images.value.length > 0)

  if (hasCurrentContent) {
    // 1. 先安全保存当前输入区的草稿
    saveDrafts()

    // 2. 自动新建一个草稿卡槽放置被撤回的内容
    {
      const newDraftItem: DraftSlot = {
        id: Date.now().toString(),
        text: data.text || '',
        presets: data.presets || [],
        images: data.images || [],
        updated_at: Date.now()
      }
      multiDrafts.value.drafts.push(newDraftItem)
      multiDrafts.value.activeIndex = multiDrafts.value.drafts.length - 1
      responseText.value = newDraftItem.text
      selectedPresets.value = newDraftItem.presets
      images.value = newDraftItem.images
      saveDrafts()
      // 换了草稿卡槽，输入框会被整个换掉：等新输入框出现后，光标落到撤回内容的末尾
      followFilledTextWhenReady()
      return
    }
  }

  // 若当前为空或草稿槽位已满，直接载入当前卡槽
  responseText.value = data.text || ''
  selectedPresets.value = data.presets || []
  images.value = data.images || []
  saveDrafts()
  followFilledText()
}

// 打字高频触发：仅保存纯文本与预设草稿，避免任何深度遍历
watch([responseText, selectedPresets], () => {
  saveDrafts()
})

// 图片增删与调序独立监听（非高频打字，且不开启 deep 深度监听，杜绝遍历几兆字符）
watch(images, () => {
  saveDrafts()
  // 整体换了一组图片（切换草稿卡槽、载入撤回的内容）：保证其中的图片都在云端（幂等，已上传的不会重传）
  ensureCloudImages()
}, { deep: false })

// 仅当用户切换到不同的 workflow_id 时才做草稿切换；以 currentBindingWorkflowId 为唯一真源
watch(currentBindingWorkflowId, (newWId) => {
  if (!newWId || newWId === boundWorkflowId) return
  const previous = boundWorkflowId
  if (previous) {
    // 此刻输入区与 multiDrafts 仍然属于旧工作流：先把它立即落盘，再切换绑定（INV-D1 / INV-D2）
    flushDrafts(previous)
    draftPersister.release(previous)
  }
  void loadDrafts(newWId)
})

// 页面被隐藏（切标签、锁屏、关闭）时立即落盘，不等去抖
function handleDraftVisibilityChange() {
  if (document.visibilityState === 'hidden') draftPersister.flush()
  else if (document.visibilityState === 'visible') recheckRemoteDraft()
}

function handleDraftPageHide() {
  draftPersister.flush()
}

// ==========================================
// 快捷预设 Pills (支持右键编辑、自动追加规则勾选)
// ==========================================
const activeQuickPresets = computed<QuickPresetItem[]>(() => settingsStore.getNormalizedStatusPresets('online'))

const isPresetDialogOpen = ref(false)
const editingPresetItem = ref<QuickPresetItem | null>(null)
const isNewPreset = ref(false)

function openEditPresetDialog(preset: QuickPresetItem) {
  editingPresetItem.value = { ...preset }
  isNewPreset.value = false
  isPresetDialogOpen.value = true
}

function openCreatePresetDialog() {
  editingPresetItem.value = null
  isNewPreset.value = true
  isPresetDialogOpen.value = true
}

function handleSavePreset(item: QuickPresetItem) {
  settingsStore.saveStatusPresetItem('online', item)
  toast.success('已保存快捷回复预设')
}

function handleDeletePreset(id: string) {
  settingsStore.removeStatusPresetItem('online', id)
  toast.success('已删除快捷回复预设')
}

function togglePresetAppend(preset: QuickPresetItem) {
  settingsStore.toggleStatusPresetAppendActive('online', preset.id)
  if (!preset.isAppendActive) {
    toast.success(`已开启「${preset.appendTitle || preset.title}」自动追加`, {
      description: '每次人工回复时，将在末尾自动附加此规则'
    })
  } else {
    toast.info(`已关闭「${preset.appendTitle || preset.title}」自动追加`)
  }
}

function togglePreset(preset: QuickPresetItem) {
  const textToInsert = preset.content || preset.title
  const idx = selectedPresets.value.indexOf(preset.title)
  if (idx >= 0) {
    selectedPresets.value.splice(idx, 1)
  } else {
    selectedPresets.value.push(preset.title)
  }

  if (responseText.value.trim() === '') {
    responseText.value = textToInsert
    followFilledText()
  } else if (!responseText.value.includes(textToInsert)) {
    responseText.value = `${responseText.value}\n${textToInsert}`
    followFilledText()
  }
}

const draggedImageIndex = ref<number | null>(null)
const dragOverImageIndex = ref<number | null>(null)

function onImageDragStart(idx: number, e: DragEvent) {
  draggedImageIndex.value = idx
  if (e.dataTransfer) {
    e.dataTransfer.effectAllowed = 'move'
    e.dataTransfer.setData('text/plain', String(idx))
  }
}

function onImageDragOver(idx: number, e: DragEvent) {
  e.preventDefault()
  if (e.dataTransfer) {
    e.dataTransfer.dropEffect = 'move'
  }
  dragOverImageIndex.value = idx
}

function onImageDragLeave(idx: number) {
  if (dragOverImageIndex.value === idx) {
    dragOverImageIndex.value = null
  }
}

function onImageDrop(idx: number, e: DragEvent) {
  e.preventDefault()
  const from = draggedImageIndex.value
  draggedImageIndex.value = null
  dragOverImageIndex.value = null
  if (from === null || from === idx) return

  const item = images.value.splice(from, 1)[0]
  images.value.splice(idx, 0, item)
  // 文字里的「【图 N】」跟着图片走，而不是停在原来的数字上指向另一张图
  responseText.value = remapImageRefs(responseText.value, moveMapper(from, idx))
  saveDrafts()
}

function onImageDragEnd() {
  draggedImageIndex.value = null
  dragOverImageIndex.value = null
}

function triggerImageUpload() {
  fileInputRef.value?.click()
}

function removeImage(index: number) {
  const removed = images.value[index]
  images.value.splice(index, 1)
  // 指向被移除图片的标记一并删掉，后面的标记整体前移，保持「序号 = 图片当前位置」
  responseText.value = remapImageRefs(responseText.value, removalMapper(index))
  saveDrafts()
  // 用户明确移除的图片：云端那份没有人再需要了（同一张图若还在别的位置被引用则保留）
  if (removed?.key) releaseUnreferenced(boundWorkflowId, [removed.key])
}

const textareaRef = ref<HTMLTextAreaElement | null>(null)

// 输入框最近一次的光标 / 选区：点击缩略图编号、上传按钮时输入框会失去焦点，靠它仍能插回用户刚才所在的位置。
let lastSelection: { start: number; end: number } | null = null

// 正在输入时把「用户还在」告诉服务端：这次挂起的倒计时暂停（#121）
const { noteTyping } = usePresence()

function rememberSelection() {
  const el = textareaRef.value
  if (el) lastSelection = { start: el.selectionStart, end: el.selectionEnd }
}

// 按钮（快捷标签 / 预设）或语音转写往输入框里填入内容后：光标落到新内容末尾，输入框滚到最底，让刚填入的内容一定可见。
// 点按钮时焦点在按钮上，输入框不会自己跟随；新内容若正好落在可视区域下沿之外，用户就看不出已经添加。
// 桌面端同时把焦点交给输入框（接着打字就接在新内容后面）；触屏设备上聚焦会弹出软键盘，只移动光标与滚动，不抢焦点。
function followFilledText(options: { focus?: boolean } = {}) {
  void nextTick(() => {
    const el = textareaRef.value
    if (!el) return
    moveCaretToEnd(el, {
      focus: options.focus !== false && !isTouchPrimaryInput(),
      focused: document.activeElement === el
    })
    rememberSelection()
  })
}

// 载入撤回的内容时可能换了草稿卡槽：输入框整个被换掉（带过渡动画），要等新输入框挂上来才能处理光标。
// 先记下「待跟随」，新输入框一出现就跟随到末尾；万一一直没有新输入框，过一会儿自动作废，免得误伤之后的某次挂载。
let followOnNextTextarea = false
let followOnNextTextareaTimer: ReturnType<typeof setTimeout> | null = null

function followFilledTextWhenReady() {
  followOnNextTextarea = true
  if (followOnNextTextareaTimer) clearTimeout(followOnNextTextareaTimer)
  followOnNextTextareaTimer = setTimeout(() => {
    followOnNextTextarea = false
    followOnNextTextareaTimer = null
  }, 1500)
}

watch(textareaRef, (el, previous) => {
  if (!el || el === previous || !followOnNextTextarea) return
  followOnNextTextarea = false
  if (followOnNextTextareaTimer) {
    clearTimeout(followOnNextTextareaTimer)
    followOnNextTextareaTimer = null
  }
  followFilledText()
}, { flush: 'post' })

onBeforeUnmount(() => {
  if (followOnNextTextareaTimer) clearTimeout(followOnNextTextareaTimer)
})

// 在光标处插入「【图 N】」，与 MCP 回执里的图片编号一致。
// 输入框有焦点（典型场景：边打字边 Ctrl+V）用当前光标并把光标留在标记之后；没有焦点（点编号 / 上传按钮 / 拖拽）
// 用最近一次记住的光标；从未编辑过则追加到末尾。不主动抢焦点，免得移动端弹出键盘。
function insertImageRefIntoText(n: number) {
  const el = textareaRef.value
  const current = responseText.value
  const focused = !!el && document.activeElement === el
  let start = current.length
  let end = current.length
  if (focused) {
    start = el!.selectionStart
    end = el!.selectionEnd
  } else if (lastSelection) {
    start = lastSelection.start
    end = lastSelection.end
  }
  const edit = insertImageRef(current, start, end, n)
  responseText.value = edit.text
  lastSelection = { start: edit.caret, end: edit.caret }
  const atEnd = edit.caret >= edit.text.length
  if (focused) {
    void nextTick(() => {
      el!.setSelectionRange(edit.caret, edit.caret)
      // 标记落在末尾时把末尾滚进视野，别让刚插入的「【图 N】」藏在可视区域之外
      if (atEnd) el!.scrollTop = el!.scrollHeight
    })
  } else if (atEnd) {
    // 没有焦点（点缩略图编号 / 上传按钮）：同样只让末尾可见，不抢焦点
    followFilledText({ focus: false })
  }
}

// 点会话里的「引用」（轮次头 / 进度气泡 / 恢复胶囊）：在光标处插入「【回复 哈希】」这类标记（#106）。
// 与图片标记不同，引用意味着用户接下来要打字：桌面端聚焦输入框，光标落在标记（及补上的空格）之后；
// 触屏设备上聚焦会弹出软键盘，只移动光标不抢焦点（与上面按钮填入内容的约定一致）。
// 光标位置与图片标记同一套：有焦点用当前光标，否则用最近一次记住的，从未编辑过就追加到末尾。
const quoteStore = useReplyQuoteStore()
watch(() => quoteStore.tick, () => insertQuoteIntoText(quoteStore.token))

function insertQuoteIntoText(token: string) {
  if (!token) return
  const el = textareaRef.value
  const current = responseText.value
  const focused = !!el && document.activeElement === el
  let start = current.length
  let end = current.length
  if (focused) {
    start = el!.selectionStart
    end = el!.selectionEnd
  } else if (lastSelection) {
    start = lastSelection.start
    end = lastSelection.end
  }
  const edit = insertQuoteRef(current, start, end, token)
  if (edit.inserted) responseText.value = edit.text
  lastSelection = { start: edit.caret, end: edit.caret }
  void nextTick(() => {
    const target = textareaRef.value
    if (!target) return
    if (!isTouchPrimaryInput() && document.activeElement !== target) target.focus({ preventScroll: true })
    target.setSelectionRange(edit.caret, edit.caret)
    if (edit.caret >= target.value.length) target.scrollTop = target.scrollHeight
  })
}

// 换了草稿卡槽 / 工作流，旧的光标位置不再有意义
watch([() => multiDrafts.value.activeIndex, currentBindingWorkflowId], () => {
  lastSelection = null
})

async function computeImageHash(base64Data: string): Promise<string> {
  try {
    const raw = base64Data.includes(',') ? base64Data.split(',')[1] : base64Data
    const binary = atob(raw.slice(0, Math.min(raw.length, 65536)))
    const buffer = new Uint8Array(binary.length)
    for (let i = 0; i < binary.length; i++) {
      buffer[i] = binary.charCodeAt(i)
    }
    const digest = await crypto.subtle.digest('SHA-256', buffer)
    const hashArray = Array.from(new Uint8Array(digest))
    return hashArray.map(b => b.toString(16).padStart(2, '0')).join('').slice(0, 16)
  } catch {
    return Math.random().toString(36).slice(2, 18)
  }
}

function processAndAppendImage(file: File | Blob, defaultName: string) {
  const reader = new FileReader()
  reader.onload = (evt) => {
    const res = evt.target?.result as string
    if (!res) return
    const base64Data = res.split(',')[1] || res
    let format = (file.type && file.type.split('/')[1]) || 'png'
    if (format === 'jpeg') format = 'jpg'

    // 严禁缩放分辨率：100% 保持 1:1 几何原生像素不变
    const tempImg = new Image()
    tempImg.onload = async () => {
      const width = tempImg.naturalWidth || tempImg.width || 0
      const height = tempImg.naturalHeight || tempImg.height || 0
      const hash = await computeImageHash(base64Data)

      const added: SessionImage = {
        name: defaultName,
        format: format,
        data: base64Data,
        data_type: 'base64',
        hash: hash,
        width: width,
        height: height,
        // 内容键：整段正文的哈希 + 长度。刷新后本地副本里大图只剩空壳，靠它到本地大图仓库 / 服务端草稿里认回原图
        key: computeImageKey(base64Data)
      }
      images.value.push(added)
      // 不等草稿去抖落盘，立刻把大图存进本地仓库：落盘与关闭页面之间的空窗里图片也不会丢
      void draftImageSync?.stash(boundWorkflowId, added).catch(() => {})
      // 同时立即上传到服务器的草稿图片存储（粘贴即上传）：换设备也能取回，提交时服务端直接复用，草稿 JSON 里只留引用
      ensureCloudImages()
      // 文字里同步插入「【图 N】」，N 就是它在缩略图条里的序号
      insertImageRefIntoText(images.value.length)
      saveDrafts()
    }
    tempImg.src = res
  }
  reader.readAsDataURL(file)
}

function handleFileSelect(e: Event) {
  const target = e.target as HTMLInputElement
  if (!target.files || target.files.length === 0) return

  for (let i = 0; i < target.files.length; i++) {
    const file = target.files[i]
    processAndAppendImage(file, file.name)
  }
  target.value = ''
}

function handlePaste(e: ClipboardEvent) {
  if (!e.clipboardData) return
  const items = e.clipboardData.items
  for (const item of items) {
    if (item.type.startsWith('image/')) {
      const file = item.getAsFile()
      if (file) {
        processAndAppendImage(file, `paste-${Date.now()}.png`)
      }
    }
  }
}

function handleKeydown(e: KeyboardEvent) {
  // Ctrl + ← / Ctrl + → 快速平移切换草稿箱
  if ((e.ctrlKey || e.metaKey) && (e.key === 'ArrowLeft' || e.key === 'ArrowRight')) {
    e.preventDefault()
    const total = multiDrafts.value.drafts.length
    if (total <= 1) return
    const cur = multiDrafts.value.activeIndex
    if (e.key === 'ArrowLeft') {
      const prev = (cur - 1 + total) % total
      switchDraft(prev)
    } else {
      const next = (cur + 1) % total
      switchDraft(next)
    }
    return
  }

  // Ctrl + Enter 提交
  if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') {
    e.preventDefault()
    handleSubmit()
  }
}

// 动态提交按钮文本与状态
const submitButtonInfo = computed(() => {
  const sess = sessionStore.selectedSession || sessionStore.currentSession
  if (sess?.status === 'pending') {
    return {
      label: '提交反馈',
      submittingLabel: '提交中...',
      description: '向 AI 提交针对当前汇报的即时反馈'
    }
  }
  if (sess?.status === 'completed' && !sess.consumed_by_ai) {
    return {
      label: '追加反馈',
      submittingLabel: '追加中...',
      description: 'AI 尚未读取上一条反馈，将自动合并追加'
    }
  }
  return {
    label: '追加指令',
    submittingLabel: '暂存中...',
    description: appendDescription(sessionStore.inboxConfig)
  }
})

function handleSubmit() {
  // 图片正文缺失（还在载入、或确认丢失）时不能提交：否则会把一张空图当附件发出去，对方看到的是一个坏掉的图片
  const loadingCount = images.value.filter(isImageUnloaded).length
  if (loadingCount > 0) {
    toast.warning(`有 ${loadingCount} 张图片还在载入`, {
      description: '草稿图片正在从本机或服务器取回，稍等片刻再发送；一直没有结果可点缩略图上的 × 移除。'
    })
    return
  }
  const lostCount = images.value.filter(img => img.lost).length
  if (lostCount > 0) {
    toast.error(`有 ${lostCount} 张草稿图片已丢失，无法发送`, {
      description: '请重新粘贴这些图片，或点缩略图右上角的 × 移除后再发送。'
    })
    return
  }

  let mainText = responseText.value.trim()
  if (mainText === '' && selectedPresets.value.length > 0) {
    mainText = selectedPresets.value.join('；')
  }

  // 检查并自动附加所有激活的追加规则 (allowAppend && isAppendActive)
  const activeAppendRules = activeQuickPresets.value.filter(p => p.allowAppend && p.isAppendActive)
  if (activeAppendRules.length > 0) {
    const appendBlocks = activeAppendRules.map(rule => {
      const title = rule.appendTitle || rule.title
      return `【${title}】\n${rule.content}`
    }).join('\n\n')

    if (mainText !== '') {
      mainText = `${mainText}\n\n${appendBlocks}`
    } else {
      mainText = appendBlocks
    }
  }

  // 已经上传到云端的图片只带引用（draft_key），服务端直接复用同一份字节，提交时不必再传一遍；
  // 正文仍然留在这些对象上：万一服务端说引用的图片不在了，传输层会改用内联正文重发一次（api/client.ts）
  const wId = boundWorkflowId
  const outgoing = images.value.map(img =>
    img.key && draftImageCloud.isUploaded(wId, img.key)
      ? { ...img, draft_key: img.key, draft_workflow_id: wId }
      : { ...img }
  )

  emit('submit', {
    text: mainText,
    presets: [],
    images: outgoing
  })
}

// ==========================================
// Voice Streaming & Speech Recognition
// ==========================================
const voiceStreamer = new VoiceRecorderStreamer()
const isRecordingVoice = ref(false)
const isTranscribingVoice = ref(false)
const voiceInterimText = ref('')
let recognitionInstance: any = null

function toggleVoiceRecognition() {
  if (isRecordingVoice.value || isTranscribingVoice.value) {
    stopVoiceRecognition()
  } else {
    startVoiceRecognition()
  }
}

async function startVoiceRecognition() {
  const provider = settingsStore.settings.asrProvider || 'mimo'

  if (provider === 'mimo') {
    if (!settingsStore.settings.asrApiKey || !settingsStore.settings.asrApiKey.trim()) {
      toast.warning('未配置语音识别 API Key', {
        description: '请前往「设置 > 语音 ASR」填写 Xiaomi MIMO API Key，或切换为免 Key 的浏览器 WebSpeech 引擎。'
      })
      return
    }

    try {
      voiceInterimText.value = '正在录音...'
      await voiceStreamer.startRecording({
        onStart: () => {
          isRecordingVoice.value = true
          isTranscribingVoice.value = false
        },
        onError: (err) => {
          toast.error('麦克风启动失败', {
            description: err.message || String(err)
          })
          isRecordingVoice.value = false
          isTranscribingVoice.value = false
          voiceInterimText.value = ''
        }
      })
    } catch (err: any) {
      console.error('Failed to start mimo recording:', err)
      toast.error('启动录音失败', {
        description: err.message || String(err)
      })
      isRecordingVoice.value = false
      isTranscribingVoice.value = false
    }
    return
  }

  // Fallback to Web Speech API
  const SpeechRecognition = (window as any).SpeechRecognition || (window as any).webkitSpeechRecognition
  if (!SpeechRecognition) {
    toast.error('浏览器不支持 Web Speech API', {
      description: '当前浏览器不支持 Web Speech API，建议使用 Chrome/Edge 访问。'
    })
    return
  }

  try {
    const rec = new SpeechRecognition()
    rec.continuous = true
    rec.interimResults = true
    rec.lang = settingsStore.settings.speechLang || 'zh-CN'

    rec.onstart = () => {
      isRecordingVoice.value = true
      voiceInterimText.value = '正在倾听...'
    }

    rec.onresult = (event: any) => {
      let interim = ''
      let final = ''

      for (let i = event.resultIndex; i < event.results.length; ++i) {
        if (event.results[i].isFinal) {
          final += event.results[i][0].transcript
        } else {
          interim += event.results[i][0].transcript
        }
      }

      if (final) {
        if (responseText.value && !responseText.value.endsWith(' ') && !responseText.value.endsWith('\n')) {
          responseText.value += ' ' + final
        } else {
          responseText.value += final
        }
        // 听写期间焦点在麦克风按钮上：只让转写出的新内容可见，不把焦点抢走
        followFilledText({ focus: false })
      }
      voiceInterimText.value = interim
    }

    rec.onerror = (event: any) => {
      console.warn('Speech recognition error:', event.error)
      if (event.error !== 'no-speech') {
        toast.error('WebSpeech 识别异常', {
          description: event.error
        })
        stopVoiceRecognition()
      }
    }

    rec.onend = () => {
      isRecordingVoice.value = false
      voiceInterimText.value = ''
    }

    recognitionInstance = rec
    rec.start()
  } catch (err: any) {
    console.error('Failed to start speech recognition:', err)
    toast.error('启动 WebSpeech 失败', {
      description: err.message || String(err)
    })
    isRecordingVoice.value = false
  }
}

async function stopVoiceRecognition() {
  const provider = settingsStore.settings.asrProvider || 'mimo'

  if (provider === 'mimo' && isRecordingVoice.value) {
    isRecordingVoice.value = false
    isTranscribingVoice.value = true
    voiceInterimText.value = 'MIMO 流式转录中...'

    try {
      await voiceStreamer.stopRecordingAndTranscribe({
        onTranscribing: () => {
          isTranscribingVoice.value = true
          voiceInterimText.value = 'MIMO 流式转录中...'
        },
        onDelta: (delta) => {
          if (responseText.value && !responseText.value.endsWith(' ') && !responseText.value.endsWith('\n') && delta) {
            responseText.value += ' ' + delta
          } else {
            responseText.value += delta
          }
          followFilledText({ focus: false })
        },
        onError: (err) => {
          const errMsg = err.message || String(err)
          if (errMsg.includes('401') || errMsg.includes('Invalid API Key')) {
            toast.error('语音转录失败：API Key 无效 (401)', {
              description: 'Xiaomi MIMO API Key 验证失败，请在「设置 > 语音 ASR」检查 API Key 密钥或切换为 WebSpeech 引擎。'
            })
          } else {
            toast.error('流式语音转录失败', {
              description: errMsg
            })
          }
          isTranscribingVoice.value = false
          voiceInterimText.value = ''
        },
        onFinish: () => {
          isTranscribingVoice.value = false
          voiceInterimText.value = ''
        }
      })
    } catch (e: any) {
      console.error('Voice transcription error:', e)
      isTranscribingVoice.value = false
      voiceInterimText.value = ''
    }
  } else {
    if (recognitionInstance) {
      try {
        recognitionInstance.stop()
      } catch (_) {}
      recognitionInstance = null
    }
    isRecordingVoice.value = false
    isTranscribingVoice.value = false
    voiceInterimText.value = ''
  }
}

function handleWindowResize() {
  // 拖动期间高度由拖动自己管（pointerup 时才记下想要的高度），这里不插手，免得拖到一半被改回去。
  if (isDraggingResize.value) return
  // 视口变化只重新「夹」实际高度：太高就缩小，有空间了就回到想要的高度。**不写本机记忆**：
  // 以前这里把夹过的高度存回 localStorage，手机弹出输入法（视口变矮）后，用户拖出来的高度就被永久改小了（#198）。
  const target = Math.max(MIN_INPUT_HEIGHT, Math.min(getMaxAllowedDockHeight(), preferredDockHeight))
  if (target === inputDockHeight.value) return
  inputDockHeight.value = target
  lastEmittedHeight = target
  document.documentElement.style.setProperty('--input-dock-height', `${target}px`)
}

onMounted(() => {
  loadSavedDockHeight()
  loadDrafts()
  window.addEventListener('paste', handlePaste)
  window.addEventListener('resize', handleWindowResize)
  document.addEventListener('visibilitychange', handleDraftVisibilityChange)
  window.addEventListener('focus', recheckRemoteDraft)
  window.addEventListener('pagehide', handleDraftPageHide)
  unsubscribeDraftImageCloud = subscribeDraftImageCloud(applyCloudState)
})

onBeforeUnmount(() => {
  // 卸载前把所有待处理的草稿落盘（此时输入区状态仍然有效）
  draftPersister.flush()
  // 还在等待的云端图片释放（删除草稿的撤销窗口、提交后的宽限期）不再等：此刻输入区状态仍然有效，核对引用后立即处理
  runPendingReleasesNow()
  if (remoteRetryTimer !== null) {
    clearTimeout(remoteRetryTimer)
    remoteRetryTimer = null
  }
})

onUnmounted(() => {
  stopVoiceRecognition()
  unsubscribeDraftImageCloud?.()
  unsubscribeDraftImageCloud = null
  window.removeEventListener('paste', handlePaste)
  window.removeEventListener('resize', handleWindowResize)
  document.removeEventListener('visibilitychange', handleDraftVisibilityChange)
  window.removeEventListener('focus', recheckRemoteDraft)
  window.removeEventListener('pagehide', handleDraftPageHide)
})

  // 悬浮区真实尺寸感知与几何自适应拓扑（Demand 3 & Demand 2）
  const floatingActionBarRef = ref<HTMLElement | null>(null)
  const quickActionsRowRef = ref<HTMLElement | null>(null)
  const hasInlineScrollSpace = ref(true)
  const isPhone = useMediaQuery(PHONE_MEDIA)

  // 手机宽度的提示语只留前半句：括号里的快捷键与长说明在两三行高的输入框里读不下，手机上也没有 Ctrl 键（#155）。
  // 前半句仍保留「回复指导意见 / 追加补充指令」，输入区当前是哪种模式一眼能看出。
  const inputPlaceholder = computed(() => {
    const full = activeSession.value?.status === 'pending'
      ? '回复指导意见或确认要求...（支持粘贴图片并自动插入【图 N】引用，按 Ctrl + Enter 发送，Ctrl + ←/→ 切换草稿）'
      : appendPlaceholder(sessionStore.inboxConfig)
    return isPhone.value ? full.replace(/（.*）$/, '') : full
  })

  useResizeObserver(floatingActionBarRef, (entries) => {
    const entry = entries[0]
    if (entry) {
      const h = entry.contentRect.height
      // 真实 DOM 高度 + 8px 安全间距
      document.documentElement.style.setProperty('--input-dock-floating-offset', `${Math.ceil(h + 8)}px`)
    }
  })

  useResizeObserver(quickActionsRowRef, (entries) => {
    const entry = entries[0]
    if (entry) {
      const containerWidth = entry.contentRect.width
      const leftPillsEl = quickActionsRowRef.value?.querySelector('.quick-presets-container') as HTMLElement | null
      if (leftPillsEl) {
        const leftWidth = leftPillsEl.offsetWidth
        const isMultiLine = leftPillsEl.offsetHeight > 34
        // 当单行且右侧剩余空间 >= 90px 时判定为有空间
        // 手机宽度：左边的芯片是一整行可横向滑动的条（不换行），「回到底部」固定在它右边、条自己收窄，不再浮到条上方多占一行（#155）
        hasInlineScrollSpace.value = isPhone.value || (!isMultiLine && (containerWidth - leftWidth >= 90))
      } else {
        hasInlineScrollSpace.value = containerWidth >= 480
      }
    }
  })

  function clearDraft() {
    resetForm()
  }

  defineExpose({
    resetForm,
    clearDraft,
    loadSpecificContent,
    images
  })
</script>

<template>
  <!-- Full-Width Bottom-Flush Feedback Submission Dock (支持顶部横线拖拽与双击重置高度) -->
  <!-- data-session-id / data-session-status：输入区此刻绑定的会话（模式看它：待答复 → 回复；否则追加）。e2e 与只读实测直接读它，不靠占位文字去猜 -->
  <div
    data-testid="feedback-dock"
    :data-session-id="activeSession?.session_id ?? ''"
    :data-session-status="activeSession?.status ?? ''"
    class="relative w-full bg-background/95 backdrop-blur-xs px-3 sm:px-6 pt-3 shrink-0 z-30 flex flex-col justify-between"
    :class="isDraggingResize
      ? 'select-none transition-none border-t-2 border-primary/80 shadow-[0_-4px_20px_rgba(0,0,0,0.15)] dark:shadow-[0_-4px_25px_rgba(0,0,0,0.4)]'
      : 'border-t border-border/70 transition-[height] duration-150'"
    :style="{
      height: `${inputDockHeight}px`,
      paddingBottom: 'calc(0.75rem + env(safe-area-inset-bottom, 0px))'
    }"
  >
    <!-- Top Border Drag Handle Bar (仅在居中手柄区域响应拖拽与 hover，24px 触控热区 + 4px 居中指示条，双击重置) -->
    <div
      class="absolute top-0 left-0 right-0 h-6 -translate-y-1/2 flex items-center justify-center z-50 pointer-events-none select-none"
    >
      <div
        class="h-full px-6 flex items-center justify-center cursor-row-resize pointer-events-auto touch-none transition-all rounded-full group/drag"
        :class="isDraggingResize ? 'bg-primary/20' : 'hover:bg-primary/10'"
        title="按住拖拽调节输入框高度，双击重置默认高度"
        @pointerdown="startResizeDrag"
        @dblclick="resetInputHeight"
      >
        <div
          class="w-10 h-1 rounded-full transition-all duration-150"
          :class="isDraggingResize ? 'bg-primary w-16 scale-y-125 shadow-[0_0_8px_rgba(var(--primary),0.8)]' : 'bg-muted-foreground/30 group-hover/drag:bg-muted-foreground/60'"
        ></div>
      </div>

      <Transition
        enter-active-class="transition duration-100 ease-out"
        enter-from-class="opacity-0 scale-95"
        enter-to-class="opacity-100 scale-100"
        leave-active-class="transition duration-100 ease-in"
        leave-from-class="opacity-100 scale-100"
        leave-to-class="opacity-0 scale-95"
      >
        <div
          v-if="isDraggingResize"
          class="absolute -top-3 left-1/2 -translate-x-1/2 -translate-y-full z-50 bg-neutral-900 text-neutral-50 dark:bg-neutral-100 dark:text-neutral-900 px-3.5 py-1 rounded-full shadow-2xl border border-white/10 dark:border-black/10 flex items-center gap-2 text-xs font-mono select-none pointer-events-none"
        >
          <div class="flex items-center gap-1.5 font-semibold">
            <MoveVertical class="w-3.5 h-3.5" />
            <span>{{ inputDockHeight }}<span class="text-[10px] font-normal opacity-70 ml-0.5">px</span></span>
          </div>
          <span class="w-[1px] h-3 bg-current opacity-25"></span>
          <span class="text-[10px] opacity-75">双击重置默认 ({{ DEFAULT_INPUT_HEIGHT }}px)</span>
        </div>
      </Transition>
    </div>

    <!-- Floating Actions & Presets Row with Smart Upward Responsive Wrapping (挂载尺寸感知 ref) -->
    <div
      ref="floatingActionBarRef"
      class="absolute bottom-full left-3 sm:left-6 right-3 sm:right-6 mb-2 flex flex-col gap-1.5 z-40 transition-opacity duration-150"
      :class="isDraggingResize ? 'opacity-25 pointer-events-none' : 'opacity-100 pointer-events-auto'"
    >
      <!-- Row 1: Floating Image Previews (当有图片时悬浮在操作按钮上方，依次横向排开，支持拖拽调序) -->
      <div v-if="images.length > 0" class="flex flex-wrap items-center gap-2 max-w-full">
        <div
          v-for="(img, idx) in images"
          :key="img.name + '-' + idx"
          draggable="true"
          class="relative group rounded-md overflow-hidden border border-border/80 bg-card/95 backdrop-blur-xs w-14 h-14 sm:w-16 sm:h-16 flex items-center justify-center shadow-md shrink-0 transition-all cursor-move select-none"
          :class="{
            'ring-2 ring-primary scale-105 shadow-lg border-primary': dragOverImageIndex === idx,
            'opacity-40': draggedImageIndex === idx,
            'border-destructive/70': img.lost
          }"
          :title="img.lost
            ? `【图 ${idx + 1}】内容已丢失：本机和服务器都找不到它，请重新粘贴或移除`
            : isImageUnloaded(img)
              ? `【图 ${idx + 1}】正在从本机 / 服务器取回图片内容…`
              : `【图 ${idx + 1}】点击编号在光标处插入引用，拖拽可调序`"
          @dragstart="onImageDragStart(idx, $event)"
          @dragover="onImageDragOver(idx, $event)"
          @dragleave="onImageDragLeave(idx)"
          @drop="onImageDrop(idx, $event)"
          @dragend="onImageDragEnd"
          @click.stop="getImageUrl(img) && previewStore.openImagePreview({
            src: getImageUrl(img),
            alt: img.name || `草稿附件-${idx + 1}`
          })"
        >
          <img
            v-if="getImageUrl(img)"
            :src="getImageUrl(img)"
            :alt="img.name || 'preview'"
            class="w-full h-full object-cover pointer-events-none"
          />
          <!-- 正文缺失：载入中（脉冲）或确认丢失（标红）。旧实现在这里是一张破图，用户无从知道发生了什么 -->
          <div
            v-else
            class="w-full h-full flex flex-col items-center justify-center gap-0.5 pb-4 text-[9px] leading-none pointer-events-none"
            :class="img.lost ? 'bg-destructive/10 text-destructive' : 'bg-muted/60 text-muted-foreground animate-pulse'"
          >
            <ImageOff class="w-4 h-4" />
            <span>{{ img.lost ? '已丢失' : '载入中' }}</span>
          </div>
          <!-- 云端状态：粘贴即上传。上传中显示脉冲云朵；没传上去（仍保存在本机，草稿照常同步）显示断开的云朵，点击重试 -->
          <button
            v-if="cloudBadge(img)"
            type="button"
            data-testid="draft-image-cloud-badge"
            :data-cloud-state="cloudBadge(img)"
            class="absolute top-0.5 left-0.5 rounded-full p-0.5 shadow-xs z-10 bg-black/75 text-white"
            :class="cloudBadge(img) === 'failed' ? 'text-amber-300 hover:text-amber-200 cursor-pointer' : 'animate-pulse cursor-default'"
            :title="cloudBadge(img) === 'failed' ? '这张图片还没能上传到服务器（仍保存在本机，草稿内容照常同步）。点击重试' : '正在上传到服务器…'"
            @click.stop="cloudBadge(img) === 'failed' && retryCloudUpload(img)"
          >
            <CloudOff v-if="cloudBadge(img) === 'failed'" class="w-3 h-3" />
            <CloudUpload v-else class="w-3 h-3" />
          </button>
          <button
            type="button"
            class="absolute top-0.5 right-0.5 bg-black/75 hover:bg-destructive text-white rounded-full p-0.5 opacity-90 group-hover:opacity-100 transition-all cursor-pointer shadow-xs z-10"
            @click.stop="removeImage(idx)"
            title="移除此截图"
          >
            <X class="w-3 h-3" />
          </button>
          <!-- 底部一体化分区标题栏：左侧嵌入编号（点击在光标处插入「【图 N】」），右侧嵌入文件名 -->
          <div class="absolute bottom-0 inset-x-0 bg-black/80 backdrop-blur-xs text-white text-[9px] font-mono flex items-stretch h-4.5 select-none pointer-events-none overflow-hidden">
            <button
              type="button"
              class="px-1.5 bg-primary text-primary-foreground font-bold flex items-center justify-center shrink-0 border-r border-white/20 pointer-events-auto cursor-pointer hover:brightness-110 active:brightness-90"
              :title="`在光标处插入【图 ${idx + 1}】`"
              @mousedown.prevent
              @click.stop="insertImageRefIntoText(idx + 1)"
            >
              #{{ idx + 1 }}
            </button>
            <div class="flex-1 truncate px-1 text-center self-center text-white/90">
              {{ img.name || `img-${idx + 1}` }}
            </div>
          </div>
        </div>
      </div>

      <!-- Row 2: Exec Status, Phase & Presets (带空间感知与回到底部自适应排布) -->
      <div ref="quickActionsRowRef" class="relative flex items-center justify-between gap-1.5 w-full">
        <!-- Left Side: Exec Status & Phase & Presets & Tag Settings -->
        <!-- 手机宽度（#155）：芯片与快捷标签不换行，收成一整行可横向滑动的条——原来换成三四行，盖住会话主区约三成；
             py-2.5 / -my-2.5：条上下各多出 10px 只放点按区（让每个芯片的点按区够 44px），不占版面高度；条里的子项一律不收缩，宁可滑也不挤 -->
        <div class="quick-presets-container flex flex-wrap items-center gap-1.5 max-w-full min-w-0 max-md:-my-2.5 max-md:flex-nowrap max-md:overflow-x-auto max-md:overflow-y-hidden max-md:py-2.5 max-md:[&>*]:shrink-0 no-scrollbar" data-testid="quick-strip">
        <!-- 0. 执行状态块：22:28 · 1/6 · 5m（状态图标 + 计时 · 空回执 · 提示词等待）；右键 / 点击展开快捷设置与「我的状态」 -->
        <!-- 交互式（异步）场景只有「剩余等待回复时间」；1:1 场景才有挂起倒计时 / 空回执 / 提示词等待（#123） -->
        <!-- Agent 状态：上线点 · 状态图标 · 运行时间 · token（#81）；点开是身份卡（改名 / 唤醒）。没有 hook 汇报时不出现 -->
        <AgentStatusChip v-if="currentWorkflowId" :workflow-id="currentWorkflowId" />
        <!-- 「运行」（#140）：这个会话有对应的非交互续聊预设、对话已停下来时，一点就在预设的主机上接着这个对话运行一次；不满足时不出现 -->
        <SessionQuickRun v-if="currentWorkflowId" :workflow-id="currentWorkflowId" />
        <!-- 「启用 Agent」（#176）：网页新建、智能体还没接入（待接入）的会话，一点就在主机上无人值守地新开一个 Cursor CLI 对话接入它；接入后自己消失 -->
        <SessionEnableAgent v-if="currentWorkflowId" :workflow-id="currentWorkflowId" />
        <ReplyWindowChip v-if="currentWorkflowId && !phaseSliderVisible" />
        <SessionExecChip v-else-if="activeSession" />

        <!-- 收件箱状态块（D-212）：hook 最近一次来问收件箱的时间 + 排队 / 待确认条数；点开看 Agent 活动、提醒方式与 hook 安装命令 -->
        <InboxStatusChip v-if="currentWorkflowId" :workflow-id="currentWorkflowId" />

        <!-- 待答复徽标（#50）：智能体的提问 / 授权 / 计划确认还没答复的数量；点开定位到对话里那张卡。没有待答复的事时不出现 -->
        <ConfirmPendingChip v-if="currentWorkflowId" :workflow-id="currentWorkflowId" />

        <!-- 异步对话生效时，阶段滑块让位给授权档位（异步模式里不再注入阶段提示词） -->
        <AuthProfileChip v-if="currentWorkflowId && !phaseSliderVisible" :workflow-id="currentWorkflowId" />

        <!-- Phase Slider (收件箱状态块之后) -->
        <PhaseSlider v-if="currentWorkflowId && phaseSliderVisible" ref="phaseSliderRef" :workflow-id="currentWorkflowId" />

        <!-- 3..N Quick Preset Pills (支持点击填充、勾选追加在文字之后、右键编辑弹窗) -->
        <div
          v-for="preset in activeQuickPresets"
          :key="preset.id"
          class="flex items-center rounded-sm border transition-all shadow-2xs shrink-0 whitespace-nowrap group text-[11px] sm:text-xs overflow-hidden max-md:overflow-visible"
          :class="[
            preset.isAppendActive
              ? 'border-primary/60 bg-primary/10 text-primary font-medium'
              : (selectedPresets.includes(preset.title)
                  ? 'border-border bg-muted/80 text-foreground font-medium'
                  : 'bg-card/95 hover:bg-muted border-border/80 text-foreground backdrop-blur-xs')
          ]"
          :title="preset.allowAppend
            ? `快捷回复: ${preset.title} (右键编辑规则，勾选开启每次回复自动追加)`
            : `快捷回复: ${preset.title} (右键编辑内容)`"
          @contextmenu.prevent="openEditPresetDialog(preset)"
        >
          <!-- 1. 文本主体 (点击插入/切换预设内容，位于前部) -->
          <button
            type="button"
            class="px-2 sm:px-2.5 py-1 transition-all cursor-pointer flex items-center gap-1 hover:bg-muted/40"
            @click="togglePreset(preset)"
          >
            <span>{{ preset.title }}</span>
            <span v-if="preset.allowAppend && preset.isAppendActive" class="w-1.5 h-1.5 rounded-full bg-primary shrink-0 animate-pulse" title="已激活自动追加"></span>
          </button>

          <!-- 2. 允许追加时的独立 Checkbox (调整到文字之后！点击切换自动追加激活态) -->
          <button
            v-if="preset.allowAppend"
            type="button"
            class="px-1.5 py-1 border-l border-border/40 hover:bg-primary/20 transition-colors flex items-center justify-center cursor-pointer"
            :class="preset.isAppendActive ? 'text-primary' : 'text-muted-foreground hover:text-foreground'"
            :title="preset.isAppendActive ? '已开启自动追加规则（点击关闭）' : '未开启自动追加（点击开启每次回复末尾自动附加）'"
            @click.stop="togglePresetAppend(preset)"
          >
            <CheckSquare v-if="preset.isAppendActive" class="w-3.5 h-3.5 fill-primary/20 text-primary" />
            <Square v-else class="w-3.5 h-3.5" />
          </button>
        </div>

        <!-- Add New Preset Button -->
        <button
          type="button"
          class="p-1 rounded-sm border border-dashed border-border/80 bg-card/90 backdrop-blur-xs text-muted-foreground hover:text-foreground hover:border-primary/50 transition-colors flex items-center justify-center shadow-2xs shrink-0 cursor-pointer"
          title="新建快捷回复规则"
          @click="openCreatePresetDialog"
        >
          <Plus class="w-3.5 h-3.5" />
        </button>

        <!-- Setting Tags Trigger -->
        <button
          type="button"
          class="text-[11px] sm:text-xs px-2 py-1 rounded-sm border border-dashed border-border/80 bg-card/90 backdrop-blur-xs text-muted-foreground hover:text-foreground hover:border-border transition-colors flex items-center gap-1 shadow-2xs shrink-0 whitespace-nowrap cursor-pointer"
          @click="emit('open-settings')"
          title="配置快捷标签与超时设置"
        >
          <SlidersHorizontal class="w-3 h-3" />
          <span>设置标签</span>
        </button>

        <!-- 手机宽度：「搜索消息 / 回到经典视图（或试用预览视图）」入口放在条的末尾（桌面在会话区上方一行小字，见 FeedbackView） -->
        <ViewTrialLinks v-if="isPhone && currentWorkflowId" variant="chip" />
      </div>

      <!-- Right Side: 回到底部最新（Demand 2: 空间感知自适应居右/居上跃迁） -->
      <Transition
        enter-active-class="transition duration-150 ease-out"
        enter-from-class="opacity-0 translate-y-1 scale-95"
        enter-to-class="opacity-100 translate-y-0 scale-100"
        leave-active-class="transition duration-100 ease-in"
        leave-from-class="opacity-100 translate-y-0 scale-100"
        leave-to-class="opacity-0 translate-y-1 scale-95"
      >
        <button
          v-if="props.isScrolledUp"
          type="button"
          class="text-[11px] sm:text-xs px-2.5 py-1 rounded-sm border border-primary/40 bg-card/95 hover:bg-muted text-foreground backdrop-blur-xs shadow-2xs flex items-center gap-1.5 font-mono cursor-pointer transition-all shrink-0 select-none hover:border-primary/80 group z-50"
          :class="hasInlineScrollSpace
            ? 'ml-auto'
            : 'absolute bottom-[calc(100%+6px)] right-0 shadow-md'"
          @click="emit('scroll-to-bottom')"
          title="回到底部最新消息"
        >
          <ArrowDown class="w-3.5 h-3.5 text-primary group-hover:translate-y-0.5 transition-transform" />
          <span class="font-medium">回到底部</span>
        </button>
      </Transition>
      </div>
    </div>

    <!-- Seamless Full-Width Input Area (支持草稿左右平移动画切换) -->
    <div class="flex-1 flex flex-col min-h-0 space-y-1.5 overflow-hidden relative">
      <Transition :name="slideDirection" mode="out-in">
        <div :key="multiDrafts.activeIndex" class="w-full h-full flex flex-col min-h-0 flex-1">
          <!-- 文字里的「【回复 …】」引用标记显示成引用条（#118）；点 × 同时从文字里去掉标记 -->
          <QuoteChips v-model:text="responseText" />
          <textarea
            ref="textareaRef"
            v-model="responseText"
            data-testid="feedback-input"
            @select="rememberSelection"
            @keyup="rememberSelection"
            @mouseup="rememberSelection"
            @input="rememberSelection(); noteTyping()"
            @blur="rememberSelection"
            class="w-full flex-1 bg-transparent p-1 text-sm focus:outline-none placeholder:text-muted-foreground resize-none leading-relaxed text-foreground min-h-0"
            :placeholder="inputPlaceholder"
            @keydown="handleKeydown"
          ></textarea>
        </div>
      </Transition>
    </div>

    <!-- Bottom Action Row (Demand 8: 窄屏自适应与优雅降级响应式重构) -->
    <div class="flex flex-wrap items-center justify-between gap-y-1.5 gap-x-2 pt-1.5 border-t border-border/40 text-xs text-muted-foreground shrink-0">
      <div class="flex items-center gap-1.5 sm:gap-2 min-w-0">
        <!-- 多草稿箱轮播切换控件 (在极窄宽度隐藏「草稿」二字，由 140px 优雅压缩至 90px) -->
        <div class="h-7 flex items-center gap-1 sm:gap-1.5 bg-card/90 border border-border/80 px-1.5 sm:px-2 rounded-sm shadow-2xs font-mono text-xs select-none shrink-0">
          <FileText class="w-3.5 h-3.5 text-muted-foreground shrink-0" />
          <span class="text-muted-foreground font-medium hidden sm:inline">草稿</span>
          <span class="font-bold text-foreground">{{ multiDrafts.activeIndex + 1 }}</span>
          <span class="text-muted-foreground">/</span>
          <span class="text-muted-foreground">{{ multiDrafts.drafts.length }}</span>

          <button
            type="button"
            class="hover:bg-muted p-1 rounded-sm cursor-pointer transition-colors disabled:opacity-30 disabled:cursor-not-allowed"
            :disabled="multiDrafts.drafts.length <= 1"
            title="上一个草稿 (Ctrl + ←)"
            @click="switchDraft((multiDrafts.activeIndex - 1 + multiDrafts.drafts.length) % multiDrafts.drafts.length)"
          >
            <ChevronLeft class="w-3.5 h-3.5 text-foreground" />
          </button>

          <button
            type="button"
            class="hover:bg-muted p-1 rounded-sm cursor-pointer transition-colors disabled:opacity-30 disabled:cursor-not-allowed"
            :disabled="multiDrafts.drafts.length <= 1"
            title="下一个草稿 (Ctrl + →)"
            @click="switchDraft((multiDrafts.activeIndex + 1) % multiDrafts.drafts.length)"
          >
            <ChevronRight class="w-3.5 h-3.5 text-foreground" />
          </button>

          <button
            type="button"
            class="hover:bg-primary/20 hover:text-primary p-1 rounded-sm cursor-pointer transition-colors"
            title="新建草稿"
            @click="createNewDraft"
          >
            <Plus class="w-3.5 h-3.5 text-foreground" />
          </button>

          <button
            type="button"
            class="hover:bg-destructive/20 hover:text-destructive p-1 rounded-sm cursor-pointer transition-colors"
            :title="multiDrafts.drafts.length > 1 ? '删除当前草稿' : '清空当前草稿'"
            @click="deleteCurrentDraft"
          >
            <Trash2 class="w-3.5 h-3.5 text-muted-foreground hover:text-destructive" />
          </button>
        </div>

        <span class="hidden lg:inline text-muted-foreground/80 text-[11px]">按 <kbd class="px-1.5 py-0.5 rounded-xs bg-muted border border-border font-mono text-[10px]">Ctrl + Enter</kbd> 发送</span>

        <!-- 语音识别实时转录中指示 -->
        <span v-if="isRecordingVoice || isTranscribingVoice" class="flex items-center gap-1.5 font-mono font-medium text-xs truncate max-w-[140px] sm:max-w-[200px]" :class="isRecordingVoice ? 'text-destructive animate-pulse' : 'text-primary animate-pulse'">
          <span class="w-1.5 h-1.5 rounded-full shrink-0" :class="isRecordingVoice ? 'bg-destructive' : 'bg-primary'"></span>
          <span class="truncate">{{ voiceInterimText || (isRecordingVoice ? '正在录音...' : 'MIMO 流式转写中...') }}</span>
        </span>
      </div>

      <div class="flex items-center gap-1.5 sm:gap-2 ml-auto shrink-0">
        <!-- 上传截图（窄屏自适应降级为仅图标模式） -->
        <button
          type="button"
          class="h-7 px-2 sm:px-2.5 rounded-sm border transition-all text-xs font-mono flex items-center justify-center gap-1.5 cursor-pointer select-none"
          :class="images.length > 0
            ? 'border-primary text-primary bg-primary/10 font-medium shadow-2xs'
            : 'border-border/70 bg-card/80 hover:bg-muted text-muted-foreground hover:text-foreground'"
          @click="triggerImageUpload"
          title="上传屏幕截图附件（亦支持直接 Ctrl + V 粘贴）"
        >
          <ImagePlus class="w-3.5 h-3.5" />
          <span class="hidden sm:inline">{{ images.length > 0 ? `${images.length} 张截图` : '上传截图' }}</span>
          <span v-if="images.length > 0" class="sm:hidden text-[10px] font-bold">{{ images.length }}</span>
        </button>
        <input
          ref="fileInputRef"
          type="file"
          multiple
          accept="image/*"
          class="hidden"
          @change="handleFileSelect"
        />

        <!-- 语音识别快捷按钮（窄屏自适应降级为仅图标模式） -->
        <button
          type="button"
          class="h-7 px-2 sm:px-2.5 rounded-sm border transition-all text-xs font-mono flex items-center justify-center gap-1.5 cursor-pointer select-none"
          :class="isRecordingVoice
            ? 'border-destructive text-destructive bg-destructive/10 animate-pulse font-medium shadow-2xs'
            : isTranscribingVoice
              ? 'border-primary text-primary bg-primary/10 animate-pulse font-medium shadow-2xs'
              : 'border-border/70 bg-card/80 hover:bg-muted text-muted-foreground hover:text-foreground'"
          :title="isRecordingVoice ? '点击停止录音并流式转录' : isTranscribingVoice ? '正在流式转写中...' : '开启语音输入识别（支持小米 MIMO 流式转写）'"
          @click="toggleVoiceRecognition"
        >
          <Mic v-if="!isRecordingVoice && !isTranscribingVoice" class="w-3.5 h-3.5 text-muted-foreground" />
          <RotateCcw v-else-if="isTranscribingVoice" class="w-3.5 h-3.5 text-primary animate-spin" />
          <MicOff v-else class="w-3.5 h-3.5 text-destructive animate-bounce" />
          <span class="hidden sm:inline">{{ isRecordingVoice ? '停止录音' : isTranscribingVoice ? 'MIMO转写中' : '语音输入' }}</span>
        </button>

        <!-- 发送主按钮（最高优先级，在窄屏始终完整呈现） -->
        <Button
          variant="default"
          :disabled="sessionStore.submitting"
          class="h-7 px-3 sm:px-3.5 text-xs rounded-sm gap-1 bg-primary text-primary-foreground hover:opacity-90 font-medium cursor-pointer shrink-0"
          :title="submitButtonInfo.description"
          @click="handleSubmit"
        >
          <Send class="w-3 h-3" />
          <span>{{ sessionStore.submitting ? submitButtonInfo.submittingLabel : submitButtonInfo.label }}</span>
        </Button>
      </div>
    </div>

    <!-- Quick Preset Edit Dialog -->
    <QuickPresetEditDialog
      v-model:open="isPresetDialogOpen"
      :preset-item="editingPresetItem"
      :is-new="isNewPreset"
      :available-presets="activeQuickPresets"
      @save="handleSavePreset"
      @delete="handleDeletePreset"
    />
  </div>
</template>

<style scoped>
/* Multi-Draft Carousel Slide Animations */
.slide-left-enter-active,
.slide-left-leave-active,
.slide-right-enter-active,
.slide-right-leave-active {
  transition: all 0.2s cubic-bezier(0.25, 1, 0.5, 1);
}

.slide-left-enter-from {
  opacity: 0;
  transform: translateX(36px);
}
.slide-left-leave-to {
  opacity: 0;
  transform: translateX(-36px);
}

.slide-right-enter-from {
  opacity: 0;
  transform: translateX(-36px);
}
.slide-right-leave-to {
  opacity: 0;
  transform: translateX(36px);
}
</style>
