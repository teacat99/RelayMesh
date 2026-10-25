import { defineStore } from 'pinia'
import { DEFAULT_PHASE_TEMPLATE, DEFAULT_SERVER_INSTRUCTIONS } from '@/lib/defaultPrompts.generated'
import { normalizePhaseTemplate } from '@/lib/phases'
import { dropRetiredSettings, hasRetiredSettings } from '@/lib/retiredSettings'
import type { PromptTemplateOverrides } from '@/lib/residentPrompt'
import type { WakeTemplateOverrides } from '@/lib/wakePrompts'
import {
  archiveReplaced,
  clearArchive,
  deleteArchived,
  normalizeVersions,
  reconcile,
  reenableArchived,
  resolveConflicts,
  type Decision,
  type PresetConflict,
  type PresetScope,
  type PresetVersions,
  type ScopeItem,
  type ScopeLists,
} from '@/lib/presetVersions'
import { computed, ref, watch } from 'vue'
import { settingsApi, authApi, type BlockedIPInfo } from '../api/client'

export interface QuickPresetItem {
  id: string
  title: string
  content: string
  allowAppend?: boolean
  appendTitle?: string
  isAppendActive?: boolean
  isMutex?: boolean          // 是否开启互斥追加
  mutexTargets?: string[]    // 互斥关联的其他预设 id 列表 (支持多选)
}

export type PresetEntry = string | QuickPresetItem

export function normalizePresetItem(entry: PresetEntry): QuickPresetItem {
  if (typeof entry === 'string') {
    return {
      id: `qp-${entry}`,
      title: entry,
      content: entry,
      allowAppend: false,
      appendTitle: entry,
      isAppendActive: false,
      isMutex: false,
      mutexTargets: []
    }
  }
  return {
    id: entry.id || `qp-${entry.title || Date.now()}`,
    title: entry.title || '',
    content: entry.content || entry.title || '',
    allowAppend: !!entry.allowAppend,
    appendTitle: entry.appendTitle || entry.title || '',
    isAppendActive: !!entry.isAppendActive,
    isMutex: !!entry.isMutex,
    mutexTargets: Array.isArray(entry.mutexTargets) ? entry.mutexTargets : []
  }
}

export function normalizePresetList(list: PresetEntry[] | undefined): QuickPresetItem[] {
  if (!list || !Array.isArray(list)) return []
  return list.map(normalizePresetItem)
}

export interface StatusPresetConfig {
  online: PresetEntry[]
}

export interface FlowPromptsConfig {
  online: {
    waitPollPrompt: string           // 在线等待轮询提示词 (支持 {wait_minutes}, {wait_ms}, {session_id}, {workflow_id})
    exhaustedPrompt: string          // 在线超限终态提示词 (支持 {max_checks}, {total_hours}, {workflow_id})
  }
}

export interface SecuritySettings {
  bruteForceProtection: boolean
  maxFailedAttempts: number
  lockoutMinutes: number
  whitelistIps?: string[]
}

export interface PhaseTemplateItem {
  id: string
  label: string
  description?: string
  prompt?: string
}

// 出厂服务指引与阶段模板的真源在 Go 端，前端这份由 internal/store 的 TestFrontendDefaultsInSync 生成。
export { DEFAULT_SERVER_INSTRUCTIONS }

