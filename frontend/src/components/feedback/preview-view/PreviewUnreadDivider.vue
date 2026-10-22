<script setup lang="ts">
// 「以下为新消息」分隔（#120 阶段 E-c2）：画在您上次读到的位置之后的第一条 Agent 消息前面。
// 只是一条分隔，没有交互；不带 data-rail-*，右侧轨不为它画刻度。位置由 lib/previewUnread 的 unreadDividerKey 算出。
</script>

<template>
  <div class="flex select-none items-center gap-3" role="separator" aria-label="以下为新消息" data-testid="preview-unread-divider">
    <div class="h-px flex-1 bg-primary/40"></div>
    <span class="rounded-full bg-primary/10 px-2.5 py-0.5 text-[10px] font-medium text-primary sm:text-[11px]">以下为新消息</span>
    <div class="h-px flex-1 bg-primary/40"></div>
  </div>
</template>
