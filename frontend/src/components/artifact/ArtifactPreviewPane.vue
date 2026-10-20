<script setup lang="ts">
// 聊天右侧的预览栏：桌面端是主区右边的一栏，手机上盖满屏幕。HTML 走沙箱渲染，其余文档显示 Markdown。
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import { ExternalLink, X } from 'lucide-vue-next'
import { openDoc as navigateToDoc } from '../../lib/docNavClient'
import { useArtifactPreviewStore } from '../../stores/artifactPreview'
import MarkdownRenderer from '../MarkdownRenderer.vue'
import ArtifactComments from './ArtifactComments.vue'
import ArtifactFrame from './ArtifactFrame.vue'

const store = useArtifactPreviewStore()
const router = useRouter()
const info = computed(() => {
  const m = store.meta[store.docId]
  return m && m !== 'missing' ? m : null
})

function openInDocs() {
  if (!info.value) return
  void navigateToDoc(router, { workflowId: info.value.workflow_id, path: info.value.path })
  store.close()
}
</script>

<template>
  <aside
    v-if="store.docId"
    class="fixed inset-0 z-50 flex flex-col bg-background md:static md:z-auto md:w-[440px] md:shrink-0 md:border-l md:border-border/60 lg:w-[520px]"
    data-testid="artifact-pane"
  >
    <header class="flex h-12 shrink-0 items-center gap-2 border-b border-border/60 px-3">
      <span class="min-w-0 flex-1 truncate text-sm font-semibold" data-testid="artifact-pane-title">{{ info?.title || '加载中…' }}</span>
      <button v-if="info" type="button" class="inline-flex cursor-pointer items-center gap-1 rounded-md px-2 py-1 text-[12px] text-muted-foreground hover:bg-accent hover:text-accent-foreground" data-testid="artifact-pane-open" @click="openInDocs">
        <ExternalLink class="size-3.5" />文档页
      </button>
      <button type="button" class="cursor-pointer rounded-md p-1.5 hover:bg-accent" title="关闭预览" aria-label="关闭预览" data-testid="artifact-pane-close" @click="store.close()">
        <X class="size-4" />
      </button>
    </header>
    <div class="min-h-0 flex-1 overflow-y-auto p-3">
      <template v-if="info">
        <ArtifactFrame v-if="info.render === 'html'" :key="`${info.id}-${info.rev}`" :doc-id="info.id" :rev="info.rev" />
        <MarkdownRenderer v-else :content="info.body ?? ''" :workflow-id="info.workflow_id" />
        <ArtifactComments :doc-id="info.id" />
      </template>
      <div v-else class="py-10 text-center text-[12px] text-muted-foreground">加载中…</div>
    </div>
  </aside>
</template>