export interface AppSettings {
  hostName: string
  defaultTimeoutSeconds: number
  serverInstructions?: string
  quickPresets: PresetEntry[]
  statusPresets: StatusPresetConfig
  flowPrompts: FlowPromptsConfig
  security: SecuritySettings
  userMemory: string
  phaseTemplate: PhaseTemplateItem[]
  autoExtendMinutes: number
  promptWaitMinutes: number
  maxNoFeedbackChecks: number
  defaultWaitCountdownMinutes: number
  /** 秒级默认挂起倒计时（秒回 0 / 30 / 60）；-1 表示没设过，沿用分钟值。 */
  defaultWaitCountdownSeconds?: number
  /** 异步对话：每条新消息给回复窗口加多少分钟（默认 5）/ 智能体单次计时器最长等多少分钟（默认 10）。 */
  asyncWindowAddMinutes?: number
  asyncWaitSliceMinutes?: number
  speechLang: string
  asrProvider: 'mimo' | 'webspeech'
  asrApiUrl: string
  asrApiKey: string
  asrModel: string
  asrLanguage: string
  asrStream: boolean
  soundEnabled: boolean
  desktopNotifyEnabled: boolean
  autoScrollToBottom: boolean
  imageRateLimitKB: number
  /** 快捷标签预设的版本留存：出厂文案的基线、历史与旧版 / 新版留存区（见 lib/presetVersions.ts）。 */
  presetVersions?: PresetVersions
  /** 会话提示词模板的全局覆盖（#232）：没覆盖的用出厂文字；只影响以后套用的会话（见 lib/residentPrompt.ts）。 */
  promptTemplates?: PromptTemplateOverrides
  /** 唤醒词的全局覆盖（#232 D3）：续聊 / 新开对话 / 网页启动提示词；没覆盖的用内置默认（见 lib/wakePrompts.ts）。 */
  wakeTemplates?: WakeTemplateOverrides
}

const STORAGE_KEY = 'relaymesh.settings'

const DEFAULT_SETTINGS: AppSettings = {
  hostName: '',
  defaultTimeoutSeconds: 120, // 2 minutes
  imageRateLimitKB: 0, // 默认 0 (不限速)
  serverInstructions: DEFAULT_SERVER_INSTRUCTIONS,
  phaseTemplate: DEFAULT_PHASE_TEMPLATE.map(p => ({ ...p })),
  quickPresets: [
    { id: 'qp-1', title: '按计划推进', content: '按计划推进', allowAppend: false, appendTitle: '按计划推进', isAppendActive: false, isMutex: false, mutexTargets: [] },
    { id: 'qp-2', title: '同意方案，请继续', content: '同意方案，请继续', allowAppend: false, appendTitle: '同意方案', isAppendActive: false, isMutex: false, mutexTargets: [] },
    { id: 'qp-3', title: '实施规范', content: '应先阐述对当前问题的理解，不修改代码，汇报后等待二次确认。', allowAppend: true, appendTitle: '实施规范', isAppendActive: false, isMutex: true, mutexTargets: ['qp-5'] },
    { id: 'qp-4', title: '已核对无误', content: '已核对无误', allowAppend: false, appendTitle: '核对确认', isAppendActive: false, isMutex: false, mutexTargets: [] },
    { id: 'qp-5', title: '需要调整方案', content: '需要调整方案，请暂停后续改动，先提供备选设计。', allowAppend: true, appendTitle: '方案调整', isAppendActive: false, isMutex: true, mutexTargets: ['qp-3'] }
  ],
  statusPresets: {
    online: [
      { id: 'qp-on-1', title: '按计划推进', content: '按计划推进', allowAppend: false, appendTitle: '按计划推进', isAppendActive: false, isMutex: false, mutexTargets: [] },
      { id: 'qp-on-2', title: '同意方案，请继续', content: '同意方案，请继续', allowAppend: false, appendTitle: '同意方案', isAppendActive: false, isMutex: false, mutexTargets: [] },
      { id: 'qp-on-3', title: '实施规范', content: '应先阐述对当前问题的理解，不修改代码，汇报后等待二次确认。', allowAppend: true, appendTitle: '实施规范', isAppendActive: false, isMutex: true, mutexTargets: ['qp-on-5'] },
      { id: 'qp-on-4', title: '已核对无误', content: '已核对无误', allowAppend: false, appendTitle: '核对确认', isAppendActive: false, isMutex: false, mutexTargets: [] },
      { id: 'qp-on-5', title: '需要调整方案', content: '需要调整方案，请暂停后续改动，先提供备选设计。', allowAppend: true, appendTitle: '方案调整', isAppendActive: false, isMutex: true, mutexTargets: ['qp-on-3'] }
    ]
  },
  security: {
    bruteForceProtection: true,
    maxFailedAttempts: 5,
    lockoutMinutes: 15,
    whitelistIps: ['127.0.0.1', '::1']
  },
  userMemory: '',
  flowPrompts: {
    online: {
      waitPollPrompt: '',
      exhaustedPrompt: ''
    }
  },
  autoExtendMinutes: 5,
  promptWaitMinutes: 2, // 新会话默认提示词等待 2 分钟 (2m)
  maxNoFeedbackChecks: 24, // 默认最大空回执检查 24 次
  defaultWaitCountdownMinutes: 2, // 默认等待倒计时 2 分钟 (0m, 1m, 2m)
  speechLang: 'zh-CN',
  asrProvider: 'mimo',
  asrApiUrl: 'https://api.xiaomimimo.com/v1/chat/completions',
  asrApiKey: '',
  asrModel: 'mimo-v2.5-asr',
  asrLanguage: 'auto',
  asrStream: true,
  soundEnabled: true,
  desktopNotifyEnabled: true,
  autoScrollToBottom: true
}

