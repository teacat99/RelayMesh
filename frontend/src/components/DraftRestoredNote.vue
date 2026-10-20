<script setup lang="ts">
// 「已恢复你上次没提交的 xx」小提示（#144）：告诉用户这是从本机浏览器草稿里找回来的，并给一个「丢弃」出口。
defineProps<{
  /** 找回的是什么，如「选择与备注」「评论」。 */
  what?: string
  testid: string
}>()
defineEmits<{ (e: 'discard'): void }>()
</script>

<template>
  <p class="flex flex-wrap items-center gap-x-1.5 text-[11px] text-muted-foreground" role="status" :data-testid="testid">
    <span>已恢复你上次没提交的{{ what || '内容' }}（只保存在这个浏览器里）</span>
    <button
      type="button"
      class="cursor-pointer underline underline-offset-2 hover:text-foreground"
      :data-testid="`${testid}-discard`"
      @click="$emit('discard')"
    >
      丢弃
    </button>
  </p>
</template>
