<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import type { ProjectDoc } from '@/api/projectDocs'
import { buildDocTree, defaultExpandedKeys, loadExpandedFolders, saveExpandedFolders } from '@/lib/docTree'
import DocTreeNodes from './DocTreeNodes.vue'

const props = defineProps<{
  items: ProjectDoc[]
  workflowId: string
  activePath: string
}>()

const emit = defineEmits<{ (e: 'open', path: string): void }>()

const expanded = ref<Set<string>>(new Set())

watch(
  () => [props.workflowId, props.items.length] as const,
  () => {
    const loaded = loadExpandedFolders(props.workflowId)
    const tree = buildDocTree(props.items)
    expanded.value = loaded.size ? loaded : new Set(defaultExpandedKeys(tree))
  },
  { immediate: true },
)

watch(
  expanded,
  s => {
    if (props.workflowId) saveExpandedFolders(props.workflowId, s)
  },
  { deep: true },
)

const tree = computed(() => buildDocTree(props.items))

function toggleFolder(key: string) {
  const next = new Set(expanded.value)
  if (next.has(key)) next.delete(key)
  else next.add(key)
  expanded.value = next
}
</script>

<template>
  <ul class="space-y-0.5 p-0 m-0" data-testid="docs-tree">
    <DocTreeNodes
      :nodes="tree"
      :depth="0"
      :active-path="activePath"
      :expanded="expanded"
      @toggle="toggleFolder"
      @open="emit('open', $event)"
    />
  </ul>
</template>
