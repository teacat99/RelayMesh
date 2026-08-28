<script setup lang="ts">
import { ref } from 'vue'
import WaitingSettings from './settings/WaitingSettings.vue'
import FlowPromptsTab from './settings/FlowPromptsTab.vue'
import AsrSettings from './settings/AsrSettings.vue'
import PresetSettings from './settings/PresetSettings.vue'
import AppearanceSettings from './settings/AppearanceSettings.vue'
import SecurityMcpTab from './settings/SecurityMcpTab.vue'
import DataSettings from './settings/DataSettings.vue'
import AboutSettings from './settings/AboutSettings.vue'
import {
  Clock,
  FileText,
  Mic,
  MessageSquareQuote,
  Sparkles,
  Shield,
  Database,
  Info
} from 'lucide-vue-next'

const activeTab = ref<'waiting' | 'prompts' | 'speech' | 'presets' | 'appearance' | 'security' | 'data' | 'about'>('waiting')

const tabOptions = [
  { id: 'waiting', label: '等待策略', icon: Clock },
  { id: 'prompts', label: '提示词流转', icon: FileText },
  { id: 'speech', label: '语音 ASR', icon: Mic },
  { id: 'presets', label: '快捷标签预设', icon: MessageSquareQuote },
  { id: 'appearance', label: '外观与通知', icon: Sparkles },
  { id: 'security', label: 'MCP 与安全', icon: Shield },
  { id: 'data', label: '数据与维护', icon: Database },
  { id: 'about', label: '关于 RelayMesh', icon: Info }
]
</script>

<template>
  <div class="flex flex-col gap-3.5 h-full min-h-[420px]">
    <!-- 顶部水平分类切换导航栏 (Top Tab Navigation - 上一版标准圆角与紧凑标签) -->
    <div class="w-full shrink-0 border-b border-border/70 pb-2">
      <div class="flex items-center gap-1.5 overflow-x-auto no-scrollbar py-0.5">
        <button
          v-for="tab in tabOptions"
          :key="tab.id"
          type="button"
          class="flex items-center gap-1.5 px-3 py-1.5 rounded-sm text-xs font-mono font-medium transition-all shrink-0 cursor-pointer select-none border"
          :class="activeTab === tab.id
            ? 'bg-primary text-primary-foreground border-primary font-bold shadow-xs'
            : 'bg-card/70 border-border/70 text-muted-foreground hover:text-foreground hover:bg-muted hover:border-border'"
          @click="activeTab = tab.id as any"
        >
          <component :is="tab.icon" class="w-3.5 h-3.5 shrink-0" />
          <span>{{ tab.label }}</span>
        </button>
      </div>
    </div>

    <!-- Main Content Area -->
    <div class="flex-1 overflow-y-auto pr-1 py-1">
      <WaitingSettings v-if="activeTab === 'waiting'" />
      <FlowPromptsTab v-else-if="activeTab === 'prompts'" />
      <AsrSettings v-else-if="activeTab === 'speech'" />
      <PresetSettings v-else-if="activeTab === 'presets'" />
      <AppearanceSettings v-else-if="activeTab === 'appearance'" />
      <SecurityMcpTab v-else-if="activeTab === 'security'" />
      <DataSettings v-else-if="activeTab === 'data'" />
      <AboutSettings v-else-if="activeTab === 'about'" />
    </div>
  </div>
</template>
