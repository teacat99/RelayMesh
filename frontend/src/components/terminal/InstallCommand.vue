<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { Check, Copy, Hourglass, KeyRound, ShieldAlert, ShieldCheck, TriangleAlert } from 'lucide-vue-next'
import { toast } from 'vue-sonner'
import { useTerminalStore } from '../../stores/terminal'
import {
  CERT_SUMMARY,
  buildInstallCommand,
  chooseSource,
  inferDefault,
  isValidAutoWindow,
  parseSshAllow,
  type CertMode,
  type RunMode,
  type Scope,
  type SourceChoice,
  type TargetOS,
} from '../../lib/terminal/installCommand'
import { copyText } from '../../lib/terminal/clipboard'
import { formatCountdown } from '../../lib/terminal/format'

// 生成「在目标主机上执行」的 Agent 安装命令，或（action="upgrade"）升级已安装 Agent 的命令。
// 服务端地址默认取自管理员当前的访问方式，但目标主机往往在别处，所以主机名 / 端口可以改。
// 证书方式同样按访问方式自动选择，界面上只显示结论；确有需要时点「修改」才展开选项。
//
// 升级模式（由升级对话框使用）：目标系统固定为那台主机的系统，来源是那台主机的升级窗口票据（不是发现窗口），
// 运行方式 / SSH 网关这类安装选项不出现，因为升级只换程序、不改服务配置。
const props = withDefaults(
  defineProps<{
    action?: 'install' | 'upgrade'
    /** 升级模式：目标主机的系统（固定，不可改）。 */
    lockedOs?: TargetOS
    /** 升级模式：这台主机升级窗口的票据；缺省表示窗口没开。 */
    upgradeTicket?: string
    /** 升级模式：预选的升级范围（用户可以改）。 */
    defaultScope?: 'system' | 'user'
  }>(),
  { action: 'install', lockedOs: undefined, upgradeTicket: '', defaultScope: 'system' },
)
const isUpgrade = computed(() => props.action === 'upgrade')
const store = useTerminalStore()

const pin = computed(() => store.status?.tls_pin ?? '')
const httpsPort = computed(() => store.status?.https_port ?? null)

const inferred = computed(() =>
  inferDefault({ protocol: location.protocol, hostname: location.hostname, port: location.port }, httpsPort.value, pin.value),
)

const form = reactive({
  os: (props.lockedOs ?? 'linux') as TargetOS,
  mode: 'service' as RunMode,
  scope: (isUpgrade.value ? props.defaultScope : 'system') as Scope,
  cert: inferred.value.cert as CertMode,
  host: inferred.value.host,
  port: inferred.value.port === null ? '' : String(inferred.value.port),
  // SSH 网关（默认开启，不分版本）：同一个 Agent 的标配能力，不是另一个安装包。取消勾选才会在命令里写 --no-ssh-gateway；
  // allowText 是 --ssh-allow 的白名单输入。
  gateway: true,
  allowText: '',
  // 自动升级（默认不开启）：装好后同时开启——提示（只提示新版本）/ 静默（在允许的时间段内自己下载、验签、升级）。
  // 同意写在主机本地，Hub 改不了；只有 Linux 的常驻服务、且命令来自服务端自带安装源时可用。
  autoMode: 'off' as 'off' | 'notify' | 'silent',
  autoWindow: '',
})

// 用户没有手动选过证书方式时，跟随自动判断：状态是异步加载的，指纹 / 端口到达后可能从明文改为更安全的指纹校验。
const certTouched = ref(false)
const editingCert = ref(false)
watch(inferred, d => {
  if (!certTouched.value) form.cert = d.cert
})

// 切换证书方式时，把端口联动到对应的默认值（内置 HTTPS 端口 / 页面端口），用户仍可手动改。
watch(
  () => form.cert,
  cert => {
    if (cert === 'pinned' && httpsPort.value) form.port = String(httpsPort.value)
    else if (cert === 'insecure') form.port = location.protocol === 'http:' && location.port ? location.port : '18775'
    else if (cert === 'trusted') form.port = location.protocol === 'https:' && location.port ? location.port : ''
  },
)

