<script setup lang="ts">
// 项目知识库：左侧模块与目录树，中间搜索结果 / 文档列表（跨工作流），点开文档进入项目内的文档页（用文档所属工作流打开）。
// 「推荐结构」列出项目还缺哪些关键文档；缺口只是提示，不强制。
// 知识库只认已并入项目的文档；还留在工作流里的老文档（迁移预览给出）在列表上方单独提示并可直接打开，并入后才和别的文档一起检索。
import { computed, onMounted, ref, watch } from 'vue'
import { ChevronRight, FileText, Search, SlidersHorizontal } from 'lucide-vue-next'
import { useRouter } from 'vue-router'
import { toast } from 'vue-sonner'
import { projectErrorMessage, projectsApi, type BlueprintItem, type KbDoc, type KbSearchHit, type MigrationItem } from '../../api/projects'
import { DOC_KINDS, DOC_KIND_LABEL, DOC_STATUSES, DOC_STATUS_LABEL } from '../../lib/docMeta'
import { openDoc } from '../../lib/docNavClient'
import { flattenModules, renderLabel, toTreeDocs } from '../../lib/projects'
import { useProjectStore } from '../../stores/projects'
import DocTreeNav from '../docs/DocTreeNav.vue'
import PageState from '../shell/PageState.vue'

const props = defineProps<{ projectId: number }>()
const emit = defineEmits<{ (e: 'open-module', path: string): void }>()
const router = useRouter()
const store = useProjectStore()

const all = ref<KbDoc[]>([])
const hits = ref<KbSearchHit[]>([])
const blueprint = ref<BlueprintItem[]>([])
const q = ref('')
const kind = ref('')
const status = ref('')
const moduleFilter = ref('')
const renderFilter = ref('')
const sinceDays = ref(0) // 0 = 不限；只看最近 N 天更新过的
const loading = ref(false)
// 还留在工作流里、没并入知识库的文档；预览取不到只是少一条提示，不打扰
const pending = ref<MigrationItem[]>([])
const pendingOpen = ref<boolean | null>(null) // null = 没手动切过：知识库还是空的就默认展开
// 迁移预览还在路上：这段时间空状态不能宣称「没有文档」（预览回来后可能是「有 N 篇留在工作流里」）。
// 初始就是「检查中」：挂载后马上会去取预览，首帧也不能先闪一下「还没有文档」。
const pendingChecking = ref(true)

const modules = computed(() => flattenModules(store.detail?.modules ?? []))
const searching = computed(() => q.value.trim().length > 0 || !!kind.value || !!status.value || !!moduleFilter.value || !!renderFilter.value || sinceDays.value > 0)
// 手机宽度的折叠（#155）：模块与目录树、五个筛选下拉平时收起，首屏留给搜索框和文档列表；桌面（≥md）仍是常驻的左栏与下拉网格。
const treeOpen = ref(false)
const filtersOpen = ref(false)
const activeFilters = computed(() => [kind.value, status.value, moduleFilter.value, renderFilter.value].filter(Boolean).length + (sinceDays.value > 0 ? 1 : 0))
const gaps = computed(() => blueprint.value.filter(b => !b.present))
const treeDocs = computed(() => toTreeDocs(all.value))
const byPath = computed(() => new Map(all.value.map(d => [d.path, d])))

const rows = computed(() =>
  searching.value
    ? hits.value
    : [...all.value].sort((a, b) => b.updated_at.localeCompare(a.updated_at)).slice(0, 50).map(d => ({
        path: d.path, title: d.title, score: 0, snippet: '', doc_id: d.id, workflow_id: d.workflow_id,
        module_path: d.module_path, kind: d.kind, status: d.status, render: d.render, updated_at: d.updated_at,
      } as KbSearchHit)),
)

async function loadAll() {
  loading.value = true
  // 组件在项目之间复用：上一个项目的待迁移提示不能留到新项目的预览回来之前
  pending.value = []
  pendingOpen.value = null
  pendingChecking.value = true
  try {
    const [docs, bp] = await Promise.all([projectsApi.docs(props.projectId), projectsApi.blueprint(props.projectId)])
    all.value = docs
    blueprint.value = bp.items
  } catch (e) {
    toast.error(projectErrorMessage(e, '知识库加载失败'))
  } finally {
    loading.value = false
  }
  void loadPending()
}

