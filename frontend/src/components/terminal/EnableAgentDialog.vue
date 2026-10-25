<script setup lang="ts">
// 会话页「启用 Agent」弹窗（#176 第三期）：给一个「待接入」的网页会话，在某台主机的某个目录里无人值守地新开一个 Cursor CLI 对话。
//
// 它不是一条普通的运行：它会让主机在没有人看着的情况下运行一个有模型用量、可能改文件的智能体，所以弹窗把「会发生什么」摆在点之前：
//   - 在哪台主机、哪个目录、什么权限姿态（默认放开全部命令，红字写明后果；可以改成白名单）、最长跑多久（默认 24 小时）、要不要异常自动续跑（默认关）；
//   - 服务端按真正提交时同一份检查算出来的只读预览：将运行的命令、不能启用的原因（主机离线、已有智能体在运行、上一次还没结束……）、需要提醒的前提；
//   - 点「启用并运行」要刚刚解锁（它会让主机运行命令）；提交期间按钮灰掉、不能重复点；失败时原因就地显示，不会留下半成品预设。
// 防重复开多个智能体的闸门都在服务端（待接入才能启用、已有智能体在运行 / 上一次没结束就拒绝、一个会话只有一条预设）；这里只负责让人看懂。
import { computed, nextTick, onBeforeUnmount, reactive, ref, watch } from 'vue'
import { AlertTriangle, ChevronDown, ChevronRight, Loader2, Rocket } from 'lucide-vue-next'
import { Dialog, DialogContent, DialogDescription, DialogTitle } from '@/components/ui/dialog'
import Button from '@/components/ui/button/Button.vue'
import { toast } from '@/components/ui/sonner'
import { UnlockCancelled, terminalApi, toApiError, type SessionEnablePreview } from '@/api/terminal'
import { useTerminalStore } from '@/stores/terminal'
import { RUN_LIMIT_UNITS, charCount, collapseSpaces, dirPlaceholder, isWindowsHost, launcherLimits } from '@/lib/terminal/launcher'
import { hostTitle } from '@/lib/terminal/format'
import {
  blankEnableDraft,
  enableHostChoices,
  enableNotice,
  enableSummary,
  hasEnableErrors,
  loadEnableMemory,
  pickEnableDir,
  pickEnableHost,
  postureWarning,
  previousRunNote,
  saveEnableMemory,
  toEnableRequest,
  validateEnableDraft,
  type EnableDraft,
  type EnableMemory,
} from '@/lib/terminal/sessionEnable'

const props = defineProps<{ workflowId: string }>()
const open = defineModel<boolean>('open', { required: true })
const emit = defineEmits<{ (e: 'enabled'): void }>()

const terminal = useTerminalStore()
const limits = computed(() => launcherLimits(terminal.prefs?.limits))

// ---- 草稿与记住的上一次填写 ----
function storage(): Storage | null {
  try {
    return typeof localStorage === 'undefined' ? null : localStorage
  } catch {
    return null // 隐私模式 / 被禁用：当作没有
  }
}
let memory: EnableMemory = {}
const draft = reactive<EnableDraft>(blankEnableDraft(null))

type Field = 'host' | 'dir' | 'model' | 'maxRun' | 'bin' | 'prompt'
const touched = reactive<Record<Field, boolean>>({ host: false, dir: false, model: false, maxRun: false, bin: false, prompt: false })
const submitted = ref(false)
const submitting = ref(false)
const submitError = ref('')
const commandOpen = ref(false)
const advancedOpen = ref(false)
// 用户有没有亲手改过目录输入框（只看 input 事件，程序填的默认值不算）：没改过时，会话绑定的项目目录优先于「上一次记住的」目录。
const dirEdited = ref(false)

// ---- 只读预览（服务端按真正提交时同一份检查算好） ----
const preview = ref<SessionEnablePreview | null>(null)
const previewFailed = ref('')
const previewLoading = ref(false)
const unsupported = ref(false)
let seq = 0
let timer: ReturnType<typeof setTimeout> | undefined

