<script setup lang="ts">
// 页头上的分支标记（#236）：分支页上写「分支 · 主会话「X」」，点一下回到主会话；主会话上写「N 个分支」，点一下打开分支面板。
// 只是一个按钮外观，逻辑在 lib/branch.ts 的 branchChipView 与调用方。
import { GitBranch } from 'lucide-vue-next'
import type { BranchChipView } from '../../lib/branch'

defineProps<{ chip: BranchChipView }>()
defineEmits<{ (e: 'click'): void }>()
</script>

<template>
  <button
    type="button"
    class="inline-flex min-w-0 max-w-[14rem] shrink items-center gap-1 rounded-sm border border-primary/40 bg-primary/5 px-1.5 py-0.5 text-[10px] sm:text-[11px] leading-4 text-primary cursor-pointer transition-colors outline-none hover:bg-primary/10 focus-visible:ring-1 focus-visible:ring-ring"
    :title="chip.title"
    :data-kind="chip.kind"
    data-testid="session-branch-chip"
    @click="$emit('click')"
  >
    <GitBranch class="size-3 shrink-0" />
    <span class="truncate">{{ chip.text }}</span>
  </button>
</template>