const osOptions: { id: TargetOS; label: string }[] = [
  { id: 'linux', label: 'Linux' },
  { id: 'macos', label: 'macOS' },
  { id: 'windows', label: 'Windows' },
]
const modeOptions: { id: RunMode; label: string }[] = [
  { id: 'service', label: '常驻服务' },
  { id: 'temporary', label: '临时运行' },
]
// 升级模式下，换到另一台主机（或它的系统 / 默认范围变了）就重新套用，不沿用上一台的选择。
watch(
  () => [props.lockedOs, props.defaultScope] as const,
  ([os, scope]) => {
    if (!isUpgrade.value) return
    if (os) form.os = os
    form.scope = scope
  },
)
const scopeOptions = computed<{ id: Scope; label: string; hint: string }[]>(() => {
  const windows = form.os === 'windows'
  if (isUpgrade.value) {
    // 升级只认「装在哪」：系统级服务 / 计划任务，还是当前用户的；「当前用户 + 最高权限」的任务也属于后者（命令一样，只是需要管理员 PowerShell）。
    return [
      {
        id: 'system',
        label: windows ? '系统级（需要管理员）' : '系统级（root 或 sudo）',
        hint: windows ? '升级 SYSTEM 账户运行的计划任务；需要管理员 PowerShell' : '升级 systemd / launchd 管理的系统服务；命令会自己判断：root 直接运行，普通用户有 sudo 才加 sudo',
      },
      {
        id: 'user',
        label: '仅当前用户',
        hint: '升级当前用户自己的服务；必须由安装它的那个账户执行',
      },
    ]
  }
  const opts: { id: Scope; label: string; hint: string }[] = [
    {
      id: 'system',
      label: windows ? '系统级（需要管理员）' : '系统级（root 或 sudo）',
      hint: windows
        ? '以 SYSTEM 账户开机自启，注销后继续运行；终端权限等同本机最高'
        : '由 systemd / launchd 管理，开机自启；需要 root。命令会自己判断：root 直接运行，普通用户有 sudo 才加 sudo，不用手动改',
    },
    {
      id: 'user',
      label: '仅当前用户',
      hint: windows
        ? '登录 Windows 后自启，注销后停止；不需要管理员，Shell 为普通用户权限'
        : form.os === 'linux'
          ? '仅当前用户的服务；不需要 root。安装时会自动开启 linger，退出登录后服务继续运行、开机后无需登录也会启动'
          : '仅当前用户的服务；不需要 root',
    },
  ]
  if (windows) {
    opts.push({
      id: 'user-admin',
      label: '当前用户 + 最高权限',
      hint: '以你的账户身份运行，并在登录时自动带上完整的管理员令牌（无 UAC 弹窗）；安装需要管理员 PowerShell',
    })
  }
  return opts
})
// 「当前用户 + 最高权限」只存在于 Windows：切到别的系统时退回「仅当前用户」，避免留下一个无效的选择
watch(
  () => form.os,
  os => {
    if (os !== 'windows' && form.scope === 'user-admin') form.scope = 'user'
  },
)
const certOptions: { id: CertMode; label: string }[] = [
  { id: 'pinned', label: '内置 HTTPS（指纹校验）' },
  { id: 'trusted', label: '受信任证书（域名 / 反向代理）' },
  { id: 'insecure', label: '明文 HTTP（仅限内网）' },
]
const certSummary = computed(() => CERT_SUMMARY[form.cert])
const certReason = computed(() => (certTouched.value ? '已手动选择。' : `已根据你当前的访问方式自动选择：${inferred.value.reason}。`))

