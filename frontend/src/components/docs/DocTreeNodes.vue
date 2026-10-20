<script setup lang="ts">
import { ChevronDown, ChevronRight, FileText } from 'lucide-vue-next'
import type { DocTreeNode } from '@/lib/docTree'
import { docKindAbbrev, docStatusDotClass } from '@/lib/docMeta'

defineProps<{
  nodes: DocTreeNode[]
  depth: number
  activePath: string
  expanded: Set<string>
}>()

const emit = defineEmits<{ (e: 'open', path: string): void; (e: 'toggle', key: string): void }>()
</script>

<template>
  <template v-for="node in nodes" :key="node.key">
    <li v-if="node.type === 'folder'" class="list-none">
      <button
        type="button"
        class="flex w-full items-center gap-1 rounded-xs px-1 py-0.5 text-left hover:bg-muted/80 outline-none focus-visible:ring-1 focus-visible:ring-ring"
        :style="{ paddingLeft: depth * 8 + 4 + 'px' }"
        :aria-expanded="expanded.has(node.key)"
        :title="`文件夹 ${node.name}`"
        :data-testid="`docs-tree-folder-${node.key}`"
        @click="emit('toggle', node.key)"
      >
        <ChevronDown v-if="expanded.has(node.key)" class="size-3 shrink-0 text-muted-foreground" aria-hidden="true" />
        <ChevronRight v-else class="size-3 shrink-0 text-muted-foreground" aria-hidden="true" />
        <span class="truncate font-medium text-muted-foreground">{{ node.name }}/</span>
      </button>
      <ul v-show="expanded.has(node.key)" class="mt-0.5">
        <DocTreeNodes
          :nodes="node.children"
          :depth="depth + 1"
          :active-path="activePath"
          :expanded="expanded"
          @toggle="emit('toggle', $event)"
          @open="emit('open', $event)"
        />
      </ul>
    </li>
    <li v-else class="list-none">
      <button
        type="button"
        class="flex w-full items-center gap-1.5 truncate rounded-xs px-2 py-0.5 text-left hover:bg-muted/80 outline-none focus-visible:ring-1 focus-visible:ring-ring"
        :class="activePath === node.doc.path ? 'bg-muted font-medium' : ''"
        :style="{ paddingLeft: depth * 8 + 16 + 'px' }"
        :title="node.doc.path"
        :data-testid="`docs-tree-leaf-${node.doc.path}`"
        @click="emit('open', node.doc.path)"
      >
        <span
          class="size-1.5 shrink-0 rounded-full"
          :class="docStatusDotClass(node.doc.status)"
          :aria-label="`状态 ${node.doc.status}`"
        />
        <span class="shrink-0 text-[9px] font-mono text-muted-foreground w-3 text-center" :title="node.doc.kind">{{
          docKindAbbrev(node.doc.kind)
        }}</span>
        <FileText class="size-3 shrink-0 text-muted-foreground" aria-hidden="true" />
        <span class="truncate">{{ node.name }}</span>
      </button>
    </li>
  </template>
</template>
