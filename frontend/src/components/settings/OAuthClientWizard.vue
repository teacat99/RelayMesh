<script setup lang="ts">
import { computed, ref } from 'vue'
import { Copy } from 'lucide-vue-next'
import { toast } from 'vue-sonner'
import type { OAuthPolicy } from '../../api/client'
import { copyText } from '../../lib/terminal/clipboard'
import {
  CLIENT_PRESETS,
  PRESET_TOGGLE_LABELS,
  PRESET_TOGGLE_NOTES,
  presetById,
  presetMissing,
  type ClientPresetId,
} from '../../lib/oauthSetup'
import { Button } from '../ui/button'

// 接入向导：ChatGPT / Claude（网页）/ Claude Code 三个标签，每个给出照着做的步骤，
// 「按此客户端设置」只通知父组件把该客户端需要的开关勾进表单草稿——不保存、不碰总开关与对外地址。
const props = defineProps<{
  connectorUrl: string
  /** 表单草稿（还没保存的也算），用来显示每一项勾上没有 */
  policy: OAuthPolicy
  /** 服务端此刻总开关是否真的开着（决定要不要提示「还没开」） */
  switchOn: boolean
}>()
const emit = defineEmits<{ (e: 'apply', id: ClientPresetId): void }>()

const active = ref<ClientPresetId>('chatgpt')
const preset = computed(() => presetById(active.value))
const steps = computed(() => preset.value.steps(props.connectorUrl))
const missing = computed(() => presetMissing(props.policy, active.value))
const applied = ref<{ id: ClientPresetId; labels: string[] } | null>(null)

function apply() {
  applied.value = { id: active.value, labels: missing.value.map(t => PRESET_TOGGLE_LABELS[t]) }
  emit('apply', active.value)
}

async function copyCode(code: string) {
  if (await copyText(code)) toast.success('已复制')
  else toast.error('复制失败，请手动选中后复制')
}
</script>

<template>
  <section class="rounded-xs border border-border/60 p-3 space-y-3" data-testid="oauth-wizard">
    <div>
      <h4 class="text-xs font-bold">接入向导：选你要连的客户端</h4>
      <p class="text-[10px] text-muted-foreground mt-0.5 leading-relaxed">照着下面的步骤做；每一步需要粘贴的地址或命令都带着复制按钮。</p>
    </div>

    <div role="tablist" aria-label="选择客户端" class="flex flex-wrap gap-1">
      <button
        v-for="p in CLIENT_PRESETS"
        :key="p.id"
        type="button"
        role="tab"
        :aria-selected="active === p.id"
        :data-testid="`oauth-tab-${p.id}`"
        class="rounded-xs border px-2.5 py-1 text-xs transition-colors"
        :class="active === p.id ? 'border-border bg-card font-medium shadow-sm' : 'border-transparent text-muted-foreground hover:text-foreground'"
        @click="active = p.id"
      >
        {{ p.label }}
      </button>
    </div>

    <div role="tabpanel" :data-testid="`oauth-panel-${active}`" class="space-y-3">
      <p class="text-xs text-muted-foreground leading-relaxed">{{ preset.blurb }}</p>

      <ol class="space-y-2.5">
        <li v-for="(s, i) in steps" :key="`${active}-${i}`" class="flex gap-2 text-xs">
          <span class="mt-px flex size-4 shrink-0 items-center justify-center rounded-full bg-primary/10 text-[10px] font-medium text-primary">{{ i + 1 }}</span>
          <div class="min-w-0 flex-1 space-y-1">
            <div class="leading-relaxed">{{ s.text }}</div>
            <!-- 窄屏上命令很长：代码块占满一行，复制按钮挪到它下面靠右，免得命令被挤成四五行。 -->
            <div v-if="s.code" class="flex flex-col gap-1.5 sm:flex-row sm:items-start">
              <code class="min-w-0 break-all rounded-xs bg-muted px-2 py-1 font-mono text-[11px] sm:flex-1" data-testid="oauth-step-code">{{ s.code }}</code>
              <Button size="sm" variant="outline" class="h-7 shrink-0 self-end text-[10px] sm:self-auto" data-testid="oauth-step-copy" @click="copyCode(s.code)">
                <Copy class="mr-1 size-3" />复制
              </Button>
            </div>
          </div>
        </li>
      </ol>

      <div class="space-y-1.5 rounded-xs border border-dashed border-border p-2.5" data-testid="oauth-preset">
        <div class="flex flex-wrap items-center gap-2">
          <Button size="sm" variant="outline" class="h-7 text-xs" data-testid="oauth-preset-apply" @click="apply">按此客户端设置</Button>
          <span class="text-[10px] text-muted-foreground">只在下面的表单里勾选，不会替你保存，也不会替你打开总开关。</span>
        </div>
        <ul class="space-y-0.5 text-[10px] text-muted-foreground">
          <li v-for="t in preset.toggles" :key="t" :data-testid="`oauth-preset-item-${t}`" :data-on="policy[t] ? 'true' : 'false'">
            <span :class="policy[t] ? 'text-emerald-600 dark:text-emerald-400' : ''">{{ policy[t] ? '✓' : '○' }}</span>
            {{ PRESET_TOGGLE_LABELS[t] }}：{{ PRESET_TOGGLE_NOTES[t] }}
          </li>
        </ul>
        <p v-if="applied && applied.id === active" class="text-[10px] leading-relaxed text-emerald-600 dark:text-emerald-400" data-testid="oauth-preset-applied">
          <template v-if="applied.labels.length">已在下面的表单里勾选：{{ applied.labels.join('、') }}。这些改动还没有保存——往下滑，点「保存策略」才会生效。</template>
          <template v-else>这个客户端需要的选项已经都勾上了。</template>
        </p>
        <p v-if="!switchOn" class="text-[10px] leading-relaxed text-amber-600 dark:text-amber-400" data-testid="oauth-preset-switch-off">
          总开关现在还没有开启：要在下面勾选「启用 OAuth 授权服务器」并保存，客户端才连得上。
        </p>
      </div>

      <p class="text-[10px] leading-relaxed text-muted-foreground">{{ preset.note }}</p>
    </div>
  </section>
</template>
