<script setup lang="ts">
// 轻量行的正文门（#169 第二期「索引优先 + 正文按需」，只给预览视图用）。
//
// 预览视图取会话表时，较早的行只带首行预览（session.body_omitted）；这样的行页面不能当正文渲染（只有一行字，像被截断的汇报）。
// 套在卡片正文外面：行是完整的就直接渲染默认插槽；是轻量行就渲染一块骨架，同时向 store 排队取正文——
// 取回之后行被换成完整行，骨架自然换成正文。取失败（离线 / 服务端报错）时骨架一直在，隔一会儿自动再试，不会永远卡住。
// 正常使用路径（滚动加载更早的轮次、跳转）列表会先把正文取回再显示卡片，这道门只是兜底（直接挂出轻量行时自助补）。
import { onBeforeUnmount, onMounted, watch } from 'vue'
import type { FeedbackSession } from '../../../api/types'
import { useSessionStore } from '../../../stores/session'

const props = withDefaults(defineProps<{
  session: Pick<FeedbackSession, 'session_id' | 'workflow_id' | 'body_omitted'>
  /** 骨架画几行。 */
  lines?: number
}>(), { lines: 3 })

// 取失败后的自动重试间隔（store 里同一行还有 10 秒退避，这里取略大一点）
const RETRY_MS = 12 * 1000

const sessionStore = useSessionStore()
let timer: ReturnType<typeof setInterval> | null = null

function request() {
  if (!props.session.body_omitted) return
  sessionStore.queueSessionBody(props.session.workflow_id || props.session.session_id, props.session.session_id)
}

function sync() {
  if (props.session.body_omitted) {
    request()
    if (!timer) timer = setInterval(request, RETRY_MS)
  } else if (timer) {
    clearInterval(timer)
    timer = null
  }
}

onMounted(sync)
watch(() => props.session.body_omitted, sync)
onBeforeUnmount(() => {
  if (timer) clearInterval(timer)
  timer = null
})

// 各行宽度错开一点，像一段文字；最后一行短一些
function widthOf(i: number): string {
  if (i === props.lines) return '45%'
  return ['92%', '78%', '86%', '70%'][(i - 1) % 4]
}
</script>

<template>
  <div
    v-if="session.body_omitted"
    class="space-y-2 py-1"
    data-testid="body-skeleton"
    :data-session-id="session.session_id"
    role="status"
    aria-busy="true"
    aria-label="正在加载正文"
  >
    <div
      v-for="i in lines"
      :key="i"
      class="h-3 animate-pulse rounded-xs bg-muted"
      :style="{ width: widthOf(i) }"
    />
  </div>
  <slot v-else />
</template>
