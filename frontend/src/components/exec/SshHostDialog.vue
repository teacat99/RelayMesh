<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { toast } from 'vue-sonner'
import { AlertTriangle, ArrowLeft, Copy, Loader2, ShieldCheck } from 'lucide-vue-next'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '../ui/dialog'
import Button from '../ui/button/Button.vue'
import { useTerminalStore } from '../../stores/terminal'
import { UnlockCancelled, toApiError, type HostView, type SshAuthType, type SshConnBody, type SshProbe } from '../../api/terminal'
import { copyText } from '../../lib/terminal/clipboard'
import { hostTitle } from '../../lib/terminal/format'
import HostDefaultOption from '../terminal/HostDefaultOption.vue'
import SshRouteField from './SshRouteField.vue'
import { gatewayHosts, hostKeyFile, sameFingerprint, sshEndpoint, validateSshForm, type SshDialogMode } from '../../lib/terminal/hosts'

// SSH 主机的三种对话：
//   add   ——接入：先探测主机密钥并由管理员核对指纹（这是防止中间人的唯一机会），再填凭据登记；
//   edit  ——修改连接信息：服务端会先用已固定的密钥验证新连接，通过才保存；凭据留空表示不改；
//   rekey ——密钥不一致后重新确认：必须看到对方当前的指纹并明确确认，服务端不会自动重新固定。
// 凭据只在这个对话框的内存里短暂存在：提交后立即清空，关闭对话框也会清空；服务端永远不会回显它们。
//
// 接入路径有两条，管理与授权完全一致（都有人工终端，模型获得授权后都能执行命令）：Hub 直连（凭据加密保存在 Hub，需要 SSH 主密钥，
// 终端会话由 Hub 持有）与经网关主机（凭据只保存在网关上，Hub 不留副本也不需要 SSH 主密钥，会话由网关持有并记录）。
// 网关能力随 Agent 默认开启；没有在线可用的网关主机时界面只提供直连。

const open = defineModel<boolean>('open', { required: true })
const props = defineProps<{ mode: SshDialogMode; host?: HostView | null }>()

const store = useTerminalStore()

const step = ref<'address' | 'verify'>('address')
const busy = ref(false)
const error = ref('')
const probe = ref<SshProbe | null>(null)
const confirmed = ref(false)
const pasted = ref('')
const form = reactive({ remark: '', address: '', user: '', auth: 'password' as SshAuthType, password: '', privateKey: '', passphrase: '' })
// 新增主机时是否按默认态授予现有模型令牌（默认不授予）。
const grantExisting = ref(false)

// 接入路径：direct = Hub 直连（凭据在 Hub 上，会话由 Hub 持有）；gateway = 经网关主机（凭据在网关上，会话由网关持有）。
const via = ref<'direct' | 'gateway'>('direct')
const gatewayCode = ref('')
const gateways = computed(() => gatewayHosts(store.hosts))
/** 下拉里的网关：当前可用的，加上这台主机正在用、但此刻不可用的那一台（让管理员看得到它，而不是悄悄消失）。 */
const gatewayOptions = computed(() => {
  const list = gateways.value.map(h => ({ code: h.machine_code, label: hostTitle(h) }))
  const cur = props.host?.ssh?.gateway
  if (cur && !list.some(o => o.code === cur)) {
    const h = store.hosts.find(x => x.machine_code === cur)
    list.unshift({ code: cur, label: `${h ? hostTitle(h) : cur}（当前不可用）` })
  }
  return list
})
const gatewayAvailable = computed(() => gatewayOptions.value.length > 0)
const gatewayTitleOf = (code: string) => {
  const h = store.hosts.find(x => x.machine_code === code)
  return h ? hostTitle(h) : code
}
const viaGateway = computed(() => via.value === 'gateway')