async function refreshPreview() {
  const n = ++seq
  previewLoading.value = true
  try {
    const r = await terminalApi.sessionEnablePreview(toEnableRequest(props.workflowId, draft))
    if (n !== seq) return // 又改了选项：这次的结果已经过期
    preview.value = r.preview
    previewFailed.value = ''
    // 主机：没选、或选的已经不在列表里（被删除）时给个默认；用户自己挑的主机不动（pickEnableHost 在当前值还在列表里时原样返回它）
    const pick = pickEnableHost(draft.host, memory.host, r.preview.host, r.preview.hosts)
    if (pick !== draft.host) draft.host = pick
    // 目录：会话在网页里绑定了项目目录时默认用它（智能体第一次汇报必须带这个目录，D-161）；用户亲手填的不动
    const dir = pickEnableDir(draft.dir, dirEdited.value, r.preview.bound_dir)
    if (dir !== draft.dir) draft.dir = dir
  } catch (e) {
    if (n !== seq) return
    const err = toApiError(e)
    if (err.status === 404) unsupported.value = true // 旧版 Hub 没有这个接口
    else previewFailed.value = err.message
  } finally {
    if (n === seq) previewLoading.value = false
  }
}

function schedulePreview(delay = 350) {
  if (timer) clearTimeout(timer)
  timer = setTimeout(() => void refreshPreview(), delay)
}

function reset() {
  memory = loadEnableMemory(storage())
  Object.assign(draft, blankEnableDraft(memory))
  for (const f of Object.keys(touched) as Field[]) touched[f] = false
  dirEdited.value = false
  submitted.value = false
  submitting.value = false
  submitError.value = ''
  commandOpen.value = false
  advancedOpen.value = false
  preview.value = null
  previewFailed.value = ''
  unsupported.value = false
  void refreshPreview()
}

watch(
  open,
  v => {
    if (v) reset()
    else {
      seq++ // 关了之后到的预览结果丢掉
      if (timer) clearTimeout(timer)
    }
  },
  { immediate: true },
)
onBeforeUnmount(() => {
  seq++
  if (timer) clearTimeout(timer)
})

// 选项一变就重新算预览（防抖）：主机、目录、权限、时限、自动续跑、模型、可执行文件、提示词都会改变命令或前置条件
watch(
  () => [draft.host, draft.dir, draft.model, draft.posture, draft.maxRun, draft.maxRunUnit, draft.autoResume, draft.bin, draft.prompt] as const,
  () => {
    if (open.value) schedulePreview()
  },
)

// ---- 校验与显示 ----
const hosts = computed(() => preview.value?.hosts ?? [])
const choices = computed(() => enableHostChoices(hosts.value))
// 所选主机的系统：目录示例与提示跟着它走（Windows 主机写盘符路径，#178）；还没选主机时按 Linux 的写法。
const chosenOs = computed(() => hosts.value.find(h => h.machine_code === draft.host)?.os)
const chosenIsWindows = computed(() => isWindowsHost(chosenOs.value))
const errors = computed(() => validateEnableDraft(props.workflowId, draft, limits.value, hosts.value))
const hasErrors = computed(() => hasEnableErrors(errors.value))
const shown = (f: Field) => (touched[f] || submitted.value ? errors.value[f] : undefined)
const border = (f: Field) => (shown(f) ? 'border-destructive' : 'border-border/80')
const fieldClass =
  'w-full rounded-xs border bg-background px-2 text-xs text-foreground placeholder:text-muted-foreground focus:border-primary focus:outline-none focus:ring-1 focus:ring-primary/30 disabled:opacity-60'

