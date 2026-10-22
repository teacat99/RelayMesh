<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import { toast } from 'vue-sonner'
import { ChevronDown, ChevronRight, ChevronUp, Inbox, ListTree, Pencil, Plus, Trash2 } from 'lucide-vue-next'
import WorkProgressBar from './WorkProgressBar.vue'
import { useWorkItemStore } from '../../stores/workItems'
import { workErrorMessage } from '../../api/workItems'
import {
  MODULE_ALL,
  MODULE_LIMITS,
  MODULE_NONE,
  UNASSIGNED_LABEL,
  checkModuleName,
  type ModuleSelection,
  type WorkModule,
  type WorkModuleStats,
} from '../../lib/workItems'

/** drawer：放在窄屏抽屉里时，给抽屉自带的关闭按钮让出右上角的位置。 */
defineProps<{ drawer?: boolean }>()

/** 选中了某一项（窄屏抽屉据此关闭自己）。 */
const emit = defineEmits<{ (e: 'picked'): void }>()

const store = useWorkItemStore()

// 输入框出现时自动聚焦（改名、添加）。
const vFocus = { mounted: (el: HTMLElement) => el.focus() }

const editing = ref(false)
const busy = ref(false)
/** 折叠起来的模块（只影响树的显示，不影响筛选）。 */
const folded = reactive(new Set<number>())

const sel = computed(() => store.filter.selection)
function isSelected(s: ModuleSelection) {
  return sel.value === s
}

function pick(s: ModuleSelection) {
  store.select(s)
  emit('picked')
}

function toggleFold(id: number) {
  if (folded.has(id)) folded.delete(id)
  else folded.add(id)
}

// ---------- 树的行 ----------

interface NodeRow {
  kind: 'node'
  m: WorkModule
  depth: 0 | 1
  first: boolean
  last: boolean
  features: number
  folded: boolean
}
interface AddRow {
  kind: 'add'
  parent: WorkModule
}
type TreeRow = NodeRow | AddRow

const addingFeatureTo = ref<number | null>(null)

const rows = computed<TreeRow[]>(() => {
  const out: TreeRow[] = []
  const tree = store.tree
  tree.forEach((n, i) => {
    const isFolded = folded.has(n.module.id) && addingFeatureTo.value !== n.module.id
    out.push({ kind: 'node', m: n.module, depth: 0, first: i === 0, last: i === tree.length - 1, features: n.features.length, folded: isFolded })
    if (!isFolded) {
      n.features.forEach((f, j) => out.push({ kind: 'node', m: f, depth: 1, first: j === 0, last: j === n.features.length - 1, features: 0, folded: false }))
      if (editing.value && addingFeatureTo.value === n.module.id) out.push({ kind: 'add', parent: n.module })
    }
  })
  return out
})

function openOf(s: WorkModuleStats | null | undefined) {
  return s ? s.todo + s.doing + s.review : 0
}

const allCounts = computed(() => {
  const c = store.counts
  const open = (c.todo || 0) + (c.doing || 0) + (c.review || 0)
  const total = open + (c.done || 0) + (c.shelved || 0)
  return { open, total }
})

// ---------- 编辑规划 ----------

function toggleEditing() {
  editing.value = !editing.value
  renamingId.value = null
  addingFeatureTo.value = null
  armedDelete.value = null
  newModuleError.value = ''
}

const renamingId = ref<number | null>(null)
const renameText = ref('')
const renameError = ref('')

function siblingsOf(m: WorkModule): { id: number; name: string }[] {
  return store.modules.filter(x => (x.parent_id || 0) === (m.parent_id || 0))
}

function startRename(m: WorkModule) {
  armedDelete.value = null
  addingFeatureTo.value = null
  renamingId.value = m.id
  renameText.value = m.name
  renameError.value = ''
}

function cancelRename() {
  renamingId.value = null
  renameError.value = ''
}

async function commitRename(m: WorkModule) {
  if (renamingId.value !== m.id || busy.value) return
  const check = checkModuleName(renameText.value, siblingsOf(m), m.parent_id ? '功能' : '模块', m.id)
  if (!check.ok) {
    renameError.value = check.error || ''
    return
  }
  if (check.name === m.name) {
    cancelRename()
    return
  }
  busy.value = true
  try {
    await store.renameModule(m.id, check.name)
    // 同上：刷新期间用户可能已转去改别的节点，只收起自己这一个编辑框
    if (renamingId.value === m.id) cancelRename()
  } catch (err) {
    if (renamingId.value === m.id) renameError.value = workErrorMessage(err)
    else toast.error(workErrorMessage(err))
  } finally {
    busy.value = false
  }
}

