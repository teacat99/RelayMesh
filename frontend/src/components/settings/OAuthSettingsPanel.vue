<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Copy } from 'lucide-vue-next'
import { toast } from 'vue-sonner'
import {
  oauthApi,
  type OAuthClientRow,
  type OAuthDcrMode,
  type OAuthEffective,
  type OAuthGrantRow,
  type OAuthPolicy,
} from '../../api/client'
import { useTerminalStore } from '../../stores/terminal'
import {
  applyClientPreset,
  grantCreatedText,
  grantPermissionText,
  grantUsageText,
  policyDirty,
  publicBaseNote,
  switchState,
  toneClasses,
  type ClientPresetId,
} from '../../lib/oauthSetup'
import { DCR_MODE_OPTIONS, dcrModeOf, emptyClientsText, windowView, withDcrMode } from '../../lib/oauthWindow'
import { copyText } from '../../lib/terminal/clipboard'
import { Button } from '../ui/button'
import ConfirmDialog from '../terminal/ConfirmDialog.vue'
import OAuthClientWizard from './OAuthClientWizard.vue'
import OAuthPendingClients from './OAuthPendingClients.vue'
import OAuthSelfCheck from './OAuthSelfCheck.vue'
import OAuthWindowCard from './OAuthWindowCard.vue'

// 「设置 → OAuth 授权」整页，从上到下：一句话说明 → 状态卡（总开关是否真的生效 / 对外地址与来源 / 连接器地址 / 一键自检）
// → 接入窗口（开 / 续 / 关，倒计时）→ 接入向导 → 刚登记、还没授权的客户端 → 已授权的应用 → 授权服务器设置（常用项 + 折叠的高级项）。
// 流程和 ExecMesh 的「接入主机」一样：开窗口 → 去客户端添加 → 回来核对并管理。
// 文案口径与 docs/oauth.md「快速开始」一致；逻辑在 lib/oauthSetup.ts 与 lib/oauthWindow.ts（有单元测试）。
const terminal = useTerminalStore()

const grants = ref<OAuthGrantRow[]>([])
const grantsError = ref('')
const clients = ref<OAuthClientRow[]>([])
const clientsError = ref('')
const expireHours = ref(24)
/** 表单草稿（含还没保存的改动） */
const policy = ref<OAuthPolicy | null>(null)
/** 服务端当前保存的策略，用来判断「有没有未保存的改动」 */
const saved = ref<OAuthPolicy | null>(null)
/** 服务端算出来的「此刻真正生效的状态」；旧后端没有这个字段时为 null，页面退回只有表单的样子 */
const effective = ref<OAuthEffective | null>(null)
const loading = ref(true)
const loadError = ref('')
const saving = ref(false)

const sw = computed(() => switchState(effective.value))
const baseNote = computed(() => publicBaseNote(effective.value))
const envOff = computed(() => effective.value?.env_off === true)
const dirty = computed(() => policyDirty(saved.value, policy.value))
const dcrMode = computed<OAuthDcrMode>(() => (policy.value ? dcrModeOf(policy.value) : 'window'))

// ───────────── 接入窗口：倒计时、开 / 续 / 关 ─────────────
// 剩余时间以「收到状态那一刻」为基准在浏览器里递减（不依赖两边时钟对得准）；窗口开着时每秒推进一次，
// 并每隔几秒向服务端读一次真实状态与登记清单——你在 ChatGPT / Claude 里添加连接器时，回来就能看到新登记。
/** 窗口开着时，每隔多少秒向服务端刷新一次状态与清单 */
const LIVE_REFRESH_SECONDS = 5
const fetchedAt = ref(Date.now())
const nowMs = ref(Date.now())
const view = computed(() => windowView(effective.value, fetchedAt.value, nowMs.value))
const windowKind = computed(() => view.value?.kind ?? null)
const busyWindow = ref(false)

let ticker: number | null = null
let tickCount = 0

function stopTicker() {
  if (ticker !== null) {
    window.clearInterval(ticker)
    ticker = null
  }
}

