<script setup lang="ts">
import { computed, ref } from 'vue'
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription } from '../ui/dialog'
import Button from '../ui/button/Button.vue'
import { Check, ChevronDown, ChevronRight, Copy, Crown, Laptop, Loader2, Plus, Radar, Server } from 'lucide-vue-next'
import { toast } from 'vue-sonner'
import InstallCommand from './InstallCommand.vue'
import HostDefaultOption from './HostDefaultOption.vue'
import { useTerminalStore } from '../../stores/terminal'
import type { HostView, PendingView } from '../../api/terminal'
import { isImeKey } from '../../lib/ime'
import { copyText } from '../../lib/terminal/clipboard'
import { formatCountdown, hostTitle, privilegeLabel, relativeTime } from '../../lib/terminal/format'
import { formatMachineCode } from '../../lib/terminal/installCommand'

// 接入主机的全流程：打开发现窗口 → 在目标主机运行安装命令 → 核对机器码后启用。
// 发现窗口之外，陌生机器码的连接会被直接拒绝，所以管理员看到的待启用列表不会被扫描流量刷屏。
const open = defineModel<boolean>('open', { required: true })
const store = useTerminalStore()

const windowMinutes = 10
/** 服务端对发现窗口总时长的上限（与 terminal.MaxDiscoveryWindow 一致）；窗口已经长到这个程度就不能再续期。 */
const MAX_WINDOW_SECONDS = 60 * 60
/** 剩余时间不足这个数就把倒计时标成琥珀色，提醒要么续期、要么马上把安装命令跑完。 */
const WINDOW_WARN_SECONDS = 120
const busyWindow = ref(false)
const busyCode = ref('')
const confirming = ref('')
const remarkDraft = ref('')
const grantDraft = ref(false)
const showIgnored = ref(false)
const showInstall = ref(true)
const copiedCode = ref('')

const discovery = computed(() => store.discovery)
const ignored = computed<HostView[]>(() => store.hosts.filter(h => h.status === 'blocked'))
const hasAnyHost = computed(() => store.hosts.some(h => h.status === 'enabled'))

async function openWindow() {
  busyWindow.value = true
  try {
    await store.openDiscovery(windowMinutes)
  } catch (e) {
    store.notifyError(e, '开启发现窗口失败')
  } finally {
    busyWindow.value = false
  }
}

/** 续期：在剩余时间上累加，而不是重新计时；已经签发的安装命令继续有效（正在下载的主机不会被打断）。 */
async function extendWindow() {
  busyWindow.value = true
  try {
    await store.openDiscovery(windowMinutes, true)
  } catch (e) {
    store.notifyError(e, '续期发现窗口失败')
  } finally {
    busyWindow.value = false
  }
}

const canExtend = computed(() => (discovery.value?.remaining_seconds ?? 0) < MAX_WINDOW_SECONDS - 5)
const windowEnding = computed(() => !!discovery.value?.open && (discovery.value.remaining_seconds ?? 0) <= WINDOW_WARN_SECONDS)

async function closeWindow() {
  busyWindow.value = true
  try {
    await store.closeDiscovery()
  } catch (e) {
    store.notifyError(e, '关闭发现窗口失败')
  } finally {
    busyWindow.value = false
  }
}

function startConfirm(p: PendingView) {
  confirming.value = p.machine_code
  remarkDraft.value = p.hostname
  grantDraft.value = false
}

// 备注框里的 Esc：收起「确认启用」这一步，不关掉整个对话框；输入法组合期间的 Esc 只是取消候选词，不收起。
function onRemarkEsc(ev: KeyboardEvent) {
  ev.stopPropagation()
  if (isImeKey(ev)) return
  confirming.value = ''
}

