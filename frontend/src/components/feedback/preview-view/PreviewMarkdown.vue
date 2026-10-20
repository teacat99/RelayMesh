<script setup lang="ts">
// 预览卡片里的智能体正文（#120 阶段 E-a）：Markdown 全文 + 从正文里抽出来的 Artifact 引用卡。
// 正文里的 [[artifact:N]] 记号先去掉（否则会当成普通文字露出来），每个文档编号一张引用卡，点开在右侧预览栏
// （ArtifactPreviewPane，已经挂在 FeedbackView 上，经典与预览共用）。
// 只给智能体写的正文用（汇报 / 进度通知 / 提问与计划）；您自己写的话不抽取。正文里没有记号时，和直接用 MarkdownRenderer 完全一样。
import { computed } from 'vue'
import { extractArtifactRefs } from '../../../lib/artifactRef'
import ArtifactRefCard from '../../artifact/ArtifactRefCard.vue'
import MarkdownRenderer from '../../MarkdownRenderer.vue'

const props = defineProps<{
  content: string | undefined | null
  /** 正文所属的工作流：引用链接（#N / Q<N> / [[路径]] / 会话号）据此装饰，原样转给 MarkdownRenderer。 */
  workflowId?: string
}>()

const extracted = computed(() => extractArtifactRefs(props.content))
</script>

<template>
  <MarkdownRenderer :content="extracted.text" :workflow-id="workflowId" />
  <div v-if="extracted.ids.length" data-testid="preview-artifact-refs">
    <ArtifactRefCard v-for="id in extracted.ids" :key="id" :doc-id="id" />
  </div>
</template>