function startTicker() {
  if (ticker !== null) return
  tickCount = 0
  ticker = window.setInterval(() => {
    nowMs.value = Date.now()
    tickCount++
    if (tickCount % LIVE_REFRESH_SECONDS === 0) void refreshLive()
  }, 1000)
}

watch(
  windowKind,
  (kind, prev) => {
    if (kind === 'open') startTicker()
    else stopTicker()
    // 倒计时走到 0：窗口关了。向服务端核对一次，免得本地与真实状态对不上。
    if (prev === 'open' && kind === 'closed') void refreshLive()
  },
  { immediate: true },
)

function markFetched() {
  fetchedAt.value = Date.now()
  nowMs.value = fetchedAt.value
}

function onVisible() {
  if (document.visibilityState === 'visible' && !loading.value) void refreshLive()
}

function clonePolicy(p: OAuthPolicy): OAuthPolicy {
  return { ...p }
}

async function loadPolicy() {
  const p = await oauthApi.getPolicy()
  effective.value = p.effective ?? null
  markFetched()
  saved.value = clonePolicy(p.policy)
  policy.value = clonePolicy(p.policy)
}

async function loadGrants() {
  grantsError.value = ''
  // 被环境变量关闭时授权相关的接口根本没有注册，不去请求。
  if (envOff.value) {
    grants.value = []
    return
  }
  try {
    grants.value = (await oauthApi.listGrants()).grants
  } catch (e: unknown) {
    grants.value = []
    grantsError.value = `读取授权列表失败：${e instanceof Error ? e.message : '未知错误'}`
  }
}

async function loadClients() {
  clientsError.value = ''
  if (envOff.value) {
    clients.value = []
    return
  }
  try {
    const r = await oauthApi.listClients()
    clients.value = r.clients
    if (r.expire_hours > 0) expireHours.value = r.expire_hours
  } catch (e: unknown) {
    clients.value = []
    clientsError.value = `读取登记清单失败：${e instanceof Error ? e.message : '未知错误'}`
  }
  nowMs.value = Date.now()
}

/**
 * 只刷新「此刻的真实状态」与登记清单，不碰表单草稿（用户可能正在改还没保存的设置）。
 * 窗口开着时每隔几秒调一次，回到本页标签时也调一次。
 */
async function refreshLive() {
  if (envOff.value || loading.value) return
  try {
    const p = await oauthApi.getPolicy()
    effective.value = p.effective ?? null
    markFetched()
  } catch {
    /* 偶发失败，下一轮再试：倒计时仍按上一次的到期时间在本地递减 */
  }
  await loadClients()
}

async function reload() {
  loading.value = true
  loadError.value = ''
  try {
    await loadPolicy()
    await loadGrants()
    await loadClients()
  } catch (e: unknown) {
    loadError.value = e instanceof Error ? e.message : '读取失败'
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  document.addEventListener('visibilitychange', onVisible)
  void reload()
})

onBeforeUnmount(() => {
  stopTicker()
  document.removeEventListener('visibilitychange', onVisible)
})

async function runWindow(action: 'open' | 'extend' | 'close') {
  if (busyWindow.value) return
  busyWindow.value = true
  try {
    const minutes = effective.value?.dcr?.default_minutes ?? 10
    const res = await terminal.withUnlock(
      () => (action === 'close' ? oauthApi.closeWindow() : oauthApi.openWindow(minutes, action === 'extend')),
      action === 'close' ? '关闭接入窗口需要再次验证密码' : '开启接入窗口会让客户端能向这台 Hub 登记，需要再次验证密码',
    )
    if (effective.value) effective.value = { ...effective.value, dcr: res.dcr }
    markFetched()
    await loadClients()
  } catch (e: unknown) {
    terminal.notifyError(e, action === 'close' ? '关闭接入窗口失败' : action === 'extend' ? '续期接入窗口失败' : '开启接入窗口失败')
    // 被服务端拒绝多半是页面上的状态已经过时（比如刚在别处改了自动注册方式）：重读一次真实状态。
    await refreshLive()
  } finally {
    busyWindow.value = false
  }
}