// 没有可用的 SSH 主密钥只影响 Hub 直连（它要把凭据加密保存在 Hub）；经网关接入的凭据在网关上，不需要这把密钥。
const backendOff = computed(() => props.mode === 'add' && !viaGateway.value && store.sshStatus !== null && !store.sshStatus.enabled)
const title = computed(() => {
  if (props.mode === 'edit') return `修改连接信息 · ${props.host ? hostTitle(props.host) : ''}`
  if (props.mode === 'rekey') return '重新确认主机密钥'
  return '接入 SSH 主机'
})
const description = computed(() => {
  if (props.mode === 'edit') return '服务端会先用已固定的主机密钥验证新的连接，通过才会保存。凭据留空表示不修改。'
  if (props.mode === 'rekey') return '这台主机出示的密钥与之前固定的不一致。确认之前，Hub 不会再向它发送任何凭据。'
  return '通过 SSH 接入一台没有 Agent 的主机：由 Hub 直连让模型执行命令，或经网关主机接入（有人工终端，模型也能用，凭据只保存在网关上）。第一步探测并核对主机密钥，第二步填写登录凭据。'
})
const keyFile = computed(() => hostKeyFile(probe.value?.key_type))
const pastedWrong = computed(() => pasted.value.trim() !== '' && !sameFingerprint(pasted.value, probe.value?.fingerprint))
const oldFingerprint = computed(() => props.host?.ssh?.fingerprint ?? '')

function wipeSecrets() {
  form.password = ''
  form.privateKey = ''
  form.passphrase = ''
}

// 探测要几秒：回来之前用户可能已经按 Esc 关掉对话框，甚至重新打开去处理另一台主机。
// 每次探测领一个序号，reset（重新打开）与新的探测都会让之前的作废；作废的探测回来后什么都不动（结果、提示、busy 都不碰）——
// 否则别的主机的指纹会出现在这一台的确认步骤里，而这一步要用户确认的恰恰就是指纹。
let probeSeq = 0

function reset() {
  probeSeq++
  error.value = ''
  busy.value = false
  probe.value = null
  confirmed.value = false
  pasted.value = ''
  wipeSecrets()
  grantExisting.value = false
  form.remark = ''
  form.address = ''
  form.user = ''
  form.auth = 'password'
  step.value = 'address'
  via.value = 'direct'
  gatewayCode.value = ''
  // 修改 / 重新确认一台已经经网关接入的主机：沿用它现在的路径（重新确认时探测也会走同一条路径）。
  if (props.mode !== 'add' && props.host?.ssh?.gateway) {
    via.value = 'gateway'
    gatewayCode.value = props.host.ssh.gateway
  }
  if (props.mode === 'edit' && props.host?.ssh) {
    form.address = props.host.ssh.address
    form.user = props.host.ssh.user
    form.auth = props.host.ssh.auth === 'key' ? 'key' : 'password'
    step.value = 'verify'
  } else if (props.mode === 'rekey') {
    step.value = 'verify'
    void probeHost()
  }
}

watch(open, o => {
  if (o) reset()
  else wipeSecrets()
})

function fail(e: unknown) {
  if (e instanceof UnlockCancelled) return
  error.value = toApiError(e).message
}

