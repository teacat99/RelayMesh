<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { AlertTriangle, CheckCircle2, Info, Loader2, XCircle } from 'lucide-vue-next'
import type { OAuthEffective } from '../../api/client'
import {
  checkStatusTone,
  runOAuthSelfCheck,
  selfCheckSummary,
  toneClasses,
  type CheckStatus,
  type FetchFn,
  type SelfCheckItem,
} from '../../lib/oauthSetup'
import { Button } from '../ui/button'

// 一键自检：在浏览器里请求「当前打开本页的地址」上的两个元数据端点与未带令牌的 /mcp（逻辑见 lib/oauthSetup.ts）。
const props = defineProps<{
  effective: OAuthEffective
  /** 下面的策略表单有没有未保存的改动：自检看的是服务端已保存的配置，要提醒一下 */
  dirty: boolean
}>()

const running = ref(false)
const items = ref<SelfCheckItem[] | null>(null)
const summary = computed(() => (items.value ? selfCheckSummary(items.value) : null))

// 开关或对外地址变了，旧结果就不再可信：清掉，免得绿色停留在过期的配置上。
watch([() => props.effective.enabled, () => props.effective.env_off, () => props.effective.public_base], () => {
  items.value = null
})

const fetchFn: FetchFn = (url, init) => fetch(url, init)

async function run() {
  running.value = true
  try {
    items.value = await runOAuthSelfCheck(fetchFn, window.location.origin, props.effective)
  } finally {
    running.value = false
  }
}

function iconFor(s: CheckStatus) {
  if (s === 'ok') return CheckCircle2
  if (s === 'warn') return AlertTriangle
  if (s === 'fail') return XCircle
  return Info
}
</script>

<template>
  <div class="space-y-2" data-testid="oauth-selfcheck">
    <div class="flex flex-wrap items-center gap-2">
      <Button size="sm" variant="outline" class="h-7 text-xs" :disabled="running" data-testid="oauth-selfcheck-run" @click="run">
        <Loader2 v-if="running" class="size-3 animate-spin mr-1" />
        {{ items ? '重新自检' : '一键自检' }}
      </Button>
      <span class="text-[10px] text-muted-foreground">检查两个元数据端点，以及不带令牌访问 /mcp 时是否返回 401。</span>
    </div>
    <p v-if="dirty" class="text-[10px] text-amber-600 dark:text-amber-400" data-testid="oauth-selfcheck-dirty">
      下面的表单还有没保存的改动；自检看的是已经保存的配置，保存后再自检。
    </p>
    <div v-if="items && summary" class="space-y-1.5">
      <div class="rounded-xs border px-2 py-1 text-xs font-medium" :class="toneClasses(summary.tone)" data-testid="oauth-check-summary" :data-tone="summary.tone">
        {{ summary.text }}
      </div>
      <ul class="space-y-1.5">
        <li
          v-for="it in items"
          :key="it.id"
          class="rounded-xs border px-2 py-1.5 text-xs"
          :class="toneClasses(checkStatusTone(it.status))"
          data-testid="oauth-check-item"
          :data-id="it.id"
          :data-status="it.status"
        >
          <div class="flex items-start gap-1.5">
            <component :is="iconFor(it.status)" class="size-3.5 mt-0.5 shrink-0" />
            <div class="min-w-0 break-words">
              <div class="font-medium">{{ it.label }}</div>
              <div class="opacity-90">{{ it.detail }}</div>
              <div v-if="it.hint" class="mt-0.5 text-foreground/80">{{ it.hint }}</div>
            </div>
          </div>
        </li>
      </ul>
    </div>
  </div>
</template>
