<script setup lang="ts">
// 单篇文档的阅读 / 编辑（#131）：正文、决策卡、出链 / 反链、评论、大纲，以及编辑、版本冲突、修订历史。
// 两个外框共用它：项目里的文档页（ProjectDocPage）与还没归入项目的工作流用的老文档页（DocsPage）。
// 外框只管目录 / 页头 / 跳转，文档本身的一切在这里；要跳到别的文档时只发 open-doc，不自己碰路由。
import { computed, nextTick, onBeforeUnmount, reactive, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useMediaQuery } from '@vueuse/core'
import { ClipboardCopy, FileDown, FileX, History, Pencil } from 'lucide-vue-next'
import Button from '../ui/button/Button.vue'
import PageState from '../shell/PageState.vue'
import { Sheet, SheetContent, SheetHeader, SheetTitle } from '../ui/sheet'
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription } from '../ui/dialog'
import DocMarkdownBody from './DocMarkdownBody.vue'
import ArtifactFrame from '../artifact/ArtifactFrame.vue'
import ArtifactComments from '../artifact/ArtifactComments.vue'
import DocLinksPanel from './DocLinksPanel.vue'
import DocDecisionBar from './DocDecisionBar.vue'
import DraftRestoredNote from '../DraftRestoredNote.vue'
import { artifactsApi } from '@/api/artifacts'
import { useProjectDocStore } from '@/stores/projectDocs'
import { useLocalDraft } from '@/composables/useLocalDraft'
import { projectDocsApi } from '@/api/projectDocs'
import { parseDocHeadings } from '@/lib/docHeadings'
import { lineDiffLcs } from '@/lib/lineDiff'
import { clearLocalDraft, draftScopes } from '@/lib/localDraft'
import { toast } from 'vue-sonner'

const props = defineProps<{ workflowId: string; docRef: string }>()
const emit = defineEmits<{
  /** 点了文档里的链接 / 反链：外框决定怎么跳（同一个工作流内、别的工作流、项目内）。 */
  (e: 'open-doc', workflowId: string, path: string, anchor?: string): void
  /** 保存 / 还原之后：外框要刷新自己的目录（标题、更新时间可能变了）。 */
  (e: 'changed'): void
}>()

const store = useProjectDocStore()
const route = useRoute()
const isNarrow = useMediaQuery('(max-width: 1023px)')
const tocDrawerOpen = ref(false)
// HTML Artifact：默认在沙箱里渲染，可切到源码；评论列表由子组件维护
const showArtifactSource = ref(false)
const commentsRef = ref<InstanceType<typeof ArtifactComments> | null>(null)

// ---- 加载 ----
// store.detail 是全站共用的一份（决策卡作答、实时事件都在刷新它），所以这里记下「这份是为哪一篇加载的」：
// 切文档的瞬间不把上一篇的正文挂在新地址下面；加载失败（文档被删 / 改了路径）明说，而不是留着旧内容。
const keyOf = (wf: string, ref: string) => `${wf}\u0000${ref}`
const currentKey = computed(() => keyOf(props.workflowId, props.docRef))
const loadedKey = ref('')
const loadError = ref(false)
let loadSeq = 0

async function load() {
  const wf = props.workflowId
  const ref = props.docRef
  if (!wf || !ref) {
    loadSeq++
    store.clearDetail()
    loadedKey.value = ''
    loadError.value = false
    return
  }
  const mine = ++loadSeq
  loadError.value = false
  try {
    await store.loadDetail(wf, ref)
    if (mine !== loadSeq) return
    loadedKey.value = keyOf(wf, ref)
  } catch {
    if (mine !== loadSeq) return
    store.clearDetail()
    loadedKey.value = ''
    loadError.value = true
  }
}
watch(() => [props.workflowId, props.docRef], () => void load(), { immediate: true })

const detail = computed(() => (loadedKey.value === currentKey.value ? store.detail : null))
/**
 * 发起一次异步写（保存 / 还原 / 发评论）或取数（修订历史）时先记下 currentKey，回来之后用它对一下：
 * 这个组件在文档之间是复用的，一个来回的工夫里用户可能已经在应用内切去了别的文档。不是同一篇，
 * 就不能再动现在这一篇的详情、编辑框、评论框、草稿与历史窗——否则会把上一篇的内容挂在新地址下面，
 * 或把别的文档里没发出去 / 没保存的内容冲掉。
 */
