<script setup lang="ts">
// 群成员面板：成员与实时状态、每位 Agent 的推送策略、移出 / 加入成员、改名、归档。
// 所有改动都先落服务端再刷新，不做乐观更新：成员关系和推送策略决定「谁会收到什么」，宁可慢一拍也不能显示成假的。
import { computed, ref, watch } from 'vue'
import { Archive, ArchiveRestore, Plus, UserMinus, X } from 'lucide-vue-next'
import { toast } from 'vue-sonner'
import { chatErrorMessage } from '../../api/conversations'
import { groupsApi, type GroupInfo, type GroupMember, type GroupPushPolicy } from '../../api/groups'
import { memberStatusText, memberTone, policyLabel } from '../../lib/group'
import { memberIdentity } from '../../lib/groupCard'
import type { ConvPolicy } from '../../lib/notifyPrefs'
import { useGroupStore } from '../../stores/groups'
import { useNotifyPrefsStore } from '../../stores/notifyPrefs'
import MemberAvatar from './MemberAvatar.vue'

// hues：群成员的色相表（lib/groupCard 的 assignMemberHues）。给了就画成「成员头像 + 状态点」（预览视图，E-d2）；不给就是原来的纯状态点（经典，不变）。
const props = defineProps<{ info: GroupInfo; hues?: Record<string, number> }>()
const emit = defineEmits<{ (e: 'close'): void; (e: 'changed'): void }>()

const groups = useGroupStore()
const prefs = useNotifyPrefsStore()
const title = ref(props.info.title)
const busy = ref(false)
const adding = ref(false)
const picked = ref<string[]>([])
watch(() => props.info.title, t => (title.value = t))

const active = computed(() => props.info.members.filter(m => !m.left))
const agents = computed(() => active.value.filter(m => m.kind === 'workflow'))
const human = computed(() => active.value.find(m => m.kind === 'human'))
const inGroup = computed(() => new Set(agents.value.map(m => m.ref)))
const addable = computed(() => groups.candidates.filter(c => !inGroup.value.has(c.workflow_id)))
/** 这个群给「你」的通知策略（服务端偏好，浏览器与手机推送一起生效）：默认只在被 @ 时通知。 */
const notifyPolicy = computed(() => prefs.policy(props.info.id))
function setNotifyPolicy(p: ConvPolicy) {
  if (p === 'mentions') prefs.resetConversation(props.info.id)
  else prefs.setConversationPolicy(props.info.id, p)
}
/** 带头像时状态行 / 推送行要缩进到名字下面：头像 20px + 间距 8px = pl-7；原来的状态点 8px + 间距 8px = pl-4。 */
const indentClass = computed(() => (props.hues ? 'pl-7' : 'pl-4'))
const titleDirty = computed(() => title.value.trim() !== '' && title.value.trim() !== props.info.title)

const TONE_CLASS: Record<string, string> = {
  active: 'bg-emerald-500',
  attention: 'bg-amber-500',
  danger: 'bg-red-500',
  muted: 'bg-muted-foreground/40',
}

async function run(action: () => Promise<void>, fail: string) {
  if (busy.value) return
  busy.value = true
  try {
    await action()
    emit('changed')
  } catch (e) {
    toast.error(chatErrorMessage(e, fail))
  } finally {
    busy.value = false
  }
}

const saveTitle = () => run(() => groupsApi.update(props.info.id, { title: title.value.trim() }), '改名失败')
const toggleArchive = () => run(() => groupsApi.update(props.info.id, { archived: !props.info.archived }), '操作失败')
const remove = (m: GroupMember) => run(() => groupsApi.removeMember(props.info.id, m.ref), '移出失败')
const setPolicy = (m: GroupMember, policy: GroupPushPolicy) => run(() => groupsApi.setPolicy(props.info.id, m.ref, { push_policy: policy }), '修改推送策略失败')

async function openAdd() {
  adding.value = !adding.value
  picked.value = []
  if (adding.value) {
    try {
      await groups.loadCandidates()
    } catch (e) {
      toast.error(chatErrorMessage(e, '候选成员加载失败'))
    }
  }
}
async function addPicked() {
  if (!picked.value.length) return
  await run(async () => {
    await groupsApi.addMembers(props.info.id, picked.value)
    adding.value = false
    picked.value = []
  }, '加入失败')
}

function dirName(p: string | undefined): string {
  const parts = (p ?? '').split('/').filter(Boolean)
  return parts[parts.length - 1] ?? ''
}
</script>

