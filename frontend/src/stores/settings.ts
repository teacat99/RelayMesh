import { defineStore } from 'pinia'
import { ref, watch } from 'vue'
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
  away: PresetEntry[]
  autopilot: PresetEntry[]
}

export interface StatusStrategyConfig {
  online: {
    interactiveLevel: string
    onTimeout: string
    irreversibleAction: string
  }
  away: {
    interactiveLevel: string
    onTimeout: string
    irreversibleAction: string
  }
  autopilot: {
    interactiveLevel: string
    stopConditions: string
    irreversibleAction: string
  }
}

export interface FlowPromptsConfig {
  online: {
    waitPollPrompt: string           // 在线等待轮询提示词 (支持 {wait_minutes}, {wait_ms}, {session_id}, {workflow_id})
    exhaustedPrompt: string          // 在线超限终态提示词 (支持 {max_checks}, {total_hours}, {workflow_id})
  }
  away: {
    immediatePrompt: string          // 安全兜底模式提示词 (支持 {session_id}, {workflow_id})
  }
  autopilot: {
    immediatePrompt: string          // 外部编排模式提示词 (支持 {session_id}, {workflow_id})
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

export interface AppSettings {
  hostName: string
  defaultTimeoutSeconds: number
  quickPresets: PresetEntry[]
  statusPresets: StatusPresetConfig
  statusStrategies: StatusStrategyConfig
  flowPrompts: FlowPromptsConfig
  security: SecuritySettings
  userMemory: string
  phaseTemplate: PhaseTemplateItem[]
  autoExtendMinutes: number
  promptWaitMinutes: number
  maxNoFeedbackChecks: number
  defaultWaitCountdownMinutes: number
  userPresence: 'online' | 'away' | 'autopilot'
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
}

const STORAGE_KEY = 'relaymesh.settings'

const DEFAULT_SETTINGS: AppSettings = {
  hostName: '',
  defaultTimeoutSeconds: 120, // 2 minutes
  phaseTemplate: [
    { id: 'assess', label: '评估', description: '需求接入与理解确认', prompt: '当前处于需求评估阶段。通过 feedback 收集用户描述，逐条记录到会话文档，保留用户原话。对每条需求复述自己的理解：真实场景、根因推测、期望行为、验收标准。等待用户确认后再进入方案阶段。不急于敲定方案选型，先听完并理解真实需求，并引导用户完善需求，汇报不同方案的利弊，对每个需求列出推荐方案、风险、改动范围与备选。方向敲定后可调整到方案阶段。⚠️ 本阶段禁止修改代码。可以读取代码验证可行性，但不得创建、修改或删除任何源代码文件。如确需修改代码，必须先通过 feedback 获得用户二次确认并切换到开发阶段。' },
    { id: 'plan', label: '方案', description: '方案设计与评审拍板', prompt: '当前处于方案设计阶段。决策确认→问题拆解→数据流→不变量→边界→影响面→备选→验收。阅读代码和文档，注意核对方案可行性，确保实施阶段的逻辑闭环；通过 feedback 与用户逐项确认，将每条决策写入会话文档「关键决策」。决策全部锁定后等待用户确认再进入开发。⚠️ 本阶段禁止修改代码。可以读取代码验证可行性，但不得创建、修改或删除任何源代码文件。如确需修改代码，必须先通过 feedback 获得用户二次确认并切换到开发阶段。' },
    { id: 'dev', label: '开发', description: '编码实施与增量验证', prompt: '当前处于开发执行阶段。改前先读相关代码与文档，沿用项目惯用模式。每完成一个逻辑单元立即增量验证（lint/type-check→build），不等全部完成再统一修。改 import/接口/类型时检查所有引用方。每 200-500 行改动即 commit。如发现方案和代码冲突，先记录并尝试解决，解决不了则向用户汇报，并回退到方案阶段。开发完成后进入验证阶段。' },
    { id: 'verify', label: '验证', description: '三件套通过与功能验证', prompt: '当前处于部署验证阶段。执行三件套：lint/type-check→build→功能验证。部署、DB迁移、push main 等不可逆操作必须二次确认。完成标准 = 功能 + 类型 + 编译 + 校验 + 文档同步 + 配置同步 + 开发记录，每条须有可验证证据。验证失败回退到开发阶段修复，验证成功进入完成阶段，使用 feedback 汇报。' },
    { id: 'done', label: '完成', description: '汇报完成与等待下一步', prompt: '当前阶段的开发任务完成。通过 feedback 提交最终汇报：修改内容、原因、影响范围、验证结果与后续建议。盘点后台进程和未提交变更，归档会话文档。等待用户确认下一步需求或结束会话。' },
  ],
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
    ],
    away: [
      { id: 'qp-aw-1', title: '暂缓执行，待进一步讨论', content: '暂缓执行，待进一步讨论', allowAppend: false, appendTitle: '暂缓执行', isAppendActive: false, isMutex: false, mutexTargets: [] },
      { id: 'qp-aw-2', title: '已记录，稍后处理', content: '已记录，稍后处理', allowAppend: false, appendTitle: '已记录', isAppendActive: false, isMutex: false, mutexTargets: [] },
      { id: 'qp-aw-3', title: '仅执行只读分析', content: '仅执行只读分析，不要修改任何代码和文件', allowAppend: true, appendTitle: '执行限制', isAppendActive: false, isMutex: true, mutexTargets: [] }
    ],
    autopilot: [
      { id: 'qp-ap-1', title: '全自动自驾推进', content: '全自动自驾推进', allowAppend: false, appendTitle: '自驾模式', isAppendActive: false, isMutex: false, mutexTargets: [] },
      { id: 'qp-ap-2', title: '遇阻跳过并记录', content: '遇阻跳过并记录', allowAppend: false, appendTitle: '异常策略', isAppendActive: false, isMutex: false, mutexTargets: [] },
      { id: 'qp-ap-3', title: '完成全部规划后最终汇报', content: '完成全部规划后最终汇报', allowAppend: false, appendTitle: '汇报节点', isAppendActive: false, isMutex: false, mutexTargets: [] },
      { id: 'qp-ap-4', title: '遇到不可逆硬停点即停', content: '遇到不可逆硬停点（如生产部署、数据库迁移、破坏性删除）必须立即暂停并汇报', allowAppend: true, appendTitle: '安全硬停约束', isAppendActive: false, isMutex: true, mutexTargets: [] }
    ]
  },
  statusStrategies: {
    online: {
      interactiveLevel: '高频互动 · 即时确认',
      onTimeout: '倒计时提醒，等待用户现场确认',
      irreversibleAction: '关键决策与不可逆操作现场拍板'
    },
    away: {
      interactiveLevel: '批量答复 · 暂存待决',
      onTimeout: '放宽超时容忍，自动保持会话活跃',
      irreversibleAction: '不可逆动作一律暂缓，待归来确认'
    },
    autopilot: {
      interactiveLevel: '静默自驾 · 跨阶段推进',
      stopConditions: '范围到点 / 阻塞卡点 / 验收完成 / 命中即停',
      irreversibleAction: '命中不可逆硬停点（部署/迁移/删数据）即停'
    }
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
    },
    away: {
      immediatePrompt: ''
    },
    autopilot: {
      immediatePrompt: ''
    }
  },
  autoExtendMinutes: 5,
  promptWaitMinutes: 2, // 新会话默认提示词等待 2 分钟 (2m)
  maxNoFeedbackChecks: 24, // 默认最大空回执检查 24 次
  defaultWaitCountdownMinutes: 2, // 默认等待倒计时 2 分钟 (0m, 1m, 2m)
  userPresence: 'online', // 默认在线状态: 'online' 在线 | 'away' 暂离 | 'autopilot' 托管
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

