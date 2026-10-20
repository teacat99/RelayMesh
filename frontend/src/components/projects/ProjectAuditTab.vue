<script setup lang="ts">
// 审计：谁（标注身份）在什么范围做了什么、结果如何；可按主体 / 动作 / 结果 / 范围 / 时间筛选，导出 CSV。
// 从不记录文档正文与聊天正文。「按推断」= 调用方没声明身份（没带 project_directory），身份是系统推断的；
// 审计档里的 acl.denied 没有拦下任何东西，只是记下「若强制执行会被拒绝」，所以标成「已放行 · 强制执行会拒绝」，
// 不再和强制执行档里真的「被拒绝」画成一样（#150）。每条记录展开后先给一句白话解释（auditExplain），再列原始字段。
import { computed, onMounted, ref, watch } from 'vue'
import { Download } from 'lucide-vue-next'
import { toast } from 'vue-sonner'
import { projectErrorMessage, projectsApi, type AuditEvent } from '../../api/projects'
import { AUDIT_ACTION_OPTIONS, AUDIT_LEGEND, auditActionLabel, auditActorText, auditDetail, auditEventLabel, auditExplain, auditResultChip, auditScopeText, isWouldDeny } from '../../lib/projects'
import PageState from '../shell/PageState.vue'

const props = defineProps<{ projectId: number }>()

const items = ref<AuditEvent[]>([])
const nextBefore = ref(0)
const loading = ref(false)
const action = ref('')
const result = ref('')
const actor = ref('')
const scope = ref('')
const since = ref('') // datetime-local
const expanded = ref<number | null>(null)

const wouldDeny = computed(() => items.value.filter(isWouldDeny).length)

function query(before = 0) {
  return {
    project: props.projectId,
    action: action.value || undefined,
    result: result.value || undefined,
    actor: actor.value.trim() || undefined,
    scope: scope.value.trim() || undefined,
    since: since.value ? new Date(since.value).toISOString() : undefined,
    before: before || undefined,
    limit: 50,
  }
}

async function load(more = false) {
  loading.value = true
  try {
    const res = await projectsApi.audit(query(more ? nextBefore.value : 0))
    items.value = more ? [...items.value, ...res.items] : res.items
    nextBefore.value = res.next_before
  } catch (e) {
    toast.error(projectErrorMessage(e, '审计加载失败'))
  } finally {
    loading.value = false
  }
}

async function exportCsv() {
  try {
    const blob = await projectsApi.auditCsv({ ...query(), limit: 500 })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = 'relaymesh-audit.csv'
    a.click()
    URL.revokeObjectURL(url)
  } catch (e) {
    toast.error(projectErrorMessage(e, '导出失败'))
  }
}

function timeText(iso: string): string {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString('zh-CN', { hour12: false })
}

let timer: ReturnType<typeof setTimeout> | null = null
watch([action, result, actor, scope, since], () => {
  if (timer) clearTimeout(timer)
  timer = setTimeout(() => void load(), 250)
})
watch(() => props.projectId, () => void load())
onMounted(() => void load())
</script>

