<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { toast } from 'vue-sonner'
import { KeyRound, Loader2, ShieldCheck } from 'lucide-vue-next'
import { Switch } from '../ui/switch'
import Button from '../ui/button/Button.vue'
import ConfirmDialog from '../terminal/ConfirmDialog.vue'
import { useTerminalStore } from '@/stores/terminal'
import { useExecRunsStore } from '@/stores/execRuns'
import { useCredentialsStore } from '@/stores/credentials'
import { useSettingsUiStore } from '@/stores/settingsUi'
import { UnlockCancelled, toApiError, type HostDefault } from '@/api/terminal'
import {
  MAX_HOSTS_PER_TOKEN,
  accessHosts,
  accessTokens,
  cellGranted,
  columnState,
  grantSummary,
  hiddenGrants,
  overLimit,
  rowState,
  setRow,
  toggleCell,
  wouldGrant,
  wouldRevoke,
  type AccessHost,
  type AccessToken,
} from '@/lib/terminal/modelAccess'

// 模型执行与权限：把「谁（模型令牌）能用哪台主机」放在同一张表里看清楚、改清楚。
//
// 权限只有一条判定：令牌自己的主机名单。主机的「新模型默认」只是预填规则——新增主机时写入现有令牌，
// 新建令牌时预选——写成明确的条目，运行期间不继承，所以这里看到的勾选就是事实。
// 授予（放宽）需要再次验证密码；收回（收紧）只需登录，刹车永远比油门容易。
const terminal = useTerminalStore()
const execRuns = useExecRunsStore()
const credStore = useCredentialsStore()
const settingsUi = useSettingsUiStore()

const loading = ref(true)
const hosts = computed<AccessHost[]>(() => accessHosts(terminal.hosts))
const visible = computed(() => hosts.value.map(h => h.code))
const matrix = computed(() => accessTokens(credStore.credentials))
const rows = computed<AccessToken[]>(() => matrix.value.rows)
const execOff = computed(() => execRuns.status !== null && !execRuns.status.enabled)

onMounted(async () => {
  try {
    await Promise.all([terminal.refreshHosts(), credStore.fetchCredentials(), execRuns.refreshStatus()])
  } finally {
    loading.value = false
  }
})

// ───── 写入：按「令牌新的主机名单」整体保存 ─────
const busy = reactive<Record<string, boolean>>({})

async function withBusy(key: string, fn: () => Promise<void>) {
  if (busy[key]) return
  busy[key] = true
  try {
    await fn()
  } catch (e) {
    if (!(e instanceof UnlockCancelled)) toast.error(toApiError(e).message)
  } finally {
    busy[key] = false
  }
}

/** 保存一个令牌的主机名单；授予需要解锁，由服务端判断，被要求时才弹出解锁。 */
async function saveHosts(t: AccessToken, list: string[]) {
  if (overLimit(list)) {
    toast.error(`一个令牌最多授权 ${MAX_HOSTS_PER_TOKEN} 台主机`)
    return
  }
  await terminal.unlockOnDemand(() => credStore.updateCredential(t.id, { terminal_hosts: list }), '授予模型使用主机需要再次验证密码')
}

/** 让复选框回到真实状态（失败 / 取消解锁 / 确认框被取消时，浏览器已经翻转了它）。 */
function sync(el: HTMLInputElement | null, real: boolean) {
  if (el) el.checked = real
}

// 通配令牌「取消一格 / 清空」要先确认：它同时意味着以后新增的主机不再自动可用。
const ask = reactive({ open: false, title: '', description: '', confirmText: '确认', destructive: false, busy: false })
let askAction: (() => Promise<void>) | null = null

function openAsk(opts: { title: string; description: string; confirmText?: string; destructive?: boolean }, action: () => Promise<void>) {
  ask.title = opts.title
  ask.description = opts.description
  ask.confirmText = opts.confirmText ?? '确认'
  ask.destructive = !!opts.destructive
  askAction = action
  ask.open = true
}

