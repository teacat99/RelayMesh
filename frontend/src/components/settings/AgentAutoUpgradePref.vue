<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { toast } from 'vue-sonner'
import { useTerminalStore } from '@/stores/terminal'
import { UnlockCancelled, toApiError } from '@/api/terminal'
import { AUTO_UPGRADE_ENABLE_COMMAND, AUTO_UPGRADE_SILENT_COMMAND, autoUpgradeCoverage, autoUpgradeCoverageText } from '@/lib/terminal/autoUpgrade'
import { Switch } from '../ui/switch'

// Agent 自动升级总闸（服务端设置，所有主机共用，默认关）。
// 它只决定服务端肯不肯回答主机上的 Agent「有没有新版本」、肯不肯批准静默模式主机的升级请求——真正要不要检查、要不要升级，
// 由主机所有者在那台主机上自己决定，服务端无法替主机打开。改动需要刚刚重新输入过密码，并写入审计（它决定主机会不会向服务端打听新版本）。
const terminal = useTerminalStore()
// undefined = 这台服务端还不认识自动升级（旧版本）。
const switchValue = computed(() => terminal.agentAutoUpgrade)
const supported = computed(() => switchValue.value !== undefined)
const enabled = computed(() => switchValue.value === true)
const coverage = computed(() => autoUpgradeCoverageText(autoUpgradeCoverage(terminal.hosts), switchValue.value))

const saving = ref(false)
const error = ref('')

onMounted(() => {
  // 统计在线主机里有多少支持 / 已开启，需要主机清单（进入这个设置页之前可能还没加载过）。
  if (!terminal.hostsLoaded) void terminal.refreshHosts()
})

async function onToggle(on: boolean) {
  saving.value = true
  error.value = ''
  try {
    await terminal.saveAgentAutoUpgrade(on)
    toast.success(on ? '总闸已打开：在本机开启了自动升级的主机会开始向服务端检查新版本' : '总闸已关闭：服务端不再回答主机的检查', { duration: 2800 })
  } catch (e) {
    if (e instanceof UnlockCancelled) return
    error.value = toApiError(e).message
    toast.error(`保存失败：${error.value}`, { duration: 4000 })
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <section class="p-3.5 rounded-xs border border-border/70 bg-card/60 space-y-2.5" aria-label="Agent 自动升级" data-testid="pref-auto-upgrade">
    <label class="flex items-center justify-between gap-3 cursor-pointer">
      <span class="flex items-center gap-2 min-w-0 flex-wrap">
        <span class="text-xs font-mono font-medium text-foreground">Agent 自动升级</span>
        <span class="px-1.5 py-0.5 rounded-xs text-[10px] font-mono bg-primary/10 border border-primary/30 text-primary">所有主机 · 服务端总闸</span>
        <span
          class="px-1.5 py-0.5 rounded-xs text-[10px] font-mono border"
          :class="enabled ? 'bg-amber-500/10 border-amber-500/30 text-amber-500' : 'bg-muted/60 border-border/70 text-muted-foreground'"
          data-testid="pref-auto-upgrade-state"
        >{{ enabled ? '总闸已打开' : '总闸关闭' }}</span>
      </span>
      <Switch
        :checked="enabled"
        :disabled="!supported || saving"
        aria-label="Agent 自动升级总闸"
        data-testid="pref-auto-upgrade-switch"
        @update:checked="onToggle"
      />
    </label>

    <p v-if="!supported" class="text-[10px] leading-relaxed text-muted-foreground" data-testid="pref-auto-upgrade-unavailable">
      终端功能未开启，或服务端版本较旧、还不支持这项设置。
    </p>

    <template v-else>
      <p class="text-[10px] leading-relaxed text-muted-foreground">
        这是服务端的总闸（默认关）：关着时，服务端不回答主机上 Agent 的「有没有新版本」，也不批准任何升级请求，主机即使开启了自动升级也不会有任何检查结果。
        打开总闸本身不会改动任何主机：要不要检查、是否升级，由主机所有者在那台主机上自己决定（只有选了静默模式的主机才会自己升级），服务端无法替主机打开。
      </p>
      <p class="text-[10px] leading-relaxed text-muted-foreground" data-testid="pref-auto-upgrade-coverage">{{ coverage }}</p>
      <p v-if="error" class="text-[10px] text-destructive" role="alert" data-testid="pref-auto-upgrade-error">{{ error }}</p>
      <p class="text-[10px] leading-relaxed text-muted-foreground" data-testid="pref-auto-upgrade-notify">
        提示模式：主机上执行 <code class="font-mono">{{ AUTO_UPGRADE_ENABLE_COMMAND }}</code> 开启后，Agent 在被批准、连上之后检查一次，之后约每 6 小时一次，
        只把结果记在主机上并在主机卡片上提示，<span class="text-foreground">不会下载、也不会升级</span>；主机上的 <code class="font-mono">relaymesh-agent auto-upgrade disable</code> 随时关闭。
      </p>
      <p class="text-[10px] leading-relaxed text-muted-foreground" data-testid="pref-auto-upgrade-silent">
        静默模式：主机上执行 <code class="font-mono">{{ AUTO_UPGRADE_SILENT_COMMAND }}</code>（可加 <code class="font-mono">--window 03:00-05:00</code> 限定开始升级的时间段）。
        服务端有更高版本时，Agent 自己发起升级：<span class="text-foreground">只升级、不降级</span>；下载后先验证 SHA-256 与发布签名，再试运行新程序、交给独立的升级进程替换并重启，新版本连不上服务端会自动还原。
        <span class="text-foreground">没有发布签名的版本一律不安装</span>（公开流水线构建的版本目前没有发布签名，可改用提示模式）；主机上有运行中的会话、又没有独立的会话守护进程时（升级会结束它们）也不会升级；同一个版本最多自动尝试 2 次。
        主机要信任签名公钥：官方发布的 Agent 已内置发布公钥（主钥 + 备用钥），不需要额外操作；自行编译的版本没有内置公钥，要用 <code class="font-mono">--trust-key</code> 钉住发布流程所用的公钥，一把都没有时主机会拒绝开启静默模式。
        也可以在「接入主机」生成安装命令时选择自动升级，装好的同时一并开启。
      </p>
      <p class="text-[10px] leading-relaxed text-muted-foreground">只有 Linux 上以系统服务方式运行的 Agent 支持这两种模式。</p>
    </template>
  </section>
</template>
