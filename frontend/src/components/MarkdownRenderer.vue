<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import DOMPurify from 'dompurify'
import { usePreviewStore } from '@/stores/preview'
import { toast } from 'vue-sonner'
import { renderMarkdown } from '@/lib/markdownRender'
import type { PurifyLike } from '@/lib/markdownSafe'
import { decorateLinkRefs } from '@/lib/linkRefsDom'
import { useLinkRefs } from '@/composables/useLinkRefs'

const props = defineProps<{
  content: string
  /** 正文所属的工作流：给了就把正文里存在的引用（任务 #36、确认项 Q23、文档 [[路径]]、会话号 sess-xxxxxxxx）装饰成可点击的链接；缺省不做任何链接。 */
  workflowId?: string
  /** 正文所在文档的路径（文档页才传）：[[./x]] / [[../x]] 这样的相对写法按它所在的目录解析。 */
  docPath?: string
  /** 给标题加锚点 id（文档页用：目录跳转、「评论」按钮要靠它）；聊天里不需要。 */
  headingAnchors?: boolean
}>()

const previewStore = usePreviewStore()
const markdownContainerRef = ref<HTMLElement | null>(null)
const linkRefs = useLinkRefs(() => props.workflowId)
// 先粗筛：不是每段正文都有会话号，装饰时才不必每次都去问 store（识别规则本身在 lib/linkRefs.ts 的 SESSION_RE）
const MAYBE_SESSION_REF_RE = /sess-[0-9a-f]{6}/i

// 内容来自 AI 客户端，属于不可信输入：渲染管线（marked → 自定义渲染器 → DOMPurify 白名单 → LRU 缓存）见 lib/markdownRender.ts
const renderedHtml = computed(() => {
  if (!props.content) return ''
  return renderMarkdown(DOMPurify as unknown as PurifyLike, props.content, props.headingAnchors ? { headingAnchors: true } : undefined)
})

// 引用（任务 / 确认项 / 文档 / 会话号）在净化之后、对着已经渲染出来的 DOM 装饰（只替换文本节点，见 lib/linkRefsDom.ts），
// 所以渲染缓存里始终是不带上下文的纯净结果。v-html 每次换内容都会把装饰冲掉，因此内容变了、
// 或引用目标集合变了（任务 / 确认项 / 文档晚于正文加载出来）都要重新装饰；装饰是幂等的。
function decorateRefs() {
  const el = markdownContainerRef.value
  const id = props.workflowId
  if (!el || !id || !linkRefs.active.value) return
  if (renderedHtml.value.includes('[[')) linkRefs.ensureDocs() // 文档索引只在正文里真的写了 [[ 才去取
  if (MAYBE_SESSION_REF_RE.test(renderedHtml.value)) linkRefs.ensureSessions() // 会话整表同理：正文里真有 sess- 才保证它在本地（#169）
  decorateLinkRefs(el, linkRefs.index, { workflowId: id, ...(props.docPath ? { fromPath: props.docPath } : {}) })
}
onMounted(decorateRefs)
watch([renderedHtml, linkRefs.signature], () => void nextTick(decorateRefs), { flush: 'post' })

function handleContainerClick(event: MouseEvent) {
  const target = event.target as HTMLElement
  if (!target) return

  // 0. 点击正文里的引用链接：任务弹窗 / 点名确认卡 / 站内打开文档 / 滚到那一轮；带修饰键的点击放行给浏览器（新标签页打开深链）
  if (linkRefs.handleClick(event)) return

  // 1. 点击代码块复制按钮
  const copyBtn = target.closest('.copy-code-button') as HTMLElement | null
  if (copyBtn) {
    const wrapper = copyBtn.closest('.code-block-wrapper')
    const codeEl = wrapper?.querySelector('code')
    if (codeEl) {
      const rawText = codeEl.innerText || codeEl.textContent || ''
      const idleIcon = copyBtn.querySelector('.copy-icon-idle')
      const successIcon = copyBtn.querySelector('.copy-icon-success')

      const showSuccess = () => {
        if (idleIcon) idleIcon.classList.add('hidden')
        if (successIcon) successIcon.classList.remove('hidden')
        copyBtn.classList.add('border-green-500/50', 'bg-green-500/10')
        toast.success('代码已复制到剪贴板')
        setTimeout(() => {
          if (idleIcon) idleIcon.classList.remove('hidden')
          if (successIcon) successIcon.classList.add('hidden')
          copyBtn.classList.remove('border-green-500/50', 'bg-green-500/10')
        }, 1800)
      }

      navigator.clipboard.writeText(rawText).then(() => {
        showSuccess()
      }).catch(() => {
        const ta = document.createElement('textarea')
        ta.value = rawText
        document.body.appendChild(ta)
        ta.select()
        document.execCommand('copy')
        document.body.removeChild(ta)
        showSuccess()
      })
    }
    return
  }

  // 2. 点击正文中的图片触发全屏纯手势灯箱预览
  if (target.tagName === 'IMG' && target.classList.contains('markdown-previewable-image')) {
    const img = target as HTMLImageElement
    previewStore.openImagePreview({
      src: img.src,
      alt: img.alt,
      title: img.title
    })
  }
}
</script>

<template>
  <div class="prose dark:prose-invert max-w-none prose-pre:p-0 prose-pre:bg-transparent prose-pre:border-0">
    <div
      ref="markdownContainerRef"
      class="markdown-body text-sm leading-relaxed space-y-3"
      v-html="renderedHtml"
      @click="handleContainerClick"
    ></div>
  </div>
