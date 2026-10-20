<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { toast } from 'vue-sonner'
import { useTerminalStore } from '@/stores/terminal'
import { UnlockCancelled, toApiError } from '@/api/terminal'
import { FIXED_COLS_DEFAULT, FIXED_COLS_PRESETS, parseFixedCols } from '@/lib/terminal/fixedWidth'
import { Switch } from '../ui/switch'
import { Input } from '../ui/input'

// 固定终端宽度（宽度保持）：和上面的字号、粘贴确认不同，这一项保存在服务端，所有设备共用——
// 它的目的就是让电脑、手机、平板看到同一个宽度的排版。只改浏览器上报的终端尺寸，不涉及主机上的数据，登录即可修改。
const terminal = useTerminalStore()
const prefs = computed(() => terminal.prefs)
const limits = computed(() => ({
  min: prefs.value?.limits.fixed_cols_min ?? 40,
  max: prefs.value?.limits.fixed_cols_max ?? 400,
}))
const cols = computed(() => prefs.value?.fixed_cols ?? 0)
const enabled = computed(() => cols.value > 0)

const draft = ref(cols.value > 0 ? String(cols.value) : '')
const error = ref('')
const saving = ref(false)

// 别的设备改了值（状态轮询同步过来）：输入框跟随。
watch(cols, v => {
  if (v > 0) draft.value = String(v)
  error.value = ''
})

async function save(next: number) {
  saving.value = true
  error.value = ''
  try {
    await terminal.saveFixedCols(next)
  } catch (e) {
    if (e instanceof UnlockCancelled) return
    error.value = toApiError(e).message
    toast.error(`保存失败：${error.value}`, { duration: 4000 })
  } finally {
    saving.value = false
  }
}

async function onToggle(on: boolean) {
  if (!on) return save(0)
  const parsed = parseFixedCols(draft.value, limits.value)
  const next = parsed.ok ? parsed.value : FIXED_COLS_DEFAULT
  draft.value = String(next)
  return save(next)
}

async function applyDraft() {
  if (!enabled.value) return
  const parsed = parseFixedCols(draft.value, limits.value)
  if (!parsed.ok) {
    error.value = parsed.message
    return
  }
  error.value = ''
  if (parsed.value !== cols.value) await save(parsed.value)
}

async function pick(n: number) {
  // 点预设等于放弃手输的内容：先清掉之前输入框里的错误提示，再决定要不要保存。
  draft.value = String(n)
  error.value = ''
  if (n !== cols.value) await save(n)
}
</script>

<template>
  <section class="p-3.5 rounded-xs border border-border/70 bg-card/60 space-y-2.5" aria-label="固定终端宽度" data-testid="pref-fixed-width">
    <label class="flex items-center justify-between gap-3 cursor-pointer">
      <span class="flex items-center gap-2 min-w-0">
        <span class="text-xs font-mono font-medium text-foreground">固定终端宽度</span>
        <span class="px-1.5 py-0.5 rounded-xs text-[10px] font-mono bg-primary/10 border border-primary/30 text-primary" data-testid="pref-fixed-width-scope">所有设备共用</span>
      </span>
      <Switch
        :checked="enabled"
        :disabled="!prefs || saving"
        aria-label="固定终端宽度"
        data-testid="pref-fixed-width-switch"
        @update:checked="onToggle"
      />
    </label>

    <p v-if="!prefs" class="text-[10px] leading-relaxed text-muted-foreground" data-testid="pref-fixed-width-unavailable">
      终端功能未开启，或服务端版本较旧、还不支持这项设置。
    </p>

    <div v-if="enabled" class="space-y-2">
      <div class="flex items-center gap-2 flex-wrap">
        <Input
          v-model="draft"
          type="text"
          inputmode="numeric"
          autocomplete="off"
          class="h-7 w-20 rounded-sm px-2 text-xs font-mono tabular-nums"
          aria-label="固定列数"
          :disabled="saving"
          data-testid="pref-fixed-cols-input"
          @keydown.enter.prevent="applyDraft"
          @blur="applyDraft"
        />
        <span class="text-xs text-muted-foreground">列</span>
        <span class="mx-1 h-4 w-px bg-border" aria-hidden="true" />
        <button
          v-for="n in FIXED_COLS_PRESETS"
          :key="n"
          type="button"
          class="h-6 min-w-9 px-2 rounded-sm border text-[11px] font-mono tabular-nums cursor-pointer transition-colors disabled:opacity-50 disabled:cursor-default"
          :class="n === cols ? 'border-primary/60 bg-primary/10 text-primary' : 'border-border/80 text-muted-foreground hover:text-foreground hover:bg-muted'"
          :disabled="saving"
          :data-testid="`pref-fixed-cols-preset-${n}`"
          @click="pick(n)"
        >{{ n }}</button>
      </div>
      <p v-if="error" class="text-[10px] text-destructive" role="alert" data-testid="pref-fixed-width-error">{{ error }}</p>
      <p v-else class="text-[10px] text-muted-foreground">范围 {{ limits.min }}–{{ limits.max }} 列；按 Enter 或点其他地方保存。</p>
    </div>

    <p class="text-[10px] leading-relaxed text-muted-foreground">
      开启后，每个终端会话的宽度固定为设定的列数，不再随浏览器窗口变化：在电脑、平板、手机上看到的是同一个宽度的排版，会话记录也是。
      窗口放不下时自动缩小字号（宽窗口仍用上面设置的字号，最小缩到 4 px；手机上字会比较小，可以按需调低列数），放不下到极限时可以横向滑动。
      这一项只改浏览器上报的终端尺寸，不涉及主机上的任何数据；关闭后恢复为跟随窗口。
      常用列数里哪个最合适，之后会结合 Cursor / Claude / Codex 等工具的实际排版与常见命令行需求再评估，现在的 {{ FIXED_COLS_DEFAULT }} 只是暂定值。
    </p>
  </section>
</template>
