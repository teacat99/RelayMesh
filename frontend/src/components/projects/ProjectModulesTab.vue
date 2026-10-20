<script setup lang="ts">
// 模块树：项目的功能模块（封闭列表）。每个模块可以指定负责人（Agent 身份）；设计文档放在 modules/<模块>/ 下自动归入。
import { computed, ref } from 'vue'
import { Pencil, Plus, Trash2 } from 'lucide-vue-next'
import { toast } from 'vue-sonner'
import { projectErrorMessage, projectsApi, type ProjectModule } from '../../api/projects'
import { flattenModules, joinModulePath, parentCandidates, validModuleSegment } from '../../lib/projects'
import { useProjectStore } from '../../stores/projects'
import PageState from '../shell/PageState.vue'

const props = defineProps<{ projectId: number }>()
const emit = defineEmits<{ (e: 'open-module', path: string): void }>()
const store = useProjectStore()

const modules = computed(() => store.detail?.modules ?? [])
const flat = computed(() => flattenModules(modules.value))

const editing = ref<string | null>(null) // null = 不在编辑；'' = 新建；其余是正在改的路径
const parent = ref('')
const segment = ref('')
const title = ref('')
const desc = ref('')
const owner = ref(0)
const busy = ref(false)

const pathPreview = computed(() => (editing.value === '' ? joinModulePath(parent.value, segment.value) : editing.value ?? ''))
const canSave = computed(() => {
  if (busy.value || !title.value.trim()) return false
  return editing.value === '' ? validModuleSegment(segment.value) : true
})

function startNew() {
  editing.value = ''
  parent.value = ''
  segment.value = ''
  title.value = ''
  desc.value = ''
  owner.value = 0
}
function startEdit(m: ProjectModule) {
  editing.value = m.path
  title.value = m.title
  desc.value = m.description ?? ''
  owner.value = m.owner_identity_id ?? 0
}

async function save() {
  if (!canSave.value) return
  busy.value = true
  try {
    await projectsApi.upsertModule(props.projectId, {
      path: pathPreview.value,
      title: title.value.trim(),
      description: desc.value.trim() || undefined,
      owner_identity_id: owner.value || undefined,
    })
    editing.value = null
    await store.loadDetail(props.projectId)
  } catch (e) {
    toast.error(projectErrorMessage(e, '保存失败'))
  } finally {
    busy.value = false
  }
}

async function remove(m: ProjectModule) {
  if (!window.confirm(`删除模块「${m.title}」及其子模块？里面的文档不会被删除，只是不再属于任何模块。`)) return
  try {
    await projectsApi.deleteModule(props.projectId, m.path)
    await store.loadDetail(props.projectId)
  } catch (e) {
    toast.error(projectErrorMessage(e, '删除失败'))
  }
}
</script>

<template>
  <div class="mx-auto w-full max-w-3xl space-y-3 p-4" data-testid="project-modules">
    <div class="flex items-center gap-2">
      <h3 class="text-[13px] font-semibold">模块</h3>
      <button type="button" class="ml-auto inline-flex h-8 cursor-pointer items-center gap-1 rounded-md bg-primary px-3 text-[12px] text-primary-foreground" data-testid="module-new" @click="startNew">
        <Plus class="size-3.5" />新建模块
      </button>
    </div>
    <p class="text-[12px] text-muted-foreground">模块是项目里稳定的功能域。负责人对自己的模块和其下文档有管理权；其他人默认只读，需要你或负责人授权才能写。</p>

    <div v-if="editing !== null" class="space-y-2 rounded-md border border-border/70 bg-card p-3" data-testid="module-form">
      <template v-if="editing === ''">
        <div class="grid grid-cols-1 gap-2 sm:grid-cols-[12rem_1fr]">
          <select v-model="parent" class="h-8 rounded-md border border-input bg-background px-2 text-[12px]" data-testid="module-parent">
            <option v-for="p in parentCandidates(modules)" :key="p" :value="p">{{ p || '（顶层）' }}</option>
          </select>
          <input v-model="segment" placeholder="路径名：小写字母、数字、连字符，如 chat" class="h-8 rounded-md border border-input bg-background px-2 font-mono text-[12px]" data-testid="module-segment" />
        </div>
        <div class="font-mono text-[11px] text-muted-foreground">路径：{{ pathPreview || '—' }}</div>
      </template>
      <div v-else class="font-mono text-[11px] text-muted-foreground">路径：{{ editing }}</div>
      <input v-model="title" maxlength="60" placeholder="标题，如「群聊」" class="h-8 w-full rounded-md border border-input bg-background px-2 text-[12px]" data-testid="module-title" />
      <textarea v-model="desc" rows="2" maxlength="500" placeholder="说明（可选）" class="w-full resize-y rounded-md border border-input bg-background px-2 py-1.5 text-[12px]" />
      <label class="flex items-center gap-2 text-[12px]">
        <span class="shrink-0 text-muted-foreground">负责人</span>
        <select v-model.number="owner" class="h-8 min-w-0 flex-1 rounded-md border border-input bg-background px-2 text-[12px]" data-testid="module-owner">
          <option :value="0">未指定</option>
          <option v-for="i in store.identities" :key="i.id" :value="i.id">{{ i.label }}（{{ i.project_directory }}）</option>
        </select>
      </label>
      <div class="flex justify-end gap-2">
        <button type="button" class="h-8 cursor-pointer rounded-md border border-border px-3 text-[12px] hover:bg-accent" @click="editing = null">取消</button>
        <button type="button" class="h-8 cursor-pointer rounded-md bg-primary px-3 text-[12px] text-primary-foreground disabled:cursor-not-allowed disabled:opacity-40" :disabled="!canSave" data-testid="module-save" @click="save">保存</button>
      </div>
    </div>

    <ul class="space-y-1" data-testid="module-list">
      <PageState v-if="!flat.length" as="li" kind="empty" size="sm" bordered title="还没有模块" data-testid="project-modules-empty" />
      <li v-for="n in flat" :key="n.module.path" class="flex items-center gap-2 rounded-md border border-border/60 py-1.5 pr-2 text-[12px]" :style="{ paddingLeft: 10 + n.depth * 18 + 'px' }" :data-testid="`module-${n.module.path}`">
        <button type="button" class="min-w-0 flex-1 cursor-pointer text-left" @click="emit('open-module', n.module.path)">
          <span class="block truncate font-medium">{{ n.module.title }}</span>
          <span class="block truncate font-mono text-[10px] text-muted-foreground">{{ n.module.path }} · 负责人：{{ store.identityLabel(n.module.owner_identity_id) }}</span>
        </button>
        <button type="button" class="cursor-pointer rounded p-1.5 text-muted-foreground hover:bg-accent" title="编辑" @click="startEdit(n.module)"><Pencil class="size-3.5" /></button>
        <button type="button" class="cursor-pointer rounded p-1.5 text-muted-foreground hover:bg-destructive/10 hover:text-destructive" title="删除" @click="remove(n.module)"><Trash2 class="size-3.5" /></button>
      </li>
    </ul>
  </div>
</template>