</template>

<style>
.markdown-body h1 {
  font-size: 1.25rem;
  font-weight: 600;
  margin-top: 1rem;
  margin-bottom: 0.5rem;
  padding-bottom: 0.3rem;
  border-bottom: 1px solid var(--border);
  color: var(--foreground);
}
.markdown-body h2 {
  font-size: 1.1rem;
  font-weight: 600;
  margin-top: 0.85rem;
  margin-bottom: 0.4rem;
  color: var(--foreground);
}
.markdown-body h3 {
  font-size: 0.95rem;
  font-weight: 600;
  margin-top: 0.75rem;
  margin-bottom: 0.3rem;
  color: var(--foreground);
}
/* h4 ~ h6：设计文档里常有「#### 子标题」，没有这几条时它们和正文一样大、一样粗（#154 补） */
.markdown-body h4 {
  font-size: 0.9rem;
  font-weight: 600;
  margin-top: 0.65rem;
  margin-bottom: 0.25rem;
  color: var(--foreground);
}
.markdown-body h5, .markdown-body h6 {
  font-size: 0.85rem;
  font-weight: 600;
  margin-top: 0.6rem;
  margin-bottom: 0.2rem;
  color: var(--muted-foreground);
}
.markdown-body p {
  margin-bottom: 0.6rem;
  line-height: 1.65;
  color: var(--foreground);
}
.markdown-body ul, .markdown-body ol {
  padding-left: 1.5rem;
  margin-bottom: 0.6rem;
}
.markdown-body ul {
  list-style-type: disc;
}
.markdown-body ol {
  list-style-type: decimal;
}
.markdown-body li {
  margin-bottom: 0.25rem;
}
.markdown-body blockquote {
  border-left: 3px solid var(--primary);
  padding-left: 1rem;
  margin-left: 0;
  margin-right: 0;
  margin-top: 0.75rem;
  margin-bottom: 0.75rem;
  color: var(--muted-foreground);
  font-style: italic;
  background-color: var(--muted);
  padding-top: 0.5rem;
  padding-bottom: 0.5rem;
  border-radius: var(--radius-xs);
}
.markdown-body pre {
  margin: 0;
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", "Courier New", monospace;
  font-size: 0.8125rem;
  line-height: 1.5;
  overflow-x: auto;
  letter-spacing: 0;
  white-space: pre;
  tab-size: 2;
  font-feature-settings: "tnum" 1, "zero" 1;
  text-autospace: no-autospace;
  -webkit-text-autospace: no-autospace;
}
.markdown-body code:not(pre code) {
  background-color: var(--muted);
  color: var(--foreground);
  padding: 0.15rem 0.35rem;
  border-radius: var(--radius-xs);
  /* 0.875em：正文 14px 时是 12.25px；原来的 0.85em 只有 11.9px，手机上被算进「小于 12px」（#155） */
  font-size: 0.875em;
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", "Courier New", monospace;
  border: 1px solid var(--border);
}

.markdown-body .cjk-char {
  display: inline-block;
  width: 2ch;
  text-align: center;
  box-sizing: content-box;
  letter-spacing: 0;
  text-autospace: no-autospace;
  -webkit-text-autospace: no-autospace;
}

/* 长到一行放不下、又没有空格可断的字符串（路径、标识符、裸链接、加粗长串）允许在任意位置折行。
   不加的话它们会把卡片撑宽，进而把整个消息列表的横向滚动范围撑大：手机上右侧多出一片能滑动的空白（#167 / #283）。
   overflow-wrap:anywhere 只在「这一行实在装不下」时才折，平时的断行位置不变。
   表格单元格另算（它会改变整张表的列宽分配）：先恢复成默认，手机上再按下面的规则单独处理。 */
.markdown-body :where(p, li, blockquote, h1, h2, h3, h4, h5, h6) {
  overflow-wrap: anywhere;
}
.markdown-body :where(th, td) {
  overflow-wrap: normal;
}

.markdown-body table {
  width: 100%;
  border-collapse: collapse;
  margin-top: 0.75rem;
  margin-bottom: 0.75rem;
}
/* 手机（窄屏）上的表格：和设计文档页一样按内容宽度排，放不下就在表格自己里横向滚动，不去撑宽外面的消息列表（#167 / #283）。
   单元格里的长串允许折行（折得下就不必滚），每格至少 4.5em 免得被挤成一个字宽；桌面上的表格不动，仍是 100% 宽的整张表。
   设计文档页（DocMarkdownBody）自己有同样的规则，选择器更具体，不受这里影响。 */
@media (max-width: 767.98px) {
  .markdown-body table {
    display: block;
    width: max-content;
    max-width: 100%;
    overflow-x: auto;
  }
  .markdown-body th, .markdown-body td {
    min-width: 4.5em;
    overflow-wrap: anywhere;
  }
}
.markdown-body th, .markdown-body td {
  border: 1px solid var(--border);
  padding: 0.4rem 0.6rem;
  font-size: 0.8125rem;
}
.markdown-body th {
  background-color: var(--muted);
  font-weight: 600;
  text-align: left;
}
.markdown-body tr:nth-child(even) {
  background-color: var(--card);
}
.markdown-body a {
  color: var(--primary);
  text-decoration: underline;
  text-underline-offset: 2px;
}

.markdown-body img {
  cursor: pointer;
  border-radius: 4px;
  border: 1px solid var(--border);
  transition: opacity 0.15s ease;
  max-width: 100%;
}
.markdown-body img:hover {
  opacity: 0.9;
}
</style>
