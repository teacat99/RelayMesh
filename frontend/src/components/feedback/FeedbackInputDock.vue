<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import { useSessionStore } from '../../stores/session'
import { useSettingsStore } from '../../stores/settings'
import ImageUploader from '../ImageUploader.vue'
import Button from '../ui/button/Button.vue'
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem
} from '../ui/dropdown-menu'
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
  Check,
  FileText,
  ChevronLeft,
  ChevronRight,
  Plus,
  Trash2,
  X,
  CheckSquare,
  Square,
  MessageSquareQuote
} from 'lucide-vue-next'
import QuickPresetEditDialog from './QuickPresetEditDialog.vue'
import PhaseSlider from './PhaseSlider.vue'
import type { QuickPresetItem } from '../../stores/settings'
import type { SessionImage } from '../../api/types'
import { draftsApi } from '../../api/client'
import { VoiceRecorderStreamer } from '../../utils/voiceStream'
import { usePreviewStore } from '../../stores/preview'
import { toast } from 'vue-sonner'

const props = withDefaults(defineProps<{
  isScrolledUp?: boolean
  isSubmitting?: boolean
  placeholder?: string
  buttonText?: string
}>(), {
  isScrolledUp: false,
  isSubmitting: false,
  placeholder: '',
  buttonText: ''
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

// ==========================================
// 会话级在线状态切换
// ==========================================
const activeSession = computed(() => sessionStore.currentSession || sessionStore.selectedSession)

const currentSessionPresence = computed(() => {
  return activeSession.value?.user_presence || settingsStore.settings.userPresence || 'online'
})

const currentSessionPresenceLabel = computed(() => {
  const p = currentSessionPresence.value
  if (p === 'away') return '暂离'
  if (p === 'autopilot') return '托管'
  return '在线'
})

async function handleSessionPresenceChange(presence: 'online' | 'away' | 'autopilot') {
  if (!activeSession.value) return
  await sessionStore.updateUserPresence(activeSession.value.session_id, presence)
}

const phaseSliderRef = ref<InstanceType<typeof PhaseSlider> | null>(null)
const currentWorkflowId = computed(() => {
  return activeSession.value?.workflow_id || sessionStore.currentSession?.workflow_id || sessionStore.selectedSession?.workflow_id || ''
})

// Feedback input panel resizing state with LocalStorage persistence
const DOCK_HEIGHT_STORAGE_KEY = 'relaymesh_input_dock_height'
const DEFAULT_INPUT_HEIGHT = 160
const MIN_INPUT_HEIGHT = 100
const MAX_INPUT_HEIGHT = 500
const inputDockHeight = ref(DEFAULT_INPUT_HEIGHT)
const isDraggingResize = ref(false)
let dragStartY = 0
let dragStartHeight = 0
let lastEmittedHeight = DEFAULT_INPUT_HEIGHT

function loadSavedDockHeight() {
  try {
    const saved = localStorage.getItem(DOCK_HEIGHT_STORAGE_KEY)
    if (saved) {
      const h = parseInt(saved, 10)
      if (!isNaN(h) && h >= MIN_INPUT_HEIGHT && h <= MAX_INPUT_HEIGHT) {
        inputDockHeight.value = h
        lastEmittedHeight = h
      }
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

function startResizeDrag(e: MouseEvent) {
  isDraggingResize.value = true
  dragStartY = e.clientY
  dragStartHeight = inputDockHeight.value
  lastEmittedHeight = dragStartHeight
  window.addEventListener('mousemove', handleResizeMouseMove)
  window.addEventListener('mouseup', handleResizeMouseUp)
  document.body.style.userSelect = 'none'
  document.body.style.cursor = 'row-resize'
}

function handleResizeMouseMove(e: MouseEvent) {
  if (!isDraggingResize.value) return
  const deltaFromStart = dragStartY - e.clientY
  const newH = Math.min(MAX_INPUT_HEIGHT, Math.max(MIN_INPUT_HEIGHT, dragStartHeight + deltaFromStart))
  const delta = newH - lastEmittedHeight
  if (delta !== 0) {
    inputDockHeight.value = newH
    lastEmittedHeight = newH
    emit('resize-delta', delta)
  }
}

function handleResizeMouseUp() {
  if (!isDraggingResize.value) return
  isDraggingResize.value = false
  saveDockHeight(inputDockHeight.value)
  window.removeEventListener('mousemove', handleResizeMouseMove)
  window.removeEventListener('mouseup', handleResizeMouseUp)
  document.body.style.userSelect = ''
  document.body.style.cursor = ''
}

function resetInputHeight() {
  const delta = DEFAULT_INPUT_HEIGHT - inputDockHeight.value
  inputDockHeight.value = DEFAULT_INPUT_HEIGHT
  lastEmittedHeight = DEFAULT_INPUT_HEIGHT
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

function getDraftStorageKey(workflowId?: string): string {
  const wId = workflowId || sessionStore.currentSession?.workflow_id || sessionStore.selectedSession?.workflow_id || 'default'
  return `relaymesh_multidrafts_wf_${wId}`
}

let dbSaveTimer: number | null = null

function triggerDbSave(wId: string) {
  if (dbSaveTimer) window.clearTimeout(dbSaveTimer)
  dbSaveTimer = window.setTimeout(() => {
    draftsApi.save(wId, multiDrafts.value.activeIndex, JSON.stringify(multiDrafts.value)).catch(err => {
      console.warn('Failed to sync draft to server db:', err)
    })
  }, 400)
}

function saveDrafts() {
  if (isResetting) return
  const wId = sessionStore.currentSession?.workflow_id || sessionStore.selectedSession?.workflow_id || 'default'
  const wKey = getDraftStorageKey(wId)
  const curIdx = multiDrafts.value.activeIndex
  if (multiDrafts.value.drafts[curIdx]) {
    multiDrafts.value.drafts[curIdx].text = responseText.value
    multiDrafts.value.drafts[curIdx].presets = selectedPresets.value
    multiDrafts.value.drafts[curIdx].images = images.value
    multiDrafts.value.drafts[curIdx].updated_at = Date.now()
  }

  try {
    localStorage.setItem(wKey, JSON.stringify(multiDrafts.value))
    // 彻底清除旧版本遗留的全局污染草稿键
    localStorage.removeItem('relaymesh_active_draft_content')
    localStorage.removeItem('relaymesh_draft_global')
  } catch (_) {}

  // 异步同步到后端数据库
  triggerDbSave(wId)
}

async function loadDrafts(forceWorkflowId?: string) {
  if (isResetting) return
  const wId = forceWorkflowId || sessionStore.currentSession?.workflow_id || sessionStore.selectedSession?.workflow_id || 'default'
  const wKey = getDraftStorageKey(wId)
  
  // 彻底清除旧版本遗留的全局污染草稿键，避免跨会话乱恢复
  try {
    localStorage.removeItem('relaymesh_active_draft_content')
    localStorage.removeItem('relaymesh_draft_global')
  } catch (_) {}

  // 1. 本地快速恢复 (0 延迟即时呈现)
  try {
    const raw = localStorage.getItem(wKey)
    if (raw) {
      const data = JSON.parse(raw)
      if (data && Array.isArray(data.drafts) && data.drafts.length > 0) {
        multiDrafts.value = {
          activeIndex: typeof data.activeIndex === 'number' && data.activeIndex < data.drafts.length ? data.activeIndex : 0,
          drafts: data.drafts
        }
      }
    } else {
      // 检查旧单草稿格式
      const oldKey = `relaymesh_draft_wf_${wId}`
      const oldRaw = localStorage.getItem(oldKey)
      if (oldRaw) {
        const oldData = JSON.parse(oldRaw)
        multiDrafts.value = {
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
      } else {
        // 无草稿时生成 1 个干净空草稿
        multiDrafts.value = {
          activeIndex: 0,
          drafts: [
            { id: '1', text: '', presets: [], images: [], updated_at: Date.now() }
          ]
        }
      }
    }

    const current = multiDrafts.value.drafts[multiDrafts.value.activeIndex] || multiDrafts.value.drafts[0]
    responseText.value = current.text || ''
    selectedPresets.value = current.presets || []
    images.value = current.images || []
  } catch (_) {}

  // 2. 异步从后端数据库拉取最新草稿数据（仅当本地无任何记录时才启用，防止覆盖已清空的草稿）
  const hasLocalEntry = (() => { try { return localStorage.getItem(wKey) !== null } catch { return false } })()
  if (!hasLocalEntry) {
    try {
      const res = await draftsApi.get(wId)
      if (res && res.draft && res.draft.drafts_json) {
        const serverData = JSON.parse(res.draft.drafts_json)
        if (serverData && Array.isArray(serverData.drafts) && serverData.drafts.length > 0) {
          multiDrafts.value = {
            activeIndex: typeof serverData.activeIndex === 'number' && serverData.activeIndex < serverData.drafts.length ? serverData.activeIndex : 0,
            drafts: serverData.drafts
          }
          const cur = multiDrafts.value.drafts[multiDrafts.value.activeIndex] || multiDrafts.value.drafts[0]
          responseText.value = cur.text || ''
          selectedPresets.value = cur.presets || []
          images.value = cur.images || []
          try {
            localStorage.setItem(wKey, JSON.stringify(multiDrafts.value))
          } catch (_) {}
        }
      }
    } catch (err) {
      console.warn('Failed to fetch draft from server db:', err)
    }
  }
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

function deleteCurrentDraft() {
  const wId = sessionStore.currentSession?.workflow_id || sessionStore.selectedSession?.workflow_id || 'default'
  if (multiDrafts.value.drafts.length > 1) {
    const curIdx = multiDrafts.value.activeIndex
    multiDrafts.value.drafts.splice(curIdx, 1)
    const nextIdx = Math.max(0, curIdx - 1)
    multiDrafts.value.activeIndex = nextIdx
    const target = multiDrafts.value.drafts[nextIdx]
    responseText.value = target.text || ''
    selectedPresets.value = target.presets || []
    images.value = target.images || []
    saveDrafts()
    toast.success('已删除该草稿')
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
    toast.success('已清空当前草稿')
  }
  draftsApi.save(wId, multiDrafts.value.activeIndex, JSON.stringify(multiDrafts.value)).catch(() => {})
}

function resetForm(targetWorkflowId?: string) {
  isResetting = true
  const wId = targetWorkflowId || sessionStore.currentSession?.workflow_id || sessionStore.selectedSession?.workflow_id || 'default'
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

  const wKey = getDraftStorageKey(targetWorkflowId)
  try {
    localStorage.setItem(wKey, JSON.stringify(multiDrafts.value))
  } catch (_) {}

  // 数据库也同步更新
  draftsApi.save(wId, multiDrafts.value.activeIndex, JSON.stringify(multiDrafts.value)).catch(() => {})

  setTimeout(() => {
    isResetting = false
  }, 100)
}

function getImageUrl(img: { data?: string; format?: string } | null | undefined): string {
  if (!img || !img.data) return ''
  if (img.data.startsWith('data:') || img.data.startsWith('http://') || img.data.startsWith('https://')) {
    return img.data
  }
  const format = img.format || 'png'
  return `data:image/${format};base64,${img.data}`
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
      return
    }
  }

  // 若当前为空或草稿槽位已满，直接载入当前卡槽
  responseText.value = data.text || ''
  selectedPresets.value = data.presets || []
  images.value = data.images || []
  saveDrafts()
}

watch([responseText, selectedPresets, images], () => {
  saveDrafts()
}, { deep: true })

// 仅当用户切换到不同的 workflow_id 时才做草稿切换；在同一工作流下产生新轮次 session_id 时绝不清空输入
watch(() => (sessionStore.currentSession?.workflow_id || sessionStore.selectedSession?.workflow_id), (newWId, oldWId) => {
  if (newWId !== oldWId) {
    if (oldWId) {
      saveDrafts()
    }
    loadDrafts(newWId)
  }
})

// ==========================================
// 快捷预设 Pills (支持右键编辑、自动追加规则勾选)
// ==========================================
const activeQuickPresets = computed<QuickPresetItem[]>(() => {
  const targetSession = sessionStore.currentSession || sessionStore.selectedSession
  const presence = targetSession?.user_presence || settingsStore.settings.userPresence || 'online'
  return settingsStore.getNormalizedStatusPresets(presence)
})

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
  const targetSession = sessionStore.currentSession || sessionStore.selectedSession
  const presence = targetSession?.user_presence || settingsStore.settings.userPresence || 'online'
  settingsStore.saveStatusPresetItem(presence, item)
  toast.success('已保存快捷回复预设')
}

function handleDeletePreset(id: string) {
  const targetSession = sessionStore.currentSession || sessionStore.selectedSession
  const presence = targetSession?.user_presence || settingsStore.settings.userPresence || 'online'
  settingsStore.removeStatusPresetItem(presence, id)
  toast.success('已删除快捷回复预设')
}

function togglePresetAppend(preset: QuickPresetItem) {
  const targetSession = sessionStore.currentSession || sessionStore.selectedSession
  const presence = targetSession?.user_presence || settingsStore.settings.userPresence || 'online'
  settingsStore.toggleStatusPresetAppendActive(presence, preset.id)
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
  } else if (!responseText.value.includes(textToInsert)) {
    responseText.value = `${responseText.value}\n${textToInsert}`
  }
}

function triggerImageUpload() {
  fileInputRef.value?.click()
}

function removeImage(index: number) {
  images.value.splice(index, 1)
  saveDrafts()
}

function handleFileSelect(e: Event) {
  const target = e.target as HTMLInputElement
  if (!target.files || target.files.length === 0) return

  for (let i = 0; i < target.files.length; i++) {
    const file = target.files[i]
    const reader = new FileReader()
    reader.onload = (evt) => {
      const res = evt.target?.result as string
      if (res) {
        const base64Data = res.split(',')[1] || res
        const format = file.type.split('/')[1] || 'png'
        images.value.push({
          name: file.name,
          format: format,
          data: base64Data,
          data_type: 'base64'
        })
      }
    }
    reader.readAsDataURL(file)
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
        const reader = new FileReader()
        reader.onload = (evt) => {
          const res = evt.target?.result as string
          if (res) {
            const base64Data = res.split(',')[1] || res
            images.value.push({
              name: `paste-${Date.now()}.png`,
              format: 'png',
              data: base64Data,
              data_type: 'base64'
            })
          }
        }
        reader.readAsDataURL(file)
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
  const sess = sessionStore.currentSession || sessionStore.selectedSession
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
    description: '当前无交互，暂存用户指令并在 AI 下次发起交互时秒回'
  }
})

function handleSubmit() {
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

  emit('submit', {
    text: mainText,
    presets: [],
    images: images.value
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

onMounted(() => {
  loadSavedDockHeight()
  loadDrafts()
  window.addEventListener('paste', handlePaste)
})

onUnmounted(() => {
  stopVoiceRecognition()
  window.removeEventListener('paste', handlePaste)
})

  watch(() => images.value.length, (count) => {
    // 若有悬浮图片，悬浮区域高度增加 70px
    const floatingOffset = count > 0 ? 116 : 46
    document.documentElement.style.setProperty('--input-dock-floating-offset', `${floatingOffset}px`)
  }, { immediate: true })

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
  <div
    class="relative w-full bg-background/95 backdrop-blur-xs px-3 sm:px-6 pt-3 pb-3 shrink-0 z-30 flex flex-col justify-between"
    :class="isDraggingResize
      ? 'select-none transition-none border-t-2 border-primary/80 shadow-[0_-4px_20px_rgba(0,0,0,0.15)] dark:shadow-[0_-4px_25px_rgba(0,0,0,0.4)]'
      : 'border-t border-border/70 transition-[height] duration-150'"
    :style="{ height: `${inputDockHeight}px` }"
  >
    <!-- Top Border Drag Handle Bar (按下拖拽时呈现高质感活动线与浮动 HUD 指示徽章，双击重置) -->
    <div
      class="absolute top-0 left-0 right-0 h-3 -translate-y-1/2 cursor-row-resize flex items-center justify-center z-50 transition-colors group/drag"
      title="按住拖拽调节输入框高度，双击重置默认高度"
      @mousedown="startResizeDrag"
      @dblclick="resetInputHeight"
    >
      <div
        class="w-full h-full flex items-center justify-center transition-all"
        :class="isDraggingResize ? 'bg-primary/20' : 'hover:bg-primary/10'"
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

    <!-- Floating Actions & Presets Row with Smart Upward Responsive Wrapping -->
    <div
      class="absolute bottom-full left-3 sm:left-6 right-3 sm:right-6 mb-2 flex flex-col gap-1.5 z-40 transition-opacity duration-150"
      :class="isDraggingResize ? 'opacity-25 pointer-events-none' : 'opacity-100 pointer-events-auto'"
    >
      <!-- Row 1: Floating Image Previews (当有图片时悬浮在操作按钮上方，依次横向排开) -->
      <div v-if="images.length > 0" class="flex flex-wrap items-center gap-2 max-w-full">
        <div
          v-for="(img, idx) in images"
          :key="idx"
          class="relative group rounded-md overflow-hidden border border-border/80 bg-card/95 backdrop-blur-xs w-14 h-14 sm:w-16 sm:h-16 flex items-center justify-center shadow-md shrink-0 transition-transform hover:scale-105 cursor-pointer"
          @click.stop="previewStore.openImagePreview({
            src: getImageUrl(img),
            alt: img.name || `草稿附件-${idx + 1}`
          })"
        >
          <img
            :src="getImageUrl(img)"
            :alt="img.name || 'preview'"
            class="w-full h-full object-cover pointer-events-none"
          />
          <button
            type="button"
            class="absolute top-0.5 right-0.5 bg-black/75 hover:bg-destructive text-white rounded-full p-0.5 opacity-90 group-hover:opacity-100 transition-all cursor-pointer shadow-xs z-10"
            @click.stop="removeImage(idx)"
            title="移除此截图"
          >
            <X class="w-3 h-3" />
          </button>
          <div class="absolute bottom-0 inset-x-0 bg-black/60 backdrop-blur-2xs text-white text-[9px] font-mono px-1 truncate text-center select-none pointer-events-none">
            {{ img.name || `img-${idx + 1}` }}
          </div>
        </div>
      </div>

      <!-- Row 2: Floating Actions, Presence & Presets -->
      <div class="flex flex-wrap items-center justify-between gap-1.5 w-full">
        <!-- Left Side: Presence & Upload & Presets & Tag Settings -->
        <div class="flex flex-wrap items-center gap-1.5 max-w-full min-w-0">
        <!-- 0. 用户在线状态切换下拉按钮 -->
        <DropdownMenu v-if="activeSession">
          <DropdownMenuTrigger as-child>
            <button
              type="button"
              class="text-[11px] sm:text-xs px-2 sm:px-2.5 py-1 rounded-sm border transition-all shadow-2xs flex items-center gap-1.5 cursor-pointer shrink-0 bg-card/95 hover:bg-muted border-border/80 text-foreground backdrop-blur-xs select-none"
              :title="`当前会话模式: ${currentSessionPresenceLabel} (点击切换)`"
            >
              <span
                class="w-2 h-2 rounded-full shrink-0"
                :class="{
                  'bg-emerald-500 shadow-[0_0_5px_rgba(16,185,129,0.7)]': currentSessionPresence === 'online',
                  'bg-amber-500 shadow-[0_0_5px_rgba(245,158,11,0.7)]': currentSessionPresence === 'away',
                  'bg-indigo-500 shadow-[0_0_5px_rgba(99,102,241,0.7)]': currentSessionPresence === 'autopilot'
                }"
              ></span>
              <span class="font-medium text-foreground">{{ currentSessionPresenceLabel }}</span>
            </button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start" class="w-28 p-1 font-mono text-xs shadow-modal border border-border bg-popover z-50">
            <DropdownMenuItem
              class="flex items-center gap-2 cursor-pointer rounded-xs px-2 py-1.5 text-xs font-mono"
              :class="currentSessionPresence === 'online' ? 'bg-accent font-bold text-accent-foreground' : ''"
              @click="handleSessionPresenceChange('online')"
            >
              <span class="w-2 h-2 rounded-full bg-emerald-500 shrink-0"></span>
              <span>在线</span>
              <Check v-if="currentSessionPresence === 'online'" class="w-3.5 h-3.5 text-primary ml-auto" />
            </DropdownMenuItem>
            <DropdownMenuItem
              class="flex items-center gap-2 cursor-pointer rounded-xs px-2 py-1.5 text-xs font-mono"
              :class="currentSessionPresence === 'away' ? 'bg-accent font-bold text-accent-foreground' : ''"
              @click="handleSessionPresenceChange('away')"
            >
              <span class="w-2 h-2 rounded-full bg-amber-500 shrink-0"></span>
              <span>暂离</span>
              <Check v-if="currentSessionPresence === 'away'" class="w-3.5 h-3.5 text-primary ml-auto" />
            </DropdownMenuItem>
            <DropdownMenuItem
              class="flex items-center gap-2 cursor-pointer rounded-xs px-2 py-1.5 text-xs font-mono"
              :class="currentSessionPresence === 'autopilot' ? 'bg-accent font-bold text-accent-foreground' : ''"
              @click="handleSessionPresenceChange('autopilot')"
            >
              <span class="w-2 h-2 rounded-full bg-indigo-500 shrink-0"></span>
              <span>托管</span>
              <Check v-if="currentSessionPresence === 'autopilot'" class="w-3.5 h-3.5 text-primary ml-auto" />
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>

        <!-- Phase Slider (在线状态之后) -->
        <PhaseSlider v-if="currentWorkflowId" ref="phaseSliderRef" :workflow-id="currentWorkflowId" />

        <!-- 3..N Quick Preset Pills (支持点击填充、勾选追加在文字之后、右键编辑弹窗) -->
        <div
          v-for="preset in activeQuickPresets"
          :key="preset.id"
          class="flex items-center rounded-sm border transition-all shadow-2xs shrink-0 whitespace-nowrap group text-[11px] sm:text-xs overflow-hidden"
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
      </div>

      <!-- Right Side: 回到底部最新 -->
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
          class="ml-auto text-[11px] sm:text-xs px-2.5 py-1 rounded-sm border border-primary/40 bg-card/95 hover:bg-muted text-foreground backdrop-blur-xs shadow-2xs flex items-center gap-1.5 font-mono cursor-pointer transition-all shrink-0 select-none hover:border-primary/80 group"
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
          <textarea
            v-model="responseText"
            class="w-full flex-1 bg-transparent p-1 text-sm focus:outline-none placeholder:text-muted-foreground resize-none leading-relaxed text-foreground min-h-0"
            :placeholder="activeSession?.status === 'pending'
              ? '回复指导意见或确认要求...（支持粘贴图片，按 Ctrl + Enter 发送，Ctrl + ←/→ 切换草稿）'
              : '追加补充指令...（当前无交互，发送后将在 AI 下次发起交互时直接秒回）'"
            @keydown="handleKeydown"
          ></textarea>
        </div>
      </Transition>
    </div>

    <!-- Bottom Action Row -->
    <div class="flex items-center justify-between pt-1.5 border-t border-border/40 text-xs text-muted-foreground shrink-0">
      <div class="flex items-center gap-2">
        <!-- 多草稿箱轮播切换控件 -->
        <div class="h-7 flex items-center gap-1.5 bg-card/90 border border-border/80 px-2 rounded-sm shadow-2xs font-mono text-xs select-none">
          <FileText class="w-3.5 h-3.5 text-muted-foreground shrink-0" />
          <span class="text-muted-foreground font-medium">草稿</span>
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

        <span class="hidden md:inline text-muted-foreground/80">按 <kbd class="px-1.5 py-0.5 rounded-xs bg-muted border border-border font-mono text-[10px]">Ctrl + Enter</kbd> 发送</span>

        <!-- 语音识别实时转录中指示 -->
        <span v-if="isRecordingVoice || isTranscribingVoice" class="flex items-center gap-1.5 font-mono font-medium text-xs" :class="isRecordingVoice ? 'text-destructive animate-pulse' : 'text-primary animate-pulse'">
          <span class="w-1.5 h-1.5 rounded-full" :class="isRecordingVoice ? 'bg-destructive' : 'bg-primary'"></span>
          <span>{{ voiceInterimText || (isRecordingVoice ? '正在录音...' : 'MIMO 流式转写中...') }}</span>
        </span>
      </div>

      <div class="flex items-center gap-2">
        <!-- 上传截图 -->
        <button
          type="button"
          class="h-7 px-2.5 rounded-sm border transition-all text-xs font-mono flex items-center gap-1.5 cursor-pointer select-none"
          :class="images.length > 0
            ? 'border-primary text-primary bg-primary/10 font-medium shadow-2xs'
            : 'border-border/70 bg-card/80 hover:bg-muted text-muted-foreground hover:text-foreground'"
          @click="triggerImageUpload"
          title="上传屏幕截图附件（亦支持直接 Ctrl + V 粘贴）"
        >
          <ImagePlus class="w-3.5 h-3.5" />
          <span>{{ images.length > 0 ? `${images.length} 张截图` : '上传截图' }}</span>
        </button>
        <input
          ref="fileInputRef"
          type="file"
          multiple
          accept="image/*"
          class="hidden"
          @change="handleFileSelect"
        />

        <!-- 语音识别快捷按钮 -->
        <button
          type="button"
          class="h-7 px-2.5 rounded-sm border transition-all text-xs font-mono flex items-center gap-1.5 cursor-pointer select-none"
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
          <span>{{ isRecordingVoice ? '停止录音' : isTranscribingVoice ? 'MIMO转写中' : '语音输入' }}</span>
        </button>

        <Button
          variant="default"
          :disabled="sessionStore.submitting"
          class="h-7 px-3 sm:px-3.5 text-xs rounded-sm gap-1 bg-primary text-primary-foreground hover:opacity-90 font-medium cursor-pointer"
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
