<script setup lang="ts">
import { computed } from 'vue'

// SSH 主机的接入路径。两条路径的管理方式与权限分配完全一致（都有人工终端，模型获得授权后也都能执行命令，都没有文件传输），
// 区别只在「由谁去连、会话放在哪」：
//   Hub 直连——由 Hub 出站连接，目标主机需要能被 Hub 访问到；登录凭据加密保存在 Hub（需要 SSH 主密钥）；
//            终端会话由 Hub 持有，不做记录，Hub 重启时会结束。
//   经网关——由一台装了 Agent 的主机出站连接目标（网关能力随 Agent 默认开启，目标不必能被 Hub 访问）；
//            登录凭据只保存在网关自己的存储里，Hub 不留副本；终端会话由网关持有（Hub 重启后仍在）并有终端记录。
// 没有任何在线的网关主机时「经网关」不可选，并说明怎么得到一个。

const via = defineModel<'direct' | 'gateway'>('via', { required: true })
const gateway = defineModel<string>('gateway', { required: true })
const props = defineProps<{
  /** 可选的网关主机（机器码 + 显示名）。 */
  options: { code: string; label: string }[]
  /** 表单控件 id 的前缀（同一页可能同时有多个对话）。 */
  idPrefix: string
  disabled?: boolean
}>()

const available = computed(() => props.options.length > 0)

const HINT: Record<'direct' | 'gateway', string> = {
  direct:
    '由 Hub 出站连接，目标主机需要能被 Hub 访问到。登录凭据加密保存在 Hub，需要服务端配置 SSH 主密钥；终端会话由 Hub 持有，不做记录，Hub 重启时会结束。',
  gateway:
    '由网关主机出站连接目标，目标不必能被 Hub 访问。登录凭据只加密保存在网关主机自己的存储里，Hub 不留副本，也不需要 SSH 主密钥；终端会话由网关持有（Hub 重启后仍在），并有终端记录。',
}

function pick(v: 'direct' | 'gateway') {
  via.value = v
  // 切到「经网关」时默认选中第一台可用的网关，省一次点击。
  if (v === 'gateway' && !gateway.value) gateway.value = props.options[0]?.code ?? ''
}

const fieldClass =
  'w-full h-8 px-2 rounded-sm bg-background border border-border/80 focus:border-primary focus:outline-none text-xs text-foreground transition-colors disabled:opacity-50 disabled:cursor-not-allowed'
const segClass = 'h-6 flex-1 px-2 rounded-xs text-[11px] cursor-pointer transition-colors disabled:opacity-50 disabled:cursor-not-allowed'
</script>

<template>
  <div class="space-y-1.5" data-testid="ssh-route-field">
    <span class="text-[11px] font-medium text-foreground">接入路径</span>
    <div class="inline-flex w-full rounded-sm border border-border/80 p-0.5 gap-0.5" role="group" aria-label="接入路径">
      <button
        type="button"
        :class="[segClass, via === 'direct' ? 'bg-primary text-primary-foreground' : 'text-muted-foreground hover:bg-muted']"
        :aria-pressed="via === 'direct'"
        :disabled="disabled"
        @click="pick('direct')"
      >Hub 直连</button>
      <button
        type="button"
        :class="[segClass, via === 'gateway' ? 'bg-primary text-primary-foreground' : 'text-muted-foreground hover:bg-muted']"
        :aria-pressed="via === 'gateway'"
        :disabled="disabled || (!available && via !== 'gateway')"
        :title="available || via === 'gateway' ? undefined : '还没有在线、可用的网关主机'"
        @click="pick('gateway')"
      >经网关</button>
    </div>
    <p class="text-[11px] leading-snug text-muted-foreground">{{ HINT[via] }}</p>
    <p class="text-[11px] leading-snug text-muted-foreground">两种方式的管理和授权完全一样：都有人工终端，模型获得授权后也都能执行命令；都没有文件传输。</p>

    <template v-if="via === 'gateway'">
      <label :for="`${idPrefix}-gateway`" class="block text-[11px] font-medium text-foreground">网关主机</label>
      <select :id="`${idPrefix}-gateway`" v-model="gateway" :class="fieldClass" :disabled="disabled || !available" aria-label="网关主机">
        <option v-if="!available" value="" disabled>没有可用的网关主机</option>
        <option v-for="o in options" :key="o.code" :value="o.code">{{ o.label }}</option>
      </select>
      <p v-if="!available" class="text-[11px] leading-snug text-amber-700 dark:text-amber-300" role="status">
        当前没有在线、可用的网关主机。网关是 Agent 的标配能力（与主机服务一样默认开启）：在一台可信主机上用「接入主机」生成命令并安装，主机在线、会话守护进程就绪后，它就会出现在这里；
        如果已装的 Agent 当初用 <code class="font-mono">--no-ssh-gateway</code> 关闭了网关，去掉该参数重新安装即可。
      </p>
    </template>
    <p v-else-if="!available" class="text-[11px] leading-snug text-muted-foreground">
      目标不方便被 Hub 直接访问，或不想让 Hub 保存登录凭据？在一台可信主机上用「接入主机」生成命令并安装 Agent（网关能力默认开启），它在线之后这里就会出现「经网关」。
    </p>
  </div>
</template>
