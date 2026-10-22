<script setup lang="ts">
// 项目列表：已建的项目 + 「可归入的目录」（还没归属任何项目、但已有工作流与文档的目录，原来叫「项目建议」）。
// 项目只有你能建：Agent 不能自建。可归入的目录勾选后可以合并成一个项目（比如同一仓库的几个工作树）。
// #156 批 3：项目卡片置顶；「新建项目」按钮挪到页头右侧（与任务清单 / 群聊同位，由 ProjectsPage 调 startCreate）；
// 目录建议折叠成一行「发现 N 个可归入的目录」，点开才列出；说明文字挪到列表下面（空列表时它在空态里也出现一遍）。
import { computed, ref } from 'vue'
import { ChevronDown, Sparkles } from 'lucide-vue-next'
import { toast } from 'vue-sonner'
import { projectErrorMessage, projectsApi, type ProjectSuggestion } from '../../api/projects'
import { aclModeLabel } from '../../lib/projects'
import { useProjectStore } from '../../stores/projects'
import Button from '../ui/button/Button.vue'
import PageState from '../shell/PageState.vue'

const emit = defineEmits<{ (e: 'open', id: number): void }>()
const store = useProjectStore()

const creating = ref(false)
const name = ref('')
const picked = ref<Set<string>>(new Set())
const busy = ref(false)
// 目录建议默认收起；开「新建项目」表单时自动展开（勾选目录就是新建流程的一部分）
const suggestionsOpen = ref(false)

const keyOf = (s: ProjectSuggestion) => `${s.host_name}\u0000${s.project_directory}`
const pickedSuggestions = computed(() => store.suggestions.filter(s => picked.value.has(keyOf(s))))
const canCreate = computed(() => !busy.value && name.value.trim().length > 0)
const createHint = computed(() => {
  if (!store.suggestions.length) return '项目建好后，在「管理 › 设置」里添加它的目录。'
  if (pickedSuggestions.value.length) return `下面「可归入的目录」里勾选的目录会一并归入（已选 ${pickedSuggestions.value.length} 个）。`
  return '下面「可归入的目录」里勾选的目录会一并归入；也可以先建空项目，之后在「管理 › 设置」里添加目录。'
})

function toggle(s: ProjectSuggestion) {
  const next = new Set(picked.value)
  const k = keyOf(s)
  if (next.has(k)) next.delete(k)
  else next.add(k)
  picked.value = next
  if (!name.value.trim() && next.size === 1) name.value = s.suggested_name
}

function startCreate() {
  creating.value = true
  name.value = ''
  picked.value = new Set()
  suggestionsOpen.value = true
}
defineExpose({ startCreate })