// ---------- 安装来源 ----------
// 票据随「发现窗口」生灭：窗口没开就没有可用的命令；以实时的窗口状态为准，不信任 /status 里缓存的票据。
const windowActive = computed(() => !!(store.discovery?.open || store.discovery?.always))
const install = computed(() => store.status?.install ?? null)
const source = computed<SourceChoice>(() => {
  // 升级：来源只有一个——这台主机升级窗口的票据（服务端一定附带了安装文件，否则升级对话框不会给出命令）。
  if (isUpgrade.value) {
    return props.upgradeTicket
      ? { kind: 'hub', hub: { ticket: props.upgradeTicket, spki: install.value?.spki_pin ?? '' } }
      : { kind: 'waiting', reason: 'window-closed' }
  }
  return chooseSource(form.os, install.value, windowActive.value)
})

// 窗口已经开了但还没拿到票据（例如在别处开的）：主动刷新一次状态，而不是干等下一次轮询。
watch(
  [windowActive, () => install.value?.available, () => install.value?.ticket],
  ([active, available, ticket]) => {
    if (isUpgrade.value) return
    if (active && available && !ticket) void store.loadStatus()
  },
  { immediate: true },
)

// 自动升级选项的可用性：Linux + 常驻服务 + 服务端自带的安装源（GitHub 上的 Agent 可能还不认识 --auto-upgrade）。
const autoUpgradeAvailable = computed(() => !isUpgrade.value && form.os === 'linux' && form.mode === 'service')
const autoUpgradeUsable = computed(() => autoUpgradeAvailable.value && source.value.kind === 'hub')
const autoModeOptions: { id: 'off' | 'notify' | 'silent'; label: string; hint: string }[] = [
  { id: 'off', label: '不开启', hint: '默认：这台主机不会检查新版本，需要时手动升级；以后也可以在主机上自己开启' },
  { id: 'notify', label: '提示新版本', hint: 'Agent 定期问 Hub 有没有更新的版本，只在日志与主机卡片上提示，不下载、不安装' },
  { id: 'silent', label: '静默升级', hint: '在允许的时间段内自己下载、验证发布签名并升级；没有签名或签名不对的版本一律不安装' },
]
const autoWindowInvalid = computed(() => form.autoMode === 'silent' && !isValidAutoWindow(form.autoWindow.trim()))

const result = computed(() => {
  const portText = form.port.trim()
  const port = portText === '' ? null : Number(portText)
  return buildInstallCommand({
    action: props.action,
    os: form.os,
    mode: isUpgrade.value ? 'service' : form.mode,
    scope: form.scope,
    cert: form.cert,
    host: form.host,
    port,
    pin: pin.value,
    hub: source.value.kind === 'hub' ? source.value.hub : null,
    // 临时运行不提供网关（一次性身份），选项不参与；常驻安装默认开启，只有取消勾选或填了允许名单才会改变命令。
    sshGateway: !isUpgrade.value && form.mode === 'service' ? { enabled: form.gateway, allow: form.gateway ? parseSshAllow(form.allowText) : [] } : null,
    autoUpgrade:
      autoUpgradeUsable.value && form.autoMode !== 'off'
        ? { mode: form.autoMode, window: form.autoMode === 'silent' ? form.autoWindow.trim() : '' }
        : null,
  })
})
const showCommand = computed(() => source.value.kind !== 'waiting' && !!result.value.command)

const remaining = computed(() => (store.discovery?.always ? '' : formatCountdown(store.discovery?.remaining_seconds ?? 0)))

const copied = ref(false)
async function copyCommand() {
  if (!showCommand.value) return
  if (await copyText(result.value.command)) {
    copied.value = true
    setTimeout(() => (copied.value = false), 1600)
  } else {
    toast.error('复制失败，请手动选中命令复制')
  }
}

