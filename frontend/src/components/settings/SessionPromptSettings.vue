<script setup lang="ts">
// 「会话提示词」设置页（#232）：编辑常驻规则的内置模板（主线 / 分支）。
// 模板只是「套用时放进文本框的起点」：在会话输入区的「常驻」弹窗里一点套用、新建会话的高级选项里也能选。
// 改模板只影响以后套用的会话，已经保存在某个会话里的常驻规则不会变。
// 页面下半部分是「唤醒词」的全局覆盖（#232 D3）：Hub 叫醒智能体时发给它的那段话（WakeTemplateSettings）。
import { computed, reactive, watch } from 'vue'
import { toast } from '@/components/ui/sonner'
import { ScrollText } from 'lucide-vue-next'
import {
  RESIDENT_MAX_DEFAULT,
  builtinTemplate,
  residentLength,
  templateOptions,
  withTemplateOverride,
  type ResidentTemplateId,
} from '@/lib/residentPrompt'
import { useSettingsStore } from '@/stores/settings'
import WakeTemplateSettings from './WakeTemplateSettings.vue'

const settingsStore = useSettingsStore()
const options = computed(() => templateOptions(settingsStore.settings.promptTemplates))

const drafts = reactive<Record<ResidentTemplateId, string>>({ main: '', branch: '' })
const touched = reactive<Record<ResidentTemplateId, boolean>>({ main: false, branch: false })

// 生效文字变化（首次载入 / 另一台设备同步过来）：没动过的编辑框跟着更新
watch(
  options,
  list => {
    for (const t of list) {
      if (!touched[t.id]) drafts[t.id] = t.current
    }
  },
  { immediate: true },
)

function isDirty(id: ResidentTemplateId): boolean {
  const cur = options.value.find(t => t.id === id)?.current ?? ''
  return drafts[id].trim() !== cur.trim()
}

function over(id: ResidentTemplateId): boolean {
  return residentLength(drafts[id]) > RESIDENT_MAX_DEFAULT
}

function save(id: ResidentTemplateId) {
  if (!isDirty(id) || over(id)) return
  settingsStore.updateSettings({ promptTemplates: withTemplateOverride(settingsStore.settings.promptTemplates, id, drafts[id]) })
  touched[id] = false
  drafts[id] = options.value.find(t => t.id === id)?.current ?? drafts[id]
  toast.success('已保存模板', { description: '只影响以后套用的会话，已保存在会话里的规则不变。', duration: 2200 })
}

function reset(id: ResidentTemplateId) {
  settingsStore.updateSettings({ promptTemplates: withTemplateOverride(settingsStore.settings.promptTemplates, id, '') })
  touched[id] = false
  drafts[id] = builtinTemplate(id).text
  toast.success('已恢复出厂文字', { duration: 1800 })
}
</script>

<template>
  <div class="space-y-3" data-testid="session-prompt-settings">
    <div class="p-3 rounded-xs border border-border/70 bg-card/60 space-y-2">
      <div class="flex items-center gap-1.5 text-xs font-mono font-medium text-foreground">
        <ScrollText class="w-3.5 h-3.5 text-primary" />
        <span>常驻规则 · 怎么生效</span>
      </div>
      <ul class="text-[11px] text-muted-foreground leading-relaxed list-disc pl-4 space-y-0.5">
        <li>每个会话可以有一份常驻规则（会话输入区工具栏的「常驻」）：<b class="text-foreground">智能体每次调用 RelayMesh 的回执都会带上它</b>，同步和异步会话都一样。</li>
        <li>上下文被压缩后的恢复路径（读收件箱、读恢复胶囊）也会带，所以它不会因为「忘了」而失效。</li>
        <li>你改了规则，智能体下一次调用时会收到「刚更新」的全文，只提醒一次；「常驻」弹窗里能看到它读到没有。</li>
        <li>常驻规则<b class="text-foreground">不能放宽授权档位</b>：冲突时以更严的为准；智能体只读，改规则只有你能做。</li>
      </ul>
    </div>

    <div
      v-for="t in options"
      :key="t.id"
      class="p-3 rounded-xs border border-border/70 bg-card/60 space-y-2 font-mono"
      :data-testid="`prompt-template-${t.id}`"
      :data-overridden="t.overridden ? 'true' : 'false'"
    >
      <div class="flex flex-wrap items-center gap-2">
        <span class="text-xs font-semibold text-foreground">{{ t.label }}</span>
        <span
          class="text-[10px] px-1.5 py-px rounded-xs border"
          :class="t.overridden ? 'border-primary/40 bg-primary/10 text-primary' : 'border-border/70 text-muted-foreground'"
        >{{ t.overridden ? '已改成你自己的文字' : '出厂文字' }}</span>
      </div>
      <p class="text-[11px] text-muted-foreground leading-relaxed">{{ t.hint }}</p>
      <textarea
        v-model="drafts[t.id]"
        :data-testid="`prompt-template-${t.id}-text`"
        rows="9"
        spellcheck="false"
        class="w-full px-2.5 py-2 text-xs bg-background border border-border/80 rounded-xs focus:outline-none focus:border-primary text-foreground resize-y leading-relaxed min-h-[8rem]"
        :class="over(t.id) ? 'border-destructive focus:border-destructive' : ''"
        @input="touched[t.id] = true"
      />
      <div class="flex flex-wrap items-center gap-2 text-[11px]">
        <span
          class="tabular-nums"
          :class="over(t.id) ? 'text-destructive font-semibold' : 'text-muted-foreground'"
          :data-testid="`prompt-template-${t.id}-count`"
        >{{ residentLength(drafts[t.id]) }} / {{ RESIDENT_MAX_DEFAULT }}</span>
        <span v-if="over(t.id)" class="text-destructive">超出上限，请删减后再保存</span>
        <span class="flex-1" />
        <button
          v-if="t.overridden"
          type="button"
          :data-testid="`prompt-template-${t.id}-reset`"
          class="px-2.5 h-7 rounded-xs border border-border/80 text-[11px] text-muted-foreground hover:text-foreground cursor-pointer"
          @click="reset(t.id)"
        >恢复出厂</button>
        <button
          type="button"
          :data-testid="`prompt-template-${t.id}-save`"
          :disabled="!isDirty(t.id) || over(t.id)"
          class="px-3 h-7 rounded-xs bg-primary text-primary-foreground text-[11px] font-semibold cursor-pointer hover:brightness-110 disabled:opacity-50 disabled:cursor-not-allowed"
          @click="save(t.id)"
        >保存</button>
      </div>
    </div>

    <WakeTemplateSettings />
  </div>
</template>