async function loadPending() {
  const id = props.projectId
  pendingChecking.value = true
  try {
    const rep = await projectsApi.migrationDryRun(id)
    if (id === props.projectId) pending.value = rep.items ?? []
  } catch {
    if (id === props.projectId) pending.value = []
  } finally {
    if (id === props.projectId) pendingChecking.value = false
  }
}
const pendingShown = computed(() => pendingOpen.value ?? all.value.length === 0)
const emptyText = computed(() => {
  if (searching.value) return '没有符合条件的文档。'
  if (pendingChecking.value) return '正在检查是否还有文档留在工作流里…'
  if (pending.value.length) return `知识库里还没有文档；有 ${pending.value.length} 篇留在工作流里的文档等待并入（见上方提示）。`
  return '项目知识库还没有文档。把目录归入项目后，在「管理 › 迁移」里并入已有文档，或让 Agent 用 scope=project 写入。'
})

let seq = 0
async function runSearch() {
  if (!searching.value) {
    hits.value = []
    return
  }
  const mine = ++seq
  try {
    const res = await projectsApi.search(props.projectId, {
      q: q.value.trim() || undefined, kind: kind.value || undefined, status: status.value || undefined,
      module: moduleFilter.value || undefined, render: renderFilter.value || undefined,
      since: sinceDays.value > 0 ? new Date(Date.now() - sinceDays.value * 86400_000).toISOString() : undefined, limit: 50,
    })
    if (mine === seq) hits.value = res
  } catch (e) {
    if (mine === seq) toast.error(projectErrorMessage(e, '搜索失败'))
  }
}

let timer: ReturnType<typeof setTimeout> | null = null
watch([q, kind, status, moduleFilter, renderFilter, sinceDays], () => {
  if (timer) clearTimeout(timer)
  timer = setTimeout(() => void runSearch(), 250)
})
watch(() => props.projectId, () => void loadAll())
onMounted(() => void loadAll())

// 整个项目的文档打成一个压缩包（含已归档）
async function downloadZip() {
  try {
    const blob = await projectsApi.exportZip(props.projectId)
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `project-${props.projectId}-docs.zip`
    a.click()
    URL.revokeObjectURL(url)
  } catch (e) {
    toast.error(projectErrorMessage(e, '打包下载失败'))
  }
}

function open(workflowId: string, path: string) {
  void openDoc(router, { workflowId, path, projectId: props.projectId })
}
function goMigration() {
  void router.push({ name: 'projects', params: { projectId: String(props.projectId) }, query: { tab: 'migration' } })
}
function openFromTree(path: string) {
  const d = byPath.value.get(path)
  if (d) open(d.workflow_id, path)
}
</script>

