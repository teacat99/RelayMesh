<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { Archive, Info } from 'lucide-vue-next'
import { useTerminalStore } from '@/stores/terminal'
import ExecRecordLinesPref from './ExecRecordLinesPref.vue'

// 记录与保留：展示服务端当前生效的策略。保留天数与字节配额来自服务端的环境变量，运行期间不能在界面里修改——
// 不做成假的可编辑开关，而是把「现在是什么」和「去哪里改」说清楚。唯一可以在界面里改的是「终端限高（按行数）」，
// 它和这些配额互相独立，由单独的区块（ExecRecordLinesPref）负责，改动需要解锁并写审计。
const terminal = useTerminalStore()
const retention = computed(() => terminal.status?.retention ?? null)

onMounted(() => {
  void terminal.loadStatus()
})

function limitText(n: number, unit: string, fallback: string): string {
  return n > 0 ? `${n} ${unit}` : fallback
}
</script>

<template>
  <div class="space-y-4" data-testid="exec-retention">
    <div class="flex items-center justify-between pb-1.5 border-b border-border/70">
      <div class="flex items-center gap-1.5">
        <Archive class="w-3.5 h-3.5 text-primary" />
        <span class="text-xs font-bold font-mono text-foreground">记录与保留</span>
      </div>
      <span class="text-[10px] font-mono text-muted-foreground">除「终端限高」外只读 · 由服务端配置</span>
    </div>

    <p v-if="!retention" class="text-xs text-muted-foreground" data-testid="retention-unavailable">
      {{ terminal.statusError || '终端功能未开启或尚未加载，暂时无法读取保留策略。' }}
    </p>

    <template v-else>
      <section class="p-3.5 rounded-xs border border-border/70 bg-card/60 space-y-2.5" aria-label="终端记录" data-testid="retention-record">
        <div class="flex items-center gap-2">
          <div class="text-xs font-mono font-medium text-foreground">终端记录（保存在每台主机本机）</div>
          <span
            class="px-1.5 py-0.5 rounded-xs text-[10px] font-mono"
            :class="retention.record_off ? 'bg-amber-500/10 border border-amber-500/30 text-amber-500' : 'bg-primary/10 border border-primary/30 text-primary'"
            data-testid="retention-record-state"
          >{{ retention.record_off ? '服务端已全局关闭' : '按主机配置录制' }}</span>
        </div>
        <dl class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-xs">
          <dt class="text-muted-foreground">保留期</dt>
          <dd class="font-mono" data-testid="retention-days">{{ limitText(retention.record_days, '天（上限）', '不额外限制（主机本机默认 30 天）') }}</dd>
          <dt class="text-muted-foreground">单会话上限</dt>
          <dd class="font-mono" data-testid="retention-session">{{ limitText(retention.record_session_mb, 'MiB', '不额外限制（主机本机默认 64 MiB，滚动覆盖最旧部分）') }}</dd>
          <dt class="text-muted-foreground">单主机总量</dt>
          <dd class="font-mono" data-testid="retention-host">{{ limitText(retention.record_host_mb, 'MiB', '不额外限制（主机本机默认 512 MiB）') }}</dd>
        </dl>
        <p class="text-[10px] leading-relaxed text-muted-foreground">
          服务端的限制只会让主机本机的配置更严格，不会放宽。无痕会话从不录制，也不受这里影响。
        </p>
      </section>

      <ExecRecordLinesPref />

      <section class="p-3.5 rounded-xs border border-border/70 bg-card/60 space-y-2.5" aria-label="清单与模型运行记录" data-testid="retention-others">
        <div class="text-xs font-mono font-medium text-foreground">清单与模型运行记录（保存在服务端）</div>
        <dl class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-xs">
          <dt class="text-muted-foreground">已结束会话的清单</dt>
          <dd class="font-mono" data-testid="retention-index">保留 {{ retention.index_days }} 天</dd>
          <dt class="text-muted-foreground">模型运行记录</dt>
          <dd class="font-mono" data-testid="retention-run">保留 {{ retention.run_days }} 天</dd>
        </dl>
        <p class="text-[10px] leading-relaxed text-muted-foreground">
          审计日志按条数保留（最近 20000 条），不按天清理；超期的运行记录由服务端定时清理，主机上的原始输出仍按主机的保留期处理。
        </p>
      </section>

      <div class="flex items-start gap-2 rounded-xs border border-border/70 bg-muted/40 px-3 py-2.5 text-[11px] leading-relaxed text-muted-foreground">
        <Info class="w-3.5 h-3.5 shrink-0 mt-0.5" />
        <p>
          要调整这些值，修改服务端环境变量
          <code class="font-mono text-foreground">RELAYMESH_TERMINAL_RECORD</code>（<code class="font-mono">off</code> 全局关闭）、
          <code class="font-mono text-foreground">RELAYMESH_TERMINAL_RECORD_DAYS</code>、
          <code class="font-mono text-foreground">RELAYMESH_TERMINAL_RECORD_SESSION_MB</code>、
          <code class="font-mono text-foreground">RELAYMESH_TERMINAL_RECORD_HOST_MB</code> 后重启服务；
          主机本机的保留期与配额用 Agent 的 <code class="font-mono text-foreground">--record-retention-days</code>、<code class="font-mono text-foreground">--record-session-mb</code>、<code class="font-mono text-foreground">--record-host-mb</code> 参数设置。
        </p>
      </div>
    </template>
  </div>
</template>
