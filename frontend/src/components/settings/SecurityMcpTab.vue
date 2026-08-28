<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useSettingsStore } from '../../stores/settings'
import { useAuthStore } from '../../stores/auth'
import {
  Shield,
  Key,
  Terminal,
  Copy,
  Check,
  RefreshCw,
  Eye,
  EyeOff,
  Lock,
  Globe,
  HelpCircle,
  FileCode2,
  LogOut,
  ShieldCheck,
  ShieldAlert,
  Unlock,
  AlertTriangle,
  Flame,
  User,
  KeyRound,
  RotateCcw,
  Save,
  CheckCircle2
} from 'lucide-vue-next'
import Button from '../ui/button/Button.vue'
import { toast } from 'vue-sonner'

const settingsStore = useSettingsStore()
const authStore = useAuthStore()

const activeClientType = ref<'cursor' | 'claude' | 'cline'>('cursor')
const customMcpToken = ref('')
const tokenAuthStyle = ref<'url' | 'header'>('url')

// 账号密码修改表单状态
const editUsername = ref('')
const editOldPassword = ref('')
const editNewPassword = ref('')
const editConfirmPassword = ref('')
const showOldPass = ref(false)
const showNewPass = ref(false)
const isUpdatingCredentials = ref(false)
const isResettingCredentials = ref(false)

const currentHost = computed(() => {
  return window.location.hostname || 'localhost'
})

const httpPort = computed(() => {
  return window.location.port || '18775'
})

const mcpHttpUrl = computed(() => {
  const base = `${window.location.protocol}//${currentHost.value}${window.location.port ? ':' + window.location.port : ''}/mcp`
  if (customMcpToken.value.trim()) {
    if (tokenAuthStyle.value === 'url') {
      return `${base}?token=${encodeURIComponent(customMcpToken.value.trim())}`
    }
  }
  return base
})

async function copyText(text: string, label: string) {
  try {
    await navigator.clipboard.writeText(text)
    toast.success(`已复制 ${label}`, { duration: 2000 })
  } catch (_) {
    const input = document.createElement('textarea')
    input.value = text
    document.body.appendChild(input)
    input.select()
    document.execCommand('copy')
    document.body.removeChild(input)
    toast.success(`已复制 ${label}`, { duration: 2000 })
  }
}

// 动态生成符合客户端规范的 MCP 配置文件片段
const clientConfigSnippet = computed(() => {
  const token = customMcpToken.value.trim()
  const isUrlAuth = tokenAuthStyle.value === 'url' || activeClientType.value === 'claude'
  const headers = (!isUrlAuth && token) ? { Authorization: `Bearer ${token}` } : undefined
  const targetUrl = isUrlAuth && token ? `${window.location.protocol}//${currentHost.value}${window.location.port ? ':' + window.location.port : ''}/mcp?token=${encodeURIComponent(token)}` : `${window.location.protocol}//${currentHost.value}${window.location.port ? ':' + window.location.port : ''}/mcp`

  if (activeClientType.value === 'cursor') {
    return JSON.stringify({
      mcpServers: {
        relaymesh: headers ? {
          url: targetUrl,
          headers: headers
        } : {
          url: targetUrl
        }
      }
    }, null, 2)
  } else if (activeClientType.value === 'claude') {
    return JSON.stringify({
      mcpServers: {
        relaymesh: {
          url: targetUrl
        }
      }
    }, null, 2)
  } else {
    return JSON.stringify({
      name: 'RelayMesh MCP Hub',
      type: 'streamable-http',
      url: targetUrl,
      headers: headers
    }, null, 2)
  }
})

function handleLogout() {
  authStore.logout()
  toast.info('已退出登录', { duration: 2000 })
}

function handleUpdateSecurity() {
  settingsStore.updateSettings({
    security: {
      ...settingsStore.settings.security
    }
  })
}

async function handleRefreshBlockedIPs() {
  await settingsStore.fetchBlockedIPs()
  toast.success('已刷新被封禁 IP 列表', { duration: 1500 })
}

async function handleUnblock(ip: string) {
  await settingsStore.unblockIP(ip)
  toast.success(`已解封 IP: ${ip}`, { duration: 2000 })
}

async function handleClearAll() {
  if (confirm('确定要清空所有被封禁的 IP 吗？')) {
    await settingsStore.clearAllBlockedIPs()
    toast.success('已清空所有封禁 IP', { duration: 2000 })
  }
}

