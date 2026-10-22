<script setup lang="ts">
// 任务详情里的「关联的确认项」：绑定到这个任务的计划确认 / 提问 / 决策，以及各自的结局。
// 待批准的计划放在最前并标出来——智能体在批准之前只做只读准备。作答统一在交流页的「待确认」条里。
import { computed, ref, watch } from 'vue'
import { ClipboardCheck } from 'lucide-vue-next'
import { confirmsApi, type ConfirmItem } from '../../api/confirms'
import { useConfirmStore } from '../../stores/confirms'
import { KIND_LABEL, STATUS_LABEL, ageText, answerText } from '../../lib/confirms'

const props = defineProps<{ workflowId: string; taskSeq: number }>()

const store = useConfirmStore()
const items = ref<ConfirmItem[]>([])
let token = 0

async function load() {
  const mine = ++token
  try {
    const list = await confirmsApi.list(props.workflowId, { task_seq: props.taskSeq, limit: 20 })
    if (mine === token) items.value = list
  } catch {
    if (mine === token) items.value = []
  }
}

watch(() => [props.workflowId, props.taskSeq, store.eventTick] as const, load, { immediate: true })

const rows = computed(() =>
  [...items.value].sort((a, b) => Number(b.status === 'pending') - Number(a.status === 'pending') || b.seq - a.seq),
)
const pendingPlans = computed(() => items.value.filter(i => i.kind === 'plan' && i.status === 'pending').length)
</script>

<template>
  <section v-if="rows.length" class="space-y-1.5" data-testid="work-detail-confirms">
    <h3 class="flex items-center gap-1.5 text-xs font-semibold text-foreground">
      <ClipboardCheck class="size-3.5 text-muted-foreground" />关联的确认项
      <span class="rounded-xs bg-muted px-1 font-mono text-[10px] tabular-nums text-muted-foreground">{{ rows.length }}</span>
      <span v-if="pendingPlans" class="rounded-xs border border-primary/40 px-1 text-[10px] font-normal text-primary" data-testid="work-detail-confirms-plan">待批准的计划 {{ pendingPlans }}</span>
    </h3>
    <ul class="divide-y divide-border/50 rounded-md border border-border/60 bg-muted/10">
      <li v-for="c in rows" :key="c.seq" class="space-y-0.5 px-3 py-1.5 text-xs" :data-testid="`work-detail-confirm-${c.ref}`" :data-status="c.status">
        <div class="flex flex-wrap items-center gap-x-2">
          <span class="font-mono text-[11px] font-semibold text-foreground/70">{{ c.ref }}</span>
          <span class="text-[10px] text-muted-foreground">{{ KIND_LABEL[c.kind] }}</span>
          <span class="min-w-0 break-words font-medium text-foreground">{{ c.title }}</span>
        </div>
        <p class="text-[11px] text-muted-foreground">
          <template v-if="c.status === 'answered'">已答复：<span class="text-foreground/80">{{ answerText(c, 60) }}</span></template>
          <template v-else-if="c.status === 'pending'">待答复——在交流页的「待确认」条里作答</template>
          <template v-else>{{ STATUS_LABEL[c.status] }}</template>
          <span class="ml-1.5 opacity-70">· {{ ageText(c.answered_at || c.created_at) }}</span>
        </p>
      </li>
    </ul>
  </section>
</template>
