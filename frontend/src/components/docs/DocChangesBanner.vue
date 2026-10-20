<script setup lang="ts">
// 「自上次查看后的变更」：列出你上次离开文档页之后更新过的文档，点开即视为看过。
import { computed, ref } from 'vue'
import type { SeenItem } from '@/lib/docSeen'

const props = defineProps<{ items: SeenItem[] }>()
const emit = defineEmits<{ (e: 'open', path: string): void; (e: 'dismiss'): void }>()

const expanded = ref(false)
const shown = computed(() => (expanded.value ? props.items : props.items.slice(0, 5)))
</script>

<template>
  <div v-if="items.length" class="border-b border-border/60 bg-primary/5 px-2 py-2 text-[11px]" data-testid="docs-changes">
    <div class="flex items-center gap-1">
      <span class="font-medium text-foreground">自上次查看后 {{ items.length }} 篇有更新</span>
      <button type="button" class="ml-auto cursor-pointer text-muted-foreground hover:text-foreground" data-testid="docs-changes-dismiss" @click="emit('dismiss')">全部已读</button>
    </div>
    <ul class="mt-1 space-y-0.5">
      <li v-for="it in shown" :key="it.path">
        <button
          type="button"
          class="max-w-full cursor-pointer truncate text-left text-primary hover:underline"
          :data-testid="`docs-changes-${it.path}`"
          @click="emit('open', it.path)"
        >{{ it.title || it.path }}</button>
      </li>
    </ul>
    <button
      v-if="items.length > 5"
      type="button"
      class="mt-1 cursor-pointer text-muted-foreground hover:text-foreground"
      @click="expanded = !expanded"
    >{{ expanded ? '收起' : `还有 ${items.length - 5} 篇…` }}</button>
  </div>
</template>
