<script setup lang="ts">
import { computed, nextTick, onMounted, reactive, ref, watch } from 'vue'
import { Bot, ChevronDown, Loader2, Play, ShieldCheck, SquareTerminal } from 'lucide-vue-next'
import Button from '../../ui/button/Button.vue'
import type { HostView, LauncherGuard, TerminalLauncher } from '../../../api/terminal'
import { hostTitle, runLimitText, shortCode } from '../../../lib/terminal/format'
import {
  GUARD_DEFAULT_LIMITS,
  guardDraftOf,
  guardHints,
  hasGuardErrors,
  normalizePrompt,
  toGuard,
  usesChatId,
  validateGuardDraft,
  type GuardField,
  type GuardLimits,
} from '../../../lib/terminal/guard'
import {
  AUTO_RESUME_OK_DEFAULT_MINUTES,
  AUTO_RESUME_OK_MAX_MINUTES,
  CURSOR_CLI_COMMAND,
  CURSOR_CLI_RESUME_NAME,
  RUN_LIMIT_UNITS,
  blankRunDraft,
  charCount,
  collapseSpaces,
  dirAdvice,
  dirPlaceholder,
  hasErrors,
  hostPlatformOf,
  hostReadiness,
  isMacHost,
  isWindowsHost,
  launcherHosts,
  shellKindOf,
  supportsExitEndsSession,
  toLauncher,
  validateDraft,
  type DraftField,
  type LauncherDraft,
  type LauncherLimits,
} from '../../../lib/terminal/launcher'

/**
 * 预设的编辑表单：名字、主机、目录、命令；「非交互续聊」预设（#126）没有命令与守护，换成一组运行选项。
 * 校验规则与服务端一致（lib/terminal/launcher.ts），
 * 没碰过的字段不提前报错；点了保存仍有问题的才把全部错误亮出来，并把光标送到第一个有问题的字段。
 * 保存（需要解锁）由外面做：表单只交出规整过的预设，外面失败时把原因通过 serverError 交回来就地显示。
 */
const props = defineProps<{
  draft: LauncherDraft
  isNew: boolean
  hosts: HostView[]
  /** 已保存的其他预设（用来查重名；不含正在编辑的这一条）。 */
  others: TerminalLauncher[]
  limits: LauncherLimits
  saving: boolean
  serverError: string
  /** Hub 是否认识会话守护（旧版 Hub 不认识：不显示守护这一节，也不会改动已有的守护配置）。 */
  guardSupported?: boolean
  /** 正在编辑的预设已有的守护配置；新建预设时没有。 */
  initialGuard?: LauncherGuard
  guardLimits?: GuardLimits
}>()

const emit = defineEmits<{
  (e: 'save', l: TerminalLauncher, thenStart: boolean): void
  (e: 'cancel'): void
}>()

const form = reactive<LauncherDraft>({ ...props.draft })
watch(
  () => props.draft,
  d => Object.assign(form, { run: undefined }, d),
)

const touched = reactive<Record<DraftField, boolean>>({
  name: false, host: false, dir: false, command: false, bin: false, model: false, workflow: false, conversation: false, prompt: false, maxRun: false,
  autoResumeOk: false,
})
const submitted = ref(false)

// ---------- 预设类型：终端 | 非交互续聊（#126） ----------
// 类型只在新建时可选（已有的预设不换类型：想换就新建一个）；旧版 Hub 不认识续聊预设时不提供。
const isRun = computed(() => !!form.run)
const canPickKind = computed(() => props.isNew && props.limits.runSupported)
function setKind(k: 'terminal' | 'headless') {
  if (k === 'headless') {
    if (form.run) return
    form.run = blankRunDraft()
    form.command = ''
    if (!form.name.trim()) form.name = CURSOR_CLI_RESUME_NAME
  } else {
    if (!form.run) return
    form.run = undefined
    if (form.name.trim() === CURSOR_CLI_RESUME_NAME) form.name = ''
  }
  submitted.value = false
  for (const f of Object.keys(touched) as DraftField[]) touched[f] = false
}
const runPromptCount = computed(() => charCount(collapseSpaces(form.run?.prompt ?? '')))
// 「新开对话」（#176）用另一份默认提示词：它要让新开的智能体先接进这个工作流，而不是「恢复现场」
const runPromptDefault = computed(() => (form.run?.newConversation ? props.limits.runNewPromptDefault : props.limits.runPromptDefault))
const useDefaultRunPrompt = () => {
  if (!form.run) return
  form.run.prompt = runPromptDefault.value
  touched.prompt = true
}
// 选了「新开对话」就不能再固定对话编号：把它清掉，免得保存时被拒绝（取消勾选后需要的话再填回去）
watch(
  () => form.run?.newConversation,
  on => {
    if (on && form.run?.conversation) form.run.conversation = ''
  },
)
// 只有 Hub 认识「新开对话」，或者这条预设本来就开着它（回滚到旧版 Hub 后编辑，不能悄悄把它关掉）才显示这个选项
const showNewConversation = computed(() => !!props.limits.runNewPromptDefault || form.run?.newConversation === true)
const createdByEnable = computed(() => form.run?.origin === 'enable_agent')

// ---------- 会话守护（D-214） ----------
// 守护配置在表单里自成一节；校验规则与服务端一致（lib/terminal/guard.ts）。
const glimits = computed(() => props.guardLimits ?? GUARD_DEFAULT_LIMITS)
const gform = reactive(guardDraftOf(props.initialGuard))
watch(
  () => props.initialGuard,
  g => Object.assign(gform, guardDraftOf(g)),
)
const gtouched = reactive<Record<GuardField, boolean>>({ workflow: false, silence: false, resume: false, prompt: false, command: false })
const gerrors = computed(() => validateGuardDraft(gform, glimits.value, form.command))
const gshown = (f: GuardField) => (gtouched[f] || submitted.value ? gerrors.value[f] : undefined)
const ghints = computed(() => guardHints(gform, form.command.trim() !== ''))
// 已经开着监测、或者有填过内容时默认展开；点了保存仍有错误时强制展开。
const guardOpen = ref(!!props.initialGuard?.enabled)
const promptCount = computed(() => charCount(normalizePrompt(gform.prompt)))
const resumeNeedsHook = computed(() => usesChatId(gform.resume))
const useDefaultPrompt = () => {
  gform.prompt = glimits.value.promptDefault
  gtouched.prompt = true
}

