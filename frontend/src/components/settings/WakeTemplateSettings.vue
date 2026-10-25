<script setup lang="ts">
// 「唤醒词」全局覆盖（#232 / #233 D3，D-376）：Hub 在叫醒智能体时发给它的那段话，一共三类，每一类都能在这里改成自己的文字。
// 优先级（谁在前谁生效）：启动器预设里自己写的正文 > 会话里的会话模板（仅续聊 / 新开对话）> 这里的全局覆盖 > 内置默认。
// 全局覆盖随设置 JSON 的 wakeTemplates 字段同步到服务端；因为设置是整包同步、服务端遇到非法的 wakeTemplates 会拒收整份设置，
// 所以保存前先用预览接口按服务端的同一套规则预检一次，不合格的不写进设置。
import { reactive, watch } from 'vue'
import { BellRing } from 'lucide-vue-next'
import { toast } from '@/components/ui/sonner'
import { wakeErrorMessage, wakePromptsApi, type WakeKind } from '@/api/wakePrompts'
import {
  WAKE_KINDS,
  wakeDirty,
  wakeKindLabel,
  wakeOverrideOf,
  wakeTemplateProblem,
  withWakeOverride,
} from '@/lib/wakePrompts'
import { useSettingsStore } from '@/stores/settings'
import { useWakePromptsStore } from '@/stores/wakePrompts'
import WakeTemplateField from '@/components/feedback/WakeTemplateField.vue'

const settingsStore = useSettingsStore()
const wakeStore = useWakePromptsStore()
void wakeStore.ensure()

function blank(): Record<WakeKind, string> {
  return { resume: '', new: '', web_start: '' }
}
const drafts = reactive<Record<WakeKind, string>>(blank())
const touched = reactive<Record<WakeKind, boolean>>({ resume: false, new: false, web_start: false })
const errors = reactive<Record<WakeKind, string>>(blank())
const busy = reactive<Record<WakeKind, boolean>>({ resume: false, new: false, web_start: false })

function savedOf(kind: WakeKind): string {
  return wakeOverrideOf(settingsStore.settings.wakeTemplates, kind)
}
function tid(kind: WakeKind): string {
  return `wake-global-${kind.replace('_', '-')}`
}
function problemOf(kind: WakeKind): string {
  return wakeTemplateProblem(kind, drafts[kind], wakeStore.max)
}
function isDirty(kind: WakeKind): boolean {
  return wakeDirty(drafts[kind], savedOf(kind))
}

// 已保存的覆盖变化（首次载入 / 另一台设备同步过来）：没动过的编辑框跟着更新
watch(
  () => settingsStore.settings.wakeTemplates,
  () => {
    for (const k of WAKE_KINDS) {
      if (!touched[k]) drafts[k] = savedOf(k)
    }
  },
  { immediate: true, deep: true },
)

function onDraft(kind: WakeKind, value: string) {
  drafts[kind] = value
  touched[kind] = true
  errors[kind] = ''
}

async function save(kind: WakeKind) {
  if (busy[kind] || !isDirty(kind) || problemOf(kind)) return
  errors[kind] = ''
  busy[kind] = true
  try {
    const text = drafts[kind]
    // 预检：服务端按真正发出时用的同一套规则校验；非法的直接在这里报错，不写进设置
    if (text.trim() !== '') await wakePromptsApi.preview({ kind, template: text })
    settingsStore.updateSettings({ wakeTemplates: withWakeOverride(settingsStore.settings.wakeTemplates, kind, text) })
    touched[kind] = false
    drafts[kind] = savedOf(kind)
    toast.success(text.trim() === '' ? `已清除「${wakeKindLabel(kind)}」的全局覆盖` : `已保存「${wakeKindLabel(kind)}」`, {
      description: '下一次生成这类唤醒词时就用它；预设或会话里单独写的文字仍然优先。',
      duration: 2400,
    })
  } catch (err) {
    errors[kind] = wakeErrorMessage(err, '保存失败')
  } finally {
    busy[kind] = false
  }
}

