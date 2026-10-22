<script setup lang="ts">
import { computed } from 'vue'
import { GitCommitHorizontal, Ellipsis } from 'lucide-vue-next'
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem
} from '../ui/dropdown-menu'
import { gitLogSinceCommand, gitShowCommand, shortHash } from '../../lib/anchor'

// 交付锚点芯片：模型随本次交付申报的仓库最新提交。
// 只做展示与复制——点哈希复制哈希；菜单里可复制 `git log <哈希>..HEAD` 与 `git show <哈希>`。
const props = defineProps<{
  commit: string
  subject?: string
}>()

const emit = defineEmits<{
  (e: 'copy', text: string, label: string): void
}>()

const short = computed(() => shortHash(props.commit))
const logCommand = computed(() => gitLogSinceCommand(props.commit))
const showCommand = computed(() => gitShowCommand(props.commit))
</script>

<template>
  <div
    class="flex items-center gap-1.5 min-w-0 text-[10px] sm:text-[11px] font-mono text-muted-foreground"
    data-testid="anchor-chip"
  >
    <GitCommitHorizontal class="w-3.5 h-3.5 shrink-0" />
    <button
      type="button"
      class="shrink-0 font-medium text-foreground hover:text-primary cursor-pointer transition-colors"
      :title="`点击复制提交哈希 ${commit}`"
      data-testid="anchor-hash"
      @click="emit('copy', commit, '提交哈希')"
    >{{ short }}</button>
    <span v-if="subject" class="truncate min-w-0 flex-1" :title="subject" data-testid="anchor-subject">{{ subject }}</span>
    <span v-else class="flex-1"></span>

    <DropdownMenu>
      <DropdownMenuTrigger as-child>
        <button
          type="button"
          class="shrink-0 p-0.5 rounded-sm text-muted-foreground hover:text-foreground hover:bg-muted cursor-pointer transition-colors"
          aria-label="提交操作"
          data-testid="anchor-menu"
        >
          <Ellipsis class="w-3.5 h-3.5" />
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" class="p-1 font-mono text-xs shadow-modal border border-border bg-popover">
        <DropdownMenuItem
          class="cursor-pointer rounded-xs px-2 py-1.5 text-xs font-mono"
          @click="emit('copy', commit, '提交哈希')"
        >复制哈希</DropdownMenuItem>
        <DropdownMenuItem
          v-if="logCommand"
          class="cursor-pointer rounded-xs px-2 py-1.5 text-xs font-mono"
          @click="emit('copy', logCommand, 'git log 命令')"
        >复制 git log（至今）</DropdownMenuItem>
        <DropdownMenuItem
          v-if="showCommand"
          class="cursor-pointer rounded-xs px-2 py-1.5 text-xs font-mono"
          @click="emit('copy', showCommand, 'git show 命令')"
        >复制 git show</DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  </div>
</template>
