<script setup lang="ts">
// 老的文档库整页（按工作流）。#131 之后它只服务「还没归入项目」的工作流：属于项目的工作流，
// 文档在项目页里阅读 / 编辑（ProjectDocPage），老地址 /docs/... 由路由守卫替换过去。
// 这里只管外框：页头、目录（搜索 / 筛选 / 变更提示 / 蓝图）与跳转；单篇文档本身是 DocViewer，与项目内的文档页共用。
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useMediaQuery } from '@vueuse/core'
import { BookOpen, Download, FolderGit2 } from 'lucide-vue-next'
import Button from '../ui/button/Button.vue'
import { Sheet, SheetContent, SheetHeader, SheetTitle } from '../ui/sheet'
import DocViewer from './DocViewer.vue'
import DocChangesBanner from './DocChangesBanner.vue'
import DocTreeNav from './DocTreeNav.vue'
import DocsSidebarFilters from './DocsSidebarFilters.vue'
import { useProjectDocStore } from '@/stores/projectDocs'
import { projectDocsApi } from '@/api/projectDocs'
import { docRefFromParam, paramString } from '@/lib/docNav'
import { openDoc as navigateToDoc, ownerProjects } from '@/lib/docNavClient'
import { changedSince, latestUpdate, nextCursor, readSeenCursor, writeSeenCursor, type SeenItem } from '@/lib/docSeen'
import { toast } from 'vue-sonner'
import PageHeader from '../shell/PageHeader.vue'
import PageBackButton from '../shell/PageBackButton.vue'
import PageState from '../shell/PageState.vue'

defineProps<{ sidebarCollapsed?: boolean }>()
const emit = defineEmits<{ (e: 'open-mobile-sidebar'): void; (e: 'expand-sidebar'): void }>()

const store = useProjectDocStore()
const route = useRoute()
const router = useRouter()
const isNarrow = useMediaQuery('(max-width: 1023px)')
const drawerOpen = ref(false)

const workflowId = computed(() => paramString(route.params.workflowId as string | string[] | undefined))
const docRef = computed(() => docRefFromParam(route.params.docRef as string | string[] | undefined))

// 只有「确认没有归属项目」才提示（接口查询失败不算：不能把故障说成「还没归入项目」）
const noProject = ref(false)
watch(
  workflowId,
  async id => {
    noProject.value = false
    if (!id) return
    await ownerProjects.resolve(id)
    if (workflowId.value === id) noProject.value = ownerProjects.peek(id) === 0
  },
  { immediate: true },
)

const searchQ = ref('')
const kindFilter = ref('')
const statusFilter = ref('')
const tagFilter = ref('')
const blueprintGaps = ref(0)
const searchHits = ref<{ path: string; title: string; snippet: string; anchor?: string }[]>([])
const searchMode = ref(false)

const allTags = computed(() => {
  const s = new Set<string>()
  for (const d of store.items) for (const t of d.tags || []) s.add(t)
  return [...s].sort()
})

const filteredListParams = computed(() => ({
  q: searchMode.value ? undefined : searchQ.value || undefined,
  kind: kindFilter.value || undefined,
  status: statusFilter.value || undefined,
  tag: tagFilter.value || undefined,
}))

async function refreshList() {
  if (!workflowId.value) return
  await store.loadList(workflowId.value, filteredListParams.value as Record<string, string>)
  const bp = await projectDocsApi.blueprint(workflowId.value)
  blueprintGaps.value = bp.gaps
}

async function runSearch() {
  if (!workflowId.value) return
  const q = searchQ.value.trim()
  if (!q) {
    searchMode.value = false
    searchHits.value = []
    await refreshList()
    return
  }
  searchMode.value = true
  const res = await projectDocsApi.search(workflowId.value, q)
  searchHits.value = res.hits
  await store.loadList(workflowId.value, {
    kind: kindFilter.value || undefined,
    status: statusFilter.value || undefined,
    tag: tagFilter.value || undefined,
  } as Record<string, string>)
}