async function confirmEnable(p: PendingView) {
  busyCode.value = p.machine_code
  try {
    // 临时主机没有「默认授权」可言（只活在内存里，不会写进令牌名单）。
    const grant = await store.enableHost(p.machine_code, remarkDraft.value.trim(), p.kind === 'temporary' ? undefined : grantDraft.value ? 'allow' : 'deny')
    confirming.value = ''
    const title = hostTitle({ remark: remarkDraft.value, hostname: p.hostname, machine_code: p.machine_code })
    if (grant && grant.granted.length > 0) toast.success(`已启用 ${title}`, { description: `并授予了 ${grant.granted.length} 个模型令牌：${grant.granted.slice(0, 4).join('、')}${grant.granted.length > 4 ? ' 等' : ''}` })
    else toast.success(`已启用 ${title}`)
  } catch (e) {
    store.notifyError(e, '启用失败')
  } finally {
    busyCode.value = ''
  }
}

async function ignore(p: PendingView) {
  busyCode.value = p.machine_code
  try {
    await store.ignoreHost(p.machine_code)
  } catch (e) {
    store.notifyError(e, '忽略失败')
  } finally {
    busyCode.value = ''
  }
}

async function unignore(h: HostView) {
  busyCode.value = h.machine_code
  try {
    await store.unignoreHost(h.machine_code)
  } catch (e) {
    store.notifyError(e, '取消忽略失败')
  } finally {
    busyCode.value = ''
  }
}

async function copyCode(code: string) {
  if (await copyText(code)) {
    copiedCode.value = code
    setTimeout(() => {
      if (copiedCode.value === code) copiedCode.value = ''
    }, 1500)
  }
}

