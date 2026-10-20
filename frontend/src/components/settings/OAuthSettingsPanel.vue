<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import { oauthApi, type OAuthGrantRow, type OAuthPolicy } from '../../api/client'
import { useTerminalStore } from '../../stores/terminal'
import { allowlistToText, normalizeClientHostAllowlist, parseAllowlistText } from '../../lib/oauthPolicyHosts'
import { Button } from '../ui/button'
import { toast } from 'vue-sonner'

const terminal = useTerminalStore()
const grants = ref<OAuthGrantRow[]>([])
const policy = ref<OAuthPolicy | null>(null)
const allowlistText = ref('')
const allowlistError = ref('')
const loading = ref(true)
const saving = ref(false)

async function reload() {
  loading.value = true
  try {
    const [g, p] = await Promise.all([oauthApi.listGrants(), oauthApi.getPolicy()])
    grants.value = g.grants
    policy.value = p.policy
    allowlistText.value = allowlistToText(p.policy.client_host_allowlist)
    allowlistError.value = ''
  } finally {
    loading.value = false
  }
}

watch(allowlistText, () => {
  if (!policy.value) return
  const { hosts, error } = normalizeClientHostAllowlist(parseAllowlistText(allowlistText.value))
  allowlistError.value = error ?? ''
  if (!error) policy.value.client_host_allowlist = hosts
})

onMounted(reload)

async function revoke(id: number) {
  if (!confirm('撤销后该应用的令牌立即失效，进行中的命令会被结束。确定？')) return
  await oauthApi.revokeGrant(id)
  await reload()
}

async function savePolicy() {
  if (!policy.value) return
  const { hosts, error } = normalizeClientHostAllowlist(parseAllowlistText(allowlistText.value))
  if (error) {
    allowlistError.value = error
    toast.error(error)
    return
  }
  policy.value.client_host_allowlist = hosts
  const draft = policy.value
  saving.value = true
  try {
    // 总开关 / 对外地址 / 注册与受信来源决定公网上谁能申请终端能力，保存前要再次验证密码。
    await terminal.withUnlock(
      () => oauthApi.savePolicy(draft),
      '修改 OAuth 策略会改变公网上谁能向这台 Hub 申请终端能力，需要再次验证密码',
    )
    toast.success('OAuth 策略已保存')
  } catch (e: unknown) {
    const msg = e instanceof Error ? e.message : '保存失败'
    toast.error(msg)
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <div data-testid="oauth-settings-panel" class="p-3.5 rounded-xs border border-border/80 bg-card/40 space-y-4">
    <div>
      <h3 class="font-bold text-sm">OAuth 授权的应用</h3>
      <p class="text-[10px] text-muted-foreground mt-1">通过 ChatGPT / Claude 等连接器授权的应用；令牌为隐藏凭据，不在上方 MCP 令牌列表中显示。</p>
    </div>
    <div v-if="loading" class="text-xs text-muted-foreground">加载中…</div>
    <ul v-else-if="grants.length" class="space-y-2">
      <li v-for="g in grants" :key="g.id" class="flex items-center justify-between gap-2 text-xs border border-border/60 rounded-xs p-2">
        <div class="min-w-0">
          <div class="font-medium truncate">{{ g.client_name }}</div>
          <div class="text-muted-foreground">主机 {{ g.terminal_hosts?.join(', ') || '—' }} · 模式 {{ g.terminal_mode_cap || '跟随主机' }}</div>
          <div v-if="g.revoked" class="text-destructive">已撤销</div>
        </div>
        <Button v-if="!g.revoked" size="sm" variant="outline" class="h-7 text-[10px]" @click="revoke(g.id)">撤销</Button>
      </li>
    </ul>
    <p v-else class="text-xs text-muted-foreground">暂无 OAuth 授权。</p>

    <div v-if="policy" class="border-t border-border/60 pt-4 space-y-2">
      <h3 class="font-bold text-sm">OAuth 策略</h3>
      <label class="flex items-start gap-2 text-xs">
        <input v-model="policy.enabled" type="checkbox" data-testid="oauth-enabled" class="mt-0.5" />
        <span>
          <span class="font-medium">启用 OAuth 授权服务器</span>
          <span class="block text-[10px] text-muted-foreground mt-0.5">公网授权服务器；建议先填写下方对外地址。关闭后所有 /oauth/* 与元数据端点返回 404，已授权的应用也会立即无法访问（授权记录保留，重新开启后恢复；要永久回收请在上方「撤销」）。环境变量 RELAYMESH_OAUTH=off 优先级更高。</span>
        </span>
      </label>
      <div class="space-y-1 text-xs">
        <label class="font-medium" for="oauth-public-url">对外地址（issuer / resource）</label>
        <input
          id="oauth-public-url"
          v-model="policy.public_url"
          data-testid="oauth-public-url"
          type="url"
          placeholder="https://relaymesh.example.com"
          class="w-full h-8 px-2 border rounded-xs bg-background font-mono text-[11px]"
        />
        <p class="text-[10px] text-muted-foreground">须为 https:// 开头的纯源地址（环回可用 http://）；优先于请求头推导。也可设置环境变量 RELAYMESH_PUBLIC_URL。</p>
      </div>
      <label class="flex items-center gap-2 text-xs">
        <input v-model="policy.allow_dcr" type="checkbox" />
        允许动态客户端注册 (DCR)
      </label>
      <label class="flex items-center gap-2 text-xs">
        <input v-model="policy.allow_chatgpt" type="checkbox" />
        信任 ChatGPT 来源 (chatgpt.com)
      </label>
      <label class="flex items-center gap-2 text-xs">
        <input v-model="policy.allow_claude" type="checkbox" />
        信任 Claude 来源 (claude.ai)
      </label>
      <label class="flex items-center gap-2 text-xs">
        <input v-model="policy.allow_loopback" type="checkbox" />
        允许本机回环重定向（Claude Code 等）
      </label>
      <div class="flex gap-2 items-center text-xs">
        <span>访问令牌有效期（分钟）</span>
        <input v-model.number="policy.access_token_ttl_minutes" type="number" min="5" max="1440" class="w-20 h-7 px-1 border rounded-xs bg-background" />
      </div>
      <div class="space-y-1 text-xs">
        <label class="font-medium" for="oauth-client-host-allowlist">自定义信任的客户端域名</label>
        <p class="text-[10px] text-muted-foreground">每行一个主机名（不含 https://、端口或路径）；用于 CIMD 客户端 ID 的域名白名单，最多 32 条。</p>
        <textarea
          id="oauth-client-host-allowlist"
          v-model="allowlistText"
          data-testid="oauth-client-host-allowlist"
          rows="3"
          class="w-full px-2 py-1.5 border rounded-xs bg-background font-mono text-[11px]"
          placeholder="tools.example.com"
        />
        <p v-if="allowlistError" class="text-destructive text-[10px]">{{ allowlistError }}</p>
      </div>
      <Button size="sm" :disabled="saving || !!allowlistError" @click="savePolicy">保存策略</Button>
    </div>
  </div>
</template>
