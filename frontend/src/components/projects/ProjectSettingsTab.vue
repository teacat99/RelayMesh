<script setup lang="ts">
// 项目设置：名称与说明、权限档位（先审计、后强制）、归入的目录、归档。
import { computed, ref, watch } from 'vue'
import { Trash2 } from 'lucide-vue-next'
import { toast } from 'vue-sonner'
import { projectErrorMessage, projectsApi, type ACLMode } from '../../api/projects'
import { ACL_MODES, ENFORCE_WARNING, aclModeHint, aclModeLabel } from '../../lib/projects'
import { useProjectStore } from '../../stores/projects'
import PageState from '../shell/PageState.vue'

const props = defineProps<{ projectId: number }>()
const emit = defineEmits<{ (e: 'archived'): void }>()
const store = useProjectStore()

const project = computed(() => store.detail?.project)
const dirs = computed(() => store.detail?.dirs ?? [])

const name = ref('')
const description = ref('')
const mode = ref<ACLMode>('audit')
const enforceAck = ref(false)
const saving = ref(false)

watch(
  project,
  p => {
    if (!p) return
    name.value = p.name
    description.value = p.description ?? ''
    mode.value = p.acl_mode
    enforceAck.value = false
  },
  { immediate: true },
)

const dirty = computed(() => {
  const p = project.value
  return !!p && (p.name !== name.value.trim() || (p.description ?? '') !== description.value.trim() || p.acl_mode !== mode.value)
})
const needsAck = computed(() => mode.value === 'enforce' && project.value?.acl_mode !== 'enforce')
const canSave = computed(() => dirty.value && !saving.value && name.value.trim().length > 0 && (!needsAck.value || enforceAck.value))

async function save() {
  if (!canSave.value) return
  saving.value = true
  try {
    await projectsApi.update(props.projectId, { name: name.value.trim(), description: description.value.trim(), acl_mode: mode.value })
    toast.success('已保存')
    await store.loadDetail(props.projectId)
  } catch (e) {
    toast.error(projectErrorMessage(e, '保存失败'))
  } finally {
    saving.value = false
  }
}

// ---- 目录 ----
const newHost = ref('')
const newDir = ref('')
const newBranch = ref('')
const dirBusy = ref(false)
const freeSuggestions = computed(() => store.suggestions)

async function addDir(host: string, dir: string, branch = '') {
  if (!dir.trim()) return
  const pid = props.projectId
  dirBusy.value = true
  try {
    await projectsApi.addDir(pid, { host_name: host.trim(), project_directory: dir.trim(), branch_hint: branch.trim() || undefined })
    // 归入在路上用户可能已经切去了别的项目：那边表单里刚填的内容不能被冲掉
    if (props.projectId === pid) {
      newHost.value = ''
      newDir.value = ''
      newBranch.value = ''
    }
    await Promise.all([store.loadDetail(props.projectId), store.loadList()])
  } catch (e) {
    toast.error(projectErrorMessage(e, '归入失败'))
  } finally {
    dirBusy.value = false
  }
}

async function removeDir(id: number) {
  if (!window.confirm('把这个目录移出项目？它的工作流之后不再属于这个项目（文档不会被删除，但在项目里看不到）。')) return
  try {
    await projectsApi.removeDir(props.projectId, id)
    await Promise.all([store.loadDetail(props.projectId), store.loadList()])
  } catch (e) {
    toast.error(projectErrorMessage(e, '移出失败'))
  }
}

async function archive() {
  if (!window.confirm('归档这个项目？项目从列表里消失，目录释放出来；文档不会被删除。')) return
  try {
    await projectsApi.archive(props.projectId)
    toast.success('已归档')
    emit('archived')
  } catch (e) {
    toast.error(projectErrorMessage(e, '归档失败'))
  }
}
</script>

