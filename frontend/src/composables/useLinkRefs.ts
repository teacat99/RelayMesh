import { computed, nextTick } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import type { ConfirmItem } from '../api/confirms'
import type { FeedbackSession } from '../api/types'
import { useConfirmStore } from '../stores/confirms'
import { useSessionStore } from '../stores/session'
import { useWorkItemStore } from '../stores/workItems'
import { openDoc } from '../lib/docNavClient'
import { linkRefHref, type LinkRef, type LinkRefIndex, type LinkRefKind } from '../lib/linkRefs'
import { ensureDocIndex, docIndexVersion, lookupDoc, noteDocMiss } from './useDocIndex'
import { requestSessionJump } from './useSessionJump'
import { useTaskRefs } from './useTaskRefs'

const KINDS: readonly LinkRefKind[] = ['task', 'confirm', 'doc', 'session']

/** 数据还没就绪时共用的空索引（只读，从不被改），每个实例不必各 new 一个。 */
const NO_CONFIRMS: ReadonlyMap<number, ConfirmItem> = new Map()
const NO_SESSIONS: ReadonlyMap<string, FeedbackSession> = new Map()

function firstLine(text: string | undefined): string {
  return (text ?? '').split('\n').find(l => l.trim())?.trim() ?? ''
}

/**
 * 正文里的引用链接（#161）：任务 #N、确认项 Q<N>、文档 [[路径]]、会话号 sess-xxxxxxxx 统一的「哪些目标存在」与「点开」。
 * 识别规则在 lib/linkRefs.ts，DOM 装饰在 lib/linkRefsDom.ts，这里把它们接到各个 store 上。
 *
 * 每一种引用只在它的数据已经就绪时才解析（没就绪就当作没有目标，正文保持原样）：
 *   - 任务：任务 store 的 feed 正是这个工作流且加载完；
 *   - 确认项：确认 store 正是这个工作流且加载完（待答复 + 最近处理的）；
 *   - 文档：按工作流懒加载的文档索引（composables/useDocIndex.ts）；
 *   - 会话号：会话 store 里选中的就是这个工作流，用它的会话列表。
 *
 * 点击：普通左键点击拦下来做站内动作（任务弹窗 / 点名确认卡 / 站内打开文档 / 滚到那一轮），
 * 带修饰键（⌘ Ctrl Shift Alt）或中键则交给浏览器，在新标签页打开真实的深链地址（lib/linkRefs.ts 的 linkRefHref）。
 */