const errors = computed(() => validateDraft(form, { limits: props.limits, others: props.others }))
const commandExecErr = computed(() =>
  props.guardSupported && (submitted.value || gtouched.command || gform.exitEndsSession) ? gerrors.value.command : undefined,
)
const shown = (f: DraftField) => {
  if (f === 'command' && commandExecErr.value) return commandExecErr.value
  return touched[f] || submitted.value ? errors.value[f] : undefined
}

const hostOptions = computed(() => launcherHosts(props.hosts))
// 预设里的主机现在不在可选列表里（离线不算，被删除 / 停用 / 是 SSH 主机才算）：保留一个只读的选项，免得下拉框显示成空白、保存时悄悄换了主机。
const orphanHost = computed(() => (form.host && !hostOptions.value.some(h => h.machine_code === form.host) ? form.host : ''))
const selectedHost = computed(() => props.hosts.find(h => h.machine_code === form.host))
// 目录怎么写、启动那一句怎么拼，都跟着所选主机的系统与 shell 走（#163）：Windows 上是 PowerShell / cmd，不是 bash。
const platform = computed(() => hostPlatformOf(selectedHost.value))
const shellKind = computed(() => shellKindOf(platform.value))
const hostIsWindows = computed(() => isWindowsHost(platform.value.os))
const hostIsMac = computed(() => isMacHost(platform.value.os))
const dirNote = computed(() => dirAdvice(form.dir ?? '', platform.value))
const exitEndsOk = computed(() => supportsExitEndsSession(platform.value))
// 启动命令是原样「敲」进主机的 shell 的，语法由那个 shell 决定：Windows 主机上提醒一句（我们不翻译命令）。
const commandSyntaxNote = computed(() => {
  switch (shellKind.value) {
    case 'powershell':
      return "这台主机的 shell 是 PowerShell：命令按 PowerShell 的写法（设环境变量用 $env:FOO='1'; agent，不是 FOO=1 agent；路径分隔符是 \\）。"
    case 'cmd':
      return '这台主机的 shell 是 cmd：命令按 cmd 的写法（设环境变量用 set FOO=1 && agent）。'
    case 'windows':
      return '这台主机是 Windows：命令按它的 shell（PowerShell 或 cmd）的写法，不是 bash。'
    default:
      return ''
  }
})
// 「命令退出即结束会话」靠 POSIX 的 exec：Windows 主机上用不了，勾选的也撤掉（否则会拿 POSIX 的规则去校验 PowerShell 写的命令）。
watch(
  [hostIsWindows, () => gform.exitEndsSession],
  ([win, on]) => {
    if (win && on) gform.exitEndsSession = false
  },
  { immediate: true },
)
const startable = computed(() => hostReadiness(selectedHost.value).ok && !(isRun.value && hostIsWindows.value))

const nameInput = ref<HTMLInputElement | null>(null)
const hostSelect = ref<HTMLSelectElement | null>(null)
const dirInput = ref<HTMLInputElement | null>(null)
const commandInput = ref<HTMLInputElement | null>(null)
const runWorkflowInput = ref<HTMLInputElement | null>(null)
const runConversationInput = ref<HTMLInputElement | null>(null)
const runModelInput = ref<HTMLInputElement | null>(null)
const runBinInput = ref<HTMLInputElement | null>(null)
const runPromptInput = ref<HTMLTextAreaElement | null>(null)
const runMaxRunInput = ref<HTMLInputElement | null>(null)
const runAutoResumeOkInput = ref<HTMLInputElement | null>(null)
const fieldRefs: Record<DraftField, () => HTMLElement | null> = {
  name: () => nameInput.value,
  host: () => hostSelect.value,
  dir: () => dirInput.value,
  command: () => commandInput.value,
  workflow: () => runWorkflowInput.value,
  conversation: () => runConversationInput.value,
  model: () => runModelInput.value,
  bin: () => runBinInput.value,
  prompt: () => runPromptInput.value,
  maxRun: () => runMaxRunInput.value,
  autoResumeOk: () => runAutoResumeOkInput.value,
}

// 最长运行时间（#141）：数字 + 单位；留空 = 平台上限。旧版 Hub 不认识它（上限为 0）时不显示。
const maxRunCap = computed(() => runLimitText(props.limits.runMaxMinutes * 60))
const workflowInput = ref<HTMLInputElement | null>(null)
const silenceInput = ref<HTMLInputElement | null>(null)
const resumeInput = ref<HTMLInputElement | null>(null)
const promptInput = ref<HTMLTextAreaElement | null>(null)
const guardRefs: Record<GuardField, () => HTMLElement | null> = {
  workflow: () => workflowInput.value,
  silence: () => silenceInput.value,
  resume: () => resumeInput.value,
  prompt: () => promptInput.value,
  command: () => commandInput.value,
}

onMounted(() => void nextTick(() => nameInput.value?.focus()))

function submit(thenStart: boolean) {
  if (props.saving) return
  submitted.value = true
  const e = errors.value
  if (hasErrors(e)) {
    const order: DraftField[] = isRun.value
      ? ['name', 'host', 'dir', 'workflow', 'conversation', 'model', 'maxRun', 'autoResumeOk', 'bin', 'prompt']
      : ['name', 'host', 'dir', 'command']
    const first = order.find(f => e[f])
    if (first) fieldRefs[first]()?.focus()
    return
  }
  // 续聊预设没有守护这一节，也就没有守护的校验。
  const ge = gerrors.value
  if (!isRun.value && props.guardSupported && hasGuardErrors(ge)) {
    guardOpen.value = true
    const first = (['command', 'workflow', 'silence', 'resume', 'prompt'] as GuardField[]).find(f => ge[f])
    if (first) void nextTick(() => guardRefs[first]()?.focus())
    return
  }
  const launcher = toLauncher(form)
  // 旧版 Hub 不认识守护：不带这个字段（它也不会有已存在的守护配置要保留）。
  if (!isRun.value && props.guardSupported) {
    const g = toGuard(gform)
    if (g) launcher.guard = g
  }
  emit('save', launcher, thenStart)
}

