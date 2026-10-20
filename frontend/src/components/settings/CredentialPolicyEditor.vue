<script setup lang="ts">
// 令牌「权限页」：档位预设、工具权限、终端授权（主机 / 执行模式 / 限额）、访问控制（到期 / 来源）与「将暴露」预览。
// 纯逻辑在 lib/credentialPolicy.ts；这里只负责呈现与事件。
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { AlertTriangle, ChevronDown, ChevronRight, Link2, ShieldAlert } from 'lucide-vue-next'
import { credentialsApi, type CredentialPreview, type MCPPermissions } from '@/api/client'
import { terminalApi, toApiError, type HostView } from '@/api/terminal'
import { hostTitle, shortCode } from '@/lib/terminal/format'
import { supportsModel } from '@/lib/terminal/hosts'
import { defaultSelection } from '@/lib/terminal/modelAccess'
import {
  GOVERNANCE_OPTIONS,
  LAUNCHER_OPTIONS,
  MODE_CAP_OPTIONS,
  TERMINAL_OPTIONS,
  applyCapability,
  applyMode,
  modeOf,
  pathIssues,
  pathIssueText,
  terminalToolNames,
  tierLabel,
  type PolicyForm,
  type PolicyMode
} from '@/lib/credentialPolicy'

const props = defineProps<{
  modelValue: PolicyForm
  /** 编辑已有令牌时传入服务端判定的令牌强度原因；空串或缺省表示合格。 */
  tokenWeakness?: string
  /** 新建凭据：此时才能选择接入方式（请求头令牌 / 能力 URL）；凭据类型创建后不可更改。 */
  isNew?: boolean
}>()
const emit = defineEmits<{ 'update:modelValue': [value: PolicyForm] }>()

const MODES: { value: PolicyMode; title: string; body: string }[] = [
  {
    value: 'relaymesh',
    title: 'RelayMesh',
    body: '完整的 RelayMesh 治理工具（沟通、会话、任务等），不含终端。适合自带执行环境的 IDE 智能体，也是新令牌的默认档位。'
  },
  {
    value: 'execmesh',
    title: 'ExecMesh',
    body: '只开放终端执行工具，且不下发 RelayMesh 治理文本。适合没有执行环境、需要借你的主机执行命令的智能体；全部执行内容都能在 Web 的 ExecMesh 模式里查看。'
  },
  {
    value: 'custom',
    title: '自定义',
    body: '自行组合工具权限。同时带治理与终端工具的令牌，调用终端时必须带 workflow_id 与 description。'
  }
]

const mode = computed(() => modeOf(props.modelValue.permissions))
const terminalEnabled = computed(() => !!props.modelValue.permissions.terminal || !!props.modelValue.permissions.terminal_files)

function patch(partial: Partial<PolicyForm>) {
  emit('update:modelValue', { ...props.modelValue, ...partial })
}

const isCapability = computed(() => !!props.modelValue.capability)
// 接入方式只在新建 ExecMesh 档凭据时出现；已有的能力凭据只读展示说明。
const showAccessBlock = computed(() => !!props.isNew && (mode.value === 'execmesh' || isCapability.value))

function pickAccess(next: 'header' | 'capability') {
  emit('update:modelValue', applyCapability(props.modelValue, next === 'capability'))
}

function pickMode(next: PolicyMode) {
  // 能力 URL 凭据只能是 ExecMesh 档：其它档位在界面上不可选，这里再兜一层。
  if (isCapability.value && next !== 'execmesh') return
  if (next === 'custom') {
    // 「自定义」本身不改权限：保持当前选择，只是让用户清楚可以逐项调整。
    return
  }
  emit('update:modelValue', applyMode(props.modelValue, next))
}

function setPermission(key: keyof MCPPermissions, value: boolean) {
  patch({ permissions: { ...props.modelValue.permissions, [key]: value } })
}

// ───── 主机选择 ─────
const hosts = ref<HostView[]>([])
const hostsError = ref('')
const hostsLoading = ref(false)

async function loadHosts() {
  hostsLoading.value = true
  hostsError.value = ''
  try {
    const res = await terminalApi.hosts()
    // SSH 主机（Hub 直连与经网关）都能授权给模型令牌；只有探测不出 POSIX 环境、只能当人工终端用的那种不出现在这里。
    hosts.value = (res.hosts || []).filter(h => h.status === 'enabled' && supportsModel(h))
  } catch (e) {
    hosts.value = []
    hostsError.value = toApiError(e).message
  } finally {
    hostsLoading.value = false
  }
}

