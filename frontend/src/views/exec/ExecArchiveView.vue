<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { toast } from 'vue-sonner'
import { Archive, ArchiveRestore, Bot, Search, SquareTerminal, X } from 'lucide-vue-next'
import Button from '../../components/ui/button/Button.vue'
import ExecPageHeader from '../../components/exec/ExecPageHeader.vue'
import { useExecStore } from '../../stores/exec'
import { useTerminalStore } from '../../stores/terminal'
import { archiveStatusText, archiveSummary, archivedItems, groupArchived } from '../../lib/terminal/archivePage'
import type { ExecItem } from '../../lib/terminal/execList'
import { EXEC_HOME, execSessionPath, execThreadPath } from '../../lib/mode'
import { relativeTime } from '../../lib/terminal/format'

// 归档页：侧栏里被归档的会话与模型线程都在这里，随时能进来（侧栏的「已归档」筛选只在有归档时才出现，所以以前很难找到）。
// 归档只是不在侧栏里占位——会话不会结束，记录与审计都还在；取消归档后条目回到侧栏。

const router = useRouter()
const exec = useExecStore()
const terminal = useTerminalStore()

const query = ref('')
const all = computed(() => archivedItems(exec.items))
const shown = computed(() => archivedItems(exec.items, query.value))
const groups = computed(() => groupArchived(shown.value))
const summary = computed(() => archiveSummary(all.value))
const loaded = computed(() => terminal.hostsLoaded)

// ---------- 多选 ----------
const selecting = ref(false)
const selected = ref<string[]>([])
const busy = ref(false)

function exitSelect() {
  selecting.value = false
  selected.value = []
}

function toggle(key: string) {
  selected.value = selected.value.includes(key) ? selected.value.filter(k => k !== key) : [...selected.value, key]
}

function selectAllShown() {
  selected.value = shown.value.map(i => i.key)
}

// 取消归档（或列表刷新）后，选中的条目可能已经不在归档里：把它们剔除。
watch(all, list => {
  if (!selected.value.length) return
  const keys = new Set(list.map(i => i.key))
  const kept = selected.value.filter(k => keys.has(k))
  if (kept.length !== selected.value.length) selected.value = kept
})

// 归档清空后没有可选的东西了，退出多选。
watch(
  () => summary.value.total,
  n => {
    if (n === 0) exitSelect()
  },
)

// ---------- 操作 ----------
function open(it: ExecItem) {
  void router.push(it.model ? execThreadPath(it.model.threadKey) : execSessionPath(it.sid))
}

async function unarchive(keys: string[]) {
  if (busy.value || keys.length === 0) return
  busy.value = true
  try {
    if (await terminal.setArchived(keys, false)) {
      toast.success(keys.length === 1 ? '已取消归档，回到侧栏' : `已取消归档 ${keys.length} 个条目，回到侧栏`)
      selected.value = selected.value.filter(k => !keys.includes(k))
    }
  } finally {
    busy.value = false
  }
}

function dotClass(it: ExecItem): string {
  if (!it.running) return 'bg-muted-foreground/40'
  return it.hostOnline ? 'bg-emerald-500' : 'border border-emerald-500'
}

function timeText(it: ExecItem): string {
  return it.lastActive ? relativeTime(new Date(it.lastActive).toISOString(), exec.now) : ''
}

function backToSessions() {
  void router.push(EXEC_HOME)
}
</script>