<template>
  <div class="mx-auto w-full max-w-4xl space-y-3 p-4" data-testid="project-audit">
    <div class="flex flex-wrap items-center gap-2">
      <h3 class="text-[13px] font-semibold">审计</h3>
      <span v-if="wouldDeny" class="rounded-full bg-amber-500/15 px-2 py-0.5 text-[11px] text-amber-700 dark:text-amber-300" title="审计档没有拦下它们；切到强制执行后这些调用会被拒绝" data-testid="audit-would-deny">本页有 {{ wouldDeny }} 条「已放行，但强制执行会被拒绝」</span>
      <button type="button" class="ml-auto inline-flex h-8 cursor-pointer items-center gap-1 rounded-md border border-border px-3 text-[12px] hover:bg-accent" data-testid="audit-export" @click="exportCsv">
        <Download class="size-3.5" />导出 CSV
      </button>
    </div>
    <details class="rounded-md border border-border/60 px-3 py-2 text-[12px]" data-testid="audit-legend">
      <summary class="cursor-pointer select-none text-muted-foreground">怎么看这些记录（「按推断」「已放行 · 强制执行会拒绝」是什么意思）</summary>
      <dl class="mt-2 space-y-1.5">
        <div v-for="item in AUDIT_LEGEND" :key="item.term" class="flex flex-col gap-0.5 sm:flex-row sm:gap-3">
          <dt class="shrink-0 font-medium sm:w-44">{{ item.term }}</dt>
          <dd class="text-muted-foreground">{{ item.text }}</dd>
        </div>
      </dl>
    </details>
    <div class="grid grid-cols-2 gap-2 sm:grid-cols-5">
      <select v-model="action" class="h-8 rounded-md border border-input bg-background px-2 text-[12px]" data-testid="audit-action">
        <option value="">全部动作</option>
        <option v-for="a in AUDIT_ACTION_OPTIONS" :key="a" :value="a">{{ auditActionLabel(a) }}</option>
      </select>
      <select v-model="result" class="h-8 rounded-md border border-input bg-background px-2 text-[12px]" data-testid="audit-result">
        <option value="">全部结果</option>
        <option value="allow">放行</option>
        <option value="deny">被拒绝（含审计档里将被拒绝的）</option>
        <option value="assumed">按推断</option>
      </select>
      <input v-model="actor" placeholder="主体，如 identity:3" class="h-8 rounded-md border border-input bg-background px-2 text-[12px]" data-testid="audit-actor" />
      <input v-model="scope" placeholder="范围，如 modules/chat" class="h-8 rounded-md border border-input bg-background px-2 text-[12px]" />
      <input v-model="since" type="datetime-local" class="h-8 rounded-md border border-input bg-background px-2 text-[12px]" title="起始时间" />
    </div>

    <ul class="space-y-1" data-testid="audit-list">
      <PageState v-if="!items.length && !loading" as="li" kind="empty" size="sm" bordered title="没有符合条件的记录" data-testid="audit-empty" />
      <li v-for="e in items" :key="e.id" class="rounded-md border border-border/60 text-[12px]" :data-testid="`audit-row-${e.id}`" :data-action="e.action" :data-result="e.result" :data-would-deny="isWouldDeny(e) ? 'true' : 'false'">
        <button type="button" class="flex w-full cursor-pointer flex-wrap items-center gap-x-3 gap-y-0.5 px-2.5 py-1.5 text-left" @click="expanded = expanded === e.id ? null : e.id">
          <span class="w-36 shrink-0 text-[11px] text-muted-foreground">{{ timeText(e.at) }}</span>
          <span class="font-medium">{{ auditActorText(e) }}</span>
          <span :data-testid="`audit-label-${e.id}`">{{ auditEventLabel(e) }}</span>
          <span class="min-w-0 flex-1 truncate font-mono text-[11px] text-muted-foreground">{{ e.target || e.scope_ref }}</span>
          <span class="rounded-full px-2 py-0.5 text-[10px]" :class="auditResultChip(e).tone" :title="auditResultChip(e).hint" :data-testid="`audit-chip-${e.id}`">{{ auditResultChip(e).label }}</span>
        </button>
        <div v-if="expanded === e.id" class="space-y-0.5 border-t border-border/50 px-2.5 py-2 font-mono text-[11px] text-muted-foreground">
          <p v-if="auditExplain(e)" class="mb-1.5 font-sans text-[12px] leading-relaxed text-foreground" :data-testid="`audit-explain-${e.id}`">{{ auditExplain(e) }}</p>
          <div>主体：{{ e.actor_ref }}</div>
          <div>范围：{{ auditScopeText(e) }}</div>
          <div v-for="(v, k) in auditDetail(e)" :key="k">{{ k }}：{{ typeof v === 'object' ? JSON.stringify(v) : v }}</div>
        </div>
      </li>
    </ul>
    <div v-if="nextBefore" class="flex justify-center">
      <button type="button" class="h-8 cursor-pointer rounded-md border border-border px-4 text-[12px] hover:bg-accent disabled:opacity-50" :disabled="loading" data-testid="audit-more" @click="load(true)">加载更多</button>
    </div>
  </div>
</template>