watch(workflowId, id => {
  if (id) void refreshList()
}, { immediate: true })

// 自上次查看后的变更：游标按工作流存在这台浏览器里；本次访问期间冻结，离开时才前进。
const seenCursor = ref('')
const allDocs = ref<SeenItem[]>([])
const seenPaths = ref<Set<string>>(new Set())
const changedDocs = computed(() => changedSince(allDocs.value, seenCursor.value, seenPaths.value))
let seenFor = ''

function persistCursor() {
  if (!seenFor) return
  const known = [...allDocs.value, ...store.items.filter(d => d.workflow_id === seenFor)]
  writeSeenCursor(localStorage, seenFor, nextCursor(readSeenCursor(localStorage, seenFor), known))
}

async function loadSeen(id: string) {
  persistCursor()
  seenFor = id
  seenPaths.value = new Set()
  allDocs.value = []
  seenCursor.value = id ? readSeenCursor(localStorage, id) : ''
  if (!id) return
  try {
    const res = await projectDocsApi.list(id)
    if (seenFor !== id) return
    allDocs.value = res.items
    if (!seenCursor.value) writeSeenCursor(localStorage, id, latestUpdate(res.items)) // 第一次来：不把全部文档算成新的
  } catch {
    /* 取不到就不提示变更 */
  }
}

function dismissChanges() {
  seenPaths.value = new Set(changedDocs.value.map(d => d.path).concat([...seenPaths.value]))
}

function openChanged(path: string) {
  seenPaths.value = new Set([...seenPaths.value, path])
  go(workflowId.value, path)
}

watch(workflowId, id => void loadSeen(id), { immediate: true })
watch(docRef, p => {
  if (p) seenPaths.value = new Set([...seenPaths.value, p])
})

/** 跳到一篇文档（本工作流或别的工作流）：统一走 openDoc，属于项目的会进项目内的文档页。 */
function go(wf: string, path: string, anchor?: string) {
  void navigateToDoc(router, { workflowId: wf, path, anchor })
  drawerOpen.value = false
}

onBeforeUnmount(() => {
  persistCursor()
  window.removeEventListener('pagehide', persistCursor)
})

function goBack() {
  void router.push({ name: 'home' })
}

async function startBlueprint() {
  const bp = await projectDocsApi.blueprint(workflowId.value, true)
  const first = bp.items.find(i => !i.present && i.template)
  if (!first?.template) {
    toast.message('蓝图文档已齐全')
    return
  }
  await projectDocsApi.create(workflowId.value, { path: first.path, body: first.template, title: first.title })
  await refreshList()
  go(workflowId.value, first.path)
}

function exportZip() {
  window.open(projectDocsApi.exportZipUrl(workflowId.value), '_blank')
}

onMounted(() => {
  window.addEventListener('pagehide', persistCursor)
  if (workflowId.value) void refreshList()
})
</script>