async function runAsk() {
  if (!askAction || ask.busy) return
  ask.busy = true
  try {
    await askAction()
    ask.open = false
  } catch (e) {
    if (!(e instanceof UnlockCancelled)) toast.error(toApiError(e).message)
  } finally {
    ask.busy = false
  }
}

function onCell(t: AccessToken, h: AccessHost, ev: Event) {
  const el = ev.target as HTMLInputElement
  const on = el.checked
  if (t.wildcard && !on) {
    sync(el, true)
    const rest = visible.value.filter(c => c !== h.code).length
    openAsk(
      {
        title: `取消「${t.name}」对 ${h.title} 的授权？`,
        description: `这个令牌现在可以使用全部主机（包括以后新增的）。取消后会改成逐台授权：其余 ${rest} 台保持可用，但以后新增的主机不会再自动可用。`,
        confirmText: '取消授权',
      },
      () => saveHosts(t, toggleCell(t, h.code, false, visible.value)),
    )
    return
  }
  void withBusy(`cell:${t.id}:${h.code}`, async () => {
    try {
      await saveHosts(t, toggleCell(t, h.code, on, visible.value))
    } finally {
      const fresh = rows.value.find(r => r.id === t.id)
      sync(el, fresh ? cellGranted(fresh, h.code) : !on)
    }
  })
}

function onRow(t: AccessToken, ev: Event) {
  const el = ev.target as HTMLInputElement
  const on = el.checked
  if (t.wildcard && !on) {
    sync(el, true)
    openAsk(
      {
        title: `取消「${t.name}」对全部主机的授权？`,
        description: '这个令牌现在可以使用全部主机（包括以后新增的）。取消后它将不能使用任何主机，需要时再逐台勾选。',
        confirmText: '全部取消',
        destructive: true,
      },
      () => saveHosts(t, setRow(t, visible.value, false)),
    )
    return
  }
  void withBusy(`row:${t.id}`, async () => {
    try {
      await saveHosts(t, setRow(t, visible.value, on))
    } finally {
      const fresh = rows.value.find(r => r.id === t.id)
      sync(el, fresh ? rowState(fresh, visible.value) === 'all' : !on)
    }
  })
}

function onColumn(h: AccessHost, ev: Event) {
  const el = ev.target as HTMLInputElement
  const on = el.checked
  // 浏览器已经翻转了复选框：先还原，等确认 / 完成后按真实状态再同步。
  sync(el, columnState(rows.value, h.code) === 'all')
  if (on) {
    const targets = wouldGrant(rows.value, h.code)
    if (targets.length === 0) {
      toast.info('没有需要授予的令牌', { description: '其余令牌要么已经授权，要么是「全部主机」或能力 URL 令牌（能力 URL 令牌必须逐台勾选）。' })
      return
    }
    openAsk(
      {
        title: `把 ${h.title} 授予现有模型？`,
        description: `${grantSummary(targets.map(t => t.name))}。授予会写进每个令牌自己的授权名单并记入审计；能力 URL 令牌与「全部主机」令牌不受影响。`,
        confirmText: `授予 ${targets.length} 个模型`,
      },
      async () => {
        const r = await terminal.grantModelsToExisting(h.code)
        await credStore.fetchCredentials()
        toast.success(`已授予 ${r.granted.length} 个模型令牌`, r.full > 0 ? { description: `另有 ${r.full} 个令牌的名单已满，没有写入。` } : undefined)
      },
    )
    return
  }
  const targets = wouldRevoke(rows.value, h.code)
  if (targets.length === 0) return
  const wildcards = rows.value.filter(t => t.wildcard).length
  openAsk(
    {
      title: `从 ${targets.length} 个模型收回 ${h.title}？`,
      description:
        `${targets.map(t => t.name).slice(0, 4).join('、')}${targets.length > 4 ? ' 等' : ''} 将不能再使用这台主机，进行中的命令不受影响。` +
        (wildcards > 0 ? `另有 ${wildcards} 个「全部主机」令牌不受影响，需要在表里逐个改成逐台授权。` : ''),
      confirmText: '收回',
      destructive: true,
    },
    async () => {
      for (const t of targets) await saveHosts(t, toggleCell(t, h.code, false, visible.value))
      toast.success(`已从 ${targets.length} 个模型收回`)
    },
  )
}

