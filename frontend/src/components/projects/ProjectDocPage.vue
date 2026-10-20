<script setup lang="ts">
// 项目里的单篇文档页（#131）：知识库点开一篇文档、确认卡 / 决策卡 / 产物预览里的文档链接、老的 /docs/... 地址都落在这里。
// 在项目壳里显示（「知识库」页签高亮）；阅读 / 编辑 / 修订历史 / 评论都是 DocViewer，这里只管外框：
// 返回知识库、这篇文档来自哪个工作流 / 属于哪个模块、还没并入知识库时的提示，以及文档里的链接往哪跳。
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import { ArrowLeft } from 'lucide-vue-next'
import DocViewer from '../docs/DocViewer.vue'
import { useProjectDocStore } from '@/stores/projectDocs'
import { projectKbLocation } from '@/lib/docNav'
import { openDoc } from '@/lib/docNavClient'

const props = defineProps<{ projectId: number; workflowId: string; docRef: string }>()
const router = useRouter()
const store = useProjectDocStore()

// 这篇文档的元信息：只用来写页头，不影响正文（切文档的瞬间可能还是上一篇的，换成新的只是一眨眼的事）
const doc = computed(() => (store.detail && store.detail.doc.workflow_id === props.workflowId ? store.detail.doc : null))
const shownPath = computed(() => doc.value?.path || props.docRef)
// 文档还在工作流里、没有并入项目知识库（「管理 › 迁移」里并入）：知识库列表里暂时看不到它，但在这里照常阅读 / 编辑
const unmigrated = computed(() => !!doc.value && (doc.value.project_id ?? 0) !== props.projectId)

function back() {
  void router.push(projectKbLocation(props.projectId))
}
function toMigration() {
  void router.push({ name: 'projects', params: { projectId: String(props.projectId) }, query: { tab: 'migration' } })
}
function open(wf: string, path: string, anchor?: string) {
  void openDoc(router, { workflowId: wf, path, anchor, projectId: props.projectId })
}
</script>

<template>
  <div class="flex w-full min-w-0 flex-col gap-3 p-3 sm:p-4" data-testid="project-doc-page">
    <div class="flex flex-wrap items-center gap-2 text-[12px]">
      <button
        type="button"
        class="inline-flex cursor-pointer items-center gap-1 rounded-md border border-border/70 px-2 py-1 hover:bg-accent hover:text-accent-foreground"
        data-testid="project-doc-back"
        @click="back"
      >
        <ArrowLeft class="size-3.5" />返回知识库
      </button>
      <span class="min-w-0 max-w-full truncate font-mono text-[11px] text-muted-foreground" :title="shownPath" data-testid="project-doc-path">{{ shownPath }}</span>
      <span
        class="max-w-full truncate rounded-full bg-muted px-2 py-0.5 font-mono text-[10px] text-muted-foreground"
        :title="`来自工作流 ${workflowId}`"
        data-testid="project-doc-workflow"
      >{{ workflowId }}</span>
      <span v-if="doc?.module_path" class="rounded-full bg-primary/10 px-2 py-0.5 text-[10px] text-primary" data-testid="project-doc-module">模块 {{ doc.module_path }}</span>
      <span
        v-if="unmigrated"
        class="inline-flex items-center gap-1 rounded-full border border-amber-500/40 bg-amber-500/10 px-2 py-0.5 text-[10px] text-amber-700 dark:text-amber-400"
        data-testid="project-doc-unmigrated"
      >
        还没并入知识库
        <button type="button" class="cursor-pointer underline" data-testid="project-doc-to-migration" @click="toMigration">去迁移</button>
      </span>
    </div>
    <DocViewer :workflow-id="workflowId" :doc-ref="docRef" @open-doc="open" />
  </div>
</template>
