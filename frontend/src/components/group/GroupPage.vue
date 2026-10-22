<script setup lang="ts">
// 群聊页：/group 是全部群的列表（可新建），/group/:groupId 是一个群的聊天窗口。与会话页共用同一个壳，左侧栏不变。
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Plus, Users } from 'lucide-vue-next'
import { isGroupId } from '../../api/conversations'
import { groupPreviewLine } from '../../lib/group'
import { clockTime } from '../../lib/chatView'
import { useGroupStore } from '../../stores/groups'
import GroupChatPane from './GroupChatPane.vue'
import GroupCreateDialog from './GroupCreateDialog.vue'
import Button from '../ui/button/Button.vue'
import PageHeader from '../shell/PageHeader.vue'
import PageBackButton from '../shell/PageBackButton.vue'
import PageState from '../shell/PageState.vue'

defineProps<{ sidebarCollapsed?: boolean }>()
const emit = defineEmits<{ (e: 'open-mobile-sidebar'): void; (e: 'expand-sidebar'): void }>()

const route = useRoute()
const router = useRouter()
const groups = useGroupStore()
const createOpen = ref(false)

const groupId = computed(() => {
  const p = route.params.groupId
  return Array.isArray(p) ? p[0] ?? '' : (p as string | undefined) ?? ''
})

onMounted(() => void groups.load())

function open(id: string) {
  void router.push({ name: 'group', params: { groupId: id } })
}
function backToList() {
  void router.push({ name: 'group' })
}
</script>

<template>
  <div class="flex h-full min-h-0 flex-col bg-background text-foreground" data-testid="group-page">
    <!-- 页头骨架与任务清单 / 项目 / 文档页共用（PageHeader）。群里面只留「全部群聊」返回（群名在聊天窗口自己的顶部）；列表页是图标 + 标题 + 「显示已归档」，主操作「新建群聊」靠右 -->
    <PageHeader :sidebar-collapsed="sidebarCollapsed" @open-mobile-sidebar="emit('open-mobile-sidebar')" @expand-sidebar="emit('expand-sidebar')">
      <template v-if="groupId" #leading>
        <PageBackButton label="全部群聊" always-label data-testid="group-back" @click="backToList" />
      </template>

      <template v-if="!groupId">
        <Users class="size-4 shrink-0 text-primary" aria-hidden="true" />
        <span class="shrink-0 text-sm font-semibold">群聊</span>
        <label class="ml-1 flex min-w-0 cursor-pointer items-center gap-1.5 text-[11px] text-muted-foreground sm:ml-3">
          <input type="checkbox" class="size-3 shrink-0" :checked="groups.includeArchived" data-testid="group-show-archived" @change="groups.setIncludeArchived(($event.target as HTMLInputElement).checked)" />
          <span class="truncate">显示已归档</span>
        </label>
      </template>

      <template v-if="!groupId" #actions>
        <Button size="sm" class="h-8 shrink-0 gap-1 text-xs" data-testid="group-create-open" @click="createOpen = true">
          <Plus class="size-3.5" />新建群聊
        </Button>
      </template>
    </PageHeader>

    <GroupChatPane v-if="groupId && isGroupId(groupId)" :key="groupId" :workflow-id="groupId" />

    <!-- 地址里的编号不是群（手动改了地址）：不当成群打开，指回群列表 -->
    <div v-else-if="groupId" class="min-h-0 flex-1 overflow-y-auto px-4 py-4">
      <PageState kind="error" title="这不是一个群聊" hint="这个地址指向的不是群聊。回到「全部群聊」里选一个群。" class="mx-auto max-w-md" data-testid="group-invalid">
        <template #actions>
          <Button variant="outline" size="sm" class="h-8 text-xs" data-testid="group-invalid-back" @click="backToList">全部群聊</Button>
        </template>
      </PageState>
    </div>

    <div v-else class="min-h-0 flex-1 overflow-y-auto px-4 py-4">
      <!-- 列表还是空的时整块显示错误并给「重试」；已经有列表时只在上面挂一条提示，不把现有的群挤走 -->
      <PageState v-if="groups.error && !groups.list.length" kind="error" title="群聊列表加载失败" :hint="groups.error" data-testid="group-list-error">
        <template #actions>
          <Button variant="outline" size="sm" class="h-8 text-xs" data-testid="group-list-retry" @click="groups.load()">重试</Button>
        </template>
      </PageState>
      <div v-else-if="groups.error" class="mb-3 rounded-md border border-destructive/40 px-3 py-2 text-[12px] text-destructive" data-testid="group-list-error">{{ groups.error }}</div>
      <PageState
        v-if="groups.loaded && !groups.list.length"
        kind="empty"
        :icon="Users"
        title="还没有群聊"
        hint="把几个开着「聊天模式」的 Agent 拉进同一个群，它们就能互相 @ 协作，你也能随时插话。"
        class="mx-auto max-w-md"
        data-testid="group-empty"
      >
        <template #actions>
          <Button size="sm" class="h-8 text-xs" data-testid="group-empty-create" @click="createOpen = true">新建第一个群聊</Button>
        </template>
      </PageState>
      <ul class="mx-auto max-w-2xl space-y-2" data-testid="group-list">
        <li v-for="g in groups.list" :key="g.id">
          <button
            type="button"
            class="flex w-full cursor-pointer items-start gap-3 rounded-md border border-border/70 px-3 py-2.5 text-left hover:bg-accent/50"
            :class="g.archived ? 'opacity-60' : ''"
            data-testid="group-item"
            :data-group-id="g.id"
            @click="open(g.id)"
          >
            <Users class="mt-0.5 size-4 shrink-0 text-muted-foreground" />
            <span class="min-w-0 flex-1">
              <span class="flex items-center gap-2">
                <span class="truncate text-[13px] font-medium">{{ g.title }}</span>
                <span class="shrink-0 text-[11px] text-muted-foreground">{{ g.member_count }} 位成员</span>
                <span v-if="g.archived" class="shrink-0 rounded-full border border-border px-1.5 text-[10px] text-muted-foreground">已归档</span>
                <span v-if="g.cross_project" class="shrink-0 rounded-full border border-amber-500/40 px-1.5 text-[10px] text-amber-700 dark:text-amber-400">跨项目</span>
              </span>
              <span class="mt-0.5 block truncate text-[12px] text-muted-foreground">{{ groupPreviewLine(g) }}</span>
            </span>
            <span class="flex shrink-0 flex-col items-end gap-1">
              <span v-if="g.last" class="font-mono text-[10px] text-muted-foreground">{{ clockTime(g.last.sent_at) }}</span>
              <span v-if="g.mentioned > 0" class="rounded-full bg-amber-500 px-1.5 text-[10px] font-medium text-white" data-testid="group-item-mentioned">@我 {{ g.mentioned }}</span>
              <span v-else-if="g.unread > 0" class="rounded-full bg-primary px-1.5 text-[10px] font-medium text-primary-foreground" data-testid="group-item-unread">{{ g.unread > 99 ? '99+' : g.unread }}</span>
            </span>
          </button>
        </li>
      </ul>
    </div>

    <GroupCreateDialog v-model:open="createOpen" @created="open" />
  </div>
</template>