async function move(m: WorkModule, direction: 'up' | 'down') {
  if (busy.value) return
  busy.value = true
  try {
    await store.moveModule(m.id, direction)
  } catch (err) {
    toast.error(workErrorMessage(err))
  } finally {
    busy.value = false
  }
}

// 删除：先说明后果，再点「确认删除」才执行。
const armedDelete = ref<number | null>(null)

function deleteHint(m: WorkModule): string {
  const total = m.stats?.total || 0
  if (m.parent_id) {
    const parent = store.modules.find(x => x.id === m.parent_id)
    return `删除功能「${m.name}」：其中 ${total} 个任务会回到「${parent?.name || '上一级'}」。`
  }
  const feats = store.modules.filter(x => x.parent_id === m.id).length
  return `删除模块「${m.name}」${feats ? `及其 ${feats} 个功能` : ''}：${total} 个任务会变成「${UNASSIGNED_LABEL}」，任务本身不会被删除。`
}

function askDelete(m: WorkModule) {
  renamingId.value = null
  addingFeatureTo.value = null
  armedDelete.value = armedDelete.value === m.id ? null : m.id
}

async function confirmDelete(m: WorkModule) {
  if (busy.value) return
  busy.value = true
  try {
    const res = await store.removeModule(m.id)
    // removeModule 要等整表刷新（好几个来回）才返回；这期间用户可能已点了另一个节点的删除并展开了它的确认框，
    // 这里只收起属于自己这一次的那个，不能一概清空，否则后点的确认框会被这次迟到的完成冲掉。
    if (armedDelete.value === m.id) armedDelete.value = null
    toast.success(`已删除「${res.path || m.name}」`, {
      description: res.moved_tasks ? `${res.moved_tasks} 个任务已${m.parent_id ? '回到上一级' : `变成「${UNASSIGNED_LABEL}」`}` : undefined,
    })
  } catch (err) {
    toast.error(workErrorMessage(err))
  } finally {
    busy.value = false
  }
}

// 添加功能（在某个模块下）
const featureText = ref('')
const featureError = ref('')

function startAddFeature(m: WorkModule) {
  renamingId.value = null
  armedDelete.value = null
  featureText.value = ''
  featureError.value = ''
  addingFeatureTo.value = m.id
  folded.delete(m.id)
}

function cancelAddFeature() {
  addingFeatureTo.value = null
  featureError.value = ''
}

async function commitAddFeature(parent: WorkModule) {
  if (busy.value) return
  const siblings = store.modules.filter(x => x.parent_id === parent.id)
  const check = checkModuleName(featureText.value, siblings, '功能')
  if (!check.ok) {
    featureError.value = check.error || ''
    return
  }
  busy.value = true
  try {
    await store.createModule(check.name, parent.id)
    // 创建在路上用户可能已经去给另一个模块加功能了：输入框是共用的，只在还停在这个模块上时才清
    if (addingFeatureTo.value === parent.id) {
      featureText.value = ''
      featureError.value = ''
    }
  } catch (err) {
    if (addingFeatureTo.value === parent.id) featureError.value = workErrorMessage(err)
    else toast.error(workErrorMessage(err))
  } finally {
    busy.value = false
  }
}

// 添加模块（顶层）
const newModuleText = ref('')
const newModuleError = ref('')

async function commitAddModule() {
  if (busy.value) return
  const check = checkModuleName(newModuleText.value, store.tree.map(n => n.module), '模块')
  if (!check.ok) {
    newModuleError.value = check.error || ''
    return
  }
  busy.value = true
  try {
    await store.createModule(check.name, 0)
    newModuleText.value = ''
    newModuleError.value = ''
  } catch (err) {
    newModuleError.value = workErrorMessage(err)
  } finally {
    busy.value = false
  }
}

const unassignedOpen = computed(() => openOf(store.unassigned))
</script>

