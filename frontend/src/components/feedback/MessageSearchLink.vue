<script setup lang="ts">
// 「搜索消息」入口（#120 阶段 E-c1）：点一下展开 / 收起会话列表里的搜索面板。
// 桌面放在会话区上方的一行小字（variant=link），手机宽度放进输入区上方那条可横向滑动的芯片条的末尾（variant=chip，#155），
// 省下页头下面那一整行高度。同一时刻只会挂载其中一处，data-testid 因此不会重复。
// （这里曾经还有两个切换视图的按钮，#166 删掉经典视图时一并移除。）
import { computed } from 'vue'
import { usePreviewSearch } from '../../composables/usePreviewSearch'

const props = withDefaults(defineProps<{ variant?: 'link' | 'chip' }>(), { variant: 'link' })
const previewSearch = usePreviewSearch()
const cls = computed(() =>
  props.variant === 'chip'
    ? 'shrink-0 cursor-pointer whitespace-nowrap rounded-sm border border-dashed border-border/80 bg-card/90 px-2 py-1 text-[11px] text-muted-foreground shadow-2xs backdrop-blur-xs'
    : 'cursor-pointer text-[11px] text-muted-foreground/70 underline-offset-2 hover:text-foreground hover:underline',
)
</script>

<template>
  <button
    type="button"
    :class="cls"
    :aria-expanded="previewSearch.open.value"
    data-testid="preview-search-toggle"
    @click="previewSearch.toggle()"
  >
    搜索消息
  </button>
</template>
