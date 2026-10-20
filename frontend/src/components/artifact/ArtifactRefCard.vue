<script setup lang="ts">
// 聊天气泡里的 Artifact / 文档引用卡片：显示标题与类型，点一下在右侧预览。
import { computed, onMounted } from 'vue'
import { FileText, LayoutTemplate } from 'lucide-vue-next'
import { useArtifactPreviewStore } from '../../stores/artifactPreview'

const props = defineProps<{ docId: number }>()
const store = useArtifactPreviewStore()
const meta = computed(() => store.meta[props.docId])
const missing = computed(() => meta.value === 'missing')
const info = computed(() => (meta.value && meta.value !== 'missing' ? meta.value : null))
const active = computed(() => store.docId === props.docId)

onMounted(() => void store.load(props.docId))
</script>

<template>
  <button
    type="button"
    class="mt-1.5 flex w-full max-w-sm cursor-pointer items-center gap-2 rounded-lg border bg-background/70 px-2.5 py-2 text-left text-[12px] text-foreground transition-colors hover:bg-accent disabled:cursor-default disabled:opacity-60"
    :class="active ? 'border-primary/60' : 'border-border/70'"
    :disabled="missing"
    :data-testid="`artifact-ref-${docId}`"
    @click="store.open(docId)"
  >
    <component :is="info?.render === 'html' ? LayoutTemplate : FileText" class="size-4 shrink-0 text-primary" />
    <span class="min-w-0 flex-1">
      <span class="block truncate font-medium">{{ missing ? '这篇文档已不存在' : info?.title || '加载中…' }}</span>
      <span v-if="info" class="block truncate font-mono text-[10px] text-muted-foreground">{{ info.path }}</span>
    </span>
    <span v-if="info?.render === 'html'" class="shrink-0 rounded-full bg-primary/10 px-1.5 py-0.5 text-[10px] text-primary">可交互</span>
  </button>
</template>
