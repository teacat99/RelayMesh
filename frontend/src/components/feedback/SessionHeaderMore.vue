<script setup lang="ts">
// 手机会话页头的「⋯」菜单（#156 批 1、#168）：把次要动作——这个会话所属项目的知识库、提醒策略、会话工作表——收到一个按钮里，
// 把页头的宽度让给标题（400px 宽的页头原来被 5 个按钮挤到只剩 1~2 个字的标题）。PC 不用它：那里宽度够，仍然全部展开。
// 页头上只留 终端 / 任务 / ⋯ 三个同样大小（36×36、图标 16px）的按钮；提醒被静音时，⋯ 的右上角亮一个小黄点，不用点开也看得出来。
// 「项目」放在最上面：它是除了终端和任务以外最常去的地方；工作流归属的项目认出来后，⋯ 上带 data-project-id（与 PC 上的「项目」按钮同一个属性）。
import { FileText, FolderGit2, MoreHorizontal } from 'lucide-vue-next'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from '../ui/dropdown-menu'
import { useConversationNotify } from '../../composables/useConversationNotify'
import ConversationNotifyItems from './ConversationNotifyItems.vue'

const props = defineProps<{
  workflowId: string
  /** 这个工作流归属的项目编号；0 / 没传 = 还没归入项目（点「项目」仍打开老的文档页）。 */
  projectId?: number
  /** 「项目」条目的悬停说明（与 PC 页头上的「项目」按钮同一句话）。 */
  projectTitle?: string
  /** 这个工作流里的文档篇数，大于 0 时在「项目」条目右侧显示角标。 */
  docsCount?: number | null
}>()
const emit = defineEmits<{ (e: 'open-sheet'): void; (e: 'open-project'): void }>()

const { muted, note } = useConversationNotify(() => props.workflowId)
</script>

<template>
  <DropdownMenu>
    <DropdownMenuTrigger as-child>
      <button
        type="button"
        class="relative inline-flex size-9 shrink-0 cursor-pointer items-center justify-center rounded-sm border border-border/80 bg-muted/40 shadow-xs outline-none transition-colors hover:bg-muted/80 hover:text-primary focus-visible:ring-1 focus-visible:ring-ring"
        :title="muted ? `更多：项目、提醒策略（已静音：${note}）、会话工作表` : '更多：项目、提醒策略、会话工作表'"
        aria-label="更多"
        :data-muted="muted ? 'true' : 'false'"
        :data-project-id="props.projectId || undefined"
        data-testid="session-more-menu"
      >
        <MoreHorizontal class="size-4 text-primary" />
        <span v-if="muted" class="absolute -right-1 -top-1 size-2.5 rounded-full bg-amber-500 ring-2 ring-background" aria-hidden="true" data-testid="session-more-muted-dot" />
      </button>
    </DropdownMenuTrigger>
    <DropdownMenuContent align="end" class="min-w-[15rem] max-w-[min(20rem,calc(100vw-1rem))] text-xs" data-testid="session-more-menu-content">
      <DropdownMenuItem class="text-xs" data-testid="session-project-entry" :title="props.projectTitle" @select="emit('open-project')">
        <FolderGit2 class="!size-3.5 text-primary" />项目知识库
        <span
          v-if="props.docsCount"
          class="ml-auto min-w-4 rounded-xs border border-border px-1 text-center text-[10px] leading-4 tabular-nums text-muted-foreground"
          :title="`这个工作流里有 ${props.docsCount} 篇文档`"
          data-testid="session-project-badge"
        >{{ props.docsCount }}</span>
      </DropdownMenuItem>
      <DropdownMenuSeparator />
      <ConversationNotifyItems :workflow-id="props.workflowId" />
      <DropdownMenuSeparator />
      <DropdownMenuItem class="text-xs" data-testid="session-sheet-entry" title="打开会话工作表 (Workflow Sheet Drawer)" @select="emit('open-sheet')">
        <FileText class="!size-3.5 text-primary" />打开会话工作表
      </DropdownMenuItem>
    </DropdownMenuContent>
  </DropdownMenu>
</template>
