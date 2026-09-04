<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { useCredentialsStore } from '@/stores/credentials'
import type { MCPCredential, MCPPermissions } from '@/api/client'
import {
  Key,
  Plus,
  Pencil,
  Trash2,
  X,
  Check,
  ToggleLeft,
  ToggleRight,
  AlertCircle,
  Copy,
  RefreshCw,
  Eye,
  EyeOff
} from 'lucide-vue-next'

const store = useCredentialsStore()

const isEditing = ref(false)
const editTarget = ref<number | null>(null)
const form = ref({
  name: '',
  host_name: '',
  note: '',
  is_active: true,
  permissions: { feedback: true, sessions: true, system_info: true, skills: true, configure: true, execute: true } as MCPPermissions
})
const formError = ref('')
const revealedToken = ref<string | null>(null)
const copiedId = ref<number | null>(null)

const permLabels: { key: keyof MCPPermissions; label: string; tools: string }[] = [
  { key: 'feedback', label: '沟通反馈', tools: 'interactive_feedback, continue_feedback_session' },
  { key: 'sessions', label: '会话查询', tools: 'list_sessions, get_session_history' },
  { key: 'system_info', label: '系统信息', tools: 'get_system_info' },
  { key: 'skills', label: '规范管理', tools: 'manage_skills' },
  { key: 'configure', label: '任务指挥', tools: 'configure_task' },
  { key: 'execute', label: '任务执行', tools: 'report_progress' },
]

onMounted(() => {
  store.fetchCredentials()
})

function openCreate() {
  editTarget.value = null
  form.value = {
    name: '',
    host_name: '',
    note: '',
    is_active: true,
    permissions: { feedback: true, sessions: true, system_info: true, skills: true, configure: true, execute: true }
  }
  formError.value = ''
  revealedToken.value = null
  isEditing.value = true
}

function openEdit(cred: MCPCredential) {
  editTarget.value = cred.id
  form.value = {
    name: cred.name,
    host_name: cred.host_name || '',
    note: cred.note || '',
    is_active: cred.is_active,
    permissions: { ...cred.permissions }
  }
  formError.value = ''
  revealedToken.value = null
  isEditing.value = true
}

function closeEditor() {
  isEditing.value = false
  editTarget.value = null
  formError.value = ''
  revealedToken.value = null
}

async function handleSave() {
  formError.value = ''
  const { name, host_name, note, is_active, permissions } = form.value
  if (!name.trim()) { formError.value = '凭据名称不能为空'; return }

  try {
    if (editTarget.value !== null) {
      await store.updateCredential(editTarget.value, { name, host_name, note, is_active, permissions })
      closeEditor()
    } else {
      const res = await store.createCredential({ name: name.trim(), host_name, note, is_active, permissions })
      revealedToken.value = res.token
    }
  } catch (e: any) {
    formError.value = e?.response?.data?.error || e.message || '保存失败'
  }
}

async function handleDelete(id: number, name: string) {
  if (!confirm(`确认删除凭据「${name}」？此操作不可撤销。`)) return
  try {
    await store.deleteCredential(id)
  } catch (e: any) {
    alert(e?.response?.data?.error || '删除失败')
  }
}

async function handleToggle(cred: MCPCredential) {
  await store.toggleActive(cred.id, !cred.is_active)
}

async function handleRegenerate(id: number) {
  if (!confirm('确认重新生成 Token？旧 Token 将立即失效，使用旧 Token 的 MCP 客户端需要更新配置。')) return
  try {
    const token = await store.regenerateToken(id)
    revealedToken.value = token
  } catch (e: any) {
    alert(e?.response?.data?.error || '重新生成失败')
  }
}

async function copyToken(token: string, id: number) {
  try {
    await navigator.clipboard.writeText(token)
    copiedId.value = id
    setTimeout(() => { copiedId.value = null }, 2000)
  } catch {
    /* ignore */
  }
}

