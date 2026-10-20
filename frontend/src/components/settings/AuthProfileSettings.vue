<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { toast } from 'vue-sonner'
import { Pencil, Plus, ShieldCheck, Star, Trash2 } from 'lucide-vue-next'
import { asyncModeErrorMessage } from '@/api/asyncMode'
import type { AuthProfile } from '@/api/types'
import { levelLabel, validateProfileDraft } from '@/lib/asyncMode'
import { useAsyncModeStore } from '@/stores/asyncMode'

// 授权档位：异步对话模式下，智能体不必先问就能直接做什么。
// 默认只有一档「标准授权」，其余由你自己加；每个工作流在工具栏的小标签里各自选档。
// 不可逆操作（部署、推送远端、删数据、改凭据、不可逆迁移）在任何档位下都要停下来问你，所以这里没有「放飞」一档。

const store = useAsyncModeStore()

const LEVELS: ReadonlyArray<{ id: 'L0' | 'L1'; title: string }> = [
  { id: 'L0', title: 'L0 · 只读，写入先问' },
  { id: 'L1', title: 'L1 · 可回滚的改动直接做' },
]

interface Draft {
  /** 0 = 新增；否则是正在编辑的档位。 */
  id: number
  name: string
  level: 'L0' | 'L1'
  rules: string
}

const editing = ref<Draft | null>(null)
const formError = ref('')
/** 点过一次「删除」、等待二次确认的档位。 */
const confirmingDelete = ref(0)
let confirmTimer: ReturnType<typeof setTimeout> | null = null

const atLimit = computed(() => store.profiles.length >= store.limits.max)
const nameLen = computed(() => [...(editing.value?.name ?? '')].length)
const rulesLen = computed(() => [...(editing.value?.rules ?? '')].length)

const inputClass =
  'w-full h-8 px-2.5 text-xs font-mono bg-background border border-border/80 rounded-xs focus:outline-none focus:border-primary text-foreground disabled:opacity-60'

onMounted(() => {
  void store.loadProfiles(true)
})
onBeforeUnmount(() => {
  if (confirmTimer) clearTimeout(confirmTimer)
})

function startAdd() {
  formError.value = ''
  editing.value = reactive<Draft>({ id: 0, name: '', level: 'L1', rules: '' })
}

function startEdit(p: AuthProfile) {
  formError.value = ''
  editing.value = reactive<Draft>({ id: p.id, name: p.name, level: p.level, rules: p.rules })
}

function cancel() {
  editing.value = null
  formError.value = ''
}

async function save() {
  const d = editing.value
  if (!d) return
  const problem = validateProfileDraft(d, store.limits)
  if (problem) {
    formError.value = problem
    return
  }
  formError.value = ''
  try {
    const draft = { name: d.name.trim(), level: d.level, rules: d.rules.trim() }
    if (d.id) await store.updateProfile(d.id, draft)
    else await store.createProfile(draft)
    toast.success(d.id ? '已保存档位' : '已新增档位', { duration: 1600 })
    editing.value = null
  } catch (err) {
    formError.value = asyncModeErrorMessage(err, '保存失败')
  }
}

async function makeDefault(p: AuthProfile) {
  try {
    await store.setDefault(p.id)
    toast.success(`已把「${p.name}」设为默认档位`, { description: '新工作流与「跟随默认」的工作流都会用它', duration: 2200 })
  } catch (err) {
    toast.error(asyncModeErrorMessage(err, '设置默认失败'))
  }
}

async function remove(p: AuthProfile) {
  if (confirmingDelete.value !== p.id) {
    confirmingDelete.value = p.id
    if (confirmTimer) clearTimeout(confirmTimer)
    confirmTimer = setTimeout(() => {
      confirmingDelete.value = 0
    }, 4000)
    return
  }
  confirmingDelete.value = 0
  try {
    await store.removeProfile(p.id)
    toast.success(`已删除「${p.name}」`, { description: '选用它的工作流会回落到默认档位', duration: 2200 })
  } catch (err) {
    toast.error(asyncModeErrorMessage(err, '删除失败'))
  }
}
</script>

