<script setup lang="ts">
import { computed } from 'vue'
import { useSettingsUiStore } from '../stores/settingsUi'
import { moduleSegments } from '../lib/settingsNav'
import type { AppMode } from '../lib/mode'

// 设置窗口里的「RelayMesh | ExecMesh」分段切换：并列两段，正在看的那段高亮，另一段是跳转按钮。
// 宽屏放在左栏顶部，窄屏放在选择条上方，两处都不占用分类导航自己的位置。
// 真正切换背景页面由宿主（桌面弹窗 / 移动端页面）负责，这里只发出 jump 事件。
const emit = defineEmits<{ jump: [next: AppMode] }>()
const ui = useSettingsUiStore()

const segments = computed(() => moduleSegments(ui.module))
</script>

<template>
  <div
    class="grid grid-cols-2 gap-0.5 rounded-sm border border-border/70 bg-muted/60 p-0.5 text-xs font-mono"
    role="group"
    aria-label="设置所属的模块"
    data-testid="settings-module-switch"
  >
    <template v-for="s in segments" :key="s.id">
      <span
        v-if="s.current"
        class="flex min-w-0 select-none items-center justify-center rounded-xs bg-card px-2.5 py-1.5 font-bold text-foreground shadow-xs ring-1 ring-border/70"
        aria-current="true"
        :title="s.title"
        data-testid="settings-module-current"
      >
        <span class="truncate">{{ s.name }}</span>
      </span>
      <button
        v-else
        type="button"
        class="flex min-w-0 cursor-pointer items-center justify-center rounded-xs px-2.5 py-1.5 text-muted-foreground transition-colors hover:bg-card/70 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        :title="s.title"
        :aria-label="s.title"
        data-testid="settings-jump"
        @click="emit('jump', s.id)"
      >
        <span class="truncate">{{ s.name }}</span>
      </button>
    </template>
  </div>
</template>
