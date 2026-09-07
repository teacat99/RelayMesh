<script setup lang="ts">
import { computed, ref } from 'vue'
import { marked } from 'marked'
import { usePreviewStore } from '@/stores/preview'
import { toast } from 'vue-sonner'

const props = defineProps<{
  content: string
}>()

const previewStore = usePreviewStore()
const markdownContainerRef = ref<HTMLElement | null>(null)

// 严格处理代码块中 CJK 与 ASCII 字符：为 CJK 字符包裹严格 2ch 宽度的 span，确保像素级绝对对齐
function formatMonospaceCode(codeText: string): string {
  const lines = codeText.split('\n')
  return lines.map(line => {
    let formattedLine = ''
    for (const char of line) {
      const code = char.codePointAt(0) || 0
      // 判断是否为 CJK 全宽字符范围 (汉字、全角标点、日韩字符等)
      const isCJK = (
        (code >= 0x4E00 && code <= 0x9FFF) ||   // CJK Unified Ideographs
        (code >= 0x3400 && code <= 0x4DBF) ||   // CJK Extension A
        (code >= 0x20000 && code <= 0x2A6DF) || // CJK Extension B
        (code >= 0x3000 && code <= 0x303F) ||   // CJK Symbols and Punctuation (如 【】、。)
        (code >= 0xFF01 && code <= 0xFF60) ||   // Fullwidth Forms (如 ：！等)
        (code >= 0xFE30 && code <= 0xFE4F)      // CJK Compatibility Forms
      )

      if (isCJK) {
        formattedLine += `<span class="cjk-char">${char}</span>`
      } else if (char === '<') {
        formattedLine += '&lt;'
      } else if (char === '>') {
        formattedLine += '&gt;'
      } else if (char === '&') {
        formattedLine += '&amp;'
      } else if (char === '"') {
        formattedLine += '&quot;'
      } else {
        formattedLine += char
      }
    }
    return formattedLine
  }).join('\n')
}

// Custom renderer for marked to process pre / code blocks with character grid
const renderer = new marked.Renderer()
renderer.code = function(token: any, lang?: string) {
  let codeText = ''
  let language = ''
  if (typeof token === 'string') {
    codeText = token
    language = lang || ''
  } else if (token && typeof token === 'object') {
    codeText = token.text || ''
    language = token.lang || lang || ''
  }
  const formatted = formatMonospaceCode(codeText)
  const langClass = language ? ` class="language-${language}"` : ''
  return `<div class="code-block-wrapper my-3 rounded-xs border border-border/80 bg-card overflow-hidden shadow-2xs relative group">
    <button
      type="button"
      class="copy-code-button absolute top-2 right-2 p-1.5 rounded-sm bg-muted/70 hover:bg-muted text-muted-foreground hover:text-foreground cursor-pointer transition-all border border-border/60 shadow-2xs opacity-75 hover:opacity-100 group-hover:opacity-100 z-10"
      title="复制代码"
    >
      <svg class="copy-icon-idle w-3.5 h-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
        <rect width="14" height="14" x="8" y="8" rx="2" ry="2"/>
        <path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2"/>
      </svg>
      <svg class="copy-icon-success w-3.5 h-3.5 hidden text-green-600 dark:text-green-400" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
        <path d="M20 6 9 17l-5-5"/>
      </svg>
    </button>
    <pre class="p-3 m-0 overflow-x-auto"><code${langClass}>${formatted}</code></pre>
  </div>`
}

// 处理 Markdown 中的图片渲染：支持点击大图预览
renderer.image = function(token: any) {
  let href = ''
  let title = ''
  let text = ''
  if (typeof token === 'string') {
    href = token
  } else if (token && typeof token === 'object') {
    href = token.href || ''
    title = token.title || ''
    text = token.text || ''
  }
  return `<img src="${href}" alt="${text}" title="${title}" class="markdown-previewable-image rounded-sm border border-border my-2 max-h-96 object-contain cursor-pointer hover:opacity-95 transition-all shadow-xs" />`
}

// Configure marked renderer for safe and rich HTML output
marked.setOptions({
  renderer: renderer,
  breaks: true,
  gfm: true
})

const renderedHtml = computed(() => {
  if (!props.content) return ''
  return marked.parse(props.content) as string
})

function handleContainerClick(event: MouseEvent) {
  const target = event.target as HTMLElement
  if (!target) return

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
  font-size: 0.85em;
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

.markdown-body table {
  width: 100%;
  border-collapse: collapse;
  margin-top: 0.75rem;
  margin-bottom: 0.75rem;
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
