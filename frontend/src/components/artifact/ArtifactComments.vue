<script setup lang="ts">
// 一篇文档的评论列表：未解决的在前，可标成已解决 / 重新打开。新评论经 SSE 刷新。
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Check, RotateCcw } from 'lucide-vue-next'
import { toast } from 'vue-sonner'
import { artifactsApi, type ArtifactComment } from '../../api/artifacts'
import { projectErrorMessage } from '../../api/projects'

const props = defineProps<{ docId: number }>()
const items = ref<ArtifactComment[]>([])
const showResolved = ref(false)
const loading = ref(false)

const openCount = computed(() => items.value.filter(c => !c.resolved).length)

async function refresh() {
  loading.value = true
  try {
    items.value = await artifactsApi.comments(props.docId, true)
  } catch (e) {
    toast.error(projectErrorMessage(e, '评论加载失败'))
  } finally {
    loading.value = false
  }
}

const shown = computed(() => (showResolved.value ? items.value : items.value.filter(c => !c.resolved)))

async function toggle(c: ArtifactComment) {
  try {
    const upd = await artifactsApi.resolveComment(props.docId, c.id, !c.resolved)
    items.value = items.value.map(x => (x.id === upd.id ? upd : x))
  } catch (e) {
    toast.error(projectErrorMessage(e, '操作失败'))
  }
}

function onEvent(ev: Event) {
  const d = (ev as CustomEvent).detail as { type?: string; doc_id?: number } | undefined
  if (d?.type === 'artifact_comment_updated' && d.doc_id === props.docId) void refresh()
}

watch(() => props.docId, () => void refresh())
onMounted(() => {
  window.addEventListener('rm-artifact-event', onEvent)
  void refresh()
})
onBeforeUnmount(() => window.removeEventListener('rm-artifact-event', onEvent))
defineExpose({ refresh })
</script>

<template>
  <section v-if="items.length" class="mt-6 space-y-2" data-testid="artifact-comments">
    <div class="flex items-center gap-2 text-[12px]">
      <span class="font-medium">评论</span>
      <span class="rounded-full bg-muted px-2 py-0.5 text-[10px] text-muted-foreground" data-testid="artifact-comments-open">{{ openCount }} 条未解决</span>
      <label class="ml-auto inline-flex cursor-pointer items-center gap-1 text-[11px] text-muted-foreground">
        <input v-model="showResolved" type="checkbox" class="size-3" data-testid="artifact-comments-show-resolved" />含已解决
      </label>
    </div>
    <ul class="space-y-1.5">
      <li v-if="!shown.length" class="rounded-md border border-dashed border-border/70 px-3 py-3 text-center text-[12px] text-muted-foreground">没有未解决的评论。</li>
      <li v-for="c in shown" :key="c.id" class="rounded-md border border-border/60 px-3 py-2 text-[12px]" :class="c.resolved ? 'opacity-60' : ''" :data-testid="`artifact-comment-${c.id}`">
        <div class="flex items-center gap-2 text-[11px] text-muted-foreground">
          <span class="font-medium text-foreground">{{ c.author_label }}</span>
          <span v-if="c.anchor" class="rounded bg-muted px-1.5 font-mono">#{{ c.anchor }}</span>
          <span>{{ new Date(c.created_at).toLocaleString() }}</span>
          <button
            type="button"
            class="ml-auto inline-flex cursor-pointer items-center gap-1 rounded px-1.5 py-0.5 hover:bg-accent"
            :data-testid="`artifact-comment-resolve-${c.id}`"
            @click="toggle(c)"
          >
            <component :is="c.resolved ? RotateCcw : Check" class="size-3" />{{ c.resolved ? '重新打开' : '标为已解决' }}
          </button>
        </div>
        <p class="mt-1 whitespace-pre-wrap break-words">{{ c.body }}</p>
      </li>
    </ul>
  </section>
</template>
