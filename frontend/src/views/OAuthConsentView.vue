<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { oauthApi } from '../api/client'
import { useAuthStore } from '../stores/auth'
import { useTerminalStore } from '../stores/terminal'
import { Button } from '../components/ui/button'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const terminal = useTerminalStore()

const reqId = computed(() => String(route.query.req || ''))
const loading = ref(true)
const error = ref('')
const info = ref<{
  client_name: string
  redirect_host: string
  scopes: string[]
  warn_untrusted: boolean
  warn_loopback: boolean
} | null>(null)
const busy = ref(false)

onMounted(async () => {
  if (!reqId.value) {
    error.value = '缺少授权请求编号'
    loading.value = false
    return
  }
  await auth.checkAuthStatus()
  if (auth.authRequired && !auth.token) {
    await router.replace({ path: '/settings', query: { login: '1', redirect: route.fullPath } })
    return
  }
  try {
    const r = await oauthApi.getRequest(reqId.value)
    info.value = r.request
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '无法加载授权请求'
  } finally {
    loading.value = false
  }
})

async function decide(approve: boolean) {
  if (!info.value || !reqId.value) return
  busy.value = true
  try {
    if (approve) {
      await terminal.withUnlock(async () => {
        const r = await oauthApi.decision(reqId.value, {
          approve: true,
          nonce: reqId.value,
          terminal_hosts: ['*'],
          terminal_mode_cap: 'approve',
        })
        window.location.href = r.redirect_url
      }, 'OAuth 授权需要解锁终端能力')
    } else {
      const r = await oauthApi.decision(reqId.value, { approve: false, nonce: reqId.value })
      window.location.href = r.redirect_url
    }
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '提交失败'
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="min-h-screen flex items-center justify-center p-6 bg-background">
    <div class="w-full max-w-md rounded-xs border border-border/80 bg-card p-6 space-y-4">
      <h1 class="text-lg font-bold">OAuth 授权确认</h1>
      <p v-if="loading" class="text-sm text-muted-foreground">加载中…</p>
      <p v-else-if="error" class="text-sm text-destructive">{{ error }}</p>
      <template v-else-if="info">
        <p class="text-sm">
          应用 <strong>{{ info.client_name || '未知客户端' }}</strong> 请求访问 ExecMesh（终端执行）。
        </p>
        <p class="text-xs text-muted-foreground">重定向至主机：<span class="font-mono">{{ info.redirect_host }}</span></p>
        <p class="text-xs text-muted-foreground">范围：{{ info.scopes?.join(' ') || 'exec' }}</p>
        <p v-if="info.warn_untrusted" class="text-xs text-amber-600 dark:text-amber-400 border border-amber-500/40 rounded-xs p-2">
          此客户端不在默认信任列表中，请确认你认识该应用与重定向地址。
        </p>
        <p v-if="info.warn_loopback" class="text-xs text-amber-600 dark:text-amber-400 border border-amber-500/40 rounded-xs p-2">
          重定向地址为本机回环地址，通常用于本地开发工具（如 Claude Code）。
        </p>
        <div class="flex gap-2 pt-2">
          <Button data-testid="oauth-consent-approve" class="flex-1" :disabled="busy" @click="decide(true)">批准</Button>
          <Button data-testid="oauth-consent-deny" variant="outline" class="flex-1" :disabled="busy" @click="decide(false)">拒绝</Button>
        </div>
      </template>
    </div>
  </div>
</template>
