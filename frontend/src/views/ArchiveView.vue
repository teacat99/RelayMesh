<script setup lang="ts">
// 归档库整页（手机上从侧栏底部的归档入口进来；PC 上是弹窗，直接打开 /archive 也是这一页）。
// #156 批 4：页头换成各整页共用的 PageHeader 骨架（h-14、底线、留白与其他页一致）；这一页没有侧栏，所以 standalone（不画汉堡 / 展开侧栏）。
// 「返回」：上一步在本应用里就是浏览器的返回，直接打开的链接则前往会话页（不会把人退到应用外面去）；「工作区主页」留着，手机上只留图标。
import { useRouter } from 'vue-router'
import { useSessionStore } from '../stores/session'
import { backOrSession } from '../lib/sessionNav'
import ArchiveList from '../components/ArchiveList.vue'
import Button from '../components/ui/button/Button.vue'
import PageHeader from '../components/shell/PageHeader.vue'
import PageBackButton from '../components/shell/PageBackButton.vue'
import { Archive, Home } from 'lucide-vue-next'
import type { FeedbackSession } from '../api/types'

const router = useRouter()
const sessionStore = useSessionStore()

function handleBack() {
  backOrSession(router)
}

function handleSelectSession(s: FeedbackSession) {
  sessionStore.selectSession(s)
  router.push('/')
}
</script>

<template>
  <div class="min-h-full min-h-[100dvh] w-full bg-background text-foreground flex flex-col" data-testid="archive-page">
    <PageHeader standalone class="sticky top-0">
      <template #leading>
        <PageBackButton label="返回" title="返回上一页" always-label data-testid="archive-back" @click="handleBack" />
      </template>

      <Archive class="size-4 shrink-0 text-primary" aria-hidden="true" />
      <h1 class="min-w-0 truncate text-sm font-semibold">历史会话归档库</h1>

      <template #actions>
        <Button variant="outline" size="sm" class="h-8 shrink-0 gap-1 text-xs" title="回到工作区主页" data-testid="archive-home" @click="router.push('/')">
          <Home class="size-3.5" aria-hidden="true" /><span class="hidden sm:inline">工作区主页</span>
        </Button>
      </template>
    </PageHeader>

    <!-- Main Content Container (移动端去除内部容器边框，自适应全屏流式空间) -->
    <main class="flex-1 max-w-4xl w-full mx-auto p-3 sm:p-5 flex flex-col overflow-y-auto">
      <div class="flex-1 flex flex-col border-0 sm:border border-border/80 rounded-none sm:rounded-sm p-0 sm:p-5 bg-transparent sm:bg-card shadow-none sm:shadow-xs">
        <ArchiveList @select-session="handleSelectSession" />
      </div>
    </main>
  </div>
</template>
