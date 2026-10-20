<script setup lang="ts">
// 侧栏条目的「右滑露出操作条」容器：只响应触摸 / 笔（鼠标用右键菜单），手势判定交给 lib/terminal/swipe.ts。
// 内容层盖在操作条上，向右滑动时把操作条从左侧露出来；展开状态由父级通过 open 控制，保证同一时间只展开一行。
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import {
  SWIPE_DEFAULTS,
  SWIPE_IDLE,
  swipeCancel,
  swipeEnd,
  swipeMove,
  swipeStart,
  swipeTranslate,
  swipeVelocity,
  type SwipeConfig,
  type SwipeState,
} from '../../lib/terminal/swipe'

const props = defineProps<{
  /** 操作条是否展开。 */
  open: boolean
  /** 操作条宽度（px）；0 表示这一行没有可用的操作，不响应滑动。 */
  width: number
  /** 禁用滑动（例如正在重命名）。 */
  disabled?: boolean
}>()
const emit = defineEmits<{ (e: 'update:open', value: boolean): void }>()

const state = ref<SwipeState>(SWIPE_IDLE)
const cfg = computed<SwipeConfig>(() => ({ width: props.width, ...SWIPE_DEFAULTS }))
const dragging = computed(() => state.value.phase === 'dragging')
const offset = computed(() => swipeTranslate(state.value, props.open, cfg.value))

// 静止在收起状态时，内容层保持透明、操作条隐藏，行的外观与没有滑动功能时完全一致；
// 只有拖动、展开以及收起动画期间才显示操作条并让内容层变成实色（否则会透出下面的操作条）。
const revealed = ref(false)
let hideTimer: ReturnType<typeof setTimeout> | null = null
watch(
  [offset, dragging],
  () => {
    if (dragging.value || offset.value > 0) {
      if (hideTimer) clearTimeout(hideTimer)
      hideTimer = null
      revealed.value = true
    } else if (revealed.value && !hideTimer) {
      hideTimer = setTimeout(() => {
        revealed.value = false
        hideTimer = null
      }, 260)
    }
  },
)

let pointerId = -1
let lastX = 0
let lastT = 0
let velocity = 0
let swallowClick = false
let swallowTimer: ReturnType<typeof setTimeout> | null = null

function armSwallow() {
  swallowClick = true
  if (swallowTimer) clearTimeout(swallowTimer)
  // click 紧跟在 pointerup 之后触发；留一点余量，过后自动解除，避免吞掉下一次真正的点击。
  swallowTimer = setTimeout(() => {
    swallowClick = false
    swallowTimer = null
  }, 80)
}

onBeforeUnmount(() => {
  if (swallowTimer) clearTimeout(swallowTimer)
  if (hideTimer) clearTimeout(hideTimer)
})

function onPointerDown(e: PointerEvent) {
  if (props.disabled || props.width <= 0 || e.pointerType === 'mouse' || !e.isPrimary) return
  pointerId = e.pointerId
  state.value = swipeStart(e.clientX, e.clientY, props.open, cfg.value)
  lastX = e.clientX
  lastT = e.timeStamp
  velocity = 0
}

function onPointerMove(e: PointerEvent) {
  if (e.pointerId !== pointerId) return
  const was = state.value.phase
  state.value = swipeMove(state.value, e.clientX, e.clientY, cfg.value)
  if (state.value.phase !== 'dragging') return
  // 判定为横向滑动之后再捕获指针，纵向滚动、点按不受影响。指针若已经失效（例如被系统取消）捕获会抛错，忽略即可。
  if (was !== 'dragging') {
    try {
      ;(e.currentTarget as HTMLElement).setPointerCapture?.(e.pointerId)
    } catch {
      /* 指针已失效 */
    }
  }
  velocity = swipeVelocity(lastX, lastT, e.clientX, e.timeStamp)
  lastX = e.clientX
  lastT = e.timeStamp
}

function finish(e: PointerEvent, cancelled: boolean) {
  if (e.pointerId !== pointerId) return
  pointerId = -1
  const result = cancelled ? swipeCancel(state.value) : swipeEnd(state.value, cfg.value, velocity)
  state.value = SWIPE_IDLE
  if (result.moved) armSwallow()
  if (result.open !== props.open) emit('update:open', result.open)
}

/** 拖动之后抬手触发的 click 要吞掉；操作条展开时点内容只是把它收起来，不进入会话。 */
function onClickCapture(e: MouseEvent) {
  if (swallowClick) {
    e.preventDefault()
    e.stopPropagation()
    return
  }
  if (props.open) {
    e.preventDefault()
    e.stopPropagation()
    emit('update:open', false)
  }
}
</script>

<template>
  <div class="group/swipe relative overflow-hidden rounded-sm">
    <!-- 操作条：在内容层下面，向右滑动时露出。收起时不可见、不可聚焦、不进入读屏树。 -->
    <div
      v-if="width > 0"
      class="absolute inset-y-0 left-0 flex items-stretch bg-muted"
      :class="revealed ? '' : 'invisible'"
      :style="{ width: `${width}px` }"
      :aria-hidden="revealed ? undefined : 'true'"
    >
      <slot name="actions" />
    </div>
    <div
      class="relative touch-pan-y"
      :class="[revealed ? 'bg-card' : '', dragging ? 'will-change-transform' : 'transition-transform duration-200 ease-out motion-reduce:transition-none']"
      :style="{ transform: offset ? `translateX(${offset}px)` : undefined }"
      @pointerdown="onPointerDown"
      @pointermove="onPointerMove"
      @pointerup="finish($event, false)"
      @pointercancel="finish($event, true)"
      @click.capture="onClickCapture"
    >
      <slot />
    </div>
  </div>
</template>
