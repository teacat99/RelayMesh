<script setup lang="ts">
// 「回到经典视图 / 试用预览视图」入口（#120）；预览视图开着时再多一个「搜索消息」（阶段 E-c1，只在预览视图出现，经典视图没有）。
// #165 起预览视图是默认视图，「回到经典视图」写下这台设备的选择；选了经典的设备在这里还能「试用预览视图」回来。
// 桌面放在会话区上方的一行小字（variant=link）；手机宽度放进输入区上方那条可横向滑动的芯片条的末尾（variant=chip，#155），
// 省下页头下面那一整行高度。同一时刻只会挂载其中一处，data-testid 因此不会重复。
import { computed } from 'vue'
import { usePreviewSearch } from '../../composables/usePreviewSearch'
import { usePreviewView } from '../../composables/usePreviewView'

const props = withDefaults(defineProps<{ variant?: 'link' | 'chip' }>(), { variant: 'link' })
const previewView = usePreviewView()
const previewSearch = usePreviewSearch()
const cls = computed(() =>
  props.variant === 'chip'
    ? 'shrink-0 cursor-pointer whitespace-nowrap rounded-sm border border-dashed border-border/80 bg-card/90 px-2 py-1 text-[11px] text-muted-foreground shadow-2xs backdrop-blur-xs'
    : 'cursor-pointer text-[11px] text-muted-foreground/70 underline-offset-2 hover:text-foreground hover:underline',
)
</script>

<template>
  <button
    v-if="previewView.enabled.value"
    type="button"
    :class="cls"
    :aria-expanded="previewSearch.open.value"
    data-testid="preview-search-toggle"
    @click="previewSearch.toggle()"
  >
    搜索消息
  </button>
  <button
    v-if="!previewView.enabled.value"
    type="button"
    :class="cls"
    data-testid="preview-view-on"
    @click="previewView.set(true)"
  >
    试用预览视图
  </button>
  <button
    v-else
    type="button"
    :class="cls"
    data-testid="preview-view-off"
    @click="previewView.set(false)"
  >
    回到经典视图
  </button>
</template>
