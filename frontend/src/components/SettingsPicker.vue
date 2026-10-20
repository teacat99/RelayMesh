<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { ChevronDown } from 'lucide-vue-next'
import { useSettingsUiStore } from '../stores/settingsUi'
import { categoryById } from '../lib/settingsRegistry'
import { moveCategory, navGroups } from '../lib/settingsNav'
import { SETTINGS_ICONS } from './settings/icons'

// 窄屏（<768px）的设置分类选择条：一条显示「当前分类」的按钮，点开后在原位展开分类列表（分组与顺序和宽屏左栏一致），
// 选中即收起。不用浮层：设置窗口本身就在浮层里，原位展开不会被裁剪，也不会和软键盘抢位置。
const ui = useSettingsUiStore()

const open = ref(false)
const root = ref<HTMLElement | null>(null)
const groups = computed(() => navGroups(ui.module))
const current = computed(() => categoryById(ui.category))

function pick(id: string) {
  ui.select(id)
  open.value = false
}

// 展开时上下键 / Home / End 与宽屏左栏一样移动并切换（Esc 留给窗口本身：对话框在捕获阶段处理它，这里拦不住也不该拦）
function onKeydown(e: KeyboardEvent) {
  if (!open.value || e.altKey || e.ctrlKey || e.metaKey) return
  const next = moveCategory(ui.module, ui.category, e.key)
  if (!next) return
  e.preventDefault()
  ui.select(next)
  void nextTick(() => root.value?.querySelector<HTMLElement>(`[data-category="${next}"]`)?.focus())
}

// 换了模块，旧列表的内容已经不对了：收起来，让人看到新模块的「当前分类」
watch(() => ui.module, () => { open.value = false })
</script>

<template>
  <div ref="root" data-testid="settings-picker" @keydown="onKeydown">
    <button
      type="button"
      class="flex w-full cursor-pointer items-center gap-2.5 rounded-sm border border-border/80 bg-card px-3 py-2 text-left font-mono transition-colors hover:bg-muted/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      :class="open ? 'border-primary/50 bg-muted/30' : ''"
      :aria-expanded="open"
      aria-controls="settings-picker-list"
      data-testid="settings-picker-toggle"
      @click="open = !open"
    >
      <component :is="SETTINGS_ICONS[ui.category]" class="size-4 shrink-0 text-primary" aria-hidden="true" />
      <span class="min-w-0 flex-1">
        <span class="block text-[10px] leading-tight text-muted-foreground">当前分类</span>
        <span class="block truncate text-xs font-bold leading-snug text-foreground" data-testid="settings-picker-current">{{ current?.label }}</span>
      </span>
      <ChevronDown class="size-4 shrink-0 text-muted-foreground transition-transform duration-150" :class="open ? 'rotate-180' : ''" aria-hidden="true" />
    </button>

    <div
      v-if="open"
      id="settings-picker-list"
      role="tablist"
      aria-orientation="vertical"
      class="mt-1.5 max-h-[55dvh] overflow-y-auto rounded-sm border border-border/80 bg-card p-1.5 shadow-md"
      data-testid="settings-picker-list"
    >
      <div v-for="g in groups" :key="g.key" role="presentation" class="mt-2 first:mt-0">
        <p class="px-2.5 pb-1 text-[10px] font-mono font-medium tracking-wider text-muted-foreground/80">{{ g.title }}</p>
        <button
          v-for="c in g.items"
          :key="c.id"
          type="button"
          role="tab"
          :aria-selected="ui.category === c.id"
          aria-controls="settings-panel"
          :tabindex="ui.category === c.id ? 0 : -1"
          :data-testid="`settings-tab-${c.id}`"
          :data-category="c.id"
          class="relative flex min-h-11 w-full cursor-pointer select-none items-center gap-2.5 rounded-sm px-2.5 py-2.5 text-left text-xs font-mono transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          :class="ui.category === c.id
            ? 'bg-primary/10 font-bold text-foreground before:absolute before:inset-y-1.5 before:left-0 before:w-0.5 before:rounded-full before:bg-primary'
            : 'text-muted-foreground hover:bg-muted hover:text-foreground'"
          @click="pick(c.id)"
        >
          <component :is="SETTINGS_ICONS[c.id]" class="size-3.5 shrink-0" :class="ui.category === c.id ? 'text-primary' : ''" aria-hidden="true" />
          <span class="min-w-0 flex-1 truncate">{{ c.label }}</span>
        </button>
      </div>
    </div>
  </div>
</template>
