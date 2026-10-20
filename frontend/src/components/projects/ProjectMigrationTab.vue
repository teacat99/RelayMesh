<script setup lang="ts">
// 迁移向导：把已归入项目的目录下、原来按工作流存放的文档并入项目知识库。
// 先 dry-run 看清楚每篇文档的去向（只读；进页面就自动预览一次），确认时带上 dry-run 的哈希：两次之间数据变了会被拒绝，要求重新预览。
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { toast } from 'vue-sonner'
import { projectErrorMessage, projectsApi, type MigrationReport } from '../../api/projects'
import { projectKbLocation } from '../../lib/docNav'
import { moveReasonLabel } from '../../lib/projects'
import { useProjectStore } from '../../stores/projects'

const props = defineProps<{ projectId: number }>()
const store = useProjectStore()
const router = useRouter()

const report = ref<MigrationReport | null>(null)
const result = ref<MigrationReport | null>(null)
const busy = ref(false)

const pending = computed(() => report.value?.docs ?? 0)

async function preview() {
  busy.value = true
  result.value = null
  try {
    report.value = await projectsApi.migrationDryRun(props.projectId)
  } catch (e) {
    toast.error(projectErrorMessage(e, '预览失败'))
  } finally {
    busy.value = false
  }
}

async function apply() {
  if (!report.value) return
  if (!window.confirm(`确认迁移？${report.value.docs} 篇文档归入项目，其中 ${report.value.moves.length} 篇会改路径。`)) return
  busy.value = true
  try {
    result.value = await projectsApi.migrationApply(props.projectId, report.value.hash)
    report.value = null
    toast.success('迁移完成')
    await Promise.all([store.loadDetail(props.projectId), store.loadList()])
  } catch (e) {
    toast.error(projectErrorMessage(e, '迁移失败'))
    report.value = null // 哈希失效：要求重新预览
  } finally {
    busy.value = false
  }
}

function toKb() {
  void router.push(projectKbLocation(props.projectId))
}

// 预览是只读的：进来就算一遍，用户直接看到会动哪些文档；确认迁移仍然是单独的一步。
onMounted(() => void preview())
</script>

<template>
  <div class="mx-auto w-full max-w-3xl space-y-3 p-4" data-testid="project-migration">
    <h3 class="text-[13px] font-semibold">文档迁移</h3>
    <p class="text-[12px] text-muted-foreground">
      归入项目的目录里，以前按工作流分开存放的文档可以并入项目知识库。不会自动迁移：先预览，再由你确认。
      原路径不冲突的文档路径不变；会话工作表放到 <code>sessions/&lt;工作流&gt;/</code>；不同工作流撞路径的放到 <code>legacy/&lt;工作流&gt;/</code>。
    </p>

    <button type="button" class="h-8 cursor-pointer rounded-md border border-border px-3 text-[12px] hover:bg-accent disabled:opacity-50" :disabled="busy" data-testid="migration-preview" @click="preview">
      {{ busy && !report ? '计算中…' : '预览迁移（不改任何数据）' }}
    </button>

    <div v-if="result" class="rounded-md border border-emerald-500/40 bg-emerald-500/10 p-3 text-[12px]" data-testid="migration-done">
      已迁移 {{ result.docs }} 篇文档（{{ result.assigned }} 篇路径不变，{{ result.moves.length }} 篇改了路径）。每篇文档的去向（编号与路径）都记在审计里，需要时可据此还原。
      <button type="button" class="ml-1 cursor-pointer text-primary underline" data-testid="migration-to-kb" @click="toKb">去知识库查看</button>
    </div>

    <div v-if="report" class="space-y-2 rounded-md border border-border/70 bg-card p-3" data-testid="migration-report">
      <div class="text-[12px]" data-testid="migration-summary">
        涉及 {{ report.workflows.length }} 个工作流；待迁移 {{ report.docs }} 篇（路径不变 {{ report.assigned }} · 改路径 {{ report.moves.length }}）；已在项目里 {{ report.already }} 篇。
      </div>
      <ul v-if="report.items?.length" class="max-h-72 space-y-1 overflow-y-auto" data-testid="migration-items">
        <li v-for="it in report.items" :key="it.doc_id" class="rounded border border-border/50 px-2 py-1 text-[11px]" :data-testid="`migration-item-${it.doc_id}`">
          <div class="truncate font-medium">{{ it.title || it.from_path }}</div>
          <div class="truncate font-mono">{{ it.from_path }}<template v-if="it.reason"> → {{ it.to_path }}</template></div>
          <div class="text-muted-foreground">工作流 {{ it.workflow_id }} · {{ it.reason ? moveReasonLabel(it.reason) : '路径不变' }}</div>
        </li>
      </ul>
      <ul v-else-if="report.moves.length" class="max-h-72 space-y-1 overflow-y-auto" data-testid="migration-moves">
        <li v-for="m in report.moves" :key="m.doc_id" class="rounded border border-border/50 px-2 py-1 text-[11px]">
          <div class="truncate font-mono">{{ m.from_path }} → {{ m.to_path }}</div>
          <div class="text-muted-foreground">工作流 {{ m.workflow_id }} · {{ moveReasonLabel(m.reason) }}</div>
        </li>
      </ul>
      <ul v-if="report.unplaceable?.length" class="space-y-0.5 text-[11px] text-destructive">
        <li v-for="u in report.unplaceable" :key="u">无法安置：{{ u }}</li>
      </ul>
      <div v-if="pending === 0" class="text-[12px] text-muted-foreground">没有需要迁移的文档。</div>
      <div v-else class="flex justify-end">
        <button type="button" class="h-8 cursor-pointer rounded-md bg-primary px-4 text-[12px] text-primary-foreground disabled:opacity-50" :disabled="busy" data-testid="migration-apply" @click="apply">
          确认迁移 {{ pending }} 篇
        </button>
      </div>
    </div>
  </div>
</template>
