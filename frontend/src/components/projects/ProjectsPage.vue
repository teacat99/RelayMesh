<script setup lang="ts">
// 项目页：/projects 是项目列表，/projects/:projectId 是一个项目（知识库 / 模块 / 管理；管理里是 设置 / 迁移 / 授权 / 审计 四个分区，#156 批 3）。
// 与会话页共用同一个壳，左侧栏不变。
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ChevronRight, FolderGit2, Plus } from 'lucide-vue-next'
import { docRefFromParam, paramString } from '../../lib/docNav'
import { aclModeLabel } from '../../lib/projects'
import { DEFAULT_MANAGE_SECTION, resolveProjectTab, type ManageSection, type ProjectTab } from '../../lib/projectTabs'
import { backToSession } from '../../lib/sessionNav'
import { useAgentStateStore } from '../../stores/agentState'
import { useProjectStore } from '../../stores/projects'
import ProjectDocPage from './ProjectDocPage.vue'
import ProjectKnowledgeTab from './ProjectKnowledgeTab.vue'
import ProjectListPanel from './ProjectListPanel.vue'
import ProjectManageTab from './ProjectManageTab.vue'
import ProjectModulePage from './ProjectModulePage.vue'
import ProjectModulesTab from './ProjectModulesTab.vue'
import Button from '../ui/button/Button.vue'
import PageHeader from '../shell/PageHeader.vue'
import PageBackButton from '../shell/PageBackButton.vue'
import PageState from '../shell/PageState.vue'

defineProps<{ sidebarCollapsed?: boolean }>()
const emit = defineEmits<{ (e: 'open-mobile-sidebar'): void; (e: 'expand-sidebar'): void }>()

const route = useRoute()
const router = useRouter()
const store = useProjectStore()
const agents = useAgentStateStore()

// 页签只有三个；设置 / 迁移 / 授权 / 审计 收进「管理」（地址里仍是 ?tab=settings 等旧取值，见 lib/projectTabs）。
const TABS = [
  { id: 'kb', label: '知识库' },
  { id: 'modules', label: '模块' },
  { id: 'manage', label: '管理' },
] as const

const projectId = computed(() => {
  const p = route.params.projectId
  const raw = Array.isArray(p) ? p[0] : p
  const n = Number(raw)
  return Number.isFinite(n) && n > 0 ? n : 0
})
// 单篇文档页（/projects/:项目/docs/:工作流/:文档，#131）：项目壳不变，「知识库」页签高亮，内容区换成这篇文档。
const isDocMode = computed(() => route.name === 'project-doc')
const docWorkflowId = computed(() => paramString(route.params.workflowId as string | string[] | undefined))
const docRef = computed(() => docRefFromParam(route.params.docRef as string | string[] | undefined))
// ?tab= 的取值沿用旧的 kb | modules | module | settings | migration | grants | audit（旧链接不断）：后四个统归「管理」页签并指明分区
const resolved = computed(() => resolveProjectTab(route.query.tab))
const tab = computed<ProjectTab>(() => (isDocMode.value ? 'kb' : resolved.value.tab))
const section = computed<ManageSection>(() => resolved.value.section)
const modulePath = computed(() => String(route.query.m ?? ''))
const project = computed(() => store.detail?.project)

/** 地址里的 ?tab= 取值：知识库是默认（不写），管理页签写分区名（settings / migration / grants / audit）。 */
type TabQuery = 'kb' | 'modules' | 'module' | ManageSection
function go(id: number, t: TabQuery = 'kb', m?: string) {
  void router.push({ name: 'projects', params: { projectId: String(id) }, query: t === 'kb' ? {} : { tab: t, ...(m ? { m } : {}) } })
}
function openTab(id: (typeof TABS)[number]['id']) {
  // 已经在「管理」里时再点它不动（别把正在看的分区重置回设置）
  if (id === 'manage') go(projectId.value, tab.value === 'manage' ? section.value : DEFAULT_MANAGE_SECTION)
  else go(projectId.value, id)
}
function openModule(path: string) {
  go(projectId.value, 'module', path)
}

// 列表页页头的「新建项目」：表单在列表面板里，页头只负责叫它打开（与任务清单 / 群聊的「新建」同在页头右侧）
const listPanel = ref<InstanceType<typeof ProjectListPanel> | null>(null)

watch(
  projectId,
  id => {
    if (id) void store.loadDetail(id)
    else store.clearDetail()
  },
  { immediate: true },
)
onMounted(() => {
  void store.loadList()
  void agents.refreshIdentities().catch(() => {})
})
</script>

