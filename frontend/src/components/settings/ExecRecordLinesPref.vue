<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { toast } from 'vue-sonner'
import { useTerminalStore } from '@/stores/terminal'
import { UnlockCancelled, toApiError } from '@/api/terminal'
import {
  RECORD_LINES_DEFAULT,
  RECORD_LINES_PRESETS,
  coverageText,
  fmtLines,
  parseRecordLines,
  tightensRecordLines,
} from '@/lib/terminal/recordLines'
import Button from '../ui/button/Button.vue'
import { Input } from '../ui/input'
import ConfirmDialog from '../terminal/ConfirmDialog.vue'

// 终端限高（按行数）：每个会话在主机上的记录至少保留最近这么多行输出，更早的整段滚动丢弃，避免长上下文会话的记录无限膨胀。
// 它和上面只读的字节 / 天数配额是两回事：这一项可以在界面里改，但会让主机丢弃已记录的旧内容，所以必须刚刚重新输入过密码，并写入审计。
const terminal = useTerminalStore()
const prefs = computed(() => terminal.prefs)
const limits = computed(() => ({
  min: prefs.value?.limits.record_max_lines_min ?? 5000,
  max: prefs.value?.limits.record_max_lines_max ?? 10_000_000,
}))
const current = computed(() => prefs.value?.record_max_lines ?? 0)
const coverage = computed(() => (current.value > 0 ? coverageText(prefs.value?.record_lines) : null))

const draft = ref(current.value > 0 ? fmtLines(current.value) : '')
const error = ref('')
const saving = ref(false)
const confirmOpen = ref(false)
const pending = ref(0)

// 别的设备 / 页面改了值：输入框跟随（没有在编辑时才有意义，这里简单地总是同步已保存的值）。
watch(current, v => {
  draft.value = v > 0 ? fmtLines(v) : ''
  error.value = ''
})

const parsed = computed(() => parseRecordLines(draft.value, limits.value))
const dirty = computed(() => parsed.value.ok && parsed.value.value !== current.value)

async function commit(next: number) {
  saving.value = true
  error.value = ''
  try {
    await terminal.saveRecordMaxLines(next)
    toast.success(next > 0 ? `已设置：每个会话保留最近 ${fmtLines(next)} 行` : '已取消按行数限制', { duration: 2500 })
  } catch (e) {
    if (e instanceof UnlockCancelled) return
    error.value = toApiError(e).message
    toast.error(`保存失败：${error.value}`, { duration: 4000 })
  } finally {
    saving.value = false
  }
}

function apply() {
  const p = parsed.value
  if (!p.ok) {
    error.value = p.message
    return
  }
  error.value = ''
  if (p.value === current.value) return
  if (tightensRecordLines(current.value, p.value)) {
    pending.value = p.value
    confirmOpen.value = true
    return
  }
  void commit(p.value)
}

function onConfirm() {
  confirmOpen.value = false
  void commit(pending.value)
}

function pick(n: number) {
  draft.value = fmtLines(n)
  error.value = ''
}

function clearCap() {
  void commit(0)
}

function fillDefault() {
  draft.value = fmtLines(RECORD_LINES_DEFAULT)
  error.value = ''
}
</script>