async function handleChangeCredentials() {
  if (!editUsername.value.trim()) {
    toast.error('管理账号不能为空')
    return
  }
  if (!editNewPassword.value.trim()) {
    toast.error('新访问密码不能为空')
    return
  }
  if (editNewPassword.value.trim().length < 6) {
    toast.error('新访问密码至少需要 6 位')
    return
  }
  if (editNewPassword.value !== editConfirmPassword.value) {
    toast.error('两次输入的新密码不一致')
    return
  }

  isUpdatingCredentials.value = true
  try {
    const res = await authStore.changeCredentials(
      editUsername.value.trim(),
      editNewPassword.value.trim(),
      editOldPassword.value.trim()
    )
    if (res.success) {
      toast.success(res.message, { duration: 2500 })
      editOldPassword.value = ''
      editNewPassword.value = ''
      editConfirmPassword.value = ''
    } else {
      toast.error(res.message, { duration: 3000 })
    }
  } finally {
    isUpdatingCredentials.value = false
  }
}

async function handleResetToEnv() {
  if (!confirm('确定要将账号与密码重置回环境变量的初始配置吗？数据库保存的自定义凭据将被清除。')) {
    return
  }
  isResettingCredentials.value = true
  try {
    const res = await authStore.resetCredentials()
    if (res.success) {
      toast.success(res.message, { duration: 2500 })
      editUsername.value = authStore.currentUsername
      editOldPassword.value = ''
      editNewPassword.value = ''
      editConfirmPassword.value = ''
    } else {
      toast.error(res.message, { duration: 3000 })
    }
  } finally {
    isResettingCredentials.value = false
  }
}

onMounted(() => {
  editUsername.value = authStore.currentUsername || 'admin'
  if (authStore.isAuthenticated) {
    settingsStore.fetchBlockedIPs()
    authStore.checkAuthStatus()
  }
})
</script>