const notPending = computed(() => !!preview.value && !preview.value.pending)
const chosenHost = computed(() => hosts.value.find(h => h.machine_code === draft.host))
const hostLabel = computed(() => (chosenHost.value ? hostTitle(chosenHost.value) : ''))
const summary = computed(() => enableSummary(draft, hostLabel.value))
const risk = computed(() => postureWarning(draft.posture))
const previous = computed(() => previousRunNote(preview.value))
const warnings = computed(() => preview.value?.warnings ?? [])
// 选项本身有错时，错误已经写在各个字段下面；服务端的原因只在选项都合法之后才有意义（主机离线、已有智能体在运行……）
const issues = computed(() => (hasErrors.value ? [] : (preview.value?.issues ?? [])))
const promptCount = computed(() => charCount(collapseSpaces(draft.prompt)))
const maxRunCap = computed(() => (limits.value.runMaxMinutes > 0 ? '平台上限 7 天' : '平台上限'))

// 点「启用并运行」之前要同时满足：预览已经到了、没在提交；选项有错时允许点（点了会把错误亮出来）；选项没错就要服务端说能运行
const canSubmit = computed(() => !submitting.value && !!preview.value && !notPending.value && (hasErrors.value || preview.value.can_run))

const dirInput = ref<HTMLInputElement | null>(null)
const hostSelect = ref<HTMLSelectElement | null>(null)
const modelInput = ref<HTMLInputElement | null>(null)
const maxRunInput = ref<HTMLInputElement | null>(null)
const binInput = ref<HTMLInputElement | null>(null)
const promptInput = ref<HTMLTextAreaElement | null>(null)

async function focusFirstError() {
  const order: Array<[Field, () => HTMLElement | null]> = [
    ['host', () => hostSelect.value],
    ['dir', () => dirInput.value],
    ['model', () => modelInput.value],
    ['maxRun', () => maxRunInput.value],
    ['bin', () => binInput.value],
    ['prompt', () => promptInput.value],
  ]
  const first = order.find(([f]) => errors.value[f])
  if (first && (first[0] === 'bin' || first[0] === 'prompt')) advancedOpen.value = true
  await nextTick()
  first?.[1]()?.focus()
}

