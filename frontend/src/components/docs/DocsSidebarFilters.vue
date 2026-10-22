<script setup lang="ts">
import { Search } from 'lucide-vue-next'
import { DOC_KINDS, DOC_KIND_LABEL, DOC_STATUSES, DOC_STATUS_LABEL } from '@/lib/docMeta'

defineProps<{
  searchQ: string
  kind: string
  status: string
  tag: string
  allTags: string[]
  blueprintGaps: number
  searchHits: { path: string; title: string; snippet: string; anchor?: string }[]
  searchMode: boolean
}>()

const emit = defineEmits<{
  (e: 'update:searchQ', v: string): void
  (e: 'update:kind', v: string): void
  (e: 'update:status', v: string): void
  (e: 'update:tag', v: string): void
  (e: 'search'): void
  (e: 'blueprint'): void
  (e: 'openHit', path: string, anchor?: string): void
}>()

function highlightSnippet(snippet: string, q: string): string {
  if (!q.trim()) return snippet
  const esc = q.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  return snippet.replace(new RegExp(`(${esc})`, 'gi'), '<mark class="bg-primary/20 text-foreground rounded-xs px-0.5">$1</mark>')
}
</script>

<template>
  <div class="p-2 border-b border-border/60 space-y-2">
    <div class="relative">
      <Search class="absolute left-2 top-2 size-3.5 text-muted-foreground" aria-hidden="true" />
      <input
        :value="searchQ"
        class="w-full rounded-xs border border-border bg-muted/30 py-1.5 pl-8 pr-2 text-xs"
        placeholder="搜索…"
        data-testid="docs-search"
        @input="emit('update:searchQ', ($event.target as HTMLInputElement).value)"
        @keydown.enter="emit('search')"
      />
    </div>
    <div class="flex flex-wrap gap-1">
      <select
        :value="kind"
        class="rounded-xs border border-border bg-background text-[10px] py-0.5 px-1"
        data-testid="docs-filter-kind"
        @change="emit('update:kind', ($event.target as HTMLSelectElement).value)"
      >
        <option value="">类型</option>
        <option v-for="k in DOC_KINDS" :key="k" :value="k">{{ DOC_KIND_LABEL[k] || k }}</option>
      </select>
      <select
        :value="status"
        class="rounded-xs border border-border bg-background text-[10px] py-0.5 px-1"
        data-testid="docs-filter-status"
        @change="emit('update:status', ($event.target as HTMLSelectElement).value)"
      >
        <option value="">状态</option>
        <option v-for="s in DOC_STATUSES" :key="s" :value="s">{{ DOC_STATUS_LABEL[s] || s }}</option>
      </select>
      <select
        :value="tag"
        class="rounded-xs border border-border bg-background text-[10px] py-0.5 px-1 max-w-[5rem]"
        data-testid="docs-filter-tag"
        @change="emit('update:tag', ($event.target as HTMLSelectElement).value)"
      >
        <option value="">标签</option>
        <option v-for="t in allTags" :key="t" :value="t">{{ t }}</option>
      </select>
    </div>
    <p v-if="blueprintGaps > 0" class="text-[10px] text-amber-600 dark:text-amber-400" data-testid="docs-blueprint-hint">
      蓝图缺口 {{ blueprintGaps }} 项 ·
      <button type="button" class="underline" data-testid="docs-blueprint-start" @click="emit('blueprint')">用蓝图开始</button>
    </p>
    <ul v-if="searchMode && searchHits.length" class="text-[10px] space-y-1 max-h-40 overflow-y-auto" data-testid="docs-search-hits">
      <li v-for="hit in searchHits" :key="hit.path + (hit.anchor || '')">
        <button type="button" class="text-left w-full hover:text-primary" @click="emit('openHit', hit.path, hit.anchor)">
          <span class="font-mono block truncate">{{ hit.path }}</span>
          <span class="text-muted-foreground line-clamp-2" v-html="highlightSnippet(hit.snippet, searchQ)" />
        </button>
      </li>
    </ul>
  </div>
</template>
