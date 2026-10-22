<script setup lang="ts">
// 文档里的决策卡在对话里的汇总卡（#105）：一份文档可能有几十张决策卡，全灌进对话会把话淹没，
// 所以这里只出一张「文档《xxx》有 N 项待决策」的卡，去文档页逐张作答（文档页顶部还有一键采用推荐）。
//
// compact（#120 预览视图，默认关）：换成 32px 的事件行（阶段 C2：[决策]《文档》…… 状态句 · 状态 · 去文档页作答）。
// 经典列表不传它，经典的标记一个字没动。
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import { BookOpen, CheckCircle2 } from 'lucide-vue-next'
import { openDoc as navigateToDoc } from '../../lib/docNavClient'
import type { DecisionGroup } from '../../lib/timeline'
import { useConfirmStore } from '../../stores/confirms'
import { EVENT_BOX } from '../../lib/eventRow'
import { CARD_TONE_CLASS, CARD_TYPES } from '../../lib/messageCard'
import { STATUS_WORD } from '../../lib/statusWords'
import EventRowHead from '../feedback/EventRowHead.vue'
import EventStatus from '../feedback/EventStatus.vue'

const props = defineProps<{ group: DecisionGroup; compact?: boolean }>()
const router = useRouter()
const store = useConfirmStore()

const done = computed(() => props.group.pending === 0)

// 事件行的状态句：与经典同一句话（经典里待决策的数字加粗，这里是单行截断的纯文字）
const TONE = CARD_TONE_CLASS[CARD_TYPES.decision.tone]
const stateText = computed(() =>
  done.value
    ? `${props.group.total} 项决策都已处理（${props.group.answered} 项已答复）`
    : `有 ${props.group.pending} 项待决策（共 ${props.group.total} 项），去文档页逐项作答`,
)

function openDoc() {
  if (!props.group.docPath) return
  void navigateToDoc(router, { workflowId: store.workflowId, path: props.group.docPath })
}
</script>

<template>
  <!-- 事件行（预览视图，compact）：待决策时状态标签带脉动小点；行末的按钮去文档页 -->
  <div
    v-if="compact"
    :class="[EVENT_BOX, TONE]"
    data-testid="decision-group-card"
    :data-doc="group.docPath"
    :data-pending="group.pending"
  >
    <EventRowHead type="decision" :title="`《${group.docTitle}》`" :preview="stateText" preview-testid="decision-group-state">
      <template #status>
        <EventStatus :tone="done ? 'plain' : 'amber'" :pulse="!done">{{ done ? STATUS_WORD.done : STATUS_WORD.pending }}</EventStatus>
      </template>
      <template #trail>
        <button
          v-if="group.docPath"
          type="button"
          class="shrink-0 cursor-pointer rounded-xs border px-2 py-0.5 text-[11px] hover:bg-muted"
          :class="done ? 'border-border text-muted-foreground' : 'border-amber-500/50 text-amber-700 hover:bg-amber-500/10 dark:text-amber-300'"
          data-testid="decision-group-open"
          @click="openDoc"
        >
          {{ done ? '查看文档' : '去文档页作答' }}
        </button>
      </template>
    </EventRowHead>
  </div>

  <div
    v-else
    class="flex items-start gap-2 rounded-sm border px-3 py-2 text-xs"
    :class="done ? 'border-border/60 bg-muted/20' : 'border-amber-500/40 bg-amber-500/5'"
    data-testid="decision-group-card"
    :data-doc="group.docPath"
    :data-pending="group.pending"
  >
    <component :is="done ? CheckCircle2 : BookOpen" class="mt-0.5 size-3.5 shrink-0" :class="done ? 'text-emerald-600 dark:text-emerald-400' : 'text-amber-600 dark:text-amber-400'" />
    <div class="min-w-0 flex-1 space-y-0.5">
      <div class="flex flex-wrap items-center gap-x-2 gap-y-0.5">
        <span class="text-[10px] text-muted-foreground">文档决策</span>
        <span class="min-w-0 break-words text-[13px] font-medium leading-snug text-foreground">《{{ group.docTitle }}》</span>
      </div>
      <p class="text-[11px] text-muted-foreground" data-testid="decision-group-state">
        <template v-if="done">{{ group.total }} 项决策都已处理（{{ group.answered }} 项已答复）</template>
        <template v-else>有 <b class="tabular-nums text-foreground">{{ group.pending }}</b> 项待决策（共 {{ group.total }} 项），去文档页逐项作答</template>
      </p>
    </div>
    <button
      v-if="group.docPath"
      type="button"
      class="shrink-0 cursor-pointer rounded-xs border px-2 py-0.5 text-[11px] hover:bg-muted"
      :class="done ? 'border-border text-muted-foreground' : 'border-amber-500/50 text-amber-700 hover:bg-amber-500/10 dark:text-amber-300'"
      data-testid="decision-group-open"
      @click="openDoc"
    >
      {{ done ? '查看文档' : '去文档页作答' }}
    </button>
  </div>
</template>
