<script setup lang="ts">
// 项目「管理」页签（#156 批 3）：原来并排放在页签栏里的 设置 / 迁移 / 授权 / 审计 四个页，收进这里当四个分区。
// 都是低频的管理类操作，常用的是「知识库」与「模块」，所以页签栏只留三个，这里再分一层：
//   宽屏（≥1024px，主区够宽）：左侧一列竖排的分区导航（做法与设置窗口左侧的分类列表一致）；
//   窄屏：顶上一行胶囊（四个一行放得下，滚动时吸顶，随时能换分区）。
// 同一个 <nav>，靠响应式 class 变形，DOM 里只有一份（测试 id 不重复）。分区由地址里的 ?tab= 决定（见 lib/projectTabs），本组件只发 select。
import { computed, nextTick, ref, type Component } from 'vue'
import { useMediaQuery } from '@vueuse/core'
import { ArrowRightLeft, ScrollText, Settings2, ShieldCheck } from 'lucide-vue-next'
import { MANAGE_SECTIONS, moveManageSection, type ManageSection } from '../../lib/projectTabs'
import ProjectSettingsTab from './ProjectSettingsTab.vue'
import ProjectMigrationTab from './ProjectMigrationTab.vue'
import ProjectGrantsTab from './ProjectGrantsTab.vue'
import ProjectAuditTab from './ProjectAuditTab.vue'

const props = defineProps<{ projectId: number; section: ManageSection }>()
const emit = defineEmits<{ (e: 'select', section: ManageSection): void; (e: 'archived'): void }>()

const ICONS: Record<ManageSection, Component> = {
  settings: Settings2,
  migration: ArrowRightLeft,
  grants: ShieldCheck,
  audit: ScrollText,
}

// ≥1024px 是竖排（左栏），否则横排（胶囊）：决定 aria-orientation 与方向键的含义（两种键都认，见 moveManageSection）
const vertical = useMediaQuery('(min-width: 1024px)')
const list = ref<HTMLElement | null>(null)
const current = computed(() => MANAGE_SECTIONS.find(s => s.id === props.section) ?? MANAGE_SECTIONS[0])

function onKeydown(e: KeyboardEvent) {
  if (e.altKey || e.ctrlKey || e.metaKey) return
  const next = moveManageSection(props.section, e.key)
  if (!next) return
  e.preventDefault()
  if (next !== props.section) emit('select', next)
  void nextTick(() => list.value?.querySelector<HTMLElement>(`[data-section="${next}"]`)?.focus())
}
</script>

<template>
  <div class="flex min-h-full flex-col lg:flex-row" data-testid="project-manage">
    <nav
      class="sticky top-0 z-10 shrink-0 border-b border-border/60 bg-background/95 backdrop-blur-xs lg:w-48 lg:self-start lg:border-b-0 lg:bg-transparent lg:p-3 lg:backdrop-blur-none"
      aria-label="项目管理分区"
      data-testid="project-manage-nav"
    >
      <div
        ref="list"
        role="tablist"
        :aria-orientation="vertical ? 'vertical' : 'horizontal'"
        class="flex gap-1.5 overflow-x-auto px-3 py-2 lg:flex-col lg:gap-0.5 lg:overflow-visible lg:p-0"
        @keydown="onKeydown"
      >
        <button
          v-for="s in MANAGE_SECTIONS"
          :id="`project-manage-btn-${s.id}`"
          :key="s.id"
          type="button"
          role="tab"
          :aria-selected="section === s.id"
          aria-controls="project-manage-panel"
          :tabindex="section === s.id ? 0 : -1"
          :title="s.hint"
          :data-section="s.id"
          :data-testid="`project-manage-${s.id}`"
          class="relative inline-flex h-8 shrink-0 cursor-pointer select-none items-center gap-1.5 rounded-full border px-3 text-[12px] transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring lg:h-auto lg:w-full lg:rounded-sm lg:border-0 lg:px-2.5 lg:py-2 lg:text-left"
          :class="section === s.id
            ? 'border-primary/40 bg-primary/10 font-medium text-foreground lg:font-bold lg:before:absolute lg:before:inset-y-1.5 lg:before:left-0 lg:before:w-0.5 lg:before:rounded-full lg:before:bg-primary'
            : 'border-border/70 text-muted-foreground hover:bg-accent/50 hover:text-foreground lg:hover:bg-muted'"
          @click="emit('select', s.id)"
        >
          <component :is="ICONS[s.id]" class="size-3.5 shrink-0" :class="section === s.id ? 'text-primary' : ''" aria-hidden="true" />
          <span class="min-w-0 truncate">{{ s.label }}</span>
        </button>
      </div>
    </nav>

    <div
      id="project-manage-panel"
      role="tabpanel"
      :aria-labelledby="`project-manage-btn-${current.id}`"
      class="min-w-0 flex-1 lg:border-l lg:border-border/60"
      data-testid="project-manage-panel"
    >
      <ProjectSettingsTab v-if="section === 'settings'" :project-id="projectId" @archived="emit('archived')" />
      <ProjectMigrationTab v-else-if="section === 'migration'" :project-id="projectId" />
      <ProjectGrantsTab v-else-if="section === 'grants'" :project-id="projectId" />
      <ProjectAuditTab v-else :project-id="projectId" />
    </div>
  </div>
</template>
