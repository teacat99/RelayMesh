import type { Component } from 'vue'
import {
  Archive,
  Clock,
  Cpu,
  Database,
  Download,
  FileText,
  Info,
  Mic,
  MessageSquareQuote,
  Shield,
  ShieldCheck,
  Sparkles,
  Terminal
} from 'lucide-vue-next'
import type { SettingsCategoryId } from '../../lib/settingsRegistry'

// 每个设置分类的图标。左栏（SettingsRail）与窄屏的「当前分类」选择条（SettingsPicker）共用同一份，
// 用 Record<SettingsCategoryId, …> 约束：新增分类却忘了配图标时，类型检查会直接报错。
export const SETTINGS_ICONS: Record<SettingsCategoryId, Component> = {
  waiting: Clock,
  prompts: FileText,
  speech: Mic,
  presets: MessageSquareQuote,
  authProfiles: ShieldCheck,
  sessionData: Database,
  termPrefs: Terminal,
  modelAccess: Cpu,
  retention: Archive,
  appearance: Sparkles,
  security: Shield,
  backup: Download,
  about: Info
}
