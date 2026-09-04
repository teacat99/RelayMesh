<script setup lang="ts">
import { useRouter } from 'vue-router'
import { useSettingsStore } from '../stores/settings'
import SettingsForm from '../components/SettingsForm.vue'
import Button from '../components/ui/button/Button.vue'
import { Settings, ChevronLeft, Home, Loader2, CheckCircle2 } from 'lucide-vue-next'

const router = useRouter()
const settingsStore = useSettingsStore()

function handleBack() {
  if (window.history.length > 1) {
    router.back()
  } else {
    router.push('/')
  }
}
</script>

<template>
  <div class="min-h-full min-h-[100dvh] w-full bg-background text-foreground flex flex-col">
    <!-- Top Header Bar (紧凑适宜高度 h-11，文字靠左) -->
    <header class="h-11 px-3 sm:px-5 border-b border-border/70 flex items-center justify-between bg-card/80 backdrop-blur-md sticky top-0 z-40 shrink-0">
      <div class="flex items-center gap-2">
        <button
          type="button"
          class="p-1 -ml-1 rounded-sm text-muted-foreground hover:text-foreground hover:bg-muted transition-colors flex items-center gap-1 text-xs font-mono cursor-pointer"
          @click="handleBack"
        >
          <ChevronLeft class="w-3.5 h-3.5" />
          <span>返回</span>
        </button>
        <span class="text-border">/</span>
        <div class="flex items-center gap-1.5 font-bold text-xs sm:text-sm font-mono text-foreground">
          <Settings class="w-3.5 h-3.5 text-primary" />
          <span>系统偏好与设置</span>
        </div>
      </div>

      <div class="flex items-center gap-2">
        <!-- 自动保存动态提示（1秒后自动消失） -->
        <div
          v-if="settingsStore.saveStatus !== 'idle'"
          class="flex items-center gap-1.5 text-[11px] font-mono text-primary transition-all animate-in fade-in duration-150"
        >
          <Loader2 v-if="settingsStore.saveStatus === 'saving'" class="w-3 h-3 animate-spin text-primary shrink-0" />
          <CheckCircle2 v-else class="w-3 h-3 text-primary shrink-0" />
          <span class="hidden sm:inline">{{ settingsStore.saveStatus === 'saving' ? '正在保存...' : '已自动保存' }}</span>
        </div>

        <Button
          variant="outline"
          size="sm"
          class="h-7 text-xs font-mono rounded-sm flex items-center gap-1.5 px-2 cursor-pointer"
          @click="router.push('/')"
        >
          <Home class="w-3 h-3" />
          <span class="hidden sm:inline">工作区主页</span>
        </Button>
      </div>
    </header>

    <!-- Main Content Container (移动端去除内部多余容器边框，自适应全屏流式空间) -->
    <main class="flex-1 max-w-4xl w-full mx-auto p-3 sm:p-5 flex flex-col overflow-y-auto">
      <div class="flex-1 flex flex-col border-0 sm:border border-border/80 rounded-none sm:rounded-sm p-0 sm:p-5 bg-transparent sm:bg-card shadow-none sm:shadow-xs">
        <SettingsForm />
      </div>
    </main>
  </div>
</template>