async function copyConnector() {
  const url = effective.value?.connector_url
  if (!url) return
  if (await copyText(url)) toast.success('连接器地址已复制')
  else toast.error('复制失败，请手动选中地址后复制')
}

/** 「按此客户端设置」：只把该客户端需要的开关勾进表单草稿；不保存，也不碰总开关与对外地址。 */
function onApplyPreset(id: ClientPresetId) {
  if (policy.value) policy.value = applyClientPreset(policy.value, id)
}

function setDcrMode(mode: OAuthDcrMode) {
  if (policy.value) policy.value = withDcrMode(policy.value, mode)
}

async function savePolicy() {
  if (!policy.value) return
  const draft = policy.value
  saving.value = true
  try {
    // 总开关 / 对外地址 / 自动注册决定公网上谁能申请终端能力，保存前要再次验证密码。
    await terminal.withUnlock(
      () => oauthApi.savePolicy(draft),
      '修改 OAuth 策略会改变公网上谁能向这台 Hub 申请终端能力，需要再次验证密码',
    )
    toast.success('OAuth 策略已保存')
    // 重新读取：状态卡要反映「现在是否真的生效、对外地址来自哪里」，而不是我们刚填的值。
    await loadPolicy()
    await loadClients()
  } catch (e: unknown) {
    const msg = e instanceof Error ? e.message : '保存失败'
    toast.error(msg)
  } finally {
    saving.value = false
  }
}

// ───────────── 删除一条还没授权的登记（应用内确认） ─────────────
const deleteClientTarget = ref<OAuthClientRow | null>(null)
const deletingClient = ref(false)
const deleteClientOpen = computed({
  get: () => deleteClientTarget.value !== null,
  set: (v: boolean) => {
    if (!v && !deletingClient.value) deleteClientTarget.value = null
  },
})

async function confirmDeleteClient() {
  const c = deleteClientTarget.value
  if (!c) return
  deletingClient.value = true
  try {
    await terminal.withUnlock(() => oauthApi.deleteClient(c.id), '删除登记会让这个客户端不能再发起授权，需要再次验证密码')
    toast.success(`已删除「${c.client_name || '未命名客户端'}」的登记`)
    deleteClientTarget.value = null
    await loadClients()
  } catch (e: unknown) {
    terminal.notifyError(e, '删除登记失败')
    // 找不到多半是它刚好被授权了或已被清理：刷新清单，让页面回到真实状态。
    await loadClients()
  } finally {
    deletingClient.value = false
  }
}

// ───────────── 撤销授权（应用内确认，替代浏览器的 confirm()） ─────────────
const revokeTarget = ref<OAuthGrantRow | null>(null)
const revoking = ref(false)
const revokeOpen = computed({
  get: () => revokeTarget.value !== null,
  set: (v: boolean) => {
    if (!v && !revoking.value) revokeTarget.value = null
  },
})

async function confirmRevoke() {
  const g = revokeTarget.value
  if (!g) return
  revoking.value = true
  try {
    await oauthApi.revokeGrant(g.id)
    toast.success(`已撤销「${g.client_name}」的授权`)
    revokeTarget.value = null
    await loadGrants() // 只刷新列表：不要把表单里还没保存的改动冲掉
  } catch (e: unknown) {
    toast.error(e instanceof Error ? e.message : '撤销失败')
  } finally {
    revoking.value = false
  }
}
</script>

