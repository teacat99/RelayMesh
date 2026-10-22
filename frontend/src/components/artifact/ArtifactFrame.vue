<script setup lang="ts">
// HTML Artifact 的沙箱渲染框（设计稿 06 §6）：
// - iframe 只开 allow-scripts，不开 allow-same-origin：页面在不透明源里运行，拿不到 cookie / 本地存储 / 宿主 DOM；
// - 页面地址是一次性票据 URL，响应头自带 sandbox + connect-src 'none' 的 CSP，页面不能发起任何网络请求；
// - 页面和宿主只能通过 postMessage 说话：宿主先校验 event.source，再按白名单解析，数据操作以「查看者」身份代为执行。
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { RefreshCw } from 'lucide-vue-next'
import { artifactsApi } from '../../api/artifacts'
import { projectErrorMessage } from '../../api/projects'
import { WriteLimiter, bridgeReply, dataItemForPage, parseBridgeMessage, MIN_FRAME_HEIGHT } from '../../lib/artifactBridge'

const props = defineProps<{ docId: number; rev?: number }>()

const frame = ref<HTMLIFrameElement | null>(null)
const src = ref('')
const height = ref(360)
const error = ref('')
const limiter = new WriteLimiter(20, 1000)
// 页面自己写的版本 / 删除：服务端随后推来的同一次变化不要再通知回页面（否则页面一响应就会再写，形成回声循环）
const ownVersion = new Map<string, number>()
const ownDeleted = new Set<string>()
// 写入还在途中时到达的同 key 事件先扣住，等本次写入的版本记下后再判断（事件常常比 HTTP 响应先到）
const inflight = new Map<string, number>()
const held: ArtifactEventDetail[] = []

interface ArtifactEventDetail { type?: string; doc_id?: number; key?: string; version?: number; deleted?: boolean }

async function load() {
  src.value = ''
  error.value = ''
  try {
    src.value = await artifactsApi.renderURL(props.docId)
  } catch (e) {
    error.value = projectErrorMessage(e, '无法加载这个 Artifact')
  }
}

function reply(id: number, ok: boolean, payload: unknown) {
  // 页面在不透明源里，没有可指定的目标源；已经用 event.source 确认过对方就是这个 iframe。
  frame.value?.contentWindow?.postMessage(bridgeReply(id, ok, payload), '*')
}

async function onMessage(ev: MessageEvent) {
  const win = frame.value?.contentWindow
  if (!win || ev.source !== win) return
  const req = parseBridgeMessage(ev.data)
  if (!req) return
  if (req.kind === 'resize') {
    height.value = Math.max(MIN_FRAME_HEIGHT, req.height)
    return
  }
  try {
    switch (req.kind) {
      case 'data.list': {
        const items = await artifactsApi.dataList(props.docId)
        reply(req.id, true, items.map(dataItemForPage))
        return
      }
      case 'data.get': {
        reply(req.id, true, dataItemForPage(await artifactsApi.dataGet(props.docId, req.key)))
        return
      }
      case 'data.set':
      case 'data.delete':
      case 'comment': {
        if (!limiter.allow()) {
          reply(req.id, false, '写入太频繁，请稍后再试')
          return
        }
        const wkey = req.kind === 'comment' ? '' : req.key
        if (wkey) inflight.set(wkey, (inflight.get(wkey) ?? 0) + 1)
        try {
        if (req.kind === 'data.set') {
          const item = await artifactsApi.dataSet(props.docId, req.key, req.value, req.expected)
          ownVersion.set(item.key, item.version)
          ownDeleted.delete(item.key)
          reply(req.id, true, { key: item.key, version: item.version })
        } else if (req.kind === 'data.delete') {
          const deleted = await artifactsApi.dataDelete(props.docId, req.key)
          if (deleted) {
            ownDeleted.add(req.key)
            ownVersion.delete(req.key)
          }
          reply(req.id, true, { deleted })
        } else {
          const c = await artifactsApi.postComment(props.docId, req.anchor, req.text)
          reply(req.id, true, { id: c.id })
        }
        } finally {
          if (wkey) {
            const left = (inflight.get(wkey) ?? 1) - 1
            if (left > 0) inflight.set(wkey, left)
            else {
              inflight.delete(wkey)
              const pending = held.splice(0).filter(h => {
                if (h.key !== wkey) { held.push(h); return false }
                return true
              })
              pending.forEach(deliver)
            }
          }
        }
        return
      }
    }
  } catch (e) {
    reply(req.id, false, projectErrorMessage(e, '操作失败'))
  }
}

// 服务端推来的数据变化（Agent 写了数据 / 别处改了）：告诉页面，让它自己决定要不要重新读。
function onArtifactEvent(ev: Event) {
  const d = (ev as CustomEvent).detail as ArtifactEventDetail | undefined
  if (!d || d.type !== 'artifact_data_updated' || d.doc_id !== props.docId) return
  if (d.key && inflight.has(d.key)) {
    held.push(d)
    return
  }
  deliver(d)
}

function deliver(d: ArtifactEventDetail) {
  const key = d.key ?? ''
  if (d.deleted) {
    if (ownDeleted.delete(key)) return
  } else if (key && typeof d.version === 'number' && (ownVersion.get(key) ?? 0) >= d.version) {
    return
  }
  frame.value?.contentWindow?.postMessage({ rm: 1, type: 'data.changed', key: d.key ?? '' }, '*')
}

watch(() => [props.docId, props.rev], () => void load())
onMounted(() => {
  window.addEventListener('message', onMessage)
  window.addEventListener('rm-artifact-event', onArtifactEvent)
  void load()
})
onBeforeUnmount(() => {
  window.removeEventListener('message', onMessage)
  window.removeEventListener('rm-artifact-event', onArtifactEvent)
})
</script>

<template>
  <div class="space-y-1.5" data-testid="artifact-frame-wrap">
    <div v-if="error" class="rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-[12px] text-destructive" data-testid="artifact-error">{{ error }}</div>
    <iframe
      v-else-if="src"
      ref="frame"
      :src="src"
      sandbox="allow-scripts"
      referrerpolicy="no-referrer"
      title="Artifact"
      class="block w-full rounded-md border border-border/70 bg-white"
      :style="{ height: height + 'px' }"
      data-testid="artifact-frame"
    />
    <div v-else class="py-8 text-center text-[12px] text-muted-foreground">加载 Artifact…</div>
    <div class="flex items-center gap-2 text-[11px] text-muted-foreground">
      <span>在隔离沙箱中运行：页面不能联网，也读不到登录信息。</span>
      <button type="button" class="ml-auto inline-flex cursor-pointer items-center gap-1 rounded px-1.5 py-0.5 hover:bg-accent" title="重新加载" data-testid="artifact-reload" @click="load">
        <RefreshCw class="size-3" />重新加载
      </button>
    </div>
  </div>
</template>