const fieldClass =
  'w-full rounded-xs border bg-background px-2 text-xs text-foreground placeholder:text-muted-foreground focus:border-primary focus:outline-none focus:ring-1 focus:ring-primary/30 disabled:opacity-60'
const border = (f: DraftField) => (shown(f) ? 'border-destructive' : 'border-border/80')

const nameCount = computed(() => charCount(form.name))
const commandCount = computed(() => charCount(form.command))
</script>

<template>
  <form class="space-y-3" data-testid="launcher-form" :data-kind="isRun ? 'headless' : 'terminal'" @submit.prevent="submit(false)">
    <!-- 预设类型（只在新建时可选）：终端 = 开一个终端会话并输入命令；非交互续聊 = 由 Hub 在主机上运行一次 agent -p --resume（#126） -->
    <div v-if="canPickKind" class="space-y-1" data-testid="launcher-kind">
      <span class="text-[11px] font-medium text-foreground">类型</span>
      <div class="grid grid-cols-2 gap-1.5" role="radiogroup" aria-label="预设类型">
        <button
          type="button"
          role="radio"
          :aria-checked="!isRun"
          class="flex cursor-pointer items-start gap-2 rounded-md border px-2.5 py-2 text-left transition-colors"
          :class="!isRun ? 'border-primary/60 bg-primary/5' : 'border-border/70 hover:bg-muted/30'"
          :disabled="saving"
          data-testid="launcher-kind-terminal"
          @click="setKind('terminal')"
        >
          <SquareTerminal class="mt-0.5 size-3.5 shrink-0" :class="!isRun ? 'text-primary' : 'text-muted-foreground'" aria-hidden="true" />
          <span class="min-w-0 space-y-0.5">
            <span class="block text-[11px] font-medium text-foreground">终端</span>
            <span class="block text-[11px] leading-snug text-muted-foreground">开一个终端会话，自动输入命令，可以接着操作</span>
          </span>
        </button>
        <button
          type="button"
          role="radio"
          :aria-checked="isRun"
          class="flex cursor-pointer items-start gap-2 rounded-md border px-2.5 py-2 text-left transition-colors"
          :class="isRun ? 'border-primary/60 bg-primary/5' : 'border-border/70 hover:bg-muted/30'"
          :disabled="saving"
          data-testid="launcher-kind-headless"
          @click="setKind('headless')"
        >
          <Bot class="mt-0.5 size-3.5 shrink-0" :class="isRun ? 'text-primary' : 'text-muted-foreground'" aria-hidden="true" />
          <span class="min-w-0 space-y-0.5">
            <span class="block text-[11px] font-medium text-foreground">非交互续聊</span>
            <span class="block text-[11px] leading-snug text-muted-foreground">不开终端，由 Hub 在主机上接着原对话运行一次</span>
          </span>
        </button>
      </div>
    </div>

    <label class="block space-y-1">
      <span class="flex items-center justify-between text-[11px] font-medium text-foreground">
        <span>名字</span>
        <span class="font-mono font-normal" :class="nameCount > limits.name ? 'text-destructive' : 'text-muted-foreground'">{{ nameCount }}/{{ limits.name }}</span>
      </span>
      <input
        ref="nameInput"
        v-model="form.name"
        type="text"
        autocomplete="off"
        placeholder="例如：Cursor CLI · RelayMesh"
        :disabled="saving"
        :aria-invalid="!!shown('name')"
        :class="[fieldClass, border('name'), 'h-8']"
        data-testid="launcher-name"
        @blur="touched.name = true"
      />
      <span v-if="shown('name')" class="block text-[11px] text-destructive" role="alert" data-testid="launcher-error-name">{{ shown('name') }}</span>
      <span v-else-if="isRun" class="block text-[11px] leading-relaxed text-muted-foreground">它会出现在 ExecMesh 的运行记录里（线程名）和聊天里的系统行里。</span>
      <span v-else class="block text-[11px] leading-relaxed text-muted-foreground">它也是新开会话的名字：在 ExecMesh 的会话列表里就叫这个。</span>
    </label>

    <label class="block space-y-1">
      <span class="text-[11px] font-medium text-foreground">主机</span>
      <select
        ref="hostSelect"
        v-model="form.host"
        :disabled="saving"
        :aria-invalid="!!shown('host')"
        :class="[fieldClass, border('host'), 'h-8 cursor-pointer']"
        data-testid="launcher-host"
        @change="touched.host = true"
      >
        <option value="" disabled>选择一台主机</option>
        <option v-for="h in hostOptions" :key="h.machine_code" :value="h.machine_code">{{ hostTitle(h) }}{{ h.online ? '' : '（离线）' }}</option>
        <option v-if="orphanHost" :value="orphanHost">{{ shortCode(orphanHost) }}（不可用：已停用、被删除或不是 Agent 主机）</option>
      </select>
      <span v-if="shown('host')" class="block text-[11px] text-destructive" role="alert" data-testid="launcher-error-host">{{ shown('host') }}</span>
      <span v-else-if="!hostOptions.length" class="block text-[11px] leading-relaxed text-amber-600 dark:text-amber-400" data-testid="launcher-no-hosts">
        还没有可用的主机：先到 ExecMesh 的「主机」页启用一台装了 Agent 的主机。
      </span>
    </label>

    <label class="block space-y-1">
      <span class="text-[11px] font-medium text-foreground">{{ isRun ? '目录' : '目录（可选）' }}</span>
      <input
        ref="dirInput"
        v-model="form.dir"
        type="text"
        autocomplete="off"
        spellcheck="false"
        :placeholder="dirPlaceholder(platform)"
        :disabled="saving"
        :aria-invalid="!!shown('dir')"
        :class="[fieldClass, border('dir'), 'h-8 font-mono']"
        data-testid="launcher-dir"
        @blur="touched.dir = true"
      />
      <span v-if="shown('dir')" class="block text-[11px] text-destructive" role="alert" data-testid="launcher-error-dir">{{ shown('dir') }}</span>
      <span v-else-if="isRun" class="block text-[11px] leading-relaxed text-muted-foreground">
        必须是主机上的绝对路径（以 <code class="font-mono">/</code> 开头）：续聊在这个目录里运行（<code class="font-mono">--workspace</code>），要和原对话所在的工作区一致。
      </span>
      <span v-else-if="shellKind === 'powershell' || shellKind === 'cmd'" class="block text-[11px] leading-relaxed text-muted-foreground" data-testid="launcher-dir-help-win">
        写完整路径（盘符开头，如 <code class="font-mono">C:\work\项目</code>；<code class="font-mono">~</code> 按这台主机的家目录展开）；留空用主机的默认目录。有命令时会先
        <code class="font-mono">{{ shellKind === 'cmd' ? 'cd /d' : 'Set-Location' }}</code> 到这里再运行——目录不存在就是一句看得见的报错，命令不会在错误的位置运行。
      </span>
      <span v-else-if="shellKind === 'windows'" class="block text-[11px] leading-relaxed text-muted-foreground" data-testid="launcher-dir-help-win">
        写完整路径（盘符开头，如 <code class="font-mono">C:\work\项目</code>）；留空用主机的默认目录。
      </span>
      <span v-else class="block text-[11px] leading-relaxed text-muted-foreground">
        写完整路径{{ hostIsMac ? '（如 /Users/你/projects/…，~ 开头也行）' : '' }}；留空用主机的默认目录。有命令时会先 <code class="font-mono">cd</code> 到这里再运行——目录不存在就是一句看得见的报错，命令不会在错误的位置运行。
      </span>
      <span v-if="dirNote" class="block text-[11px] leading-relaxed text-amber-600 dark:text-amber-400" data-testid="launcher-dir-advice">{{ dirNote }}</span>
    </label>

    <!-- 非交互续聊（#126）：没有命令，运行的命令由下面的选项拼出来；预设里只存选项，不存任意命令 -->
    <section
      v-if="form.run"
      class="space-y-3 rounded-md border border-border/70 bg-muted/10 px-3 py-3"
      data-testid="run-section"
    >
      <p v-if="hostIsWindows" class="text-[11px] leading-relaxed text-amber-600 dark:text-amber-400" role="status" data-testid="run-windows-note">
        这台主机是 Windows：暂不支持「非交互续聊」（点运行会被拒绝，这里仍可以保存）。请换一台 Linux / macOS 主机，或改建「终端」类型的预设。
      </p>
      <p class="text-[11px] leading-relaxed text-muted-foreground">
        点「运行」后，Hub 经这台主机的执行通道在上面的目录里运行一次
        <code class="font-mono">agent -p --resume &lt;对话&gt; …</code>：不打开终端，结果显示在清单里，聊天里也会有记录。只在你点了之后才做，
        并且受主机的「模型访问」与执行模式（含「需批准」）约束。
      </p>

      <label class="block space-y-1">
        <span class="text-[11px] font-medium text-foreground">工作流编号</span>
        <input
          ref="runWorkflowInput"
          v-model="form.run.workflow"
          type="text"
          autocomplete="off"
          spellcheck="false"
          placeholder="例如：relaymesh-continuation"
          :disabled="saving"
          :aria-invalid="!!shown('workflow')"
          :class="[fieldClass, border('workflow'), 'h-8 font-mono']"
          data-testid="run-workflow"
          @blur="touched.workflow = true"
        />
        <span v-if="shown('workflow')" class="block text-[11px] text-destructive" role="alert" data-testid="run-error-workflow">{{ shown('workflow') }}</span>
        <span v-else class="block text-[11px] leading-relaxed text-muted-foreground">
          智能体使用的 RelayMesh 工作流：用它找到智能体最近一次汇报的对话来续聊，运行记录与聊天里的系统行也归到这个工作流，并据此判断这次运行有没有向 RelayMesh 汇报。
        </span>
      </label>

      <label class="block space-y-1">
        <span class="text-[11px] font-medium text-foreground">对话编号（可选）</span>
        <input
          ref="runConversationInput"
          v-model="form.run.conversation"
          type="text"
          autocomplete="off"
          spellcheck="false"
          :placeholder="form.run.newConversation ? '第一次运行时新开，之后接着它' : '留空 = 工作流里最近一次汇报的对话'"
          :disabled="saving || form.run.newConversation"
          :aria-invalid="!!shown('conversation')"
          :class="[fieldClass, border('conversation'), 'h-8 font-mono']"
          data-testid="run-conversation"
          @blur="touched.conversation = true"
        />
        <span v-if="shown('conversation')" class="block text-[11px] text-destructive" role="alert" data-testid="run-error-conversation">{{ shown('conversation') }}</span>
        <span v-else class="block text-[11px] leading-relaxed text-muted-foreground">
          固定要续的对话（<code class="font-mono">agent --resume</code> 用的编号）。填了就不再找最近的对话；不填则必须填上面的工作流编号，且装好编辑器 hook 让它汇报过对话编号。
        </span>
      </label>

      <!-- 第一次运行时新开对话（#176 第三期）：命令里没有 --resume，新开的智能体按提示词带着上面的工作流编号接入；之后的运行就接着它 -->
      <label v-if="showNewConversation" class="flex cursor-pointer items-start gap-2" data-testid="run-newconv-field">
        <input v-model="form.run.newConversation" type="checkbox" class="mt-0.5 size-3.5 cursor-pointer accent-primary" :disabled="saving" data-testid="run-newconv" />
        <span class="text-[11px] leading-relaxed text-muted-foreground">
          <b class="font-medium text-foreground">第一次运行时新开对话</b>（默认关闭）：命令里不带 <code class="font-mono">--resume</code>，在主机上无人值守地新开一个 Cursor CLI 对话，
          它按提示词带着上面的工作流编号接入 RelayMesh；Hub 从运行输出里记下新对话的编号，<b class="font-medium text-foreground">只新开这一次</b>，之后点「运行」都接着它。
          工作流里已经有智能体在工作、或上一次运行还没结束时会拒绝，不会开出第二个。
          <span v-if="createdByEnable" class="block text-foreground/80" data-testid="run-origin-note">这条预设由会话页的「启用 Agent」创建；保存时来源标记会原样保留。</span>
        </span>
      </label>

      <label class="block space-y-1">
        <span class="text-[11px] font-medium text-foreground">模型（可选）</span>
        <input
          ref="runModelInput"
          v-model="form.run.model"
          type="text"
          autocomplete="off"
          spellcheck="false"
          placeholder="例如：claude-sonnet-5-5-max；留空沿用对话原来的模型"
          :disabled="saving"
          :aria-invalid="!!shown('model')"
          :class="[fieldClass, border('model'), 'h-8 font-mono']"
          data-testid="run-model"
          @blur="touched.model = true"
        />
        <span v-if="shown('model')" class="block text-[11px] text-destructive" role="alert" data-testid="run-error-model">{{ shown('model') }}</span>
      </label>

      <div class="space-y-1.5" data-testid="run-posture">
        <span class="text-[11px] font-medium text-foreground">权限姿态</span>
        <label class="flex cursor-pointer items-start gap-2">
          <input v-model="form.run.posture" type="radio" value="allowlist" class="mt-0.5 size-3.5 cursor-pointer accent-primary" :disabled="saving" data-testid="run-posture-allowlist" />
          <span class="text-[11px] leading-relaxed text-muted-foreground">
            <b class="font-medium text-foreground">只放行白名单内的操作（推荐）</b>：不带 <code class="font-mono">--force</code>。
            要让它读写 RelayMesh，需要先在主机上 CLI 配置的 <code class="font-mono">permissions.allow</code> 里加一条 <code class="font-mono">Mcp(&lt;服务器名&gt;:*)</code>；
            白名单之外的 shell 命令会被拒绝。
          </span>
        </label>
        <label class="flex cursor-pointer items-start gap-2">
          <input v-model="form.run.posture" type="radio" value="force" class="mt-0.5 size-3.5 cursor-pointer accent-primary" :disabled="saving" data-testid="run-posture-force" />
          <span class="text-[11px] leading-relaxed text-muted-foreground">
            <b class="font-medium text-amber-700 dark:text-amber-400">放开全部命令</b>：带 <code class="font-mono">--force</code>，它可以不经询问地运行任何命令。只在你完全信任这个工作区时使用。
          </span>
        </label>
      </div>

      <!-- 最长运行时间（#141）：到点主机强制结束这次运行；留空 = 平台上限。旧版 Hub 不认识它：不显示。 -->
      <div v-if="limits.runMaxMinutes > 0" class="space-y-1" data-testid="run-maxrun-field">
        <label for="run-maxrun" class="block text-[11px] font-medium text-foreground">最长运行时间（可选）</label>
        <div class="flex items-center gap-2">
          <input
            id="run-maxrun"
            ref="runMaxRunInput"
            v-model="form.run.maxRun"
            type="text"
            inputmode="decimal"
            autocomplete="off"
            spellcheck="false"
            :placeholder="`留空 = ${maxRunCap}`"
            :disabled="saving"
            :aria-invalid="!!shown('maxRun')"
            :class="[fieldClass, border('maxRun'), 'h-8 w-28 font-mono']"
            data-testid="run-maxrun"
            @blur="touched.maxRun = true"
          />
          <select
            v-model="form.run.maxRunUnit"
            :disabled="saving"
            aria-label="最长运行时间的单位"
            :class="[fieldClass, 'h-8 w-20 cursor-pointer']"
            data-testid="run-maxrun-unit"
          >
            <option v-for="u in RUN_LIMIT_UNITS" :key="u.value" :value="u.value">{{ u.label }}</option>
          </select>
        </div>
        <span v-if="shown('maxRun')" class="block text-[11px] text-destructive" role="alert" data-testid="run-error-maxrun">{{ shown('maxRun') }}</span>
        <span v-else class="block text-[11px] leading-relaxed text-muted-foreground">
          一次运行最长跑多久，到点主机会强制结束它（运行状态写「达到运行时长上限」）。留空 = 平台上限 {{ maxRunCap }}；可填 1 分钟到 {{ maxRunCap }}。
          续聊是一个会话，不是一条短命令，所以默认不设短限；想给无人看管的运行封个顶才需要填。
        </span>
      </div>

      <!-- 异常退出时自动续跑（#151）：默认关闭；只重试 Cursor CLI 标为可重试的错误与崩溃类信号 -->
      <label class="flex cursor-pointer items-start gap-2" data-testid="run-autoresume-field">
        <input v-model="form.run.autoResume" type="checkbox" class="mt-0.5 size-3.5 cursor-pointer accent-primary" :disabled="saving" data-testid="run-autoresume" />
        <span class="text-[11px] leading-relaxed text-muted-foreground">
          <b class="font-medium text-foreground">异常退出时自动续跑</b>（默认关闭）：运行以 Cursor CLI 标为「可重试」的错误（例如服务端限流
          <code class="font-mono">resource_exhausted</code>、连接失败）或进程崩溃退出时，等一会儿自动再续聊一次——退避 5 分钟、20 分钟、1 小时，
          24 小时内最多 3 次；续跑出来的运行连续跑满下面的判定时长就算续跑成功，次数与退避从头算；连续 2 次没跑满就又失败，则停下来通知你。
          你取消的、到了运行时长上限的、需要你处理或认不出原因的失败不会重试。这是无人值守的模型调用，会产生用量。
        </span>
      </label>
      <!-- 续跑成功的判定时长（#172）：只在开着自动续跑时显示；留空 = 默认 10 分钟 -->
      <div v-if="form.run.autoResume" class="space-y-1 pl-5" data-testid="run-autoresume-ok-field">
        <label for="run-autoresume-ok" class="block text-[11px] font-medium text-foreground">续跑成功的判定时长（分钟，可选）</label>
        <input
          id="run-autoresume-ok"
          ref="runAutoResumeOkInput"
          v-model="form.run.autoResumeOk"
          type="text"
          inputmode="numeric"
          autocomplete="off"
          spellcheck="false"
          :placeholder="`留空 = ${AUTO_RESUME_OK_DEFAULT_MINUTES}`"
          :disabled="saving"
          :aria-invalid="!!shown('autoResumeOk')"
          :class="[fieldClass, border('autoResumeOk'), 'h-8 w-28 font-mono']"
          data-testid="run-autoresume-ok"
          @blur="touched.autoResumeOk = true"
        />
        <span v-if="shown('autoResumeOk')" class="block text-[11px] text-destructive" role="alert" data-testid="run-error-autoresumeok">{{ shown('autoResumeOk') }}</span>
        <span v-else class="block text-[11px] leading-relaxed text-muted-foreground">
          续跑出来的运行连续跑满这么久，就认定这次续跑成功：24 小时内累计的续跑次数清零、退避从头算，之后再出事是新的一轮，不会被前面的次数拖到停下。
          留空 = {{ AUTO_RESUME_OK_DEFAULT_MINUTES }} 分钟，可填 1 到 {{ AUTO_RESUME_OK_MAX_MINUTES }} 分钟（1 天）；没跑满就又失败的续跑照常累计。
        </span>
      </div>

      <div class="space-y-1">
        <div class="flex items-center justify-between text-[11px] font-medium text-foreground">
          <label for="run-prompt">提示词（可选）</label>
          <span class="font-mono font-normal" :class="runPromptCount > limits.runPrompt ? 'text-destructive' : 'text-muted-foreground'">{{ runPromptCount }}/{{ limits.runPrompt }}</span>
        </div>
        <textarea
          id="run-prompt"
          ref="runPromptInput"
          v-model="form.run.prompt"
          rows="4"
          spellcheck="false"
          :placeholder="runPromptDefault || '留空使用内置的默认提示词'"
          :disabled="saving"
          :aria-invalid="!!shown('prompt')"
          :class="[fieldClass, border('prompt'), 'resize-y py-1.5 leading-relaxed']"
          data-testid="run-prompt"
          @blur="touched.prompt = true"
        />
        <span v-if="shown('prompt')" class="block text-[11px] text-destructive" role="alert" data-testid="run-error-prompt">{{ shown('prompt') }}</span>
        <span v-else class="block text-[11px] leading-relaxed text-muted-foreground">
          单行文本（换行会折成空格），可以用 <code class="font-mono">%workflow</code>（上面的工作流编号）。留空用内置的默认提示词：它会让智能体先恢复现场，
          并要求它无论如何都向 RelayMesh 发一条消息说明结果——非交互运行结束后没有人在终端里看输出；运行时还会自动补一句本次的最长运行时间，
          让它在到点之前先汇报进度（自定义提示词不会被加东西）。
        </span>
        <button
          v-if="runPromptDefault && !form.run.prompt.trim()"
          type="button"
          class="cursor-pointer text-[11px] text-primary underline-offset-2 hover:underline"
          data-testid="run-use-default"
          @click="useDefaultRunPrompt"
        >
          以内置的默认提示词为起点改写
        </button>
      </div>

      <label class="block space-y-1">
        <span class="text-[11px] font-medium text-foreground">可执行文件（可选）</span>
        <input
          ref="runBinInput"
          v-model="form.run.bin"
          type="text"
          autocomplete="off"
          spellcheck="false"
          placeholder="agent"
          :disabled="saving"
          :aria-invalid="!!shown('bin')"
          :class="[fieldClass, border('bin'), 'h-8 w-60 font-mono']"
          data-testid="run-bin"
          @blur="touched.bin = true"
        />
        <span v-if="shown('bin')" class="block text-[11px] text-destructive" role="alert" data-testid="run-error-bin">{{ shown('bin') }}</span>
        <span v-else class="block text-[11px] leading-relaxed text-muted-foreground">
          命令名或路径；留空是 <code class="font-mono">agent</code>，旧版叫 <code class="font-mono">cursor-agent</code>，按那台主机上装的写。
          运行时由登录 shell（bash）执行，只读 <code class="font-mono">.bash_profile</code> / <code class="font-mono">.profile</code>，不读 <code class="font-mono">.zshrc</code>：
          {{ hostIsMac ? 'macOS 上' : '' }}找不到命令时，这里写完整路径，例如 <code class="font-mono">~/.local/bin/agent</code>。
        </span>
      </label>
    </section>

    <label v-if="!isRun" class="block space-y-1">
      <span class="flex items-center justify-between text-[11px] font-medium text-foreground">
        <span>启动命令（可选）</span>
        <span class="font-mono font-normal" :class="commandCount > limits.command ? 'text-destructive' : 'text-muted-foreground'">{{ commandCount }}/{{ limits.command }}</span>
      </span>
      <input
        ref="commandInput"
        v-model="form.command"
        type="text"
        autocomplete="off"
        spellcheck="false"
        :placeholder="CURSOR_CLI_COMMAND"
        :disabled="saving"
        :aria-invalid="!!shown('command')"
        :class="[fieldClass, border('command'), 'h-8 font-mono']"
        data-testid="launcher-command"
        @blur="touched.command = true; gtouched.command = true"
      />
      <span v-if="shown('command')" class="block text-[11px] text-destructive" role="alert" data-testid="launcher-error-command">{{ shown('command') }}</span>
      <span v-else class="block text-[11px] leading-relaxed text-muted-foreground">
        会话就绪后自动输入并回车，和你手敲一样（会留在 shell 历史里）。Cursor CLI 通常是 <code class="font-mono">agent</code>，旧版叫 <code class="font-mono">cursor-agent</code>，按那台主机上装的写。留空则只打开终端。
      </span>
      <span v-if="commandSyntaxNote" class="block text-[11px] leading-relaxed text-muted-foreground" data-testid="launcher-command-help-win">{{ commandSyntaxNote }}</span>
    </label>

    <!-- 会话守护（D-214）：监测会话是否意外退出 / 停摆，需要时一键唤醒。只描述怎么判断、怎么唤醒，不会让任何东西自动运行。 -->
    <section v-if="guardSupported && !isRun" class="rounded-md border border-border/70 bg-muted/10" data-testid="guard-section" :data-open="guardOpen">
      <button
        type="button"
        class="flex w-full cursor-pointer items-center gap-2 px-3 py-2 text-left"
        :aria-expanded="guardOpen"
        data-testid="guard-toggle"
        @click="guardOpen = !guardOpen"
      >
        <ShieldCheck class="size-3.5 shrink-0" :class="gform.enabled ? 'text-emerald-600 dark:text-emerald-400' : 'text-muted-foreground'" aria-hidden="true" />
        <span class="min-w-0 flex-1">
          <span class="block text-[11px] font-medium text-foreground">会话守护（可选）</span>
          <span class="block truncate text-[11px] text-muted-foreground">
            {{ gform.enabled ? '已开启：意外退出或停摆时通知你，并可一键唤醒' : '会话意外退出或停摆时通知你，并可一键唤醒' }}
          </span>
        </span>
        <ChevronDown class="size-3.5 shrink-0 text-muted-foreground transition-transform" :class="guardOpen ? 'rotate-180' : ''" aria-hidden="true" />
      </button>

      <div v-show="guardOpen" class="space-y-3 border-t border-border/60 px-3 py-3">
        <label class="flex cursor-pointer items-start gap-2">
          <input v-model="gform.exitEndsSession" type="checkbox" class="mt-0.5 size-3.5 accent-primary" :class="exitEndsOk ? 'cursor-pointer' : 'cursor-not-allowed'" :disabled="saving || !exitEndsOk" data-testid="guard-exit-ends" @change="gtouched.command = true" />
          <span class="space-y-0.5">
            <span class="block text-[11px] font-medium text-foreground">命令退出即结束会话</span>
            <span v-if="exitEndsOk" class="block text-[11px] leading-relaxed text-muted-foreground">
              启动时用 <code class="font-mono">exec</code> 包装命令：Cursor CLI 一退出，会话就结束，退出码准确，守护能立刻发现「意外结束」。命令不能含 shell 运算符、复合语句或内建命令。
            </span>
            <span v-else class="block text-[11px] leading-relaxed text-amber-600 dark:text-amber-400" data-testid="guard-exit-ends-win">
              这台主机是 Windows，用不了这个选项：它依赖 POSIX shell 的 <code class="font-mono">exec</code>，PowerShell / cmd 里没有。命令退出后会话会停在 shell 提示符，守护只能靠「长时间没有动静」发现命令已经退出。
            </span>
          </span>
        </label>

        <label class="flex cursor-pointer items-start gap-2">
          <input v-model="gform.enabled" type="checkbox" class="mt-0.5 size-3.5 cursor-pointer accent-primary" :disabled="saving" data-testid="guard-enabled" />
          <span class="space-y-0.5">
            <span class="block text-[11px] font-medium text-foreground">监测这个预设的会话</span>
            <span class="block text-[11px] leading-relaxed text-muted-foreground">
              发现会话意外退出，或者智能体停摆（出错中断、长时间没有任何动静）时推送通知，并在启动器里给出「唤醒」按钮。
              <b class="font-medium text-foreground/80">不会自动唤醒</b>——唤醒永远是你点了之后才做。
            </span>
          </span>
        </label>

        <label class="block space-y-1">
          <span class="text-[11px] font-medium text-foreground">工作流编号</span>
          <input
            ref="workflowInput"
            v-model="gform.workflow"
            type="text"
            autocomplete="off"
            spellcheck="false"
            placeholder="例如：relaymesh-continuation"
            :disabled="saving"
            :aria-invalid="!!gshown('workflow')"
            :class="[fieldClass, gshown('workflow') ? 'border-destructive' : 'border-border/80', 'h-8 font-mono']"
            data-testid="guard-workflow"
            @blur="gtouched.workflow = true"
          />
          <span v-if="gshown('workflow')" class="block text-[11px] text-destructive" role="alert" data-testid="guard-error-workflow">{{ gshown('workflow') }}</span>
          <span v-else class="block text-[11px] leading-relaxed text-muted-foreground">
            这个会话里的智能体使用的 RelayMesh 工作流。填了才能分清「它在合法地等你回复」和「它停摆了」，也才会提醒你有没读的回复、追加指令和进行中的任务；不填只能发现会话意外退出。
          </span>
        </label>

        <label class="block space-y-1">
          <span class="text-[11px] font-medium text-foreground">停摆判定（分钟，可选）</span>
          <input
            ref="silenceInput"
            v-model="gform.silence"
            type="text"
            inputmode="numeric"
            autocomplete="off"
            :placeholder="`${glimits.silenceDefault}（默认）`"
            :disabled="saving"
            :aria-invalid="!!gshown('silence')"
            :class="[fieldClass, gshown('silence') ? 'border-destructive' : 'border-border/80', 'h-8 w-40 font-mono']"
            data-testid="guard-silence"
            @blur="gtouched.silence = true"
          />
          <span v-if="gshown('silence')" class="block text-[11px] text-destructive" role="alert" data-testid="guard-error-silence">{{ gshown('silence') }}</span>
          <span v-else class="block text-[11px] leading-relaxed text-muted-foreground">
            没有任何活动（工具调用、收尾、RelayMesh 交互）超过这么久，并且智能体不是在合法地等你回复，才算停摆。范围 {{ glimits.silenceMin }}～{{ glimits.silenceMax }}。
          </span>
        </label>

        <label class="block space-y-1">
          <span class="text-[11px] font-medium text-foreground">重新拉起命令（可选）</span>
          <input
            ref="resumeInput"
            v-model="gform.resume"
            type="text"
            autocomplete="off"
            spellcheck="false"
            placeholder="agent --resume {chat_id}"
            :disabled="saving"
            :aria-invalid="!!gshown('resume')"
            :class="[fieldClass, gshown('resume') ? 'border-destructive' : 'border-border/80', 'h-8 font-mono']"
            data-testid="guard-resume"
            @blur="gtouched.resume = true"
          />
          <span v-if="gshown('resume')" class="block text-[11px] text-destructive" role="alert" data-testid="guard-error-resume">{{ gshown('resume') }}</span>
          <span v-else class="block text-[11px] leading-relaxed text-muted-foreground">
            会话已经结束时，新开一个会话运行它来接回原来的对话；<code class="font-mono">{chat_id}</code> 换成智能体最近一次汇报的对话编号（需要装好编辑器 hook）。
            留空则新开一个对话，靠提示词让智能体从 RelayMesh 接上。
          </span>
          <span v-if="resumeNeedsHook" class="block text-[11px] leading-relaxed text-muted-foreground" data-testid="guard-resume-hook-note">
            还不知道对话编号时（hook 没汇报过），会退回预设的启动命令，新开一个对话。
          </span>
        </label>

        <div class="space-y-1">
          <div class="flex items-center justify-between text-[11px] font-medium text-foreground">
            <label for="guard-prompt">唤醒提示词（可选）</label>
            <span class="font-mono font-normal" :class="promptCount > glimits.promptMax ? 'text-destructive' : 'text-muted-foreground'">{{ promptCount }}/{{ glimits.promptMax }}</span>
          </div>
          <textarea
            id="guard-prompt"
            ref="promptInput"
            v-model="gform.prompt"
            rows="3"
            spellcheck="false"
            :placeholder="glimits.promptDefault || '留空使用内置的默认提示词'"
            :disabled="saving"
            :aria-invalid="!!gshown('prompt')"
            :class="[fieldClass, gshown('prompt') ? 'border-destructive' : 'border-border/80', 'resize-y py-1.5 leading-relaxed']"
            data-testid="guard-prompt"
            @blur="gtouched.prompt = true"
          />
          <span v-if="gshown('prompt')" class="block text-[11px] text-destructive" role="alert" data-testid="guard-error-prompt">{{ gshown('prompt') }}</span>
          <span v-else class="block text-[11px] leading-relaxed text-muted-foreground">
            开头会固定加上「【系统唤醒 第几次/上限 · 原因：…】」。正文是单行文本，可以用 <code class="font-mono">%n</code>（第几次）、<code class="font-mono">%max</code>（上限）、
            <code class="font-mono">%reason</code>（原因）、<code class="font-mono">%workflow</code>（上面的工作流编号）；不能有 shell 特殊字符，因为万一 Cursor CLI 已经退出，这句话会落在普通命令行里。
          </span>
          <button
            v-if="glimits.promptDefault && !gform.prompt.trim()"
            type="button"
            class="cursor-pointer text-[11px] text-primary underline-offset-2 hover:underline"
            data-testid="guard-use-default"
            @click="useDefaultPrompt"
          >
            以内置的默认提示词为起点改写
          </button>
        </div>

        <!-- 非交互唤醒（默认关闭，M3.4）：会话已经结束时，Hub 在主机上运行一次 agent -p --resume，不打开终端 -->
        <div class="space-y-2 rounded-sm border border-border/70 px-2.5 py-2" data-testid="guard-headless-section">
          <label class="flex cursor-pointer items-start gap-2">
            <input v-model="gform.headless" type="checkbox" class="mt-0.5 size-3.5 cursor-pointer accent-primary" :disabled="saving" data-testid="guard-headless" />
            <span class="space-y-0.5">
              <span class="block text-[11px] font-medium text-foreground">允许「非交互唤醒」（默认关闭）</span>
              <span class="block text-[11px] leading-relaxed text-muted-foreground">
                会话已经结束时，启动器里多一个按钮：Hub 经这台主机的执行通道，接着原来的对话运行一次
                <code class="font-mono">agent -p --resume …</code>，不打开终端，结束后在聊天里留一条记录。仍然只在你点了之后才做，
                并且受主机的「模型访问」与执行模式（含「需批准」）约束；和普通唤醒共用 24 小时 3 次的上限，这里是硬上限。
              </span>
            </span>
          </label>
          <div v-if="gform.headless" class="space-y-1.5 pl-5" data-testid="guard-headless-posture">
            <label class="flex cursor-pointer items-start gap-2">
              <input v-model="gform.posture" type="radio" value="allowlist" class="mt-0.5 size-3.5 cursor-pointer accent-primary" :disabled="saving" data-testid="guard-posture-allowlist" />
              <span class="text-[11px] leading-relaxed text-muted-foreground">
                <b class="font-medium text-foreground">只放行白名单内的操作（推荐）</b>：不带 <code class="font-mono">--force</code>。
                要让它读写 RelayMesh，需要先在主机上 CLI 配置的 <code class="font-mono">permissions.allow</code> 里加一条 <code class="font-mono">Mcp(&lt;服务器名&gt;:*)</code>；
                白名单之外的 shell 命令会被拒绝。
              </span>
            </label>
            <label class="flex cursor-pointer items-start gap-2">
              <input v-model="gform.posture" type="radio" value="force" class="mt-0.5 size-3.5 cursor-pointer accent-primary" :disabled="saving" data-testid="guard-posture-force" />
              <span class="text-[11px] leading-relaxed text-muted-foreground">
                <b class="font-medium text-amber-700 dark:text-amber-400">放开全部命令</b>：带 <code class="font-mono">--force</code>，它可以不经询问地运行任何命令。只在你完全信任这个工作区时使用。
              </span>
            </label>
          </div>
        </div>

        <ul v-if="ghints.length" class="space-y-1 rounded-sm border border-amber-500/40 bg-amber-500/5 px-2.5 py-1.5" data-testid="guard-hints">
          <li v-for="h in ghints" :key="h" class="text-[11px] leading-relaxed text-amber-700 dark:text-amber-400">{{ h }}</li>
        </ul>
      </div>
    </section>

    <p v-if="serverError" class="rounded-sm border border-destructive/40 bg-destructive/5 px-2.5 py-1.5 text-xs leading-relaxed text-destructive" role="alert" data-testid="launcher-error-server">
      {{ serverError }}
    </p>
    <p v-if="isRun" class="text-[11px] leading-relaxed text-muted-foreground">预设会决定你点「运行」后主机上运行什么，所以保存时需要再验证一次登录密码。</p>
    <p v-else class="text-[11px] leading-relaxed text-muted-foreground">预设会决定你点「启动」后在主机上运行什么，所以保存时需要再验证一次登录密码。</p>

    <div class="flex flex-wrap items-center justify-end gap-2 pt-1">
      <Button type="button" variant="ghost" size="sm" class="h-8 text-xs" :disabled="saving" data-testid="launcher-cancel" @click="emit('cancel')">取消</Button>
      <Button type="submit" variant="outline" size="sm" class="h-8 text-xs" :disabled="saving" data-testid="launcher-save">
        <Loader2 v-if="saving" class="size-3.5 animate-spin" />
        {{ saving ? '保存中…' : isNew ? '保存' : '保存修改' }}
      </Button>
      <Button
        type="button"
        size="sm"
        class="h-8 gap-1 text-xs"
        :disabled="saving || !startable"
        :title="startable ? '' : isRun ? '这台主机现在不能运行（离线、未启用或暂不支持）' : '这台主机现在不能启动会话'"
        data-testid="launcher-save-start"
        @click="submit(true)"
      >
        <Play class="size-3.5" />{{ isRun ? '保存并运行' : '保存并启动' }}
      </Button>
    </div>
  </form>
</template>
