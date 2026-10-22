<script setup lang="ts">
import { watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useSettingsStore } from '../stores/settings'
import { useSettingsUiStore } from '../stores/settingsUi'
import { EXEC_HOME, RELAY_HOME, type AppMode } from '../lib/mode'
import { settingsTitle } from '../lib/settingsRegistry'
import SettingsForm from '../components/SettingsForm.vue'
import Button from '../components/ui/button/Button.vue'
import { Settings, ChevronLeft, Home, Loader2, CheckCircle2 } from 'lucide-vue-next'

const route = useRoute()
const router = useRouter()
const settingsStore = useSettingsStore()
const ui = useSettingsUiStore()

// 这是移动端的设置页：看的是哪个模块的设置由地址里的 ?module=exec|relay 决定（缺省 RelayMesh）。
// 内容区里的模块分段切换只是换一个 module 参数；背景没有第二个页面，所以不需要切路由。
watch(
  () => route.query.module,
  m => ui.enter(m === 'exec' ? 'exec' : 'relay'),
  { immediate: true }
)

function jump(next: AppMode) {
  void router.replace({ path: '/settings', query: { module: next } })
}

function handleBack() {
  if (window.history.length > 1) {
    router.back()
  } else {
    router.push('/')
  }
}
</script>

<template>
  <!-- 整页撑满视口：顶栏固定高度，设置框架占满剩下的高度并自己管理滚动（窄屏时选择条始终在内容上方） -->
  <div class="h-[100dvh] w-full bg-background text-foreground flex flex-col overflow-hidden">
    <!-- Top Header Bar (紧凑适宜高度 h-11，文字靠左) -->
    <header class="h-11 px-3 sm:px-5 border-b border-border/70 flex items-center justify-between bg-card/80 backdrop-blur-md z-40 shrink-0">
      <div class="flex items-center gap-2 min-w-0">
        <button
          type="button"
          class="p-1 -ml-1 rounded-sm text-muted-foreground hover:text-foreground hover:bg-muted transition-colors flex items-center gap-1 text-xs font-mono cursor-pointer shrink-0"
          @click="handleBack"
        >
          <ChevronLeft class="w-3.5 h-3.5" />
          <span>返回</span>
        </button>
        <span class="text-border">/</span>
        <div class="flex items-center gap-1.5 font-bold text-xs sm:text-sm font-mono text-foreground min-w-0">
          <Settings class="w-3.5 h-3.5 text-primary shrink-0" />
          <span class="truncate" data-testid="settings-title">{{ settingsTitle(ui.module) }}</span>
        </div>
      </div>

      <div class="flex items-center gap-2 shrink-0">
        <!-- 自动保存动态提示（1秒后自动消失） -->
        <div
          v-if="settingsStore.saveStatus !== 'idle'"
          class="flex items-center gap-1.5 text-[11px] font-mono text-primary transition-all animate-in fade-in duration-150"
          data-testid="settings-save-status"
        >
          <Loader2 v-if="settingsStore.saveStatus === 'saving'" class="w-3 h-3 animate-spin text-primary shrink-0" />
          <CheckCircle2 v-else class="w-3 h-3 text-primary shrink-0" />
          <span class="hidden sm:inline">{{ settingsStore.saveStatus === 'saving' ? '正在保存...' : '已自动保存' }}</span>
        </div>

        <Button
          variant="outline"
          size="sm"
          class="h-7 text-xs font-mono rounded-sm flex items-center gap-1.5 px-2 cursor-pointer"
          @click="router.push(ui.module === 'exec' ? EXEC_HOME : RELAY_HOME)"
        >
          <Home class="w-3 h-3" />
          <span class="hidden sm:inline">工作区主页</span>
        </Button>
      </div>
    </header>

    <!-- 移动端去掉外框与内边距，贴边铺满；较宽的屏幕（平板 / 直接打开 /settings）才显示外框 -->
    <main class="flex-1 min-h-0 w-full max-w-6xl mx-auto sm:p-5 flex flex-col">
      <div class="flex-1 min-h-0 overflow-hidden border-0 sm:border border-border/80 rounded-none sm:rounded-sm bg-transparent sm:bg-card shadow-none sm:shadow-xs">
        <SettingsForm @jump="jump" />
      </div>
    </main>
  </div>
</template>