type StatusKey = 'online'

const asScope = <T extends { id: string }>(items: T[]): ScopeItem[] => JSON.parse(JSON.stringify(items)) as ScopeItem[]

/** 当前版本出厂的快捷标签预设（阶段模板、输入区的快捷回复预设、全局通用预设）。每次都是新拷贝，调用方可以随意改。 */
export function shippedPresetLists(): ScopeLists {
  return {
    phase: asScope(DEFAULT_PHASE_TEMPLATE),
    'status:online': asScope(normalizePresetList(DEFAULT_SETTINGS.statusPresets.online)),
    general: asScope(normalizePresetList(DEFAULT_SETTINGS.quickPresets)),
  }
}

const KNOWN_STALE_PREFIXES: Record<string, string[]> = {
  'online.waitPollPrompt': [
    '严格执行：等待 {wait_minutes} 分钟',
    '严格执行：等待',
  ],
  'online.exhaustedPrompt': [
    '用户反馈已超时，进入会话结束与环境收尾规程',
  ],
}

function migrateStaleFlowPrompts(fp: any): void {
  if (!fp) return
  for (const [path, prefixes] of Object.entries(KNOWN_STALE_PREFIXES)) {
    const [section, key] = path.split('.')
    const val = fp[section]?.[key]
    if (typeof val === 'string' && val.length > 0 && prefixes.some(p => val.startsWith(p))) {
      fp[section][key] = ''
    }
  }
}

function loadSettings(): AppSettings {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (raw) {
      const parsed = JSON.parse(raw)
      if (parsed.flowPrompts) {
        migrateStaleFlowPrompts(parsed.flowPrompts)
      }
      return dropRetiredSettings({
        ...DEFAULT_SETTINGS,
        ...parsed,
        serverInstructions: parsed.serverInstructions ?? DEFAULT_SETTINGS.serverInstructions,
        // 本地缓存里可能还留着五段式模板：收敛为三段式（服务端的迁移结果会在同步时覆盖它）
        phaseTemplate: normalizePhaseTemplate(parsed.phaseTemplate),
        security: {
          ...DEFAULT_SETTINGS.security,
          ...(parsed.security || {})
        },
        flowPrompts: {
          online: {
            ...DEFAULT_SETTINGS.flowPrompts.online,
            ...(parsed.flowPrompts?.online || {})
          }
        }
      })
    }
  } catch (e) {
    console.error('Failed to parse stored settings', e)
  }
  return { ...DEFAULT_SETTINGS }
}

