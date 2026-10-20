<script setup lang="ts">
// 页头里的「返回」按钮（#156 批 2）：各整页同一个外观——边框小按钮 + 箭头 + 去处。
// 文字在手机上默认收起、只留箭头（给标题让出位置）；去处名字本身就是信息的页面（例如「全部群聊」）用 always-label 保留。
// data-testid、点击事件等通过属性落在根节点 <button> 上。
import { ArrowLeft } from 'lucide-vue-next'

withDefaults(defineProps<{ label: string; title?: string; alwaysLabel?: boolean }>(), { title: '', alwaysLabel: false })
</script>

<template>
  <button
    type="button"
    class="inline-flex h-8 shrink-0 cursor-pointer items-center gap-1 rounded-xs border border-border/80 px-2 text-xs text-muted-foreground transition-colors hover:bg-muted/70 hover:text-foreground"
    :title="title || label"
    :aria-label="title || label"
  >
    <ArrowLeft class="size-3.5" aria-hidden="true" /><span :class="alwaysLabel ? '' : 'hidden sm:inline'">{{ label }}</span>
  </button>
</template>