<template>
  <div v-if="project" class="mx-auto w-full max-w-3xl space-y-6 p-4" data-testid="project-settings">
    <section class="space-y-2">
      <h3 class="text-[13px] font-semibold">基本信息</h3>
      <input v-model="name" maxlength="60" class="h-9 w-full rounded-md border border-input bg-background px-2.5 text-[13px] outline-none focus-visible:ring-1 focus-visible:ring-ring" data-testid="project-name" />
      <textarea
        v-model="description"
        rows="2"
        maxlength="500"
        placeholder="一句话说明这个项目（可选）"
        class="w-full resize-y rounded-md border border-input bg-background px-2.5 py-1.5 text-[13px] outline-none focus-visible:ring-1 focus-visible:ring-ring"
        data-testid="project-description"
      />
      <div class="font-mono text-[10px] text-muted-foreground">标识：{{ project.slug }}</div>
    </section>

    <section class="space-y-2" data-testid="project-acl">
      <h3 class="text-[13px] font-semibold">权限档位</h3>
      <div class="flex flex-wrap gap-2" role="radiogroup" aria-label="权限档位">
        <label
          v-for="m in ACL_MODES"
          :key="m"
          class="flex cursor-pointer items-center gap-1.5 rounded-md border px-3 py-1.5 text-[12px]"
          :class="mode === m ? 'border-primary bg-primary/10' : 'border-border/70 hover:bg-accent/50'"
        >
          <input v-model="mode" type="radio" name="acl-mode" class="size-3" :value="m" :data-testid="`acl-mode-${m}`" />
          {{ aclModeLabel(m) }}
        </label>
      </div>
      <p class="text-[12px] text-muted-foreground" data-testid="acl-hint">{{ aclModeHint(mode) }}</p>
      <label v-if="needsAck" class="flex items-start gap-2 rounded-md border border-amber-500/40 bg-amber-500/10 p-2 text-[12px]">
        <input v-model="enforceAck" type="checkbox" class="mt-0.5 size-3.5" data-testid="acl-enforce-ack" />
        <span>{{ ENFORCE_WARNING }}</span>
      </label>
    </section>

    <div class="flex justify-end">
      <button
        type="button"
        class="h-8 cursor-pointer rounded-md bg-primary px-4 text-[12px] text-primary-foreground disabled:cursor-not-allowed disabled:opacity-40"
        :disabled="!canSave"
        data-testid="project-save"
        @click="save"
      >
        {{ saving ? '保存中…' : '保存修改' }}
      </button>
    </div>

    <section class="space-y-2" data-testid="project-dirs">
      <h3 class="text-[13px] font-semibold">归入的目录</h3>
      <p class="text-[12px] text-muted-foreground">一个项目可以有多个目录（同一仓库的不同工作树 / 分支）。在这些目录里工作的 Agent 就属于这个项目。</p>
      <ul class="space-y-1">
        <PageState v-if="!dirs.length" as="li" kind="empty" size="sm" bordered title="还没有目录" data-testid="project-dirs-empty" />
        <li v-for="d in dirs" :key="d.id" class="flex items-center gap-2 rounded-md border border-border/60 px-2.5 py-1.5 text-[12px]" :data-testid="`dir-${d.project_directory}`">
          <span class="min-w-0 flex-1">
            <span class="block truncate font-mono text-[11px]">{{ d.host_name }}:{{ d.project_directory }}</span>
            <span v-if="d.branch_hint" class="text-[10px] text-muted-foreground">分支：{{ d.branch_hint }}</span>
          </span>
          <button type="button" class="cursor-pointer rounded p-1.5 text-muted-foreground hover:bg-destructive/10 hover:text-destructive" title="移出项目" @click="removeDir(d.id)">
            <Trash2 class="size-3.5" />
          </button>
        </li>
      </ul>

      <div v-if="freeSuggestions.length" class="space-y-1">
        <div class="text-[11px] text-muted-foreground">从建议里添加：</div>
        <button
          v-for="s in freeSuggestions"
          :key="s.host_name + s.project_directory"
          type="button"
          class="mr-1 inline-flex max-w-full cursor-pointer items-center truncate rounded-full border border-border/70 px-2.5 py-1 font-mono text-[10px] hover:bg-accent/60 disabled:opacity-50"
          :disabled="dirBusy"
          :data-testid="`dir-suggest-${s.project_directory}`"
          @click="addDir(s.host_name, s.project_directory)"
        >
          + {{ s.host_name }}:{{ s.project_directory }}
        </button>
      </div>

      <div class="grid grid-cols-1 gap-2 sm:grid-cols-[8rem_1fr_8rem_auto]">
        <input v-model="newHost" placeholder="主机名" class="h-8 rounded-md border border-input bg-background px-2 text-[12px]" data-testid="dir-host" />
        <input v-model="newDir" placeholder="目录绝对路径" class="h-8 rounded-md border border-input bg-background px-2 font-mono text-[12px]" data-testid="dir-path" />
        <input v-model="newBranch" placeholder="分支（可选）" class="h-8 rounded-md border border-input bg-background px-2 text-[12px]" />
        <button
          type="button"
          class="h-8 cursor-pointer rounded-md border border-border px-3 text-[12px] hover:bg-accent disabled:cursor-not-allowed disabled:opacity-40"
          :disabled="dirBusy || !newDir.trim()"
          data-testid="dir-add"
          @click="addDir(newHost, newDir, newBranch)"
        >
          归入
        </button>
      </div>
    </section>

    <section class="space-y-2 border-t border-border/60 pt-4">
      <h3 class="text-[13px] font-semibold text-destructive">归档项目</h3>
      <p class="text-[12px] text-muted-foreground">归档后项目从列表消失、目录释放；文档与审计都保留，不会删除任何内容。</p>
      <button type="button" class="h-8 cursor-pointer rounded-md border border-destructive/50 px-3 text-[12px] text-destructive hover:bg-destructive/10" data-testid="project-archive" @click="archive">归档这个项目</button>
    </section>
  </div>
</template>