export const useSettingsStore = defineStore('settings', () => {
  const settings = ref<AppSettings>(loadSettings())
  const isSettingsOpen = ref(false)
  const saveStatus = ref<'idle' | 'saving' | 'saved'>('idle')
  const blockedIPs = ref<any[]>([])
  const isLoadingBlockedIPs = ref(false)
  // 快捷标签预设的出厂文案更新了、而用户改过它：这些条目在此等用户选择（均保留 / 使用最新 / 使用旧版）
  const presetConflicts = ref<PresetConflict[]>([])
  // 用户点了「稍后」：本次页面不再自动弹出，设置页里仍有入口
  const presetConflictsPostponed = ref(false)
  // 刚刚自动换成新版的条数（用户没改过的）：给界面发一条「已更新」的提示，seq 区分每一次
  const presetUpgrade = ref<{ count: number; seq: number } | null>(null)
  let presetUpgradeSeq = 0
  const presetVersions = computed<PresetVersions>(() => normalizeVersions(settings.value.presetVersions))
  let saveTimer: number | null = null

  function triggerSaveStatus() {
    saveStatus.value = 'saving'
    if (saveTimer) window.clearTimeout(saveTimer)
    setTimeout(() => {
      saveStatus.value = 'saved'
      saveTimer = window.setTimeout(() => {
        saveStatus.value = 'idle'
      }, 1000)
    }, 250)
  }

  function openSettings() {
    isSettingsOpen.value = true
  }

  function closeSettings() {
    isSettingsOpen.value = false
  }

  function updateSettings(partial: Partial<AppSettings>) {
    settings.value = { ...settings.value, ...partial }
    save(true)
  }

  function getNormalizedStatusPresets(status: StatusKey = 'online'): QuickPresetItem[] {
    if (!settings.value.statusPresets || !settings.value.statusPresets[status]) {
      return normalizePresetList(DEFAULT_SETTINGS.statusPresets[status])
    }
    return normalizePresetList(settings.value.statusPresets[status])
  }

  function getNormalizedQuickPresets(): QuickPresetItem[] {
    return normalizePresetList(settings.value.quickPresets)
  }

  // ---------- 快捷标签预设的版本留存（判定逻辑在 lib/presetVersions.ts） ----------

  function currentPresetLists(): ScopeLists {
    return {
      phase: asScope(settings.value.phaseTemplate || []),
      'status:online': asScope(getNormalizedStatusPresets('online')),
      general: asScope(getNormalizedQuickPresets()),
    }
  }

  function applyPresetLists(lists: ScopeLists, versions?: PresetVersions) {
    settings.value = {
      ...settings.value,
      phaseTemplate: lists.phase as unknown as PhaseTemplateItem[],
      quickPresets: lists.general as unknown as QuickPresetItem[],
      statusPresets: {
        online: lists['status:online'] as unknown as QuickPresetItem[],
      },
      ...(versions ? { presetVersions: versions } : {}),
    }
  }

  /**
   * 对账出厂文案与用户的内容：用户没改过的自动换成新版；用户改过的列为待处理的冲突，等用户选择。
   * 有变化时写回设置并同步到服务端（再触发的同步事件对账结果不变，不会循环）。返回自动升级了几条。
   */
  function reconcilePresets(): number {
    const result = reconcile({
      shipped: shippedPresetLists(),
      mine: currentPresetLists(),
      versions: presetVersions.value,
      now: new Date().toISOString(),
    })
    presetConflicts.value = result.conflicts
    if (result.conflicts.length === 0) presetConflictsPostponed.value = false
    if (result.changed) {
      applyPresetLists(result.lists, result.versions)
      save()
    }
    return result.upgraded
  }

  /** 按用户的选择处理冲突；没给出选择的继续留在待处理里。返回处理了几条。 */
  function resolvePresetConflicts(decisions: Record<string, Decision>): number {
    const result = resolveConflicts({
      lists: currentPresetLists(),
      versions: presetVersions.value,
      conflicts: presetConflicts.value,
      decisions,
      now: new Date().toISOString(),
    })
    applyPresetLists(result.lists, result.versions)
    save(true)
    reconcilePresets()
    return result.resolved
  }

  function postponePresetConflicts() {
    presetConflictsPostponed.value = true
  }

  function reopenPresetConflicts() {
    presetConflictsPostponed.value = false
  }

  /** 重新启用留存区里的一条：作为一条新内容加回对应列表末尾。 */
  function reenablePresetVersion(uid: string): boolean {
    const out = reenableArchived(currentPresetLists(), presetVersions.value, uid)
    if (!out) return false
    applyPresetLists(out.lists, out.versions)
    save(true)
    return true
  }

  function deletePresetVersion(uid: string) {
    settings.value = { ...settings.value, presetVersions: deleteArchived(presetVersions.value, uid) }
    save(true)
  }

  function clearPresetVersions() {
    settings.value = { ...settings.value, presetVersions: clearArchive(presetVersions.value) }
    save(true)
  }

  /** 恢复默认：先把会被换掉的自定义内容（改过的、自建的）存进留存区，再换成出厂内容。 */
  function resetPresetScopes(scopes: PresetScope[]) {
    const shipped = shippedPresetLists()
    const current = currentPresetLists()
    const now = new Date().toISOString()
    let versions = presetVersions.value
    const next = { ...current }
    for (const scope of scopes) {
      versions = archiveReplaced(versions, scope, current[scope], shipped[scope], now)
      next[scope] = shipped[scope]
    }
    applyPresetLists(next, versions)
    save(true)
  }

  function resetPhaseTemplate() {
    resetPresetScopes(['phase'])
  }

  function resetGeneralPresets() {
    resetPresetScopes(['general'])
  }

  function saveStatusPresetItem(status: StatusKey, item: QuickPresetItem) {
    if (!settings.value.statusPresets) {
      settings.value.statusPresets = { ...DEFAULT_SETTINGS.statusPresets }
    }
    const currentList = normalizePresetList(settings.value.statusPresets[status] || [])
    const idx = currentList.findIndex(p => p.id === item.id)
    if (idx >= 0) {
      const existing = currentList[idx]
      currentList[idx] = {
        ...item,
        isAppendActive: existing.isAppendActive // 保留已有的运行时勾选状态
      }
    } else {
      currentList.push({ ...item, isAppendActive: false })
    }
    settings.value.statusPresets[status] = currentList
    save(true)
  }

  function toggleStatusPresetAppendActive(status: StatusKey, id: string) {
    if (!settings.value.statusPresets) {
      settings.value.statusPresets = { ...DEFAULT_SETTINGS.statusPresets }
    }
    const currentList = normalizePresetList(settings.value.statusPresets[status] || [])
    const item = currentList.find(p => p.id === id)
    if (item && item.allowAppend) {
      const nextActive = !item.isAppendActive
      item.isAppendActive = nextActive

      // 互斥联动：当本次操作为开启追加且当前项开启了互斥
      if (nextActive) {
        const itemTargets = new Set(item.mutexTargets || [])
        for (const other of currentList) {
          if (other.id === item.id) continue

          const isDirectTarget = item.isMutex && itemTargets.has(other.id)
          const isReverseTarget = other.isMutex && Array.isArray(other.mutexTargets) && other.mutexTargets.includes(item.id)
          const isBothMutexDefault = item.isMutex && other.isMutex && itemTargets.size === 0 && (!other.mutexTargets || other.mutexTargets.length === 0)

          if (isDirectTarget || isReverseTarget || isBothMutexDefault) {
            other.isAppendActive = false
          }
        }
      }

      settings.value.statusPresets[status] = currentList
      save(true)
    }
  }

  function removeStatusPresetItem(status: StatusKey, id: string) {
    if (!settings.value.statusPresets) return
    const currentList = normalizePresetList(settings.value.statusPresets[status] || [])
    settings.value.statusPresets[status] = currentList.filter(p => p.id !== id)
    save(true)
  }

  function saveQuickPresetItem(item: QuickPresetItem) {
    const currentList = normalizePresetList(settings.value.quickPresets || [])
    const idx = currentList.findIndex(p => p.id === item.id)
    if (idx >= 0) {
      const existing = currentList[idx]
      currentList[idx] = {
        ...item,
        isAppendActive: existing.isAppendActive
      }
    } else {
      currentList.push({ ...item, isAppendActive: false })
    }
    settings.value.quickPresets = currentList
    save(true)
  }

  function toggleQuickPresetAppendActive(id: string) {
    const currentList = normalizePresetList(settings.value.quickPresets || [])
    const item = currentList.find(p => p.id === id)
    if (item && item.allowAppend) {
      const nextActive = !item.isAppendActive
      item.isAppendActive = nextActive

      if (nextActive) {
        const itemTargets = new Set(item.mutexTargets || [])
        for (const other of currentList) {
          if (other.id === item.id) continue

          const isDirectTarget = item.isMutex && itemTargets.has(other.id)
          const isReverseTarget = other.isMutex && Array.isArray(other.mutexTargets) && other.mutexTargets.includes(item.id)
          const isBothMutexDefault = item.isMutex && other.isMutex && itemTargets.size === 0 && (!other.mutexTargets || other.mutexTargets.length === 0)

          if (isDirectTarget || isReverseTarget || isBothMutexDefault) {
            other.isAppendActive = false
          }
        }
      }

      settings.value.quickPresets = currentList
      save(true)
    }
  }

  function removeQuickPresetItem(id: string) {
    const currentList = normalizePresetList(settings.value.quickPresets || [])
    settings.value.quickPresets = currentList.filter(p => p.id !== id)
    save(true)
  }

  function addPreset(preset: string) {
    const current = normalizePresetList(settings.value.quickPresets)
    if (preset.trim() && !current.some(p => p.title === preset.trim())) {
      current.push({
        id: `qp-${Date.now()}`,
        title: preset.trim(),
        content: preset.trim(),
        allowAppend: false,
        appendTitle: preset.trim(),
        isAppendActive: false
      })
      settings.value.quickPresets = current
      save(true)
    }
  }

  function removePreset(index: number) {
    const current = normalizePresetList(settings.value.quickPresets)
    current.splice(index, 1)
    settings.value.quickPresets = current
    save(true)
  }

  function addStatusPreset(status: StatusKey, preset: string) {
    if (!settings.value.statusPresets) {
      settings.value.statusPresets = { ...DEFAULT_SETTINGS.statusPresets }
    }
    const current = normalizePresetList(settings.value.statusPresets[status] || [])
    if (preset.trim() && !current.some(p => p.title === preset.trim())) {
      current.push({
        id: `qp-${status}-${Date.now()}`,
        title: preset.trim(),
        content: preset.trim(),
        allowAppend: false,
        appendTitle: preset.trim(),
        isAppendActive: false
      })
      settings.value.statusPresets[status] = current
      save(true)
    }
  }

  function removeStatusPreset(status: StatusKey, index: number) {
    if (settings.value.statusPresets && settings.value.statusPresets[status]) {
      const current = normalizePresetList(settings.value.statusPresets[status])
      current.splice(index, 1)
      settings.value.statusPresets[status] = current
      save(true)
    }
  }

  function resetStatusPresets(status?: StatusKey) {
    if (status) {
      resetPresetScopes([`status:${status}`])
      return
    }
    resetPresetScopes(['status:online'])
  }

  function updateFlowPrompt(status: 'online', key: keyof FlowPromptsConfig['online'], prompt: string) {
    if (!settings.value.flowPrompts) {
      settings.value.flowPrompts = { ...DEFAULT_SETTINGS.flowPrompts }
    }
    if (!settings.value.flowPrompts[status]) {
      settings.value.flowPrompts[status] = { ...DEFAULT_SETTINGS.flowPrompts[status] }
    }
    settings.value.flowPrompts[status][key] = prompt
    save(true)
  }

  function resetFlowPrompts(status: 'online') {
    if (!settings.value.flowPrompts) {
      settings.value.flowPrompts = { ...DEFAULT_SETTINGS.flowPrompts }
    }
    settings.value.flowPrompts[status] = { ...DEFAULT_SETTINGS.flowPrompts[status] }
    save(true)
  }

  function resetServerInstructions() {
    settings.value.serverInstructions = DEFAULT_SERVER_INSTRUCTIONS
    save(true)
  }

  function resetToDefault() {
    // 全部恢复默认：快捷标签预设里改过的、自建的先进留存区，留存区本身不丢
    const shipped = shippedPresetLists()
    const current = currentPresetLists()
    const now = new Date().toISOString()
    let versions = presetVersions.value
    for (const scope of Object.keys(shipped) as PresetScope[]) {
      versions = archiveReplaced(versions, scope, current[scope], shipped[scope], now)
    }
    settings.value = { ...DEFAULT_SETTINGS, presetVersions: versions }
    save(true)
  }

  // 读取按「发出的先后」落地：晚发的先到之后，早发的慢响应不能再盖回去。
  // 启动时的读取、settings_updated 触发的读取与实时流连上之后的补取（#243）可能同时在路上，响应的到达顺序不由发出顺序决定。
  let fetchRemoteIssued = 0
  let fetchRemoteApplied = 0

  async function fetchRemoteSettings() {
    const mine = ++fetchRemoteIssued
    try {
      const res = await settingsApi.get()
      if (mine < fetchRemoteApplied) return
      fetchRemoteApplied = mine
      if (res && res.settings && Object.keys(res.settings).length > 0) {
        // 服务端若还留着已下线功能（我的状态：暂离 / 托管）的字段：丢掉，并在同步完后存一次，让它们从服务端也消失
        const retiredOnServer = hasRetiredSettings(res.settings)
        settings.value = dropRetiredSettings({
          ...settings.value,
          ...res.settings,
          serverInstructions: res.settings.serverInstructions ?? settings.value.serverInstructions ?? DEFAULT_SETTINGS.serverInstructions,
          phaseTemplate: normalizePhaseTemplate(res.settings.phaseTemplate ?? settings.value.phaseTemplate),
          flowPrompts: {
            online: {
              ...DEFAULT_SETTINGS.flowPrompts.online,
              ...(res.settings.flowPrompts?.online || {})
            }
          }
        })
        localStorage.setItem(STORAGE_KEY, JSON.stringify(settings.value))
        // 以服务端为准之后再对账：出厂文案更新了，用户没改过的自动换新、改过的等用户选择
        try {
          const upgraded = reconcilePresets()
          if (upgraded > 0) presetUpgrade.value = { count: upgraded, seq: ++presetUpgradeSeq }
        } catch (e) {
          console.warn('Failed to reconcile preset versions:', e)
        }
        if (retiredOnServer) save()
      }
    } catch (e) {
      console.warn('Failed to fetch remote settings, using local settings:', e)
    }
  }

  /**
   * 实时流（首次或重新）连上：页面载入到连上之间、断线期间别处改了全局设置，settings_updated 没人听见也不会补发，
   * 这里重取一次（#243）。取失败时 fetchRemoteSettings 自己兜住并继续用本地副本。
   */
  function onConnected() {
    void fetchRemoteSettings()
  }

  function save(notify = false) {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(settings.value))
    if (notify) {
      triggerSaveStatus()
    }
    // 异步同步至后端 SQLite 数据库
    settingsApi.update(settings.value).catch(err => {
      console.warn('Failed to sync settings to server database:', err)
    })
  }

  async function fetchBlockedIPs() {
    isLoadingBlockedIPs.value = true
    try {
      const res = await authApi.getBlockedIPs()
      blockedIPs.value = res.blocked_ips || []
    } catch (e) {
      console.warn('Failed to fetch blocked ips:', e)
    } finally {
      isLoadingBlockedIPs.value = false
    }
  }

  async function unblockIP(ip: string) {
    try {
      await authApi.unblockIP(ip)
      await fetchBlockedIPs()
    } catch (e) {
      console.error('Failed to unblock ip:', e)
    }
  }

  async function clearAllBlockedIPs() {
    try {
      await authApi.clearAllBlockedIPs()
      await fetchBlockedIPs()
    } catch (e) {
      console.error('Failed to clear blocked ips:', e)
    }
  }

  return {
    settings,
    isSettingsOpen,
    saveStatus,
    blockedIPs,
    isLoadingBlockedIPs,
    presetConflicts,
    presetConflictsPostponed,
    presetUpgrade,
    presetVersions,
    reconcilePresets,
    resolvePresetConflicts,
    postponePresetConflicts,
    reopenPresetConflicts,
    reenablePresetVersion,
    deletePresetVersion,
    clearPresetVersions,
    resetPhaseTemplate,
    resetGeneralPresets,
    fetchRemoteSettings,
    onConnected,
    fetchBlockedIPs,
    unblockIP,
    clearAllBlockedIPs,
    triggerSaveStatus,
    openSettings,
    closeSettings,
    updateSettings,
    getNormalizedStatusPresets,
    getNormalizedQuickPresets,
    saveStatusPresetItem,
    toggleStatusPresetAppendActive,
    removeStatusPresetItem,
    saveQuickPresetItem,
    toggleQuickPresetAppendActive,
    removeQuickPresetItem,
    addPreset,
    removePreset,
    addStatusPreset,
    removeStatusPreset,
    resetStatusPresets,
    updateFlowPrompt,
    resetFlowPrompts,
    resetServerInstructions,
    resetToDefault
  }
})
