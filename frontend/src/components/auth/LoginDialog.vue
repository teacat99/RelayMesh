<script setup lang="ts">
import { ref, onMounted, nextTick } from 'vue'
import { useAuthStore } from '../../stores/auth'
import { useSessionStore } from '../../stores/session'
import { User, KeyRound, Eye, EyeOff, ShieldCheck, ArrowRight, Loader2, ShieldAlert, Timer } from 'lucide-vue-next'

const authStore = useAuthStore()
const sessionStore = useSessionStore()

const username = ref(localStorage.getItem('relaymesh_username') || 'admin')
const password = ref('')
const showPassword = ref(false)

const usernameInput = ref<HTMLInputElement | null>(null)
const passwordInput = ref<HTMLInputElement | null>(null)

onMounted(() => {
  nextTick(() => {
    if (!username.value.trim() && usernameInput.value) {
      usernameInput.value.focus()
    } else if (passwordInput.value) {
      passwordInput.value.focus()
    }
  })
})

function handleUsernameEnter() {
  if (passwordInput.value) {
    passwordInput.value.focus()
  }
}

async function handleLogin() {
  if (authStore.isLocked || !username.value.trim() || !password.value.trim() || authStore.loginLoading) return
  
  const success = await authStore.login(username.value.trim(), password.value.trim())
  if (success) {
    localStorage.setItem('relaymesh_username', username.value.trim())
    password.value = ''
    // 登录成功后刷新数据并建立 SSE
    sessionStore.fetchCurrentSession()
    sessionStore.fetchSessions()
    sessionStore.connectSSE()
  }
}
</script>

<template>
  <div 
    v-if="authStore.showLoginModal" 
    class="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/50 dark:bg-black/70 backdrop-blur-xs transition-opacity duration-200"
  >
    <div 
      class="w-full max-w-[360px] bg-card border border-border/90 rounded-sm shadow-2xl p-6 space-y-5 animate-in fade-in zoom-in-95 duration-150"
    >
      <!-- Header -->
      <div class="flex flex-col items-center text-center space-y-2">
        <div class="w-10 h-10 rounded-full bg-primary/10 border border-primary/20 flex items-center justify-center text-primary mb-1">
          <ShieldCheck class="w-5 h-5" />
        </div>
        <h2 class="text-sm font-bold text-foreground tracking-tight">RelayMesh 访问认证</h2>
        <p class="text-xs text-muted-foreground leading-relaxed">
          当前环境已开启公网访问保护，请输入账号与访问密码以解锁中继控制台
        </p>
      </div>

      <!-- Lockout Alert Banner -->
      <div v-if="authStore.isLocked" class="p-3 rounded-xs bg-destructive/10 border border-destructive/25 text-destructive space-y-1.5 animate-in fade-in">
        <div class="flex items-center gap-1.5 font-bold text-xs">
          <ShieldAlert class="w-4 h-4 shrink-0" />
          <span>IP 触发安全锁定</span>
        </div>
        <p class="text-[11px] leading-relaxed opacity-90">
          由于连续多次尝试失败，系统已限制当前 IP 访问以保障数据安全。
        </p>
        <div class="flex items-center gap-1 text-[11px] font-mono font-semibold pt-0.5">
          <Timer class="w-3.5 h-3.5 animate-pulse" />
          <span>解封倒计时: {{ Math.floor(authStore.lockedRemainingSeconds / 60) }}分 {{ authStore.lockedRemainingSeconds % 60 }}秒</span>
        </div>
      </div>

      <!-- Form -->
      <div class="space-y-3">
        <!-- Username Field -->
        <div class="space-y-1.5">
          <label class="text-[11px] font-medium text-foreground flex items-center justify-between">
            <span class="flex items-center gap-1.5">
              <User class="w-3.5 h-3.5 text-muted-foreground" />
              <span>登录账号 (Username)</span>
            </span>
          </label>
          <input
            ref="usernameInput"
            type="text"
            v-model="username"
            :disabled="authStore.isLocked"
            placeholder="输入管理账号 (默认 admin)..."
            class="w-full h-9 px-3 rounded-xs bg-background border border-border/80 focus:border-primary focus:outline-none text-xs text-foreground placeholder:text-muted-foreground/60 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
            @keydown.enter="handleUsernameEnter"
          />
        </div>

        <!-- Password Field -->
        <div class="space-y-1.5">
          <label class="text-[11px] font-medium text-foreground flex items-center justify-between">
            <span class="flex items-center gap-1.5">
              <KeyRound class="w-3.5 h-3.5 text-muted-foreground" />
              <span>访问密码 (Password)</span>
            </span>
          </label>
          <div class="relative flex items-center">
            <input
              ref="passwordInput"
              :type="showPassword ? 'text' : 'password'"
              v-model="password"
              :disabled="authStore.isLocked"
              placeholder="输入访问密码..."
              class="w-full h-9 px-3 pr-9 rounded-xs bg-background border border-border/80 focus:border-primary focus:outline-none text-xs text-foreground placeholder:text-muted-foreground/60 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
              @keydown.enter="handleLogin"
            />
            <button
              type="button"
              class="absolute right-2.5 text-muted-foreground hover:text-foreground transition-colors cursor-pointer"
              @click="showPassword = !showPassword"
              tabindex="-1"
            >
              <EyeOff v-if="showPassword" class="w-3.5 h-3.5" />
              <Eye v-else class="w-3.5 h-3.5" />
            </button>
          </div>
        </div>

        <!-- Error Tip (when not locked) -->
        <div v-if="authStore.loginError && !authStore.isLocked" class="text-[11px] text-destructive bg-destructive/10 border border-destructive/20 rounded-2xs px-2.5 py-1.5 flex items-center gap-1.5">
          <span>{{ authStore.loginError }}</span>
        </div>

        <!-- Submit Button -->
        <button
          type="button"
          :disabled="authStore.isLocked || !username.trim() || !password.trim() || authStore.loginLoading"
          class="w-full h-9 rounded-xs bg-primary text-primary-foreground text-xs font-semibold flex items-center justify-center gap-1.5 hover:opacity-90 active:opacity-95 disabled:opacity-50 disabled:cursor-not-allowed transition-all cursor-pointer shadow-xs"
          @click="handleLogin"
        >
          <Loader2 v-if="authStore.loginLoading" class="w-3.5 h-3.5 animate-spin" />
          <span v-if="authStore.loginLoading">正在验证...</span>
          <span v-else-if="authStore.isLocked">已锁定防护中</span>
          <span v-else class="flex items-center gap-1.5">
            <span>进入控制台</span>
            <ArrowRight class="w-3.5 h-3.5" />
          </span>
        </button>
      </div>
    </div>
  </div>
</template>