<template>
  <div class="flex h-full min-h-0 flex-col bg-background text-foreground" data-testid="docs-page">
    <!-- 页头骨架与任务清单 / 群聊 / 项目页共用（PageHeader）；侧栏在 PC 上折叠时这里也有「展开侧边栏」了（之前文档页没有） -->
    <PageHeader :sidebar-collapsed="sidebarCollapsed" @open-mobile-sidebar="emit('open-mobile-sidebar')" @expand-sidebar="emit('expand-sidebar')">
      <template #leading>
        <PageBackButton label="返回" always-label data-testid="docs-back" @click="goBack" />
      </template>

      <BookOpen class="size-4 shrink-0 text-primary" aria-hidden="true" />
      <span class="shrink-0 text-sm font-semibold">项目文档</span>
      <span class="min-w-0 truncate font-mono text-xs text-muted-foreground">{{ workflowId }}</span>

      <!-- 不折行：窄屏上「目录 / 导出」折成两行会顶出页头被裁掉（#153 真机档案里量出来的）；标题那几段本来就能截断让位。 -->
      <template #actions>
        <Button
          v-if="isNarrow"
          variant="outline"
          size="sm"
          data-testid="docs-drawer-tree"
          title="文档目录"
          aria-label="打开文档目录"
          @click="drawerOpen = true"
        >
          目录
        </Button>
        <Button
          variant="outline"
          size="sm"
          data-testid="docs-export"
          title="导出全部 zip"
          aria-label="导出全部 zip"
          @click="exportZip"
        >
          <Download class="size-3.5" />
        </Button>
      </template>
    </PageHeader>

    <div
      v-if="noProject"
      class="flex shrink-0 flex-wrap items-center gap-x-2 gap-y-1 border-b border-border/60 bg-primary/5 px-3 py-1.5 text-[11px] text-muted-foreground"
      data-testid="docs-no-project-hint"
    >
      <FolderGit2 class="size-3.5 shrink-0 text-primary" aria-hidden="true" />
      <span>这个工作流还没归入项目。把它的目录归入项目后，文档会汇入项目知识库，在项目页里阅读与编辑。</span>
      <button type="button" class="cursor-pointer text-primary hover:underline" data-testid="docs-goto-projects" @click="router.push({ name: 'projects' })">去项目页 →</button>
    </div>

    <div class="flex min-h-0 flex-1 overflow-hidden">
      <aside
        v-if="!isNarrow"
        class="w-56 shrink-0 border-r border-border/60 flex flex-col overflow-hidden"
        data-testid="docs-sidebar"
      >
        <DocsSidebarFilters
          v-model:search-q="searchQ"
          v-model:kind="kindFilter"
          v-model:status="statusFilter"
          v-model:tag="tagFilter"
          :all-tags="allTags"
          :blueprint-gaps="blueprintGaps"
          :search-hits="searchHits"
          :search-mode="searchMode"
          @search="runSearch"
          @blueprint="startBlueprint"
          @open-hit="(path, anchor) => go(workflowId, path, anchor)"
        />
        <DocChangesBanner v-if="!searchMode" :items="changedDocs" @open="openChanged" @dismiss="dismissChanges" />
        <div v-if="!searchMode" class="flex-1 overflow-y-auto p-2 text-xs">
          <DocTreeNav :items="store.items" :workflow-id="workflowId" :active-path="docRef" @open="path => go(workflowId, path)" />
        </div>
      </aside>

      <main class="min-w-0 flex-1 overflow-y-auto p-4" data-testid="docs-main">
        <PageState v-if="!store.items.length && !store.loading" kind="empty" :icon="BookOpen" title="项目文档库还是空的" data-testid="docs-empty">
          按路径存放设计、决策与运行手册；智能体通过 MCP <code class="rounded-xs bg-muted px-1 font-mono">project_docs</code> 读写。
          <template #actions>
            <Button size="sm" data-testid="docs-empty-blueprint" @click="startBlueprint">用蓝图开始</Button>
          </template>
        </PageState>
        <DocViewer
          v-else-if="docRef"
          :workflow-id="workflowId"
          :doc-ref="docRef"
          @open-doc="go"
          @changed="refreshList"
        />
        <p v-else class="text-sm text-muted-foreground">从左侧选择一篇文档</p>
      </main>
    </div>

    <Sheet v-model:open="drawerOpen">
      <SheetContent side="left" class="w-[85vw] max-w-xs flex flex-col">
        <SheetHeader><SheetTitle>文档目录</SheetTitle></SheetHeader>
        <DocChangesBanner :items="changedDocs" @open="openChanged" @dismiss="dismissChanges" />
        <div class="flex-1 overflow-y-auto mt-2">
          <DocTreeNav :items="store.items" :workflow-id="workflowId" :active-path="docRef" @open="path => go(workflowId, path)" />
        </div>
      </SheetContent>
    </Sheet>
  </div>
</template>