<template>
  <div class="p-3 rounded-xs border border-border/70 bg-card/60 space-y-3" data-testid="auth-profile-settings">
    <div class="flex items-center gap-1.5 text-xs font-mono font-medium text-foreground">
      <ShieldCheck class="w-3.5 h-3.5 text-primary" />
      <span>授权档位 · 异步对话时智能体不必先问就能做的事</span>
    </div>
    <p class="text-[10px] text-muted-foreground leading-relaxed">
      在会话页工具栏的 hook 芯片里为某个工作流开启「异步对话」后，智能体发完进度就继续工作，不再停下来等你回复；
      它能直接处理到哪一步，由这里的授权档位决定（取代原来的「讨论 / 方案 / 开发」阶段提示词）。
      每个工作流在工具栏的授权小标签里各自选档，没选的跟随默认档位。
      <b class="text-foreground">不可逆操作（部署、推送远端、删数据、改凭据、不可逆迁移）在任何档位下都会停下来问你</b>，所以没有「放飞」一档。
    </p>

    <div v-if="store.error && !store.loaded" class="text-[11px] text-destructive flex items-center gap-1.5" data-testid="auth-profiles-error">
      <span>{{ store.error }}</span>
      <button type="button" class="underline cursor-pointer" @click="store.loadProfiles(true)">重试</button>
    </div>

    <template v-else-if="store.loaded">
      <ul class="space-y-2" data-testid="auth-profiles-list">
        <li
          v-for="p in store.profiles"
          :key="p.id"
          :data-testid="`auth-profile-row-${p.id}`"
          :data-default="p.is_default ? 'true' : 'false'"
          class="p-2.5 rounded-xs border border-border/70 bg-background/70 font-mono space-y-1.5"
        >
          <div class="flex items-center gap-2 flex-wrap">
            <span class="text-xs font-semibold text-foreground">{{ p.name }}</span>
            <span class="text-[10px] px-1.5 py-px rounded-xs border border-border/70 text-muted-foreground tabular-nums">{{ p.level }} · {{ levelLabel(p.level) }}</span>
            <span v-if="p.is_default" class="text-[10px] px-1.5 py-px rounded-xs border border-primary/40 bg-primary/10 text-primary" data-testid="auth-profile-default-badge">默认</span>
            <span class="flex-1" />
            <button
              v-if="!p.is_default"
              type="button"
              :data-testid="`auth-profile-default-${p.id}`"
              :disabled="store.busy"
              class="text-[11px] flex items-center gap-1 text-muted-foreground hover:text-foreground cursor-pointer disabled:opacity-50"
              @click="makeDefault(p)"
            ><Star class="w-3 h-3" />设为默认</button>
            <button
              type="button"
              :data-testid="`auth-profile-edit-${p.id}`"
              :disabled="store.busy"
              class="text-[11px] flex items-center gap-1 text-muted-foreground hover:text-foreground cursor-pointer disabled:opacity-50"
              @click="startEdit(p)"
            ><Pencil class="w-3 h-3" />编辑</button>
            <button
              v-if="!p.is_default"
              type="button"
              :data-testid="`auth-profile-delete-${p.id}`"
              :disabled="store.busy"
              class="text-[11px] flex items-center gap-1 cursor-pointer disabled:opacity-50"
              :class="confirmingDelete === p.id ? 'text-destructive font-semibold' : 'text-muted-foreground hover:text-destructive'"
              @click="remove(p)"
            ><Trash2 class="w-3 h-3" />{{ confirmingDelete === p.id ? '确认删除' : '删除' }}</button>
          </div>
          <p class="text-[11px] text-muted-foreground leading-relaxed whitespace-pre-wrap break-words">{{ p.rules }}</p>
        </li>
      </ul>

      <!-- 新增 / 编辑 -->
      <form
        v-if="editing"
        class="p-3 rounded-xs border border-primary/40 bg-primary/5 space-y-2.5 font-mono"
        data-testid="auth-profile-form"
        @submit.prevent="save"
      >
        <div class="text-xs font-semibold text-foreground">{{ editing.id ? '编辑档位' : '新增档位' }}</div>
        <label class="block space-y-1">
          <span class="flex items-center justify-between text-[11px] text-muted-foreground">
            <span>名称</span>
            <span class="tabular-nums" :class="nameLen > store.limits.name_max ? 'text-destructive' : ''">{{ nameLen }} / {{ store.limits.name_max }}</span>
          </span>
          <input v-model="editing.name" data-testid="auth-profile-name" type="text" autocomplete="off" spellcheck="false" placeholder="例如：谨慎、夜间放手" :class="inputClass" />
        </label>
        <div class="space-y-1">
          <span class="text-[11px] text-muted-foreground">等级</span>
          <div class="flex flex-wrap gap-2" role="radiogroup" aria-label="档位等级">
            <button
              v-for="lv in LEVELS"
              :key="lv.id"
              type="button"
              role="radio"
              :aria-checked="editing.level === lv.id"
              :data-testid="`auth-profile-level-${lv.id}`"
              class="px-2.5 py-1 rounded-xs border text-[11px] cursor-pointer transition-colors"
              :class="editing.level === lv.id ? 'border-primary bg-primary/10 text-foreground font-semibold' : 'border-border/80 text-muted-foreground hover:text-foreground'"
              @click="editing.level = lv.id"
            >{{ lv.title }}</button>
          </div>
        </div>
        <label class="block space-y-1">
          <span class="flex items-center justify-between text-[11px] text-muted-foreground">
            <span>规则摘要（写给智能体看：哪些事可以直接做、哪些先问）</span>
            <span class="tabular-nums" :class="rulesLen > store.limits.rules_max ? 'text-destructive' : ''">{{ rulesLen }} / {{ store.limits.rules_max }}</span>
          </span>
          <textarea
            v-model="editing.rules"
            data-testid="auth-profile-rules"
            rows="4"
            spellcheck="false"
            placeholder="例如：读与分析、跑测试、工作树内可回滚的改动直接做；部署、推送远端、删数据先问。"
            class="w-full px-2.5 py-1.5 text-xs font-mono bg-background border border-border/80 rounded-xs focus:outline-none focus:border-primary text-foreground resize-y"
          />
        </label>
        <p v-if="formError" class="text-[11px] text-destructive" data-testid="auth-profile-form-error">{{ formError }}</p>
        <div class="flex items-center gap-2">
          <button
            type="submit"
            data-testid="auth-profile-save"
            :disabled="store.busy"
            class="px-3 h-7 rounded-xs bg-primary text-primary-foreground text-[11px] font-semibold cursor-pointer hover:brightness-110 disabled:opacity-60"
          >保存</button>
          <button type="button" data-testid="auth-profile-cancel" class="px-3 h-7 rounded-xs border border-border/80 text-[11px] text-muted-foreground hover:text-foreground cursor-pointer" @click="cancel">取消</button>
        </div>
      </form>

      <div v-else class="flex items-center gap-3">
        <button
          type="button"
          data-testid="auth-profile-add"
          :disabled="atLimit || store.busy"
          class="px-3 h-7 rounded-xs border border-border/80 text-[11px] font-mono flex items-center gap-1.5 text-foreground hover:bg-muted cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed"
          @click="startAdd"
        ><Plus class="w-3 h-3" />新增档位</button>
        <span class="text-[10px] font-mono text-muted-foreground tabular-nums" data-testid="auth-profile-count">{{ store.profiles.length }} / {{ store.limits.max }}</span>
        <span v-if="atLimit" class="text-[10px] font-mono text-amber-600 dark:text-amber-400">已到上限，先删掉不用的</span>
      </div>
    </template>

    <div v-else class="text-[11px] text-muted-foreground">读取中…</div>
  </div>
</template>
