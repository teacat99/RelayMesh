<script setup lang="ts">
import type { OAuthClientRow } from '../../api/client'
import { toneClasses } from '../../lib/oauthSetup'
import { clientBadges, clientHostsText, clientRegisteredText, clientRegisteredTitle } from '../../lib/oauthWindow'
import { Button } from '../ui/button'

// 刚登记、还没授权的客户端：客户端自动登记之后，还要你在授权页点「批准」才算连上；这里列出登记了、但还没走完授权的，
// 不认识的可以删除登记（对应 ExecMesh 接入主机里的「忽略」）。批准发生在授权页，不会有「启用」按钮。
defineProps<{
  clients: OAuthClientRow[]
  /** 登记后超过这么多小时还没授权会被自动清理（服务端给的值） */
  expireHours: number
  error: string
  emptyText: string
  /** 正在删除的那一条（置灰它的按钮） */
  busyId: string
  /** 「现在」的时间戳，用来算「3 分钟前」；由父组件的计时器推进 */
  nowMs: number
}>()
const emit = defineEmits<{ (e: 'delete', c: OAuthClientRow): void; (e: 'refresh'): void }>()
</script>

<template>
  <section class="space-y-2 rounded-xs border border-border/60 p-3" data-testid="oauth-clients">
    <div class="flex flex-wrap items-start justify-between gap-2">
      <div class="min-w-0 flex-1">
        <h4 class="text-xs font-bold">
          刚登记、还没授权的客户端<span v-if="clients.length" class="ml-1 font-mono text-muted-foreground" data-testid="oauth-clients-count">({{ clients.length }})</span>
        </h4>
        <p class="mt-0.5 text-[10px] leading-relaxed text-muted-foreground">
          客户端自动登记之后，还要你在授权页点「批准」才算连上。这里是登记了、但还没走完授权的：不认识的就删除登记。
          超过 {{ expireHours }} 小时没人授权的登记会被自动清理；已经授权的应用在下面的「已授权的应用」里。
        </p>
      </div>
      <Button size="sm" variant="outline" class="h-7 shrink-0 text-[10px]" data-testid="oauth-clients-refresh" @click="emit('refresh')">刷新</Button>
    </div>

    <p v-if="error" class="text-xs text-destructive" data-testid="oauth-clients-error">{{ error }}</p>
    <ul v-else-if="clients.length" class="space-y-2">
      <li
        v-for="c in clients"
        :key="c.id"
        class="flex items-start justify-between gap-2 rounded-xs border border-border/60 p-2 text-xs"
        :data-testid="`oauth-client-${c.id}`"
      >
        <div class="min-w-0 space-y-0.5">
          <div class="flex flex-wrap items-center gap-1.5">
            <span class="break-all font-medium">{{ c.client_name || '（未命名客户端）' }}</span>
            <span
              v-for="b in clientBadges(c)"
              :key="b.id"
              class="inline-flex items-center rounded-xs border px-1.5 py-px text-[10px]"
              :class="toneClasses(b.tone)"
              :title="b.title"
              :data-badge="b.id"
            >
              {{ b.label }}
            </span>
          </div>
          <div class="break-all text-muted-foreground">{{ clientHostsText(c) }}</div>
          <div class="text-[10px] text-muted-foreground" :title="clientRegisteredTitle(c)">{{ clientRegisteredText(c, nowMs) }}</div>
        </div>
        <Button
          size="sm"
          variant="outline"
          class="h-7 shrink-0 text-[10px]"
          :disabled="busyId === c.id"
          :data-testid="`oauth-client-delete-${c.id}`"
          @click="emit('delete', c)"
        >
          删除登记
        </Button>
      </li>
    </ul>
    <p v-else class="text-xs text-muted-foreground" data-testid="oauth-clients-empty">{{ emptyText }}</p>
  </section>
</template>
