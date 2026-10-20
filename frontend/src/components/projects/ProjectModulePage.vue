<script setup lang="ts">
// 模块页：一个模块的总述、设计、决策、渲染型文档（图 / 页面）、相关任务与最近修订。
import { onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { toast } from 'vue-sonner'
import { projectErrorMessage, projectsApi, type KbDoc, type ModulePageData } from '../../api/projects'
import { DOC_KIND_LABEL } from '../../lib/docMeta'
import { openDoc } from '../../lib/docNavClient'
import { renderLabel } from '../../lib/projects'
import PageState from '../shell/PageState.vue'

const props = defineProps<{ projectId: number; path: string }>()
const emit = defineEmits<{ (e: 'open-module', path: string): void }>()
const router = useRouter()

const page = ref<ModulePageData | null>(null)
const loading = ref(false)
const missing = ref(false)

async function load() {
  loading.value = true
  missing.value = false
  try {
    page.value = await projectsApi.modulePage(props.projectId, props.path)
  } catch (e) {
    page.value = null
    missing.value = true
    toast.error(projectErrorMessage(e, '模块页加载失败'))
  } finally {
    loading.value = false
  }
}

function open(d: KbDoc) {
  void openDoc(router, { workflowId: d.workflow_id, path: d.path, projectId: props.projectId })
}

const STATE_TEXT: Record<string, string> = { todo: '待办', doing: '进行中', review: '验证中', done: '完成', shelved: '搁置' }

watch(() => [props.projectId, props.path], () => void load())
onMounted(() => void load())
</script>

<template>
  <div class="mx-auto w-full max-w-3xl space-y-4 p-4" data-testid="module-page">
    <PageState v-if="loading && !page" kind="loading" title="模块加载中…" data-testid="module-loading" />
    <PageState v-else-if="missing" kind="empty" title="没有这个模块" hint="可能已被删除，或路径改过了。" data-testid="module-missing" />
    <template v-else-if="page">
      <header class="space-y-1">
        <h2 class="text-base font-semibold" data-testid="module-page-title">{{ page.module.title }}</h2>
        <div class="font-mono text-[11px] text-muted-foreground">{{ page.module.path }} · 负责人：{{ page.owner_label || '未指定' }}</div>
        <p v-if="page.module.description" class="text-[12px] text-muted-foreground">{{ page.module.description }}</p>
        <div v-if="page.children.length" class="flex flex-wrap gap-1.5 pt-1">
          <button v-for="c in page.children" :key="c.path" type="button" class="cursor-pointer rounded-full border border-border/70 px-2.5 py-0.5 text-[11px] hover:bg-accent/60" @click="emit('open-module', c.path)">{{ c.title }}</button>
        </div>
      </header>

      <section class="grid grid-cols-1 gap-2 sm:grid-cols-2">
        <button v-if="page.index" type="button" class="cursor-pointer rounded-md border border-border/70 bg-card p-3 text-left hover:bg-accent/40" data-testid="module-index" @click="open(page.index!)">
          <div class="text-[11px] text-muted-foreground">总述</div>
          <div class="truncate text-[13px] font-medium">{{ page.index.title }}</div>
        </button>
        <div v-else class="rounded-md border border-dashed border-border/70 p-3 text-[12px] text-muted-foreground">还没有总述（modules/{{ page.module.path }}/index.md）</div>
        <button v-if="page.design" type="button" class="cursor-pointer rounded-md border border-border/70 bg-card p-3 text-left hover:bg-accent/40" data-testid="module-design" @click="open(page.design!)">
          <div class="text-[11px] text-muted-foreground">设计</div>
          <div class="truncate text-[13px] font-medium">{{ page.design.title }}</div>
        </button>
        <div v-else class="rounded-md border border-dashed border-border/70 p-3 text-[12px] text-muted-foreground">还没有设计（modules/{{ page.module.path }}/design.md）</div>
      </section>

      <section v-if="page.decisions.length" class="space-y-1" data-testid="module-decisions">
        <h3 class="text-[12px] font-semibold">决策</h3>
        <button v-for="d in page.decisions" :key="d.id" type="button" class="block w-full cursor-pointer truncate rounded-md border border-border/60 px-2.5 py-1.5 text-left text-[12px] hover:bg-accent/40" @click="open(d)">{{ d.title }} <span class="font-mono text-[10px] text-muted-foreground">{{ d.path }}</span></button>
      </section>

      <section v-if="page.artifacts.length" class="space-y-1" data-testid="module-artifacts">
        <h3 class="text-[12px] font-semibold">图与页面</h3>
        <button v-for="d in page.artifacts" :key="d.id" type="button" class="flex w-full cursor-pointer items-center gap-2 truncate rounded-md border border-border/60 px-2.5 py-1.5 text-left text-[12px] hover:bg-accent/40" @click="open(d)">
          <span class="rounded bg-primary/10 px-1 text-[10px] text-primary">{{ renderLabel(d.render) }}</span>{{ d.title }}
        </button>
      </section>

      <section v-if="page.docs.length" class="space-y-1" data-testid="module-docs">
        <h3 class="text-[12px] font-semibold">其他文档</h3>
        <button v-for="d in page.docs" :key="d.id" type="button" class="block w-full cursor-pointer truncate rounded-md border border-border/60 px-2.5 py-1.5 text-left text-[12px] hover:bg-accent/40" @click="open(d)">
          {{ d.title }} <span class="text-[10px] text-muted-foreground">{{ DOC_KIND_LABEL[d.kind] ?? d.kind }} · {{ d.path }}</span>
        </button>
      </section>

      <section class="space-y-1" data-testid="module-tasks">
        <h3 class="text-[12px] font-semibold">相关任务（按名称与任务看板对齐）</h3>
        <div v-if="!page.tasks.length" class="text-[11px] text-muted-foreground">任务看板里没有同名模块的任务。</div>
        <router-link v-for="t in page.tasks" :key="t.workflow_id + t.seq" :to="{ name: 'work', params: { workflowId: t.workflow_id } }" class="flex items-center gap-2 rounded-md border border-border/60 px-2.5 py-1.5 text-[12px] hover:bg-accent/40">
          <span class="rounded bg-muted px-1.5 text-[10px]">{{ STATE_TEXT[t.state] ?? t.state }}</span>
          <span class="min-w-0 flex-1 truncate">#{{ t.seq }} {{ t.title }}</span>
        </router-link>
      </section>

      <section v-if="page.recent.length" class="space-y-1" data-testid="module-recent">
        <h3 class="text-[12px] font-semibold">最近修订</h3>
        <ul class="space-y-0.5 text-[11px] text-muted-foreground">
          <li v-for="d in page.recent" :key="d.id" class="truncate">{{ new Date(d.updated_at).toLocaleString('zh-CN', { hour12: false }) }} · {{ d.title }}</li>
        </ul>
      </section>
    </template>
  </div>
</template>
