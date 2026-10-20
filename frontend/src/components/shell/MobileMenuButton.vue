<script setup lang="ts">
// 手机页头左上角的「展开侧栏」按钮（汉堡）：各个页头用同一个组件（#156 批 1）。
// 别处有会话在等你答复时，右上角带一个数字角标——不用先开抽屉就知道「有事要我处理」。
// 外观（class）与点击事件由使用它的页头通过属性传进来，落在根节点 <button> 上；这里只管图标、角标与统一的名字
// （title / aria-label 都是「打开侧栏」——批 2 前各页叫法不一：展开会话与工单侧边栏 / 展开会话侧边栏 / 打开会话侧栏）。
import { Menu } from 'lucide-vue-next'
import { usePendingSessions } from '../../composables/usePendingSessions'
import { badgeText } from '../../lib/pendingSessions'

const pending = usePendingSessions()
</script>

<template>
  <button type="button" class="relative" title="打开侧栏" aria-label="打开侧栏" data-testid="open-sidebar">
    <Menu class="size-4" />
    <span
      v-if="pending.elsewhere > 0"
      class="absolute -right-1 -top-1 min-w-4 rounded-full bg-amber-500 px-1 text-center text-[10px] font-medium leading-4 text-white ring-2 ring-background"
      :title="`${pending.elsewhere} 个会话在等你答复`"
      :aria-label="`${pending.elsewhere} 个会话在等你答复`"
      data-testid="open-sidebar-badge"
    >{{ badgeText(pending.elsewhere) }}</span>
  </button>
</template>