export function useLinkRefs(workflowId: () => string | undefined) {
  const router = useRouter()
  const route = useRoute()
  const taskRefs = useTaskRefs(workflowId)
  const workStore = useWorkItemStore()
  const confirmStore = useConfirmStore()
  const sessionStore = useSessionStore()

  /** 有工作流才谈得上引用；具体每一种引用是否可解析，由下面各自的数据就绪情况决定。 */
  const active = computed(() => !!workflowId())

  // 性能（#186 C2a）：下面两份索引（确认项按序号、会话按短哈希）要扫整张表，而 useLinkRefs 每段 Markdown 正文都有一个实例——
  // 长会话里几百个实例各扫一遍就是几万次计算（生产实测占打开过程忙碌时间的约一成），所以索引在 store 里建一份共用，
  // 这里只负责「这个实例的工作流对不对、数据就绪没有」的判断，是 O(1) 的。
  const confirmsReady = computed(() => {
    const id = workflowId()
    return !!id && confirmStore.workflowId === id && confirmStore.loaded
  })
  const confirmBySeq = computed<ReadonlyMap<number, ConfirmItem>>(() => (confirmsReady.value ? confirmStore.timelineSeqIndex.bySeq : NO_CONFIRMS))

  /** 选中的会话属于这个工作流吗：会话号只在这种情形下解析（见 sessionByHash / ensureSessions）。 */
  const sessionScopeReady = computed(() => {
    const id = workflowId()
    const sel = sessionStore.selectedSession
    return !!id && (sel?.workflow_id || sel?.session_id || '') === id
  })

  const sessionByHash = computed<ReadonlyMap<string, FeedbackSession>>(() => (sessionScopeReady.value ? sessionStore.sessionByHash : NO_SESSIONS))

  /** 任何一种来源的「存在集合」变了它就变；装饰逻辑据此重新装饰（幂等）。没有工作流时为空串。 */
  const signature = computed(() => {
    if (!active.value) return ''
    const maxSeq = confirmsReady.value ? confirmStore.timelineSeqIndex.maxSeq : 0
    // 选中会话归属本工作流这件事本身也在签名里：直接打开文档页时正文常常比「选中默认会话」先到，那一次装饰里 ensureSessions 因为
    // 还没选中而什么都没做；整表没加载时 sessionByHash 是空的、大小不变，没有这一项签名就不变，装饰不会重来，会话号引用就一直是纯文本
    return [taskRefs.signature.value, `${confirmBySeq.value.size}:${maxSeq}`, docIndexVersion.value, sessionByHash.value.size, sessionScopeReady.value ? 1 : 0].join('|')
  })

  const index: LinkRefIndex = {
    task: seq => (taskRefs.has(seq) ? { title: workStore.feedTitleBySeq.get(seq) } : null),
    confirm: n => {
      const item = confirmBySeq.value.get(n)
      return item ? { key: item.ref || `Q${n}`, title: item.title } : null
    },
    doc: path => {
      const id = workflowId()
      if (!id) return null
      const hit = lookupDoc(id, path)
      if (!hit) noteDocMiss(id) // 没命中：索引可能已经旧了，最多每 15 秒补刷一次
      return hit
    },
    session: hash => {
      const s = sessionByHash.value.get(hash)
      return s ? { key: s.session_id, title: s.title || firstLine(s.summary) } : null
    },
  }

  /** 正文里出现了 [[ 时调用：保证文档索引已加载（只请求一次，之后按缓存与未命中规则刷新）。 */
  function ensureDocs() {
    const id = workflowId()
    if (id) ensureDocIndex(id)
  }

  /**
   * 正文里出现了 sess- 引用时调用：会话号要靠选中工作流的整张会话表解析，而任务 / 文档 / 项目等页面不再顺带加载它（#169），
   * 所以在这里按需保证一次（走 store 的单飞与缓存；已有缓存不再请求，会话页本来就加载着）。
   * 只管「选中的就是这个工作流」的情形——sessionByHash 也只在这种情形下解析，别的工作流的会话号本来就不解析。
   */
  function ensureSessions() {
    const id = workflowId()
    if (!id || !sessionScopeReady.value) return
    sessionStore.ensureWorkflowSessions(id)
  }

  /** 当前是不是正显示这个工作流的会话页（确认卡 / 消息列表都在这个页面上，才能原地点名 / 滚动）。 */
  function onSessionPage(id: string): boolean {
    if (route.name !== 'home') return false
    const sel = sessionStore.selectedSession
    return (sel?.workflow_id || sel?.session_id || '') === id
  }

  async function open(ref: LinkRef): Promise<void> {
    const id = workflowId()
    if (!id) return
    switch (ref.kind) {
      case 'task':
        await taskRefs.open(Number(ref.key))
        return
      case 'doc':
        await openDoc(router, { workflowId: id, path: ref.key, ...(ref.anchor ? { anchor: ref.anchor } : {}) })
        return
      case 'confirm':
        if (onSessionPage(id) && confirmStore.workflowId === id) {
          // 先清空再点名：同一张卡连点两次也要各触发一次
          confirmStore.focusRef = ''
          await nextTick()
          confirmStore.focusRef = ref.key
        } else {
          await router.push(linkRefHref(ref, id))
        }
        return
      case 'session':
        if (onSessionPage(id)) requestSessionJump(id, ref.key)
        else await router.push(linkRefHref(ref, id))
        return
    }
  }

  /** 点击的统一入口：普通左键拦下来做站内动作；带修饰键 / 非左键放行给浏览器（新标签页打开深链）。 */
  function activate(event: MouseEvent, ref: LinkRef): void {
    if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
    event.preventDefault()
    void open(ref)
  }

  /** 容器上的点击委托（v-html 渲染出来的正文用）：点中引用链接就处理并返回 true，调用方不必再往下判断。 */
  function handleClick(event: MouseEvent): boolean {
    const el = (event.target as HTMLElement | null)?.closest?.('a.link-ref') as HTMLAnchorElement | null
    if (!el) return false
    const kind = el.dataset.refKind as LinkRefKind | undefined
    const key = el.dataset.refKey
    if (!kind || !key || !KINDS.includes(kind)) return false
    activate(event, { kind, key, ...(el.dataset.refAnchor ? { anchor: el.dataset.refAnchor } : {}) })
    return true
  }

  return { active, signature, index, ensureDocs, ensureSessions, open, activate, handleClick }
}
