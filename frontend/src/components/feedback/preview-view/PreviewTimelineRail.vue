<script setup lang="ts">
// 预览视图的「右侧轨」（#120 阶段 2；#157 / 追加 #223 改版）：会话列表右边的一条缩略轴。
//
// 列表里每个条目（轮次 / 您的回复 / 追加 / 各种卡片）的根节点都挂了 data-rail-*（见 lib/timelineRail 的 railDomAttrs），
// 这里把它们量出来；轨上**只画非用户消息的类型**（您自己的确认回复与追加不上轨，lib/timelineRail 的 RAIL_STYLE 说了算）。
// 每道刻度的点击区是 16px 高 × 整条轨宽（32px），相邻两道至少隔 16px、不合并；位置尽量按条目的真实位置等比，
// 挤在一起时由 layoutRail 推开。放不下时轨自己上下滚动（滚轮 / 触摸都行，不出滚动条，上下沿渐隐提示还有内容），
// 并跟着会话列表的可视窗口走；用户在轨上拨动（滚轮 / 触摸）之后 1.5 秒内不再自动跟随，免得把正在看的位置抢走。
// 当前可视范围用一块半透明窗口标出；点击跳转、悬停看「类型 · 状态 / 标题 / 时间」。
//
// 性能：ResizeObserver（容器与内容尺寸变化，如图片加载）+ MutationObserver（条目增删、状态翻转）都只做「标记脏」，
// 真正的测量合并到下一帧（rAF）里做一次；会话列表滚动时只更新可视窗口那一块，不重排刻度。
// 窄屏（< md）整条轨 display:none，量出来轨高为 0 就直接跳过，不扫 DOM。
// （#158）刻度列表画在 PreviewRailTicks 里，排布与可视窗口框用 shallowRef 持有：滚动时每帧变的只有窗口框 / 轨自己的 scrollTop，
// 它们只重渲染这个组件的一小块模板，不再带着几百道刻度一起重渲染，也不给刻度数据套深层响应式代理。
import { computed, nextTick, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import PreviewRailTicks from './PreviewRailTicks.vue'
import {
  RAIL_PITCH_PX,
  RAIL_WATCH_ATTRS,
  followRailScroll,
  formatRailTime,
  layoutRail,
  parseRailItem,
  railLabel,
  railViewportBox,
  shouldShowRail,
  type RailItem,
  type RailLayout,
} from '../../../lib/timelineRail'
import { formatStackParts } from '../../../lib/stackTypes'

// 轨的下沿要让开输入区浮在列表底部的那一堆东西（FeedbackInputDock 的悬浮行，z-40，盖在列表容器下缘）：
// 两行芯片约 62px + 8px 间隙，会话上滚时「回到底部」又会浮到芯片上方再占约 31px（6px 间隙 + 25px 高），合计约 101px；
// 有待发图片时再加一排缩略图（约 70px）。所以轨的下沿固定让开 112px（bottom-28；有图 176px = bottom-44），
// 否则最靠下的几道刻度（恰好是最新的条目）会躲在悬浮行或「回到底部」下面，看得见也点不到，悬停提示也会被盖住
// （追加 #223：e2e 长会话一步的 elementFromPoint 与观感截图查出来的）。
// 群聊页（#241）的输入框在列表下面、不盖在列表上（dock=false）：只需要让开列表右下角浮着的「回到最新」小药丸
// （bottom-3 + 约 30px 高 ≈ 42px），所以下沿固定让开 56px（bottom-14）。
const props = withDefaults(
  defineProps<{
    /** 列表的滚动容器（PreviewMessageList 的 chatScrollContainer）；挂载前是 null。 */
    scrollEl: HTMLElement | null
    /** 还有更早的轮次没载入：轨顶出现「更早」标记，点击载入。 */
    hasEarlier: boolean
    loadingEarlier: boolean
    /** 输入区有待发图片：悬浮行变高，轨的下沿跟着让开更多（与列表的底部留白同步）。 */
    hasDraftImages: boolean
    /** 输入区是否浮在列表底部、盖住列表下缘（会话页是；群聊页的输入框在列表下面，传 false）。 */
    dock?: boolean
  }>(),
  { dock: true },
)

const emit = defineEmits<{
  /** 点击刻度：交给列表去滚。 */
  jump: [item: RailItem]
  'load-earlier': []
}>()

/** 用户在轨上拨动（滚轮 / 触摸）之后，这么久之内不自动跟随可视窗口。 */
const FOLLOW_PAUSE_MS = 1500

const trackEl = ref<HTMLElement | null>(null)
const scrollerEl = ref<HTMLElement | null>(null)
const EMPTY_LAYOUT: RailLayout = { ticks: [], height: 0, pitch: RAIL_PITCH_PX }
// 排布是只读的整块数据（layoutRail 每次产出新对象），只会整体替换：shallowRef 就够，不用给几百道刻度套深层响应式代理。
const layout = shallowRef<RailLayout>(EMPTY_LAYOUT)
const trackHeight = ref(0)
const box = shallowRef({ top: 0, height: 0 })

/** 窗口框没变就不赋新对象：整轨重新量一遍之后窗口框多半没变，赋一个同值的新对象照样会触发重渲染。 */
function setBox(next: { top: number; height: number }) {
  const cur = box.value
  if (cur.top !== next.top || cur.height !== next.height) box.value = next
}
/** 轨自己的 scrollTop（镜像）：提示框定位、上下沿渐隐都要用。 */
const railScroll = ref(0)
const show = ref(false)
const hoverId = ref<string | null>(null)

/** 轨内容比可用高度高 = 放不下，轨自己滚动。 */
const overflowing = computed(() => layout.value.height > trackHeight.value + 0.5)

// 上一次排好的刻度的「指纹」：没变就不重新赋值，省掉一次整条轨的重渲染。
let layoutSig = ''
let lastRailUserScrollAt = -Infinity

// 用户正在用轨时（鼠标指针停在轨上，或刚拨过轨）轨不自己动：不能在用户眼皮底下跳，否则又点偏了（追加 #223 的本意）。
// 这期间被拦下的跟随（会话动了，窗口框可能已经跑出轨的可见范围）记在 followHeld 里，拦截一结束就补一次；
// 会话没动就没有被拦下的跟随，所以轨会停在用户拨到的地方，不会自己弹回去。
let pointerInside = false
let followHeld = false
let catchUpTimer: ReturnType<typeof setTimeout> | null = null

function resetFollowHold() {
  pointerInside = false
  followHeld = false
  if (catchUpTimer) clearTimeout(catchUpTimer)
  catchUpTimer = null
}

function clearTicks() {
  show.value = false
  if (layout.value.ticks.length > 0) layout.value = EMPTY_LAYOUT
  layoutSig = ''
  hoverId.value = null
  railScroll.value = 0
  resetFollowHold()
}

function measure() {
  const el = props.scrollEl
  const track = trackEl.value
  if (!el || !track) {
    clearTicks()
    return
  }
  const th = track.clientHeight
  trackHeight.value = th
  // 轨在窄屏是 display:none，量出来高度为 0：不用扫 DOM。
  if (th <= 0) {
    clearTicks()
    return
  }
  // 内容坐标原点：容器顶边 - 已滚动距离；条目位置 = 条目顶边 - 原点。
  const origin = el.getBoundingClientRect().top - el.scrollTop
  const items: RailItem[] = []
  el.querySelectorAll<HTMLElement>('[data-rail-kind]').forEach(node => {
    // 被隐藏的节点（display:none）没有盒子，位置没有意义。
    if (node.getClientRects().length === 0) return
    const item = parseRailItem(attr => node.getAttribute(attr), node.getBoundingClientRect().top - origin)
    if (item) items.push(item)
  })
  const scrollHeight = el.scrollHeight
  const clientHeight = el.clientHeight
  const next = layoutRail(items, { scrollHeight, trackHeight: th })
  if (!shouldShowRail({ scrollHeight, clientHeight, trackHeight: th, tickCount: next.ticks.length })) {
    clearTicks()
    return
  }
  show.value = true
  const sig = `${next.height}|${next.ticks.map(t => `${t.item.id}|${t.item.state}|${formatStackParts(t.item.parts)}|${t.y}|${Math.round(t.item.top)}`).join(',')}`
  if (sig !== layoutSig) {
    layoutSig = sig
    layout.value = next
  }
  setBox(railViewportBox(layout.value, { scrollTop: el.scrollTop, clientHeight, scrollHeight }))
  // 轨内容的高度要等这一轮渲染完才会变，滚动位置得在渲染之后再设，否则会被旧的内容高度夹住。
  void nextTick(followViewport)
}

function updateViewport() {
  const el = props.scrollEl
  if (!el || !show.value) return
  setBox(railViewportBox(layout.value, { scrollTop: el.scrollTop, clientHeight: el.clientHeight, scrollHeight: el.scrollHeight }))
  followViewport()
}

/** 距离「可以跟随」还要多久（毫秒）：0 = 现在就可以；Infinity = 鼠标指针还停在轨上，等它离开。 */
function followHoldMs(): number {
  if (pointerInside) {
    // 保险：漏掉 pointerleave（比如轨在指针下面被藏起来）时，按真实的悬停状态自愈，免得跟随被永久拦住
    if (scrollerEl.value?.matches(':hover')) return Infinity
    pointerInside = false
  }
  return Math.max(0, FOLLOW_PAUSE_MS - (performance.now() - lastRailUserScrollAt))
}

/** 拦截结束（afterMs 之后）补一次跟随；指针还在轨上（Infinity）就不排，等 onRailPointerLeave 来排。 */
function scheduleCatchUp(afterMs: number) {
  if (catchUpTimer) clearTimeout(catchUpTimer)
  catchUpTimer = null
  if (!Number.isFinite(afterMs)) return
  catchUpTimer = setTimeout(() => {
    catchUpTimer = null
    followViewport()
  }, afterMs + 20)
}

/** 轨放不下时，让它跟着可视窗口走（用户正在用轨的话先不动，拦截结束后补一次）。 */
function followViewport() {
  const s = scrollerEl.value
  if (!s || !show.value || !overflowing.value) return
  const hold = followHoldMs()
  if (hold > 0) {
    followHeld = true
    scheduleCatchUp(hold)
    return
  }
  followHeld = false
  const next = followRailScroll(box.value, { scrollTop: s.scrollTop, trackHeight: trackHeight.value, height: layout.value.height })
  if (Math.abs(next - s.scrollTop) >= 1) s.scrollTop = next
}

function onRailScroll() {
  railScroll.value = scrollerEl.value?.scrollTop ?? 0
}

/** 用户在轨上拨动（滚轮 / 触摸）：记下时间，followViewport 在这之后 FOLLOW_PAUSE_MS 内不去动轨。 */
function onRailUserScroll() {
  lastRailUserScrollAt = performance.now()
}

/**
 * 鼠标指针进 / 出轨：指针在轨上时不自动跟随；离开后再安静一个暂停期，才把被拦下的跟随补上。
 * 触摸点不算（触摸靠 touchstart / touchmove 记时间；触屏上的 :hover 会粘住，不能拿它当依据）。
 */
function onRailPointerEnter(ev: PointerEvent) {
  if (ev.pointerType !== 'touch') pointerInside = true
}
function onRailPointerLeave(ev: PointerEvent) {
  if (ev.pointerType === 'touch' || !pointerInside) return
  pointerInside = false
  lastRailUserScrollAt = performance.now()
  if (followHeld) scheduleCatchUp(FOLLOW_PAUSE_MS)
}

let measureRaf = 0
let viewportRaf = 0

function scheduleMeasure() {
  if (measureRaf) return
  measureRaf = requestAnimationFrame(() => {
    measureRaf = 0
    measure()
  })
}

function scheduleViewport() {
  // 整轨正等着重新量的话，窗口会跟着一起更新，不用再排一次。
  if (viewportRaf || measureRaf) return
  viewportRaf = requestAnimationFrame(() => {
    viewportRaf = 0
    updateViewport()
  })
}

let ro: ResizeObserver | null = null
let mo: MutationObserver | null = null
let boundEl: HTMLElement | null = null
const observedChildren = new Set<Element>()

// 滚动容器自己的大小不变、只是里面的内容长高（图片加载、展开卡片）时，容器的 ResizeObserver 不会响：
// 把它的每个直接子节点也一起观察。
function syncObservedChildren() {
  if (!ro || !boundEl) return
  const current = new Set<Element>(Array.from(boundEl.children))
  for (const child of Array.from(observedChildren)) {
    if (!current.has(child)) {
      ro.unobserve(child)
      observedChildren.delete(child)
    }
  }
  for (const child of current) {
    if (!observedChildren.has(child)) {
      ro.observe(child)
      observedChildren.add(child)
    }
  }
}

function detach() {
  if (boundEl) boundEl.removeEventListener('scroll', scheduleViewport)
  ro?.disconnect()
  mo?.disconnect()
  ro = null
  mo = null
  boundEl = null
  observedChildren.clear()
}

function attach(el: HTMLElement | null) {
  detach()
  if (!el) {
    clearTicks()
    return
  }
  boundEl = el
  el.addEventListener('scroll', scheduleViewport, { passive: true })
  ro = new ResizeObserver(scheduleMeasure)
  ro.observe(el)
  if (trackEl.value) ro.observe(trackEl.value)
  syncObservedChildren()
  mo = new MutationObserver(() => {
    syncObservedChildren()
    scheduleMeasure()
  })
  mo.observe(el, { childList: true, subtree: true, attributes: true, attributeFilter: RAIL_WATCH_ATTRS })
  scheduleMeasure()
}

onMounted(() => attach(props.scrollEl))
watch(() => props.scrollEl, el => attach(el))
onBeforeUnmount(() => {
  detach()
  if (measureRaf) cancelAnimationFrame(measureRaf)
  if (viewportRaf) cancelAnimationFrame(viewportRaf)
  measureRaf = 0
  viewportRaf = 0
  resetFollowHold()
})

// ---------- 轨自己滚动时，上下沿渐隐，提示还有内容 ----------
// 上沿 / 下沿要不要渐隐只取决于两个布尔量，先单独算出来：railScroll 在会话列表滚动时几乎每帧都变，
// 这两个布尔量却大多数帧不变，下面的样式对象就不会重算，渲染时也不会白白重设一遍 mask-image（#158）。
const fadeUp = computed(() => overflowing.value && railScroll.value > 1)
const fadeDown = computed(() => overflowing.value && railScroll.value + trackHeight.value < layout.value.height - 1)
const fadeStyle = computed(() => {
  const up = fadeUp.value
  const down = fadeDown.value
  if (!up && !down) return undefined
  const g = `linear-gradient(to bottom, ${up ? 'transparent' : '#000'} 0, #000 ${RAIL_PITCH_PX}px, #000 calc(100% - ${RAIL_PITCH_PX}px), ${down ? 'transparent' : '#000'} 100%)`
  return { maskImage: g, WebkitMaskImage: g }
})

// ---------- 悬停提示 ----------
const hoverTick = computed(() => (hoverId.value ? layout.value.ticks.find(t => t.item.id === hoverId.value) ?? null : null))
const hoverTime = computed(() => formatRailTime(hoverTick.value?.item.at))
// 提示框在滚动容器外面（容器 overflow 会把它削掉），位置 = 刻度在可见窗口里的位置（减掉轨自己的滚动）；
// 垂直居中，但别伸出轨的上下沿（列表容器 overflow-hidden 会把它削掉）。
const tipTop = computed(() => {
  const t = hoverTick.value
  if (!t) return 0
  const half = 36
  return Math.min(Math.max(t.y - railScroll.value, half), Math.max(half, trackHeight.value - half))
})

</script>

<template>
  <nav
    class="pointer-events-none absolute right-1.5 top-3 z-20 hidden w-8 select-none flex-col items-end transition-[bottom] duration-150 md:flex"
    :class="[show ? '' : 'invisible', !dock ? 'bottom-14' : hasDraftImages ? 'bottom-44' : 'bottom-28']"
    :aria-hidden="!show"
    aria-label="会话时间线"
    data-testid="pv-rail"
    :data-visible="show ? '1' : '0'"
  >
    <button
      v-if="hasEarlier"
      type="button"
      class="pointer-events-auto mb-1 shrink-0 cursor-pointer rounded px-1 text-[10px] font-mono leading-4 text-muted-foreground/70 hover:bg-muted hover:text-foreground disabled:cursor-default"
      :disabled="loadingEarlier"
      title="还有更早的对话没载入，点击载入"
      data-testid="pv-rail-earlier"
      @click="emit('load-earlier')"
    >
      <span :class="loadingEarlier ? 'animate-pulse' : ''">更早</span>
    </button>

    <div ref="trackEl" class="relative min-h-0 w-full flex-1" data-testid="pv-rail-track">
      <!--
        轨自己的滚动容器：放得下时整条轨对鼠标透明（点击与滚轮都穿到下面的会话列表），只有刻度本身可点；
        放不下时整条轨接管滚轮（overscroll-contain：滚到头不带动页面），不出滚动条。
      -->
      <div
        ref="scrollerEl"
        class="no-scrollbar absolute inset-0 overflow-y-auto overscroll-contain"
        :class="overflowing ? 'pointer-events-auto' : ''"
        :style="fadeStyle"
        data-testid="pv-rail-scroller"
        :data-overflowing="overflowing ? '1' : '0'"
        @scroll.passive="onRailScroll"
        @wheel.passive="onRailUserScroll"
        @touchstart.passive="onRailUserScroll"
        @touchmove.passive="onRailUserScroll"
        @pointerenter="onRailPointerEnter"
        @pointerleave="onRailPointerLeave"
      >
        <div class="relative w-full" :style="{ height: `${layout.height}px` }" data-testid="pv-rail-content">
          <!-- 当前可视范围 -->
          <div
            class="pointer-events-none absolute inset-x-0 rounded-sm bg-foreground/[0.07] ring-1 ring-foreground/10"
            :style="{ top: `${box.top}px`, height: `${box.height}px` }"
            data-testid="pv-rail-viewport"
          />

          <!-- 刻度：独立组件（#158），只在排布换了新数组时才重渲染；悬停 / 点击事件由这里接 -->
          <PreviewRailTicks :ticks="layout.ticks" @jump="emit('jump', $event)" @hover="hoverId = $event" />
        </div>
      </div>

      <!-- 悬停提示：类型 · 状态 / 标题 / 时间（放在滚动容器外面，不被 overflow 削掉） -->
      <div
        v-if="hoverTick"
        class="pointer-events-none absolute right-full z-30 mr-2 w-max max-w-[260px] -translate-y-1/2 rounded-md border border-border bg-popover px-2.5 py-1.5 text-popover-foreground shadow-md"
        :style="{ top: `${tipTop}px` }"
        role="tooltip"
        data-testid="pv-rail-tip"
      >
        <div class="text-[11px] font-medium leading-4">{{ railLabel(hoverTick.item.kind, hoverTick.item.state, hoverTick.item.parts) }}</div>
        <div v-if="hoverTick.item.title" class="mt-0.5 line-clamp-2 break-words text-[11px] leading-4 text-muted-foreground">
          {{ hoverTick.item.title }}
        </div>
        <div v-if="hoverTime" class="mt-0.5 flex items-center gap-1.5 text-[10px] leading-4 text-muted-foreground/80">
          <span class="font-mono">{{ hoverTime }}</span>
        </div>
      </div>
    </div>
  </nav>
</template>
