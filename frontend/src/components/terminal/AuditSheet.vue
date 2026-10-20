<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import dayjs from 'dayjs'
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '../ui/sheet'
import Button from '../ui/button/Button.vue'
import { Loader2, RefreshCw, ScrollText } from 'lucide-vue-next'
import { useTerminalStore } from '../../stores/terminal'
import { toApiError, type AuditLog } from '../../api/terminal'
import { auditEventLabel, resultLabel, shortCode } from '../../lib/terminal/format'

// 终端操作审计：谁在什么时候解锁、启用了哪台主机、连接了哪个会话。只读，服务端只保留摘要信息（不含密码 / 票据 / 终端内容）。
const open = defineModel<boolean>('open', { required: true })
const store = useTerminalStore()

const PAGE = 100
const logs = ref<AuditLog[]>([])
const loading = ref(false)
const error = ref('')
const exhausted = ref(false)

async function load(reset: boolean) {
  if (loading.value) return
  loading.value = true
  error.value = ''
  try {
    const before = reset ? undefined : logs.value[logs.value.length - 1]?.id
    const page = await store.loadAudit(before, PAGE)
    logs.value = reset ? page : [...logs.value, ...page]
    exhausted.value = page.length < PAGE
  } catch (e) {
    error.value = toApiError(e).message
  } finally {
    loading.value = false
  }
}

watch(open, v => {
  if (v) void load(true)
})

const empty = computed(() => !loading.value && !error.value && logs.value.length === 0)

function tone(result: string): string {
  switch (resultLabel(result).tone) {
    case 'bad':
      return 'text-destructive'
    case 'warn':
      return 'text-amber-600 dark:text-amber-400'
    case 'ok':
      return 'text-emerald-600 dark:text-emerald-400'
    default:
      return 'text-muted-foreground'
  }
}
</script>

<template>
  <Sheet v-model:open="open">
    <SheetContent class="p-0 gap-0 max-w-xl" side="right">
      <SheetHeader class="px-5 py-4 border-b border-border/80 shrink-0 text-left space-y-1 pr-12">
        <SheetTitle class="text-sm font-semibold flex items-center gap-2">
          <ScrollText class="w-4 h-4 text-muted-foreground" />
          <span>终端审计日志</span>
        </SheetTitle>
        <SheetDescription class="text-xs leading-relaxed text-muted-foreground">
          解锁、主机启停、会话创建与连接的记录。不包含密码、票据或终端里的任何内容。
        </SheetDescription>
      </SheetHeader>

      <div class="flex-1 min-h-0 overflow-y-auto">
        <div v-if="error" class="m-4 rounded-sm border border-destructive/30 bg-destructive/5 px-3 py-2 text-xs text-destructive flex items-center justify-between gap-2" role="alert">
          <span>{{ error }}</span>
          <Button variant="outline" size="sm" class="h-6 px-2 text-[11px] rounded-sm" @click="load(true)">重试</Button>
        </div>

        <div v-if="loading && logs.length === 0" class="p-6 flex items-center justify-center gap-2 text-xs text-muted-foreground">
          <Loader2 class="w-3.5 h-3.5 animate-spin" />正在读取…
        </div>
        <p v-else-if="empty" class="p-6 text-xs text-muted-foreground">还没有任何记录。</p>

        <ol v-else class="divide-y divide-border/60">
          <li v-for="l in logs" :key="l.id" class="px-5 py-2.5 text-xs grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-0.5">
            <time class="font-mono text-[11px] text-muted-foreground tabular-nums pt-px" :datetime="l.created_at">{{ dayjs(l.created_at).format('MM-DD HH:mm:ss') }}</time>
            <div class="min-w-0">
              <p class="flex flex-wrap items-baseline gap-x-2">
                <span class="font-medium text-foreground">{{ auditEventLabel(l.event) }}</span>
                <span v-if="resultLabel(l.result).text" :class="['text-[11px]', tone(l.result)]">{{ resultLabel(l.result).text }}</span>
              </p>
              <p class="text-[11px] text-muted-foreground font-mono break-all leading-relaxed">
                <template v-if="l.actor">{{ l.actor }}</template>
                <template v-if="l.ip"><template v-if="l.actor"> · </template>{{ l.ip }}</template>
                <template v-if="l.machine_code"> · {{ shortCode(l.machine_code) }}</template>
                <template v-if="l.session_id"> · 会话 {{ l.session_id.slice(0, 6) }}</template>
              </p>
              <p v-if="l.detail" class="text-[11px] text-muted-foreground leading-relaxed break-words">{{ l.detail }}</p>
            </div>
          </li>
        </ol>

        <div v-if="logs.length && !exhausted" class="p-4 flex justify-center">
          <Button variant="outline" size="sm" class="h-7 rounded-sm text-xs" :disabled="loading" @click="load(false)">
            <Loader2 v-if="loading" class="w-3.5 h-3.5 animate-spin" />
            加载更早的记录
          </Button>
        </div>
      </div>

      <div class="px-5 py-2.5 border-t border-border/80 shrink-0 flex justify-end">
        <Button variant="ghost" size="sm" class="h-7 rounded-sm text-xs gap-1" :disabled="loading" @click="load(true)">
          <RefreshCw class="w-3 h-3" :class="{ 'animate-spin': loading }" />刷新
        </Button>
      </div>
    </SheetContent>
  </Sheet>
</template>
