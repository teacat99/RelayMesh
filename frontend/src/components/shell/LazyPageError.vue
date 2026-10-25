<script setup lang="ts">
// 整页懒加载块没能取回时的出路（#186）：页头照常、主体给一句人话和「刷新页面」。
// 多半是页面开着期间发了新版本（带哈希的旧文件名没了），或网络抖了一下；浏览器会记住这次失败，同一页面里重试没用，所以只给刷新。
// 它被当作 defineAsyncComponent 的 errorComponent 使用；同时全局会弹一条同样的「刷新」提示（utils/chunkFailure）。
import PageHeader from './PageHeader.vue'
import PageState from './PageState.vue'
import Button from '../ui/button/Button.vue'
import { RefreshCw } from 'lucide-vue-next'

defineProps<{ sidebarCollapsed?: boolean }>()
const emit = defineEmits<{
  (e: 'open-mobile-sidebar'): void
  (e: 'expand-sidebar'): void
}>()

function reload() {
  window.location.reload()
}
</script>

<template>
  <div class="flex min-h-0 flex-1 flex-col bg-background" data-testid="lazy-page-error">
    <PageHeader :sidebar-collapsed="sidebarCollapsed" @open-mobile-sidebar="emit('open-mobile-sidebar')" @expand-sidebar="emit('expand-sidebar')" />
    <PageState kind="error" title="这一页没能载入" hint="多半是刚更新了版本，当前页面还是旧的；也可能是网络抖了一下。刷新后再试。" class="flex-1">
      <template #actions>
        <Button size="sm" variant="outline" data-testid="lazy-reload" @click="reload">
          <RefreshCw />
          刷新页面
        </Button>
      </template>
    </PageState>
  </div>
</template>
