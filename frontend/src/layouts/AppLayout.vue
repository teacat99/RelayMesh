<script setup lang="ts">
import { onMounted } from 'vue'
import { useRoute } from 'vue-router'
import { useSessionStore } from '../stores/session'
import { useNotifyStore } from '../stores/notify'
import { useThemeStore } from '../stores/theme'
import { useSettingsStore } from '../stores/settings'
import { useAuthStore } from '../stores/auth'
import { registerUnauthorizedHandler } from '../api/client'
import SettingsDialog from '../components/SettingsDialog.vue'
import LoginDialog from '../components/auth/LoginDialog.vue'
import Button from '../components/ui/button/Button.vue'
import {
  MessageSquareCode,
  Layers,
  History,
  Sun,
  Moon,
  Bell,
  BellOff,
  Volume2,
  VolumeX,
  Radio,
  Settings
} from 'lucide-vue-next'

const route = useRoute()
const sessionStore = useSessionStore()
const notifyStore = useNotifyStore()
const themeStore = useThemeStore()
const settingsStore = useSettingsStore()
const authStore = useAuthStore()

onMounted(async () => {
  registerUnauthorizedHandler(() => {
    authStore.handleUnauthorized()
  })

  // 1. 优先校验鉴权状态
  const isAuth = await authStore.checkAuthStatus()
  if (isAuth) {
    sessionStore.connectSSE()
    sessionStore.fetchCurrentSession()
    sessionStore.fetchSessions()
  }
})
</script>

<template>
  <div class="h-full w-full h-[100dvh] flex bg-background text-foreground overflow-hidden selection:bg-primary/20">
    <!-- Main Full-Height App Canvas -->
    <slot />

    <!-- Global Settings Dialog -->
    <SettingsDialog />

    <!-- Public Internet Auth Login Dialog -->
    <LoginDialog />
  </div>
</template>
