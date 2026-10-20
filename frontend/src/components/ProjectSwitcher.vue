<script setup lang="ts">
import { computed, onBeforeUnmount, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Check, ChevronsUpDown } from 'lucide-vue-next'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuTrigger } from './ui/dropdown-menu'
import RelayMeshLogo from './icons/RelayMeshLogo.vue'
import ExecMeshLogo from './icons/ExecMeshLogo.vue'
import {
  PROJECTS,
  PROJECT_ORDER,
  badgeNumber,
  modeOfPath,
  otherProject,
  projectAlert,
  switcherLabel,
  type AppMode,
  type ProjectCounts,
} from '../lib/mode'
import { useExecRunsStore } from '../stores/execRuns'
import { useModeStore } from '../stores/mode'
import { useSessionStore } from '../stores/session'
import { useTerminalStore } from '../stores/terminal'

// 侧栏左上角的品牌区同时是「项目切换器」：RelayMesh（会话与交付）｜ ExecMesh（终端与模型执行）。
// 只在终端总闸开启时才能切换（总闸关闭时只剩 RelayMesh，品牌区与以前完全一样）；两个项目各自记住上次停留的位置。
// 另一个项目有待处理事项时，触发器上的图标会亮一个琥珀色圆点，打开菜单能看到具体数字。
const emit = defineEmits<{ (e: 'switched'): void }>()

const route = useRoute()
const router = useRouter()
const mode = useModeStore()
const terminal = useTerminalStore()
const sessions = useSessionStore()
const execRuns = useExecRunsStore()

const current = computed<AppMode>(() => modeOfPath(route.path))
const canSwitch = computed(() => terminal.gateOpen)
const project = computed(() => PROJECTS[current.value])

const counts = computed<ProjectCounts>(() => ({
  relayWaiting: sessions.sessions.filter(s => s.status === 'pending').length,
  execAttention: mode.attention,
  modelRunning: mode.modelRunning,
}))
const alerts = computed(() => ({
  relay: projectAlert('relay', current.value, counts.value),
  exec: projectAlert('exec', current.value, counts.value),
}))
const otherAlert = computed(() => alerts.value[otherProject(current.value)])
const hasOtherAlert = computed(() => otherAlert.value.attention > 0 || otherAlert.value.running)
const label = computed(() => switcherLabel(current.value, counts.value))

function go(id: AppMode) {
  if (id === current.value) return
  void router.push(mode.targetOf(id))
  emit('switched')
}

// RelayMesh 里 ExecMesh 的页面不在场，没有人在轮询主机列表：这里用很低的频率刷新待启用主机数，
// 让提醒能亮起来。ExecMesh 里由终端页自己的轮询负责。只在可切换（总闸开启）时才轮询。
const BADGE_POLL_MS = 30_000
let timer: ReturnType<typeof setInterval> | null = null
let polling = false

function startAlertPolling() {
  if (polling) return
  polling = true
  if (!terminal.hostsLoaded) void terminal.refreshHosts()
  execRuns.startPolling()
  timer = setInterval(() => {
    if (current.value === 'relay' && !document.hidden && terminal.gateOpen) void terminal.refreshHosts()
  }, BADGE_POLL_MS)
}

function stopAlertPolling() {
  if (!polling) return
  polling = false
  if (timer) clearInterval(timer)
  timer = null
  execRuns.stopPolling()
}

watch(canSwitch, can => (can ? startAlertPolling() : stopAlertPolling()), { immediate: true })
onBeforeUnmount(stopAlertPolling)

const logoBase = 'rounded-xs bg-primary text-primary-foreground flex items-center justify-center shadow-2xs shrink-0'
const logoBox = `w-6 h-6 ${logoBase}`
const logoBoxLarge = `w-7 h-7 ${logoBase}`
const pill = 'min-w-4 h-4 px-1 rounded-full bg-amber-500 text-white text-[10px] leading-4 text-center font-medium tabular-nums'
</script>

