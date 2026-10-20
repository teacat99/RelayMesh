<script setup lang="ts">
// 各整页（任务清单 / 群聊 / 项目 / 项目文档）共用的页头骨架（#156 批 2）。从左到右固定是：
//   展开侧栏（PC，侧栏折叠时才有）/ 汉堡（手机） ·  leading（返回等） ·  默认插槽（图标 + 标题 + 副标题，空间不够时它先让位截断） ·  actions（主操作，靠右）
// 高度、底线与左右留白与会话页头（SessionHeader）完全一致（h-14、border-b、px-3 / sm:px-6），在页面之间切换时页头那条线不跳，
// 也与左侧栏品牌区的底线对齐。会话页头与托管自驾页头有自己的两行结构，不用这个骨架，但沿用同一套尺寸。
// standalone：独立整页（归档库）没有侧栏，不画「展开侧栏」与汉堡，其余骨架（高度、留白、leading / 标题 / actions）不变（#156 批 4）。
import { PanelLeftOpen } from 'lucide-vue-next'
import MobileMenuButton from './MobileMenuButton.vue'

defineProps<{ sidebarCollapsed?: boolean; standalone?: boolean }>()

const emit = defineEmits<{
  (e: 'open-mobile-sidebar'): void
  (e: 'expand-sidebar'): void
}>()
</script>

<template>
  <header
    class="z-20 flex h-14 shrink-0 items-center gap-2 border-b border-border/60 bg-background/80 px-3 backdrop-blur-xs sm:gap-3 sm:px-6"
    data-testid="page-header"
  >
    <button
      v-if="sidebarCollapsed && !standalone"
      type="button"
      class="-ml-1.5 mr-1 hidden shrink-0 cursor-pointer items-center rounded-sm border border-border/80 p-1.5 text-muted-foreground hover:bg-muted/70 hover:text-foreground md:inline-flex"
      title="展开侧边栏"
      aria-label="展开侧边栏"
      data-testid="page-header-expand"
      @click="emit('expand-sidebar')"
    >
      <PanelLeftOpen class="size-4" />
    </button>
    <MobileMenuButton
      v-if="!standalone"
      class="-ml-1.5 shrink-0 rounded-sm p-1.5 text-muted-foreground hover:text-foreground md:hidden max-md:p-2"
      @click="emit('open-mobile-sidebar')"
    />
    <slot name="leading" />
    <div class="flex min-w-0 flex-1 items-center gap-2">
      <slot />
    </div>
    <div v-if="$slots.actions" class="flex shrink-0 items-center gap-2">
      <slot name="actions" />
    </div>
  </header>
</template>
