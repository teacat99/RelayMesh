<script setup lang="ts">
// 授权：你对任意范围授权给 Agent 身份（只读 / 可评论 / 可编辑），并可随时撤销。
// 管理权不能转授；负责人 Agent 授予的授权也会出现在这里，你可以撤销任何一条。
// 「任务管理」（#175 B2c）：勾选哪些 Agent 是项目管理 Agent——能派任务、改所有人的任务、验收交付。默认没有人，只有你能勾。
import { computed, onMounted, ref } from 'vue'
import { toast } from 'vue-sonner'
import { projectErrorMessage, projectsApi, type AclGrant, type GrantRole, type GrantScopeKind } from '../../api/projects'
import { groupsApi, type GroupSummary } from '../../api/groups'
import { aclModeLabel, grantActive, grantExpiryLabel, grantRoleLabel, grantScopeLabel, taskManagerCandidates, taskManagerModeNote, taskManagerView } from '../../lib/projects'
import { useProjectStore } from '../../stores/projects'
import PageState from '../shell/PageState.vue'

const props = defineProps<{ projectId: number }>()
const store = useProjectStore()

const grants = ref<AclGrant[]>([])
const showAll = ref(false)
const loading = ref(false)
const busy = ref(false)

const principal = ref('')
const scopeKind = ref<GrantScopeKind>('module')
const scopeRef = ref('')
const role = ref<GrantRole>('read')
const hours = ref(0)
const note = ref('')

const groups = ref<GroupSummary[]>([])

const modules = computed(() => store.detail?.modules ?? [])

// 任务管理：目录归在本项目里的 Agent 逐个列出，勾选 = 授予 tasks 范围的编辑授权，取消 = 撤销它。
const managerBusy = ref<number | null>(null)
const aclMode = computed(() => store.detail?.project.acl_mode ?? 'off')
const managerRows = computed(() =>
  taskManagerCandidates(store.identities, store.detail?.dirs ?? [], grants.value).map(i => ({ identity: i, view: taskManagerView(grants.value, i.id) })),
)

async function toggleManager(id: number, label: string, ev: Event) {
  const box = ev.target as HTMLInputElement
  const on = box.checked
  managerBusy.value = id
  try {
    if (on) {
      await projectsApi.createGrant(props.projectId, { principal_kind: 'identity', principal_ref: String(id), scope_kind: 'tasks', role: 'edit' })
      toast.success(`已指定「${label}」为任务管理 Agent，对方的收件箱会收到通知`)
    } else {
      const gid = taskManagerView(grants.value, id).designatedGrantId
      if (gid !== null) await projectsApi.revokeGrant(props.projectId, gid)
      toast.success(`「${label}」不再是任务管理 Agent`)
    }
    await load()
  } catch (e) {
    box.checked = !on
    toast.error(projectErrorMessage(e, on ? '指定失败' : '取消失败'))
  } finally {
    managerBusy.value = null
  }
}
const shown = computed(() => (showAll.value ? grants.value : grants.value.filter(g => grantActive(g))))
const canCreate = computed(() => {
  if (busy.value || !principal.value) return false
  if (scopeKind.value === 'project') return true
  return scopeRef.value.trim().length > 0
})

async function load() {
  loading.value = true
  try {
    grants.value = await projectsApi.grants(props.projectId, true)
  } catch (e) {
    toast.error(projectErrorMessage(e, '授权列表加载失败'))
  } finally {
    loading.value = false
  }
}

async function create() {
  if (!canCreate.value) return
  busy.value = true
  try {
    await projectsApi.createGrant(props.projectId, {
      principal_kind: principal.value.startsWith('group:') ? 'group' : 'identity',
      principal_ref: principal.value.replace(/^(identity|group):/, ''),
      scope_kind: scopeKind.value,
      scope_ref: scopeKind.value === 'project' ? undefined : scopeRef.value.trim(),
      role: role.value,
      expires_hours: hours.value > 0 ? hours.value : undefined,
      note: note.value.trim() || undefined,
    })
    toast.success('已授权，对方的收件箱会收到提醒')
    scopeRef.value = ''
    note.value = ''
    await load()
  } catch (e) {
    toast.error(projectErrorMessage(e, '授权失败'))
  } finally {
    busy.value = false
  }
}

async function revoke(g: AclGrant) {
  if (!window.confirm('撤销这条授权？对方之后不能再按它访问。')) return
  try {
    await projectsApi.revokeGrant(props.projectId, g.id)
    await load()
  } catch (e) {
    toast.error(projectErrorMessage(e, '撤销失败'))
  }
}

