<script setup lang="ts">
import { nextTick, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useSettingsStore } from '../stores/settings'
import { useSettingsUiStore } from '../stores/settingsUi'
import { useModeStore } from '../stores/mode'
import { modeOfPath, type AppMode } from '../lib/mode'
import { settingsTitle } from '../lib/settingsRegistry'
import SettingsForm from './SettingsForm.vue'
import Dialog from './ui/dialog/Dialog.vue'
import DialogContent from './ui/dialog/DialogContent.vue'
import DialogTitle from './ui/dialog/DialogTitle.vue'
import { Settings, CheckCircle2, Loader2 } from 'lucide-vue-next'

// 设置是「当前模块的设置」：打开时按背景页面所在的模块决定内容；
// 窗口里的模块分段切换会把背景页面切到另一个模块，同时窗口保持打开并切到那个模块的设置。
const settingsStore = useSettingsStore()
const ui = useSettingsUiStore()
const mode = useModeStore()
const route = useRoute()
const router = useRouter()

watch(
  () => settingsStore.isSettingsOpen,
  open => {
    if (open) ui.enter(modeOfPath(route.path))
  },
  { immediate: true }
)

function jump(next: AppMode) {
  // 先切背景再切设置内容：背景切换是路由跳转，设置窗口挂在全局布局上，不会因此关闭。
  void router.push(mode.targetOf(next))
  ui.enter(next)
}

// 对话框默认把焦点给窗口里第一个能聚焦的元素（这里会是模块切换按钮），键盘用户得再按好几次 Tab 才到分类。
// 改成落在当前分类上：上下键直接换分类；窄屏没有常驻的分类标签，就落在「当前分类」选择条上。
function focusCurrentCategory(e: Event) {
  e.preventDefault()
  void nextTick(() => {
    document
      .querySelector<HTMLElement>('[data-testid=settings-shell] [role=tab][aria-selected=true], [data-testid=settings-picker-toggle]')
      ?.focus({ preventScroll: true })
  })
}
</script>

<template>
  <Dialog :open="settingsStore.isSettingsOpen" @update:open="(v) => (v ? settingsStore.openSettings() : settingsStore.closeSettings())">
    <!-- 标题栏右侧已经给关闭按钮留了位置（pr-12）；showClose 是布尔属性，不写就是 false，必须显式打开。
         宽度：左栏占 13–14rem，窗口放宽到 80rem（1280px，原来最宽 64rem），右侧内容区不比原来的单栏布局窄。 -->
    <DialogContent
      :show-close="true"
      class="sm:max-w-[80rem] w-[96vw] h-[86vh] max-h-[56rem] flex flex-col p-0 gap-0 overflow-hidden shadow-2xl rounded-md border-border/80 bg-card"
      @open-auto-focus="focusCurrentCategory"
    >
      <div class="px-5 h-11 border-b border-border/70 flex flex-row items-center justify-between shrink-0 bg-card/90 backdrop-blur-xs pr-12">
        <DialogTitle class="flex items-center gap-2 text-xs sm:text-sm font-bold font-mono tracking-tight text-foreground text-left" data-testid="settings-title">
          <div class="p-1 rounded-xs bg-primary/10 text-primary border border-primary/20 shrink-0">
            <Settings class="w-3.5 h-3.5" />
          </div>
          <span>{{ settingsTitle(ui.module) }}</span>
        </DialogTitle>

        <!-- 右侧：自动保存提示（出现 / 消失时只推动它自己，不影响左边的标题） -->
        <div class="flex items-center gap-3 min-w-0">
          <!-- 自动保存动态加载与完成提示（1秒后自动消失） -->
          <div
            v-if="settingsStore.saveStatus !== 'idle'"
            class="flex items-center gap-1.5 text-[11px] font-mono text-primary transition-all animate-in fade-in zoom-in-95 duration-150"
            data-testid="settings-save-status"
          >
            <Loader2 v-if="settingsStore.saveStatus === 'saving'" class="w-3.5 h-3.5 animate-spin text-primary shrink-0" />
            <CheckCircle2 v-else class="w-3.5 h-3.5 text-primary shrink-0" />
            <span class="hidden sm:inline">{{ settingsStore.saveStatus === 'saving' ? '正在保存...' : '已自动保存' }}</span>
          </div>
        </div>
      </div>

      <!-- 框架自己撑满这块有确定高度的区域，只有右侧内容区滚动 -->
      <div class="flex-1 min-h-0">
        <SettingsForm @jump="jump" />
      </div>
    </DialogContent>
  </Dialog>
</template>