// ───── 主机的模型策略 ─────
async function setAllowModel(h: AccessHost, on: boolean) {
  await withBusy(`allow:${h.code}`, async () => {
    if (await execRuns.setHostModel(h.code, { allow_model: on })) {
      toast.success(on ? '已允许模型使用这台主机' : '已不再允许模型使用这台主机', {
        description: on ? '模型还需要持有带终端权限、且授权了这台主机的令牌。' : '这台主机上进行中的模型命令已被取消。',
      })
    }
  })
}

async function setDefault(h: AccessHost, value: HostDefault) {
  if ((value === 'allow') === h.defaultAllow) return
  await withBusy(`default:${h.code}`, async () => {
    if (await execRuns.setHostModel(h.code, { default_for_new_models: value })) {
      toast.success(value === 'allow' ? '以后新建的令牌会预选这台主机' : '以后新建的令牌不再预选这台主机', {
        description: '这只影响新建的令牌；现有令牌的授权名单不会被改动。',
      })
    }
  })
}

const DEFAULT_OPTIONS: { value: HostDefault; label: string }[] = [
  { value: 'deny', label: '不授予' },
  { value: 'allow', label: '授予' },
]
</script>

<template>
  <div class="space-y-5" data-testid="exec-access">
    <div class="flex items-center justify-between pb-1.5 border-b border-border/70">
      <div class="flex items-center gap-1.5">
        <ShieldCheck class="w-3.5 h-3.5 text-primary" />
        <span class="text-xs font-bold font-mono text-foreground">模型执行与权限</span>
      </div>
      <Button variant="ghost" size="sm" class="h-6 px-2 text-[10px] rounded-xs gap-1" data-testid="access-to-security" @click="settingsUi.select('security')">
        <KeyRound class="w-3 h-3" />管理令牌
      </Button>
    </div>

    <p class="text-[11px] leading-relaxed text-muted-foreground">
      模型能不能用一台主机，只看<strong class="text-foreground">它的令牌自己的授权名单</strong>，再加上主机是否打开了「允许模型使用」。
      下面的「新模型默认」只是预填规则：新增主机、新建令牌时帮你预先勾好，写进名单后就和手工勾选的没有区别，不会在运行期间悄悄改变谁能用什么。
    </p>

    <p v-if="execOff" class="rounded-xs border border-amber-500/30 bg-amber-500/5 px-3 py-2 text-[11px] leading-relaxed text-amber-600 dark:text-amber-400" data-testid="access-exec-off">
      服务端还没有开启模型执行（需要运维设置 <code class="font-mono">RELAYMESH_TERMINAL_EXEC=on</code>）。这里的授权会照常保存，开启之后才会生效。
    </p>

    <p v-if="loading" class="text-xs text-muted-foreground flex items-center gap-1.5"><Loader2 class="w-3.5 h-3.5 animate-spin" />正在读取…</p>

    <template v-else>
      <p v-if="hosts.length === 0" class="rounded-xs border border-border/70 bg-card/60 px-3 py-3 text-xs leading-relaxed text-muted-foreground" data-testid="access-no-hosts">
        还没有已启用的主机。先在 ExecMesh 的「主机」页接入并启用主机，再回到这里授权模型。
      </p>

      <template v-else>
        <!-- 主机的模型策略 -->
        <section class="space-y-2" aria-label="主机的模型策略" data-testid="access-hosts">
          <h3 class="text-xs font-semibold text-foreground">主机的模型策略</h3>
          <ul class="divide-y divide-border/60 rounded-xs border border-border/70">
            <li v-for="h in hosts" :key="h.code" class="flex flex-wrap items-center gap-x-4 gap-y-2 px-3 py-2.5" :data-testid="`access-host-${h.code}`">
              <div class="min-w-0 flex-1 basis-40">
                <div class="flex items-center gap-1.5">
                  <span class="truncate text-xs font-medium text-foreground" :title="h.title">{{ h.title }}</span>
                  <span v-if="h.ssh" class="text-[9px] px-1.5 rounded-full font-mono bg-violet-500/10 text-violet-500">SSH</span>
                  <span class="text-[9px] px-1.5 rounded-full font-mono" :class="h.online ? 'bg-emerald-500/10 text-emerald-500' : 'bg-muted text-muted-foreground'">{{ h.online ? '在线' : '离线' }}</span>
                </div>
                <div class="font-mono text-[10px] text-muted-foreground">{{ h.short }}</div>
              </div>
              <label class="flex items-center gap-2 text-[11px] text-muted-foreground cursor-pointer">
                <Switch
                  :checked="h.allowModel"
                  :disabled="busy[`allow:${h.code}`]"
                  :aria-label="`允许模型使用 ${h.title}`"
                  :data-testid="`allow-${h.code}`"
                  @update:checked="(v: boolean) => setAllowModel(h, v)"
                />
                <span>{{ h.allowModel ? '允许模型使用' : '不允许模型使用' }}</span>
              </label>
              <div class="flex items-center gap-2 text-[11px] text-muted-foreground">
                <span>新模型默认</span>
                <div role="group" :aria-label="`${h.title} 的新模型默认`" class="inline-flex rounded-xs border border-border/70 overflow-hidden">
                  <button
                    v-for="o in DEFAULT_OPTIONS"
                    :key="o.value"
                    type="button"
                    class="px-2 py-1 text-[11px] transition-colors cursor-pointer disabled:opacity-60 disabled:cursor-not-allowed"
                    :class="(o.value === 'allow') === h.defaultAllow ? 'bg-primary text-primary-foreground' : 'bg-background hover:bg-muted text-muted-foreground'"
                    :aria-pressed="(o.value === 'allow') === h.defaultAllow"
                    :disabled="busy[`default:${h.code}`]"
                    :data-testid="`default-${o.value}-${h.code}`"
                    @click="setDefault(h, o.value)"
                  >{{ o.label }}</button>
                </div>
              </div>
            </li>
          </ul>
        </section>

        <!-- 授权矩阵 -->
        <section class="space-y-2" aria-label="模型与主机的授权矩阵" data-testid="access-matrix-section">
          <h3 class="text-xs font-semibold text-foreground">模型 × 主机</h3>
          <p v-if="rows.length === 0" class="rounded-xs border border-border/70 bg-card/60 px-3 py-3 text-xs leading-relaxed text-muted-foreground" data-testid="access-no-tokens">
            还没有带终端权限的模型令牌。到「MCP 与安全」创建一个 ExecMesh 档令牌后，会出现在这里。
          </p>
          <div v-else class="overflow-x-auto rounded-xs border border-border/70" data-testid="access-matrix">
            <table class="min-w-full border-collapse text-xs">
              <thead>
                <tr class="bg-muted/40">
                  <th scope="col" class="sticky left-0 z-10 min-w-[180px] bg-muted/40 px-3 py-2 text-left text-[11px] font-medium text-muted-foreground">模型令牌</th>
                  <th v-for="h in hosts" :key="h.code" scope="col" class="min-w-[96px] px-2 py-2 text-center align-bottom font-normal">
                    <div class="mx-auto max-w-[110px] truncate text-[11px] text-foreground" :title="h.title">{{ h.title }}</div>
                    <div class="font-mono text-[9px] text-muted-foreground">{{ h.short }}</div>
                    <div v-if="!h.allowModel" class="text-[9px] text-amber-600 dark:text-amber-400" title="这台主机没有打开「允许模型使用」：被授权的模型也暂时用不了">未开放</div>
                    <input
                      type="checkbox"
                      class="mt-1 accent-primary cursor-pointer"
                      :checked="columnState(rows, h.code) === 'all'"
                      :indeterminate="columnState(rows, h.code) === 'some'"
                      :aria-label="`把 ${h.title} 授予全部模型 / 从全部模型收回`"
                      :data-testid="`col-${h.code}`"
                      @change="onColumn(h, $event)"
                    />
                  </th>
                </tr>
              </thead>
              <tbody class="divide-y divide-border/50">
                <tr v-for="t in rows" :key="t.id" :class="!t.active || t.expired ? 'opacity-60' : ''" :data-testid="`row-${t.id}`">
                  <th scope="row" class="sticky left-0 z-10 bg-background px-3 py-1.5 text-left font-normal">
                    <div class="flex items-center gap-2">
                      <input
                        type="checkbox"
                        class="accent-primary cursor-pointer"
                        :checked="rowState(t, visible) === 'all'"
                        :indeterminate="rowState(t, visible) === 'some'"
                        :disabled="busy[`row:${t.id}`]"
                        :aria-label="`让 ${t.name} 使用全部主机 / 清空`"
                        :data-testid="`rowall-${t.id}`"
                        @change="onRow(t, $event)"
                      />
                      <span class="max-w-[140px] truncate font-mono text-[11px] text-foreground" :title="t.name">{{ t.name }}</span>
                      <span v-if="t.wildcard" class="rounded-full bg-sky-500/10 px-1.5 text-[9px] font-mono text-sky-600 dark:text-sky-400" title="这个令牌写的是「全部主机」，含以后新增的">全部主机</span>
                      <span v-if="t.capability" class="rounded-full bg-violet-500/10 px-1.5 text-[9px] font-mono text-violet-500" title="能力 URL 令牌：必须逐台授权，也不会被自动授予">能力 URL</span>
                      <span v-if="t.files" class="rounded-full bg-muted px-1.5 text-[9px] font-mono text-muted-foreground">文件</span>
                      <span v-if="!t.active" class="rounded-full bg-muted px-1.5 text-[9px] font-mono text-muted-foreground">已停用</span>
                      <span v-if="t.expired" class="rounded-full bg-muted px-1.5 text-[9px] font-mono text-muted-foreground">已过期</span>
                    </div>
                    <div v-if="hiddenGrants(t, visible) > 0" class="ml-6 text-[10px] text-muted-foreground">另含 {{ hiddenGrants(t, visible) }} 台未启用主机的授权（不在表中）</div>
                  </th>
                  <td v-for="h in hosts" :key="h.code" class="px-2 py-1.5 text-center">
                    <input
                      type="checkbox"
                      class="accent-primary cursor-pointer"
                      :checked="cellGranted(t, h.code)"
                      :disabled="busy[`cell:${t.id}:${h.code}`] || busy[`row:${t.id}`]"
                      :aria-label="`${t.name} 使用 ${h.title}`"
                      :data-testid="`cell-${t.id}-${h.code}`"
                      @change="onCell(t, h, $event)"
                    />
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
          <p v-if="rows.length > 0" class="text-[10px] leading-relaxed text-muted-foreground">
            勾选 = 令牌自己的授权名单里有这台主机。授予需要再次验证密码，收回只需登录。
            表头的复选框一次性授予 / 收回全部模型，行首的复选框让一个模型使用全部 / 不使用任何主机。
            <template v-if="matrix.skipped > 0">另有 {{ matrix.skipped }} 个令牌没有终端权限，不在此列出。</template>
          </p>
        </section>
      </template>
    </template>

    <ConfirmDialog
      v-model:open="ask.open"
      :title="ask.title"
      :description="ask.description"
      :confirm-text="ask.confirmText"
      :destructive="ask.destructive"
      :busy="ask.busy"
      @confirm="runAsk"
    />
  </div>
</template>