// 「新增模型授权」时按各主机的「新模型默认」预选：新建令牌、或给已有令牌新开终端权限（且名单还是空的、用户没手动选过）。
// 预选的就是勾好的明确条目，管理员能看到、能改；已有终端权限的令牌打开编辑时绝不预选（空名单可能是刻意收紧的结果）。
const hostsTouched = ref(false)
const prefilled = ref(0)

function maybePrefill() {
  const f = props.modelValue
  if (hostsTouched.value || prefilled.value > 0 || isCapability.value || f.allHosts || f.terminalHosts.length > 0) return
  const codes = defaultSelection(hosts.value, false)
  if (codes.length === 0) return
  prefilled.value = codes.length
  patch({ terminalHosts: codes })
}

watch(terminalEnabled, async (enabled, was) => {
  if (!enabled) return
  if (hosts.value.length === 0 && !hostsLoading.value) await loadHosts()
  if (!was) maybePrefill()
})

// ───── 文件能力的服务端开关 ─────
// 文件权限要等服务端开启文件能力（RELAYMESH_TERMINAL_FILES=on）后才生效；权限页如实提示，而不是让人以为已经可用。
const filesServerEnabled = ref<boolean | null>(null)
const filesOn = computed(() => !!props.modelValue.permissions.terminal_files)
// 允许目录逐行即时校验：写了不合法的行，当场在输入框下面指出是哪一行、为什么，而不是等到点保存。
const pathProblems = computed(() => (filesOn.value ? pathIssues(props.modelValue.allowedPathsText).map(pathIssueText) : []))

async function loadFilesStatus() {
  try {
    filesServerEnabled.value = !!(await terminalApi.execStatus()).files_enabled
  } catch {
    filesServerEnabled.value = null
  }
}

watch(filesOn, on => {
  if (on && filesServerEnabled.value === null) void loadFilesStatus()
})

function toggleHost(code: string, checked: boolean) {
  hostsTouched.value = true
  const set = new Set(props.modelValue.terminalHosts)
  if (checked) set.add(code)
  else set.delete(code)
  patch({ terminalHosts: [...set] })
}

// 已保存但当前不在「已启用主机」里的机器码（主机被停用 / 删除 / 终端总闸关闭）：保留显示，避免静默丢失。
const orphanHosts = computed(() => {
  const known = new Set(hosts.value.map(h => h.machine_code))
  return props.modelValue.terminalHosts.filter(code => !known.has(code))
})

// ───── 高级限额折叠 ─────
const showLimits = ref(false)
const hasAnyLimit = computed(() => {
  const f = props.modelValue
  return !!(f.maxConcurrent || f.maxWaitSeconds || f.maxTimeoutSeconds || f.maxOutputBytes || f.allowedDirsText.trim())
})

// ───── 预览 ─────
const preview = ref<CredentialPreview | null>(null)
const previewError = ref('')
let previewTimer: ReturnType<typeof setTimeout> | null = null
let previewSeq = 0

function schedulePreview() {
  if (previewTimer) clearTimeout(previewTimer)
  previewTimer = setTimeout(async () => {
    const seq = ++previewSeq
    try {
      const res = await credentialsApi.preview(props.modelValue.permissions)
      if (seq === previewSeq) {
        preview.value = res
        previewError.value = ''
      }
    } catch (e: any) {
      if (seq === previewSeq) previewError.value = e?.response?.data?.error || e?.message || '预览失败'
    }
  }, 200)
}

watch(() => props.modelValue.permissions, schedulePreview, { deep: true })

onMounted(() => {
  schedulePreview()
  if (terminalEnabled.value) void loadHosts()
  if (filesOn.value) void loadFilesStatus()
})
onBeforeUnmount(() => {
  if (previewTimer) clearTimeout(previewTimer)
})

const instructionsText = computed(() => {
  switch (preview.value?.instructions) {
    case 'governance': return 'RelayMesh 治理指引'
    case 'terminal': return '精简的终端指引（不含 RelayMesh 治理文本）'
    case 'governance+terminal': return 'RelayMesh 治理指引 + 终端章节'
    default: return '无'
  }
})

const labelClass = 'block text-[10px] text-muted-foreground font-mono mb-1'
const inputClass =
  'w-full text-xs font-mono px-2.5 py-1.5 rounded-xs bg-background border border-border/80 focus:border-primary focus:outline-none transition-colors text-foreground placeholder:text-muted-foreground/50'