function reset(kind: WakeKind) {
  settingsStore.updateSettings({ wakeTemplates: withWakeOverride(settingsStore.settings.wakeTemplates, kind, '') })
  touched[kind] = false
  drafts[kind] = savedOf(kind)
  errors[kind] = ''
  toast.success('已恢复内置默认', { duration: 1800 })
}
</script>

<template>
  <section class="space-y-3" data-testid="wake-templates-settings">
    <div class="p-3 rounded-xs border border-border/70 bg-card/60 space-y-2">
      <div class="flex items-center gap-1.5 text-xs font-mono font-medium text-foreground">
        <BellRing class="w-3.5 h-3.5 text-primary" />
        <span>唤醒词 · 怎么生效</span>
      </div>
      <ul class="text-[11px] text-muted-foreground leading-relaxed list-disc pl-4 space-y-0.5">
        <li><b class="text-foreground">唤醒词</b>是 Hub 叫醒智能体时发给它的那段话。下面三类各有一份内置默认，可以分别改成自己的文字；留空就用内置默认。</li>
        <li>
          优先级从高到低：<b class="text-foreground">启动器预设里自己写的正文</b> →
          <b class="text-foreground">会话「常驻」弹窗里的会话模板</b>（只有续聊和新开对话有）→
          <b class="text-foreground">这里的全局覆盖</b> → <b class="text-foreground">内置默认</b>。
        </li>
        <li>文字里用 <code class="px-1 rounded-xs bg-muted">%变量</code> 引用会话的现状（点变量按钮插入）：发出那一刻由 Hub 换成当时的真实值，不认识的 <code class="px-1 rounded-xs bg-muted">%xxx</code> 原样保留。</li>
        <li>保存前会按服务端的同一套规则预检；预览里能看到最终发出去的文字（设置页用示例值，会话页用这个会话此刻的真实现状）。</li>
      </ul>
    </div>

    <div
      v-for="kind in WAKE_KINDS"
      :key="kind"
      class="p-3 rounded-xs border border-border/70 bg-card/60 space-y-2"
      :data-testid="`${tid(kind)}-card`"
      :data-overridden="savedOf(kind) ? 'true' : 'false'"
    >
      <WakeTemplateField
        :kind="kind"
        :model-value="drafts[kind]"
        :saved-layer="savedOf(kind) ? 'global' : 'builtin'"
        :testid="tid(kind)"
        empty-hint="留空：使用内置默认。点「以内置默认为底稿」可以在它的基础上改。"
        @update:model-value="(v: string) => onDraft(kind, v)"
      />
      <p v-if="errors[kind]" class="text-[11px] text-destructive" :data-testid="`${tid(kind)}-error`">{{ errors[kind] }}</p>
      <div class="flex flex-wrap items-center gap-2 text-[11px]">
        <span v-if="isDirty(kind)" class="text-[10px] text-amber-600 dark:text-amber-400" :data-testid="`${tid(kind)}-dirty`">有未保存的改动</span>
        <span class="flex-1" />
        <button
          v-if="savedOf(kind)"
          type="button"
          :data-testid="`${tid(kind)}-reset`"
          class="px-2.5 h-7 rounded-xs border border-border/80 text-[11px] text-muted-foreground hover:text-foreground cursor-pointer"
          @click="reset(kind)"
        >恢复内置默认</button>
        <button
          type="button"
          :data-testid="`${tid(kind)}-save`"
          :disabled="!isDirty(kind) || !!problemOf(kind) || busy[kind]"
          class="px-3 h-7 rounded-xs bg-primary text-primary-foreground text-[11px] font-semibold cursor-pointer hover:brightness-110 disabled:opacity-50 disabled:cursor-not-allowed"
          @click="save(kind)"
        >{{ busy[kind] ? '保存中…' : '保存' }}</button>
      </div>
    </div>
  </section>
</template>
