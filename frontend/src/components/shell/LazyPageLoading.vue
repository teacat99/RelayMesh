<script setup lang="ts">
// 整页懒加载块（任务清单 / 项目 / 文档 / 群聊）取回之前的占位（#186）：
// 页头骨架照常画出来（同高同底线，汉堡与「展开侧栏」都能点），主体是统一的「加载中」，不留白屏、版面也不跳。
// 它被当作 defineAsyncComponent 的 loadingComponent 使用：真页面收到的 sidebar-collapsed 与两个事件会原样落到这里。
// 块通常在首屏安静后已被预取好，真正看到这个占位的多是慢网络上很快就点进来的人。
import PageHeader from './PageHeader.vue'
import PageState from './PageState.vue'

defineProps<{ sidebarCollapsed?: boolean }>()
const emit = defineEmits<{
  (e: 'open-mobile-sidebar'): void
  (e: 'expand-sidebar'): void
}>()
</script>

<template>
  <div class="flex min-h-0 flex-1 flex-col bg-background" data-testid="lazy-page-loading">
    <PageHeader :sidebar-collapsed="sidebarCollapsed" @open-mobile-sidebar="emit('open-mobile-sidebar')" @expand-sidebar="emit('expand-sidebar')" />
    <PageState kind="loading" title="正在载入…" class="flex-1" />
  </div>
</template>
