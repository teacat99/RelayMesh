<script setup lang="ts">
// 一篇文档的出链 / 反链：改名或删除之前先看谁在引用它；指向还没建的文档会标出来。
import { ref, watch } from 'vue'
import { ArrowDownLeft, ArrowUpRight } from 'lucide-vue-next'
import { projectDocsApi, type DocLinkRef, type DocLinks } from '@/api/projectDocs'

const props = defineProps<{ workflowId: string; docPath: string; rev: number }>()
const emit = defineEmits<{ (e: 'open', workflowId: string, path: string): void }>()

const links = ref<DocLinks | null>(null)

async function load() {
  const wf = props.workflowId
  const p = props.docPath
  try {
    const l = await projectDocsApi.links(wf, p)
    if (wf === props.workflowId && p === props.docPath) links.value = l
  } catch {
    links.value = null
  }
}

watch(() => [props.workflowId, props.docPath, props.rev], () => void load(), { immediate: true })

function open(l: DocLinkRef) {
  if (l.exists) emit('open', l.workflow_id || props.workflowId, l.path)
}
</script>

<template>
  <section
    v-if="links && (links.outlinks.length || links.backlinks.length)"
    class="mt-6 grid gap-3 border-t border-border pt-4 text-xs sm:grid-cols-2"
    data-testid="docs-links"
  >
    <div>
      <p class="mb-1 flex items-center gap-1 font-medium text-foreground"><ArrowUpRight class="size-3.5" />引用了 {{ links.outlinks.length }} 篇</p>
      <ul class="space-y-0.5">
        <li v-for="l in links.outlinks" :key="l.path">
          <button
            type="button"
            class="max-w-full truncate text-left"
            :class="l.exists ? 'cursor-pointer text-primary hover:underline' : 'cursor-default text-muted-foreground'"
            :disabled="!l.exists"
            :data-testid="`docs-out-${l.path}`"
            @click="open(l)"
          >{{ l.title || l.path }}<span v-if="!l.exists" class="ml-1 rounded-sm bg-amber-500/15 px-1 text-[10px] text-amber-700 dark:text-amber-400">还没有这篇</span></button>
          <span v-if="l.exists && l.title" class="ml-1 font-mono text-[10px] text-muted-foreground">{{ l.path }}</span>
        </li>
      </ul>
    </div>
    <div>
      <p class="mb-1 flex items-center gap-1 font-medium text-foreground"><ArrowDownLeft class="size-3.5" />被 {{ links.backlinks.length }} 篇引用</p>
      <ul v-if="links.backlinks.length" class="space-y-0.5">
        <li v-for="l in links.backlinks" :key="l.path + l.workflow_id">
          <button
            type="button"
            class="max-w-full cursor-pointer truncate text-left text-primary hover:underline"
            :data-testid="`docs-back-${l.path}`"
            @click="open(l)"
          >{{ l.title || l.path }}</button>
          <span class="ml-1 font-mono text-[10px] text-muted-foreground">{{ l.path }}</span>
        </li>
      </ul>
      <p v-else class="text-muted-foreground">还没有别的文档引用它</p>
    </div>
  </section>
</template>
