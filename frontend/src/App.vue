<script setup lang="ts">
import { onBeforeUnmount, ref, watch, watchEffect } from 'vue'
import { toast } from 'vue-sonner'
import AppLayout from './layouts/AppLayout.vue'
import { Toaster } from './components/ui/sonner'
import ImageLightbox from './components/preview/ImageLightbox.vue'
import { useAppMode } from './composables/useAppMode'
import { useCrossModeNotice } from './composables/useCrossModeNotice'
import { useNotifyRuntime } from './composables/useNotifyRuntime'
import { useSettingsStore } from './stores/settings'
import { useTerminalStore } from './stores/terminal'
import { lazyDialog } from './utils/lazyComponent'
import { chunkLoaders, installPrefetchHints } from './utils/lazyChunks'

// 当前项目写到 <html data-app-mode>：只靠样式区分两个项目的地方（消息提示的位置与动画等）读它，不必各自再去解析路由。
const appMode = useAppMode()
watchEffect(() => {
  document.documentElement.dataset.appMode = appMode.value
})

// 人在 ExecMesh 里时，RelayMesh 的新反馈请求用一条带「查看」入口的消息提醒。
useCrossModeNotice()

// 统一通知总线：呈现服务端放行的通知（多标签选主、前台抑制、标题角标）、处理通知深链 /?wf=&s=。
useNotifyRuntime()

// 全局弹窗是懒加载块（#186）：只有第一次真的要打开时才取（块此时才取回并挂载），挂上之后保持挂载（关闭动画与内部状态照旧）。
const settingsStore = useSettingsStore()
const terminalStore = useTerminalStore()
// 终端解锁：块没能取回时收回这次等待，免得发起解锁的操作一直挂着
const UnlockDialog = lazyDialog(chunkLoaders.unlock, () => terminalStore.settleUnlock(false))
const PresetUpdateDialog = lazyDialog(chunkLoaders.presetUpdate)
const unlockMounted = ref(false)
const presetMounted = ref(false)
watch(
  () => terminalStore.unlockDialogOpen,
  open => {
    if (open) unlockMounted.value = true
  },
  { immediate: true },
)
watch(
  () => settingsStore.presetConflicts.length > 0,
  has => {
    if (has) presetMounted.value = true
  },
  { immediate: true },
)

// 用户没改过的快捷预设在后台自动换成了新版：告诉一声，旧版留在「版本留存」里。
// 这条提示原来写在 PresetUpdateDialog 里（全局挂载）；弹窗改成懒加载之后留在这里，没有冲突时也要提示。
watch(
  () => settingsStore.presetUpgrade,
  n => {
    if (!n || appMode.value !== 'relay') return
    toast.info(`${n.count} 条快捷预设已更新为新版`, { description: '你没改过它们；旧版在「设置 → 快捷标签预设 → 版本留存」' })
  },
)

// 悬停 / 聚焦 / 触摸到带 data-prefetch 的入口（设置按钮、任务 / 项目 / 群聊入口）就提前取对应的块
const stopPrefetchHints = installPrefetchHints()
onBeforeUnmount(stopPrefetchHints)
</script>

<template>
  <AppLayout>
    <router-view />
    <Toaster />
    <ImageLightbox />
    <!-- 终端解锁对话框全局挂载：设置页里给令牌授予终端权限等操作也会向它请求二次验证（懒加载块，第一次要解锁时才挂载） -->
    <UnlockDialog v-if="unlockMounted" />
    <!-- 快捷标签预设的出厂文案更新、而用户改过时，逐条选择「均保留 / 使用最新 / 使用旧版」（懒加载块，出现冲突时才挂载） -->
    <PresetUpdateDialog v-if="presetMounted" />
  </AppLayout>
</template>
