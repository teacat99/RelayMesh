<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { toast } from 'vue-sonner'
import {
  AlertTriangle,
  BellRing,
  CheckCircle2,
  Copy,
  Eye,
  EyeOff,
  Loader2,
  Save,
  Send,
  Shuffle,
  SlidersHorizontal,
  Smartphone,
  XCircle
} from 'lucide-vue-next'
import { ntfyApi, ntfyErrorMessage, type NtfyConfig, type NtfyKindInfo, type NtfyStatus } from '@/api/ntfy'
import {
  NTFY_AUTH_OPTIONS,
  NTFY_DELAY_OPTIONS,
  NTFY_PRIORITY_EFFECTS,
  NTFY_PRIORITY_OPTIONS,
  buildUpdate,
  describeCounters,
  describeKindTable,
  describeKindTest,
  describeLast,
  formFromConfig,
  groupKinds,
  isDirty,
  kindChoice,
  kindOptions,
  privacyWarnings,
  randomTopic,
  runMinOptions,
  subscribeUrl,
  validateForm,
  withKindChoice,
  type KindChoice,
  type NtfyAuthMode,
  type NtfyForm
} from '@/lib/ntfy'
import { PREVIEW_MODES, type PreviewMode } from '@/lib/notifyPrefs'
import { useNotifyPrefsStore } from '@/stores/notifyPrefs'

// 新消息类推送的正文预览模式（服务端偏好，即时生效，不走下面表单的「保存」）。
const notifyPrefs = useNotifyPrefsStore()
function onPreviewChange(e: Event) {
  void notifyPrefs.setNtfyPreview((e.target as HTMLSelectElement).value as PreviewMode)
}

// 服务端推送：智能体需要你回复、或模型发起的长命令跑完时，由 Hub 直接把提醒发到 ntfy
// （手机 / 桌面上订阅了主题的应用），不依赖任何网页是否打开。
// 配置存在服务端；访问令牌与密码只写不读，这里永远看不到已保存的值。

const loading = ref(true)
const loadError = ref('')
const saved = ref<NtfyConfig | null>(null)
const status = ref<NtfyStatus | null>(null)
const form = ref<NtfyForm | null>(null)
// 服务端的消息类型目录（设置页的「按消息类型设置」表按它画；老服务端没有，表就不出现）
const kinds = ref<NtfyKindInfo[]>([])

const saving = ref(false)
const testing = ref(false)
// 正在试发的消息类型 id（只有点了某一行的「试发」才有值）
const testingKind = ref('')
const showSecret = ref(false)
const formError = ref('')
const testResult = ref<{ ok: boolean; text: string } | null>(null)
// 某一行「试发」的结果：就显示在那一行下面，免得结果落在页面最底下看不见
const kindResult = ref<{ id: string; ok: boolean; text: string } | null>(null)

function apply(view: { config: NtfyConfig; status: NtfyStatus; kinds?: NtfyKindInfo[] }) {
  saved.value = view.config
  status.value = view.status
  kinds.value = view.kinds ?? []
  form.value = formFromConfig(view.config)
  formError.value = ''
}

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    apply(await ntfyApi.get())
  } catch (e) {
    loadError.value = ntfyErrorMessage(e, '读取 ntfy 设置失败')
  } finally {
    loading.value = false
  }
}

onMounted(load)