function principalText(g: AclGrant): string {
  if (g.principal_kind === 'identity') return store.identityLabel(Number(g.principal_ref))
  const grp = groups.value.find(x => x.id === g.principal_ref)
  return grp ? `群「${grp.title}」` : `群 ${g.principal_ref}`
}

onMounted(() => {
  void load()
  void groupsApi.list(false).then(l => (groups.value = l)).catch(() => {})
})
</script>

<template>
  <div class="mx-auto w-full max-w-3xl space-y-4 p-4" data-testid="project-grants">
    <h3 class="text-[13px] font-semibold">授权</h3>
    <p class="text-[12px] text-muted-foreground">
      默认：同项目的 Agent 对知识库只读；负责人管理自己的模块与文档；其余写入需要授权。知道某个工作流编号只能只读它的会话数据，不含收件箱。
    </p>

    <section class="space-y-2 rounded-md border border-border/70 bg-card p-3" data-testid="task-managers">
      <div class="flex flex-wrap items-baseline gap-x-2">
        <h4 class="text-[12px] font-semibold">任务管理</h4>
        <span class="text-[11px] text-muted-foreground" data-testid="task-managers-mode">访问控制：{{ aclModeLabel(aclMode) }}。{{ taskManagerModeNote(aclMode) }}</span>
      </div>
      <p class="text-[12px] text-muted-foreground">
        勾选的 Agent 是项目管理 Agent：能把任务派给同一张任务板上的其他工作流、编辑所有人的任务、验收承接者的交付。
        没勾选的 Agent 只能写自己创建或承接的任务，其余任务只读。默认没有人被指定；勾选后对方的收件箱会收到通知，随时可以取消。
      </p>
      <p class="text-[11px] text-muted-foreground" data-testid="task-managers-launcher-note">
        如果这个 Agent 的令牌在「MCP 与安全」里另外开启了「运行预设」，项目管理 Agent 还能运行本项目里已有的「非交互续聊」预设来唤醒子会话
        （只能运行，不能改预设、不能传提示词；有次数与间隔限制，每次都会写入审计）。这一条不受上面访问控制模式的影响。
      </p>
      <PageState v-if="!managerRows.length" kind="empty" size="sm" bordered title="这个项目的目录下还没有 Agent 身份" data-testid="task-managers-empty" />
      <ul v-else class="space-y-1" data-testid="task-manager-list">
        <li
          v-for="r in managerRows"
          :key="r.identity.id"
          class="flex items-center gap-2 rounded-md border border-border/60 px-2.5 py-1.5 text-[12px]"
          :data-testid="`task-manager-${r.identity.id}`"
          :data-manager="r.view.manager ? '1' : '0'"
        >
          <label class="flex min-w-0 flex-1 cursor-pointer items-center gap-2" :class="r.view.inherited && r.view.designatedGrantId === null ? 'cursor-not-allowed' : ''">
            <input
              type="checkbox"
              class="size-3.5"
              :checked="r.view.manager"
              :disabled="managerBusy !== null || (r.view.inherited && r.view.designatedGrantId === null)"
              :data-testid="`task-manager-toggle-${r.identity.id}`"
              @change="toggleManager(r.identity.id, r.identity.label, $event)"
            />
            <span class="truncate"><b>{{ r.identity.label }}</b><span class="text-muted-foreground">（{{ r.identity.project_directory }}）</span></span>
          </label>
          <span v-if="r.view.inherited" class="shrink-0 rounded bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground" :data-testid="`task-manager-inherited-${r.identity.id}`">
            {{ r.view.designatedGrantId === null ? '来自整个项目的编辑授权（到下方授权列表里撤销）' : '另有整个项目的编辑授权，取消勾选后仍是管理 Agent' }}
          </span>
        </li>
      </ul>
    </section>

    <section class="space-y-2 rounded-md border border-border/70 bg-card p-3" data-testid="grant-form">
      <div class="grid grid-cols-1 gap-2 sm:grid-cols-2">
        <select v-model="principal" class="h-8 rounded-md border border-input bg-background px-2 text-[12px]" data-testid="grant-principal">
          <option value="" disabled>选择 Agent 身份或群…</option>
          <optgroup label="Agent 身份">
            <option v-for="i in store.identities" :key="i.id" :value="`identity:${i.id}`">{{ i.label }}（{{ i.project_directory }}）</option>
          </optgroup>
          <optgroup v-if="groups.length" label="群（群里的所有成员，退群即失效）">
            <option v-for="g in groups" :key="g.id" :value="`group:${g.id}`">{{ g.title }}</option>
          </optgroup>
        </select>
        <select v-model="role" class="h-8 rounded-md border border-input bg-background px-2 text-[12px]" data-testid="grant-role">
          <option value="read">只读</option>
          <option value="comment">可评论</option>
          <option value="edit">可编辑</option>
        </select>
        <select v-model="scopeKind" class="h-8 rounded-md border border-input bg-background px-2 text-[12px]" data-testid="grant-scope-kind">
          <option value="module">某个模块</option>
          <option value="project">整个项目</option>
          <option value="doc">某篇文档</option>
          <option value="workflow">某个工作流的会话数据</option>
        </select>
        <select v-if="scopeKind === 'module'" v-model="scopeRef" class="h-8 rounded-md border border-input bg-background px-2 text-[12px]" data-testid="grant-scope-ref">
          <option value="" disabled>选择模块…</option>
          <option v-for="m in modules" :key="m.path" :value="m.path">{{ m.title }}（{{ m.path }}）</option>
        </select>
        <input
          v-else-if="scopeKind !== 'project'"
          v-model="scopeRef"
          :placeholder="scopeKind === 'doc' ? '文档路径，如 modules/chat/design.md' : '工作流编号'"
          class="h-8 rounded-md border border-input bg-background px-2 font-mono text-[12px]"
          data-testid="grant-scope-ref"
        />
        <div v-else class="flex items-center text-[11px] text-muted-foreground">授权覆盖整个项目知识库</div>
      </div>
      <div class="grid grid-cols-1 gap-2 sm:grid-cols-[10rem_1fr_auto]">
        <label class="flex items-center gap-2 text-[12px]">
          <span class="shrink-0 text-muted-foreground">有效（小时）</span>
          <input v-model.number="hours" type="number" min="0" max="8760" class="h-8 w-20 rounded-md border border-input bg-background px-2 text-[12px]" placeholder="0=长期" data-testid="grant-hours" />
        </label>
        <input v-model="note" maxlength="200" placeholder="备注（可选）" class="h-8 rounded-md border border-input bg-background px-2 text-[12px]" />
        <button type="button" class="h-8 cursor-pointer rounded-md bg-primary px-4 text-[12px] text-primary-foreground disabled:cursor-not-allowed disabled:opacity-40" :disabled="!canCreate" data-testid="grant-create" @click="create">授权</button>
      </div>
    </section>

    <div class="flex items-center gap-3 text-[12px]">
      <span class="font-medium">{{ showAll ? '全部记录' : '有效授权' }}（{{ shown.length }}）</span>
      <label class="ml-auto flex cursor-pointer items-center gap-1.5 text-muted-foreground">
        <input v-model="showAll" type="checkbox" class="size-3" data-testid="grant-show-all" />含已撤销 / 已过期
      </label>
    </div>
    <PageState v-if="loading && !grants.length" kind="loading" size="sm" title="授权加载中…" data-testid="project-grants-loading" />
    <ul v-else class="space-y-1" data-testid="grant-list">
      <PageState v-if="!shown.length" as="li" kind="empty" size="sm" bordered title="没有授权" data-testid="grant-empty" />
      <li v-for="g in shown" :key="g.id" class="flex items-center gap-2 rounded-md border border-border/60 px-2.5 py-1.5 text-[12px]" :class="grantActive(g) ? '' : 'opacity-55'" :data-testid="`grant-${g.id}`">
        <span class="min-w-0 flex-1">
          <span class="block truncate"><b>{{ principalText(g) }}</b> · {{ grantRoleLabel(g.role) }} · {{ grantScopeLabel(g.scope_kind, g.scope_ref) }}</span>
          <span class="block truncate text-[10px] text-muted-foreground">
            授予人：{{ g.granted_by_label || g.granted_by_ref }} · {{ g.revoked_at ? '已撤销' : grantExpiryLabel(g.expires_at) }}<template v-if="g.note"> · {{ g.note }}</template>
          </span>
        </span>
        <button v-if="grantActive(g)" type="button" class="h-7 cursor-pointer rounded-md border border-border px-2 text-[11px] hover:bg-destructive/10 hover:text-destructive" :data-testid="`grant-revoke-${g.id}`" @click="revoke(g)">撤销</button>
      </li>
    </ul>
  </div>
</template>