async function doProbe() {
  const addr = form.address.trim()
  if (!addr) {
    error.value = '请填写主机地址（IP 或域名，可带端口，例如 10.0.0.5:2222）'
    return
  }
  if (/[\s/@\\?#%]/.test(addr)) {
    error.value = '主机地址只能是「主机名 / IP」或「主机:端口」，不要带用户名或路径'
    return
  }
  if (viaGateway.value && !gatewayCode.value) {
    error.value = '请选择由哪台网关主机出站连接'
    return
  }
  const seq = ++probeSeq
  busy.value = true
  error.value = ''
  try {
    const res = await store.sshProbe(addr, viaGateway.value ? gatewayCode.value : undefined)
    if (seq !== probeSeq) return
    probe.value = res
    confirmed.value = false
    pasted.value = ''
    step.value = 'verify'
  } catch (e) {
    if (seq === probeSeq) fail(e)
  } finally {
    if (seq === probeSeq) busy.value = false
  }
}

async function probeHost() {
  if (!props.host) return
  const seq = ++probeSeq
  busy.value = true
  error.value = ''
  try {
    const res = await store.sshProbeHost(props.host.machine_code)
    if (seq !== probeSeq) return
    probe.value = res
    confirmed.value = false
    pasted.value = ''
  } catch (e) {
    if (seq === probeSeq) fail(e)
  } finally {
    if (seq === probeSeq) busy.value = false
  }
}

function backToAddress() {
  probe.value = null
  confirmed.value = false
  pasted.value = ''
  error.value = ''
  wipeSecrets()
  step.value = 'address'
}

async function copyFingerprint() {
  if (!probe.value) return
  if (await copyText(probe.value.fingerprint)) toast.success('已复制指纹')
  else toast.error('复制失败，请手动选择复制')
}

const canAdd = computed(
  () => !busy.value && !!probe.value && confirmed.value && !pastedWrong.value && validateSshForm(form) === '',
)

async function submitAdd() {
  const msg = validateSshForm(form)
  if (msg) {
    error.value = msg
    return
  }
  if (!probe.value || !confirmed.value || pastedWrong.value) return
  busy.value = true
  error.value = ''
  try {
    const { host, grant } = await store.sshAdd({
      remark: form.remark.trim() || undefined,
      address: probe.value.address,
      user: form.user.trim(),
      auth_type: form.auth,
      password: form.auth === 'password' ? form.password : undefined,
      private_key: form.auth === 'key' ? form.privateKey : undefined,
      passphrase: form.auth === 'key' && form.passphrase ? form.passphrase : undefined,
      fingerprint: probe.value.fingerprint,
      default_for_new_models: grantExisting.value ? 'allow' : 'deny',
      gateway: viaGateway.value ? gatewayCode.value : undefined,
    })
    wipeSecrets()
    const granted = grant?.granted ?? []
    if (viaGateway.value && host.ssh?.term_only) {
      // 探测不出 POSIX 环境（需要 sh 与 base64）：模型用不了，不预先授予任何令牌，也没有「允许模型访问」可开。
      toast.success(`已接入 ${hostTitle(host)}`, {
        description: `经网关「${gatewayTitleOf(gatewayCode.value)}」：在主机卡片里点「打开终端」，或在侧栏「新建终端」菜单里选它。这台主机探测不出 POSIX 环境（需要 sh 与 base64），只能当人工终端用，不支持模型执行。`,
      })
    } else {
      // 两条路径一视同仁：人可以直接开终端；模型默认碰不到，需要主机上打开「允许模型访问」并给令牌授权。
      const route = viaGateway.value ? `经网关「${gatewayTitleOf(gatewayCode.value)}」：` : ''
      toast.success(`已接入 ${hostTitle(host)}`, {
        description: `${route}在主机卡片里点「打开终端」，或在侧栏「新建终端」菜单里选它。${
          granted.length
            ? `已授予 ${granted.length} 个模型令牌：${granted.slice(0, 4).join('、')}${granted.length > 4 ? ' 等' : ''}。还需要在主机卡片里打开「允许模型访问」，模型才能使用它。`
            : '默认不允许模型使用：需要时在主机卡片里打开「允许模型访问」，并给 MCP 令牌授权这台主机。'
        }`,
      })
    }
    open.value = false
  } catch (e) {
    if (e instanceof UnlockCancelled) return
    const err = toApiError(e)
    // 对方的密钥在探测之后变了：之前核对的指纹已经作废，必须回到第一步重新探测。
    if (err.code === 'ssh_fingerprint_changed') backToAddress()
    error.value = err.message
  } finally {
    busy.value = false
  }
}

const secretGiven = computed(() => (form.auth === 'key' ? form.privateKey.trim() !== '' : form.password !== ''))

/** 编辑：只发送改动过的字段。 */
function buildConnBody(): SshConnBody {
  const ssh = props.host?.ssh
  const body: SshConnBody = {}
  if (!ssh) return body
  if (form.address.trim() !== ssh.address) body.address = form.address.trim()
  if (form.user.trim() !== ssh.user) body.user = form.user.trim()
  // 改接路径：机器码 = 改经该网关，空串 = 改为 Hub 直连；没变就不发。
  const nextGateway = viaGateway.value ? gatewayCode.value : ''
  if (nextGateway !== (ssh.gateway ?? '')) body.gateway = nextGateway
  if (form.auth !== ssh.auth || secretGiven.value) {
    body.auth_type = form.auth
    if (form.auth === 'key') {
      body.private_key = form.privateKey
      if (form.passphrase) body.passphrase = form.passphrase
    } else {
      body.password = form.password
    }
  }
  return body
}

const authChanged = computed(() => !!props.host?.ssh && form.auth !== props.host.ssh.auth)
/** 接入路径是否变了（直连 ↔ 网关，或换了一台网关）。 */
const routeChanged = computed(() => !!props.host?.ssh && (viaGateway.value ? gatewayCode.value : '') !== (props.host.ssh.gateway ?? ''))
/** 保存的凭据此刻还能不能用：经网关的看网关上还在不在（cred_missing），Hub 直连的看 Hub 有没有保存（has_secret）。 */
const credUsable = computed(() => {
  const ssh = props.host?.ssh
  if (!ssh) return false
  return ssh.gateway ? !ssh.cred_missing : ssh.has_secret
})
/** 换认证方式、换接入路径（凭据不会跟着搬走：旧路径上的会被清掉）或原来的凭据已经不可用时，必须同时给出新凭据。 */
const needSecret = computed(() => authChanged.value || routeChanged.value || !credUsable.value)
const canEdit = computed(() => {
  if (busy.value || !props.host?.ssh) return false
  if (viaGateway.value && !gatewayCode.value) return false
  return validateSshForm(form, { secretRequired: needSecret.value }) === '' && Object.keys(buildConnBody()).length > 0
})

async function submitEdit() {
  if (!props.host) return
  if (viaGateway.value && !gatewayCode.value) {
    error.value = '请选择由哪台网关主机出站连接'
    return
  }
  const msg = validateSshForm(form, { secretRequired: needSecret.value })
  if (msg) {
    error.value = msg
    return
  }
  const body = buildConnBody()
  if (Object.keys(body).length === 0) {
    error.value = '没有需要修改的内容'
    return
  }
  busy.value = true
  error.value = ''
  try {
    await store.sshUpdateConnection(props.host.machine_code, body)
    wipeSecrets()
    toast.success('已更新连接信息', { description: '已用固定的主机密钥验证过新的连接。' })
    open.value = false
  } catch (e) {
    fail(e)
  } finally {
    busy.value = false
  }
}

const canRekey = computed(() => !busy.value && !!probe.value && confirmed.value && !pastedWrong.value)

async function submitRekey() {
  if (!props.host || !probe.value || !confirmed.value || pastedWrong.value) return
  busy.value = true
  error.value = ''
  try {
    await store.sshRekey(props.host.machine_code, probe.value.fingerprint)
    toast.success('已重新固定主机密钥', { description: '之后的连接都会按这把密钥校验。' })
    open.value = false
  } catch (e) {
    if (e instanceof UnlockCancelled) return
    const err = toApiError(e)
    error.value = err.message
    // 对方的密钥又变了：之前看到的指纹作废，重新探测一次让管理员看到最新的。
    if (err.code === 'ssh_fingerprint_changed') void probeHost()
  } finally {
    busy.value = false
  }
}

const AUTH_CHOICES = [
  { id: 'password' as const, label: '口令' },
  { id: 'key' as const, label: '私钥' },
]

const fieldClass =
  'w-full h-8 px-2.5 rounded-sm bg-background border border-border/80 focus:border-primary focus:outline-none text-xs text-foreground placeholder:text-muted-foreground/60 transition-colors disabled:opacity-50 disabled:cursor-not-allowed'
</script>

<template>
  <Dialog :open="open" @update:open="(v: boolean) => (open = v)">
    <DialogContent class="max-w-[520px] rounded-sm p-5 gap-4">
      <DialogHeader class="space-y-1.5 text-left pr-6">
        <DialogTitle class="text-sm font-bold flex items-center gap-2">
          <ShieldCheck class="w-4 h-4 text-muted-foreground" aria-hidden="true" />
          <span class="truncate">{{ title }}</span>
        </DialogTitle>
        <DialogDescription class="text-xs leading-relaxed text-muted-foreground">{{ description }}</DialogDescription>
      </DialogHeader>

      <!-- Hub 没有可用的 SSH 主密钥：Hub 直连暂不可用，说明怎么开；经网关接入不需要这把密钥，所以接入路径仍然可以切换 -->
      <SshRouteField v-if="backendOff" v-model:via="via" v-model:gateway="gatewayCode" :options="gatewayOptions" id-prefix="ssh-off" :disabled="busy" />
      <div v-if="backendOff" class="rounded-sm border border-amber-500/30 bg-amber-500/10 p-3 text-xs leading-relaxed text-amber-700 dark:text-amber-300 space-y-1.5" role="alert">
        <p class="font-medium flex items-center gap-1.5"><AlertTriangle class="w-3.5 h-3.5 shrink-0" />Hub 直连暂未启用</p>
        <p>{{ store.sshStatus?.message || '服务端没有配置有效的 RELAYMESH_SSH_SECRET_KEY。' }}</p>
        <p>
          Hub 直连的登录凭据会用这把密钥加密保存。在服务端设置 <code class="font-mono">RELAYMESH_SSH_SECRET_KEY</code>（足够长的随机值，不要与其他密钥相同）并重启后即可使用；
          或者改选上面的「经网关」——网关接入的登录凭据只保存在网关主机自己的存储里，不需要这把密钥。
        </p>
      </div>

      <template v-else>
        <!-- 接入 · 第一步：地址 -->
        <form v-if="mode === 'add' && step === 'address'" class="space-y-3" @submit.prevent="doProbe">
          <SshRouteField v-model:via="via" v-model:gateway="gatewayCode" :options="gatewayOptions" id-prefix="ssh-add" :disabled="busy" />
          <div class="space-y-1.5">
            <label for="ssh-remark" class="text-[11px] font-medium text-foreground">备注（可选）</label>
            <input id="ssh-remark" v-model="form.remark" maxlength="64" :class="fieldClass" placeholder="例如：构建机" autocomplete="off" />
          </div>
          <div class="space-y-1.5">
            <label for="ssh-address" class="text-[11px] font-medium text-foreground">主机地址</label>
            <input
              id="ssh-address"
              v-model="form.address"
              :class="[fieldClass, 'font-mono']"
              placeholder="10.0.0.5 或 build.example.com:2222"
              autocomplete="off"
              spellcheck="false"
              :disabled="busy"
            />
            <p class="text-[11px] leading-snug text-muted-foreground">
              端口默认 22。只会连接你在这里登记的地址；{{ viaGateway ? '网关也只能连接登记过的主机，不会代人连接任意地址。' : '模型不能自己指定地址。' }}
            </p>
          </div>
          <p v-if="error" class="text-[11px] leading-relaxed text-destructive" role="alert">{{ error }}</p>
          <div class="flex justify-end gap-2 pt-1">
            <Button type="button" variant="outline" size="sm" class="rounded-sm text-xs" @click="open = false">取消</Button>
            <Button type="submit" size="sm" class="rounded-sm text-xs min-w-[96px]" :disabled="busy || !form.address.trim()">
              <Loader2 v-if="busy" class="w-3.5 h-3.5 animate-spin" />
              <span v-else>探测主机密钥</span>
            </Button>
          </div>
        </form>

        <!-- 指纹核对（接入第二步 / 重新确认共用） -->
        <template v-else-if="(mode === 'add' && step === 'verify') || mode === 'rekey'">
          <div v-if="mode === 'rekey' && busy && !probe" class="flex items-center gap-2 text-xs text-muted-foreground py-3">
            <Loader2 class="w-3.5 h-3.5 animate-spin" />正在探测这台主机当前出示的密钥…
          </div>

          <form class="space-y-3" @submit.prevent="mode === 'add' ? submitAdd() : submitRekey()">
            <div v-if="probe" class="rounded-sm border border-border/80 bg-muted/40 p-3 space-y-2">
              <p v-if="mode === 'rekey'" class="text-[11px] leading-relaxed text-amber-700 dark:text-amber-300 flex items-start gap-1.5">
                <AlertTriangle class="w-3.5 h-3.5 shrink-0 mt-0.5" />
                主机密钥变了。这可能是主机重装 / 更换了密钥，也可能是有人在冒充它：请通过可信的渠道核对下面的新指纹。
              </p>
              <div v-if="mode === 'rekey' && oldFingerprint" class="space-y-0.5">
                <p class="text-[11px] text-muted-foreground">之前固定的指纹</p>
                <code class="block break-all font-mono text-[11px] text-muted-foreground line-through decoration-muted-foreground/50">{{ oldFingerprint }}</code>
              </div>
              <div class="space-y-0.5">
                <p class="text-[11px] text-muted-foreground">
                  {{ mode === 'rekey' ? '现在出示的指纹' : '这台主机出示的指纹' }} · {{ probe.key_type }} · {{ sshEndpoint({ address: probe.address }) }}
                  <template v-if="viaGateway"> · 由网关「{{ gatewayTitleOf(gatewayCode) }}」探测</template>
                </p>
                <div class="flex items-start gap-1.5">
                  <code class="block flex-1 break-all font-mono text-xs text-foreground select-all" data-testid="ssh-fingerprint">{{ probe.fingerprint }}</code>
                  <button type="button" class="p-1 rounded-sm text-muted-foreground hover:text-foreground hover:bg-muted cursor-pointer shrink-0" aria-label="复制指纹" title="复制指纹" @click="copyFingerprint">
                    <Copy class="w-3 h-3" />
                  </button>
                </div>
              </div>
              <p v-if="keyFile" class="text-[11px] leading-relaxed text-muted-foreground">
                在目标主机上运行 <code class="font-mono select-all">ssh-keygen -lf {{ keyFile }}</code>，输出里的 <code class="font-mono">SHA256:…</code> 必须与上面完全一致。
              </p>
              <p v-else class="text-[11px] leading-relaxed text-muted-foreground">在目标主机上用 <code class="font-mono">ssh-keygen -lf</code> 查看对应主机公钥的指纹，必须与上面完全一致。</p>
              <p class="text-[11px] leading-relaxed text-muted-foreground">确认后这个指纹会被固定：之后只要对方出示的密钥不同，Hub 就拒绝连接，也不会向它发送任何凭据。</p>
            </div>

            <div v-if="probe" class="space-y-1.5">
              <label for="ssh-pasted" class="text-[11px] font-medium text-foreground">粘贴你核对到的指纹（可选，用来防手滑）</label>
              <input
                id="ssh-pasted"
                v-model="pasted"
                :class="[fieldClass, 'font-mono', pastedWrong ? 'border-destructive focus:border-destructive' : '']"
                placeholder="SHA256:…"
                autocomplete="off"
                spellcheck="false"
                :aria-invalid="pastedWrong"
              />
              <p v-if="pastedWrong" class="text-[11px] text-destructive" role="alert">与探测到的指纹不一致：不要继续，先弄清楚是怎么回事。</p>
            </div>

            <!-- 登录凭据（只在接入时填写） -->
            <template v-if="mode === 'add' && probe">
              <div class="grid gap-3 sm:grid-cols-2">
                <div class="space-y-1.5">
                  <label for="ssh-user" class="text-[11px] font-medium text-foreground">用户名</label>
                  <input id="ssh-user" v-model="form.user" :class="[fieldClass, 'font-mono']" placeholder="例如：ops" autocomplete="off" spellcheck="false" />
                </div>
                <div class="space-y-1.5">
                  <span class="text-[11px] font-medium text-foreground">认证方式</span>
                  <div class="inline-flex rounded-sm border border-border/80 p-0.5 gap-0.5 w-full" role="group" aria-label="认证方式">
                    <button
                      v-for="a in AUTH_CHOICES"
                      :key="a.id"
                      type="button"
                      class="h-6 flex-1 px-2 rounded-xs text-[11px] cursor-pointer transition-colors"
                      :class="form.auth === a.id ? 'bg-primary text-primary-foreground' : 'text-muted-foreground hover:bg-muted'"
                      :aria-pressed="form.auth === a.id"
                      @click="form.auth = a.id"
                    >{{ a.label }}</button>
                  </div>
                </div>
              </div>
              <div v-if="form.auth === 'password'" class="space-y-1.5">
                <label for="ssh-password" class="text-[11px] font-medium text-foreground">登录口令</label>
                <input id="ssh-password" v-model="form.password" type="password" :class="fieldClass" autocomplete="new-password" />
              </div>
              <template v-else>
                <div class="space-y-1.5">
                  <label for="ssh-key" class="text-[11px] font-medium text-foreground">私钥</label>
                  <textarea
                    id="ssh-key"
                    v-model="form.privateKey"
                    rows="5"
                    class="w-full px-2.5 py-1.5 rounded-sm bg-background border border-border/80 focus:border-primary focus:outline-none text-[11px] font-mono text-foreground placeholder:text-muted-foreground/60 resize-y"
                    placeholder="-----BEGIN OPENSSH PRIVATE KEY-----"
                    autocomplete="off"
                    spellcheck="false"
                  />
                </div>
                <div class="space-y-1.5">
                  <label for="ssh-passphrase" class="text-[11px] font-medium text-foreground">私钥口令（私钥已加密时填写）</label>
                  <input id="ssh-passphrase" v-model="form.passphrase" type="password" :class="fieldClass" autocomplete="new-password" />
                </div>
              </template>
              <p v-if="viaGateway" class="text-[11px] leading-relaxed text-muted-foreground">
                凭据只会交给网关主机：由它登录目标成功后，加密保存在网关自己的存储里（Hub 不留副本，也不需要 SSH 主密钥），只用于连接这台主机，之后不会再显示，也不会写进日志与审计。网关用 <code class="font-mono">--ssh-allow</code> 限定能连到哪里。建议使用权限受限的专用账号。
              </p>
              <p v-else class="text-[11px] leading-relaxed text-muted-foreground">凭据用服务端的主密钥加密保存，只用于连接这台主机，之后不会再显示，也不会写进日志与审计。建议使用权限受限的专用账号。</p>
            </template>

            <HostDefaultOption v-if="probe && mode === 'add'" v-model="grantExisting" :disabled="busy" />

            <label v-if="probe" class="flex items-start gap-2 text-xs leading-relaxed cursor-pointer">
              <input v-model="confirmed" type="checkbox" class="mt-0.5 accent-primary" :disabled="busy" />
              <span>
                <template v-if="mode === 'rekey'">我已通过可信渠道确认：新的指纹确实属于这台主机，同意重新固定。</template>
                <template v-else>我已在目标主机上核对过这个指纹，确认它就是我要接入的那台主机。</template>
              </span>
            </label>

            <p v-if="error" class="text-[11px] leading-relaxed text-destructive" role="alert">{{ error }}</p>

            <div class="flex items-center justify-end gap-2 pt-1">
              <Button v-if="mode === 'add'" type="button" variant="ghost" size="sm" class="rounded-sm text-xs gap-1 mr-auto" :disabled="busy" @click="backToAddress">
                <ArrowLeft class="w-3.5 h-3.5" />重新探测
              </Button>
              <Button v-else type="button" variant="ghost" size="sm" class="rounded-sm text-xs mr-auto" :disabled="busy" @click="probeHost">重新探测</Button>
              <Button type="button" variant="outline" size="sm" class="rounded-sm text-xs" :disabled="busy" @click="open = false">取消</Button>
              <Button type="submit" size="sm" class="rounded-sm text-xs min-w-[96px]" :disabled="mode === 'add' ? !canAdd : !canRekey">
                <Loader2 v-if="busy" class="w-3.5 h-3.5 animate-spin" />
                <span v-else>{{ mode === 'add' ? '核对无误，接入' : '确认并重新固定' }}</span>
              </Button>
            </div>
          </form>
        </template>

        <!-- 修改连接信息 -->
        <form v-else-if="mode === 'edit'" class="space-y-3" @submit.prevent="submitEdit">
          <SshRouteField v-model:via="via" v-model:gateway="gatewayCode" :options="gatewayOptions" id-prefix="ssh-edit" :disabled="busy" />
          <div class="grid gap-3 sm:grid-cols-2">
            <div class="space-y-1.5">
              <label for="ssh-edit-address" class="text-[11px] font-medium text-foreground">主机地址</label>
              <input id="ssh-edit-address" v-model="form.address" :class="[fieldClass, 'font-mono']" autocomplete="off" spellcheck="false" />
            </div>
            <div class="space-y-1.5">
              <label for="ssh-edit-user" class="text-[11px] font-medium text-foreground">用户名</label>
              <input id="ssh-edit-user" v-model="form.user" :class="[fieldClass, 'font-mono']" autocomplete="off" spellcheck="false" />
            </div>
          </div>
          <div class="space-y-1.5">
            <span class="text-[11px] font-medium text-foreground">认证方式</span>
            <div class="inline-flex rounded-sm border border-border/80 p-0.5 gap-0.5" role="group" aria-label="认证方式">
              <button
                v-for="a in AUTH_CHOICES"
                :key="a.id"
                type="button"
                class="h-6 px-3 rounded-xs text-[11px] cursor-pointer transition-colors"
                :class="form.auth === a.id ? 'bg-primary text-primary-foreground' : 'text-muted-foreground hover:bg-muted'"
                :aria-pressed="form.auth === a.id"
                @click="form.auth = a.id"
              >{{ a.label }}</button>
            </div>
          </div>
          <div v-if="form.auth === 'password'" class="space-y-1.5">
            <label for="ssh-edit-password" class="text-[11px] font-medium text-foreground">新的登录口令{{ needSecret ? '' : '（留空表示不修改）' }}</label>
            <input id="ssh-edit-password" v-model="form.password" type="password" :class="fieldClass" autocomplete="new-password" />
          </div>
          <template v-else>
            <div class="space-y-1.5">
              <label for="ssh-edit-key" class="text-[11px] font-medium text-foreground">新的私钥{{ needSecret ? '' : '（留空表示不修改）' }}</label>
              <textarea
                id="ssh-edit-key"
                v-model="form.privateKey"
                rows="5"
                class="w-full px-2.5 py-1.5 rounded-sm bg-background border border-border/80 focus:border-primary focus:outline-none text-[11px] font-mono text-foreground placeholder:text-muted-foreground/60 resize-y"
                placeholder="-----BEGIN OPENSSH PRIVATE KEY-----"
                autocomplete="off"
                spellcheck="false"
              />
            </div>
            <div class="space-y-1.5">
              <label for="ssh-edit-passphrase" class="text-[11px] font-medium text-foreground">私钥口令（私钥已加密时填写）</label>
              <input id="ssh-edit-passphrase" v-model="form.passphrase" type="password" :class="fieldClass" autocomplete="new-password" />
            </div>
          </template>
          <p v-if="routeChanged" class="text-[11px] leading-relaxed text-amber-700 dark:text-amber-300">
            换接入路径后凭据不会跟着搬走：请在下面重新填写登录凭据，原路径上保存的凭据会被清掉。
          </p>
          <p v-else-if="!credUsable" class="text-[11px] leading-relaxed text-amber-700 dark:text-amber-300">
            {{ host?.ssh?.gateway ? '网关上已经没有这台主机的登录凭据（凭据只保存在网关自己的存储里，例如网关换了数据目录），需要重新填写凭据。' : '保存的凭据已经不可用（例如服务端的主密钥更换过），需要重新填写凭据。' }}
          </p>
          <p class="text-[11px] leading-relaxed text-muted-foreground">主机密钥不会在这里改变：新的连接必须出示已固定的密钥，否则会被拒绝。要换一台不同的主机，请删除后重新接入。</p>
          <p v-if="error" class="text-[11px] leading-relaxed text-destructive" role="alert">{{ error }}</p>
          <div class="flex justify-end gap-2 pt-1">
            <Button type="button" variant="outline" size="sm" class="rounded-sm text-xs" :disabled="busy" @click="open = false">取消</Button>
            <Button type="submit" size="sm" class="rounded-sm text-xs min-w-[96px]" :disabled="!canEdit">
              <Loader2 v-if="busy" class="w-3.5 h-3.5 animate-spin" />
              <span v-else>验证并保存</span>
            </Button>
          </div>
        </form>
      </template>
    </DialogContent>
  </Dialog>
</template>