<template>
  <div class="flex h-full min-h-0 flex-col bg-background text-foreground" data-testid="projects-page">
    <!-- 页头骨架与任务清单 / 群聊 / 文档页共用（PageHeader） -->
    <PageHeader :sidebar-collapsed="sidebarCollapsed" @open-mobile-sidebar="emit('open-mobile-sidebar')" @expand-sidebar="emit('expand-sidebar')">
      <template #leading>
        <!-- 回会话：从会话页头的「项目」进来时，它与浏览器的返回是同一个去处（与任务清单页的「会话」同一做法）。列表页、项目页、文档页都有。 -->
        <PageBackButton label="会话" title="返回会话" data-testid="project-session-back" @click="backToSession(router)" />
        <!-- 「全部项目」是面包屑（全部项目 › 项目名），不再画返回箭头，免得和上面的「回会话」两个箭头分不清。 -->
        <button v-if="projectId" type="button" class="inline-flex shrink-0 cursor-pointer items-center gap-0.5 rounded-md px-1.5 py-1 text-[12px] text-muted-foreground hover:bg-accent hover:text-accent-foreground" title="回到项目列表" data-testid="project-back" @click="router.push({ name: 'projects' })">
          全部项目<ChevronRight class="size-3.5" aria-hidden="true" />
        </button>
      </template>

      <template v-if="projectId && project">
        <FolderGit2 class="size-4 shrink-0 text-primary" aria-hidden="true" />
        <span class="min-w-0 truncate text-sm font-semibold" data-testid="project-title">{{ project.name }}</span>
        <span class="hidden shrink-0 rounded-full bg-muted px-2 py-0.5 text-[10px] text-muted-foreground sm:inline" data-testid="project-acl-badge">权限：{{ aclModeLabel(project.acl_mode) }}</span>
      </template>
      <template v-else-if="!projectId">
        <FolderGit2 class="size-4 shrink-0 text-primary" aria-hidden="true" />
        <span class="text-sm font-semibold">项目</span>
      </template>

      <template v-if="!projectId" #actions>
        <Button size="sm" class="h-8 shrink-0 gap-1 text-xs" data-testid="project-create-open" @click="listPanel?.startCreate()">
          <Plus class="size-3.5" />新建项目
        </Button>
      </template>
    </PageHeader>

    <ProjectListPanel v-if="!projectId" ref="listPanel" class="min-h-0 flex-1 overflow-y-auto" @open="id => go(id)" />

    <template v-else>
      <!-- 项目不存在时不画页签（点哪个都去不了）；加载中仍画，免得项目出来时页面往下跳一截 -->
      <nav v-if="project || store.detailLoading" class="flex shrink-0 gap-1 overflow-x-auto border-b border-border/60 px-3" role="tablist" aria-label="项目页签" data-testid="project-tabs">
        <button
          v-for="t in TABS"
          :key="t.id"
          type="button"
          role="tab"
          :aria-selected="tab === t.id || (t.id === 'modules' && tab === 'module')"
          class="shrink-0 cursor-pointer border-b-2 px-3 py-2 text-[12px] max-md:min-h-11"
          :class="tab === t.id || (t.id === 'modules' && tab === 'module') ? 'border-primary font-medium text-foreground' : 'border-transparent text-muted-foreground hover:text-foreground'"
          :data-testid="`project-tab-${t.id}`"
          @click="openTab(t.id)"
        >
          {{ t.label }}
        </button>
      </nav>
      <PageState v-if="store.detailLoading && !project" kind="loading" title="项目加载中…" data-testid="project-loading" />
      <PageState v-else-if="!project" kind="error" :title="store.error || '没有这个项目'" data-testid="project-missing">
        <template #actions>
          <Button variant="outline" size="sm" class="h-8 text-xs" data-testid="project-missing-back" @click="router.push({ name: 'projects' })">回到项目列表</Button>
        </template>
      </PageState>
      <div v-else class="min-h-0 flex-1 overflow-y-auto">
        <ProjectDocPage v-if="isDocMode" :project-id="projectId" :workflow-id="docWorkflowId" :doc-ref="docRef" />
        <ProjectKnowledgeTab v-else-if="tab === 'kb'" :project-id="projectId" class="h-full" @open-module="openModule" />
        <ProjectModulesTab v-else-if="tab === 'modules'" :project-id="projectId" @open-module="openModule" />
        <ProjectModulePage v-else-if="tab === 'module'" :project-id="projectId" :path="modulePath" @open-module="openModule" />
        <ProjectManageTab v-else-if="tab === 'manage'" :project-id="projectId" :section="section" @select="s => go(projectId, s)" @archived="router.push({ name: 'projects' })" />
      </div>
    </template>
  </div>
</template>