</script>

<template>
  <div class="space-y-3" data-testid="credential-policy-editor">
    <!-- 档位 -->
    <div>
      <label :class="labelClass">令牌档位</label>
      <div class="grid grid-cols-3 gap-1.5">
        <button
          v-for="m in MODES"
          :key="m.value"
          type="button"
          class="px-2 py-1.5 rounded-xs border text-xs font-mono transition-colors"
          :class="[
            mode === m.value ? 'border-primary bg-primary/10 text-foreground' : 'border-border/60 bg-background text-muted-foreground hover:border-border',
            isCapability && m.value !== 'execmesh' ? 'opacity-40 cursor-not-allowed' : 'cursor-pointer'
          ]"
          :aria-pressed="mode === m.value"
          :disabled="isCapability && m.value !== 'execmesh'"
          :title="isCapability && m.value !== 'execmesh' ? '能力 URL 凭据只能是 ExecMesh 档' : undefined"
          :data-testid="`policy-mode-${m.value}`"
          @click="pickMode(m.value)"
        >{{ m.title }}</button>
      </div>
      <p class="mt-1.5 text-[10px] text-muted-foreground/80 font-sans leading-relaxed">
        {{ MODES.find(m => m.value === mode)?.body }}
      </p>
    </div>

    <!-- 接入方式：新建 ExecMesh 档凭据时可选；能力 URL 把令牌写进专用网址，给只能填一个网址的连接器用 -->
    <div v-if="showAccessBlock" data-testid="access-block">
      <label :class="labelClass">接入方式</label>
      <div class="grid grid-cols-2 gap-1.5">
        <button
          type="button"
          class="px-2 py-1.5 rounded-xs border text-xs font-mono transition-colors cursor-pointer"
          :class="!isCapability ? 'border-primary bg-primary/10 text-foreground' : 'border-border/60 bg-background text-muted-foreground hover:border-border'"
          :aria-pressed="!isCapability"
          data-testid="access-header"
          @click="pickAccess('header')"
        >请求头令牌</button>
        <button
          type="button"
          class="px-2 py-1.5 rounded-xs border text-xs font-mono transition-colors cursor-pointer inline-flex items-center justify-center gap-1"
          :class="isCapability ? 'border-amber-500/60 bg-amber-500/10 text-foreground' : 'border-border/60 bg-background text-muted-foreground hover:border-border'"
          :aria-pressed="isCapability"
          data-testid="access-capability"
          @click="pickAccess('capability')"
        ><Link2 class="w-3 h-3" />能力 URL</button>
      </div>
      <p v-if="!isCapability" class="mt-1.5 text-[10px] text-muted-foreground/80 font-sans leading-relaxed">
        标准方式：客户端在请求头里带 Authorization: Bearer 令牌。大多数 IDE 与智能体用这种方式。
      </p>
      <div v-else class="mt-1.5 p-2 rounded-xs border border-amber-500/40 bg-amber-500/5 space-y-1" data-testid="capability-warning">
        <p class="flex items-start gap-1.5 text-[10px] text-foreground font-sans leading-relaxed">
          <AlertTriangle class="w-3 h-3 shrink-0 mt-0.5 text-amber-500" />
          <span>
            给只能填「一个网址」的连接器（例如不能自定义请求头的聊天类客户端）使用：令牌就写在网址 /mcp/c/… 里，只对这个网址有效，
            也只能做终端执行。
          </span>
        </p>
        <p class="text-[10px] text-amber-600 dark:text-amber-400 font-sans leading-relaxed">
          网址等同口令：它会出现在浏览器历史、反向代理的访问日志和聊天记录里。请只授权必要的主机、保留到期时间、尽量限定来源 IP；
          网址在创建后显示，之后需再次验证 Web 密码才能重新查看；泄露后立即删除或重新生成即可让旧网址失效。
        </p>
      </div>
    </div>
    <div
      v-else-if="isCapability"
      class="p-2 rounded-xs border border-amber-500/40 bg-amber-500/5 text-[10px] text-muted-foreground font-sans leading-relaxed"
      data-testid="capability-readonly-note"
    >
      <span class="inline-flex items-center gap-1 font-mono text-foreground"><Link2 class="w-3 h-3 text-amber-500" />能力 URL 凭据</span>
      ：令牌写在连接网址里（/mcp/c/…），网址在创建或重新生成时显示，之后需再次验证 Web 密码才能重新查看。凭据类型创建后不可更改，权限与主机只能收紧、不能放宽；
      需要新网址请重新生成 Token（旧网址立即失效）。
    </div>

    <!-- 工具权限 -->
    <div>
      <label :class="labelClass">工具权限</label>
      <div class="grid grid-cols-2 gap-1.5">
        <label
          v-for="p in GOVERNANCE_OPTIONS"
          :key="p.key"
          class="flex items-start gap-2 p-2 rounded-xs border transition-colors"
          :class="[
            modelValue.permissions[p.key] ? 'bg-primary/5 border-primary/30' : 'bg-background border-border/60',
            isCapability ? 'opacity-50 cursor-not-allowed' : 'hover:border-border cursor-pointer'
          ]"
        >
          <input
            type="checkbox"
            class="mt-0.5 accent-primary"
            :checked="!!modelValue.permissions[p.key]"
            :disabled="isCapability"
            :data-testid="`perm-${p.key}`"
            @change="setPermission(p.key, ($event.target as HTMLInputElement).checked)"
          />
          <div>
            <span class="text-xs font-mono font-medium" :class="modelValue.permissions[p.key] ? 'text-foreground' : 'text-muted-foreground'">{{ p.label }}</span>
            <p class="text-[9px] text-muted-foreground/70 font-mono mt-0.5">{{ p.tools }}</p>
            <p v-if="p.hint" class="text-[9px] text-muted-foreground/60 font-sans mt-0.5">{{ p.hint }}</p>
          </div>
        </label>
        <label
          v-for="p in TERMINAL_OPTIONS"
          :key="p.key"
          class="flex items-start gap-2 p-2 rounded-xs border transition-colors"
          :class="[
            modelValue.permissions[p.key] ? 'bg-amber-500/5 border-amber-500/40' : 'bg-background border-border/60',
            isCapability ? 'opacity-70 cursor-not-allowed' : 'hover:border-border cursor-pointer'
          ]"
        >
          <input
            type="checkbox"
            class="mt-0.5 accent-primary"
            :checked="!!modelValue.permissions[p.key]"
            :disabled="isCapability"
            :data-testid="`perm-${p.key}`"
            @change="setPermission(p.key, ($event.target as HTMLInputElement).checked)"
          />
          <div>
            <span class="text-xs font-mono font-medium" :class="modelValue.permissions[p.key] ? 'text-foreground' : 'text-muted-foreground'">{{ p.label }}</span>
            <p class="text-[9px] text-muted-foreground/70 font-mono mt-0.5">{{ p.key === 'terminal' ? terminalToolNames(modelValue.permissions).join(', ') : p.tools }}</p>
            <p v-if="p.hint" class="text-[9px] text-amber-500/80 font-sans mt-0.5">{{ p.hint }}</p>
          </div>
        </label>
        <!-- 运行预设（#179）：让项目管理 Agent 唤醒子会话；能力 URL 凭据不能带，所以那种凭据里整行置灰且不可勾选 -->
        <label
          v-for="p in LAUNCHER_OPTIONS"
          :key="p.key"
          class="flex items-start gap-2 p-2 rounded-xs border transition-colors col-span-2"
          :class="[
            modelValue.permissions[p.key] ? 'bg-amber-500/5 border-amber-500/40' : 'bg-background border-border/60',
            isCapability ? 'opacity-50 cursor-not-allowed' : 'hover:border-border cursor-pointer'
          ]"
        >
          <input
            type="checkbox"
            class="mt-0.5 accent-primary"
            :checked="!!modelValue.permissions[p.key]"
            :disabled="isCapability"
            :data-testid="`perm-${p.key}`"
            @change="setPermission(p.key, ($event.target as HTMLInputElement).checked)"
          />
          <div>
            <span class="text-xs font-mono font-medium" :class="modelValue.permissions[p.key] ? 'text-foreground' : 'text-muted-foreground'">{{ p.label }}</span>
            <p class="text-[9px] text-muted-foreground/70 font-mono mt-0.5">{{ p.tools }}</p>
            <p v-if="p.hint" class="text-[9px] text-amber-500/80 font-sans mt-0.5">{{ p.hint }}</p>
          </div>
        </label>
      </div>
      <!-- 弱令牌：「运行预设」与终端权限同样要求令牌不可猜（服务端强制）；终端权限块里已有同样的提示时不重复 -->
      <div
        v-if="modelValue.permissions.launcher && tokenWeakness && !terminalEnabled"
        class="mt-1.5 flex items-start gap-1.5 text-[10px] text-destructive font-mono"
        data-testid="weak-token-warning-launcher"
      >
        <AlertTriangle class="w-3 h-3 shrink-0 mt-0.5" />
        <span>当前令牌强度不足（{{ tokenWeakness }}）。「运行预设」能让这个智能体唤醒项目里别的智能体，请先重新生成令牌，再开启。</span>
      </div>
    </div>

    <!-- 终端授权 -->
    <div v-if="terminalEnabled" class="p-3 rounded-xs border border-amber-500/30 bg-amber-500/5 space-y-3" data-testid="terminal-block">
      <div class="flex items-center gap-1.5 text-xs font-mono font-bold text-foreground">
        <ShieldAlert class="w-3.5 h-3.5 text-amber-500" />
        终端授权
      </div>

      <div v-if="tokenWeakness" class="flex items-start gap-1.5 text-[10px] text-destructive font-mono" data-testid="weak-token-warning">
        <AlertTriangle class="w-3 h-3 shrink-0 mt-0.5" />
        <span>当前令牌强度不足（{{ tokenWeakness }}）。终端权限等同远程执行，请先重新生成令牌，再开启终端权限。</span>
      </div>

      <label
        v-if="modelValue.permissions.terminal"
        class="flex items-start gap-2 p-2 rounded-xs border cursor-pointer transition-colors"
        :class="modelValue.permissions.terminal_compat ? 'bg-primary/5 border-primary/30' : 'bg-background border-border/60 hover:border-border'"
        data-testid="terminal-compat-row"
      >
        <input
          type="checkbox"
          class="mt-0.5 accent-primary"
          :checked="!!modelValue.permissions.terminal_compat"
          data-testid="terminal-compat"
          @change="setPermission('terminal_compat', ($event.target as HTMLInputElement).checked)"
        />
        <div>
          <span class="text-xs font-mono font-medium text-foreground">使用 ExecMesh 兼容工具名</span>
          <p class="text-[10px] text-muted-foreground font-sans mt-0.5">
            让既有 ExecMesh 客户端与提示词几乎不改就能迁移：工具名换成 targets_list / exec / exec_start / job_status 等。
            只改名字与返回形状，不增加任何能力；开启后原生的 terminal_* 名字不再可用，客户端需要重新连接。
          </p>
        </div>
      </label>

      <div>
        <label :class="labelClass">可用主机</label>
        <label class="flex items-center gap-2 p-1.5 rounded-xs hover:bg-card/60" :class="isCapability ? 'opacity-50 cursor-not-allowed' : 'cursor-pointer'">
          <input
            type="checkbox"
            class="accent-primary"
            :checked="modelValue.allHosts"
            :disabled="isCapability"
            data-testid="hosts-all"
            @change="hostsTouched = true; patch({ allHosts: ($event.target as HTMLInputElement).checked })"
          />
          <span class="text-xs font-mono text-foreground">全部主机</span>
          <span v-if="isCapability" class="text-[10px] text-amber-600 dark:text-amber-400 font-sans">（能力 URL 凭据必须逐台选择主机）</span>
          <span v-else class="text-[10px] text-muted-foreground font-sans">（含以后新启用的主机）</span>
        </label>
        <div v-if="!modelValue.allHosts" class="mt-1 space-y-0.5">
          <p v-if="prefilled > 0 && !hostsTouched" class="text-[10px] text-primary font-sans leading-relaxed" data-testid="hosts-prefilled">
            已按主机的「新模型默认」预选了 {{ prefilled }} 台，可以自行调整；这只是预选，保存后就是这个令牌自己的授权名单。
          </p>
          <p v-if="hostsLoading" class="text-[10px] text-muted-foreground font-mono">正在读取主机列表…</p>
          <p v-else-if="hostsError" class="text-[10px] text-muted-foreground font-sans">
            无法读取主机列表：{{ hostsError }}。终端总闸未开启时只能选择「全部主机」。
          </p>
          <p v-else-if="hosts.length === 0 && orphanHosts.length === 0" class="text-[10px] text-muted-foreground font-sans">
            还没有已启用的主机。先在终端页发现并启用主机，或选择「全部主机」。
          </p>
          <label
            v-for="h in hosts"
            :key="h.machine_code"
            class="flex items-center gap-2 p-1.5 rounded-xs cursor-pointer hover:bg-card/60"
          >
            <input
              type="checkbox"
              class="accent-primary"
              :checked="modelValue.terminalHosts.includes(h.machine_code)"
              :data-testid="`host-${h.machine_code}`"
              @change="toggleHost(h.machine_code, ($event.target as HTMLInputElement).checked)"
            />
            <span class="text-xs font-mono text-foreground">{{ hostTitle(h) }}</span>
            <span class="text-[10px] font-mono text-muted-foreground">{{ shortCode(h.machine_code) }}</span>
            <span v-if="h.kind === 'ssh'" class="text-[9px] px-1.5 rounded-full font-mono bg-violet-500/10 text-violet-500" title="通过 SSH 连接（Hub 直连或经网关）：没有 Agent，模型获得授权后可以执行命令，没有文件传输">SSH</span>
            <span class="text-[9px] px-1.5 rounded-full font-mono" :class="h.online ? 'bg-emerald-500/10 text-emerald-500' : 'bg-muted text-muted-foreground'">{{ h.online ? '在线' : '离线' }}</span>
            <span
              v-if="h.allow_model === false"
              class="text-[9px] px-1.5 rounded-full font-mono bg-amber-500/10 text-amber-600 dark:text-amber-400"
              title="这台主机还没有在「主机」页打开「允许模型访问」：令牌授权了它，模型也暂时用不了"
            >未开放给模型</span>
          </label>
          <label
            v-for="code in orphanHosts"
            :key="code"
            class="flex items-center gap-2 p-1.5 rounded-xs cursor-pointer hover:bg-card/60"
          >
            <input type="checkbox" class="accent-primary" checked @change="toggleHost(code, ($event.target as HTMLInputElement).checked)" />
            <span class="text-xs font-mono text-muted-foreground">{{ shortCode(code) }}</span>
            <span class="text-[9px] px-1.5 rounded-full font-mono bg-muted text-muted-foreground">未启用 / 未知</span>
          </label>
        </div>
      </div>

      <!-- 文件允许访问的目录：带文件权限时必填，没有「默认全部可访问」；唯一的例外是下面明确的高风险开关 -->
      <div v-if="filesOn" data-testid="files-block">
        <label :class="labelClass" data-testid="allowed-paths-label">
          {{ modelValue.allowWholeFs ? '文件允许访问的目录（开启「允许整个文件系统」期间不生效，原样保留）' : '文件允许访问的目录（必填，每行一个）' }}
        </label>
        <textarea
          :class="[inputClass, pathProblems.length > 0 && 'border-destructive focus:border-destructive', modelValue.allowWholeFs && 'opacity-60']"
          rows="3"
          placeholder="/srv/data&#10;~/work&#10;C:\Users\me\projects"
          :value="modelValue.allowedPathsText"
          :aria-invalid="pathProblems.length > 0"
          aria-describedby="allowed-paths-problems"
          data-testid="allowed-paths"
          @input="patch({ allowedPathsText: ($event.target as HTMLTextAreaElement).value })"
        />
        <ul
          v-if="pathProblems.length > 0"
          id="allowed-paths-problems"
          class="mt-1 space-y-0.5 text-[11px] text-destructive font-mono leading-snug"
          role="alert"
          data-testid="allowed-paths-problems"
        >
          <li v-for="p in pathProblems" :key="p" class="flex items-start gap-1.5">
            <AlertTriangle class="w-3 h-3 shrink-0 mt-0.5" />
            <span class="min-w-0 break-words">{{ p }}</span>
          </li>
        </ul>
        <p class="mt-1 text-[10px] text-muted-foreground/80 font-sans leading-relaxed">
          模型只能在这些目录之内查看、拉取和写入文件。写法：<span class="font-mono">/绝对路径</span>、<span class="font-mono">~/家目录下的路径</span>、
          <span class="font-mono">C:\盘符路径</span>；不接受根目录、相对路径和 ..（防止手误）。符号链接不会被跟随到目录之外，越界的访问会被拒绝并记入审计。
          确实需要整个文件系统时，不要在这里填 <span class="font-mono">/</span>，请使用下面专门的开关。
        </p>
        <p class="mt-1 text-[10px] text-muted-foreground/80 font-sans leading-relaxed">
          文件传输（拉取下载、经网址写入）要求主机与令牌的执行模式都是「自动」；查看与校验不受模式影响。
        </p>

        <!-- 高风险开关：允许整个文件系统。默认关闭；开启后的保存必须刚刚重新输入过登录密码（服务端强制） -->
        <div
          class="mt-2 rounded-xs border p-2.5"
          :class="modelValue.allowWholeFs ? 'border-destructive/60 bg-destructive/5' : 'border-border/60 bg-background'"
          data-testid="whole-fs-block"
        >
          <label class="flex items-start gap-2 cursor-pointer">
            <input
              type="checkbox"
              class="mt-0.5 accent-destructive"
              :checked="modelValue.allowWholeFs"
              data-testid="whole-fs-switch"
              @change="patch({ allowWholeFs: ($event.target as HTMLInputElement).checked })"
            />
            <div class="min-w-0">
              <span class="flex items-center gap-1 text-xs font-medium text-destructive">
                <ShieldAlert class="w-3.5 h-3.5 shrink-0" />允许整个文件系统（高风险，默认关闭）
              </span>
              <p class="mt-0.5 text-[10px] text-muted-foreground/80 font-sans leading-relaxed">
                不再限制在上面的目录里，模型可以读写这台主机上任何位置的文件。只在确实需要、并且信任这把令牌的使用者时才打开。
              </p>
            </div>
          </label>
          <ul
            v-if="modelValue.allowWholeFs"
            class="mt-2 space-y-1 pl-5 list-disc text-[10px] text-destructive font-sans leading-relaxed marker:text-destructive/60"
            role="alert"
            data-testid="whole-fs-warning"
          >
            <li>能读取并写入 Agent 所在账户有权访问的<strong>一切文件</strong>：系统配置、SSH 私钥、其他应用的数据与凭据都在其中；Windows 上包括所有盘符。</li>
            <li>保存时需要<strong>重新输入一次登录密码</strong>（即使已经解锁）；开启与关闭、以及之后的每一次文件操作（标注 scope=whole_fs）都会写入审计。</li>
            <li>主机上的 Agent 需要升级到支持该功能的版本；旧版本会明确拒绝，不会悄悄退回到目录限制。</li>
            <li>随时可以关闭：关闭后立即回到上面的目录范围（目录设置会保留），尚未使用的下载地址随之作废。</li>
          </ul>
        </div>

        <p
          v-if="filesServerEnabled === false"
          class="mt-1 flex items-start gap-1.5 text-[10px] text-amber-600 dark:text-amber-400 font-sans leading-relaxed"
          data-testid="files-server-off"
        >
          <AlertTriangle class="w-3 h-3 shrink-0 mt-0.5" />
          <span>服务端还没有开启文件能力（需要运维设置 RELAYMESH_TERMINAL_FILES=on，并同时开启 RELAYMESH_TERMINAL_EXEC=on）：现在保存的权限要等开启后才会生效。</span>
        </p>
      </div>

      <div>
        <label :class="labelClass">执行模式上限</label>
        <div class="grid grid-cols-2 gap-1.5">
          <label
            v-for="o in MODE_CAP_OPTIONS"
            :key="o.value || 'follow'"
            class="flex items-start gap-2 p-2 rounded-xs border cursor-pointer transition-colors"
            :class="modelValue.modeCap === o.value ? 'bg-primary/5 border-primary/30' : 'bg-background border-border/60 hover:border-border'"
          >
            <input
              type="radio"
              class="mt-0.5 accent-primary"
              :checked="modelValue.modeCap === o.value"
              :data-testid="`modecap-${o.value || 'follow'}`"
              @change="patch({ modeCap: o.value })"
            />
            <div>
              <span class="text-xs font-mono font-medium text-foreground">{{ o.label }}</span>
              <p class="text-[9px] text-muted-foreground/70 font-sans mt-0.5">{{ o.hint }}</p>
            </div>
          </label>
        </div>
        <p class="mt-1 text-[10px] text-muted-foreground/70 font-sans">令牌的上限只会让执行更严格，不会越过主机自己的设置。</p>
      </div>

      <div>
        <button
          type="button"
          class="flex items-center gap-1 text-[10px] font-mono text-muted-foreground hover:text-foreground cursor-pointer"
          data-testid="limits-toggle"
          @click="showLimits = !showLimits"
        >
          <component :is="showLimits ? ChevronDown : ChevronRight" class="w-3 h-3" />
          高级限额（默认不限制）
          <span v-if="hasAnyLimit" class="px-1 rounded-full bg-primary/10 text-primary text-[9px]">已设置</span>
        </button>
        <div v-if="showLimits" class="mt-2 grid grid-cols-2 gap-2">
          <div>
            <label :class="labelClass">最大并发</label>
            <input :class="inputClass" inputmode="numeric" placeholder="不限" :value="modelValue.maxConcurrent" @input="patch({ maxConcurrent: ($event.target as HTMLInputElement).value })" />
          </div>
          <div>
            <label :class="labelClass">单次等待上限（秒）</label>
            <input :class="inputClass" inputmode="numeric" placeholder="不限" :value="modelValue.maxWaitSeconds" @input="patch({ maxWaitSeconds: ($event.target as HTMLInputElement).value })" />
          </div>
          <div>
            <label :class="labelClass">单次运行超时上限（秒）</label>
            <input :class="inputClass" inputmode="numeric" placeholder="不限" :value="modelValue.maxTimeoutSeconds" @input="patch({ maxTimeoutSeconds: ($event.target as HTMLInputElement).value })" />
          </div>
          <div>
            <label :class="labelClass">单次输出上限（字节）</label>
            <input :class="inputClass" inputmode="numeric" placeholder="不限" :value="modelValue.maxOutputBytes" @input="patch({ maxOutputBytes: ($event.target as HTMLInputElement).value })" />
          </div>
          <div class="col-span-2">
            <label :class="labelClass">允许的工作目录（每行一个，按前缀匹配；留空表示不限）</label>
            <textarea :class="inputClass" rows="2" placeholder="/srv/app" :value="modelValue.allowedDirsText" @input="patch({ allowedDirsText: ($event.target as HTMLTextAreaElement).value })" />
          </div>
        </div>
      </div>
    </div>

    <!-- 访问控制 -->
    <div class="grid grid-cols-2 gap-2.5">
      <div>
        <label :class="labelClass">
          到期时间
          <span v-if="isCapability" class="text-amber-600 dark:text-amber-400">(能力 URL 建议保留，默认 30 天)</span>
          <span v-else class="text-muted-foreground/60">(可选，留空永不过期)</span>
        </label>
        <div class="flex items-center gap-1.5">
          <input
            type="datetime-local"
            :class="inputClass"
            :value="modelValue.expiresAtLocal"
            data-testid="expires-at"
            @input="patch({ expiresAtLocal: ($event.target as HTMLInputElement).value })"
          />
          <button
            v-if="modelValue.expiresAtLocal"
            type="button"
            class="shrink-0 px-2 py-1 rounded-sm text-[10px] font-mono text-muted-foreground hover:text-foreground hover:bg-muted cursor-pointer"
            @click="patch({ expiresAtLocal: '' })"
          >清除</button>
        </div>
      </div>
      <div>
        <label :class="labelClass">来源白名单 <span class="text-muted-foreground/60">(可选，每行一个 CIDR 或 IP)</span></label>
        <textarea
          :class="inputClass"
          rows="2"
          placeholder="192.168.0.0/16"
          :value="modelValue.allowedCidrsText"
          data-testid="allowed-cidrs"
          @input="patch({ allowedCidrsText: ($event.target as HTMLTextAreaElement).value })"
        />
      </div>
    </div>

    <!-- 预览 -->
    <div class="p-2.5 rounded-xs border border-border/60 bg-background" data-testid="policy-preview">
      <div class="flex items-center gap-2 text-[10px] font-mono text-muted-foreground">
        <span>将暴露的工具</span>
        <span v-if="preview" class="px-1.5 py-0.5 rounded-xs bg-primary/10 text-primary">{{ tierLabel(preview.tier) }}</span>
      </div>
      <p v-if="previewError" class="mt-1 text-[10px] text-destructive font-mono">{{ previewError }}</p>
      <div v-else-if="preview && preview.tools.length" class="mt-1.5 flex flex-wrap gap-1">
        <span
          v-for="t in preview.tools"
          :key="t.name"
          class="text-[10px] px-1.5 py-0.5 rounded-xs bg-muted text-foreground font-mono"
          :title="t.summary"
        >{{ t.name }}</span>
      </div>
      <p v-else-if="preview" class="mt-1 text-[10px] text-muted-foreground font-sans">没有任何工具。</p>
      <p v-if="preview" class="mt-1.5 text-[10px] text-muted-foreground/80 font-sans">
        连接时下发：{{ instructionsText }}
        <template v-if="preview.terminal_requires_workflow">；调用终端时必须带 workflow_id 与 description。</template>
        <template v-if="preview.terminal_compat">；终端工具使用 ExecMesh 兼容工具名。</template>
      </p>
    </div>
  </div>
</template>
