<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { splitDocDecisions } from '@/lib/docDecisions'
import { matchDecisionViews, type DocDecisionView } from '@/lib/confirms'
import MarkdownRenderer from '../MarkdownRenderer.vue'
import DocDecisionCard from './DocDecisionCard.vue'

const props = defineProps<{
  content: string
  workflowId?: string
  /** 这篇文档的路径：正文里 [[./x]] / [[../x]] 这样的相对链接按它所在的目录解析；编辑预览等没有路径时不传。 */
  docPath?: string
  /** 服务端把围栏与确认项对齐后的决策视图（文档详情里的 decisions）；不传则决策卡只读（如编辑预览）。 */
  decisions?: DocDecisionView[]
  /** 正文是历史修订时为 true：决策卡一律不能作答。 */
  historic?: boolean
}>()

const emit = defineEmits<{ (e: 'comment', anchor: string): void }>()

const segments = computed(() => splitDocDecisions(props.content))
const views = computed(() => matchDecisionViews(segments.value, props.decisions))

// 正文的排版（标题层级、列表符号、表格边框、代码底色）、代码块复制按钮与图片预览，都直接用聊天里的 MarkdownRenderer（#154，Q19 选 a：
// 复用聊天的 Markdown 样式、不加依赖）；这里只多要标题锚点（目录跳转与「评论」按钮要靠它）。
// 容器上的 [overflow-wrap:anywhere]（见模板）：超长链接 / 标识符在窄屏上要折行，不能把整篇正文撑成横向滚动
// （#153 真机档案里量出来的）；代码块（pre）不折行，仍在块内横向滚动；宽表格在表格自己里横滚（见文件末尾的样式）。
const container = ref<HTMLElement | null>(null)

function attachHeadingComments(root: HTMLElement | null) {
  if (!root) return
  root.querySelectorAll('h1[id],h2[id],h3[id],h4[id],h5[id],h6[id]').forEach(h => {
    const el = h as HTMLElement
    if (el.querySelector('[data-testid="doc-heading-comment"]')) return
    // 只加 group（悬停时让按钮出现）；不再把标题改成 flex——标题里夹着行内代码时，flex 会把每一段文字 / 代码拆成独立的项、各隔 8px
    el.classList.add('group')
    const anchor = el.id
    const btn = document.createElement('button')
    btn.type = 'button'
    btn.dataset.docHeadingComment = '1'
    btn.setAttribute('data-testid', 'doc-heading-comment')
    // 有鼠标的设备：平时藏着，悬停到标题上才出现；触屏没有悬停（hover:none），不能让它一直是透明的（#153 真机档案里量出来的）：
    // 触屏上常驻显示（淡一点），点按区也放大到 24px 高、40px 宽，免得手指点不准。
    btn.className =
      'doc-heading-comment ml-2 inline-block align-middle rounded-xs border border-border/70 px-1.5 py-0 text-[10px] font-normal text-muted-foreground opacity-0 group-hover:opacity-100 focus:opacity-100 hover:text-primary hover:border-primary/40 transition-opacity [@media(hover:none)]:min-h-6 [@media(hover:none)]:min-w-10 [@media(hover:none)]:text-[11px] [@media(hover:none)]:opacity-70'
    btn.textContent = '评论'
    btn.title = `评论标题 #${anchor}`
    btn.setAttribute('aria-label', `评论标题 ${anchor}`)
    btn.addEventListener('click', ev => {
      ev.preventDefault()
      ev.stopPropagation()
      emit('comment', anchor)
    })
    el.appendChild(btn)
  })
}

let headingObserver: MutationObserver | null = null

function scheduleAttach() {
  void nextTick(() => attachHeadingComments(container.value))
}

watch(() => props.content, scheduleAttach)

onMounted(() => {
  scheduleAttach()
  if (container.value) {
    headingObserver = new MutationObserver(() => scheduleAttach())
    headingObserver.observe(container.value, { childList: true, subtree: true })
  }
})

onBeforeUnmount(() => headingObserver?.disconnect())
</script>

<template>
  <div ref="container" class="doc-markdown-body prose prose-sm dark:prose-invert max-w-none [overflow-wrap:anywhere]" data-testid="doc-markdown-body">
    <template v-for="(seg, idx) in segments" :key="idx">
      <DocDecisionCard
        v-if="seg.kind === 'decision'"
        :card="seg.card"
        :errors="seg.errors"
        :view="views[idx]"
        :workflow-id="workflowId"
        :historic="historic"
      />
      <div v-else-if="seg.text.trim()" class="relative">
        <MarkdownRenderer :content="seg.text" :workflow-id="workflowId" :doc-path="docPath" heading-anchors />
      </div>
    </template>
  </div>
</template>

<style>
/* 文档里的宽表格：像 GitHub 那样按内容宽度排，超出就在表格自己里横向滚动（聊天里的表格桌面上不动，仍是 100% 宽；手机上同样处理，见 MarkdownRenderer.vue）。
   单元格给一个最小宽度：中文能在任意两个字之间断行、容器上又有 overflow-wrap:anywhere，不设下限时 8 列的表格会被硬挤进
   手机宽度、每列只剩两个字；有了下限，列少的表格照常换行排满，列多的表格就在表格里横向滑。 */
.doc-markdown-body .markdown-body table {
  display: block;
  width: max-content;
  max-width: 100%;
  overflow-x: auto;
}
.doc-markdown-body .markdown-body th,
.doc-markdown-body .markdown-body td {
  min-width: 4.5em;
}
</style>