const stillOn = (key: string) => currentKey.value === key
const isArtifact = computed(() => detail.value?.doc.render === 'html')
watch(() => detail.value?.doc.id, () => { showArtifactSource.value = false })

// ---- 大纲 ----
const tocHeadings = computed(() => (detail.value ? parseDocHeadings(detail.value.body) : []))
const tocActive = ref('')
let tocObserver: IntersectionObserver | null = null

function scrollToAnchor(anchor: string) {
  const el = document.getElementById(anchor)
  el?.scrollIntoView({ behavior: 'smooth', block: 'start' })
  tocActive.value = anchor
  const hash = `#${anchor}`
  if (window.location.hash !== hash) {
    history.replaceState(null, '', hash)
  }
}

function setupTocObserver() {
  tocObserver?.disconnect()
  tocObserver = null
  if (!detail.value) return
  const heads = parseDocHeadings(detail.value.body)
  if (!heads.length) return
  tocObserver = new IntersectionObserver(
    entries => {
      const visible = entries.filter(e => e.isIntersecting).sort((a, b) => a.boundingClientRect.top - b.boundingClientRect.top)
      if (visible[0]?.target?.id) tocActive.value = visible[0].target.id
    },
    { rootMargin: '-20% 0px -70% 0px', threshold: 0 },
  )
  nextTick(() => {
    for (const h of heads) {
      const el = document.getElementById(h.anchor)
      if (el) tocObserver?.observe(el)
    }
    if (route.hash) scrollToAnchor(route.hash.replace(/^#/, ''))
  })
}

watch(
  () => detail.value?.body,
  () => nextTick(() => setupTocObserver()),
)
// 同一篇文档里换了锚点（搜索命中、反链带锚点）：正文没变，要自己滚过去
watch(
  () => route.hash,
  h => {
    if (h && detail.value) nextTick(() => scrollToAnchor(h.replace(/^#/, '')))
  },
)

onBeforeUnmount(() => {
  tocObserver?.disconnect()
})

// ---- 编辑 ----
const editOpen = ref(false)
const editBody = ref('')
const editTitle = ref('')
const editExpectedRev = ref(0)
const editPreview = ref(false)

// 没保存的编辑存本机草稿（#144）：用户在编辑框里动过手（input 事件）就算「有修改」。
// 保存成功 / 点「丢弃」/ 重新加载最新版本才清掉；关掉弹窗（Esc、点外面）不算丢弃——草稿还在，
// 文档页上会出现「继续编辑」提示，切到别的文档、刷新页面之后回来也一样。
// 草稿记着它基于哪个 rev：文档之后被改过，保存时走原有的 409 冲突流程，不会悄悄覆盖。
const editTouched = ref(false)
interface EditDraft {
  touched: boolean
  title: string
  body: string
  rev: number
}
function isEditDraft(d: unknown): d is EditDraft {
  const o = d as Partial<EditDraft> | null
  return !!o && typeof o.touched === 'boolean' && typeof o.title === 'string' && typeof o.body === 'string' && typeof o.rev === 'number'
}
const EMPTY_EDIT: EditDraft = { touched: false, title: '', body: '', rev: 0 }
const editDraft = useLocalDraft<EditDraft>({
  scope: () => (props.workflowId && detail.value ? draftScopes.docEdit(props.workflowId, detail.value.doc.path) : null),
  read: () => (editTouched.value ? { touched: true, title: editTitle.value, body: editBody.value, rev: editExpectedRev.value } : EMPTY_EDIT),
  apply: saved => {
    editOpen.value = false
    editTouched.value = saved !== null
    editTitle.value = saved ? saved.title : ''
    editBody.value = saved ? saved.body : ''
    editExpectedRev.value = saved ? saved.rev : 0
    editPreview.value = false
  },
  isEmpty: d => !d.touched,
  validate: isEditDraft,
  maxChars: 400_000,
})
/** 草稿基于的 rev 已经落后于文档当前的 rev：保存时会提示冲突。 */
const editStale = computed(() => editTouched.value && !!detail.value && editExpectedRev.value !== detail.value.doc.rev)

const conflictOpen = ref(false)
const conflictServerBody = ref('')
const conflictServerRev = ref(0)

function openEdit() {
  if (!detail.value) return
  // 有没保存的修改就接着编辑，不拿服务端正文覆盖它
  if (!editTouched.value) {
    editBody.value = detail.value.body
    editTitle.value = detail.value.doc.title
    editExpectedRev.value = detail.value.doc.rev
  }
  editOpen.value = true
}

/** 丢弃没保存的修改：删掉草稿、关掉编辑框，回到文档当前版本。 */
function discardEdit() {
  editDraft.discard()
}

async function saveEdit() {
  const cur = detail.value
  if (!cur || !props.workflowId) return
  const wf = props.workflowId
  const key = currentKey.value
  try {
    await projectDocsApi.update(wf, cur.doc.path, {
      body: editBody.value,
      title: editTitle.value,
      expected_rev: editExpectedRev.value,
    })
    if (stillOn(key)) {
      editOpen.value = false
      editDraft.clear()
      editTouched.value = false
      await store.loadDetail(wf, cur.doc.path)
    } else {
      // 保存在路上用户切去了别的文档：现在这一篇的编辑框、草稿与详情一概不碰；
      // 已经保存成功的这一篇，本机留着的那份编辑草稿没用了，按它自己的作用域删掉，免得回去又提示「有没保存的编辑」。
      clearLocalDraft(draftScopes.docEdit(wf, cur.doc.path))
    }
    emit('changed')
    toast.success('已保存')
  } catch (e: unknown) {
    const err = e as { response?: { status?: number; data?: { error?: string; current_rev?: number } } }
    if (err.response?.status === 409) {
      if (!stillOn(key)) {
        // 用户已经在看别的文档：不为已经离开的那一篇弹冲突窗，但要说清楚它没保存上。
        toast.error(`「${cur.doc.title || cur.doc.path}」没有保存：它已被别人改过，回到那一篇再保存`)
        return
      }
      let serverRev = err.response.data?.current_rev ?? cur.doc.rev
      let serverBody = ''
      try {
        const latest = await projectDocsApi.get(wf, cur.doc.path)
        serverBody = latest.body
        serverRev = latest.doc.rev
      } catch {
        serverBody = ''
      }
      if (!stillOn(key)) {
        toast.error(`「${cur.doc.title || cur.doc.path}」没有保存：它已被别人改过，回到那一篇再保存`)
        return
      }
      conflictServerRev.value = serverRev
      conflictServerBody.value = serverBody
      conflictOpen.value = true
    } else {
      toast.error(err.response?.data?.error || '保存失败')
    }
  }
}

async function reloadAfterConflict() {
  const cur = detail.value
  if (!cur || !props.workflowId) return
  conflictOpen.value = false
  editOpen.value = false
  // 「重新加载」就是放弃本地修改：草稿一并清掉
  editDraft.clear()
  editTouched.value = false
  await store.loadDetail(props.workflowId, cur.doc.path)
  toast.message('已加载最新版本')
}

function viewServerVersionInConflict() {
  editBody.value = conflictServerBody.value
  editExpectedRev.value = conflictServerRev.value
  conflictOpen.value = false
  toast.message('已填入服务端版本，请核对后保存')
}

// ---- 修订历史 ----
const historyOpen = ref(false)
const revisions = ref<{ rev: number; summary: string; created_at: string }[]>([])
const historyViewRev = ref(0)
const historyBody = ref('')
const diffLines = computed(() => {
  if (!detail.value || !historyBody.value) return []
  return lineDiffLcs(historyBody.value, detail.value.body)
})

// 历史窗是针对打开它的那一篇的：文档换了（比如浏览器后退）就收起，别让上一篇的修订号去还原现在这一篇
watch(currentKey, () => {
  historyOpen.value = false
})

async function openHistory() {
  const cur = detail.value
  if (!cur || !props.workflowId) return
  const key = currentKey.value
  const res = await projectDocsApi.revisions(props.workflowId, cur.doc.path)
  // 取历史的路上用户切去了别的文档：不为已经离开的那一篇弹历史窗（窗里的修订号会被拿去还原现在这一篇）
  if (!stillOn(key)) return
  revisions.value = res.revisions
  historyViewRev.value = 0
  historyBody.value = ''
  historyOpen.value = true
}

async function viewRev(rev: number) {
  const cur = detail.value
  if (!cur || !props.workflowId) return
  const key = currentKey.value
  historyViewRev.value = rev
  const d = await projectDocsApi.get(props.workflowId, cur.doc.path, rev)
  if (!stillOn(key)) return
  historyBody.value = d.body
}

async function restoreRev(rev: number) {
  const cur = detail.value
  if (!cur || !props.workflowId) return
  const wf = props.workflowId
  const key = currentKey.value
  try {
    await projectDocsApi.restore(wf, cur.doc.path, rev)
  } catch (e: unknown) {
    // 以前这里没有错误处理：服务端拒绝时用户看不到任何提示，像是「点了没反应」（#214）
    const err = e as { response?: { data?: { error?: string } } }
    toast.error(err.response?.data?.error || '还原失败')
    return
  }
  // 还原在路上用户切去了别的文档：不能再重新加载上一篇（会把它的内容挂在新地址下面），列表照常刷新、提示照常给
  if (stillOn(key)) {
    historyOpen.value = false
    await store.loadDetail(wf, cur.doc.path)
  }
  emit('changed')
  toast.success(`已从 rev ${rev} 还原`)
}

// ---- 评论 ----
const commentAnchor = ref('')
const commentText = ref('')
const commentInput = ref<HTMLTextAreaElement | null>(null)

// 没发出去的评论存本机草稿（#144，作用域按文档）：切到别的文档、刷新页面之后回来还在；发送成功 / 点「丢弃」才清掉
interface CommentDraft {
  anchor: string
  text: string
}
function isCommentDraft(d: unknown): d is CommentDraft {
  const o = d as Partial<CommentDraft> | null
  return !!o && typeof o.anchor === 'string' && typeof o.text === 'string'
}
const {
  restored: commentRestored,
  clear: clearCommentDraft,
  discard: discardCommentDraft,
} = useLocalDraft<CommentDraft>({
  scope: () => (props.workflowId && detail.value ? draftScopes.docComment(props.workflowId, detail.value.doc.path) : null),
  read: () => ({ anchor: commentAnchor.value, text: commentText.value }),
  apply: saved => {
    commentAnchor.value = saved ? saved.anchor : ''
    commentText.value = saved ? saved.text : ''
  },
  isEmpty: d => d.text.trim() === '',
  validate: isCommentDraft,
})

function focusComment(anchor: string) {
  commentAnchor.value = anchor
  commentText.value = ''
  nextTick(() => commentInput.value?.focus())
  const box = document.querySelector('[data-testid="docs-comment-box"]')
  box?.scrollIntoView({ behavior: 'smooth', block: 'nearest' })
}

// 正在发送的评论按文档分开记：甲的评论还在路上时，切到乙照样能给乙发；同一篇连点才会被挡住（防重复发送）。
const commentSendingKeys = reactive(new Set<string>())
const commentSending = computed(() => commentSendingKeys.has(currentKey.value))

async function submitComment() {
  const cur = detail.value
  const text = commentText.value.trim()
  const key = currentKey.value
  if (!cur || !props.workflowId || !text || commentSendingKeys.has(key)) return
  const scope = draftScopes.docComment(props.workflowId, cur.doc.path)
  commentSendingKeys.add(key)
  try {
    // 评论存库（智能体用 project_docs comments 读取），同时给文档所属的工作流排一条追加消息
    await artifactsApi.postComment(cur.doc.id, commentAnchor.value, text)
  } finally {
    commentSendingKeys.delete(key)
  }
  if (!stillOn(key)) {
    // 发送在路上用户切去了别的文档：现在这一篇的评论框与草稿一概不碰（草稿里是它自己没发出去的话）；
    // 发出去的那一篇留在本机的草稿已经没用了，按它自己的作用域删掉，免得回去又看到已经发过的话。
    clearLocalDraft(scope)
  } else {
    // 输入框里还是刚发出去的那一段才清空；发送期间接着写的内容不能被冲掉。
    if (commentText.value.trim() === text) {
      commentText.value = ''
      clearCommentDraft()
    }
    void commentsRef.value?.refresh()
  }
  toast.success('评论已加入收件箱')
}

// ---- 导出当前这篇 ----
function downloadCurrentMd() {
  if (!detail.value) return
  const blob = new Blob([detail.value.body], { type: 'text/markdown;charset=utf-8' })
  const a = document.createElement('a')
  a.href = URL.createObjectURL(blob)
  a.download = detail.value.doc.path.split('/').pop() || 'doc.md'
  a.click()
  URL.revokeObjectURL(a.href)
}

async function copyMarkdown() {
  if (!detail.value) return
  await navigator.clipboard.writeText(detail.value.body)
  toast.success('已复制 Markdown')
}
</script>

<template>
  <div class="min-w-0" data-testid="doc-viewer">
    <PageState v-if="loadError" kind="empty" :icon="FileX" title="没有找到这篇文档" hint="可能已被删除，或路径改过了。" data-testid="doc-viewer-missing" />
    <PageState v-else-if="!detail" kind="loading" title="文档加载中…" data-testid="doc-viewer-loading" />
    <div v-else class="flex gap-6 max-w-4xl mx-auto lg:max-w-none">
      <div class="min-w-0 flex-1">
        <div class="flex flex-wrap items-center gap-2 mb-4">
          <!-- 手机上标题（带版本）单独占一行、可折行；五个按钮另起一行排满，不再和标题抢位置折成两行（#156 批 3）。宽屏仍是同一行：md:contents 让这两个包装层不参与布局 -->
          <div class="flex min-w-0 items-baseline gap-2 max-md:basis-full md:contents">
            <h1 class="text-lg font-semibold">{{ detail.doc.title }}</h1>
            <span class="text-[10px] font-mono text-muted-foreground">rev {{ detail.doc.rev }}</span>
          </div>
          <div class="flex flex-wrap items-center gap-2 max-md:basis-full md:contents" data-testid="doc-actions">
            <Button
              v-if="isArtifact"
              size="sm"
              variant="outline"
              data-testid="docs-artifact-source-toggle"
              @click="showArtifactSource = !showArtifactSource"
            >
              {{ showArtifactSource ? '看效果' : '看源码' }}
            </Button>
            <Button size="sm" variant="outline" data-testid="docs-edit-btn" title="编辑" aria-label="编辑文档" @click="openEdit">
              <Pencil class="size-3.5 mr-1" />编辑
            </Button>
            <Button
              size="sm"
              variant="outline"
              data-testid="docs-history-btn"
              title="修订历史"
              aria-label="修订历史"
              @click="openHistory"
            >
              <History class="size-3.5" />
            </Button>
            <Button
              size="sm"
              variant="outline"
              data-testid="docs-download-md"
              title="下载当前 Markdown"
              aria-label="下载当前 Markdown"
              @click="downloadCurrentMd"
            >
              <FileDown class="size-3.5" />
            </Button>
            <Button
              size="sm"
              variant="outline"
              data-testid="docs-copy-md"
              title="复制 Markdown"
              aria-label="复制 Markdown"
              @click="copyMarkdown"
            >
              <ClipboardCopy class="size-3.5" />
            </Button>
            <Button
              v-if="isNarrow && tocHeadings.length"
              size="sm"
              variant="outline"
              data-testid="docs-drawer-toc"
              title="大纲"
              aria-label="打开大纲"
              @click="tocDrawerOpen = true"
            >
              大纲
            </Button>
          </div>
        </div>
        <!-- 有没保存的编辑（本机草稿）：关掉编辑框、切走再回来、刷新之后都在这里接着 -->
        <div
          v-if="editTouched && !editOpen"
          class="mb-4 flex flex-wrap items-center gap-x-2 gap-y-1.5 rounded-xs border border-amber-500/40 bg-amber-500/5 px-3 py-2 text-xs"
          data-testid="docs-edit-draft-banner"
        >
          <span class="min-w-0 flex-1 basis-56 leading-relaxed text-muted-foreground">
            这篇文档有没保存的编辑（只保存在这个浏览器里）<template v-if="editStale">；文档在这之后更新过（编辑基于 rev {{ editExpectedRev }}，现在是 rev {{ detail.doc.rev }}），保存时会提示冲突，可以对照对方版本合并</template>
          </span>
          <Button size="sm" variant="outline" class="h-7 text-xs" data-testid="docs-edit-draft-resume" @click="editOpen = true">继续编辑</Button>
          <Button size="sm" variant="ghost" class="h-7 text-xs" data-testid="docs-edit-draft-discard" @click="discardEdit">丢弃</Button>
        </div>
        <DocDecisionBar
          :workflow-id="workflowId"
          :decisions="detail.decisions"
          :historic="detail.historic"
        />
        <ArtifactFrame v-if="isArtifact && !showArtifactSource" :doc-id="detail.doc.id" :rev="detail.doc.rev" />
        <pre
          v-else-if="isArtifact"
          class="max-h-[70vh] overflow-auto rounded-md border border-border/70 bg-muted/40 p-3 text-[12px] leading-relaxed"
          data-testid="docs-artifact-source"
        >{{ detail.body }}</pre>
        <DocMarkdownBody
          v-else
          :content="detail.body"
          :workflow-id="workflowId"
          :doc-path="detail.doc.path"
          :decisions="detail.decisions"
          :historic="detail.historic"
          @comment="focusComment"
        />
        <DocLinksPanel
          :workflow-id="workflowId"
          :doc-path="detail.doc.path"
          :rev="detail.doc.rev"
          @open="(wf, path) => emit('open-doc', wf, path)"
        />
        <ArtifactComments ref="commentsRef" :doc-id="detail.doc.id" />
        <div class="mt-6 border-t border-border pt-4" data-testid="docs-comment-box">
          <p class="text-xs text-muted-foreground mb-2">评论会进入收件箱（不阻塞智能体）</p>
          <input
            v-model="commentAnchor"
            class="mb-2 w-full max-w-xs rounded-xs border border-border bg-background px-2 py-1 text-xs"
            placeholder="锚点（可选）"
            data-testid="docs-comment-anchor"
          />
          <textarea
            ref="commentInput"
            v-model="commentText"
            class="w-full rounded-xs border border-border bg-background p-2 text-sm min-h-[60px]"
            placeholder="写下评论…"
            data-testid="docs-comment-input"
          />
          <DraftRestoredNote v-if="commentRestored" class="mt-1" what="评论" testid="docs-comment-draft-note" @discard="discardCommentDraft" />
          <Button size="sm" class="mt-2" data-testid="docs-comment-submit" :disabled="commentSending" @click="submitComment">发送评论</Button>
        </div>
      </div>
      <aside
        v-if="!isNarrow && tocHeadings.length"
        class="hidden lg:block w-44 shrink-0 text-xs sticky top-4 self-start max-h-[calc(100vh-6rem)] overflow-y-auto"
        data-testid="docs-toc"
      >
        <p class="font-medium mb-1 text-foreground">大纲</p>
        <a
          v-for="h in tocHeadings"
          :key="h.anchor"
          href="#"
          class="block truncate py-0.5 hover:text-primary"
          :class="tocActive === h.anchor ? 'text-primary font-medium' : 'text-muted-foreground'"
          :style="{ paddingLeft: (h.level - 1) * 8 + 'px' }"
          :data-testid="`docs-toc-link-${h.anchor}`"
          @click.prevent="scrollToAnchor(h.anchor)"
        >{{ h.text }}</a>
      </aside>
    </div>

    <Sheet v-model:open="tocDrawerOpen">
      <SheetContent side="right" class="w-[75vw] max-w-xs">
        <SheetHeader><SheetTitle>大纲</SheetTitle></SheetHeader>
        <nav class="mt-4 space-y-0.5 text-xs">
          <a
            v-for="h in tocHeadings"
            :key="h.anchor"
            href="#"
            class="block truncate py-1"
            @click.prevent="scrollToAnchor(h.anchor); tocDrawerOpen = false"
          >{{ h.text }}</a>
        </nav>
      </SheetContent>
    </Sheet>

    <Dialog v-model:open="editOpen">
      <DialogContent class="max-w-2xl max-h-[90vh] overflow-y-auto" data-testid="docs-edit-dialog">
        <DialogHeader><DialogTitle>编辑文档</DialogTitle></DialogHeader>
        <input v-model="editTitle" class="w-full mb-2 rounded-xs border border-border px-2 py-1 text-sm bg-background" placeholder="标题" @input="editTouched = true" />
        <div class="flex gap-2 mb-2">
          <Button size="sm" :variant="editPreview ? 'outline' : 'default'" @click="editPreview = false">编辑</Button>
          <Button size="sm" :variant="editPreview ? 'default' : 'outline'" @click="editPreview = true">预览</Button>
        </div>
        <textarea
          v-if="!editPreview"
          v-model="editBody"
          class="w-full min-h-[240px] font-mono text-xs border border-border rounded-xs p-2 bg-background"
          data-testid="docs-edit-body"
          @input="editTouched = true"
        />
        <DocMarkdownBody v-else :content="editBody" />
        <div class="mt-3 flex flex-wrap items-center gap-2">
          <Button data-testid="docs-edit-save" @click="saveEdit">保存</Button>
          <Button v-if="editTouched" variant="ghost" size="sm" data-testid="docs-edit-discard" title="丢弃这些没保存的修改并关闭" @click="discardEdit">丢弃修改</Button>
          <span v-if="editTouched" class="text-[11px] text-muted-foreground">没保存的修改会暂存在这个浏览器里</span>
        </div>
      </DialogContent>
    </Dialog>

    <Dialog v-model:open="conflictOpen">
      <DialogContent data-testid="docs-conflict-dialog">
        <DialogHeader>
          <DialogTitle>版本冲突</DialogTitle>
          <DialogDescription>其他人或智能体已更新此文（当前 rev {{ conflictServerRev }}）。你可以重新加载放弃本地修改，或查看服务端正文后继续编辑。</DialogDescription>
        </DialogHeader>
        <div class="flex flex-wrap gap-2">
          <Button size="sm" data-testid="docs-conflict-reload" @click="reloadAfterConflict">重新加载</Button>
          <Button size="sm" variant="outline" data-testid="docs-conflict-view-server" @click="viewServerVersionInConflict">查看对方版本</Button>
        </div>
      </DialogContent>
    </Dialog>

    <Dialog v-model:open="historyOpen">
      <DialogContent class="max-w-2xl max-h-[90vh] overflow-y-auto" data-testid="docs-history-dialog">
        <DialogHeader><DialogTitle>修订历史</DialogTitle></DialogHeader>
        <ul class="text-xs space-y-1 max-h-32 overflow-y-auto">
          <li v-for="r in revisions" :key="r.rev" class="flex flex-wrap items-center gap-1">
            <button type="button" class="underline" :data-testid="`docs-history-rev-${r.rev}`" @click="viewRev(r.rev)">rev {{ r.rev }}</button>
            <span class="text-muted-foreground">— {{ r.summary }}</span>
            <Button size="sm" variant="ghost" class="h-6" data-testid="docs-history-restore" @click="restoreRev(r.rev)">还原</Button>
          </li>
        </ul>
        <p v-if="historyViewRev" class="text-[10px] text-muted-foreground mt-2">正在对比 rev {{ historyViewRev }} 与当前正文</p>
        <pre v-if="diffLines.length" class="mt-3 max-h-48 overflow-auto text-[10px] font-mono border border-border rounded-xs p-2 bg-muted/20" data-testid="docs-diff">
          <div v-for="(ln, i) in diffLines" :key="i" :class="ln.type === 'add' ? 'text-green-600 dark:text-green-400' : ln.type === 'del' ? 'text-red-600 dark:text-red-400 line-through' : ''">{{ ln.text }}</div>
        </pre>
      </DialogContent>
    </Dialog>
  </div>
</template>
