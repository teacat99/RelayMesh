<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { Ban, Bot, ChevronDown, ExternalLink, Loader2, Server } from 'lucide-vue-next'
import BrailleSpinner from '../../ui/spinner/BrailleSpinner.vue'
import type { HostView, LauncherOtherRun } from '../../../api/terminal'
import { hostTitle, shortCode } from '../../../lib/terminal/format'
import {
  RUN_TONE_DOT,
  RUN_TONE_TEXT,
  otherRunCancellable,
  otherRunHint,
  otherRunSource,
  othersSummary,
  runMeta,
  runStatus,
} from '../../../lib/terminal/launcherRun'

/**
 * 「其他非交互运行」（#147）：Hub 提交的、不属于任何预设的非交互运行——会话页旧的「运行」入口（或脚本）提交的、
 * 旧版会话守护的「非交互唤醒」提交的（该功能已下线，只剩旧记录）、以及所属预设已被删除的预设运行。预设清单只认「预设自己的最近一次运行」，
 * 这些运行在那里没有位置，以前只能到 ExecMesh 里翻；这里把进行中的和 24 小时内结束的列出来：
 * 可以到 ExecMesh 里看完整输出，进行中的可以取消（取消不需要解锁——它只会让运行停下来）。
 * 有运行在进行时展开（之后保持展开，运行结束时那一行还在眼前）；打开时全都已经结束就折成一行概况，不占清单的位置；
 * 点标题可以手动展开 / 收起。
 */
const props = defineProps<{
  runs: LauncherOtherRun[]
  hosts: HostView[]
  /** 正在取消的运行编号（取消要等服务端回应，期间这一行不允许再点）。 */
  cancelBusyId?: string
}>()

const emit = defineEmits<{
  (e: 'open-thread', key: string): void
  (e: 'cancel', run: LauncherOtherRun): void
}>()

/** null = 还没有定：有进行中的运行才展开；用户点过标题就听用户的。 */
const expanded = ref<boolean | null>(null)
const activeCount = computed(() => props.runs.filter(r => r.active).length)
const open = computed(() => expanded.value ?? activeCount.value > 0)
const summary = computed(() => othersSummary(props.runs))
// 有运行在进行、于是展开之后就保持展开：运行结束（或刚被你取消）时，那一行要留在眼前让人看到结果，而不是缩成一行概况。
watch(activeCount, n => {
  if (n > 0 && expanded.value === null) expanded.value = true
}, { immediate: true })

const rows = computed(() =>
  props.runs.map(r => {
    const host = props.hosts.find(h => h.machine_code === r.host)
    return {
      r,
      stat: runStatus(r),
      src: otherRunSource(r.source),
      hostLabel: host ? hostTitle(host) : shortCode(r.host),
      hint: otherRunHint(r),
      cancellable: otherRunCancellable(r),
    }
  }),
)

// 取消要点两次：第一次变成「确认取消」，几秒内没有再点就恢复，免得手滑把一个跑了很久的会话停掉。
const armedId = ref('')
let armedTimer: ReturnType<typeof setTimeout> | undefined
function askCancel(r: LauncherOtherRun) {
  if (props.cancelBusyId) return
  if (armedId.value === r.run_id) {
    clearTimeout(armedTimer)
    armedId.value = ''
    emit('cancel', r)
    return
  }
  armedId.value = r.run_id
  clearTimeout(armedTimer)
  armedTimer = setTimeout(() => (armedId.value = ''), 4000)
}
onBeforeUnmount(() => clearTimeout(armedTimer))
</script>

