<script setup lang="ts">
// 页面 / 面板级的「加载中 / 空 / 出错」统一组件（#156 批 4）。以前每个页面各写一份：字号、颜色、有没有图标、能不能重试都不一样。
// 约定：
//   · kind：loading（转圈 + 一句话）/ empty（图标 + 标题 + 说明 + 可选动作）/ error（红色图标 + 标题 + 原因 + 可选「重试」）。
//   · size：md = 占一整块区域（页面主体、整个面板）；sm = 嵌在列表 / 小节里的紧凑版（不画图标方块，只有文字，loading 是一行转圈）。
//   · bordered：虚线框，列表里「还没有 X」的占位用；as：根标签，放进 <ul> 时传 'li'。
//   · 说明文字写在 hint 属性或默认插槽里（要带 <code> 之类时用插槽）；按钮放 actions 插槽。
//   · data-testid、class 等属性通过属性落在根节点上，原来各处的 testid 原样保留。
// 无障碍：loading 是 role=status（读屏软件会播报），error 是 role=alert；转圈在「减少动画」偏好下不转。
// 行内 / 按钮内的「保存中…」这类局部提示不用它，仍各自写。
import { computed, type Component } from 'vue'
import { AlertTriangle, Inbox, Loader2 } from 'lucide-vue-next'
import { cn } from '@/lib/utils'

const props = withDefaults(
  defineProps<{
    kind: 'loading' | 'empty' | 'error'
    title?: string
    hint?: string
    icon?: Component
    size?: 'sm' | 'md'
    bordered?: boolean
    as?: string
  }>(),
  { title: '', hint: '', icon: undefined, size: 'md', bordered: false, as: 'div' },
)

const titleText = computed(() => props.title || (props.kind === 'loading' ? '加载中…' : props.kind === 'error' ? '出错了' : '这里还是空的'))
const StateIcon = computed<Component>(() => props.icon ?? (props.kind === 'error' ? AlertTriangle : Inbox))
const inlineLoading = computed(() => props.kind === 'loading' && props.size === 'sm')

const rootClass = computed(() =>
  cn(
    'flex items-center justify-center text-center',
    inlineLoading.value ? 'flex-row gap-2 px-3 py-4' : 'flex-col',
    !inlineLoading.value && (props.size === 'md' ? 'gap-3 px-6 py-12' : 'gap-1.5 px-3 py-4'),
    props.bordered && 'rounded-md border border-dashed border-border/70',
    props.kind === 'error' ? 'text-destructive' : 'text-muted-foreground',
  ),
)
</script>

<template>
  <component
    :is="as"
    :class="rootClass"
    :role="kind === 'error' ? 'alert' : kind === 'loading' ? 'status' : undefined"
    :aria-busy="kind === 'loading' ? 'true' : undefined"
    :data-state="kind"
  >
    <template v-if="size === 'md'">
      <Loader2 v-if="kind === 'loading'" class="size-5 animate-spin motion-reduce:animate-none" aria-hidden="true" />
      <div
        v-else
        class="flex size-11 items-center justify-center rounded-sm border"
        :class="kind === 'error' ? 'border-destructive/40 bg-destructive/10' : 'border-border/60 bg-muted/40'"
      >
        <component :is="StateIcon" class="size-5 stroke-[1.4]" aria-hidden="true" />
      </div>
    </template>
    <Loader2 v-else-if="kind === 'loading'" class="size-3.5 shrink-0 animate-spin motion-reduce:animate-none" aria-hidden="true" />
    <div class="min-w-0 max-w-md space-y-1">
      <p
        :class="[
          size === 'md' ? 'text-sm font-semibold' : 'text-xs',
          kind === 'error' ? 'text-destructive' : size === 'md' && kind === 'empty' ? 'text-foreground' : '',
        ]"
      >
        {{ titleText }}
      </p>
      <!-- 说明用 div：插槽里可以放多段（<p>）与 <code> -->
      <div v-if="hint || $slots.default" class="space-y-1 text-xs leading-relaxed">
        <slot>{{ hint }}</slot>
      </div>
    </div>
    <!-- 按钮不继承状态色：出错态根节点是红字，描边 / 幽灵按钮没有自己的字色，会跟着变红 -->
    <div v-if="$slots.actions" class="mt-1 flex flex-wrap items-center justify-center gap-2 text-foreground">
      <slot name="actions" />
    </div>
  </component>
</template>
