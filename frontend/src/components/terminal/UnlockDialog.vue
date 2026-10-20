<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription } from '../ui/dialog'
import Button from '../ui/button/Button.vue'
import { Eye, EyeOff, Loader2, LockOpen, Timer } from 'lucide-vue-next'
import { useTerminalStore } from '../../stores/terminal'
import { toApiError } from '../../api/terminal'
import { formatCountdown } from '../../lib/terminal/format'

// 二次解锁：启用主机、新建 / 连接会话等高风险操作需要再输一次登录密码。
// 令牌只存在内存里，空闲一段时间或刷新页面后失效。
const store = useTerminalStore()

const password = ref('')
const showPassword = ref(false)
const busy = ref(false)
const error = ref('')
const lockedFor = ref(0)
const input = ref<HTMLInputElement | null>(null)
let lockTimer: ReturnType<typeof setInterval> | null = null

const idleMinutes = computed(() => Math.max(1, Math.round(store.idleSeconds / 60)))

watch(
  () => store.unlockDialogOpen,
  open => {
    if (!open) return
    password.value = ''
    error.value = ''
    showPassword.value = false
  },
)

function startLockout(seconds: number) {
  lockedFor.value = Math.max(1, Math.floor(seconds))
  if (lockTimer) clearInterval(lockTimer)
  lockTimer = setInterval(() => {
    lockedFor.value--
    if (lockedFor.value <= 0) {
      if (lockTimer) clearInterval(lockTimer)
      lockTimer = null
      error.value = ''
    }
  }, 1000)
}

async function submit() {
  if (busy.value || lockedFor.value > 0 || !password.value) return
  busy.value = true
  error.value = ''
  try {
    await store.unlock(password.value)
    password.value = ''
  } catch (e) {
    const err = toApiError(e)
    error.value = err.message
    if (err.code === 'unlock_locked') startLockout(Number(err.data.remaining_seconds) || 60)
    password.value = ''
    input.value?.focus()
  } finally {
    busy.value = false
  }
}

function onOpenChange(open: boolean) {
  if (!open) store.settleUnlock(false)
}

onBeforeUnmount(() => {
  if (lockTimer) clearInterval(lockTimer)
})
</script>

<template>
  <Dialog :open="store.unlockDialogOpen" @update:open="onOpenChange">
    <!-- layer="verify"：这个验证框常常是在别的弹窗里触发的——既有普通弹窗（例如「接入主机」），也有确认框（确认删除主机之后再要求输密码），
         所以要高于确认框所在的 top 层，否则确认框的遮罩会把它盖住 -->
    <DialogContent
      layer="verify"
      class="max-w-[380px] rounded-sm p-5 gap-4"
      @open-auto-focus="(e: Event) => { e.preventDefault(); input?.focus() }"
    >
      <DialogHeader class="space-y-1.5 text-left pr-6">
        <DialogTitle class="text-sm font-bold flex items-center gap-2">
          <LockOpen class="w-4 h-4 text-muted-foreground" />
          <span>验证身份以继续</span>
        </DialogTitle>
        <DialogDescription class="text-xs leading-relaxed text-muted-foreground">
          {{ store.unlockReason || '查看主机无需验证；启用主机、新建会话和连接终端前需要再输入一次登录密码。' }}
        </DialogDescription>
      </DialogHeader>

      <form class="space-y-3" @submit.prevent="submit">
        <div class="space-y-1.5">
          <label for="terminal-unlock-password" class="text-[11px] font-medium text-foreground">登录密码</label>
          <div class="relative flex items-center">
            <input
              id="terminal-unlock-password"
              ref="input"
              v-model="password"
              :type="showPassword ? 'text' : 'password'"
              autocomplete="current-password"
              :disabled="busy || lockedFor > 0"
              :aria-invalid="error !== ''"
              aria-describedby="terminal-unlock-error"
              class="w-full h-9 px-3 pr-9 rounded-sm bg-background border border-border/80 focus:border-primary focus:outline-none text-xs text-foreground placeholder:text-muted-foreground/60 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
              placeholder="输入登录密码"
            />
            <button
              type="button"
              class="absolute right-2 p-1 rounded-sm text-muted-foreground hover:text-foreground cursor-pointer"
              :aria-label="showPassword ? '隐藏密码' : '显示密码'"
              @click="showPassword = !showPassword"
            >
              <EyeOff v-if="showPassword" class="w-3.5 h-3.5" />
              <Eye v-else class="w-3.5 h-3.5" />
            </button>
          </div>
          <p id="terminal-unlock-error" class="min-h-[1rem] text-[11px] leading-relaxed text-destructive" role="alert">
            <template v-if="lockedFor > 0">
              <Timer class="inline w-3 h-3 -mt-0.5 mr-1" />{{ error }}，请在 {{ formatCountdown(lockedFor) }} 后重试
            </template>
            <template v-else>{{ error }}</template>
          </p>
        </div>

        <p class="text-[11px] leading-relaxed text-muted-foreground">
          解锁后连续 {{ idleMinutes }} 分钟没有操作会自动失效；你也可以随时点击顶部的「已解锁」计时，立即重新锁定。
        </p>

        <div class="flex justify-end gap-2 pt-1">
          <Button type="button" variant="outline" size="sm" class="rounded-sm text-xs" @click="onOpenChange(false)">取消</Button>
          <Button type="submit" size="sm" class="rounded-sm text-xs min-w-[72px]" :disabled="busy || lockedFor > 0 || !password">
            <Loader2 v-if="busy" class="w-3.5 h-3.5 animate-spin" />
            <span v-else>解锁</span>
          </Button>
        </div>
      </form>
    </DialogContent>
  </Dialog>
</template>
