<script setup lang="ts">
import { PanelLeft } from 'lucide-vue-next'
import { useExecStore } from '../../stores/exec'

// ExecMesh 各页面共用的顶栏：手机上带「打开侧栏」，桌面侧栏被折叠时带「展开侧栏」。
const exec = useExecStore()
</script>

<template>
  <header class="h-12 md:h-14 px-2.5 sm:px-4 border-b border-border/70 flex items-center gap-2 bg-card/80 backdrop-blur-md shrink-0">
    <button
      type="button"
      class="md:hidden p-1.5 -ml-1 rounded-sm text-muted-foreground hover:text-foreground hover:bg-muted transition-colors cursor-pointer"
      aria-label="打开侧栏"
      title="打开侧栏"
      @click="exec.mobileSidebarOpen = true"
    >
      <PanelLeft class="w-4 h-4" />
    </button>
    <button
      v-if="exec.sidebarCollapsed"
      type="button"
      class="hidden md:flex p-1.5 -ml-1 rounded-sm text-muted-foreground hover:text-foreground hover:bg-muted transition-colors cursor-pointer"
      aria-label="展开侧栏"
      title="展开侧栏"
      @click="exec.setSidebarCollapsed(false)"
    >
      <PanelLeft class="w-4 h-4" />
    </button>
    <div class="flex-1 min-w-0 flex items-center gap-2"><slot /></div>
    <div class="flex items-center gap-1 shrink-0"><slot name="actions" /></div>
  </header>
</template>
