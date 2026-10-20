<script setup lang="ts">
// 侧栏导航的第一项「会话」：回到会话页（首页）。与「任务清单 / 群聊 / 项目」四项并列，当前所在的一项高亮（#156 批 1）。
// 原来导航里没有「会话」，从任务清单 / 群聊 / 项目回来只能去点下面列表里的某个会话；会话页自己也没有高亮，不知道「我现在在哪」。
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { MessagesSquare } from 'lucide-vue-next'

const emit = defineEmits<{ (e: 'navigate'): void }>()

const route = useRoute()
const router = useRouter()
const active = computed(() => route.name === 'home')

function open() {
  emit('navigate')
  if (route.name !== 'home') void router.push({ name: 'home' })
}
</script>

<template>
  <div class="px-2.5 pt-2.5" data-testid="sidebar-sessions">
    <button
      type="button"
      class="flex w-full cursor-pointer items-center gap-2 rounded-sm border px-2.5 py-1.5 text-xs transition-colors"
      :class="active ? 'border-primary/30 bg-primary/10 text-foreground' : 'border-border/60 bg-background/60 text-muted-foreground hover:bg-muted/60 hover:text-foreground'"
      :aria-current="active ? 'page' : undefined"
      title="会话：智能体的汇报与你的答复"
      data-testid="sidebar-sessions-entry"
      @click="open"
    >
      <MessagesSquare class="size-3.5 shrink-0" />
      <span class="font-medium">会话</span>
    </button>
  </div>
</template>
