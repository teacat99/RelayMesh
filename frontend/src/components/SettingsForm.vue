<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { useMediaQuery } from '@vueuse/core'
import WaitingSettings from './settings/WaitingSettings.vue'
import FlowPromptsTab from './settings/FlowPromptsTab.vue'
import AsrSettings from './settings/AsrSettings.vue'
import PresetSettings from './settings/PresetSettings.vue'
import AuthProfileSettings from './settings/AuthProfileSettings.vue'
import SessionPromptSettings from './settings/SessionPromptSettings.vue'
import AppearanceSettings from './settings/AppearanceSettings.vue'
import SecurityMcpTab from './settings/SecurityMcpTab.vue'
import OAuthSettingsPanel from './settings/OAuthSettingsPanel.vue'
import DataSettings from './settings/DataSettings.vue'
import AboutSettings from './settings/AboutSettings.vue'
import ExecTerminalPrefs from './settings/ExecTerminalPrefs.vue'
import ExecAccessSettings from './settings/ExecAccessSettings.vue'
import ExecRetentionSettings from './settings/ExecRetentionSettings.vue'
import SettingsRail from './SettingsRail.vue'
import SettingsPicker from './SettingsPicker.vue'
import SettingsModuleSwitch from './SettingsModuleSwitch.vue'
import { useSettingsUiStore } from '../stores/settingsUi'
import { categoryById } from '../lib/settingsRegistry'
import type { AppMode } from '../lib/mode'

// 设置内容的框架（桌面弹窗与移动端设置页共用）：
//   宽屏（≥768px）  左侧一整列分类导航（SettingsRail：模块分段切换 + 本模块 / 通用两组分类），右侧是当前分类的内容；
//   窄屏（<768px） 顶部「模块分段切换 + 当前分类选择条」（SettingsPicker），下面是当前分类的内容。
// 框架自己撑满宿主给的高度（宿主必须给它一个有确定高度的容器），只有右侧内容区滚动，左栏 / 选择条始终可见。
// 「去另一个模块的设置」只发出 jump 事件，由宿主决定怎么切背景页面。
const emit = defineEmits<{ jump: [next: AppMode] }>()
const ui = useSettingsUiStore()

// 与 Tailwind 的 md 断点一致（768px），也与各处进入设置的分流一致（窄于 768px 走设置页，其余开弹窗）。
const wide = useMediaQuery('(min-width: 768px)')
const current = computed(() => categoryById(ui.category))

const scroller = ref<HTMLElement | null>(null)
// 内容区在各分类之间是同一个滚动容器：不重置的话，换分类后会停在上一个分类的滚动位置上。
watch(
  () => [ui.module, ui.category],
  () => { void nextTick(() => scroller.value?.scrollTo({ top: 0 })) }
)
</script>

<template>
  <div class="flex h-full min-h-0" :class="wide ? 'flex-row' : 'flex-col'" data-testid="settings-shell" :data-layout="wide ? 'rail' : 'picker'">
    <SettingsRail v-if="wide" @jump="emit('jump', $event)" />

    <div class="flex min-h-0 min-w-0 flex-1 flex-col">
      <!-- 窄屏：模块分段切换 + 当前分类选择条，固定在内容区上方 -->
      <div v-if="!wide" class="shrink-0 space-y-2 border-b border-border/70 bg-muted/20 px-3.5 py-2.5">
        <SettingsModuleSwitch @jump="emit('jump', $event)" />
        <SettingsPicker />
      </div>

      <!-- 当前分类的内容 -->
      <div
        id="settings-panel"
        ref="scroller"
        role="tabpanel"
        class="min-h-0 flex-1 overflow-y-auto"
        :class="wide ? 'px-6 py-5' : 'px-3.5 py-3.5'"
        :aria-label="current?.label"
        :data-testid="`settings-panel-${ui.category}`"
      >
        <!-- 一句话说明这个分类管什么。不再另放分类标题：左栏 / 选择条已经标出当前分类，
             而且有的面板（终端偏好、模型执行与权限、记录与保留……）自己就带同名标题，再放一个会重复。 -->
        <p v-if="current" class="mb-4 text-xs leading-relaxed text-muted-foreground md:mb-5" data-testid="settings-hint">{{ current.hint }}</p>

        <WaitingSettings v-if="ui.category === 'waiting'" />
        <FlowPromptsTab v-else-if="ui.category === 'prompts'" />
        <AsrSettings v-else-if="ui.category === 'speech'" />
        <PresetSettings v-else-if="ui.category === 'presets'" />
        <AuthProfileSettings v-else-if="ui.category === 'authProfiles'" />
        <SessionPromptSettings v-else-if="ui.category === 'sessionPrompts'" />
        <DataSettings v-else-if="ui.category === 'sessionData'" scope="sessionData" />
        <ExecTerminalPrefs v-else-if="ui.category === 'termPrefs'" />
        <ExecAccessSettings v-else-if="ui.category === 'modelAccess'" />
        <ExecRetentionSettings v-else-if="ui.category === 'retention'" />
        <AppearanceSettings v-else-if="ui.category === 'appearance'" />
        <SecurityMcpTab v-else-if="ui.category === 'security'" />
        <OAuthSettingsPanel v-else-if="ui.category === 'oauth'" />
        <DataSettings v-else-if="ui.category === 'backup'" scope="backup" />
        <AboutSettings v-else-if="ui.category === 'about'" />
      </div>
    </div>
  </div>
</template>
