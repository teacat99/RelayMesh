<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useTerminalStore } from '../../stores/terminal'
import { toApiError } from '../../api/terminal'
import { grantSummary } from '../../lib/terminal/modelAccess'

// 新增主机时的「模型默认授权」选项：默认不勾选（不授予）。
// 勾选等于把这台主机的「新模型默认」设为允许：新增时写入现有模型令牌的主机名单，以后新建的令牌也会预选它。
// 它只是预填规则——写成令牌名单里的明确条目并记入审计，运行期间不存在继承。
const props = defineProps<{
  modelValue: boolean
  /** 待启用主机的机器码：给出时勾选后会向服务端预览「将授予 N 个模型」；SSH 主机登记前没有机器码，只给通用说明。 */
  code?: string
  disabled?: boolean
}>()
const emit = defineEmits<{ 'update:modelValue': [value: boolean] }>()

const store = useTerminalStore()
const names = ref<string[] | null>(null)
const full = ref(0)
const loadError = ref('')

watch(
  () => [props.modelValue, props.code] as const,
  async ([on, code]) => {
    names.value = null
    full.value = 0
    loadError.value = ''
    if (!on || !code) return
    try {
      const r = await store.previewGrantModels(code)
      // 期间用户可能已经取消勾选或换了主机：只采纳仍然匹配的结果。
      if (props.modelValue && props.code === code) {
        names.value = r.granted
        full.value = r.full
      }
    } catch (e) {
      if (props.modelValue && props.code === code) loadError.value = toApiError(e).message
    }
  },
  { immediate: true },
)

const summary = computed(() => (names.value ? grantSummary(names.value) : ''))
</script>

<template>
  <div class="space-y-1" data-testid="host-default-option">
    <label class="flex items-start gap-2 cursor-pointer">
      <input
        type="checkbox"
        class="mt-0.5 accent-primary"
        :checked="modelValue"
        :disabled="disabled"
        data-testid="host-default-allow"
        @change="emit('update:modelValue', ($event.target as HTMLInputElement).checked)"
      />
      <span class="min-w-0">
        <span class="block text-xs text-foreground">让现有的模型令牌也能使用这台主机</span>
        <span class="block text-[11px] leading-relaxed text-muted-foreground">
          默认不勾选。勾选后，它会被写入所有带终端权限的模型令牌的主机名单（并记入审计），以后新建的令牌也会预选它；
          不勾选则一切照旧，之后可以在「设置 · 模型执行与权限」里逐个调整。
        </span>
      </span>
    </label>
    <p v-if="modelValue && summary" class="ml-6 text-[11px] leading-relaxed text-primary" data-testid="host-default-preview">
      {{ summary }}<template v-if="full > 0">；另有 {{ full }} 个令牌的名单已满，不会被写入</template>
    </p>
    <p v-else-if="modelValue && loadError" class="ml-6 text-[11px] leading-relaxed text-muted-foreground">无法预览授予范围：{{ loadError }}</p>
  </div>
</template>