<template>
  <div class="flex-1 min-h-0 flex flex-col" data-testid="exec-archive">
    <ExecPageHeader>
      <Archive class="w-4 h-4 text-primary shrink-0" aria-hidden="true" />
      <div class="min-w-0">
        <h1 class="truncate text-sm font-medium text-foreground leading-tight">归档</h1>
        <p class="truncate text-[11px] text-muted-foreground leading-tight" data-testid="archive-summary">
          <template v-if="!loaded">正在读取…</template>
          <template v-else-if="summary.total === 0">暂无归档</template>
          <template v-else>
            共 {{ summary.total }} 个<template v-if="summary.running > 0"> · {{ summary.running }} 个仍在运行</template>
          </template>
        </p>
      </div>
      <template #actions>
        <Button
          v-if="summary.total > 0"
          variant="ghost"
          size="sm"
          class="h-7 px-2 gap-1.5 text-[11px] rounded-sm"
          :aria-pressed="selecting"
          data-testid="archive-select-toggle"
          @click="selecting ? exitSelect() : (selecting = true)"
        >
          {{ selecting ? '完成' : '选择' }}
        </Button>
      </template>
    </ExecPageHeader>

    <div class="flex-1 min-h-0 overflow-y-auto">
      <div class="max-w-3xl mx-auto px-3 sm:px-5 py-4 space-y-4">
        <p class="text-xs leading-relaxed text-muted-foreground">
          归档只是不在侧栏里占位：会话不会结束，记录与审计都还在，模型线程里的内容也不变。取消归档后，条目会回到侧栏。
        </p>

        <div v-if="!loaded" class="space-y-1.5" aria-busy="true" aria-label="正在读取归档">
          <div v-for="i in 4" :key="i" class="h-14 rounded-sm bg-muted/60 animate-pulse" />
        </div>

        <!-- 还没有归档：说明怎么归档，而不是一片空白 -->
        <div
          v-else-if="summary.total === 0"
          class="rounded-sm border border-dashed border-border/80 px-4 py-10 text-center space-y-3"
          data-testid="archive-empty"
        >
          <Archive class="mx-auto w-6 h-6 text-muted-foreground/70" aria-hidden="true" />
          <p class="text-sm text-foreground">还没有归档的会话</p>
          <p class="mx-auto max-w-sm text-xs leading-relaxed text-muted-foreground">
            在侧栏里右键一个会话（手机上向右滑）选择「归档」，它就会收进这里，侧栏随之清爽；会话本身照常运行。
          </p>
          <Button variant="outline" size="sm" class="h-8 rounded-sm text-xs" @click="backToSessions">回到会话</Button>
        </div>

        <template v-else>
          <div class="relative">
            <Search class="w-3.5 h-3.5 absolute left-2.5 top-2.5 text-muted-foreground" aria-hidden="true" />
            <input
              v-model="query"
              type="search"
              placeholder="搜索已归档的会话 / 主机 / 调用方..."
              aria-label="搜索已归档的会话"
              data-testid="archive-search"
              class="w-full pl-8 pr-8 py-1.5 text-xs bg-background border border-border/70 rounded-sm focus:outline-none focus:ring-1 focus:ring-primary/30 placeholder:text-muted-foreground"
            />
            <button
              v-if="query"
              type="button"
              class="absolute right-1.5 top-1.5 p-0.5 rounded-xs text-muted-foreground hover:text-foreground cursor-pointer"
              aria-label="清除搜索"
              @click="query = ''"
            >
              <X class="w-3.5 h-3.5" />
            </button>
          </div>

          <p v-if="shown.length === 0" class="py-8 text-center text-xs text-muted-foreground">没有匹配的归档</p>

          <section v-for="g in groups" :key="g.id" :aria-label="g.label" :data-testid="`archive-group-${g.id}`">
            <h2 class="px-1 pb-1.5 flex items-center gap-1.5 text-[11px] font-medium text-muted-foreground">
              <Bot v-if="g.id === 'model'" class="w-3 h-3" aria-hidden="true" />
              <SquareTerminal v-else class="w-3 h-3" aria-hidden="true" />
              <span class="truncate">{{ g.label }}</span>
              <span class="tabular-nums opacity-70">{{ g.items.length }}</span>
              <span v-if="g.running > 0" class="text-emerald-600 dark:text-emerald-400">· {{ g.running }} 个运行中</span>
            </h2>
            <ul class="space-y-1">
              <li v-for="it in g.items" :key="it.key" :data-testid="`archive-row-${it.key}`">
                <div class="flex items-center gap-2.5 rounded-sm border border-border/60 bg-card px-3 py-2 transition-colors hover:bg-muted/40">
                  <input
                    v-if="selecting"
                    type="checkbox"
                    class="size-4 shrink-0 cursor-pointer accent-primary"
                    :checked="selected.includes(it.key)"
                    :aria-label="`选择 ${it.title}`"
                    @change="toggle(it.key)"
                  />
                  <Bot v-if="it.model" class="w-3.5 h-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
                  <span v-else class="w-1.5 h-1.5 rounded-full shrink-0" :class="dotClass(it)" aria-hidden="true" />
                  <button
                    type="button"
                    class="min-w-0 flex-1 text-left cursor-pointer"
                    :title="selecting ? '选中 / 取消选中' : '打开（不会取消归档）'"
                    :data-testid="`archive-open-${it.key}`"
                    @click="selecting ? toggle(it.key) : open(it)"
                  >
                    <span class="block truncate text-sm font-medium text-foreground">{{ it.title }}</span>
                    <span class="block truncate text-[11px] text-muted-foreground">
                      {{ archiveStatusText(it) }}<template v-if="timeText(it)"> · {{ timeText(it) }}</template>
                    </span>
                  </button>
                  <Button
                    v-if="!selecting"
                    variant="outline"
                    size="sm"
                    class="h-7 px-2 gap-1 text-[11px] rounded-sm shrink-0"
                    :disabled="busy"
                    :data-testid="`archive-restore-${it.key}`"
                    @click="unarchive([it.key])"
                  >
                    <ArchiveRestore class="w-3.5 h-3.5" /><span class="hidden sm:inline">取消归档</span><span class="sm:hidden sr-only">取消归档</span>
                  </Button>
                </div>
              </li>
            </ul>
          </section>
        </template>
      </div>
    </div>

    <!-- 多选：底栏 -->
    <div v-if="selecting" class="shrink-0 border-t border-border/70 bg-card/90 backdrop-blur-md px-3 sm:px-5 py-2 flex items-center gap-2" data-testid="archive-bulkbar">
      <span class="text-xs text-muted-foreground tabular-nums">已选 {{ selected.length }} 个</span>
      <button type="button" class="text-xs text-primary hover:underline cursor-pointer" @click="selectAllShown">全选</button>
      <button v-if="selected.length" type="button" class="text-xs text-muted-foreground hover:text-foreground cursor-pointer" @click="selected = []">清除</button>
      <span class="flex-1" />
      <Button size="sm" class="h-8 px-3 gap-1.5 text-xs rounded-sm" :disabled="busy || selected.length === 0" data-testid="archive-bulk-restore" @click="unarchive(selected)">
        <ArchiveRestore class="w-3.5 h-3.5" />取消归档
      </Button>
    </div>
  </div>
</template>