const seg = 'h-7 px-2.5 text-[11px] border border-border/70 -ml-px first:ml-0 first:rounded-l-sm last:rounded-r-sm transition-colors cursor-pointer'
const segOn = 'bg-primary text-primary-foreground border-primary relative z-10'
const segOff = 'bg-background text-muted-foreground hover:text-foreground hover:bg-muted'
const field =
  'h-8 px-2.5 rounded-sm bg-background border border-border/80 focus:border-primary focus:outline-none text-xs font-mono text-foreground placeholder:text-muted-foreground/60'
</script>

<template>
  <div class="space-y-3">
    <div class="flex flex-wrap items-center gap-x-4 gap-y-2">
      <div v-if="!isUpgrade" class="flex" role="group" aria-label="目标系统">
        <button
          v-for="o in osOptions"
          :key="o.id"
          type="button"
          :class="[seg, form.os === o.id ? segOn : segOff]"
          :aria-pressed="form.os === o.id"
          @click="form.os = o.id"
        >
          {{ o.label }}
        </button>
      </div>
      <div v-if="!isUpgrade" class="flex" role="group" aria-label="运行方式">
        <button
          v-for="o in modeOptions"
          :key="o.id"
          type="button"
          :class="[seg, form.mode === o.id ? segOn : segOff]"
          :aria-pressed="form.mode === o.id"
          @click="form.mode = o.id"
        >
          {{ o.label }}
        </button>
      </div>
      <div v-if="isUpgrade || form.mode === 'service'" class="flex" role="group" :aria-label="isUpgrade ? '升级范围' : '安装范围'">
        <button
          v-for="o in scopeOptions"
          :key="o.id"
          type="button"
          :class="[seg, form.scope === o.id ? segOn : segOff]"
          :aria-pressed="form.scope === o.id"
          :title="o.hint"
          @click="form.scope = o.id"
        >
          {{ o.label }}
        </button>
      </div>
    </div>

    <!-- SSH 网关：默认开启（与主机服务一样是 Agent 的标配，不分版本）。取消勾选才会在命令里写 --no-ssh-gateway；登录凭据只保存在这台主机上 -->
    <div v-if="!isUpgrade" class="space-y-1.5" data-testid="gateway-option">
      <label class="flex items-start gap-2 text-xs leading-relaxed" :class="form.mode === 'temporary' ? 'opacity-60' : 'cursor-pointer'">
        <input v-model="form.gateway" type="checkbox" class="mt-0.5 accent-primary" :disabled="form.mode === 'temporary'" data-testid="gateway-toggle" />
        <span>
          <span class="font-medium text-foreground">SSH 网关</span>
          <span class="text-muted-foreground">（默认开启）除了提供自己的终端，也让服务端借这台主机去连接内网里没有 Agent 的 SSH 主机；这些主机的登录凭据只保存在这台主机上，服务端不留副本。不需要时取消勾选。</span>
        </span>
      </label>
      <p v-if="form.mode === 'temporary'" class="text-[11px] leading-relaxed text-muted-foreground pl-6">临时运行不提供网关：网关要把凭据保存在本机，需要「常驻服务」。</p>
      <label v-else-if="form.gateway" class="space-y-1 block pl-6">
        <span class="text-[11px] text-muted-foreground">允许连接的目标（可选）：CIDR / IP / 主机名，可含 *，用逗号或空格分隔；这是网关能连到哪里的唯一边界，留空表示不限制</span>
        <input v-model="form.allowText" :class="[field, 'w-full']" spellcheck="false" autocomplete="off" placeholder="例如 10.0.0.0/8, *.corp.example" data-testid="gateway-allow" />
      </label>
    </div>

    <!-- 自动升级：默认不开启。同意写在主机本地（Hub 改不了），随时可以在主机上收回；只有 Linux 的常驻服务可用 -->
    <div v-if="autoUpgradeAvailable" class="space-y-1.5" data-testid="auto-upgrade-option">
      <div class="flex flex-wrap items-center gap-x-3 gap-y-1.5">
        <span class="text-xs font-medium text-foreground">自动升级</span>
        <div class="flex" role="group" aria-label="自动升级">
          <button
            v-for="o in autoModeOptions"
            :key="o.id"
            type="button"
            :class="[seg, form.autoMode === o.id ? segOn : segOff, o.id !== 'off' && !autoUpgradeUsable ? 'opacity-50 cursor-not-allowed' : '']"
            :aria-pressed="form.autoMode === o.id"
            :disabled="o.id !== 'off' && !autoUpgradeUsable"
            :title="o.hint"
            :data-testid="`auto-upgrade-${o.id}`"
            @click="form.autoMode = o.id"
          >
            {{ o.label }}
          </button>
        </div>
      </div>
      <p v-if="!autoUpgradeUsable" class="text-[11px] leading-relaxed text-muted-foreground" data-testid="auto-upgrade-unavailable">
        自动升级选项需要从服务端自带的安装源安装（GitHub 上的 Agent 版本可能还不认识它）：开启上面的发现窗口后可选。
      </p>
      <p v-else class="text-[11px] leading-relaxed text-muted-foreground">
        {{ autoModeOptions.find(o => o.id === form.autoMode)?.hint }}。同意写在主机本地，随时可以在主机上执行 <code class="font-mono">relaymesh-agent auto-upgrade disable</code> 收回。
      </p>
      <label v-if="autoUpgradeUsable && form.autoMode === 'silent'" class="space-y-1 block">
        <span class="text-[11px] text-muted-foreground">允许开始升级的时间段（可选，主机本地时间，可跨午夜；留空表示不限）</span>
        <input
          v-model="form.autoWindow"
          :class="[field, 'w-full', autoWindowInvalid ? 'border-destructive focus:border-destructive' : '']"
          spellcheck="false"
          autocomplete="off"
          placeholder="例如 03:00-05:00"
          data-testid="auto-upgrade-window"
        />
      </label>
    </div>

    <div class="grid grid-cols-1 sm:grid-cols-[minmax(0,1fr)_96px] gap-2">
      <label class="space-y-1 block">
        <span class="text-[11px] text-muted-foreground">目标主机访问服务端的地址</span>
        <input v-model="form.host" :class="[field, 'w-full']" spellcheck="false" autocomplete="off" placeholder="例如 192.168.1.10 或 relay.example.com" />
      </label>
      <label class="space-y-1 block">
        <span class="text-[11px] text-muted-foreground">端口</span>
        <input v-model="form.port" :class="[field, 'w-full']" inputmode="numeric" spellcheck="false" autocomplete="off" placeholder="默认" />
      </label>
    </div>

    <!-- 服务端证书：自动选择，只显示结论 -->
    <div class="space-y-1.5" data-testid="cert-summary">
      <div class="flex flex-wrap items-center gap-x-2 gap-y-1 text-[11px]">
        <span class="text-muted-foreground">服务端证书</span>
        <span
          class="inline-flex items-center gap-1 font-medium"
          :class="certSummary.risky ? 'text-amber-600 dark:text-amber-400' : 'text-foreground'"
        >
          <component :is="certSummary.risky ? ShieldAlert : ShieldCheck" class="w-3.5 h-3.5" />
          {{ certSummary.title }}
        </span>
        <span v-if="!certTouched" class="text-muted-foreground">· 自动选择</span>
        <button
          type="button"
          class="ml-auto text-muted-foreground hover:text-foreground underline underline-offset-2 cursor-pointer"
          :aria-expanded="editingCert"
          @click="editingCert = !editingCert"
        >
          {{ editingCert ? '收起' : '修改' }}
        </button>
      </div>
      <div v-if="editingCert" class="space-y-1.5">
        <select
          v-model="form.cert"
          :class="[field, 'w-full font-sans']"
          aria-label="服务端证书"
          @change="certTouched = true"
        >
          <option v-for="o in certOptions" :key="o.id" :value="o.id" :disabled="o.id === 'pinned' && (!pin || !httpsPort)">
            {{ o.label }}{{ o.id === 'pinned' && (!pin || !httpsPort) ? '（服务端未启用内置 HTTPS）' : '' }}
          </option>
        </select>
        <p class="text-[11px] leading-relaxed text-muted-foreground">{{ certSummary.detail }}{{ certReason }}</p>
      </div>
    </div>

    <!-- 发现窗口没开：没有票据就没有可用的命令 -->
    <div
      v-if="source.kind === 'waiting'"
      class="rounded-sm border border-dashed border-border/80 px-3 py-3 text-xs leading-relaxed text-muted-foreground flex gap-2"
      data-testid="install-waiting"
      role="status"
    >
      <Hourglass class="w-3.5 h-3.5 mt-0.5 shrink-0" />
      <span v-if="isUpgrade">升级窗口还没有开启，开启后这里会生成升级命令。</span>
      <span v-else-if="source.reason === 'window-closed'">
        开启上面的发现窗口后，这里会生成安装命令。命令里带有只在窗口开启期间有效的安装票据，窗口关闭后自动失效。
      </span>
      <span v-else>正在取得安装票据…</span>
    </div>

    <div v-else-if="showCommand" class="relative">
      <pre
        class="rounded-sm border border-border/80 bg-muted/60 px-3 py-2.5 pr-10 text-[11px] leading-relaxed font-mono text-foreground whitespace-pre-wrap break-all select-all max-h-44 overflow-auto"
        data-testid="install-command"
      >{{ result.command }}</pre>
      <button
        type="button"
        class="absolute top-1.5 right-1.5 p-1.5 rounded-sm text-muted-foreground hover:text-foreground hover:bg-background cursor-pointer transition-colors"
        :aria-label="copied ? '已复制' : '复制命令'"
        @click="copyCommand"
      >
        <Check v-if="copied" class="w-3.5 h-3.5 text-emerald-500" />
        <Copy v-else class="w-3.5 h-3.5" />
      </button>
    </div>

    <p v-if="source.kind === 'hub' && showCommand" class="text-[11px] leading-relaxed text-muted-foreground flex gap-1.5" data-testid="install-hub-note">
      <KeyRound class="w-3.5 h-3.5 shrink-0 mt-px" />
      <span v-if="isUpgrade">
        命令里的升级票据只对这台主机有效，窗口结束（到期、提前结束，或检测到升级完成）后立即失效，请不要转发给他人。目标主机直接从这台服务端下载，不需要访问 GitHub。
      </span>
      <span v-else>
        命令里的安装票据只在发现窗口开启期间有效<template v-if="store.discovery?.always">（窗口常开，票据在服务端重启前不变）</template
        ><template v-else>（剩余 {{ remaining }}）</template>，请不要转发给他人。目标主机直接从这台服务端下载，不需要访问 GitHub。
      </span>
    </p>
    <p v-else-if="source.kind === 'github'" class="text-[11px] leading-relaxed text-muted-foreground flex gap-1.5" data-testid="install-github-note">
      <TriangleAlert class="w-3.5 h-3.5 shrink-0 mt-px" />
      <span>{{ source.note }}</span>
    </p>

    <ul v-if="result.errors.length" class="space-y-1" role="alert">
      <li v-for="e in result.errors" :key="e" class="text-[11px] leading-relaxed text-destructive flex gap-1.5">
        <TriangleAlert class="w-3.5 h-3.5 shrink-0 mt-px" />
        <span>{{ e }}</span>
      </li>
    </ul>
    <ul v-if="showCommand && result.warnings.length" class="space-y-1">
      <li v-for="w in result.warnings" :key="w" class="text-[11px] leading-relaxed text-amber-600 dark:text-amber-400 flex gap-1.5">
        <TriangleAlert class="w-3.5 h-3.5 shrink-0 mt-px" />
        <span>{{ w }}</span>
      </li>
    </ul>
  </div>
</template>