const dirty = computed(() => !!form.value && !!saved.value && isDirty(form.value, saved.value))
const warnings = computed(() => (form.value ? privacyWarnings(form.value) : []))
const last = computed(() => describeLast(status.value))
const counters = computed(() => describeCounters(status.value))
const subscribe = computed(() => (form.value ? subscribeUrl(form.value.server, form.value.topic) : ''))
// 门槛的下拉选项：当前值不在预设里时补一项，免得下拉框显示成别的值
const runMinChoices = computed(() => runMinOptions(form.value?.runMinSeconds ?? 0))
// 按消息类型的表：分组、「默认」一项要写明落在几级（取决于当前选的默认优先级）、一行小结
const defaultPriority = computed(() => form.value?.priority ?? 0)
const kindGroups = computed(() => groupKinds(kinds.value))
const kindSummary = computed(() => (form.value ? describeKindTable(kinds.value, form.value.priorityByKind, defaultPriority.value) : ''))
const credentialHint = computed(() => {
  const s = saved.value
  const f = form.value
  if (!s || !f) return ''
  if (f.authMode === 'token') return s.token_set ? '已保存令牌；留空表示保持不变，填写则替换' : ''
  if (f.authMode === 'basic') return s.password_set ? '已保存密码；留空表示保持不变，填写则替换' : ''
  return ''
})

// 切换认证方式时丢掉半截输入，也收起「显示」
watch(
  () => form.value?.authMode,
  (_now: NtfyAuthMode | undefined, before) => {
    if (!form.value || before === undefined) return
    form.value.token = ''
    form.value.password = ''
    showSecret.value = false
  }
)

function generateTopic() {
  if (form.value) form.value.topic = randomTopic()
}

function useCurrentOrigin() {
  if (form.value) form.value.clickUrl = window.location.origin + '/'
}

async function copySubscribe() {
  if (!subscribe.value) return
  try {
    await navigator.clipboard.writeText(subscribe.value)
    toast.success('订阅地址已复制')
  } catch {
    toast.error('复制失败，请手动选中复制')
  }
}

async function save() {
  if (!form.value || !saved.value || saving.value) return
  const problem = validateForm(form.value, saved.value, form.value.enabled)
  if (problem) {
    formError.value = problem
    return
  }
  formError.value = ''
  saving.value = true
  try {
    apply(await ntfyApi.update(buildUpdate(form.value, saved.value)))
    testResult.value = null
    kindResult.value = null
    toast.success(form.value?.enabled ? 'ntfy 推送已保存并启用' : 'ntfy 设置已保存')
  } catch (e) {
    formError.value = ntfyErrorMessage(e, '保存失败')
  } finally {
    saving.value = false
  }
}

async function sendTest() {
  if (!form.value || !saved.value || testing.value) return
  const problem = validateForm(form.value, saved.value, true)
  if (problem) {
    formError.value = problem
    return
  }
  formError.value = ''
  testing.value = true
  testResult.value = null
  kindResult.value = null
  try {
    // 带上尚未保存的表单一起试发：先确认通了，再保存
    const res = await ntfyApi.test(buildUpdate(form.value, saved.value))
    status.value = res.status
    testResult.value = res.ok
      ? { ok: true, text: `测试推送已送达 ntfy（用时 ${res.took_ms} 毫秒）。请到订阅了这个主题的设备上确认是否收到。` }
      : { ok: false, text: res.error || '测试推送失败，原因未知' }
  } catch (e) {
    testResult.value = { ok: false, text: ntfyErrorMessage(e, '测试推送失败') }
  } finally {
    testing.value = false
  }
}

function onKindChange(info: NtfyKindInfo, e: Event) {
  if (!form.value) return
  form.value.priorityByKind = withKindChoice(form.value.priorityByKind, info, (e.target as HTMLSelectElement).value as KindChoice)
  if (kindResult.value?.id === info.id) kindResult.value = null
}

// 按这一类现在选的优先级，在手机上试发一条（带上尚未保存的表单，所以不用先保存）。
async function sendKindTest(info: NtfyKindInfo) {
  if (!form.value || !saved.value || testing.value) return
  testResult.value = null
  kindResult.value = null
  const problem = validateForm(form.value, saved.value, true)
  if (problem) {
    // 问题就显示在被点的这一行下面：全局的错误条在页面最底下，点了「试发」的人根本看不到
    kindResult.value = { id: info.id, ok: false, text: problem }
    return
  }
  const choice = kindChoice(info, form.value.priorityByKind)
  testing.value = true
  testingKind.value = info.id
  try {
    const res = await ntfyApi.test(buildUpdate(form.value, saved.value), info.id)
    status.value = res.status
    kindResult.value = {
      id: info.id,
      ok: res.ok,
      text: res.ok ? describeKindTest(info, choice, res) : res.error || '测试推送失败，原因未知'
    }
  } catch (e) {
    kindResult.value = { id: info.id, ok: false, text: ntfyErrorMessage(e, '测试推送失败') }
  } finally {
    testing.value = false
    testingKind.value = ''
  }
}

