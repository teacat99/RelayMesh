<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { toast } from 'vue-sonner'
import { AlertTriangle, GitBranch } from 'lucide-vue-next'
import { branchesApi, type BranchPrefs } from '@/api/branches'
import { parseEscalateMinutes } from '@/lib/branch'

// 分支请示的定夺偏好（#236 P2）：分支会话里的智能体发的请示，默认先送主会话的智能体定夺；
// 这里管两件事——主会话多久没答复就自动转给您，以及高风险（L2）请示要不要禁止主会话代答。
// 每项改动点一下 / 点保存就写入，立即对之后的请示与后台的超时扫描生效（已经在等的请示按新的时限算）。

const prefs = ref<BranchPrefs | null>(null)
const loadFailed = ref(false)
const busy = ref(false)
const minutesText = ref('')
const minutesError = ref('')

const effective = computed(() => prefs.value?.escalate_after_minutes ?? 0)
const isDefault = computed(() => !!prefs.value && prefs.value.escalate_minutes === 0)
// 输入框里的内容和已保存的值不同才允许保存
const dirty = computed(() => {
  const p = prefs.value
  if (!p) return false
  const r = parseEscalateMinutes(minutesText.value, p.min_escalate_minutes, p.max_escalate_minutes)
  return !r.ok || r.value !== p.escalate_minutes
})

function sync(p: BranchPrefs) {
  prefs.value = p
  minutesText.value = p.escalate_minutes > 0 ? String(p.escalate_minutes) : ''
  minutesError.value = ''
}

async function load() {
  loadFailed.value = false
  try {
    sync(await branchesApi.prefs())
  } catch {
    loadFailed.value = true
  }
}

async function save(patch: { l2_guard?: boolean; escalate_minutes?: number }) {
  if (busy.value) return
  busy.value = true
  try {
    sync(await branchesApi.updatePrefs(patch))
  } catch (err: any) {
    toast.error('保存失败：' + (err?.response?.data?.error || err?.message || err))
  } finally {
    busy.value = false
  }
}

function toggleGuard() {
  if (prefs.value) void save({ l2_guard: !prefs.value.l2_guard })
}

function saveMinutes() {
  const p = prefs.value
  if (!p) return
  const r = parseEscalateMinutes(minutesText.value, p.min_escalate_minutes, p.max_escalate_minutes)
  if (!r.ok) {
    minutesError.value = r.reason
    return
  }
  minutesError.value = ''
  void save({ escalate_minutes: r.value })
}

function resetMinutes() {
  minutesText.value = ''
  minutesError.value = ''
  void save({ escalate_minutes: 0 })
}

onMounted(load)
</script>

<template>
  <div class="p-3 rounded-xs border border-border/70 bg-card/60 space-y-3" data-testid="branch-prefs">
    <div class="flex items-center gap-1.5 text-xs font-mono font-medium text-foreground">
      <GitBranch class="w-3.5 h-3.5 text-primary" />
      <span>分支会话 · 请示先交给主会话定夺</span>
    </div>
    <p class="text-[10px] text-muted-foreground leading-relaxed">
      分支会话里的智能体发出的请示（异步提问 / 计划确认），在「向主会话开放·定夺」开着时（协作与托管预设默认开启）会先交给主会话的智能体：
      它可以自己答复，也可以转交给您；一直没人答复时会自动转给您。您随时可以直接答复，先到先得。
    </p>

    <div v-if="loadFailed" class="text-[11px] text-destructive flex items-center gap-1.5">
      <AlertTriangle class="w-3.5 h-3.5" />
      <span>读取失败</span>
      <button type="button" class="underline cursor-pointer" @click="load">重试</button>
    </div>

    <template v-else-if="prefs">
      <!-- 超时转交 -->
      <div class="space-y-1.5">
        <label class="text-xs font-mono font-medium text-foreground flex items-center justify-between" for="branch-prefs-minutes">
          <span>主会话多久没答复就转给您</span>
          <span class="text-[10px] text-primary font-bold" data-testid="branch-prefs-summary" :data-minutes="effective" :data-default="isDefault ? 'true' : 'false'">
            {{ effective }} 分钟{{ isDefault ? '（默认）' : '' }}
          </span>
        </label>
        <div class="flex flex-wrap items-center gap-1.5">
          <input
            id="branch-prefs-minutes"
            v-model="minutesText"
            type="text"
            inputmode="numeric"
            autocomplete="off"
            :placeholder="`默认 ${prefs.default_escalate_minutes}`"
            :disabled="busy"
            data-testid="branch-prefs-minutes"
            class="h-7 w-24 rounded-xs border border-border/70 bg-background/70 px-2 text-xs font-mono text-foreground placeholder:text-muted-foreground/60 focus:border-primary focus:outline-hidden disabled:opacity-60"
            @input="minutesError = ''"
            @keydown.enter.prevent="saveMinutes"
          />
          <span class="text-[10px] text-muted-foreground">分钟（{{ prefs.min_escalate_minutes }}–{{ prefs.max_escalate_minutes }}）</span>
          <button
            type="button"
            :disabled="busy || !dirty"
            data-testid="branch-prefs-save"
            class="h-7 px-2.5 text-xs rounded-xs border font-mono transition-all cursor-pointer border-primary bg-primary text-primary-foreground disabled:cursor-not-allowed disabled:opacity-50"
            @click="saveMinutes"
          >
            保存
          </button>
          <button
            v-if="!isDefault"
            type="button"
            :disabled="busy"
            data-testid="branch-prefs-reset"
            class="h-7 px-2.5 text-xs rounded-xs border font-mono transition-all cursor-pointer border-border/70 bg-background/70 text-muted-foreground hover:bg-muted disabled:opacity-50"
            @click="resetMinutes"
          >
            恢复默认
          </button>
        </div>
        <p v-if="minutesError" class="text-[11px] text-destructive" data-testid="branch-prefs-error">{{ minutesError }}</p>
      </div>

      <!-- L2 兜底 -->
      <button
        type="button"
        role="switch"
        :aria-checked="prefs.l2_guard"
        :disabled="busy"
        data-testid="branch-prefs-l2-guard"
        class="w-full p-2.5 rounded-xs border text-left transition-all font-mono cursor-pointer flex items-center justify-between gap-3 disabled:opacity-60"
        :class="prefs.l2_guard ? 'border-primary/40 bg-primary/10 text-foreground' : 'border-border/70 bg-background/70 text-muted-foreground'"
        @click="toggleGuard"
      >
        <div class="space-y-0.5">
          <div class="text-xs font-semibold">高风险（L2）请示不让主会话代答</div>
          <div class="text-[10px] text-muted-foreground leading-relaxed">
            开启后，主会话遇到 L2（不可逆 / 高风险）请示只能转交给您，不能自己答复；默认关闭，由主会话的智能体按它对任务的了解判断。
          </div>
        </div>
        <div class="w-8 h-4.5 rounded-full transition-colors flex items-center p-0.5 shrink-0" :class="prefs.l2_guard ? 'bg-primary' : 'bg-muted-foreground/30'">
          <div class="w-3.5 h-3.5 rounded-full bg-white transition-transform" :class="prefs.l2_guard ? 'translate-x-3.5' : 'translate-x-0'"></div>
        </div>
      </button>
    </template>

    <div v-else class="text-[11px] text-muted-foreground">读取中…</div>
  </div>
</template>
