<script setup lang="ts">
import Dialog from './ui/dialog/Dialog.vue'
import DialogContent from './ui/dialog/DialogContent.vue'
import DialogHeader from './ui/dialog/DialogHeader.vue'
import DialogTitle from './ui/dialog/DialogTitle.vue'
import ArchiveList from './ArchiveList.vue'
import { Archive, Layers } from 'lucide-vue-next'
import type { FeedbackSession } from '../api/types'

const props = defineProps<{
  open: boolean
}>()

const emit = defineEmits<{
  (e: 'update:open', val: boolean): void
  (e: 'selectSession', session: FeedbackSession): void
}>()

function handleSelectSession(s: FeedbackSession) {
  emit('selectSession', s)
  emit('update:open', false)
}
</script>

<template>
  <Dialog :open="props.open" @update:open="(v) => emit('update:open', v)">
    <!-- 这个弹窗没有底栏按钮，标题栏右侧已经给关闭按钮留了位置（pr-12）；showClose 是布尔属性，不写就是 false，必须显式打开，
         否则除了 Esc 和点遮罩之外没有任何看得见的关闭方式。（窄屏不用这个弹窗，直接进 /archive 页面。） -->
    <DialogContent :show-close="true" class="sm:max-w-4xl lg:max-w-5xl w-[94vw] h-[86vh] max-h-[88vh] flex flex-col p-0 gap-0 overflow-hidden shadow-2xl rounded-md border-border/80 bg-card" data-testid="archive-dialog">
      <!-- Header (文字靠左，适宜紧凑高度 h-11) -->
      <div class="px-5 h-11 border-b border-border/70 flex flex-row items-center justify-between shrink-0 bg-card/90 backdrop-blur-xs pr-12">
        <DialogTitle class="flex items-center gap-2 text-xs sm:text-sm font-bold font-mono tracking-tight text-foreground text-left">
          <div class="p-1 rounded-xs bg-muted text-foreground border border-border shrink-0">
            <Archive class="w-3.5 h-3.5" />
          </div>
          <span>历史 Workflow 归档库</span>
          <span class="text-xs text-muted-foreground font-normal font-sans ml-1 hidden sm:inline">Archived Workflows Library</span>
        </DialogTitle>
      </div>

      <!-- Archive Content List (无全局底栏按钮) -->
      <div class="flex-1 overflow-y-auto px-5 py-4">
        <ArchiveList @select-session="handleSelectSession" />
      </div>
    </DialogContent>
  </Dialog>
</template>