function permSummary(perms: MCPPermissions): string {
  const all = Object.values(perms).every(v => v)
  if (all) return '全部权限'
  const active = permLabels.filter(p => perms[p.key]).map(p => p.label)
  return active.length ? active.join('、') : '无权限'
}
</script>

<template>
  <div class="space-y-4">
    <!-- Header -->
    <div class="flex items-center justify-between">
      <div class="flex items-center gap-2">
        <Key class="w-3.5 h-3.5 text-foreground" />
        <span class="font-mono font-bold text-xs sm:text-sm text-foreground tracking-tight">MCP 凭据管理</span>
        <span class="text-[10px] text-muted-foreground font-sans">/ Credentials</span>
      </div>
      <button
        v-if="!isEditing"
        type="button"
        class="flex items-center gap-1.5 px-2.5 py-1 rounded-sm text-xs font-mono bg-primary text-primary-foreground hover:bg-primary/90 transition-colors cursor-pointer"
        @click="openCreate"
      >
        <Plus class="w-3 h-3" />
        <span>新建凭据</span>
      </button>
    </div>

    <p class="text-[10px] text-muted-foreground font-sans leading-relaxed">
      为 AI 客户端分发独立 Token，每条凭据可绑定主机名（<code class="text-primary/80">host_name</code>）和细粒度权限。
      Token 格式 <code class="text-primary/80">rm-xxxx</code>，创建后仅显示一次完整值。
      环境变量 Token 保持原有行为，数据库凭据优先匹配。
    </p>

    <!-- Token Reveal Banner -->
    <div v-if="revealedToken && isEditing" class="p-3 rounded-xs border border-primary/40 bg-primary/5 space-y-2">
      <div class="flex items-center gap-2">
        <Eye class="w-3.5 h-3.5 text-primary" />
        <span class="text-xs font-mono font-bold text-primary">Token 已生成 — 请立即复制，关闭后不再显示</span>
      </div>
      <div class="flex items-center gap-2 bg-card p-2 rounded-xs border border-border/80">
        <code class="flex-1 text-xs font-mono text-foreground break-all select-all">{{ revealedToken }}</code>
        <button
          type="button"
          class="shrink-0 p-1.5 rounded-sm hover:bg-muted cursor-pointer"
          title="复制"
          @click="copyToken(revealedToken!, 0)"
        >
          <Copy class="w-3.5 h-3.5" :class="copiedId === 0 ? 'text-primary' : 'text-muted-foreground'" />
        </button>
      </div>
      <button
        type="button"
        class="px-3 py-1.5 rounded-sm text-xs font-mono bg-primary text-primary-foreground hover:bg-primary/90 transition-colors cursor-pointer"
        @click="closeEditor"
      >完成</button>
    </div>

    <!-- Editor Modal -->
    <div v-if="isEditing && !revealedToken" class="p-4 rounded-xs border border-primary/40 bg-card/80 space-y-3">
      <div class="flex items-center justify-between border-b border-border/70 pb-2">
        <span class="font-mono font-bold text-xs text-foreground">{{ editTarget !== null ? '编辑凭据' : '新建凭据' }}</span>
        <button type="button" class="p-1 rounded-sm hover:bg-muted cursor-pointer" @click="closeEditor">
          <X class="w-3.5 h-3.5 text-muted-foreground" />
        </button>
      </div>

      <div v-if="formError" class="flex items-center gap-1.5 text-xs text-destructive font-mono">
        <AlertCircle class="w-3 h-3 shrink-0" />
        {{ formError }}
      </div>

      <div class="grid gap-2.5">
        <div>
          <label class="block text-[10px] text-muted-foreground font-mono mb-1">凭据名称</label>
          <input
            v-model="form.name"
            type="text"
            class="w-full text-xs font-mono p-2 rounded-xs bg-card border border-border/80 focus:border-primary focus:outline-none transition-colors text-foreground placeholder:text-muted-foreground/50"
            placeholder="wsl-cursor"
            maxlength="128"
          />
        </div>
        <div>
          <label class="block text-[10px] text-muted-foreground font-mono mb-1">绑定主机名 <span class="text-muted-foreground/60">(可选，优先级 P2)</span></label>
          <input
            v-model="form.host_name"
            type="text"
            class="w-full text-xs font-mono p-2 rounded-xs bg-card border border-border/80 focus:border-primary focus:outline-none transition-colors text-foreground placeholder:text-muted-foreground/50"
            placeholder="wsl"
            maxlength="128"
          />
        </div>
        <div>
          <label class="block text-[10px] text-muted-foreground font-mono mb-1">备注 <span class="text-muted-foreground/60">(可选)</span></label>
          <input
            v-model="form.note"
            type="text"
            class="w-full text-xs font-mono p-2 rounded-xs bg-card border border-border/80 focus:border-primary focus:outline-none transition-colors text-foreground placeholder:text-muted-foreground/50"
            placeholder="WSL 上的 Cursor IDE"
            maxlength="512"
          />
        </div>

        <!-- Permissions -->
        <div>
          <label class="block text-[10px] text-muted-foreground font-mono mb-2">工具权限</label>
          <div class="grid grid-cols-2 gap-1.5">
            <label
              v-for="p in permLabels"
              :key="p.key"
              class="flex items-start gap-2 p-2 rounded-xs border border-border/60 hover:border-border cursor-pointer transition-colors"
              :class="form.permissions[p.key] ? 'bg-primary/5 border-primary/30' : 'bg-card/40'"
            >
              <input
                v-model="form.permissions[p.key]"
                type="checkbox"
                class="mt-0.5 accent-primary"
              />
              <div>
                <span class="text-xs font-mono font-medium" :class="form.permissions[p.key] ? 'text-foreground' : 'text-muted-foreground'">{{ p.label }}</span>
                <p class="text-[9px] text-muted-foreground/70 font-mono mt-0.5">{{ p.tools }}</p>
              </div>
            </label>
          </div>
        </div>

        <div class="flex items-center gap-2">
          <button
            type="button"
            class="p-0.5 rounded-sm cursor-pointer"
            @click="form.is_active = !form.is_active"
          >
            <component
              :is="form.is_active ? ToggleRight : ToggleLeft"
              class="w-5 h-5 transition-colors"
              :class="form.is_active ? 'text-primary' : 'text-muted-foreground'"
            />
          </button>
          <span class="text-xs font-mono" :class="form.is_active ? 'text-foreground' : 'text-muted-foreground'">
            {{ form.is_active ? '启用' : '已禁用（Token 将被拒绝）' }}
          </span>
        </div>
      </div>

      <div class="flex items-center justify-end gap-2 pt-2 border-t border-border/70">
        <button
          type="button"
          class="px-3 py-1.5 rounded-sm text-xs font-mono text-muted-foreground hover:text-foreground hover:bg-muted transition-colors cursor-pointer"
          @click="closeEditor"
        >取消</button>
        <button
          type="button"
          class="flex items-center gap-1.5 px-3 py-1.5 rounded-sm text-xs font-mono bg-primary text-primary-foreground hover:bg-primary/90 transition-colors cursor-pointer"
          @click="handleSave"
        >
          <Check class="w-3 h-3" />
          <span>{{ editTarget !== null ? '保存' : '创建并生成 Token' }}</span>
        </button>
      </div>
    </div>

    <!-- Credentials List -->
    <div v-if="store.credentials.length === 0 && !isEditing" class="p-6 text-center text-xs text-muted-foreground font-mono border border-dashed border-border/60 rounded-xs">
      暂无凭据，所有 MCP 请求以环境变量 Token 或开放模式运行。点击「新建凭据」添加第一条。
    </div>

    <div v-else class="space-y-2">
      <div
        v-for="cred in store.credentials"
        :key="cred.id"
        class="flex items-start gap-3 p-3 rounded-xs border border-border/80 bg-card/40 hover:bg-card/70 transition-colors group"
      >
        <!-- Toggle -->
        <button
          type="button"
          class="mt-0.5 p-0.5 rounded-sm cursor-pointer shrink-0"
          :title="cred.is_active ? '点击禁用' : '点击启用'"
          @click="handleToggle(cred)"
        >
          <component
            :is="cred.is_active ? ToggleRight : ToggleLeft"
            class="w-5 h-5 transition-colors"
            :class="cred.is_active ? 'text-primary' : 'text-muted-foreground/50'"
          />
        </button>

        <!-- Info -->
        <div class="flex-1 min-w-0">
          <div class="flex items-center gap-2 flex-wrap">
            <span class="font-mono font-bold text-xs text-foreground">{{ cred.name }}</span>
            <span
              v-if="cred.is_env"
              class="text-[9px] px-1.5 py-0.5 rounded-xs bg-primary/10 text-primary border border-primary/20 font-mono font-medium shrink-0"
            >ENV</span>
            <span
              v-if="cred.host_name"
              class="text-[9px] px-1.5 py-0.5 rounded-full bg-blue-500/10 text-blue-400 font-mono font-medium"
            >{{ cred.host_name }}</span>
            <span
              v-if="!cred.is_active"
              class="text-[9px] px-1.5 py-0.5 rounded-full bg-destructive/10 text-destructive font-mono font-medium"
            >disabled</span>
          </div>
          <div class="flex items-center gap-2 mt-1">
            <code class="text-[10px] font-mono text-muted-foreground">{{ cred.token }}</code>
          </div>
          <p class="text-[10px] text-muted-foreground/70 font-mono mt-0.5">{{ permSummary(cred.permissions) }}</p>
          <p v-if="cred.note" class="text-[10px] text-muted-foreground/60 font-sans mt-0.5 truncate">{{ cred.note }}</p>
        </div>

        <!-- Actions -->
        <div class="flex items-center gap-1 opacity-0 group-hover:opacity-100 transition-opacity shrink-0">
          <button
            v-if="!cred.is_env"
            type="button"
            class="p-1.5 rounded-sm hover:bg-muted cursor-pointer"
            title="重新生成 Token"
            @click="handleRegenerate(cred.id)"
          >
            <RefreshCw class="w-3 h-3 text-muted-foreground" />
          </button>
          <button
            type="button"
            class="p-1.5 rounded-sm hover:bg-muted cursor-pointer"
            title="编辑"
            @click="openEdit(cred)"
          >
            <Pencil class="w-3 h-3 text-muted-foreground" />
          </button>
          <button
            v-if="!cred.is_env"
            type="button"
            class="p-1.5 rounded-sm hover:bg-destructive/10 cursor-pointer"
            title="删除"
            @click="handleDelete(cred.id, cred.name)"
          >
            <Trash2 class="w-3 h-3 text-muted-foreground hover:text-destructive" />
          </button>
        </div>
      </div>
    </div>

    <!-- Regenerated Token Reveal -->
    <div v-if="revealedToken && !isEditing" class="p-3 rounded-xs border border-primary/40 bg-primary/5 space-y-2">
      <div class="flex items-center gap-2">
        <Eye class="w-3.5 h-3.5 text-primary" />
        <span class="text-xs font-mono font-bold text-primary">新 Token 已生成 — 请立即复制</span>
      </div>
      <div class="flex items-center gap-2 bg-card p-2 rounded-xs border border-border/80">
        <code class="flex-1 text-xs font-mono text-foreground break-all select-all">{{ revealedToken }}</code>
        <button
          type="button"
          class="shrink-0 p-1.5 rounded-sm hover:bg-muted cursor-pointer"
          @click="copyToken(revealedToken!, -1)"
        >
          <Copy class="w-3.5 h-3.5" :class="copiedId === -1 ? 'text-primary' : 'text-muted-foreground'" />
        </button>
      </div>
      <button
        type="button"
        class="text-xs font-mono text-muted-foreground hover:text-foreground cursor-pointer"
        @click="revealedToken = null"
      >关闭</button>
    </div>
  </div>
</template>
