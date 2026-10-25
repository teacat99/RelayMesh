<script setup lang="ts">
// 工具栏上的「待答复」小标签（取代原先输入区上方占整行的「待确认」长条，#50）。
// 长条的问题：确认没问题可能要很久，它一直占着一整行、像一笔「欠着的账」；而提问卡本来就在对话里、
// 可以就地作答，所以这里只留一个不占地方的入口：数量 + 点开列出待答复的提问，点一条定位到对话里那张卡。
// L0 / L1 且有推荐选项的可以「全部采用推荐」（提交前会列出将要提交的全部结果；L2 永远逐条确认）。
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { BookCheck, BookOpen, Sparkles } from 'lucide-vue-next'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import Button from '../ui/button/Button.vue'
import ConfirmBatchDialog from './ConfirmBatchDialog.vue'
import { toast } from '@/components/ui/sonner'
import { confirmErrorMessage } from '../../api/confirms'
import { openDoc as navigateToDoc } from '../../lib/docNavClient'
import { useConfirmStore } from '../../stores/confirms'
import {
  KIND_LABEL,
  RISK_SHORT,
  adoptPreview,
  adoptableItems,
  ageText,
  pendingChipText,
  pendingChipTitle,
  riskTone,
} from '../../lib/confirms'
import { groupDecisions } from '../../lib/timeline'

const props = defineProps<{ workflowId: string }>()

const store = useConfirmStore()
const router = useRouter()

const open = ref(false)
const items = computed(() => [...store.board.items].sort((a, b) => a.seq - b.seq))
// 提问 / 计划确认：对话里各有一张卡，点一下定位过去；文档里的决策卡按文档汇总，去文档页作答
const asks = computed(() => items.value.filter(i => i.kind !== 'decision'))
const docGroups = computed(() => groupDecisions(items.value))
const visible = computed(() => store.loaded && store.workflowId === props.workflowId && items.value.length > 0)
const text = computed(() => pendingChipText(items.value.length))
const title = computed(() => pendingChipTitle(items.value.length))

const adoptable = computed(() => adoptableItems(store.board.items))
const rows = computed(() => adoptPreview(store.board.items))

// 有新的提问进来：徽标闪一下提醒，不替用户打开任何东西（用户可能正在读上面的内容）
const pulse = ref(false)
let pulseTimer: ReturnType<typeof setTimeout> | null = null
watch(
  () => items.value.length,
  (now, before) => {
    if (!store.loaded || now <= before) return
    pulse.value = true
    if (pulseTimer) clearTimeout(pulseTimer)
    pulseTimer = setTimeout(() => (pulse.value = false), 3000)
  },
)
onBeforeUnmount(() => {
  if (pulseTimer) clearTimeout(pulseTimer)
})
// 全部处理完了，点开的面板跟着收起，免得留下一个空壳
watch(visible, v => {
  if (!v) open.value = false
})

// 定位到对话里的那张卡：先清空再赋值，同一张卡连点两次也会再次触发滚动与高亮；
// 卡在更早的轮次里时，对话列表会先把窗口撑到它所在的位置（见 PreviewMessageList 对 focusRef 的监听）。
async function jump(ref: string) {
  open.value = false
  store.focusRef = ''
  await nextTick()
  store.focusRef = ref
}

const batchOpen = ref(false)
const batchError = ref('')
const batchBusy = computed(() => store.busy.has('*'))

function openBatch() {
  batchError.value = ''
  open.value = false
  batchOpen.value = true
}

async function submitBatch() {
  batchError.value = ''
  try {
    const done = await store.adoptRecommended(adoptable.value.map(i => i.ref))
    batchOpen.value = false
    toast.success(`已采用推荐，提交 ${done.length} 项`, { duration: 2500 })
  } catch (err) {
    batchError.value = confirmErrorMessage(err)
  }
}

function openDoc(docPath: string) {
  if (!docPath) return
  open.value = false
  void navigateToDoc(router, { workflowId: props.workflowId, path: docPath })
}

</script>