<template>
  <nav class="flex min-h-0 flex-1 flex-col" aria-label="模块规划" data-testid="work-tree">
    <div class="flex shrink-0 items-center gap-2 border-b border-border/60 px-3" :class="drawer ? 'h-12 pr-12' : 'h-9'">
      <ListTree class="size-3.5 shrink-0 text-muted-foreground" />
      <h3 class="text-xs font-semibold text-foreground">模块规划</h3>
      <button
        type="button"
        class="ml-auto inline-flex h-6 cursor-pointer items-center gap-1 rounded-xs border px-2 text-[11px] transition-colors"
        :class="editing ? 'border-primary/50 bg-primary/10 text-primary' : 'border-border/70 text-muted-foreground hover:bg-muted hover:text-foreground'"
        :aria-pressed="editing"
        data-testid="work-tree-edit"
        @click="toggleEditing"
      >
        <Pencil v-if="!editing" class="size-3" />{{ editing ? '完成' : '编辑规划' }}
      </button>
    </div>

    <!-- 展开 / 收起功能、增删模块时：新出现的行淡入并落下一点，下面的行平滑让位（只动 transform / opacity；「减少动态效果」下关闭）。 -->
    <TransitionGroup tag="ul" name="work-tree" class="relative min-h-0 flex-1 overflow-y-auto py-1" role="tree">
      <!-- 全部 -->
      <li key="all" role="none">
        <div class="flex items-center transition-colors" :class="isSelected(MODULE_ALL) ? 'bg-primary/10' : 'hover:bg-muted/50'" role="treeitem" :aria-selected="isSelected(MODULE_ALL)" aria-level="1">
          <span class="size-5 shrink-0" aria-hidden="true" />
          <button type="button" class="flex min-w-0 flex-1 cursor-pointer items-center gap-2 py-1.5 pl-1 pr-3 text-left" data-testid="work-tree-all" @click="pick(MODULE_ALL)">
            <span class="truncate text-xs font-medium" :class="isSelected(MODULE_ALL) ? 'text-foreground' : 'text-foreground/90'">全部任务</span>
            <span class="ml-auto font-mono text-[10px] tabular-nums text-muted-foreground" :title="`未结束 ${allCounts.open}，共 ${allCounts.total}`">{{ allCounts.open }}/{{ allCounts.total }}</span>
          </button>
        </div>
      </li>

      <template v-for="r in rows" :key="r.kind === 'node' ? r.m.id : `add-${r.parent.id}`">
        <!-- 添加功能的输入行 -->
        <li v-if="r.kind === 'add'" role="none" class="py-1 pl-9 pr-2">
          <form class="flex items-center gap-1" @submit.prevent="commitAddFeature(r.parent)">
            <input
              v-model="featureText"
              v-focus
              type="text"
              maxlength="60"
              :placeholder="`「${r.parent.name}」下的功能名称`"
              class="h-7 min-w-0 flex-1 rounded-xs border border-border/80 bg-background px-2 text-xs focus:border-primary focus:outline-none"
              :data-testid="`work-tree-feature-input-${r.parent.id}`"
              @keydown.esc.prevent="cancelAddFeature"
            />
            <button type="submit" :disabled="busy" class="h-7 cursor-pointer rounded-xs border border-primary/40 bg-primary/5 px-2 text-[11px] text-primary hover:bg-primary/10 disabled:opacity-50" :data-testid="`work-tree-feature-add-${r.parent.id}`">添加</button>
            <button type="button" class="h-7 cursor-pointer rounded-xs px-1.5 text-[11px] text-muted-foreground hover:text-foreground" @click="cancelAddFeature">取消</button>
          </form>
          <p v-if="featureError" class="mt-1 text-[11px] text-destructive" role="alert">{{ featureError }}</p>
        </li>

        <li v-else role="none">
          <div
            class="flex items-center transition-colors"
            :class="isSelected(r.m.id) ? 'bg-primary/10' : 'hover:bg-muted/50'"
            role="treeitem"
            :aria-selected="isSelected(r.m.id)"
            :aria-level="r.depth + 1"
            :aria-expanded="r.depth === 0 && r.features ? !r.folded : undefined"
            :data-testid="`work-tree-node-${r.m.id}`"
            :data-path="r.m.path"
          >
            <span v-if="r.depth === 1" class="size-5 shrink-0" aria-hidden="true" />
            <button
              v-if="r.depth === 0 && r.features"
              type="button"
              class="inline-flex size-5 shrink-0 cursor-pointer items-center justify-center rounded-xs text-muted-foreground hover:bg-muted hover:text-foreground"
              :aria-label="r.folded ? '展开功能' : '收起功能'"
              @click="toggleFold(r.m.id)"
            >
              <ChevronRight class="size-3.5 transition-transform" :class="r.folded ? '' : 'rotate-90'" />
            </button>
            <span v-else-if="r.depth === 0" class="size-5 shrink-0" aria-hidden="true" />
            <span v-if="r.depth === 1" class="size-3 shrink-0" aria-hidden="true" />

            <!-- 改名 -->
            <form v-if="renamingId === r.m.id" class="flex min-w-0 flex-1 items-center gap-1 py-1 pl-1 pr-2" @submit.prevent="commitRename(r.m)">
              <input
                v-model="renameText"
                v-focus
                type="text"
                maxlength="60"
                class="h-7 min-w-0 flex-1 rounded-xs border border-border/80 bg-background px-2 text-xs focus:border-primary focus:outline-none"
                :data-testid="`work-tree-rename-input-${r.m.id}`"
                @keydown.esc.prevent="cancelRename"
              />
              <button type="submit" :disabled="busy" class="h-7 cursor-pointer rounded-xs border border-primary/40 bg-primary/5 px-2 text-[11px] text-primary hover:bg-primary/10 disabled:opacity-50" :data-testid="`work-tree-rename-save-${r.m.id}`">保存</button>
              <button type="button" class="h-7 cursor-pointer rounded-xs px-1.5 text-[11px] text-muted-foreground hover:text-foreground" @click="cancelRename">取消</button>
            </form>

            <button
              v-else
              type="button"
              class="flex min-w-0 flex-1 cursor-pointer items-center gap-2 py-1.5 pl-1 pr-2 text-left"
              :title="r.m.description ? `${r.m.path} · ${r.m.description}` : r.m.path"
              :data-testid="`work-tree-pick-${r.m.id}`"
              @click="pick(r.m.id)"
            >
              <span class="truncate text-xs" :class="r.depth === 0 ? 'font-medium text-foreground' : 'text-foreground/85'">{{ r.m.name }}</span>
              <template v-if="!editing">
                <span class="ml-auto shrink-0 font-mono text-[10px] tabular-nums text-muted-foreground" :title="`未结束 ${openOf(r.m.stats)}，共 ${r.m.stats.total}`">{{ openOf(r.m.stats) }}/{{ r.m.stats.total }}</span>
                <WorkProgressBar v-if="r.m.stats.total" class="w-9 shrink-0" :percent="r.m.stats.percent" tone="emerald" thin />
                <span v-else class="w-9 shrink-0" />
              </template>
            </button>

            <!-- 编辑规划：上移 / 下移 / 改名 / 加功能 / 删除 -->
            <div v-if="editing && renamingId !== r.m.id" class="flex shrink-0 items-center pr-1.5">
              <button type="button" :disabled="busy || r.first" class="inline-flex size-6 cursor-pointer items-center justify-center rounded-xs text-muted-foreground hover:bg-muted hover:text-foreground disabled:cursor-default disabled:opacity-30" title="上移" :data-testid="`work-tree-up-${r.m.id}`" @click="move(r.m, 'up')"><ChevronUp class="size-3.5" /></button>
              <button type="button" :disabled="busy || r.last" class="inline-flex size-6 cursor-pointer items-center justify-center rounded-xs text-muted-foreground hover:bg-muted hover:text-foreground disabled:cursor-default disabled:opacity-30" title="下移" :data-testid="`work-tree-down-${r.m.id}`" @click="move(r.m, 'down')"><ChevronDown class="size-3.5" /></button>
              <button type="button" class="inline-flex size-6 cursor-pointer items-center justify-center rounded-xs text-muted-foreground hover:bg-muted hover:text-foreground" title="改名" :data-testid="`work-tree-rename-${r.m.id}`" @click="startRename(r.m)"><Pencil class="size-3" /></button>
              <button v-if="r.depth === 0" type="button" class="inline-flex size-6 cursor-pointer items-center justify-center rounded-xs text-muted-foreground hover:bg-muted hover:text-foreground" title="添加功能" :data-testid="`work-tree-add-feature-${r.m.id}`" @click="startAddFeature(r.m)"><Plus class="size-3.5" /></button>
              <button type="button" class="inline-flex size-6 cursor-pointer items-center justify-center rounded-xs hover:bg-destructive/10" :class="armedDelete === r.m.id ? 'bg-destructive/10 text-destructive' : 'text-muted-foreground hover:text-destructive'" title="删除" :data-testid="`work-tree-delete-${r.m.id}`" @click="askDelete(r.m)"><Trash2 class="size-3" /></button>
            </div>
          </div>
          <p v-if="renamingId === r.m.id && renameError" class="px-9 pb-1 text-[11px] text-destructive" role="alert">{{ renameError }}</p>
          <div v-if="armedDelete === r.m.id" class="mx-2 mb-1 rounded-xs border border-destructive/30 bg-destructive/5 px-2 py-1.5 text-[11px] leading-relaxed text-destructive" role="alert" :data-testid="`work-tree-delete-hint-${r.m.id}`">
            {{ deleteHint(r.m) }}
            <span class="mt-1 flex gap-2">
              <button type="button" :disabled="busy" class="cursor-pointer rounded-xs border border-destructive/50 bg-destructive/10 px-2 py-0.5 font-medium hover:bg-destructive/20 disabled:opacity-50" :data-testid="`work-tree-delete-confirm-${r.m.id}`" @click="confirmDelete(r.m)">确认删除</button>
              <button type="button" class="cursor-pointer px-1 text-muted-foreground hover:text-foreground" @click="armedDelete = null">取消</button>
            </span>
          </div>
        </li>
      </template>

      <!-- 未归类 -->
      <li key="none" role="none" class="mt-1 border-t border-border/50 pt-1">
        <div class="flex items-center transition-colors" :class="isSelected(MODULE_NONE) ? 'bg-primary/10' : 'hover:bg-muted/50'" role="treeitem" :aria-selected="isSelected(MODULE_NONE)" aria-level="1">
          <span class="size-5 shrink-0" aria-hidden="true" />
          <button type="button" class="flex min-w-0 flex-1 cursor-pointer items-center gap-2 py-1.5 pl-1 pr-3 text-left" data-testid="work-tree-none" @click="pick(MODULE_NONE)">
            <Inbox class="size-3 shrink-0 text-muted-foreground" />
            <span class="truncate text-xs text-foreground/85">{{ UNASSIGNED_LABEL }}</span>
            <span class="ml-auto font-mono text-[10px] tabular-nums text-muted-foreground" :title="`未结束 ${unassignedOpen}，共 ${store.unassigned?.total || 0}`">{{ unassignedOpen }}/{{ store.unassigned?.total || 0 }}</span>
          </button>
        </div>
      </li>

      <li v-if="!store.hasPlan && !editing" key="empty" role="none" class="mx-3 mt-3 space-y-1.5 rounded-xs border border-dashed border-border/70 px-3 py-3 text-[11px] leading-relaxed text-muted-foreground" data-testid="work-tree-empty">
        <p class="font-medium text-foreground">还没有模块规划</p>
        <p>
          模块规划把任务按「模块 → 功能」归类，任务的标注就不会各写各的。智能体用 <code class="rounded-xs bg-muted px-1 font-mono">task_board</code> 一次就能定义整棵树，你也可以自己建。
        </p>
        <button type="button" class="cursor-pointer text-primary hover:underline" @click="toggleEditing">编辑规划</button>
      </li>
    </TransitionGroup>

    <!-- 添加模块 -->
    <div v-if="editing" class="shrink-0 space-y-1 border-t border-border/60 p-2">
      <form class="flex items-center gap-1" @submit.prevent="commitAddModule">
        <input
          v-model="newModuleText"
          type="text"
          maxlength="60"
          placeholder="新模块名称，如：终端"
          class="h-7 min-w-0 flex-1 rounded-xs border border-border/80 bg-background px-2 text-xs focus:border-primary focus:outline-none"
          data-testid="work-tree-add-module-input"
        />
        <button type="submit" :disabled="busy" class="inline-flex h-7 cursor-pointer items-center gap-1 rounded-xs border border-primary/40 bg-primary/5 px-2 text-[11px] text-primary hover:bg-primary/10 disabled:opacity-50" data-testid="work-tree-add-module"><Plus class="size-3" />添加模块</button>
      </form>
      <p v-if="newModuleError" class="text-[11px] text-destructive" role="alert">{{ newModuleError }}</p>
      <p class="text-[10px] leading-relaxed text-muted-foreground">
        每个项目最多 {{ MODULE_LIMITS.modulesPerWorkflow }} 个模块、每个模块最多 {{ MODULE_LIMITS.featuresPerModule }} 个功能；名称不能含 / &gt; → 等分隔符。删除模块不会删除任务。
      </p>
    </div>
  </nav>
</template>