const inputClass =
  'w-full h-8 px-2.5 text-xs font-mono bg-background border border-border/80 rounded-xs focus:outline-none focus:border-primary text-foreground disabled:opacity-60'
</script>

<template>
  <section class="space-y-3" data-testid="ntfy-settings">
    <div class="flex items-center justify-between pb-1.5 border-b border-border/70">
      <div class="flex items-center gap-1.5">
        <Smartphone class="w-3.5 h-3.5 text-primary" />
        <span class="text-xs font-bold font-mono text-foreground">手机 / 桌面推送（ntfy）</span>
      </div>
      <span
        v-if="saved"
        class="text-[10px] font-mono px-1.5 py-0.5 rounded-xs border"
        :class="saved.enabled ? 'border-primary/40 bg-primary/10 text-primary' : 'border-border/70 text-muted-foreground'"
        data-testid="ntfy-state"
      >{{ saved.enabled ? '已启用' : '未启用' }}</span>
    </div>

    <p class="text-[11px] leading-relaxed text-muted-foreground">
      上面的声音与系统通知只在网页开着、连着服务时才会响。启用 ntfy 后，智能体需要你回复时由服务端直接推送到订阅了主题的手机或电脑——网页没开、手机锁屏也能收到；还可以让模型在主机上跑的长命令结束时也推送一条。
    </p>

    <div v-if="loading" class="flex items-center gap-2 text-xs font-mono text-muted-foreground p-3">
      <Loader2 class="w-3.5 h-3.5 animate-spin" />
      <span>正在读取推送设置…</span>
    </div>

    <div v-else-if="loadError" class="p-2.5 rounded-xs border border-destructive/40 bg-destructive/10 text-destructive text-xs font-mono flex items-center justify-between gap-2" data-testid="ntfy-load-error">
      <span>{{ loadError }}</span>
      <button type="button" class="underline underline-offset-2 cursor-pointer shrink-0" @click="load">重试</button>
    </div>

    <template v-else-if="form && saved">
      <!-- 总开关 -->
      <button
        type="button"
        role="switch"
        :aria-checked="form.enabled"
        data-testid="ntfy-enabled"
        class="w-full p-3 rounded-xs border text-left transition-all font-mono cursor-pointer flex items-center justify-between gap-3"
        :class="form.enabled ? 'border-primary/40 bg-primary/10 text-foreground' : 'border-border/70 bg-card/60 text-muted-foreground'"
        @click="form.enabled = !form.enabled"
      >
        <div class="space-y-0.5">
          <div class="text-xs font-semibold flex items-center gap-1.5">
            <BellRing class="w-3.5 h-3.5 text-primary" />
            <span>启用 ntfy 推送</span>
          </div>
          <div class="text-[10px] text-muted-foreground">总开关：关闭时下面所有消息都不推送；启用后，哪些消息要推、各用什么优先级，在下面分别设置（改完要点「保存」才生效）</div>
        </div>
        <div class="w-8 h-4.5 rounded-full transition-colors flex items-center p-0.5 shrink-0" :class="form.enabled ? 'bg-primary' : 'bg-muted-foreground/30'">
          <div class="w-3.5 h-3.5 rounded-full bg-white transition-transform" :class="form.enabled ? 'translate-x-3.5' : 'translate-x-0'"></div>
        </div>
      </button>

      <div class="p-3.5 rounded-xs border border-border/70 bg-card/60 space-y-3">
        <!-- 服务地址 -->
        <div class="space-y-1">
          <label class="text-xs font-mono font-medium text-foreground" for="ntfy-server">服务地址</label>
          <input id="ntfy-server" v-model="form.server" type="text" autocomplete="off" spellcheck="false" placeholder="https://ntfy.sh" data-testid="ntfy-server" :class="inputClass" />
          <div class="text-[10px] text-muted-foreground">公共服务用 https://ntfy.sh；自建实例填它的地址（可带路径前缀）。服务端所在的机器必须能访问到它。</div>
        </div>

        <!-- 主题 -->
        <div class="space-y-1">
          <label class="text-xs font-mono font-medium text-foreground" for="ntfy-topic">主题</label>
          <div class="flex items-center gap-2">
            <input id="ntfy-topic" v-model="form.topic" type="text" autocomplete="off" spellcheck="false" placeholder="relaymesh-…" data-testid="ntfy-topic" :class="inputClass" />
            <button
              type="button"
              class="h-8 px-2.5 rounded-xs border border-border/80 bg-background text-xs font-mono flex items-center gap-1 shrink-0 cursor-pointer hover:bg-muted transition-colors"
              data-testid="ntfy-gen-topic"
              @click="generateTopic"
            >
              <Shuffle class="w-3 h-3" />
              <span>随机生成</span>
            </button>
          </div>
          <div v-if="subscribe" class="flex items-center gap-1.5 text-[10px] text-muted-foreground font-mono min-w-0">
            <span class="shrink-0">在 ntfy 应用里订阅：</span>
            <code class="truncate text-foreground" data-testid="ntfy-subscribe-url">{{ subscribe }}</code>
            <button type="button" class="shrink-0 cursor-pointer hover:text-foreground" title="复制订阅地址" aria-label="复制订阅地址" @click="copySubscribe">
              <Copy class="w-3 h-3" />
            </button>
          </div>
        </div>

        <!-- 认证 -->
        <div class="space-y-1.5">
          <div class="text-xs font-mono font-medium text-foreground">认证方式</div>
          <div v-if="saved.token_from_env" class="p-2 rounded-xs border border-border/70 bg-background/70 text-[10px] font-mono text-muted-foreground" data-testid="ntfy-env-token">
            访问令牌由服务器的环境变量 <code class="text-foreground">RELAYMESH_NTFY_TOKEN</code> 提供，这里不用填，也无法在界面里清除。
          </div>
          <template v-else>
            <div class="grid grid-cols-3 gap-1.5" role="radiogroup" aria-label="认证方式">
              <button
                v-for="opt in NTFY_AUTH_OPTIONS"
                :key="opt.id"
                type="button"
                role="radio"
                :aria-checked="form.authMode === opt.id"
                :title="opt.hint"
                :data-testid="`ntfy-auth-${opt.id}`"
                class="px-2 py-1.5 rounded-xs border text-[11px] font-mono cursor-pointer transition-all"
                :class="form.authMode === opt.id ? 'border-primary bg-primary text-primary-foreground font-bold' : 'border-border/70 bg-background/70 text-muted-foreground hover:bg-muted'"
                @click="form.authMode = opt.id"
              >{{ opt.label }}</button>
            </div>

            <div v-if="form.authMode === 'token'" class="space-y-1">
              <div class="flex items-center gap-2">
                <input
                  v-model="form.token"
                  :type="showSecret ? 'text' : 'password'"
                  autocomplete="off"
                  spellcheck="false"
                  :placeholder="saved.token_set ? '•••••••• 已保存' : 'tk_…'"
                  aria-label="访问令牌"
                  data-testid="ntfy-token"
                  :class="inputClass"
                />
                <button type="button" class="shrink-0 text-muted-foreground hover:text-foreground cursor-pointer" :aria-label="showSecret ? '隐藏' : '显示'" @click="showSecret = !showSecret">
                  <component :is="showSecret ? EyeOff : Eye" class="w-3.5 h-3.5" />
                </button>
              </div>
            </div>

            <div v-else-if="form.authMode === 'basic'" class="grid grid-cols-1 sm:grid-cols-2 gap-2">
              <input v-model="form.username" type="text" autocomplete="off" spellcheck="false" placeholder="用户名" aria-label="用户名" data-testid="ntfy-username" :class="inputClass" />
              <div class="flex items-center gap-2">
                <input
                  v-model="form.password"
                  :type="showSecret ? 'text' : 'password'"
                  autocomplete="new-password"
                  spellcheck="false"
                  :placeholder="saved.password_set ? '•••••••• 已保存' : '密码'"
                  aria-label="密码"
                  data-testid="ntfy-password"
                  :class="inputClass"
                />
                <button type="button" class="shrink-0 text-muted-foreground hover:text-foreground cursor-pointer" :aria-label="showSecret ? '隐藏' : '显示'" @click="showSecret = !showSecret">
                  <component :is="showSecret ? EyeOff : Eye" class="w-3.5 h-3.5" />
                </button>
              </div>
            </div>
            <div v-if="credentialHint" class="text-[10px] text-muted-foreground" data-testid="ntfy-credential-hint">{{ credentialHint }}。出于安全，已保存的凭据不会再显示。</div>
          </template>
        </div>

        <!-- 点击通知后打开 -->
        <div class="space-y-1">
          <label class="text-xs font-mono font-medium text-foreground" for="ntfy-click">点击通知后打开（可选）</label>
          <div class="flex items-center gap-2">
            <input id="ntfy-click" v-model="form.clickUrl" type="text" autocomplete="off" spellcheck="false" placeholder="https://你的 RelayMesh 地址/" data-testid="ntfy-click" :class="inputClass" />
            <button
              type="button"
              class="h-8 px-2.5 rounded-xs border border-border/80 bg-background text-xs font-mono shrink-0 cursor-pointer hover:bg-muted transition-colors"
              data-testid="ntfy-click-current"
              @click="useCurrentOrigin"
            >用当前地址</button>
          </div>
          <div class="text-[10px] text-muted-foreground">填你在手机上访问 RelayMesh 的地址，点开通知就直接回到控制台。</div>
        </div>

        <!-- 优先级 / 延迟 -->
        <div class="grid grid-cols-1 sm:grid-cols-2 gap-2.5">
          <div class="space-y-1">
            <label class="text-xs font-mono font-medium text-foreground" for="ntfy-priority">默认优先级</label>
            <select id="ntfy-priority" v-model.number="form.priority" data-testid="ntfy-priority" :class="inputClass + ' cursor-pointer'">
              <option v-for="opt in NTFY_PRIORITY_OPTIONS" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
            </select>
          </div>
          <div class="space-y-1">
            <label class="text-xs font-mono font-medium text-foreground" for="ntfy-delay">推送时机</label>
            <select id="ntfy-delay" v-model.number="form.delaySeconds" data-testid="ntfy-delay" :class="inputClass + ' cursor-pointer'">
              <option v-for="opt in NTFY_DELAY_OPTIONS" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
            </select>
          </div>
        </div>
        <div class="text-[10px] text-muted-foreground -mt-1.5 leading-relaxed space-y-0.5">
          <div>默认优先级：下面「按消息类型设置」里选了「跟随默认优先级」的几类用它；其余各类有自己的出厂优先级，也可以单独改。</div>
          <div>延迟推送：这段时间里你在网页上处理掉了这条请求，就不再打扰手机。</div>
        </div>

        <!-- 内容 -->
        <button
          type="button"
          role="switch"
          :aria-checked="form.includeSummary"
          data-testid="ntfy-include-summary"
          class="w-full p-2.5 rounded-xs border text-left transition-all font-mono cursor-pointer flex items-center justify-between gap-3"
          :class="form.includeSummary ? 'border-primary/40 bg-primary/10 text-foreground' : 'border-border/70 bg-background/70 text-muted-foreground'"
          @click="form.includeSummary = !form.includeSummary"
        >
          <div class="space-y-0.5">
            <div class="text-xs font-semibold">通知里附带摘要开头</div>
            <div class="text-[10px] text-muted-foreground">默认只发会话标题、项目与主机名；打开后再附上智能体汇报的前 160 个字（运行结束的推送则附上命令的第一行）</div>
          </div>
          <div class="w-8 h-4.5 rounded-full transition-colors flex items-center p-0.5 shrink-0" :class="form.includeSummary ? 'bg-primary' : 'bg-muted-foreground/30'">
            <div class="w-3.5 h-3.5 rounded-full bg-white transition-transform" :class="form.includeSummary ? 'translate-x-3.5' : 'translate-x-0'"></div>
          </div>
        </button>

        <!-- 新消息类推送（智能体发来的文字、待确认提问、@ 你）的正文预览：服务端偏好，即时生效 -->
        <div class="space-y-1" data-testid="ntfy-preview-block">
          <label class="text-xs font-mono font-medium text-foreground" for="ntfy-preview">新消息推送的正文预览</label>
          <select
            id="ntfy-preview"
            data-testid="ntfy-preview"
            :value="notifyPrefs.ntfyPreview"
            :disabled="notifyPrefs.saving"
            :class="inputClass + ' cursor-pointer'"
            @change="onPreviewChange"
          >
            <option v-for="m in PREVIEW_MODES" :key="m.value" :value="m.value">{{ m.label }} · {{ m.hint }}</option>
          </select>
          <div class="text-[10px] text-muted-foreground leading-relaxed">
            只影响智能体发来的文字、待确认提问与 @ 你；改动即时生效，不需要点「保存」。选「从不显示」时，手机上只会看到「有新消息」，正文留在网页里看。
          </div>
        </div>

        <!-- 模型运行结束 -->
        <button
          type="button"
          role="switch"
          :aria-checked="form.runNotify"
          data-testid="ntfy-run-notify"
          class="w-full p-2.5 rounded-xs border text-left transition-all font-mono cursor-pointer flex items-center justify-between gap-3"
          :class="form.runNotify ? 'border-primary/40 bg-primary/10 text-foreground' : 'border-border/70 bg-background/70 text-muted-foreground'"
          @click="form.runNotify = !form.runNotify"
        >
          <div class="space-y-0.5">
            <div class="text-xs font-semibold">模型运行的命令结束时也推送</div>
            <div class="text-[10px] text-muted-foreground">模型在主机上跑的长命令成功、失败、超时或被中断时推送一条：写明说明、主机、用时与退出码，从不带命令输出</div>
          </div>
          <div class="w-8 h-4.5 rounded-full transition-colors flex items-center p-0.5 shrink-0" :class="form.runNotify ? 'bg-primary' : 'bg-muted-foreground/30'">
            <div class="w-3.5 h-3.5 rounded-full bg-white transition-transform" :class="form.runNotify ? 'translate-x-3.5' : 'translate-x-0'"></div>
          </div>
        </button>
        <div v-if="form.runNotify" class="space-y-1 pl-2.5 border-l-2 border-primary/30" data-testid="ntfy-run-options">
          <label class="text-xs font-mono font-medium text-foreground" for="ntfy-run-min">只推送运行时间不少于</label>
          <select id="ntfy-run-min" v-model.number="form.runMinSeconds" data-testid="ntfy-run-min" :class="inputClass + ' cursor-pointer'">
            <option v-for="opt in runMinChoices" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
          </select>
          <div class="text-[10px] text-muted-foreground leading-relaxed">
            更短的命令不会打扰你。你自己或模型主动取消的运行不推送；主机上的守护进程停止或重启把命令中断了，会照常推送。
          </div>
        </div>

        <!-- 按消息类型设置：每一类单独选「不推送」或优先级；异常提醒也在这里打开 -->
        <div class="space-y-2.5 pt-1" data-testid="ntfy-kinds">
          <div class="space-y-1">
            <div class="text-xs font-mono font-medium text-foreground flex items-center gap-1.5">
              <SlidersHorizontal class="w-3.5 h-3.5 text-primary" />
              <span>按消息类型设置</span>
            </div>
            <div class="text-[10px] text-muted-foreground leading-relaxed">
              每一类消息可以单独选择「不推送」或指定优先级；选「默认」就是出厂设置。改完要点「保存」才生效；「试发」不用先保存，它会按你现在选的优先级在手机上发一条。
            </div>
            <div class="text-[10px] text-muted-foreground leading-relaxed" data-testid="ntfy-priority-effects">{{ NTFY_PRIORITY_EFFECTS }}</div>
            <div v-if="kindSummary" class="text-[10px] font-mono text-foreground" data-testid="ntfy-kinds-summary">{{ kindSummary }}</div>
          </div>

          <div v-if="!kindGroups.length" class="text-[10px] font-mono text-muted-foreground" data-testid="ntfy-kinds-empty">
            当前服务端没有提供消息类型目录，暂时不能按类型设置；上面的默认优先级仍然有效。
          </div>

          <div
            v-for="group in kindGroups"
            :key="group.id"
            class="rounded-xs border border-border/60 bg-background/50 px-2.5 py-2"
            :data-testid="`ntfy-kind-group-${group.id}`"
          >
            <div class="text-[11px] font-mono font-semibold text-foreground">{{ group.title }}</div>
            <div v-if="group.hint" class="text-[10px] text-muted-foreground leading-relaxed">{{ group.hint }}</div>
            <div class="divide-y divide-border/50 mt-1">
              <div v-for="k in group.kinds" :key="k.id" class="py-2 space-y-1.5" :data-testid="`ntfy-kind-${k.id}`">
                <div class="flex flex-col sm:flex-row sm:items-center gap-2">
                  <div class="min-w-0 flex-1">
                    <div class="text-xs font-mono font-semibold text-foreground">{{ k.label }}</div>
                    <div class="text-[10px] text-muted-foreground leading-relaxed">{{ k.desc }}</div>
                  </div>
                  <div class="flex items-center gap-2 sm:w-[21rem] shrink-0 min-w-0">
                    <select
                      :value="kindChoice(k, form.priorityByKind)"
                      :aria-label="`「${k.label}」怎么推送`"
                      :data-testid="`ntfy-kind-select-${k.id}`"
                      :class="inputClass + ' cursor-pointer min-w-0'"
                      @change="onKindChange(k, $event)"
                    >
                      <option v-for="o in kindOptions(k, defaultPriority)" :key="o.value" :value="o.value">{{ o.label }}</option>
                    </select>
                    <button
                      type="button"
                      class="h-8 px-2.5 rounded-xs border border-border/80 bg-background text-xs font-mono flex items-center gap-1 shrink-0 cursor-pointer hover:bg-muted transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
                      :disabled="saving || testing"
                      :aria-label="`试发「${k.label}」`"
                      :title="`按现在选的优先级，在手机上试发一条「${k.label}」`"
                      :data-testid="`ntfy-kind-test-${k.id}`"
                      @click="sendKindTest(k)"
                    >
                      <Loader2 v-if="testingKind === k.id" class="w-3 h-3 animate-spin" />
                      <Send v-else class="w-3 h-3" />
                      <span>试发</span>
                    </button>
                  </div>
                </div>
                <div
                  v-if="kindResult && kindResult.id === k.id"
                  class="p-2 rounded-xs border text-[10px] font-mono flex items-start gap-1.5"
                  :class="kindResult.ok ? 'bg-emerald-500/10 border-emerald-500/30 text-emerald-600 dark:text-emerald-400' : 'bg-destructive/10 border-destructive/30 text-destructive'"
                  role="status"
                  data-testid="ntfy-kind-result"
                >
                  <component :is="kindResult.ok ? CheckCircle2 : XCircle" class="w-3.5 h-3.5 shrink-0 mt-px" />
                  <span class="leading-normal">{{ kindResult.text }}</span>
                </div>
              </div>
            </div>
          </div>
        </div>

        <!-- 提醒 -->
        <div v-if="warnings.length" class="p-2 rounded-xs bg-amber-500/10 border border-amber-500/30 text-amber-600 dark:text-amber-400 text-[10px] font-mono space-y-1" data-testid="ntfy-warnings">
          <div v-for="w in warnings" :key="w" class="flex items-start gap-1.5">
            <AlertTriangle class="w-3.5 h-3.5 shrink-0 mt-px text-amber-500" />
            <span class="leading-normal">{{ w }}</span>
          </div>
        </div>

        <!-- 表单错误 -->
        <div v-if="formError" class="p-2 rounded-xs bg-destructive/10 border border-destructive/30 text-destructive text-xs font-mono flex items-start gap-1.5" role="alert" data-testid="ntfy-form-error">
          <XCircle class="w-3.5 h-3.5 shrink-0 mt-px" />
          <span>{{ formError }}</span>
        </div>

        <!-- 操作 -->
        <div class="flex flex-wrap items-center gap-2">
          <button
            type="button"
            class="h-8 px-3 rounded-xs border border-primary bg-primary text-primary-foreground text-xs font-mono font-bold flex items-center gap-1.5 cursor-pointer hover:opacity-90 transition-opacity disabled:opacity-50 disabled:cursor-not-allowed"
            :disabled="saving || testing || !dirty"
            data-testid="ntfy-save"
            @click="save"
          >
            <Loader2 v-if="saving" class="w-3.5 h-3.5 animate-spin" />
            <Save v-else class="w-3.5 h-3.5" />
            <span>保存</span>
          </button>
          <button
            type="button"
            class="h-8 px-3 rounded-xs border border-border/80 bg-background text-xs font-mono flex items-center gap-1.5 cursor-pointer hover:bg-muted transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
            :disabled="saving || testing"
            data-testid="ntfy-test"
            @click="sendTest"
          >
            <Loader2 v-if="testing" class="w-3.5 h-3.5 animate-spin" />
            <Send v-else class="w-3.5 h-3.5" />
            <span>{{ testing ? '正在发送…' : dirty ? '用当前设置发送测试推送' : '发送测试推送' }}</span>
          </button>
          <span v-if="dirty" class="text-[10px] font-mono text-amber-600 dark:text-amber-400" data-testid="ntfy-dirty">有尚未保存的修改</span>
        </div>

        <!-- 测试结果 -->
        <div
          v-if="testResult"
          class="p-2 rounded-xs border text-xs font-mono flex items-start gap-1.5"
          :class="testResult.ok ? 'bg-emerald-500/10 border-emerald-500/30 text-emerald-600 dark:text-emerald-400' : 'bg-destructive/10 border-destructive/30 text-destructive'"
          role="status"
          data-testid="ntfy-result"
        >
          <component :is="testResult.ok ? CheckCircle2 : XCircle" class="w-3.5 h-3.5 shrink-0 mt-px" />
          <span class="leading-normal">{{ testResult.text }}</span>
        </div>

        <!-- 最近一次 -->
        <div class="text-[10px] font-mono space-y-0.5 pt-1 border-t border-border/50">
          <div data-testid="ntfy-last" :class="last.tone === 'fail' ? 'text-destructive' : last.tone === 'ok' ? 'text-emerald-600 dark:text-emerald-400' : 'text-muted-foreground'">{{ last.text }}</div>
          <div v-if="counters" class="text-muted-foreground" data-testid="ntfy-counters">{{ counters }}</div>
        </div>
      </div>
    </template>
  </section>
</template>