<template>
  <Popover v-if="visible" v-model:open="open">
    <PopoverTrigger as-child>
      <button
        type="button"
        data-testid="confirm-chip"
        :data-count="items.length"
        :title="title"
        :aria-label="title"
        class="text-[11px] sm:text-xs px-2 sm:px-2.5 py-1 rounded-sm border transition-all shadow-2xs flex items-center gap-1.5 cursor-pointer shrink-0 border-amber-500/50 bg-amber-500/10 hover:bg-amber-500/20 text-amber-800 dark:text-amber-200 backdrop-blur-xs select-none font-mono outline-none focus-visible:ring-1 focus-visible:ring-ring"
        :class="pulse ? 'animate-pulse' : ''"
      >
        <BookCheck class="w-3.5 h-3.5 text-amber-600 dark:text-amber-400" />
        <span data-testid="confirm-chip-text">{{ text }}</span>
      </button>
    </PopoverTrigger>

    <PopoverContent align="start" class="w-[min(26rem,calc(100vw-1.5rem))] p-3 font-mono text-[11px] sm:text-xs">
      <div data-testid="confirm-chip-popover" class="space-y-2.5">
        <div class="font-semibold text-foreground">
          待答复<span class="font-normal text-muted-foreground/70"> · 点一条定位到对话里的那张卡，在卡上直接作答</span>
        </div>

        <ul v-if="asks.length || docGroups.length" class="space-y-1">
          <li v-for="it in asks" :key="it.seq">
            <button
              type="button"
              :data-testid="`confirm-chip-item-${it.ref}`"
              class="w-full cursor-pointer rounded-sm border border-border/70 px-2 py-1.5 text-left transition-colors hover:bg-muted"
              @click="jump(it.ref)"
            >
              <span class="flex flex-wrap items-center gap-x-1.5 gap-y-0.5">
                <span class="font-semibold text-foreground/80">{{ it.ref }}</span>
                <span class="text-[10px] text-muted-foreground">{{ KIND_LABEL[it.kind] }}</span>
                <span
                  v-if="riskTone(it.risk) !== 'muted'"
                  class="rounded-xs border px-1 text-[10px]"
                  :class="riskTone(it.risk) === 'danger' ? 'border-destructive/50 text-destructive' : 'border-amber-500/40 text-amber-700 dark:text-amber-300'"
                >{{ RISK_SHORT[it.risk] }}</span>
                <span class="ml-auto text-[10px] text-muted-foreground">{{ ageText(it.created_at) }}</span>
              </span>
              <span class="mt-0.5 block break-words font-sans text-[12px] leading-snug text-foreground">{{ it.title }}</span>
            </button>
          </li>
          <li v-for="g in docGroups" :key="g.key">
            <button
              type="button"
              :data-testid="`confirm-chip-doc-${g.docPath || g.key}`"
              class="w-full cursor-pointer rounded-sm border border-border/70 px-2 py-1.5 text-left transition-colors hover:bg-muted"
              @click="openDoc(g.docPath)"
            >
              <span class="flex flex-wrap items-center gap-x-1.5 gap-y-0.5">
                <BookOpen class="size-3 shrink-0 text-muted-foreground" />
                <span class="text-[10px] text-muted-foreground">文档决策</span>
                <span class="ml-auto text-[10px] text-muted-foreground">{{ g.pending }} 项待决策 · 去文档页作答</span>
              </span>
              <span class="mt-0.5 block break-words font-sans text-[12px] leading-snug text-foreground">《{{ g.docTitle }}》</span>
            </button>
          </li>
        </ul>

        <Button
          v-if="adoptable.length > 0"
          type="button"
          size="sm"
          variant="outline"
          class="h-7 w-full gap-1 border-amber-500/40 px-2 text-[11px]"
          :title="`把 ${adoptable.length} 项（L0 / L1、有推荐选项）一次采用推荐；提交前会列出全部结果`"
          data-testid="confirm-chip-adopt-all"
          @click="openBatch"
        >
          <Sparkles class="size-3" />全部采用推荐（{{ adoptable.length }}）
        </Button>
      </div>
    </PopoverContent>
  </Popover>

  <ConfirmBatchDialog v-model:open="batchOpen" :rows="rows" :busy="batchBusy" :error="batchError" @confirm="submitBatch" />
</template>