async function submit() {
  if (submitting.value) return
  submitted.value = true
  if (hasErrors.value) {
    void focusFirstError()
    return
  }
  if (!preview.value?.can_run) return
  submitting.value = true
  submitError.value = ''
  try {
    const r = await terminal.enableSessionAgent(toEnableRequest(props.workflowId, draft))
    saveEnableMemory(storage(), draft)
    const n = enableNotice(r)
    toast.success(n.title, { description: n.description, duration: r.run.warnings?.length ? 15_000 : 10_000 })
    emit('enabled')
    open.value = false
  } catch (e) {
    // 取消解锁：什么都没发生，留在弹窗里；其余失败（主机离线、已有智能体在运行、上一次没结束……）把服务端的原因就地写出来，并把预览读新
    if (!(e instanceof UnlockCancelled)) submitError.value = toApiError(e).message
    void refreshPreview()
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="max-w-lg gap-3 p-5 [&>*]:min-w-0" data-testid="enable-agent-dialog" :data-submitting="submitting ? 'true' : 'false'">
      <div class="space-y-1 pr-6">
        <DialogTitle class="flex items-center gap-2 text-sm font-semibold">
          <Rocket class="size-4 text-primary" aria-hidden="true" />
          <span class="min-w-0 truncate">启用 Agent<template v-if="preview?.title">：{{ preview.title }}</template></span>
        </DialogTitle>
        <DialogDescription class="text-xs leading-relaxed text-muted-foreground">
          在主机上<b class="font-medium text-foreground">无人值守地新开一个 Cursor CLI 对话</b>，它会带着这个会话的工作流编号接入 RelayMesh；接入之后，这个会话就和别的智能体会话一样。
          只新开这一次：之后点「运行」都接着它。会产生模型用量，并受主机的「模型访问」与执行模式（含「需批准」）约束。
        </DialogDescription>
      </div>

      <p v-if="unsupported" class="rounded-sm border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-[11px] leading-relaxed text-amber-700 dark:text-amber-400" role="alert" data-testid="enable-agent-unsupported">
        这台 Hub 的版本还不支持「启用 Agent」，请先升级 Hub。
      </p>

      <p v-else-if="notPending" class="flex items-start gap-1.5 rounded-sm border border-border/70 bg-muted/20 px-3 py-2 text-[11px] leading-relaxed text-foreground" role="status" data-testid="enable-agent-not-pending">
        <AlertTriangle class="mt-px size-3.5 shrink-0 text-amber-600 dark:text-amber-400" aria-hidden="true" />
        <span>这个会话已经有智能体接入过了，不需要再启用；要让它接着干活，请用会话页的「运行」。</span>
      </p>

      <template v-else>
        <p v-if="previewFailed" class="text-[11px] leading-relaxed text-destructive" role="alert" data-testid="enable-agent-preview-error">读取预览失败：{{ previewFailed }}</p>

        <!-- 主机 -->
        <label class="block space-y-1">
          <span class="text-[11px] font-medium text-foreground">主机</span>
          <select
            ref="hostSelect"
            v-model="draft.host"
            :disabled="submitting || !preview"
            :aria-invalid="!!shown('host')"
            :class="[fieldClass, border('host'), 'h-8 cursor-pointer']"
            data-testid="enable-agent-host"
            @change="touched.host = true"
          >
            <option value="" disabled>{{ preview ? '选择一台主机' : '正在读取主机…' }}</option>
            <option v-for="c in choices" :key="c.code" :value="c.code">{{ c.label }}</option>
          </select>
          <span v-if="shown('host')" class="block text-[11px] text-destructive" role="alert" data-testid="enable-agent-error-host">{{ shown('host') }}</span>
          <span v-else-if="preview && !choices.length" class="block text-[11px] leading-relaxed text-amber-600 dark:text-amber-400" data-testid="enable-agent-no-hosts">
            还没有可用的主机：先到 ExecMesh 的「主机」页启用一台装了 Agent 的主机。
          </span>
        </label>

        <!-- 目录 -->
        <label class="block space-y-1">
          <span class="text-[11px] font-medium text-foreground">目录</span>
          <input
            ref="dirInput"
            v-model="draft.dir"
            type="text"
            autocomplete="off"
            spellcheck="false"
            :placeholder="dirPlaceholder(chosenOs || 'linux')"
            :disabled="submitting"
            :aria-invalid="!!shown('dir')"
            :class="[fieldClass, border('dir'), 'h-8 font-mono']"
            data-testid="enable-agent-dir"
            @input="dirEdited = true"
            @blur="touched.dir = true"
          />
          <span v-if="shown('dir')" class="block text-[11px] text-destructive" role="alert" data-testid="enable-agent-error-dir">{{ shown('dir') }}</span>
          <span v-else-if="preview?.bound_dir" class="block text-[11px] leading-relaxed text-muted-foreground" data-testid="enable-agent-bound-dir">
            这个会话在网页里绑定了项目目录 <code class="font-mono">{{ preview.bound_dir }}</code>：智能体第一次汇报必须带这个目录，所以这里默认用它；填成别的目录它会被拒绝。
          </span>
          <span v-else-if="chosenIsWindows" class="block text-[11px] leading-relaxed text-muted-foreground" data-testid="enable-agent-dir-help-win">
            主机上的绝对路径（盘符开头，如 <code class="font-mono">C:\work\项目</code>，或 <code class="font-mono">\\服务器\共享\…</code>）：智能体在这个目录里工作（<code class="font-mono">--workspace</code>）。
            这台主机是 Windows，命令经它的 PowerShell 运行；路径里不能有 <code class="font-mono">" % ^ &amp; | &lt; &gt; ! * ?</code>。
          </span>
          <span v-else class="block text-[11px] leading-relaxed text-muted-foreground">
            主机上的绝对路径（以 <code class="font-mono">/</code> 开头）：智能体在这个目录里工作（<code class="font-mono">--workspace</code>）。
          </span>
        </label>

        <!-- 模型 -->
        <label class="block space-y-1">
          <span class="text-[11px] font-medium text-foreground">模型（可选）</span>
          <input
            ref="modelInput"
            v-model="draft.model"
            type="text"
            autocomplete="off"
            spellcheck="false"
            placeholder="例如：claude-sonnet-5-5-max；留空用 Cursor CLI 的默认模型"
            :disabled="submitting"
            :aria-invalid="!!shown('model')"
            :class="[fieldClass, border('model'), 'h-8 font-mono']"
            data-testid="enable-agent-model"
            @blur="touched.model = true"
          />
          <span v-if="shown('model')" class="block text-[11px] text-destructive" role="alert" data-testid="enable-agent-error-model">{{ shown('model') }}</span>
        </label>

        <!-- 权限姿态：默认放开全部命令（Q43 b），红字写明后果 -->
        <div class="space-y-1.5" role="radiogroup" aria-label="权限姿态" data-testid="enable-agent-posture">
          <span class="text-[11px] font-medium text-foreground">权限姿态</span>
          <label class="flex cursor-pointer items-start gap-2">
            <input v-model="draft.posture" type="radio" value="force" class="mt-0.5 size-3.5 cursor-pointer accent-primary" :disabled="submitting" data-testid="enable-agent-posture-force" />
            <span class="text-[11px] leading-relaxed text-muted-foreground">
              <b class="font-medium text-foreground">放开全部命令（默认）</b>：带 <code class="font-mono">--force</code>。无人值守时没有人能点「允许」，放开才能让它真正干活。
            </span>
          </label>
          <label class="flex cursor-pointer items-start gap-2">
            <input v-model="draft.posture" type="radio" value="allowlist" class="mt-0.5 size-3.5 cursor-pointer accent-primary" :disabled="submitting" data-testid="enable-agent-posture-allowlist" />
            <span class="text-[11px] leading-relaxed text-muted-foreground">
              <b class="font-medium text-foreground">只放行白名单内的操作</b>：不带 <code class="font-mono">--force</code>。主机上 CLI 配置的
              <code class="font-mono">permissions.allow</code> 里要先有 <code class="font-mono">Mcp(&lt;服务器名&gt;:*)</code>，白名单之外的命令会被拒绝（无人批准，多半会停在那里）。
            </span>
          </label>
          <p
            v-if="risk"
            class="flex items-start gap-1.5 rounded-sm border border-destructive/50 bg-destructive/10 px-2.5 py-2 text-[11px] leading-relaxed text-destructive"
            role="alert"
            data-testid="enable-agent-posture-warning"
          >
            <AlertTriangle class="mt-px size-3.5 shrink-0" aria-hidden="true" />
            <span>{{ risk }}</span>
          </p>
        </div>

        <!-- 最长运行时间 -->
        <div class="space-y-1" data-testid="enable-agent-maxrun-field">
          <label for="enable-agent-maxrun" class="block text-[11px] font-medium text-foreground">最长运行时间</label>
          <div class="flex items-center gap-2">
            <input
              id="enable-agent-maxrun"
              ref="maxRunInput"
              v-model="draft.maxRun"
              type="text"
              inputmode="decimal"
              autocomplete="off"
              spellcheck="false"
              placeholder="留空 = 平台上限"
              :disabled="submitting"
              :aria-invalid="!!shown('maxRun')"
              :class="[fieldClass, border('maxRun'), 'h-8 w-28 font-mono']"
              data-testid="enable-agent-maxrun"
              @blur="touched.maxRun = true"
            />
            <select
              v-model="draft.maxRunUnit"
              :disabled="submitting"
              aria-label="最长运行时间的单位"
              :class="[fieldClass, 'h-8 w-20 cursor-pointer']"
              data-testid="enable-agent-maxrun-unit"
            >
              <option v-for="u in RUN_LIMIT_UNITS" :key="u.value" :value="u.value">{{ u.label }}</option>
            </select>
          </div>
          <span v-if="shown('maxRun')" class="block text-[11px] text-destructive" role="alert" data-testid="enable-agent-error-maxrun">{{ shown('maxRun') }}</span>
          <span v-else class="block text-[11px] leading-relaxed text-muted-foreground">
            到点主机会强制结束这次运行；默认 24 小时，可填 1 分钟到 {{ maxRunCap }}。提示词里会提醒智能体这个时限，让它在到点之前先汇报进度。
          </span>
        </div>

        <!-- 异常退出时自动续跑 -->
        <label class="flex cursor-pointer items-start gap-2" data-testid="enable-agent-autoresume-field">
          <input v-model="draft.autoResume" type="checkbox" class="mt-0.5 size-3.5 cursor-pointer accent-primary" :disabled="submitting" data-testid="enable-agent-autoresume" />
          <span class="text-[11px] leading-relaxed text-muted-foreground">
            <b class="font-medium text-foreground">异常退出时自动续跑</b>（默认关闭）：只重试 Cursor CLI 标为「可重试」的错误（如限流）与崩溃，24 小时内最多 3 次；
            续跑会接着已经建立的对话，不会再新开。
          </span>
        </label>

        <!-- 高级：可执行文件与提示词 -->
        <div class="space-y-2">
          <button
            type="button"
            class="inline-flex cursor-pointer items-center gap-0.5 text-[11px] text-muted-foreground transition-colors hover:text-foreground"
            :aria-expanded="advancedOpen"
            data-testid="enable-agent-advanced-toggle"
            @click="advancedOpen = !advancedOpen"
          >
            <component :is="advancedOpen ? ChevronDown : ChevronRight" class="size-3" aria-hidden="true" />高级（可执行文件、提示词）
          </button>
          <div v-if="advancedOpen" class="space-y-2 rounded-sm border border-border/60 bg-muted/10 px-3 py-2.5" data-testid="enable-agent-advanced">
            <label class="block space-y-1">
              <span class="text-[11px] font-medium text-foreground">可执行文件（可选）</span>
              <input
                ref="binInput"
                v-model="draft.bin"
                type="text"
                autocomplete="off"
                spellcheck="false"
                placeholder="留空 = agent；有的主机装的是旧名字 cursor-agent"
                :disabled="submitting"
                :aria-invalid="!!shown('bin')"
                :class="[fieldClass, border('bin'), 'h-8 font-mono']"
                data-testid="enable-agent-bin"
                @blur="touched.bin = true"
              />
              <span v-if="shown('bin')" class="block text-[11px] text-destructive" role="alert" data-testid="enable-agent-error-bin">{{ shown('bin') }}</span>
            </label>
            <div class="space-y-1">
              <div class="flex items-center justify-between text-[11px] font-medium text-foreground">
                <label for="enable-agent-prompt">提示词（可选）</label>
                <span class="font-mono font-normal" :class="promptCount > limits.runPrompt ? 'text-destructive' : 'text-muted-foreground'">{{ promptCount }}/{{ limits.runPrompt }}</span>
              </div>
              <textarea
                id="enable-agent-prompt"
                ref="promptInput"
                v-model="draft.prompt"
                rows="4"
                spellcheck="false"
                :placeholder="preview?.default_prompt || '留空使用内置的默认提示词'"
                :disabled="submitting"
                :aria-invalid="!!shown('prompt')"
                :class="[fieldClass, border('prompt'), 'resize-y py-1.5 leading-relaxed']"
                data-testid="enable-agent-prompt"
                @blur="touched.prompt = true"
              />
              <span v-if="shown('prompt')" class="block text-[11px] text-destructive" role="alert" data-testid="enable-agent-error-prompt">{{ shown('prompt') }}</span>
              <span v-else class="block text-[11px] leading-relaxed text-muted-foreground">
                单行文本，可以用 <code class="font-mono">%workflow</code>（这个会话的工作流编号）等变量（发出那一刻现算，全部见「设置 › 会话提示词」）。留空 = 用下一层：这个会话的「唤醒词」模板 → 设置里的全局覆盖 → 内置默认；内置默认让新开的智能体先接入这个工作流、读第一条指示，并要求它无论如何都向 RelayMesh 发一条消息说明结果。
              </span>
            </div>
          </div>
        </div>

        <!-- 之前启用过 / 服务端的提醒与不能启用的原因 -->
        <p v-if="previous" class="text-[11px] leading-relaxed text-muted-foreground" data-testid="enable-agent-previous">{{ previous }}</p>

        <ul v-if="warnings.length" class="space-y-1" data-testid="enable-agent-warnings">
          <li v-for="w in warnings" :key="w" class="text-[11px] leading-relaxed text-amber-700 dark:text-amber-400">{{ w }}</li>
        </ul>

        <ul v-if="issues.length" class="space-y-1" role="alert" data-testid="enable-agent-issues">
          <li v-for="i in issues" :key="i.code" class="text-[11px] leading-relaxed text-destructive" :data-issue="i.code">{{ i.message }}</li>
        </ul>

        <!-- 将运行的命令 -->
        <div v-if="preview?.command && !hasErrors" class="space-y-1">
          <button
            type="button"
            class="inline-flex cursor-pointer items-center gap-0.5 text-[11px] text-muted-foreground transition-colors hover:text-foreground"
            :aria-expanded="commandOpen"
            data-testid="enable-agent-command-toggle"
            @click="commandOpen = !commandOpen"
          >
            <component :is="commandOpen ? ChevronDown : ChevronRight" class="size-3" aria-hidden="true" />将运行的命令
          </button>
          <pre
            v-if="commandOpen"
            class="max-h-40 overflow-auto whitespace-pre-wrap break-all rounded-sm border border-border/60 bg-background/60 p-2 font-mono text-[10px] leading-relaxed text-foreground/90"
            data-testid="enable-agent-command"
          >{{ preview.command }}</pre>
        </div>

        <p class="rounded-sm border border-border/60 bg-muted/10 px-3 py-2 text-[11px] leading-relaxed text-foreground/90" data-testid="enable-agent-summary">{{ summary }}</p>

        <p v-if="submitError" class="text-[11px] leading-relaxed text-destructive" role="alert" data-testid="enable-agent-error">{{ submitError }}</p>
      </template>

      <!-- 表单较长时在弹窗内滚动；底栏钉在底部，「启用并运行」始终点得到 -->
      <div class="sticky bottom-0 z-10 -mx-5 -mb-5 flex items-center justify-end gap-2 border-t border-border/70 bg-card px-5 py-3" data-testid="enable-agent-footer">
        <Button type="button" variant="ghost" size="sm" class="h-8 text-xs" data-testid="enable-agent-cancel" @click="open = false">{{ notPending || unsupported ? '关闭' : '取消' }}</Button>
        <Button
          v-if="!notPending && !unsupported"
          type="button"
          size="sm"
          class="h-8 gap-1 text-xs"
          :disabled="!canSubmit"
          data-testid="enable-agent-go"
          @click="submit"
        >
          <Loader2 v-if="submitting" class="size-3.5 animate-spin" aria-hidden="true" />
          <Rocket v-else class="size-3.5" aria-hidden="true" />
          {{ submitting ? '启用中…' : '启用并运行' }}
        </Button>
      </div>
    </DialogContent>
  </Dialog>
</template>
