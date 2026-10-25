<script setup lang="ts">
import { Loader2, Plus } from 'lucide-vue-next'
import { toneClasses } from '../../lib/oauthSetup'
import { WINDOW_SCOPE_NOTE, type WindowView } from '../../lib/oauthWindow'
import { Button } from '../ui/button'

// 接入窗口卡片：和 ExecMesh「接入主机」里的发现窗口同一套做法——平时关着，要添加新的客户端时开一段时间。
// 倒计时与可用按钮都由父组件算好（lib/oauthWindow.ts 的 windowView，有单元测试），这里只负责展示与发出动作。
defineProps<{ view: WindowView; busy: boolean }>()
const emit = defineEmits<{ (e: 'open'): void; (e: 'extend'): void; (e: 'close'): void }>()
</script>

<template>
  <section class="space-y-2 rounded-xs border border-border/60 p-3" data-testid="oauth-window" :data-kind="view.kind">
    <div class="flex flex-wrap items-start justify-between gap-2">
      <div class="min-w-0 flex-1 space-y-1.5">
        <h4 class="text-xs font-bold">接入窗口</h4>
        <span
          class="inline-flex max-w-full items-center rounded-xs border px-2 py-0.5 text-xs font-medium tabular-nums"
          :class="toneClasses(view.tone)"
          data-testid="oauth-window-state"
          :data-kind="view.kind"
          :data-ending="view.ending ? 'true' : 'false'"
        >
          {{ view.title }}
        </span>
        <p class="text-[10px] leading-relaxed text-muted-foreground" data-testid="oauth-window-detail">{{ view.detail }}</p>
      </div>

      <div v-if="view.canOpen || view.kind === 'open'" class="flex shrink-0 flex-wrap items-center gap-2">
        <!-- 续期放在「立即关闭」左边：每次在剩余时间上累加，窗口最长保持到上限，不是重新计时。 -->
        <Button
          v-if="view.kind === 'open'"
          variant="outline"
          size="sm"
          class="h-7 text-xs"
          :disabled="busy || !view.canExtend"
          :title="view.canExtend ? `在剩余时间上再加 ${view.minutes} 分钟` : '接入窗口已经达到时长上限，不能再续期'"
          data-testid="oauth-window-extend"
          @click="emit('extend')"
        >
          <Loader2 v-if="busy" class="size-3.5 animate-spin" />
          <Plus v-else class="size-3.5" />
          续期 {{ view.minutes }} 分钟
        </Button>
        <Button v-if="view.kind === 'open'" variant="outline" size="sm" class="h-7 text-xs" :disabled="busy" data-testid="oauth-window-close" @click="emit('close')">
          <Loader2 v-if="busy" class="size-3.5 animate-spin" />
          立即关闭
        </Button>
        <Button v-else size="sm" class="h-7 text-xs" :disabled="busy" data-testid="oauth-window-open" @click="emit('open')">
          <Loader2 v-if="busy" class="size-3.5 animate-spin" />
          开启 {{ view.minutes }} 分钟
        </Button>
      </div>
    </div>
    <p class="text-[10px] leading-relaxed text-muted-foreground" data-testid="oauth-window-scope">{{ WINDOW_SCOPE_NOTE }}</p>
  </section>
</template>
