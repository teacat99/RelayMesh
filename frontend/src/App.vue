<script setup lang="ts">
import { watchEffect } from 'vue'
import AppLayout from './layouts/AppLayout.vue'
import { Toaster } from './components/ui/sonner'
import ImageLightbox from './components/preview/ImageLightbox.vue'
import UnlockDialog from './components/terminal/UnlockDialog.vue'
import PresetUpdateDialog from './components/settings/PresetUpdateDialog.vue'
import { useAppMode } from './composables/useAppMode'
import { useCrossModeNotice } from './composables/useCrossModeNotice'
import { useNotifyRuntime } from './composables/useNotifyRuntime'

// 当前项目写到 <html data-app-mode>：只靠样式区分两个项目的地方（消息提示的位置与动画等）读它，不必各自再去解析路由。
const appMode = useAppMode()
watchEffect(() => {
  document.documentElement.dataset.appMode = appMode.value
})

// 人在 ExecMesh 里时，RelayMesh 的新反馈请求用一条带「查看」入口的消息提醒。
useCrossModeNotice()

// 统一通知总线：呈现服务端放行的通知（多标签选主、前台抑制、标题角标）、处理通知深链 /?wf=&s=。
useNotifyRuntime()
</script>

<template>
  <AppLayout>
    <router-view />
    <Toaster />
    <ImageLightbox />
    <!-- 终端解锁对话框全局挂载：设置页里给令牌授予终端权限等操作也会向它请求二次验证 -->
    <UnlockDialog />
    <!-- 快捷标签预设的出厂文案更新、而用户改过时，逐条选择「均保留 / 使用最新 / 使用旧版」 -->
    <PresetUpdateDialog />
  </AppLayout>
</template>
