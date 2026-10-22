<script setup lang="ts">
// 侧栏的「项目」入口：一行，点进去是项目页（知识库、模块、授权、审计）。
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { FolderGit2 } from 'lucide-vue-next'

// 手机上侧栏是抽屉：进了页面就该把抽屉收起来（navigate 通知外层关抽屉）
const emit = defineEmits<{ (e: 'navigate'): void }>()
const route = useRoute()
const router = useRouter()
const active = computed(() => route.name === 'projects' || route.name === 'project-doc')

function open() {
  emit('navigate')
  void router.push({ name: 'projects' })
}
</script>

<template>
  <div class="px-2.5 pt-1.5" data-testid="sidebar-projects">
    <button
      type="button"
      class="flex w-full cursor-pointer items-center gap-2 rounded-sm border px-2.5 py-1.5 text-xs transition-colors"
      :class="active ? 'border-primary/30 bg-primary/10 text-foreground' : 'border-border/60 bg-background/60 text-muted-foreground hover:bg-muted/60 hover:text-foreground'"
      :aria-current="active ? 'page' : undefined"
      title="项目：把同一产品的多个目录和文档合成一个知识库"
      data-testid="sidebar-project-entry"
      @click="open"
    >
      <FolderGit2 class="size-3.5 shrink-0" />
      <span class="font-medium">项目</span>
    </button>
  </div>
</template>