<template>
  <section class="rounded-md border border-border/70 bg-muted/10" data-testid="launcher-others" :data-open="open ? 'true' : 'false'" :data-active-count="activeCount">
    <button
      type="button"
      class="flex w-full cursor-pointer items-center gap-2 px-3 py-2 text-left transition-colors hover:bg-muted/30"
      :aria-expanded="open"
      data-testid="launcher-others-toggle"
      @click="expanded = !open"
    >
      <Bot class="size-3.5 shrink-0 text-violet-600 dark:text-violet-400" aria-hidden="true" />
      <span class="shrink-0 text-[13px] font-medium text-foreground">其他非交互运行</span>
      <span class="min-w-0 truncate text-[11px] text-muted-foreground" data-testid="launcher-others-summary">{{ summary }}</span>
      <ChevronDown class="ml-auto size-3.5 shrink-0 text-muted-foreground transition-transform" :class="open ? 'rotate-180' : ''" aria-hidden="true" />
    </button>

    <div v-if="open" class="space-y-2 border-t border-border/60 px-3 py-2.5">
      <p class="text-[11px] leading-relaxed text-muted-foreground" data-testid="launcher-others-note">
        这些运行不属于任何预设（会话页旧的「运行」入口、非交互唤醒，或所属预设已被删除），所以不在下面的预设清单里。只列进行中的和 24 小时内结束的。
      </p>
      <ul class="divide-y divide-border/60 rounded-sm border border-border/60 bg-background/60">
        <li
          v-for="row in rows"
          :key="row.r.run_id"
          class="space-y-1 px-2.5 py-2"
          :data-testid="`other-run-${row.r.run_id}`"
          :data-run-state="row.r.state"
          :data-source="row.r.source"
        >
          <div class="flex flex-wrap items-center justify-between gap-x-3 gap-y-1">
            <p class="flex min-w-0 flex-wrap items-center gap-x-1.5 gap-y-0.5 text-[11px]" :data-testid="`other-run-status-${row.r.run_id}`">
              <span class="inline-flex shrink-0 items-center gap-1 font-medium" :class="RUN_TONE_TEXT[row.stat.tone]">
                <BrailleSpinner v-if="row.stat.busy" size="sm" />
                <span v-else class="size-1.5 rounded-full" :class="RUN_TONE_DOT[row.stat.tone]" aria-hidden="true" />
                {{ row.stat.label }}
              </span>
              <span class="text-muted-foreground">{{ runMeta(row.r) }}</span>
            </p>
            <div class="flex shrink-0 items-center gap-2.5">
              <button
                v-if="row.r.thread_key"
                type="button"
                class="inline-flex cursor-pointer items-center gap-0.5 text-[11px] text-primary underline-offset-2 hover:underline"
                title="在 ExecMesh 里查看这次运行的完整输出"
                :data-testid="`other-run-open-${row.r.run_id}`"
                @click="emit('open-thread', row.r.thread_key!)"
              >
                <ExternalLink class="size-3" aria-hidden="true" />在 ExecMesh 查看
              </button>
              <button
                v-if="row.cancellable"
                type="button"
                class="inline-flex h-6 cursor-pointer items-center gap-1 rounded-sm border px-2 text-[11px] transition-colors disabled:cursor-not-allowed disabled:opacity-60"
                :class="armedId === row.r.run_id
                  ? 'border-destructive/60 bg-destructive/10 font-medium text-destructive'
                  : 'border-border/70 text-foreground/80 hover:border-destructive/50 hover:bg-destructive/10 hover:text-destructive'"
                :disabled="!!cancelBusyId"
                :title="armedId === row.r.run_id ? '再点一次确认：主机会终止这次运行（对话本身不受影响，之后还可以续聊）' : '取消这次运行：主机会终止它'"
                :data-testid="`other-run-cancel-${row.r.run_id}`"
                :data-armed="armedId === row.r.run_id ? 'true' : undefined"
                @click="askCancel(row.r)"
              >
                <Loader2 v-if="cancelBusyId === row.r.run_id" class="size-3 animate-spin" aria-hidden="true" />
                <Ban v-else class="size-3" aria-hidden="true" />
                {{ cancelBusyId === row.r.run_id ? '取消中…' : armedId === row.r.run_id ? '确认取消' : '取消' }}
              </button>
            </div>
          </div>

          <p class="flex min-w-0 flex-wrap items-center gap-x-1.5 gap-y-0.5 text-[11px] text-muted-foreground">
            <span
              class="inline-flex shrink-0 items-center rounded-xs border border-violet-500/40 bg-violet-500/10 px-1 py-px text-[9px] text-violet-700 dark:text-violet-400"
              :title="row.src.hint"
              :data-testid="`other-run-source-${row.r.run_id}`"
            >{{ row.src.label }}</span>
            <span class="inline-flex min-w-0 items-center gap-1 font-mono" :title="`${row.hostLabel}${row.r.dir ? ' · ' + row.r.dir : ''}`">
              <Server class="size-3 shrink-0" aria-hidden="true" />
              <span class="truncate">{{ row.hostLabel }}<template v-if="row.r.dir"> · {{ row.r.dir }}</template></span>
            </span>
            <span v-if="row.r.workflow_id" class="min-w-0 truncate font-mono" :title="`工作流 ${row.r.workflow_id}`">· {{ row.r.workflow_id }}</span>
          </p>

          <p v-if="row.r.description" class="truncate text-[11px] text-foreground/80" :title="row.r.description">{{ row.r.description }}</p>
          <p
            v-if="row.r.reply"
            class="line-clamp-2 break-words text-[11px] leading-relaxed text-foreground/80"
            :title="row.r.reply"
            :data-testid="`other-run-reply-${row.r.run_id}`"
          >{{ row.r.reply }}</p>
          <p
            v-if="row.hint"
            class="text-[11px] leading-relaxed"
            :class="RUN_TONE_TEXT[row.stat.tone === 'bad' ? 'bad' : 'warn']"
            :data-testid="`other-run-hint-${row.r.run_id}`"
          >{{ row.hint }}</p>
        </li>
      </ul>
    </div>
  </section>
</template>