<template>
  <div data-testid="oauth-settings-panel" class="space-y-4">
    <p class="text-[10px] leading-relaxed text-muted-foreground">
      让 ChatGPT、Claude 这类连接器在你确认之后自动获得授权：不用复制粘贴任何密钥，随时可以撤销。
      它和「MCP 与安全」页里的 MCP 凭据是两回事——MCP 令牌是你手动复制粘贴的长期凭据。OAuth 默认关闭。
    </p>

    <div v-if="loading" class="text-xs text-muted-foreground">正在读取设置…</div>
    <div v-else-if="loadError" class="flex flex-wrap items-center gap-2 text-xs text-destructive" data-testid="oauth-load-error">
      读取失败：{{ loadError }}
      <Button size="sm" variant="outline" class="h-7 text-[10px]" @click="reload">重试</Button>
    </div>

    <template v-else>
      <!-- 状态卡：总开关此刻是否真的生效、对外地址（与来源）、连接器地址、一键自检 -->
      <section v-if="effective" class="space-y-3 rounded-xs border border-border/60 p-3" data-testid="oauth-status">
        <div class="space-y-1">
          <div class="flex flex-wrap items-center gap-2">
            <span class="w-16 shrink-0 text-xs text-muted-foreground">总开关</span>
            <span
              v-if="sw"
              class="inline-flex items-center rounded-xs border px-2 py-0.5 text-xs font-medium"
              :class="toneClasses(sw.tone)"
              data-testid="oauth-switch-state"
              :data-kind="sw.kind"
            >
              {{ sw.title }}
            </span>
          </div>
          <p v-if="sw" class="pl-[4.5rem] text-[10px] leading-relaxed text-muted-foreground">{{ sw.detail }}</p>
        </div>

        <template v-if="!envOff">
          <div class="space-y-1.5">
            <div class="flex flex-wrap items-start gap-2">
              <span class="w-16 shrink-0 pt-px text-xs text-muted-foreground">对外地址</span>
              <code class="min-w-0 flex-1 break-all font-mono text-[11px]" data-testid="oauth-public-base">{{ effective.public_base }}</code>
            </div>
            <div
              v-if="baseNote"
              class="ml-[4.5rem] rounded-xs border px-2 py-1 text-[10px] leading-relaxed"
              :class="toneClasses(baseNote.tone)"
              data-testid="oauth-base-note"
              :data-tone="baseNote.tone"
            >
              <div class="font-medium">{{ baseNote.label }}</div>
              <div v-if="baseNote.detail">{{ baseNote.detail }}</div>
            </div>
          </div>

          <div class="space-y-1">
            <div class="flex flex-wrap items-start gap-2">
              <span class="w-16 shrink-0 pt-px text-xs text-muted-foreground">连接器地址</span>
              <code class="min-w-0 flex-1 break-all font-mono text-[11px]" data-testid="oauth-connector-url">{{ effective.connector_url }}</code>
              <Button size="sm" variant="outline" class="h-7 shrink-0 text-[10px]" data-testid="oauth-copy-connector" @click="copyConnector">
                <Copy class="mr-1 size-3" />复制
              </Button>
            </div>
            <p class="pl-[4.5rem] text-[10px] leading-relaxed text-muted-foreground">
              在 ChatGPT / Claude 的「添加自定义连接器」里粘贴这个地址（就是对外地址加上 /mcp）。
            </p>
          </div>

          <OAuthSelfCheck :effective="effective" :dirty="dirty" />
        </template>
      </section>

      <template v-if="!envOff">
        <!-- 接入窗口：自动注册默认是限时窗口，添加新的客户端之前先在这里开一段时间 -->
        <OAuthWindowCard
          v-if="view"
          :view="view"
          :busy="busyWindow"
          @open="runWindow('open')"
          @extend="runWindow('extend')"
          @close="runWindow('close')"
        />

        <OAuthClientWizard v-if="effective && policy" :connector-url="effective.connector_url" :policy="policy" :switch-on="effective.enabled" @apply="onApplyPreset" />

        <!-- 刚登记、还没授权的客户端：窗口开着时回来核对；不认识的删除登记 -->
        <OAuthPendingClients
          :clients="clients"
          :expire-hours="expireHours"
          :error="clientsError"
          :empty-text="emptyClientsText(windowKind)"
          :busy-id="deletingClient ? (deleteClientTarget?.id ?? '') : ''"
          :now-ms="nowMs"
          @delete="deleteClientTarget = $event"
          @refresh="loadClients"
        />

        <!-- 已授权的应用 -->
        <section class="space-y-2 rounded-xs border border-border/60 p-3" data-testid="oauth-grants">
          <div>
            <h4 class="text-xs font-bold">已授权的应用</h4>
            <p class="mt-0.5 text-[10px] text-muted-foreground">通过 OAuth 授权的应用；它们的令牌是隐藏凭据，不会出现在「MCP 与安全」页的 MCP 凭据列表里。</p>
          </div>
          <p v-if="grantsError" class="text-xs text-destructive" data-testid="oauth-grants-error">{{ grantsError }}</p>
          <ul v-else-if="grants.length" class="space-y-2">
            <li
              v-for="g in grants"
              :key="g.id"
              class="flex items-start justify-between gap-2 rounded-xs border border-border/60 p-2 text-xs"
              :data-testid="`oauth-grant-${g.id}`"
              :data-revoked="g.revoked ? 'true' : 'false'"
            >
              <div class="min-w-0 space-y-0.5">
                <div class="flex flex-wrap items-center gap-1.5">
                  <span class="break-all font-medium">{{ g.client_name }}</span>
                  <span v-if="g.revoked" class="text-[10px] text-destructive">已撤销</span>
                </div>
                <div class="text-muted-foreground">{{ grantPermissionText(g) }}</div>
                <div class="text-[10px] text-muted-foreground">{{ grantCreatedText(g) }} · {{ grantUsageText(g) }}</div>
              </div>
              <Button v-if="!g.revoked" size="sm" variant="outline" class="h-7 shrink-0 text-[10px]" :data-testid="`oauth-revoke-${g.id}`" @click="revokeTarget = g">撤销</Button>
            </li>
          </ul>
          <p v-else class="text-xs text-muted-foreground">还没有应用通过 OAuth 授权。</p>
        </section>

        <!-- 授权服务器设置：常用项直接露出，协议词与几乎不用改的折进「高级」 -->
        <section v-if="policy" class="space-y-3 rounded-xs border border-border/60 p-3" data-testid="oauth-policy">
          <div>
            <h4 class="text-xs font-bold">授权服务器设置</h4>
            <p class="mt-0.5 text-[10px] text-muted-foreground">改完点最下面的「保存策略」才会生效；保存需要再次验证 Web 密码。</p>
          </div>

          <label class="flex items-start gap-2 text-xs">
            <input v-model="policy.enabled" type="checkbox" data-testid="oauth-enabled" class="mt-0.5" />
            <span>
              <span class="font-medium">启用 OAuth 授权服务器</span>
              <span class="mt-0.5 block text-[10px] leading-relaxed text-muted-foreground">
                默认关闭。关闭时所有 /oauth/* 与元数据端点返回 404，已授权的应用也会立即无法访问（授权记录保留，重新开启后恢复；要永久回收请在上面「已授权的应用」里撤销）。
              </span>
            </span>
          </label>

          <div class="space-y-1 text-xs">
            <label class="font-medium" for="oauth-public-url">对外地址</label>
            <input
              id="oauth-public-url"
              v-model="policy.public_url"
              data-testid="oauth-public-url"
              type="url"
              placeholder="https://relaymesh.example.com"
              class="h-8 w-full rounded-xs border bg-background px-2 font-mono text-[11px]"
            />
            <p class="text-[10px] leading-relaxed text-muted-foreground">
              ChatGPT / Claude 从公网访问这台 Hub 用的地址：https:// 开头，只写到域名（端口不是 443 时要带上），不要带路径；本机调试可以用 http://localhost:端口。
              留空时按你现在打开页面的地址推导，只适合自己试用。设置了环境变量 RELAYMESH_PUBLIC_URL 时以它为准。
            </p>
          </div>

          <!-- 自动注册：ChatGPT、Claude、Claude Code 都靠它连接；平时是限时窗口，要添加新客户端时在「接入窗口」里开一段时间 -->
          <fieldset class="space-y-1.5 text-xs" data-testid="oauth-dcr-mode" :data-mode="dcrMode">
            <legend class="font-medium">自动注册（动态客户端注册，DCR）</legend>
            <p class="text-[10px] leading-relaxed text-muted-foreground">
              ChatGPT、Claude、Claude Code 都靠它连接：客户端第一次连上时自己向 RelayMesh 登记，不用你手填 Client ID / Secret。
              只有客户端 ID 本身是一个 https 地址的客户端（CIMD）不经过登记。每次授权仍然要你登录并点「批准」。
            </p>
            <label v-for="o in DCR_MODE_OPTIONS" :key="o.id" class="flex items-start gap-2">
              <input
                type="radio"
                name="oauth-dcr-mode"
                :value="o.id"
                :checked="dcrMode === o.id"
                :data-testid="`oauth-dcr-${o.id}`"
                class="mt-0.5"
                @change="setDcrMode(o.id)"
              />
              <span>
                <span class="font-medium">{{ o.label }}</span>
                <span class="mt-0.5 block text-[10px] leading-relaxed text-muted-foreground">{{ o.hint }}</span>
              </span>
            </label>
          </fieldset>

          <p class="text-[10px] leading-relaxed text-muted-foreground" data-testid="oauth-consent-note">
            批准页的提醒是固定的、没有开关：官方 ChatGPT / Claude 的回调地址不提示，其他陌生来源和本机回环地址（127.0.0.1 / localhost）会显示黄色提醒，请确认认识这个应用再点「批准」。提醒只是提醒，不会替你拒绝授权。
          </p>

          <details class="rounded-xs border border-border/60 p-2.5 text-xs" data-testid="oauth-advanced">
            <summary class="cursor-pointer select-none font-medium">高级设置（一般不用改）</summary>
            <div class="mt-3 space-y-4">
              <div class="space-y-1">
                <label class="font-medium" for="oauth-access-ttl">访问令牌有效期（分钟）</label>
                <input
                  id="oauth-access-ttl"
                  v-model.number="policy.access_token_ttl_minutes"
                  type="number"
                  min="5"
                  max="1440"
                  class="h-7 w-24 rounded-xs border bg-background px-1"
                />
                <p class="text-[10px] leading-relaxed text-muted-foreground">到期后客户端会用刷新令牌自动换新的，不用你重新授权。</p>
              </div>

              <p class="text-[10px] leading-relaxed text-muted-foreground" data-testid="oauth-refresh-readonly">
                刷新令牌：连续 {{ policy.refresh_idle_days }} 天不用就失效，最长 {{ policy.refresh_absolute_days }} 天（在这个页面里不能修改）。
              </p>
            </div>
          </details>

          <div class="flex flex-wrap items-center gap-2">
            <Button size="sm" :disabled="saving" data-testid="oauth-save" @click="savePolicy">保存策略</Button>
            <span v-if="dirty" class="text-[10px] text-amber-600 dark:text-amber-400" data-testid="oauth-dirty">有还没保存的改动</span>
          </div>
        </section>
      </template>
    </template>

    <ConfirmDialog
      v-model:open="deleteClientOpen"
      :title="`删除「${deleteClientTarget?.client_name || '未命名客户端'}」的登记？`"
      description="删除后这个客户端不能再发起授权，它在授权页上等待的请求也会作废。它之后想连接，需要重新登记：先开启接入窗口，再在客户端里重新添加。"
      confirm-text="删除登记"
      destructive
      :busy="deletingClient"
      @confirm="confirmDeleteClient"
    />

    <ConfirmDialog
      v-model:open="revokeOpen"
      :title="`撤销「${revokeTarget?.client_name ?? ''}」的授权？`"
      description="撤销后该应用的令牌立即失效，它正在执行的命令会被结束；之后它想再用，需要重新走一遍授权。"
      confirm-text="撤销授权"
      destructive
      :busy="revoking"
      @confirm="confirmRevoke"
    />
  </div>
</template>
