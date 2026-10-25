<script setup lang="ts">
// 侧栏的「群聊」入口：一行总入口（带未读 / @我 徽标）+ 最近有动静的几个群。点进去是群聊页。
//
// 占位（#246）：群列表要等 GET /api/v1/groups 回来才有，生产上它与会话页第一轮内容差不多同时到；列表一出来，下面的搜索框、筛选、
// 会话列表整体被推下去（4 个群约 114px），与会话页的其它位移落在同一帧里。所以记住上次列了几行（lib/shellHints.ts），
// 没回来之前先画同样高的骨架行，到了原地换成真行；取失败不占位，上次没有群也不占位。
import { computed, onMounted, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Users } from 'lucide-vue-next'
import { readSidebarGroupRows, writeSidebarGroupRows } from '../../lib/shellHints'
import { useGroupStore } from '../../stores/groups'

const MAX_SHOWN = 5
// 手机上侧栏是抽屉：进了页面就该把抽屉收起来（navigate 通知外层关抽屉）
const emit = defineEmits<{ (e: 'navigate'): void }>()
const route = useRoute()
const router = useRouter()
const groups = useGroupStore()

const active = computed(() => route.name === 'group')
const activeId = computed(() => String(route.params.groupId ?? ''))
const shown = computed(() => groups.list.filter(g => !g.archived).slice(0, MAX_SHOWN))
const badge = computed(() => groups.totals.unread)

// 上次列了几行：挂载时读一次（取到群之后改写成这次的行数，下一次挂载才用）
const rowsHint = readSidebarGroupRows()
const reservedRows = computed(() => (groups.loaded || groups.error ? 0 : rowsHint))
watch(
  () => [groups.loaded, shown.value.length] as const,
  ([loaded, n]) => {
    if (loaded) writeSidebarGroupRows(n)
  },
  { immediate: true },
)

onMounted(() => void groups.load())

function openAll() {
  emit('navigate')
  void router.push({ name: 'group' })
}
function open(id: string) {
  emit('navigate')
  void router.push({ name: 'group', params: { groupId: id } })
}
</script>

<template>
  <div class="px-2.5 pt-1.5" data-testid="sidebar-groups">
    <button
      type="button"
      class="flex w-full cursor-pointer items-center gap-2 rounded-sm border px-2.5 py-1.5 text-xs transition-colors"
      :class="active && !activeId ? 'border-primary/30 bg-primary/10 text-foreground' : 'border-border/60 bg-background/60 text-muted-foreground hover:bg-muted/60 hover:text-foreground'"
      :aria-current="active && !activeId ? 'page' : undefined"
      title="群聊：把多个 Agent 拉进同一个群协作"
      data-testid="sidebar-group-entry"
      data-prefetch="group"
      @click="openAll"
    >
      <Users class="size-3.5 shrink-0" />
      <span class="font-medium">群聊</span>
      <span v-if="groups.totals.mentioned > 0" class="ml-auto rounded-full bg-amber-500 px-1.5 text-[10px] font-medium text-white" data-testid="sidebar-group-mentioned">@我 {{ groups.totals.mentioned }}</span>
      <span v-else-if="badge > 0" class="ml-auto rounded-full bg-primary px-1.5 text-[10px] font-medium text-primary-foreground" data-testid="sidebar-group-unread">{{ badge > 99 ? '99+' : badge }}</span>
    </button>
    <!-- 占位：群列表还在路上，上次列了几行就先画几行同样高的骨架（行的盒子与真行一致：py-1、12px 字，行高才一致）。对读屏隐藏 -->
    <ul v-if="reservedRows > 0 && !shown.length" class="mt-1 space-y-0.5" aria-hidden="true" data-testid="sidebar-groups-reserved">
      <li v-for="i in reservedRows" :key="i" data-testid="sidebar-group-reserved">
        <div class="flex w-full items-center gap-2 rounded-sm px-2.5 py-1 text-left text-[12px]">
          <span class="min-w-0 flex-1 truncate"><span class="inline-block h-3 w-24 rounded-full bg-muted/60 align-middle motion-safe:animate-pulse"></span></span>
        </div>
      </li>
    </ul>
    <ul v-if="shown.length" class="mt-1 space-y-0.5">
      <li v-for="g in shown" :key="g.id">
        <button
          type="button"
          class="flex w-full cursor-pointer items-center gap-2 rounded-sm px-2.5 py-1 text-left text-[12px] transition-colors"
          :class="activeId === g.id ? 'bg-primary/10 text-foreground' : 'text-muted-foreground hover:bg-muted/60 hover:text-foreground'"
          data-testid="sidebar-group-item"
          :data-group-id="g.id"
          @click="open(g.id)"
        >
          <span class="min-w-0 flex-1 truncate">{{ g.title }}</span>
          <span v-if="g.mentioned > 0" class="shrink-0 text-[10px] font-medium text-amber-600 dark:text-amber-400" data-testid="sidebar-group-item-mentioned">@我 {{ g.mentioned > 99 ? '99+' : g.mentioned }}</span>
          <span v-else-if="g.unread > 0" class="shrink-0 rounded-full bg-primary/80 px-1.5 text-[10px] text-primary-foreground" data-testid="sidebar-group-item-unread">{{ g.unread > 99 ? '99+' : g.unread }}</span>
        </button>
      </li>
    </ul>
  </div>
</template>