<template>
  <!-- 手机宽度显式分两行：上面「模块与目录」只占它那一行，下面的列表拿走剩下的高度并在里面滚动（隐式的自动行会把剩余高度平分给上面那一行，留出一块空白，#155）。
       中间一列写成 minmax(0,1fr)：写 1fr 时它的最小宽度是内容宽度，文档标题 / 路径一长就把右栏挤出视口、主区出横向滚动条（#155 巡检在 1280 / 1440 宽的生产页上量到）；
       右栏「推荐结构的缺口」从 xl（≥1280）才出现：左侧栏占了 320px，lg（1024）宽时主区只剩 704px，三列放不下 -->
  <div class="grid h-full min-h-0 grid-cols-1 max-md:grid-rows-[auto_minmax(0,1fr)] md:grid-cols-[16rem_minmax(0,1fr)] xl:grid-cols-[16rem_minmax(0,1fr)_16rem]" data-testid="project-kb">
    <aside class="min-h-0 border-b border-border/60 md:overflow-y-auto md:border-b-0 md:border-r" data-testid="kb-tree">
      <!-- 手机宽度：「模块与目录」收成一行，点开才展开；桌面（≥md）这一行不出现，左栏照常常驻 -->
      <button
        type="button"
        class="flex h-11 w-full cursor-pointer items-center gap-1.5 px-3 text-left text-[12px] font-semibold text-muted-foreground md:hidden"
        :aria-expanded="treeOpen"
        data-testid="kb-tree-toggle"
        @click="treeOpen = !treeOpen"
      >
        <ChevronRight class="size-3.5 shrink-0 transition-transform" :class="treeOpen && 'rotate-90'" aria-hidden="true" />
        <span>模块与目录（{{ all.length }} 篇）</span>
      </button>
      <div class="p-3 max-md:max-h-[45vh] max-md:overflow-y-auto max-md:pt-0" :class="treeOpen ? '' : 'max-md:hidden'">
        <div class="mb-1 text-[11px] font-semibold text-muted-foreground">模块</div>
        <ul class="mb-3 space-y-0.5">
          <li v-if="!modules.length" class="px-1 text-[11px] text-muted-foreground">还没有模块（在「模块」页建立）</li>
          <li v-for="n in modules" :key="n.module.path">
            <button type="button" class="block w-full cursor-pointer truncate rounded-xs px-1 py-0.5 text-left text-[12px] hover:bg-muted/80" :style="{ paddingLeft: 4 + n.depth * 10 + 'px' }" :data-testid="`kb-module-${n.module.path}`" @click="emit('open-module', n.module.path)">
              {{ n.module.title }}
            </button>
          </li>
        </ul>
        <div class="mb-1 flex items-center text-[11px] font-semibold text-muted-foreground">
          <span>目录（{{ all.length }} 篇）</span>
          <button type="button" class="ml-auto cursor-pointer font-normal hover:text-foreground" title="把整个项目的文档打成压缩包" data-testid="kb-zip" @click="downloadZip">打包下载</button>
        </div>
        <DocTreeNav :items="treeDocs" :workflow-id="`project:${projectId}`" active-path="" @open="openFromTree" />
      </div>
    </aside>

    <section class="flex min-h-0 flex-col" data-testid="kb-list">
      <div class="space-y-2 border-b border-border/60 p-3">
        <div class="flex items-center gap-2">
          <label class="relative block min-w-0 flex-1">
            <Search class="pointer-events-none absolute left-2 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" aria-hidden="true" />
            <input v-model="q" placeholder="搜索整个项目的文档（跨工作流）" class="h-8 w-full rounded-md border border-input bg-background pl-7 pr-2 text-[12px] outline-none focus-visible:ring-1 focus-visible:ring-ring" data-testid="kb-search" />
          </label>
          <!-- 手机宽度：五个筛选下拉收成这一个按钮（右上角的数字是已选条数），点开才展开；桌面（≥md）不出现，下拉网格常驻 -->
          <button
            type="button"
            class="relative inline-flex h-8 shrink-0 cursor-pointer items-center gap-1 rounded-md border border-input bg-background px-2.5 text-[12px] hover:bg-accent hover:text-accent-foreground max-md:h-11 md:hidden"
            :class="filtersOpen || activeFilters ? 'border-primary/60 text-primary' : ''"
            :aria-expanded="filtersOpen"
            data-testid="kb-filter-toggle"
            @click="filtersOpen = !filtersOpen"
          >
            <SlidersHorizontal class="size-3.5" aria-hidden="true" />
            <span>筛选</span>
            <span v-if="activeFilters" class="rounded-full bg-primary px-1.5 text-[11px] leading-4 text-primary-foreground" data-testid="kb-filter-count">{{ activeFilters }}</span>
          </button>
        </div>
        <div class="grid grid-cols-2 gap-2 lg:grid-cols-3" :class="filtersOpen ? '' : 'max-md:hidden'" data-testid="kb-filters">
          <select v-model="kind" class="h-8 min-w-0 rounded-md border border-input bg-background px-1.5 text-[12px]" data-testid="kb-kind">
            <option value="">全部类型</option>
            <option v-for="k in DOC_KINDS" :key="k" :value="k">{{ DOC_KIND_LABEL[k] }}</option>
          </select>
          <select v-model="status" class="h-8 min-w-0 rounded-md border border-input bg-background px-1.5 text-[12px]">
            <option value="">全部状态</option>
            <option v-for="s in DOC_STATUSES" :key="s" :value="s">{{ DOC_STATUS_LABEL[s] }}</option>
          </select>
          <select v-model="moduleFilter" class="h-8 min-w-0 rounded-md border border-input bg-background px-1.5 text-[12px]" data-testid="kb-module-filter">
            <option value="">全部模块</option>
            <option v-for="n in modules" :key="n.module.path" :value="n.module.path">{{ n.module.title }}</option>
          </select>
          <select v-model="renderFilter" class="h-8 min-w-0 rounded-md border border-input bg-background px-1.5 text-[12px]" data-testid="kb-render-filter">
            <option value="">全部形式</option>
            <option value="md">Markdown</option>
            <option value="html">Artifact（HTML）</option>
          </select>
          <select v-model.number="sinceDays" class="h-8 min-w-0 rounded-md border border-input bg-background px-1.5 text-[12px]" data-testid="kb-since-filter">
            <option :value="0">不限时间</option>
            <option :value="1">近 1 天更新</option>
            <option :value="7">近 7 天更新</option>
            <option :value="30">近 30 天更新</option>
          </select>
        </div>
      </div>
      <div class="min-h-0 flex-1 overflow-y-auto p-3">
        <div v-if="pending.length" class="mb-3 rounded-md border border-amber-500/40 bg-amber-500/10 p-2.5 text-[12px]" data-testid="kb-pending">
          <div class="flex flex-wrap items-center gap-x-2 gap-y-1">
            <span class="font-medium text-amber-800 dark:text-amber-300" data-testid="kb-pending-summary">还有 {{ pending.length }} 篇文档留在工作流里，没有并入知识库</span>
            <button type="button" class="cursor-pointer text-primary hover:underline" data-testid="kb-pending-toggle" @click="pendingOpen = !pendingShown">{{ pendingShown ? '收起' : '查看' }}</button>
            <button type="button" class="ml-auto cursor-pointer rounded-md border border-border/70 bg-background px-2 py-0.5 hover:bg-accent hover:text-accent-foreground" data-testid="kb-pending-migrate" @click="goMigration">预览迁移</button>
          </div>
          <p class="mt-1 text-[11px] text-muted-foreground">它们现在不出现在下面的列表、搜索和模块里，并入后才和其他文档一起展示；在这之前可以直接点开阅读与编辑。</p>
          <ul v-if="pendingShown" class="mt-2 space-y-1" data-testid="kb-pending-list">
            <li v-for="it in pending" :key="it.doc_id">
              <button type="button" class="flex w-full cursor-pointer items-start gap-2 rounded-md border border-border/60 bg-background/70 px-2.5 py-1.5 text-left hover:bg-accent/40" :data-testid="`kb-pending-${it.from_path}`" @click="open(it.workflow_id, it.from_path)">
                <FileText class="mt-0.5 size-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
                <span class="min-w-0 flex-1">
                  <span class="block truncate text-[13px] font-medium">{{ it.title || it.from_path }}</span>
                  <span class="block truncate font-mono text-[10px] text-muted-foreground">{{ it.from_path }}</span>
                </span>
                <span class="max-w-[40%] shrink-0 truncate font-mono text-[10px] text-muted-foreground" :title="`工作流 ${it.workflow_id}`">{{ it.workflow_id }}</span>
              </button>
            </li>
          </ul>
        </div>
        <div class="mb-1.5 text-[11px] text-muted-foreground">{{ searching ? `找到 ${rows.length} 篇` : '最近更新' }}</div>
        <PageState v-if="loading && !all.length" kind="loading" size="sm" title="知识库加载中…" data-testid="kb-loading" />
        <ul v-else class="space-y-1" data-testid="kb-rows">
          <PageState v-if="!rows.length" as="li" kind="empty" size="sm" bordered :title="emptyText" data-testid="kb-empty" />
          <li v-for="r in rows" :key="r.doc_id">
            <button type="button" class="flex w-full cursor-pointer items-start gap-2 rounded-md border border-border/60 px-2.5 py-1.5 text-left hover:bg-accent/40" :data-testid="`kb-row-${r.path}`" @click="open(r.workflow_id, r.path)">
              <FileText class="mt-0.5 size-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
              <span class="min-w-0 flex-1">
                <span class="block truncate text-[13px] font-medium">{{ r.title }}</span>
                <span class="block truncate font-mono text-[10px] text-muted-foreground">{{ r.path }}</span>
                <span v-if="r.snippet" class="mt-0.5 line-clamp-2 block text-[11px] text-muted-foreground">{{ r.snippet }}</span>
              </span>
              <span class="shrink-0 space-x-1 text-[10px] text-muted-foreground">
                <span>{{ DOC_KIND_LABEL[r.kind] ?? r.kind }}</span>
                <span v-if="r.render && r.render !== 'md'" class="rounded bg-primary/10 px-1 text-primary">{{ renderLabel(r.render) }}</span>
              </span>
            </button>
          </li>
        </ul>
      </div>
    </section>

    <aside class="hidden min-h-0 overflow-y-auto border-l border-border/60 p-3 xl:block" data-testid="kb-blueprint">
      <div class="mb-1 text-[11px] font-semibold text-muted-foreground">推荐结构的缺口（{{ gaps.length }}）</div>
      <ul class="space-y-1.5">
        <li v-if="!gaps.length" class="text-[11px] text-muted-foreground">推荐的关键文档都有了。</li>
        <li v-for="b in gaps" :key="b.path" class="rounded border border-border/50 px-2 py-1">
          <div class="truncate font-mono text-[10px]">{{ b.path }}</div>
          <div class="text-[11px] text-muted-foreground">{{ b.hint }}</div>
        </li>
      </ul>
    </aside>
  </div>
</template>