<template>
  <div class="w-full space-y-4 pb-4">
    <!-- 1. Web 界面访问安全模式 -->
    <div class="p-3.5 rounded-xs border border-border/80 bg-card/40 space-y-3">
      <div class="flex items-center justify-between border-b border-border/60 pb-2.5">
        <div class="flex items-center gap-2">
          <Shield class="w-4 h-4 text-foreground shrink-0" />
          <div>
            <h3 class="font-bold text-xs sm:text-sm text-foreground tracking-tight">Web 界面访问安全认证</h3>
            <p class="text-[10px] text-muted-foreground font-sans mt-0.5">
              单用户免注册极简认证模型：满足局域网及公网云端私有化部署防护
            </p>
          </div>
        </div>
      </div>

      <div class="space-y-2.5 pt-1">
        <div class="p-2.5 rounded-xs border border-border/60 bg-background flex flex-col sm:flex-row sm:items-center justify-between gap-3">
          <div class="space-y-0.5">
            <span class="text-xs font-bold text-foreground">Web 界面访问状态</span>
            <p class="text-[10px] text-muted-foreground font-sans">
              <span v-if="authStore.authRequired">
                已启用访问账号密码强鉴权保护；公网环境下所有 Web API 与 SSE 事件流均需认证。
              </span>
              <span v-else>
                当前处于免密私有直连模式；如需公网暴露请配置环境变量 <code class="text-primary font-mono font-bold">RELAYMESH_WEB_PASSWORD</code> 开启密码鉴权。
              </span>
            </p>
          </div>
          <div class="flex items-center gap-2 shrink-0 self-start sm:self-center">
            <span 
              v-if="authStore.authRequired"
              class="px-2 py-0.5 text-[10px] font-mono rounded-2xs bg-primary/10 text-primary border border-primary/20 flex items-center gap-1"
            >
              <ShieldCheck class="w-3 h-3" />
              <span>密码保护中</span>
            </span>
            <span 
              v-else
              class="px-2 py-0.5 text-[10px] font-mono rounded-2xs bg-emerald-500/10 text-emerald-600 dark:text-emerald-400 border border-emerald-500/20 flex items-center gap-1"
            >
              <span>● 免密私有直连</span>
            </span>

            <Button
              v-if="authStore.authRequired && authStore.isAuthenticated"
              variant="outline"
              size="sm"
              class="h-6 text-[10px] px-2 text-destructive border-destructive/30 hover:bg-destructive/10 cursor-pointer"
              @click="handleLogout"
            >
              <LogOut class="w-2.5 h-2.5 mr-1" />
              <span>退出登录</span>
            </Button>
          </div>
        </div>
      </div>
    </div>

    <!-- 2. 管理账号与访问密码修改 (Credentials Management) -->
    <div v-if="authStore.authRequired" class="p-3.5 rounded-xs border border-border/80 bg-card/40 space-y-3">
      <div class="flex items-center justify-between border-b border-border/60 pb-2.5">
        <div class="flex items-center gap-2">
          <KeyRound class="w-4 h-4 text-primary shrink-0" />
          <div>
            <div class="flex items-center gap-2">
              <h3 class="font-bold text-xs sm:text-sm text-foreground tracking-tight">修改管理账号与访问密码</h3>
              <span 
                v-if="authStore.isCustomized"
                class="px-1.5 py-0.2 text-[9px] font-mono rounded-2xs bg-primary/10 text-primary border border-primary/20"
              >
                已自定义覆盖
              </span>
              <span 
                v-else
                class="px-1.5 py-0.2 text-[9px] font-mono rounded-2xs bg-muted text-muted-foreground border border-border/60"
              >
                环境变量默认
              </span>
            </div>
            <p class="text-[10px] text-muted-foreground font-sans mt-0.5">
              修改后凭据将持久化存入 SQLite 数据库；亦可在服务器执行 <code class="text-foreground font-mono">./relaymesh reset-auth</code> 重置为环境变量
            </p>
          </div>
        </div>

        <Button
          v-if="authStore.isCustomized"
          variant="outline"
          size="sm"
          class="h-6 text-[10px] font-mono px-2 text-muted-foreground hover:text-foreground cursor-pointer border-border/80"
          :disabled="isResettingCredentials"
          @click="handleResetToEnv"
        >
          <RotateCcw class="w-3 h-3 mr-1" />
          <span>重置为环境变量</span>
        </Button>
      </div>

      <div class="grid grid-cols-1 sm:grid-cols-2 gap-3 pt-1">
        <!-- 账号 -->
        <div class="p-2.5 rounded-xs border border-border/60 bg-background space-y-1.5">
          <label class="text-xs font-bold text-foreground flex items-center gap-1.5">
            <User class="w-3.5 h-3.5 text-muted-foreground" />
            <span>登录账号 (Username)</span>
          </label>
          <input
            type="text"
            v-model="editUsername"
            placeholder="输入管理账号..."
            class="w-full h-8 px-2.5 rounded-xs bg-card border border-border/80 focus:border-primary focus:outline-none text-xs text-foreground placeholder:text-muted-foreground/60 transition-colors"
          />
        </div>

        <!-- 当前旧密码 (验证用) -->
        <div class="p-2.5 rounded-xs border border-border/60 bg-background space-y-1.5">
          <label class="text-xs font-bold text-foreground flex items-center justify-between">
            <span class="flex items-center gap-1.5">
              <Lock class="w-3.5 h-3.5 text-muted-foreground" />
              <span>当前原密码 (Old Password)</span>
            </span>
          </label>
          <div class="relative flex items-center">
            <input
              :type="showOldPass ? 'text' : 'password'"
              v-model="editOldPassword"
              placeholder="请输入当前生效的密码以验证身份..."
              class="w-full h-8 px-2.5 pr-8 rounded-xs bg-card border border-border/80 focus:border-primary focus:outline-none text-xs text-foreground placeholder:text-muted-foreground/60 transition-colors"
            />
            <button
              type="button"
              class="absolute right-2 text-muted-foreground hover:text-foreground transition-colors cursor-pointer"
              @click="showOldPass = !showOldPass"
            >
              <EyeOff v-if="showOldPass" class="w-3.5 h-3.5" />
              <Eye v-else class="w-3.5 h-3.5" />
            </button>
          </div>
        </div>

        <!-- 新密码 -->
        <div class="p-2.5 rounded-xs border border-border/60 bg-background space-y-1.5">
          <label class="text-xs font-bold text-foreground flex items-center justify-between">
            <span class="flex items-center gap-1.5">
              <KeyRound class="w-3.5 h-3.5 text-muted-foreground" />
              <span>新访问密码 (New Password)</span>
            </span>
            <span class="text-[10px] text-muted-foreground font-mono">至少 6 位</span>
          </label>
          <div class="relative flex items-center">
            <input
              :type="showNewPass ? 'text' : 'password'"
              v-model="editNewPassword"
              placeholder="输入新访问密码..."
              class="w-full h-8 px-2.5 pr-8 rounded-xs bg-card border border-border/80 focus:border-primary focus:outline-none text-xs text-foreground placeholder:text-muted-foreground/60 transition-colors"
            />
            <button
              type="button"
              class="absolute right-2 text-muted-foreground hover:text-foreground transition-colors cursor-pointer"
              @click="showNewPass = !showNewPass"
            >
              <EyeOff v-if="showNewPass" class="w-3.5 h-3.5" />
              <Eye v-else class="w-3.5 h-3.5" />
            </button>
          </div>
        </div>

        <!-- 确认新密码 -->
        <div class="p-2.5 rounded-xs border border-border/60 bg-background space-y-1.5">
          <label class="text-xs font-bold text-foreground flex items-center gap-1.5">
            <CheckCircle2 class="w-3.5 h-3.5 text-muted-foreground" />
            <span>确认新密码 (Confirm)</span>
          </label>
          <input
            type="password"
            v-model="editConfirmPassword"
            placeholder="再次输入新访问密码..."
            class="w-full h-8 px-2.5 rounded-xs bg-card border border-border/80 focus:border-primary focus:outline-none text-xs text-foreground placeholder:text-muted-foreground/60 transition-colors"
          />
        </div>
      </div>

      <div class="flex justify-end pt-1">
        <Button
          variant="default"
          size="sm"
          class="h-7 text-xs font-medium px-4 cursor-pointer"
          :disabled="isUpdatingCredentials || !editUsername.trim() || !editNewPassword.trim() || !editOldPassword.trim()"
          @click="handleChangeCredentials"
        >
          <Save class="w-3 h-3 mr-1.5" />
          <span>{{ isUpdatingCredentials ? '正在更新...' : '保存新账号与密码' }}</span>
        </Button>
      </div>
    </div>

    <!-- 3. 防暴力破解与 IP 锁定防护 (Brute-force Protection) -->
    <div class="p-3.5 rounded-xs border border-border/80 bg-card/40 space-y-3">
      <div class="flex items-center justify-between border-b border-border/60 pb-2.5">
        <div class="flex items-center gap-2">
          <Flame class="w-4 h-4 text-orange-500 shrink-0" />
          <div>
            <h3 class="font-bold text-xs sm:text-sm text-foreground tracking-tight">反暴力破解与 IP 封禁防护 (Brute-force Shield)</h3>
            <p class="text-[10px] text-muted-foreground font-sans mt-0.5">
              内存级原子限流器：对频繁错误尝试的客户端 IP 施加自动阶梯式安全锁定
            </p>
          </div>
        </div>

        <label class="flex items-center gap-2 cursor-pointer select-none">
          <span class="text-xs font-mono font-medium text-foreground">
            {{ settingsStore.settings.security?.bruteForceProtection ? '已启用防护' : '已关闭' }}
          </span>
          <input
            type="checkbox"
            v-model="settingsStore.settings.security.bruteForceProtection"
            @change="handleUpdateSecurity"
            class="rounded-2xs accent-primary w-4 h-4 cursor-pointer"
          />
        </label>
      </div>

      <div class="space-y-3 pt-1">
        <!-- 阈值调节 -->
        <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
          <div class="p-2.5 rounded-xs border border-border/60 bg-background space-y-1.5">
            <div class="flex items-center justify-between">
              <label class="text-xs font-bold text-foreground">最大失败重试阈值</label>
              <span class="text-xs font-mono font-bold text-primary">{{ settingsStore.settings.security.maxFailedAttempts }} 次</span>
            </div>
            <p class="text-[10px] text-muted-foreground">连续登录失败达到此次数后自动封禁</p>
            <input
              type="range"
              min="3"
              max="20"
              step="1"
              v-model.number="settingsStore.settings.security.maxFailedAttempts"
              @change="handleUpdateSecurity"
              class="w-full accent-primary cursor-pointer h-1.5 bg-muted rounded-full"
            />
          </div>

          <div class="p-2.5 rounded-xs border border-border/60 bg-background space-y-1.5">
            <div class="flex items-center justify-between">
              <label class="text-xs font-bold text-foreground">封禁持续时长 (Lockout)</label>
              <span class="text-xs font-mono font-bold text-primary">{{ settingsStore.settings.security.lockoutMinutes }} 分钟</span>
            </div>
            <p class="text-[10px] text-muted-foreground">锁定期间拒绝该 IP 的一切认证请求</p>
            <input
              type="range"
              min="5"
              max="120"
              step="5"
              v-model.number="settingsStore.settings.security.lockoutMinutes"
              @change="handleUpdateSecurity"
              class="w-full accent-primary cursor-pointer h-1.5 bg-muted rounded-full"
            />
          </div>
        </div>

        <!-- 当前被封禁 IP 列表与解封管理 -->
        <div class="p-2.5 rounded-xs border border-border/60 bg-background space-y-2">
          <div class="flex items-center justify-between">
            <div class="flex items-center gap-1.5">
              <ShieldAlert class="w-3.5 h-3.5 text-destructive" />
              <span class="text-xs font-bold text-foreground">当前锁定 IP 列表</span>
              <span class="text-[10px] font-mono px-1.5 py-0.2 rounded-2xs bg-muted text-muted-foreground">
                {{ settingsStore.blockedIPs.length }} 个 IP
              </span>
            </div>
            <div class="flex items-center gap-2">
              <Button
                variant="ghost"
                size="sm"
                class="h-6 text-[10px] font-mono px-2 text-muted-foreground hover:text-foreground cursor-pointer"
                :disabled="settingsStore.isLoadingBlockedIPs"
                @click="handleRefreshBlockedIPs"
              >
                <RefreshCw class="w-3 h-3 mr-1" :class="{ 'animate-spin': settingsStore.isLoadingBlockedIPs }" />
                <span>刷新</span>
              </Button>
              <Button
                v-if="settingsStore.blockedIPs.length > 0"
                variant="outline"
                size="sm"
                class="h-6 text-[10px] font-mono px-2 text-destructive border-destructive/40 hover:bg-destructive/10 cursor-pointer"
                @click="handleClearAll"
              >
                <span>一键解封全部</span>
              </Button>
            </div>
          </div>

          <!-- 列表内容 -->
          <div v-if="settingsStore.blockedIPs.length > 0" class="divide-y divide-border/60 border border-border/70 rounded-xs bg-card/60 overflow-hidden">
            <div
              v-for="item in settingsStore.blockedIPs"
              :key="item.ip"
              class="p-2 flex items-center justify-between gap-2 text-xs font-mono"
            >
              <div class="flex items-center gap-2 min-w-0">
                <span class="font-bold text-foreground">{{ item.ip }}</span>
                <span class="text-[10px] text-destructive bg-destructive/10 px-1.5 py-0.5 rounded-2xs border border-destructive/20">
                  失败 {{ item.failed_count }} 次
                </span>
                <span class="text-[10px] text-muted-foreground hidden sm:inline">
                  剩余锁定: {{ Math.ceil(item.remaining_seconds / 60) }} 分钟
                </span>
              </div>
              <Button
                variant="outline"
                size="sm"
                class="h-5 text-[10px] px-2 text-foreground hover:bg-muted border-border cursor-pointer shrink-0"
                @click="handleUnblock(item.ip)"
              >
                <Unlock class="w-2.5 h-2.5 mr-1 text-emerald-500" />
                <span>立即解封</span>
              </Button>
            </div>
          </div>
          <div v-else class="text-center py-4 text-[11px] text-muted-foreground font-mono bg-card/30 rounded-xs border border-dashed border-border/60">
            暂无被封禁的 IP，系统运行良好
          </div>
        </div>
      </div>
    </div>

    <!-- 4. MCP 服务端点与协议分流 -->
    <div class="p-3.5 rounded-xs border border-border/80 bg-card/40 space-y-3">
      <div class="flex items-center justify-between border-b border-border/60 pb-2.5">
        <div class="flex items-center gap-2">
          <Key class="w-4 h-4 text-foreground shrink-0" />
          <div>
            <h3 class="font-bold text-xs sm:text-sm text-foreground tracking-tight">MCP 服务端点与凭据安全</h3>
            <p class="text-[10px] text-muted-foreground font-sans mt-0.5">
              为 AI 客户端（Cursor / Claude / Codex）提供标准 Streamable HTTP MCP 协议接口
            </p>
          </div>
        </div>
      </div>

      <div class="space-y-2.5 pt-1">
        <!-- MCP HTTP Endpoint -->
        <div class="p-2.5 rounded-xs border border-border/60 bg-background flex items-center justify-between gap-2">
          <div class="min-w-0 space-y-0.5">
            <span class="text-[10px] text-muted-foreground uppercase font-bold tracking-wider">MCP HTTP 端点 (推荐客户端配置)</span>
            <div class="text-xs font-mono text-foreground truncate select-all">
              {{ mcpHttpUrl }}
            </div>
          </div>
          <Button
            variant="ghost"
            size="sm"
            class="h-7 text-xs font-mono px-2.5 shrink-0 border border-border/80 hover:bg-muted cursor-pointer"
            @click="copyText(mcpHttpUrl, 'MCP HTTP 端点')"
          >
            <Copy class="w-3 h-3 mr-1" />
            <span>复制端点</span>
          </Button>
        </div>

        <!-- MCP Token Optional Input for Snippets -->
        <div class="p-2.5 rounded-xs border border-border/60 bg-background space-y-2">
          <div class="flex items-center justify-between">
            <label class="text-[11px] font-bold text-foreground flex items-center gap-1.5">
              <Lock class="w-3.5 h-3.5 text-muted-foreground" />
              <span>MCP 访问凭据 (Token) 配置助手</span>
            </label>
            <div class="flex items-center gap-3 text-[10px] text-muted-foreground">
              <span>鉴权形式：</span>
              <label class="flex items-center gap-1 cursor-pointer select-none text-foreground font-mono">
                <input type="radio" value="url" v-model="tokenAuthStyle" class="accent-primary" />
                <span>URL 嵌入 (?token=xxx) [推荐·全客户端兼容]</span>
              </label>
              <label class="flex items-center gap-1 cursor-pointer select-none text-muted-foreground hover:text-foreground font-mono">
                <input type="radio" value="header" v-model="tokenAuthStyle" class="accent-primary" />
                <span>HTTP Header (Bearer)</span>
              </label>
            </div>
          </div>
          <div class="flex items-center gap-2">
            <input
              type="text"
              v-model="customMcpToken"
              placeholder="如需生成带 Token 的配置，请在此输入 Token..."
              class="flex-1 h-7 px-2.5 rounded-xs bg-card border border-border/80 focus:border-primary focus:outline-none text-xs text-foreground placeholder:text-muted-foreground/60 transition-colors"
            />
          </div>
        </div>
      </div>
    </div>

    <!-- 5. MCP 客户端配置代码示例生成器 (Client Configuration Snippets) -->
    <div class="p-3.5 rounded-xs border border-border/80 bg-card/40 space-y-3">
      <div class="flex items-center justify-between border-b border-border/60 pb-2.5">
        <div class="flex items-center gap-2">
          <FileCode2 class="w-4 h-4 text-foreground shrink-0" />
          <div>
            <h3 class="font-bold text-xs sm:text-sm text-foreground tracking-tight">MCP 客户端配置片段生成器</h3>
            <p class="text-[10px] text-muted-foreground font-sans mt-0.5">
              一键复制开箱即用的 JSON 配置，快速接入 IDE 或外部代理
            </p>
          </div>
        </div>
      </div>

      <!-- Client Selector Tabs -->
      <div class="flex items-center gap-1.5 pt-1">
        <button
          v-for="cl in [
            { id: 'cursor', label: 'Cursor (.cursor/mcp.json)' },
            { id: 'claude', label: 'Claude Desktop' },
            { id: 'cline', label: 'VS Code Cline / Roo Code' }
          ]"
          :key="cl.id"
          type="button"
          class="px-2.5 py-1 rounded-xs text-[11px] font-mono transition-colors border cursor-pointer select-none"
          :class="activeClientType === cl.id
            ? 'bg-primary text-primary-foreground border-primary font-bold'
            : 'bg-background border-border/80 text-muted-foreground hover:text-foreground'"
          @click="activeClientType = cl.id as any"
        >
          {{ cl.label }}
        </button>
      </div>

      <!-- Code Snippet Area -->
      <div class="relative rounded-xs border border-border/80 bg-background p-3 group">
        <pre class="text-xs font-mono text-foreground overflow-x-auto leading-relaxed">{{ clientConfigSnippet }}</pre>
        <button
          type="button"
          class="absolute top-2.5 right-2.5 p-1.5 rounded-xs border border-border/80 bg-card hover:bg-muted text-foreground transition-colors cursor-pointer shadow-xs flex items-center gap-1 text-[10px] font-mono"
          @click="copyText(clientConfigSnippet, `${activeClientType.toUpperCase()} MCP 配置`)"
          title="复制配置 JSON"
        >
          <Copy class="w-3 h-3" />
          <span>复制配置</span>
        </button>
      </div>
    </div>
  </div>
</template>
