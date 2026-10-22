<script setup lang="ts">
// 工具栏上的「授权档位」小标签：异步对话生效时取代阶段滑块（异步模式里阶段提示词不再注入，
// 智能体能直接做什么由授权档位决定）。点开列出全部档位，点一下就给这个工作流换档；
// 「管理档位」打开设置里的「授权档位」分类。
import { computed, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { Check, ShieldCheck } from 'lucide-vue-next'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { toast } from '@/components/ui/sonner'
import { asyncModeErrorMessage } from '@/api/asyncMode'
import { levelLabel, profileChipText, profileChipTitle } from '@/lib/asyncMode'
import { useAsyncModeStore } from '@/stores/asyncMode'
import { useSessionStore } from '@/stores/session'
import { useSettingsStore } from '@/stores/settings'
import { useSettingsUiStore } from '@/stores/settingsUi'

const props = defineProps<{ workflowId: string }>()

const router = useRouter()
const sessionStore = useSessionStore()
const asyncStore = useAsyncModeStore()
const settingsStore = useSettingsStore()
const settingsUi = useSettingsUiStore()

const open = ref(false)
const view = computed(() => sessionStore.asyncMode)
/** 当前实际生效的档位编号（工作流选用的档位已被删掉时，服务端回落到默认档位，这里跟着解析结果走）。 */
const activeId = computed(() => view.value?.profile?.id ?? 0)

watch(open, isOpen => {
  if (isOpen) void asyncStore.loadProfiles()
})

async function pick(id: number) {
  if (id === activeId.value && view.value?.profile_id === id) {
    open.value = false
    return
  }
  try {
    await asyncStore.selectProfile(props.workflowId, id)
    open.value = false
  } catch (err) {
    toast.error(asyncModeErrorMessage(err, '无法切换授权档位'))
  }
}

function manage() {
  open.value = false
  settingsUi.enter('relay', 'authProfiles')
  if (typeof window !== 'undefined' && window.innerWidth < 768) {
    void router.push({ path: '/settings', query: { module: 'relay' } })
  } else {
    settingsStore.openSettings()
  }
}
</script>

<template>
  <Popover v-model:open="open">
    <PopoverTrigger as-child>
      <button
        type="button"
        data-testid="auth-profile-chip"
        :title="profileChipTitle(view)"
        :aria-label="`授权档位 ${profileChipText(view)}`"
        class="text-[11px] sm:text-xs px-2 sm:px-2.5 py-1 rounded-sm border transition-all shadow-2xs flex items-center gap-1.5 cursor-pointer shrink-0 bg-card/95 hover:bg-muted border-border/80 text-foreground backdrop-blur-xs select-none font-mono outline-none focus-visible:ring-1 focus-visible:ring-ring"
      >
        <ShieldCheck class="w-3.5 h-3.5 text-emerald-600 dark:text-emerald-400" />
        <span data-testid="auth-profile-chip-text">{{ profileChipText(view) }}</span>
        <span v-if="view?.profile" class="tabular-nums text-muted-foreground" data-testid="auth-profile-chip-level">{{ view.profile.level }}</span>
      </button>
    </PopoverTrigger>

    <PopoverContent align="start" class="w-[min(24rem,calc(100vw-1.5rem))] p-3 font-mono text-[11px] sm:text-xs">
      <div data-testid="auth-profile-popover" class="space-y-2">
        <div class="font-semibold text-foreground">
          授权档位<span class="font-normal text-muted-foreground/70"> · 异步对话时智能体不必先问就能做的事</span>
        </div>
        <p class="text-muted-foreground">
          只影响这个工作流。不可逆操作（部署、推送、删数据、改凭据）在任何档位下都会发 L2 确认卡等您批准，批准前不会执行。
        </p>
        <ul class="space-y-1" role="radiogroup" aria-label="授权档位">
          <li v-for="p in asyncStore.profiles" :key="p.id">
            <button
              type="button"
              role="radio"
              :aria-checked="p.id === activeId"
              :data-testid="`auth-profile-option-${p.id}`"
              :disabled="asyncStore.busy"
              class="w-full text-left rounded-sm border px-2 py-1.5 transition-colors cursor-pointer"
              :class="p.id === activeId ? 'border-primary/60 bg-primary/10' : 'border-border/70 hover:bg-muted'"
              @click="pick(p.id)"
            >
              <span class="flex items-center gap-1.5 font-semibold text-foreground">
                <Check v-if="p.id === activeId" class="w-3 h-3 text-primary shrink-0" />
                <span class="truncate">{{ p.name }}</span>
                <span class="text-muted-foreground font-normal tabular-nums">{{ p.level }} · {{ levelLabel(p.level) }}</span>
                <span v-if="p.is_default" class="ml-auto text-[10px] text-muted-foreground font-normal">默认</span>
              </span>
              <span class="block mt-0.5 text-muted-foreground">{{ p.rules }}</span>
            </button>
          </li>
        </ul>
        <button
          type="button"
          data-testid="auth-profile-manage"
          class="underline underline-offset-2 text-muted-foreground hover:text-foreground cursor-pointer"
          @click="manage"
        >管理档位…</button>
      </div>
    </PopoverContent>
  </Popover>
</template>