function osText(p: { os: string; arch: string }): string {
  return [p.os, p.arch].filter(Boolean).join(' · ')
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="max-w-2xl rounded-sm p-0 gap-0">
      <DialogHeader class="px-5 pt-5 pb-3 text-left space-y-1 pr-12">
        <DialogTitle class="text-sm font-bold flex items-center gap-2">
          <Radar class="w-4 h-4 text-muted-foreground" />
          <span>接入主机</span>
        </DialogTitle>
        <DialogDescription class="text-xs leading-relaxed text-muted-foreground">
          新主机运行 Agent 后会出现在这里，核对机器码无误再启用。已启用的主机断线后会自动重连，不需要重新开启发现窗口。
        </DialogDescription>
      </DialogHeader>

      <div class="px-5 pb-5 space-y-5">
        <!-- 发现窗口 -->
        <section aria-labelledby="disc-window" class="flex flex-wrap items-center justify-between gap-2 rounded-sm border border-border/80 px-3 py-2.5">
          <div class="min-w-0 text-xs">
            <h3 id="disc-window" class="font-semibold text-foreground">发现窗口</h3>
            <p v-if="discovery?.always" class="text-muted-foreground mt-0.5">常开（由 RELAYMESH_TERMINAL_DISCOVERY=always 设置，任何新机器码都能出现在待启用列表）</p>
            <p
              v-else-if="discovery?.open"
              class="mt-0.5 tabular-nums"
              :class="windowEnding ? 'text-amber-600 dark:text-amber-400' : 'text-emerald-600 dark:text-emerald-400'"
            >
              开启中 · 剩余 {{ formatCountdown(discovery.remaining_seconds) }}<template v-if="windowEnding">（即将关闭，需要的话先续期）</template>
            </p>
            <p v-else class="text-muted-foreground mt-0.5">已关闭：陌生的机器码会被拒绝，不会出现在列表里</p>
          </div>
          <div v-if="!discovery?.always" class="flex items-center gap-2">
            <!-- 续期放在「立即关闭」左边：正在下载安装包的主机不会因为窗口到点而中断；每次累加，窗口最长保持 60 分钟。 -->
            <Button
              v-if="discovery?.open"
              variant="outline"
              size="sm"
              class="h-7 rounded-sm text-xs"
              :disabled="busyWindow || !canExtend"
              :title="canExtend ? `在剩余时间上再加 ${windowMinutes} 分钟（已签发的安装命令继续有效）` : '发现窗口已经达到 60 分钟的上限，不能再续期'"
              :aria-label="`续期 ${windowMinutes} 分钟`"
              @click="extendWindow"
            >
              <Loader2 v-if="busyWindow" class="w-3.5 h-3.5 animate-spin" />
              <Plus v-else class="w-3.5 h-3.5" />
              续期 {{ windowMinutes }} 分钟
            </Button>
            <Button v-if="discovery?.open" variant="outline" size="sm" class="h-7 rounded-sm text-xs" :disabled="busyWindow" @click="closeWindow">
              <Loader2 v-if="busyWindow" class="w-3.5 h-3.5 animate-spin" />
              立即关闭
            </Button>
            <Button v-else size="sm" class="h-7 rounded-sm text-xs" :disabled="busyWindow" @click="openWindow">
              <Loader2 v-if="busyWindow" class="w-3.5 h-3.5 animate-spin" />
              开启 {{ windowMinutes }} 分钟
            </Button>
          </div>
        </section>

        <!-- 待启用 -->
        <section aria-labelledby="disc-pending" class="space-y-2">
          <h3 id="disc-pending" class="text-xs font-semibold text-foreground">
            待启用的主机<span v-if="store.pending.length" class="ml-1 font-mono text-muted-foreground">({{ store.pending.length }})</span>
          </h3>

          <p v-if="!store.pending.length" class="text-xs text-muted-foreground leading-relaxed py-1">
            {{ discovery?.open || discovery?.always ? '还没有新主机出现。在目标主机上运行下面的安装命令后，它会显示在这里。' : '没有待启用的主机。要接入新主机，请先开启发现窗口。' }}
          </p>

          <ul v-else class="divide-y divide-border/70 rounded-sm border border-border/80">
            <li v-for="p in store.pending" :key="p.machine_code" class="px-3 py-3 space-y-2">
              <div class="flex items-start gap-3">
                <component :is="p.os.toLowerCase().includes('windows') ? Laptop : Server" class="w-4 h-4 mt-0.5 shrink-0 text-muted-foreground" />
                <div class="min-w-0 flex-1 space-y-1">
                  <div class="flex flex-wrap items-center gap-x-2 gap-y-1">
                    <span class="text-sm font-semibold text-foreground truncate max-w-full">{{ p.hostname || '（未命名主机）' }}</span>
                    <span v-if="p.kind === 'temporary'" class="text-[10px] px-1.5 py-px rounded-sm bg-sky-500/10 text-sky-600 dark:text-sky-400">临时</span>
                    <span v-if="privilegeLabel(p.privilege).elevated" class="inline-flex items-center gap-0.5 text-[10px] px-1.5 py-px rounded-sm bg-amber-500/10 text-amber-600 dark:text-amber-400">
                      <Crown class="w-2.5 h-2.5" />{{ privilegeLabel(p.privilege).text }}
                    </span>
                    <span v-if="p.duplicate_name" class="text-[10px] px-1.5 py-px rounded-sm bg-amber-500/10 text-amber-600 dark:text-amber-400">与其他主机同名，请以机器码为准</span>
                  </div>
                  <div class="flex items-center gap-1.5">
                    <code class="font-mono text-xs text-foreground tracking-wide">{{ formatMachineCode(p.machine_code) }}</code>
                    <button
                      type="button"
                      class="p-1 rounded-sm text-muted-foreground hover:text-foreground hover:bg-muted cursor-pointer"
                      :aria-label="`复制机器码 ${p.machine_code}`"
                      @click="copyCode(p.machine_code)"
                    >
                      <Check v-if="copiedCode === p.machine_code" class="w-3 h-3 text-emerald-500" />
                      <Copy v-else class="w-3 h-3" />
                    </button>
                  </div>
                  <p class="text-[11px] text-muted-foreground leading-relaxed">
                    {{ osText(p) }}<template v-if="p.agent_version"> · Agent {{ p.agent_version }}</template> · 来自 {{ p.ip || '未知地址' }} ·
                    {{ p.online ? '在线' : '已离线' }} · 首次出现 {{ relativeTime(p.first_seen) }}
                  </p>
                </div>
                <div v-if="confirming !== p.machine_code" class="flex items-center gap-1.5 shrink-0">
                  <Button size="sm" class="h-7 rounded-sm text-xs" :disabled="busyCode === p.machine_code" @click="startConfirm(p)">启用</Button>
                  <Button variant="ghost" size="sm" class="h-7 rounded-sm text-xs text-muted-foreground" :disabled="busyCode === p.machine_code" @click="ignore(p)">忽略</Button>
                </div>
              </div>

              <form v-if="confirming === p.machine_code" class="ml-7 flex flex-wrap items-end gap-2" @submit.prevent="confirmEnable(p)">
                <label class="space-y-1 block flex-1 min-w-[180px]">
                  <span class="text-[11px] text-muted-foreground">备注（可选，最长 64 个字符）</span>
                  <input
                    v-model="remarkDraft"
                    maxlength="64"
                    class="w-full h-8 px-2.5 rounded-sm bg-background border border-border/80 focus:border-primary focus:outline-none text-xs text-foreground"
                    placeholder="例如：生产数据库"
                    @keydown.esc="onRemarkEsc($event)"
                  />
                </label>
                <Button type="submit" size="sm" class="h-8 rounded-sm text-xs" :disabled="busyCode === p.machine_code">
                  <Loader2 v-if="busyCode === p.machine_code" class="w-3.5 h-3.5 animate-spin" />
                  确认启用
                </Button>
                <Button type="button" variant="ghost" size="sm" class="h-8 rounded-sm text-xs" :disabled="busyCode === p.machine_code" @click="confirming = ''">取消</Button>
                <HostDefaultOption v-if="p.kind !== 'temporary'" v-model="grantDraft" :code="p.machine_code" :disabled="busyCode === p.machine_code" class="basis-full" />
                <p class="basis-full text-[11px] text-muted-foreground leading-relaxed">启用后，任何持有登录密码的管理员都能在这台主机上以 Agent 的运行身份执行命令。</p>
              </form>
            </li>
          </ul>
        </section>

        <!-- 已忽略 -->
        <section v-if="ignored.length" class="space-y-2">
          <button type="button" class="flex items-center gap-1 text-xs font-semibold text-foreground cursor-pointer" :aria-expanded="showIgnored" @click="showIgnored = !showIgnored">
            <component :is="showIgnored ? ChevronDown : ChevronRight" class="w-3.5 h-3.5 text-muted-foreground" />
            已忽略的主机 <span class="font-mono text-muted-foreground">({{ ignored.length }})</span>
          </button>
          <ul v-if="showIgnored" class="divide-y divide-border/70 rounded-sm border border-border/80">
            <li v-for="h in ignored" :key="h.machine_code" class="px-3 py-2 flex items-center gap-3">
              <div class="min-w-0 flex-1">
                <p class="text-xs text-foreground truncate">{{ hostTitle(h) }}</p>
                <code class="font-mono text-[11px] text-muted-foreground">{{ formatMachineCode(h.machine_code) }}</code>
              </div>
              <Button variant="outline" size="sm" class="h-7 rounded-sm text-xs" :disabled="busyCode === h.machine_code" @click="unignore(h)">取消忽略</Button>
            </li>
          </ul>
        </section>

        <!-- 安装命令 -->
        <section aria-labelledby="disc-install" class="space-y-2">
          <button type="button" class="flex items-center gap-1 text-xs font-semibold text-foreground cursor-pointer" :aria-expanded="showInstall" @click="showInstall = !showInstall">
            <component :is="showInstall ? ChevronDown : ChevronRight" class="w-3.5 h-3.5 text-muted-foreground" />
            <span id="disc-install">在目标主机上安装 Agent</span>
          </button>
          <div v-if="showInstall" class="space-y-2">
            <p v-if="!hasAnyHost && !store.pending.length" class="text-xs text-muted-foreground leading-relaxed">
              先开启上面的发现窗口，再到目标主机上执行命令；Agent 启动后会打印它的机器码，和这里显示的核对一致再启用。
            </p>
            <InstallCommand />
          </div>
        </section>
      </div>
    </DialogContent>
  </Dialog>
</template>