const KNOWN_STALE_PREFIXES: Record<string, string[]> = {
  'online.waitPollPrompt': [
    '严格执行：等待 {wait_minutes} 分钟',
    '严格执行：等待',
  ],
  'online.exhaustedPrompt': [
    '用户反馈已超时，进入会话结束与环境收尾规程',
  ],
  'away.immediatePrompt': [
    '【系统回执·用户暂离】用户当前处于暂离状态',
  ],
  'autopilot.immediatePrompt': [
    '【系统回执·托管自驾】当前处于 M-C 自驾模式',
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
      return {
        ...DEFAULT_SETTINGS,
        ...parsed,
        security: {
          ...DEFAULT_SETTINGS.security,
          ...(parsed.security || {})
        },
        flowPrompts: {
          online: {
            ...DEFAULT_SETTINGS.flowPrompts.online,
            ...(parsed.flowPrompts?.online || {})
          },
          away: {
            ...DEFAULT_SETTINGS.flowPrompts.away,
            ...(parsed.flowPrompts?.away || {})
          },
          autopilot: {
            ...DEFAULT_SETTINGS.flowPrompts.autopilot,
            ...(parsed.flowPrompts?.autopilot || {})
          }
        }
      }
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

  function getNormalizedStatusPresets(status: 'online' | 'away' | 'autopilot'): QuickPresetItem[] {
    if (!settings.value.statusPresets || !settings.value.statusPresets[status]) {
      return normalizePresetList(DEFAULT_SETTINGS.statusPresets[status])
    }
    return normalizePresetList(settings.value.statusPresets[status])
  }

  function getNormalizedQuickPresets(): QuickPresetItem[] {
    return normalizePresetList(settings.value.quickPresets)
  }

  function saveStatusPresetItem(status: 'online' | 'away' | 'autopilot', item: QuickPresetItem) {
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

  function toggleStatusPresetAppendActive(status: 'online' | 'away' | 'autopilot', id: string) {
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

  function removeStatusPresetItem(status: 'online' | 'away' | 'autopilot', id: string) {
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

  function addStatusPreset(status: 'online' | 'away' | 'autopilot', preset: string) {
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

  function removeStatusPreset(status: 'online' | 'away' | 'autopilot', index: number) {
    if (settings.value.statusPresets && settings.value.statusPresets[status]) {
      const current = normalizePresetList(settings.value.statusPresets[status])
      current.splice(index, 1)
      settings.value.statusPresets[status] = current
      save(true)
    }
  }

  function resetStatusPresets(status?: 'online' | 'away' | 'autopilot') {
    if (!settings.value.statusPresets) {
      settings.value.statusPresets = { ...DEFAULT_SETTINGS.statusPresets }
    }
    if (status) {
      settings.value.statusPresets[status] = [...DEFAULT_SETTINGS.statusPresets[status]]
    } else {
      settings.value.statusPresets = { ...DEFAULT_SETTINGS.statusPresets }
      settings.value.statusStrategies = { ...DEFAULT_SETTINGS.statusStrategies }
    }
    save(true)
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

  function resetToDefault() {
    settings.value = { ...DEFAULT_SETTINGS }
    save(true)
  }

  async function fetchRemoteSettings() {
    try {
      const res = await settingsApi.get()
      if (res && res.settings && Object.keys(res.settings).length > 0) {
        settings.value = {
          ...settings.value,
          ...res.settings,
          flowPrompts: {
            online: {
              ...DEFAULT_SETTINGS.flowPrompts.online,
              ...(res.settings.flowPrompts?.online || {})
            },
            away: {
              ...DEFAULT_SETTINGS.flowPrompts.away,
              ...(res.settings.flowPrompts?.away || {})
            },
            autopilot: {
              ...DEFAULT_SETTINGS.flowPrompts.autopilot,
              ...(res.settings.flowPrompts?.autopilot || {})
            }
          }
        }
        localStorage.setItem(STORAGE_KEY, JSON.stringify(settings.value))
      }
    } catch (e) {
      console.warn('Failed to fetch remote settings, using local settings:', e)
    }
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
    fetchRemoteSettings,
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
    resetToDefault
  }
})