<template>
  <section class="p-3.5 rounded-xs border border-border/70 bg-card/60 space-y-2.5" aria-label="终端限高" data-testid="pref-record-lines">
    <div class="flex items-center gap-2 flex-wrap">
      <div class="text-xs font-mono font-medium text-foreground">终端限高（按行数）</div>
      <span class="px-1.5 py-0.5 rounded-xs text-[10px] font-mono bg-primary/10 border border-primary/30 text-primary">所有主机 · 服务端设置</span>
      <span
        class="px-1.5 py-0.5 rounded-xs text-[10px] font-mono border"
        :class="current > 0 ? 'bg-amber-500/10 border-amber-500/30 text-amber-500' : 'bg-muted/60 border-border/70 text-muted-foreground'"
        data-testid="pref-record-lines-state"
      >{{ current > 0 ? `保留最近 ${fmtLines(current)} 行` : '未限制' }}</span>
    </div>

    <p v-if="!prefs" class="text-[10px] leading-relaxed text-muted-foreground" data-testid="pref-record-lines-unavailable">
      终端功能未开启，或服务端版本较旧、还不支持这项设置。
    </p>

    <template v-else>
      <div class="flex items-center gap-2 flex-wrap">
        <Input
          v-model="draft"
          type="text"
          inputmode="numeric"
          autocomplete="off"
          placeholder="不限制"
          class="h-7 w-28 rounded-sm px-2 text-xs font-mono tabular-nums"
          aria-label="保留行数"
          :disabled="saving"
          data-testid="pref-record-lines-input"
          @keydown.enter.prevent="apply"
        />
        <span class="text-xs text-muted-foreground">行</span>
        <Button
          size="sm"
          class="h-7 px-3 rounded-sm text-xs"
          :disabled="saving || !dirty"
          data-testid="pref-record-lines-apply"
          @click="apply"
        >应用</Button>
        <Button
          v-if="current > 0"
          variant="outline"
          size="sm"
          class="h-7 px-3 rounded-sm text-xs"
          :disabled="saving"
          data-testid="pref-record-lines-clear"
          @click="clearCap"
        >取消限制</Button>
      </div>
      <div class="flex items-center gap-1.5 flex-wrap">
        <span class="text-[10px] text-muted-foreground">常用</span>
        <button
          v-for="n in RECORD_LINES_PRESETS"
          :key="n"
          type="button"
          class="h-6 px-2 rounded-sm border text-[11px] font-mono tabular-nums cursor-pointer transition-colors disabled:opacity-50 disabled:cursor-default"
          :class="n === current ? 'border-primary/60 bg-primary/10 text-primary' : 'border-border/80 text-muted-foreground hover:text-foreground hover:bg-muted'"
          :disabled="saving"
          :data-testid="`pref-record-lines-preset-${n}`"
          @click="pick(n)"
        >{{ fmtLines(n) }}</button>
        <button
          v-if="!draft && current === 0"
          type="button"
          class="text-[10px] font-mono text-muted-foreground hover:text-foreground underline underline-offset-2 cursor-pointer"
          @click="fillDefault"
        >填入暂定值 {{ fmtLines(RECORD_LINES_DEFAULT) }}</button>
      </div>
      <p v-if="error" class="text-[10px] text-destructive" role="alert" data-testid="pref-record-lines-error">{{ error }}</p>
      <p v-else-if="draft && !parsed.ok" class="text-[10px] text-destructive" data-testid="pref-record-lines-hint">{{ parsed.message }}</p>
      <p v-else class="text-[10px] text-muted-foreground">范围 {{ fmtLines(limits.min) }}–{{ fmtLines(limits.max) }} 行；留空表示不限制。</p>

      <p
        v-if="coverage"
        class="text-[10px] leading-relaxed"
        :class="coverage.warn ? 'text-amber-600 dark:text-amber-400' : 'text-muted-foreground'"
        data-testid="pref-record-lines-coverage"
      >{{ coverage.text }}</p>
    </template>

    <p class="text-[10px] leading-relaxed text-muted-foreground">
      长时间运行的会话（例如一直在刷输出的智能体）在主机上的记录会越来越大；设了行数上限后，每个会话至少保留最近这么多行，更早的内容整段丢弃。
      行数按输出里的换行符统计；记录以分段为单位丢弃：设置之后写入的内容，实际保留的行数会比设定值多出不到一成；设置之前就已经写下的较大分段（每段最大 1 MiB），要等新增输出满设定的行数之后才会被丢弃，所以刚设置时留下的内容可能暂时多一些。它与上面的字节配额互相独立，哪个先到就先按哪个丢。
      只作用于仍在运行（以及之后新建）的会话，已结束会话的记录不受影响，仍按保留期清理。丢掉的内容无法恢复，所以修改需要再次验证密码，并会写入审计日志。
      合适的数值之后会按各工具的实际输出体量与记录文件大小来评估，现在的 {{ fmtLines(RECORD_LINES_DEFAULT) }} 行只是暂定值。
    </p>

    <ConfirmDialog
      :open="confirmOpen"
      title="确认设置终端保留行数？"
      :description="`每个运行中的会话在主机上的记录将只保留最近 ${fmtLines(pending)} 行，更早的内容会被永久丢弃，无法恢复。已结束会话的记录不受影响。`"
      confirm-text="确认设置"
      destructive
      :busy="saving"
      @update:open="(v: boolean) => (confirmOpen = v)"
      @confirm="onConfirm"
    />
  </section>
</template>
