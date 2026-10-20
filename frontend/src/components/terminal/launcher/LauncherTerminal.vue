<script setup lang="ts">
import { onBeforeUnmount, ref } from 'vue'
import TerminalPane from '../TerminalPane.vue'
import TerminalKeybar from '../TerminalKeybar.vue'
import { useTerminalFont } from '../../../composables/useTerminalFont'
import type { ConnectionState, StateInfo } from '../../../lib/terminal/connection'
import type { CursorKey } from '../../../lib/terminal/keys'
import { readyToType } from '../../../lib/terminal/launcher'

/**
 * 启动器弹窗里的终端：一个连着会话的终端窗格；会话是新开的时，等 shell 的提示符出来后把预设里的那一行命令敲进去。
 *
 * 为什么不是在新建会话时就让主机运行命令：那需要改会话协议，而且得所有主机的 Agent 都升级才能用；
 * 现在这样只在浏览器里多做一步「像人一样敲一行字」，对任何版本的 Agent 都一样，敲了什么也看得见。
 * 「什么时候敲」见 lib/terminal/launcher.ts 的 readyToType：连上之后输出安静了一小会儿（提示符已经出来）就敲，
 * 太久没有输出也敲；每个会话只敲一次，连接中途断了重连也不会重复敲，也不会在断着的时候敲丢。
 */
const props = defineProps<{
  code: string
  sid: string
  label: string
  /** 会话就绪后要敲的那一行（不含回车）；空串表示什么也不敲（连回已经在运行的会话，或预设没有命令）。 */
  line: string
  /** 弹窗开着、并且此刻显示的是终端。 */
  active: boolean
  /** 触屏 / 窄屏：显示辅助键栏。 */
  keybar: boolean
  /**
   * 大于 0：先输入文字，隔这么久再单独敲回车。唤醒（把提示词输入到 Cursor CLI 这类 TUI）要这样——
   * 文字与回车挤在同一次输入里，TUI 会把整串当成「粘贴」，回车成了换行而不是发送。
   * 普通 shell 的命令行不需要，缺省 0 = 文字与回车一次输入。
   */
  submitDelayMs?: number
}>()

const emit = defineEmits<{
  (e: 'state', p: { state: ConnectionState; info: StateInfo }): void
  (e: 'close'): void
  /** 命令已经敲进去了。 */
  (e: 'typed'): void
}>()

const termFont = useTerminalFont()
const ctrl = ref(false)
const alt = ref(false)

type PaneApi = {
  focus: () => void
  sendText: (t: string) => void
  sendCursor: (k: CursorKey) => void
  paste: () => Promise<void>
}
const pane = ref<PaneApi | null>(null)

let liveAt: number | null = null
let lastOutputAt: number | null = null
let typed = false
let timer: ReturnType<typeof setInterval> | null = null
let submitTimer: ReturnType<typeof setTimeout> | null = null
const POLL_MS = 80

function stop() {
  if (timer) clearInterval(timer)
  timer = null
}

function tick() {
  if (typed) return stop()
  if (!readyToType({ liveAt, lastOutputAt, now: performance.now() })) return
  typed = true
  stop()
  const delay = props.submitDelayMs ?? 0
  if (delay > 0) {
    // 先把文字送进去，停一小会儿再单独敲回车（见 submitDelayMs）。
    pane.value?.sendText(props.line)
    submitTimer = setTimeout(() => {
      submitTimer = null
      pane.value?.sendText('\r')
      emit('typed')
    }, delay)
    return
  }
  pane.value?.sendText(props.line + '\r')
  emit('typed')
}

function onState(p: { state: ConnectionState; info: StateInfo }) {
  emit('state', p)
  // 只在「真正连着一个还活着的会话」时才计时；断线重连时重新来过，免得敲在一个没连上的连接里丢掉。
  if (p.state === 'live' && !p.info.over) {
    if (liveAt === null) liveAt = performance.now()
    if (!typed && props.line && !timer) timer = setInterval(tick, POLL_MS)
  } else {
    liveAt = null
    stop()
  }
}

const onOutput = () => (lastOutputAt = performance.now())

function toggleModifier(m: 'ctrl' | 'alt') {
  if (m === 'ctrl') ctrl.value = !ctrl.value
  else alt.value = !alt.value
}

defineExpose({ focus: () => pane.value?.focus() })
onBeforeUnmount(() => {
  stop()
  if (submitTimer) clearTimeout(submitTimer)
})
</script>

<template>
  <div class="flex min-h-0 flex-1 flex-col" data-testid="launcher-terminal">
    <TerminalPane
      ref="pane"
      v-model:ctrl="ctrl"
      v-model:alt="alt"
      class="min-h-0 flex-1"
      :code="code"
      :sid="sid"
      :label="label"
      :active="active"
      :autofocus="true"
      :font-size="termFont.fontSize.value"
      @state="onState"
      @output="onOutput"
      @close="emit('close')"
    />
    <TerminalKeybar
      v-if="keybar"
      :ctrl="ctrl"
      :alt="alt"
      @text="(t: string) => pane?.sendText(t)"
      @cursor="(k: CursorKey) => pane?.sendCursor(k)"
      @toggle="toggleModifier"
      @paste="() => void pane?.paste()"
    />
  </div>
</template>
