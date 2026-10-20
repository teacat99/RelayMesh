<script setup lang="ts">
import { computed, nextTick, ref } from 'vue'
import { useSettingsUiStore } from '../stores/settingsUi'
import { moveCategory, navGroups } from '../lib/settingsNav'
import type { AppMode } from '../lib/mode'
import { SETTINGS_ICONS } from './settings/icons'
import SettingsModuleSwitch from './SettingsModuleSwitch.vue'

// 宽屏（≥768px）的设置分类导航：窗口左侧一整列，顶部是模块分段切换，下面是「本模块 / 通用」两组分类。
// 竖向标签列表：上下键移动并立即切换，Home / End 跳到首尾（顺序与分组一致，见 lib/settingsNav）。
const emit = defineEmits<{ jump: [next: AppMode] }>()
const ui = useSettingsUiStore()

const groups = computed(() => navGroups(ui.module))
const list = ref<HTMLElement | null>(null)

function onKeydown(e: KeyboardEvent) {
  if (e.altKey || e.ctrlKey || e.metaKey) return
  const next = moveCategory(ui.module, ui.category, e.key)
  if (!next) return
  e.preventDefault()
  ui.select(next)
  void nextTick(() => list.value?.querySelector<HTMLElement>(`[data-category="${next}"]`)?.focus())
}
</script>

<template>
  <aside class="flex w-52 shrink-0 flex-col border-r border-border/70 bg-muted/20 lg:w-56" data-testid="settings-rail">
    <div class="shrink-0 p-3 pb-1">
      <SettingsModuleSwitch @jump="emit('jump', $event)" />
    </div>

    <nav class="min-h-0 flex-1 overflow-y-auto px-2 pb-3" aria-label="设置分类">
      <div ref="list" role="tablist" aria-orientation="vertical" data-testid="settings-tabs" @keydown="onKeydown">
        <div v-for="g in groups" :key="g.key" role="presentation" class="mt-3.5">
          <p
            class="px-2.5 pb-1 text-[10px] font-mono font-medium tracking-wider text-muted-foreground/80"
            :data-testid="`settings-group-${g.key}`"
          >
            {{ g.title }}
          </p>
          <button
            v-for="c in g.items"
            :id="`settings-tab-btn-${c.id}`"
            :key="c.id"
            type="button"
            role="tab"
            :aria-selected="ui.category === c.id"
            :aria-controls="'settings-panel'"
            :tabindex="ui.category === c.id ? 0 : -1"
            :title="c.hint"
            :data-testid="`settings-tab-${c.id}`"
            :data-category="c.id"
            class="relative flex w-full cursor-pointer select-none items-center gap-2 rounded-sm px-2.5 py-2 text-left text-xs font-mono transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            :class="ui.category === c.id
              ? 'bg-primary/10 font-bold text-foreground before:absolute before:inset-y-1.5 before:left-0 before:w-0.5 before:rounded-full before:bg-primary'
              : 'text-muted-foreground hover:bg-muted hover:text-foreground'"
            @click="ui.select(c.id)"
          >
            <component
              :is="SETTINGS_ICONS[c.id]"
              class="size-3.5 shrink-0"
              :class="ui.category === c.id ? 'text-primary' : ''"
              aria-hidden="true"
            />
            <span class="min-w-0 flex-1 truncate">{{ c.label }}</span>
          </button>
        </div>
      </div>
    </nav>
  </aside>
</template>