<template>
  <aside class="flex h-full w-80 max-w-full flex-col border-l border-border bg-background" data-testid="group-members-panel">
    <header class="flex items-center gap-2 border-b border-border px-3 py-2">
      <span class="flex-1 text-[13px] font-medium">群成员 · {{ agents.length + (human ? 1 : 0) }}</span>
      <button type="button" class="cursor-pointer text-muted-foreground hover:text-foreground" title="关闭" data-testid="group-members-close" @click="emit('close')">
        <X class="size-4" />
      </button>
    </header>

    <div class="min-h-0 flex-1 space-y-3 overflow-y-auto px-3 py-3">
      <!-- 群名 -->
      <div class="flex items-center gap-1.5">
        <input
          v-model="title"
          maxlength="60"
          class="h-8 min-w-0 flex-1 rounded-md border border-input bg-background px-2 text-[13px] outline-none focus-visible:ring-1 focus-visible:ring-ring"
          data-testid="group-title-input"
          @keydown.enter="titleDirty && saveTitle()"
        />
        <button
          v-if="titleDirty"
          type="button"
          class="h-8 cursor-pointer rounded-md bg-primary px-2.5 text-[12px] text-primary-foreground disabled:opacity-50"
          :disabled="busy"
          data-testid="group-title-save"
          @click="saveTitle"
        >
          保存
        </button>
      </div>

      <label class="flex items-center gap-2 text-[11px] text-muted-foreground">
        通知
        <select
          class="h-7 min-w-0 flex-1 rounded border border-input bg-background px-1.5 text-[12px] text-foreground"
          :value="notifyPolicy"
          data-testid="group-notify-policy"
          @change="setNotifyPolicy(($event.target as HTMLSelectElement).value as ConvPolicy)"
        >
          <option value="mentions">只在 @我 时通知（默认）</option>
          <option value="all">群里的所有消息</option>
          <option value="muted">静音（@我 仍会通知）</option>
        </select>
      </label>

      <p v-if="info.cross_project" class="rounded-md bg-amber-500/10 px-2 py-1.5 text-[11px] text-amber-800 dark:text-amber-300" data-testid="group-cross-project">
        这个群的成员来自不同项目（{{ (info.projects ?? []).join('、') }}）。群里的消息会被所有成员读到，注意不要说不该共享的内容。
      </p>

      <ul class="space-y-2" data-testid="group-member-list">
        <li v-if="human" class="flex items-center gap-2 text-[13px]">
          <MemberAvatar v-if="hues" :identity="memberIdentity(human, hues)" size="md" />
          <span v-else class="size-2 shrink-0 rounded-full bg-primary" />
          <span class="font-medium">你</span>
          <span class="text-[11px] text-muted-foreground">群主</span>
        </li>
        <li v-for="m in agents" :key="m.ref" class="rounded-md border border-border/70 px-2.5 py-2" data-testid="group-member" :data-ref="m.ref">
          <div class="flex items-center gap-2">
            <MemberAvatar v-if="hues" :identity="memberIdentity(m, hues)" size="md" :dot-class="TONE_CLASS[memberTone(m)]" />
            <span v-else class="size-2 shrink-0 rounded-full" :class="TONE_CLASS[memberTone(m)]" />
            <span class="min-w-0 flex-1 truncate text-[13px] font-medium" :title="m.ref">{{ m.name }}</span>
            <button
              type="button"
              class="cursor-pointer text-muted-foreground hover:text-destructive disabled:opacity-40"
              :disabled="busy"
              title="移出群聊"
              data-testid="group-member-remove"
              @click="remove(m)"
            >
              <UserMinus class="size-3.5" />
            </button>
          </div>
          <div class="mt-0.5 truncate text-[11px] text-muted-foreground" :class="indentClass" data-testid="group-member-status">
            {{ memberStatusText(m) || '状态未知' }}<template v-if="dirName(m.project_directory)"> · {{ dirName(m.project_directory) }}</template>
          </div>
          <label class="mt-1.5 flex items-center gap-2 text-[11px] text-muted-foreground" :class="indentClass">
            推送
            <select
              class="h-6 min-w-0 flex-1 rounded border border-input bg-background px-1 text-[11px]"
              :value="m.push_policy"
              :disabled="busy"
              data-testid="group-member-policy"
              @change="setPolicy(m, ($event.target as HTMLSelectElement).value as GroupPushPolicy)"
            >
              <option v-for="p in ['mention', 'all', 'none']" :key="p" :value="p">{{ policyLabel(p) }}</option>
            </select>
          </label>
        </li>
        <li v-if="!agents.length" class="text-[12px] text-muted-foreground">群里还没有 Agent 成员。</li>
      </ul>

      <!-- 加入成员 -->
      <div>
        <button
          type="button"
          class="inline-flex cursor-pointer items-center gap-1 rounded-md border border-border px-2 py-1 text-[12px] text-muted-foreground hover:bg-accent hover:text-accent-foreground"
          data-testid="group-add-toggle"
          @click="openAdd"
        >
          <Plus class="size-3.5" />{{ adding ? '收起' : '添加成员' }}
        </button>
        <div v-if="adding" class="mt-2 space-y-1.5" data-testid="group-add-list">
          <p v-if="groups.candidatesLoading" class="text-[11px] text-muted-foreground">加载中…</p>
          <p v-else-if="!addable.length" class="text-[11px] text-muted-foreground">没有可以加入的工作流了。</p>
          <label
            v-for="c in addable"
            :key="c.workflow_id"
            class="flex items-start gap-2 rounded px-1 py-1 text-[12px]"
            :class="c.eligible ? 'cursor-pointer hover:bg-accent/60' : 'opacity-60'"
          >
            <input v-model="picked" type="checkbox" class="mt-0.5 size-3.5" :value="c.workflow_id" :disabled="!c.eligible" />
            <span class="min-w-0">
              <span class="block truncate">{{ c.name || c.workflow_id }}</span>
              <span v-if="!c.eligible" class="block text-[10px] text-muted-foreground">{{ c.reason }}</span>
            </span>
          </label>
          <button
            v-if="addable.length"
            type="button"
            class="h-7 cursor-pointer rounded-md bg-primary px-3 text-[12px] text-primary-foreground disabled:opacity-40"
            :disabled="busy || !picked.length"
            data-testid="group-add-confirm"
            @click="addPicked"
          >
            加入 {{ picked.length || '' }}
          </button>
        </div>
      </div>

      <button
        type="button"
        class="inline-flex cursor-pointer items-center gap-1 text-[12px] text-muted-foreground hover:text-foreground disabled:opacity-50"
        :disabled="busy"
        data-testid="group-archive-toggle"
        @click="toggleArchive"
      >
        <component :is="info.archived ? ArchiveRestore : Archive" class="size-3.5" />{{ info.archived ? '取消归档' : '归档这个群' }}
      </button>
    </div>
  </aside>
</template>