async function create() {
  if (!canCreate.value) return
  busy.value = true
  try {
    const res = await projectsApi.create({
      name: name.value.trim(),
      dirs: pickedSuggestions.value.map(s => ({ host_name: s.host_name, project_directory: s.project_directory })),
    })
    if (res.dir_errors?.length) toast.warning(`有目录没能归入：${res.dir_errors.join('；')}`)
    creating.value = false
    await store.loadList()
    emit('open', res.project.id)
  } catch (e) {
    toast.error(projectErrorMessage(e, '创建失败'))
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="mx-auto w-full max-w-3xl space-y-4 p-4" data-testid="project-list">
    <div v-if="creating" class="space-y-2 rounded-md border border-border/70 bg-card p-3" data-testid="project-create-form">
      <input
        v-model="name"
        maxlength="60"
        placeholder="项目名，例如「RelayMesh」"
        class="h-9 w-full rounded-md border border-input bg-background px-2.5 text-[13px] outline-none focus-visible:ring-1 focus-visible:ring-ring"
        data-testid="project-create-name"
        @keydown.enter="create"
      />
      <div class="text-[11px] text-muted-foreground" data-testid="project-create-hint">{{ createHint }}</div>
      <div class="flex justify-end gap-2">
        <button type="button" class="h-8 cursor-pointer rounded-md border border-border px-3 text-[12px] hover:bg-accent" @click="creating = false">取消</button>
        <button
          type="button"
          class="h-8 cursor-pointer rounded-md bg-primary px-3 text-[12px] text-primary-foreground disabled:cursor-not-allowed disabled:opacity-40"
          :disabled="!canCreate"
          data-testid="project-create-submit"
          @click="create"
        >
          创建
        </button>
      </div>
    </div>

    <section>
      <PageState v-if="store.loading && !store.loaded" kind="loading" size="sm" title="项目加载中…" data-testid="project-loading" />
      <PageState v-else-if="store.error && !store.list.length" kind="error" size="sm" title="项目列表加载失败" :hint="store.error" data-testid="project-list-error">
        <template #actions>
          <Button variant="outline" size="sm" class="h-8 text-xs" data-testid="project-list-retry" @click="store.loadList()">重试</Button>
        </template>
      </PageState>
      <ul v-else class="space-y-1.5" data-testid="project-items">
        <PageState v-if="!store.list.length" as="li" kind="empty" size="sm" bordered title="还没有项目" data-testid="project-empty">
          {{ store.suggestions.length ? '点开下面的「可归入的目录」挑几个建一个，或点右上角「新建项目」先建空项目。' : '点右上角「新建项目」先建一个空项目。' }}
        </PageState>
        <li v-for="p in store.list" :key="p.id">
          <button
            type="button"
            class="flex w-full cursor-pointer items-start gap-3 rounded-md border border-border/70 bg-card px-3 py-2.5 text-left hover:bg-accent/40"
            :data-testid="`project-item-${p.slug}`"
            @click="emit('open', p.id)"
          >
            <span class="min-w-0 flex-1">
              <span class="block truncate text-[13px] font-medium">{{ p.name }}</span>
              <span class="block truncate font-mono text-[10px] text-muted-foreground">{{ p.slug }}</span>
              <span v-if="p.stats" class="mt-1 block text-[11px] text-muted-foreground">
                {{ p.stats.dirs }} 个目录 · {{ p.stats.modules }} 个模块 · {{ p.stats.docs }} 篇文档 · {{ p.stats.workflows }} 个工作流
              </span>
            </span>
            <span class="shrink-0 rounded-full bg-muted px-2 py-0.5 text-[10px] text-muted-foreground" :data-acl="p.acl_mode">权限：{{ aclModeLabel(p.acl_mode) }}</span>
          </button>
        </li>
      </ul>
    </section>

    <section v-if="store.suggestions.length" class="space-y-2" data-testid="project-suggestions">
      <button
        type="button"
        class="flex w-full cursor-pointer items-center gap-2 rounded-md border border-amber-500/30 bg-amber-500/5 px-3 py-2 text-left text-[12px] hover:bg-amber-500/10"
        :aria-expanded="suggestionsOpen"
        aria-controls="project-suggestion-items"
        data-testid="project-suggestions-toggle"
        @click="suggestionsOpen = !suggestionsOpen"
      >
        <Sparkles class="size-4 shrink-0 text-amber-500" aria-hidden="true" />
        <span class="min-w-0 flex-1">
          <span class="font-medium">发现 {{ store.suggestions.length }} 个可归入的目录</span>
          <span class="ml-1.5 hidden text-muted-foreground sm:inline">已有工作流与文档、但还没归属项目</span>
        </span>
        <span v-if="picked.size" class="shrink-0 rounded-full bg-primary/10 px-2 py-0.5 text-[10px] text-primary" data-testid="project-suggestions-picked">已选 {{ picked.size }}</span>
        <ChevronDown class="size-4 shrink-0 text-muted-foreground transition-transform" :class="suggestionsOpen ? 'rotate-180' : ''" aria-hidden="true" />
      </button>
      <div v-if="suggestionsOpen" id="project-suggestion-items" class="space-y-2" data-testid="project-suggestion-items">
        <ul class="space-y-1">
          <li v-for="s in store.suggestions" :key="keyOf(s)">
            <label class="flex cursor-pointer items-start gap-2 rounded-md border border-border/60 px-2.5 py-2 text-[12px] hover:bg-accent/40">
              <input type="checkbox" class="mt-0.5 size-3.5" :checked="picked.has(keyOf(s))" :data-testid="`suggestion-${s.project_directory}`" @change="toggle(s)" />
              <span class="min-w-0 flex-1">
                <span class="block truncate font-medium">{{ s.suggested_name }}</span>
                <span class="block truncate font-mono text-[10px] text-muted-foreground">{{ s.host_name }}:{{ s.project_directory }}</span>
                <span class="block text-[11px] text-muted-foreground">{{ s.workflows.length }} 个工作流 · {{ s.docs }} 篇文档</span>
              </span>
            </label>
          </li>
        </ul>
        <p class="text-[11px] text-muted-foreground" data-testid="project-suggestions-note">
          {{ creating ? '勾选后点「创建」即可把这些目录归入新项目' : '勾选后点页头的「新建项目」即可把这些目录归入同一个项目' }}；同一仓库的几个工作树可以一起勾。
        </p>
      </div>
    </section>

    <p class="text-[11px] leading-relaxed text-muted-foreground" data-testid="project-intro">
      项目把同一个产品的多个目录（工作树、分支）和它们的文档合成一个知识库。项目只有你能建；Agent 只能读写已有项目里属于它们的部分。
    </p>
  </div>
</template>
