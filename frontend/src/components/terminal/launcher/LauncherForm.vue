<script setup lang="ts">
import { computed, nextTick, onMounted, reactive, ref, watch } from 'vue'
import { History, Loader2, Pin, Play, Sparkles } from 'lucide-vue-next'
import Button from '../../ui/button/Button.vue'
import WakePreview from '../../feedback/WakePreview.vue'
import type { HostView, LauncherAfterNew, LauncherSource, TerminalLauncher } from '../../../api/terminal'
import { hostTitle, runLimitText, shortCode } from '../../../lib/terminal/format'
import {
  AUTO_RESUME_OK_DEFAULT_MINUTES,
  AUTO_RESUME_OK_MAX_MINUTES,
  RUN_LIMIT_UNITS,
  charCount,
  collapseSpaces,
  dirAdvice,
  dirPlaceholder,
  draftSource,
  hasErrors,
  hostPlatformOf,
  hostReadiness,
  isMacHost,
  isWindowsHost,
  launcherHosts,
  toLauncher,
  validateDraft,
  type DraftField,
  type LauncherDraft,
  type LauncherLimits,
} from '../../../lib/terminal/launcher'

/**
 * 预设的编辑表单：名字、主机、目录，加上「非交互续聊」预设（#126）的一组运行选项（没有命令，运行的命令由选项拼出来）。
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
}>()

const emit = defineEmits<{
  (e: 'save', l: TerminalLauncher, thenStart: boolean): void
  (e: 'cancel'): void
}>()

const form = reactive<LauncherDraft>({ ...props.draft })
watch(
  () => props.draft,
  d => Object.assign(form, d),
)

const touched = reactive<Record<DraftField, boolean>>({
  name: false, host: false, dir: false, bin: false, model: false, workflow: false, conversation: false, prompt: false, maxRun: false,
  autoResumeOk: false,
})
const submitted = ref(false)

// ---------- 续聊预设的运行选项 ----------
// 交互式「终端」预设已经停用（#245）：表单只剩「非交互续聊」，新建时不再选类型。
const runPromptCount = computed(() => charCount(collapseSpaces(form.run.prompt ?? '')))
// 对话来源（#191）：新会话 / 最近会话 / 指定会话，三选一。草稿里没有来源（会话页「运行」那种）就是指定会话。
const runSource = computed<LauncherSource>(() => draftSource(form.run))
// 「新会话」（#176）用另一份默认提示词：它要让新开的智能体先接进这个工作流，而不是「恢复现场」；最近会话是续聊，用续聊的那份
const runPromptDefault = computed(() => (runSource.value === 'new' ? props.limits.runNewPromptDefault : props.limits.runPromptDefault))
const useDefaultRunPrompt = () => {
  if (!form.run) return
  form.run.prompt = runPromptDefault.value
  touched.prompt = true
}
// 预览这段正文发出去会是什么样（#232 D3）：续聊 / 新开对话是两类唤醒词；旧版 Hub 没有预览接口时不显示
const runWakeKind = computed(() => (runSource.value === 'new' ? 'new' : 'resume'))
const runWakeNote = '预设里自己写的正文原样发出；内置默认之后，运行时还会自动补一句本次的最长运行时间（想把它放在正文里的某个位置，写 %limit）。'
// 只有 Hub 认识「新会话」，或者这条预设本来就不是指定会话（回滚到旧版 Hub 后编辑，不能悄悄把它改掉）才显示三选一；
// 另外两种来源下对话编号输入框是藏起来的，藏着的内容不会被保存（见 lib/terminal/launcher.ts 的 toRun）
const showSourcePicker = computed(() => !!props.limits.runNewPromptDefault || runSource.value !== 'resume')
interface SourceOption { value: LauncherSource; label: string; hint: string; icon: typeof Sparkles }
const SOURCE_OPTIONS: readonly SourceOption[] = [
  { value: 'new', label: '新会话', hint: '新开一个对话，记下它的编号', icon: Sparkles },
  { value: 'continue', label: '最近会话', hint: '接着目录里最近的对话（--continue）', icon: History },
  { value: 'resume', label: '指定会话', hint: '固定编号，或跟随工作流最近的对话', icon: Pin },
]
function setSource(v: LauncherSource) {
  if (!form.run || form.run.source === v) return
  form.run.source = v
  if (v === 'new' && !form.run.afterNew) form.run.afterNew = 'remember'
}
// 「新会话」运行之后的去向（#559）
interface AfterNewOption { value: LauncherAfterNew; label: string; hint: string }
const AFTER_NEW_OPTIONS: readonly AfterNewOption[] = [
  { value: 'remember', label: '记下这次的对话编号，之后接着它（默认）', hint: '只新开这一次，之后点「运行」都接着它' },
  { value: 'continue', label: '切到「最近会话」', hint: '运行成功之后预设自动改成「最近会话」（--continue）；失败的运行不切换，下次仍是新会话' },
  { value: 'stay', label: '不切换', hint: '每次运行都新开一个对话，不接着上一次的' },
]
function setAfterNew(v: LauncherAfterNew) {
  if (form.run) form.run.afterNew = v
}
const runAfterNew = computed<LauncherAfterNew>(() => (form.run?.afterNew === 'stay' || form.run?.afterNew === 'continue' ? form.run.afterNew : 'remember'))
const createdByEnable = computed(() => form.run?.origin === 'enable_agent')

const errors = computed(() => validateDraft(form, { limits: props.limits, others: props.others }))
const shown = (f: DraftField) => (touched[f] || submitted.value ? errors.value[f] : undefined)

const hostOptions = computed(() => launcherHosts(props.hosts))
// 预设里的主机现在不在可选列表里（离线不算，被删除 / 停用 / 是 SSH 主机才算）：保留一个只读的选项，免得下拉框显示成空白、保存时悄悄换了主机。
const orphanHost = computed(() => (form.host && !hostOptions.value.some(h => h.machine_code === form.host) ? form.host : ''))
const selectedHost = computed(() => props.hosts.find(h => h.machine_code === form.host))
// 目录怎么写的提醒跟着所选主机的系统走（#163）。
const platform = computed(() => hostPlatformOf(selectedHost.value))
const hostIsWindows = computed(() => isWindowsHost(platform.value.os))
const hostIsMac = computed(() => isMacHost(platform.value.os))
const dirNote = computed(() => dirAdvice(form.dir ?? '', platform.value))
const startable = computed(() => hostReadiness(selectedHost.value).ok)

const nameInput = ref<HTMLInputElement | null>(null)
const hostSelect = ref<HTMLSelectElement | null>(null)
const dirInput = ref<HTMLInputElement | null>(null)
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

onMounted(() => void nextTick(() => nameInput.value?.focus()))

function submit(thenStart: boolean) {
  if (props.saving) return
  submitted.value = true
  const e = errors.value
  if (hasErrors(e)) {
    const order: DraftField[] = ['name', 'host', 'dir', 'workflow', 'conversation', 'model', 'maxRun', 'autoResumeOk', 'bin', 'prompt']
    const first = order.find(f => e[f])
    if (first) fieldRefs[first]()?.focus()
    return
  }
  emit('save', toLauncher(form), thenStart)
}

const fieldClass =
  'w-full rounded-xs border bg-background px-2 text-xs text-foreground placeholder:text-muted-foreground focus:border-primary focus:outline-none focus:ring-1 focus:ring-primary/30 disabled:opacity-60'
const border = (f: DraftField) => (shown(f) ? 'border-destructive' : 'border-border/80')

const nameCount = computed(() => charCount(form.name))
</script>

<template>
  <form class="space-y-3" data-testid="launcher-form" data-kind="headless" @submit.prevent="submit(false)">
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
      <span v-else class="block text-[11px] leading-relaxed text-muted-foreground">它会出现在 ExecMesh 的运行记录里（线程名）和聊天里的系统行里。</span>
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
      <span class="text-[11px] font-medium text-foreground">目录</span>
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
      <span v-else-if="hostIsWindows" class="block text-[11px] leading-relaxed text-muted-foreground" data-testid="launcher-dir-help-run-win">
        必须是主机上的绝对路径（盘符开头，如 <code class="font-mono">C:\work\项目</code>，或 <code class="font-mono">\\服务器\共享\…</code>）：续聊在这个目录里运行（<code class="font-mono">--workspace</code>），要和原对话所在的工作区一致。
        路径里不能有 <code class="font-mono">" % ^ &amp; | &lt; &gt; ! * ?</code>。
      </span>
      <span v-else class="block text-[11px] leading-relaxed text-muted-foreground">
        必须是主机上的绝对路径（以 <code class="font-mono">/</code> 开头）：续聊在这个目录里运行（<code class="font-mono">--workspace</code>），要和原对话所在的工作区一致。
      </span>
      <span v-if="dirNote" class="block text-[11px] leading-relaxed text-amber-600 dark:text-amber-400" data-testid="launcher-dir-advice">{{ dirNote }}</span>
    </label>

    <!-- 非交互续聊（#126）：没有命令，运行的命令由下面的选项拼出来；预设里只存选项，不存任意命令 -->
    <section
      v-if="form.run"
      class="space-y-3 rounded-md border border-border/70 bg-muted/10 px-3 py-3"
      data-testid="run-section"
    >
      <p v-if="hostIsWindows" class="text-[11px] leading-relaxed text-muted-foreground" role="status" data-testid="run-windows-note">
        这台主机是 Windows：续聊命令由 Hub 按 PowerShell 的写法拼好，经这台主机的 PowerShell 运行（不是 bash）。agent 常是 <code class="font-mono">agent.cmd</code> 这样的批处理，
        所以提示词里的双引号会换成单引号，<code class="font-mono">% ^ &amp; | &lt; &gt; !</code> 会换成全角字符（否则 cmd.exe 会当成命令语法）——真正运行时，确认框 / 预览与运行回执里会写明换了几处。
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

      <!-- 对话来源（#191）：新会话 / 最近会话 / 指定会话，三选一；选中的那一档下面写它的说明与附加设置 -->
      <div v-if="showSourcePicker" class="space-y-1.5" data-testid="run-source">
        <span id="run-source-label" class="text-[11px] font-medium text-foreground">对话来源</span>
        <div class="grid grid-cols-3 gap-1.5" role="radiogroup" aria-labelledby="run-source-label">
          <button
            v-for="o in SOURCE_OPTIONS"
            :key="o.value"
            type="button"
            role="radio"
            :aria-checked="runSource === o.value"
            class="flex cursor-pointer items-start gap-1.5 rounded-md border px-2 py-1.5 text-left transition-colors"
            :class="runSource === o.value ? 'border-primary/60 bg-primary/5' : 'border-border/70 hover:bg-muted/30'"
            :disabled="saving"
            :data-testid="`run-source-${o.value}`"
            @click="setSource(o.value)"
          >
            <component :is="o.icon" class="mt-0.5 size-3.5 shrink-0" :class="runSource === o.value ? 'text-primary' : 'text-muted-foreground'" aria-hidden="true" />
            <span class="min-w-0 space-y-0.5">
              <span class="block text-[11px] font-medium text-foreground">{{ o.label }}</span>
              <span class="block text-[11px] leading-snug text-muted-foreground">{{ o.hint }}</span>
            </span>
          </button>
        </div>

        <!-- 新会话：命令里没有 --resume，新开的智能体按提示词带着上面的工作流编号接入；运行之后的去向三选一（#559） -->
        <div v-if="runSource === 'new'" class="space-y-1.5 rounded-md border border-border/60 bg-muted/20 p-2.5" data-testid="run-source-new-detail">
          <p class="text-[11px] leading-relaxed text-muted-foreground">
            命令里不带 <code class="font-mono">--resume</code>，在主机上无人值守地新开一个 Cursor CLI 对话，它按提示词带着上面的工作流编号接入 RelayMesh；
            Hub 从运行输出里读到新对话的编号。工作流里已经有智能体在工作、或上一次运行还没结束时会拒绝，不会开出第二个。
          </p>
          <div class="space-y-1" role="radiogroup" aria-label="新会话运行成功之后" data-testid="run-after-new">
            <span class="block text-[11px] font-medium text-foreground">运行成功之后</span>
            <label v-for="a in AFTER_NEW_OPTIONS" :key="a.value" class="flex cursor-pointer items-start gap-2">
              <input
                type="radio"
                name="run-after-new"
                :value="a.value"
                class="mt-0.5 size-3.5 cursor-pointer accent-primary"
                :checked="runAfterNew === a.value"
                :disabled="saving"
                :data-testid="`run-after-new-${a.value}`"
                @change="setAfterNew(a.value)"
              />
              <span class="text-[11px] leading-relaxed text-muted-foreground">
                <b class="font-medium text-foreground">{{ a.label }}</b>：{{ a.hint }}
              </span>
            </label>
          </div>
        </div>

        <!-- 最近会话：接哪个对话由 Cursor CLI 决定——风险要写在明处 -->
        <div
          v-else-if="runSource === 'continue'"
          class="space-y-1 rounded-md border border-amber-500/40 bg-amber-500/5 p-2.5 text-[11px] leading-relaxed text-muted-foreground"
          role="note"
          data-testid="run-source-continue-detail"
        >
          <p>
            <b class="font-medium text-foreground">接着这个目录里最近动过的对话，由 Cursor CLI 决定，可能不是这个会话的。</b>
            命令里用 <code class="font-mono">--continue</code> 代替 <code class="font-mono">--resume</code>。
          </p>
          <p>
            编辑器里开的窗口、别的工作流的运行、你手敲的命令都算「最近」；<b class="font-medium text-foreground">同一个目录有多个会话时，请改用「指定会话」</b>。
            Hub 在运行前不知道它会接到哪个，只能按这个工作流已知的对话做近似的保护；实际接上的对话会写在运行记录里。目录里没有旧对话时运行会失败，并提示「没有可接着的旧对话」。
          </p>
        </div>
      </div>

      <!-- 指定会话：固定一个对话编号，或留空跟随工作流里最近汇报的对话 -->
      <label v-if="runSource === 'resume'" class="block space-y-1">
        <span class="text-[11px] font-medium text-foreground">对话编号（可选）</span>
        <input
          ref="runConversationInput"
          v-model="form.run.conversation"
          type="text"
          autocomplete="off"
          spellcheck="false"
          placeholder="留空 = 工作流里最近一次汇报的对话"
          :disabled="saving"
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
      <p v-if="createdByEnable" class="text-[11px] leading-relaxed text-foreground/80" data-testid="run-origin-note">这条预设由会话页的「启用 Agent」创建；保存时来源标记会原样保留。</p>

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
          单行文本（换行会折成空格），可以用 <code class="font-mono">%workflow</code>（上面的工作流编号）等变量：未读追加、任务清单、下一步……
          发出那一刻由 Hub 换成这个会话当时的真实情况，全部变量见「设置 › 会话提示词」。留空 = 用下一层：这个会话的「唤醒词」模板 → 设置里的全局覆盖 → 内置默认；
          内置默认会让智能体先恢复现场，并要求它无论如何都向 RelayMesh 发一条消息说明结果——非交互运行结束后没有人在终端里看输出；
          运行时还会自动补一句本次的最长运行时间，让它在到点之前先汇报进度（自己写的正文不会被加东西）。
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
        <WakePreview
          :kind="runWakeKind"
          :template="collapseSpaces(form.run.prompt)"
          preset
          :workflow-id="form.run.workflow"
          testid="run-wake"
          :open-by-default="false"
          :note="runWakeNote"
        />
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
        <span v-else-if="hostIsWindows" class="block text-[11px] leading-relaxed text-muted-foreground" data-testid="run-bin-help-win">
          命令名或路径；留空是 <code class="font-mono">agent</code>，旧版叫 <code class="font-mono">cursor-agent</code>，按那台主机上装的写。
          运行时由 PowerShell 执行，按 <code class="font-mono">PATH</code> 找命令；找不到时这里写完整路径，例如 <code class="font-mono">C:\tools\agent.cmd</code>
          （不能是 cmd、powershell 这类 shell，路径里不能有 <code class="font-mono">" % ^ &amp; | &lt; &gt; ! * ?</code>）。
        </span>
        <span v-else class="block text-[11px] leading-relaxed text-muted-foreground">
          命令名或路径；留空是 <code class="font-mono">agent</code>，旧版叫 <code class="font-mono">cursor-agent</code>，按那台主机上装的写。
          运行时由登录 shell（bash）执行，只读 <code class="font-mono">.bash_profile</code> / <code class="font-mono">.profile</code>，不读 <code class="font-mono">.zshrc</code>：
          {{ hostIsMac ? 'macOS 上' : '' }}找不到命令时，这里写完整路径，例如 <code class="font-mono">~/.local/bin/agent</code>。
        </span>
      </label>
    </section>

    <p v-if="serverError" class="rounded-sm border border-destructive/40 bg-destructive/5 px-2.5 py-1.5 text-xs leading-relaxed text-destructive" role="alert" data-testid="launcher-error-server">
      {{ serverError }}
    </p>
    <p class="text-[11px] leading-relaxed text-muted-foreground">预设会决定你点「运行」后主机上运行什么，所以保存时需要再验证一次登录密码。</p>

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
        :title="startable ? '' : '这台主机现在不能运行（离线、未启用、已删除或是 SSH 主机）'"
        data-testid="launcher-save-start"
        @click="submit(true)"
      >
        <Play class="size-3.5" />保存并运行
      </Button>
    </div>
  </form>
</template>
