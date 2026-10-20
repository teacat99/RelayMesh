<script setup lang="ts">
// 纯文本里的引用：把「#36」「Q23」「[[路径]]」「sess-xxxxxxxx」渲染成可点击的链接（点开任务弹窗 / 点名确认卡 / 打开文档 / 滚到那一轮），
// 其余文字原样保留（含换行，样式由调用方的 class 决定）。
// Markdown 正文用的是 MarkdownRenderer 里的 DOM 装饰，两者共用 lib/linkRefs.ts 的识别规则与链接外观（globals.css 的 .link-ref）。
import { computed, watch } from 'vue'
import { useLinkRefs } from '../../composables/useLinkRefs'
import { linkRefClass, linkRefHint, linkRefHref, splitLinkRefs, type LinkRefSegment } from '../../lib/linkRefs'

const props = defineProps<{
  text: string
  /** 引用所属的工作流；缺省时不做链接，各类引用只在它们的数据就绪时才解析（见 composables/useLinkRefs.ts）。 */
  workflowId?: string
}>()

const refs = useLinkRefs(() => props.workflowId)

// 文字里出现 [[ 时保证文档索引已加载（装饰与渲染都不发请求，这里是唯一的触发点）
watch(
  () => [props.text, props.workflowId] as const,
  ([text]) => {
    if (text.includes('[[')) refs.ensureDocs()
    if (/sess-[0-9a-f]{6}/i.test(text)) refs.ensureSessions() // 会话号靠选中工作流的整表解析，非会话页不再顺带加载它（#169）
  },
  { immediate: true }
)

const segments = computed<LinkRefSegment[]>(() => {
  void refs.signature.value // 引用目标集合变化时重新切分
  if (!refs.active.value) return [{ text: props.text }]
  return splitLinkRefs(props.text, refs.index)
})
</script>

<template>
  <p>
    <template v-for="(s, i) in segments" :key="i"
      ><a
        v-if="s.ref"
        :href="linkRefHref(s.ref, workflowId ?? '')"
        :class="linkRefClass(s.ref.kind)"
        :data-ref-kind="s.ref.kind"
        :data-ref-key="s.ref.key"
        :data-ref-anchor="s.ref.anchor"
        :data-task-seq="s.ref.kind === 'task' ? s.ref.key : undefined"
        :title="linkRefHint(s.ref, s.shown ?? s.text)"
        @click="refs.activate($event, s.ref)"
        >{{ s.shown ?? s.text }}</a
      ><template v-else>{{ s.text }}</template></template
    >
  </p>
</template>
