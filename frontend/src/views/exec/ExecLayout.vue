<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useMediaQuery } from '@vueuse/core'
import { Loader2, ShieldAlert } from 'lucide-vue-next'
import Button from '../../components/ui/button/Button.vue'
import ExecSidebar from '../../components/exec/ExecSidebar.vue'
import DiscoveryDialog from '../../components/terminal/DiscoveryDialog.vue'
import AuditSheet from '../../components/terminal/AuditSheet.vue'
import { useExecStore } from '../../stores/exec'
import { useExecRunsStore } from '../../stores/execRuns'
import { useTerminalStore } from '../../stores/terminal'
import { gateReasonInfo } from '../../lib/terminal/format'
import { RELAY_HOME } from '../../lib/mode'
import { useRouter } from 'vue-router'

// ExecMesh 的应用壳：左侧栏（会话清单）+ 右侧页面（终端 / 主机 / 模型线程）。
// 总闸关闭时 ExecMesh 不可用：给出原因与处理办法，并提供回到 RelayMesh 的出口。
const router = useRouter()
const terminal = useTerminalStore()
const exec = useExecStore()
const execRuns = useExecRunsStore()

const gateChecked = computed(() => terminal.status !== null)
const gateInfo = computed(() => gateReasonInfo(terminal.gateReason))

// 软键盘弹出时，布局视口不一定收缩；按可视视口高度排版，终端的辅助条才不会被键盘盖住。
const isTouch = useMediaQuery('(pointer: coarse)')
const viewportHeight = ref<number | null>(null)
function syncViewport() {
  const vv = window.visualViewport
  viewportHeight.value = vv && isTouch.value ? Math.round(vv.height) : null
  if (viewportHeight.value !== null) window.scrollTo(0, 0)
}

onMounted(() => {
  terminal.startPolling()
  execRuns.start()
  exec.startClock()
  exec.ensureHistory()
  window.visualViewport?.addEventListener('resize', syncViewport)
  syncViewport()
})

onBeforeUnmount(() => {
  terminal.stopPolling()
  execRuns.stop()
  exec.stopClock()
  exec.mobileSidebarOpen = false
  window.visualViewport?.removeEventListener('resize', syncViewport)
})

// 模型运行的事件流只在解锁时才下发实时输出：解锁状态变化、总闸重新打开后重连，让服务端按新的状态决定。
watch(
  () => terminal.isUnlocked,
  () => execRuns.restartStream(),
)
watch(
  () => terminal.gateOpen,
  open => {
    if (open) execRuns.restartStream()
  },
)

// 主机列表变化（新接入 / 启用）时，把新主机的历史索引也读进来。
watch(
  () => terminal.hosts.map(h => `${h.machine_code}:${h.status}`).join(','),
  () => exec.ensureHistory(),
)
</script>

<template>
  <div
    class="w-full bg-background text-foreground flex overflow-hidden"
    :class="viewportHeight === null ? 'h-[100dvh]' : ''"
    :style="viewportHeight !== null ? { height: viewportHeight + 'px' } : undefined"
  >
    <main v-if="!gateChecked" class="flex-1 min-h-0 flex items-center justify-center text-xs text-muted-foreground gap-2" aria-busy="true">
      <template v-if="terminal.statusError">
        <span class="text-destructive">{{ terminal.statusError }}</span>
        <Button variant="outline" size="sm" class="h-7 rounded-sm text-xs" @click="terminal.loadStatus()">重试</Button>
      </template>
      <template v-else><Loader2 class="w-3.5 h-3.5 animate-spin" />正在读取终端状态…</template>
    </main>

    <main v-else-if="!terminal.gateOpen" class="flex-1 min-h-0 overflow-y-auto">
      <div class="max-w-xl mx-auto px-5 py-12 space-y-4">
        <div class="flex items-center gap-2 text-foreground">
          <ShieldAlert class="w-5 h-5 text-amber-500 shrink-0" />
          <h1 class="text-sm font-bold">{{ gateInfo.title }}</h1>
        </div>
        <p class="text-xs leading-relaxed text-muted-foreground">{{ gateInfo.body }}</p>
        <div class="flex gap-2">
          <Button variant="outline" size="sm" class="h-7 rounded-sm text-xs" @click="terminal.loadStatus()">重新检查</Button>
          <Button variant="ghost" size="sm" class="h-7 rounded-sm text-xs" @click="router.push(RELAY_HOME)">回到 RelayMesh</Button>
        </div>
      </div>
    </main>

    <template v-else>
      <ExecSidebar />
      <main class="flex-1 min-w-0 min-h-0 h-full flex flex-col bg-background overflow-hidden relative">
        <router-view />
      </main>
    </template>

    <DiscoveryDialog v-model:open="exec.discoveryOpen" />
    <AuditSheet v-model:open="exec.auditOpen" />
  </div>
</template>