<template>
  <div class="flex items-center gap-1.5 min-w-0">
    <!-- 可切换：整个品牌区是一个菜单按钮 -->
    <DropdownMenu v-if="canSwitch">
      <DropdownMenuTrigger as-child>
        <button
          type="button"
          class="group -ml-1.5 pl-1.5 pr-1.5 py-1 rounded-sm flex items-center gap-2 min-w-0 text-left cursor-pointer transition-colors hover:bg-muted/80 data-[state=open]:bg-muted/80 focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-primary/50"
          :aria-label="label"
          :title="label"
          data-testid="project-switcher"
        >
          <span class="relative shrink-0">
            <span :class="logoBox">
              <ExecMeshLogo v-if="current === 'exec'" class="w-3.5 h-3.5" />
              <RelayMeshLogo v-else class="w-3.5 h-3.5" />
            </span>
            <span
              v-if="hasOtherAlert"
              class="absolute -top-1 -right-1 w-2.5 h-2.5 rounded-full border-2 border-card"
              :class="otherAlert.attention > 0 ? 'bg-amber-500' : 'bg-emerald-500 animate-pulse'"
              aria-hidden="true"
              data-testid="switcher-alert-dot"
            />
          </span>
          <span class="flex flex-col min-w-0">
            <span class="flex items-center gap-1 min-w-0">
              <span class="font-bold text-sm tracking-tight text-foreground font-mono leading-none truncate">{{ project.name }}</span>
              <ChevronsUpDown class="w-3 h-3 shrink-0 text-muted-foreground/70 group-hover:text-foreground transition-colors" aria-hidden="true" />
            </span>
            <span class="text-[10px] text-muted-foreground font-mono mt-0.5 truncate">{{ project.subtitle }}</span>
          </span>
        </button>
      </DropdownMenuTrigger>

      <DropdownMenuContent align="start" :side-offset="6" class="w-64 p-1.5" data-testid="project-menu">
        <DropdownMenuLabel class="px-2 pb-1.5 text-[11px] font-normal text-muted-foreground">切换项目</DropdownMenuLabel>
        <DropdownMenuItem
          v-for="id in PROJECT_ORDER"
          :key="id"
          class="gap-2.5 py-2 cursor-pointer"
          :data-testid="`project-${id}`"
          :aria-current="current === id ? 'true' : undefined"
          :title="alerts[id].text || undefined"
          @select="go(id)"
        >
          <span :class="logoBoxLarge">
            <ExecMeshLogo v-if="id === 'exec'" class="w-4 h-4" />
            <RelayMeshLogo v-else class="w-4 h-4" />
          </span>
          <span class="flex flex-col min-w-0 flex-1">
            <span class="font-bold text-xs font-mono leading-none">{{ PROJECTS[id].name }}</span>
            <span class="text-[11px] text-muted-foreground leading-snug mt-1">{{ PROJECTS[id].tagline }}</span>
          </span>
          <span class="flex items-center gap-1.5 shrink-0">
            <span v-if="alerts[id].attention > 0" :class="pill" :aria-label="alerts[id].text">{{ badgeNumber(alerts[id].attention) }}</span>
            <span
              v-if="alerts[id].running"
              class="w-1.5 h-1.5 rounded-full bg-emerald-500 animate-pulse"
              role="img"
              :aria-label="`${counts.modelRunning} 个模型正在运行`"
            />
            <Check v-if="current === id" class="!size-4 text-primary" aria-hidden="true" />
          </span>
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>

    <!-- 不可切换（总闸关闭）：只有 RelayMesh，保持原样 -->
    <div v-else class="flex items-center gap-2 min-w-0">
      <span :class="logoBox">
        <ExecMeshLogo v-if="current === 'exec'" class="w-3.5 h-3.5" />
        <RelayMeshLogo v-else class="w-3.5 h-3.5" />
      </span>
      <span class="flex flex-col min-w-0">
        <span class="font-bold text-sm tracking-tight text-foreground font-mono leading-none truncate">{{ project.name }}</span>
        <span class="text-[10px] text-muted-foreground font-mono mt-0.5 truncate">{{ project.subtitle }}</span>
      </span>
    </div>

    <!-- 紧跟在名称后面的附加指示（例如 RelayMesh 的 SSE 连接状态） -->
    <slot name="extra" />
  </div>
</template>
