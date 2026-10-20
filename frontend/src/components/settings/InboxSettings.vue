<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { toast } from 'vue-sonner'
import { AlertTriangle, MessageSquareDashed } from 'lucide-vue-next'
import { useSessionStore } from '@/stores/session'
import type { InboxConfig } from '@/api/types'
import { layersSummary } from '@/lib/inbox'

// 追加指令收件箱的手动开关：总开关 + 三层发现机制各自的开关。每个开关点一下就保存，立即对所有页面与新的 MCP 调用生效
// （提示词条款例外：它写在 MCP 会话建立时下发的指引里，对新建立的连接生效）。

const sessionStore = useSessionStore()
const busy = ref<keyof InboxConfig | null>(null)
const loadFailed = ref(false)

const cfg = computed(() => sessionStore.inboxConfig)
const summary = computed(() => layersSummary(cfg.value))

type LayerKey = Exclude<keyof InboxConfig, 'enabled'>
const LAYERS: ReadonlyArray<{ key: LayerKey; title: string; desc: string }> = [
  {
    key: 'receipt_hint',
    title: '回执提示行',
    desc: '智能体每次调用 RelayMesh 工具，只要收件箱里有它没读的追加，回执末尾就多一行 inbox: 提示。不需要在项目里安装任何东西。',
  },
  {
    key: 'hooks',
    title: '编辑器 hook',
    desc: '在 Cursor 的 hook 里按节奏提醒（立即、5 分钟后、15 分钟后，每条最多 3 次），并在智能体准备收尾时拦一次。需要在项目里装一个 hook 脚本：会话页收件箱条带里点「怎么装？」能复制现成的安装命令，也可以看文档 docs/inbox.md；这里关闭后 hook 即刻安静。',
  },
  {
    key: 'prompt_clause',
    title: '提示词条款',
    desc: '在 MCP 指引里告诉智能体收件箱怎么用。指引只在 MCP 会话建立时下发，所以对新建立的连接生效。',
  },
  {
    key: 'hook_install_hint',
    title: '未装 hook 时的回执提示',
    desc: '工作流还没收到编辑器 hook 查询时，在工具回执末尾偶尔提示 hook 的作用与一行安装命令（不含令牌，约 6 小时一次）。',
  },
]

async function load() {
  loadFailed.value = false
  const res = await sessionStore.loadInboxConfig()
  if (!res) loadFailed.value = true
}

async function toggle(key: keyof InboxConfig) {
  const cur = cfg.value
  if (!cur || busy.value) return
  busy.value = key
  try {
    await sessionStore.updateInboxConfig({ [key]: !cur[key] })
  } catch (err: any) {
    toast.error('保存失败：' + (err?.response?.data?.error || err?.message || err))
  } finally {
    busy.value = null
  }
}

onMounted(load)
</script>

<template>
  <div class="p-3 rounded-xs border border-border/70 bg-card/60 space-y-3" data-testid="inbox-settings">
    <div class="flex items-center gap-1.5 text-xs font-mono font-medium text-foreground">
      <MessageSquareDashed class="w-3.5 h-3.5 text-primary" />
      <span>追加指令收件箱 · 提醒智能体</span>
    </div>
    <p class="text-[10px] text-muted-foreground leading-relaxed">
      智能体工作期间（没有在等你反馈时）你追加的指令不会丢，会进入它的收件箱；你在会话里能看到每条的状态：已排队 ✓、已读取 ✓✓、已确认 ✓✓✓。
      下面这些方式让智能体「发现」新指令；全部关闭后，追加只会在它下次发起交互时被一并带走。
    </p>

    <div v-if="loadFailed" class="text-[11px] text-destructive flex items-center gap-1.5">
      <AlertTriangle class="w-3.5 h-3.5" />
      <span>读取失败</span>
      <button type="button" class="underline cursor-pointer" @click="load">重试</button>
    </div>

    <template v-else-if="cfg">
      <div
        v-if="cfg.env_off"
        class="p-2 rounded-xs border border-amber-500/40 bg-amber-500/10 text-[11px] text-amber-700 dark:text-amber-400 flex items-start gap-1.5"
        data-testid="inbox-env-off"
      >
        <AlertTriangle class="w-3.5 h-3.5 shrink-0 mt-px" />
        <span>服务端环境变量 <code class="font-mono">RELAYMESH_INBOX=off</code> 强制关闭了收件箱提醒，下面的开关暂时不起作用；要恢复请去掉这个环境变量并重启。</span>
      </div>

      <!-- 总开关 -->
      <button
        type="button"
        role="switch"
        :aria-checked="cfg.enabled"
        :disabled="busy !== null"
        data-testid="inbox-enabled"
        class="w-full p-3 rounded-xs border text-left transition-all font-mono cursor-pointer flex items-center justify-between gap-3 disabled:opacity-60"
        :class="cfg.enabled ? 'border-primary/40 bg-primary/10 text-foreground' : 'border-border/70 bg-card/60 text-muted-foreground'"
        @click="toggle('enabled')"
      >
        <div class="space-y-0.5">
          <div class="text-xs font-semibold">主动提醒智能体有新追加（总开关）</div>
          <div class="text-[10px] text-muted-foreground">关闭 = 回到原来的被动模式；已排队的追加一条都不会丢，智能体也始终可以主动读取收件箱</div>
        </div>
        <div class="w-8 h-4.5 rounded-full transition-colors flex items-center p-0.5 shrink-0" :class="cfg.enabled ? 'bg-primary' : 'bg-muted-foreground/30'">
          <div class="w-3.5 h-3.5 rounded-full bg-white transition-transform" :class="cfg.enabled ? 'translate-x-3.5' : 'translate-x-0'"></div>
        </div>
      </button>

      <!-- 三层 -->
      <div class="space-y-2" :class="cfg.enabled && !cfg.env_off ? '' : 'opacity-60'">
        <button
          v-for="layer in LAYERS"
          :key="layer.key"
          type="button"
          role="switch"
          :aria-checked="cfg[layer.key]"
          :disabled="busy !== null"
          :data-testid="`inbox-layer-${layer.key}`"
          class="w-full p-2.5 rounded-xs border text-left transition-all font-mono cursor-pointer flex items-center justify-between gap-3 disabled:opacity-60"
          :class="cfg[layer.key] ? 'border-primary/40 bg-primary/10 text-foreground' : 'border-border/70 bg-background/70 text-muted-foreground'"
          @click="toggle(layer.key)"
        >
          <div class="space-y-0.5">
            <div class="text-xs font-semibold">{{ layer.title }}</div>
            <div class="text-[10px] text-muted-foreground leading-relaxed">{{ layer.desc }}</div>
          </div>
          <div class="w-8 h-4.5 rounded-full transition-colors flex items-center p-0.5 shrink-0" :class="cfg[layer.key] ? 'bg-primary' : 'bg-muted-foreground/30'">
            <div class="w-3.5 h-3.5 rounded-full bg-white transition-transform" :class="cfg[layer.key] ? 'translate-x-3.5' : 'translate-x-0'"></div>
          </div>
        </button>
      </div>

      <p class="text-[10px] font-mono text-muted-foreground" data-testid="inbox-settings-summary" :data-mode="summary.mode">当前生效：{{ summary.text }}</p>
    </template>

    <div v-else class="text-[11px] text-muted-foreground">读取中…</div>
  </div>
</template>
