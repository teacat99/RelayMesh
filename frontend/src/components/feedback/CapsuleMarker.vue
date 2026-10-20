<script setup lang="ts">
import { computed } from 'vue'
import { createReusableTemplate } from '@vueuse/core'
import { Archive, ChevronDown, ChevronRight } from 'lucide-vue-next'
import {
  capsuleBasisLabel,
  capsuleSections,
  gitLogSinceCommand,
  type CheckpointView
} from '../../lib/anchor'
import { EVENT_BOX } from '../../lib/eventRow'
import { CARD_TONE_CLASS, CARD_TYPES, cardTimeText } from '../../lib/messageCard'
import { capsuleRefToken } from '../../lib/replyRef'
import ReplyRefButton from './ReplyRefButton.vue'
import EventRowHead from './EventRowHead.vue'

// 恢复胶囊的时间线标记（只读）：落在对应两次交付之间，显示修订号与基准哈希；
// 点击展开目标 / 决策 / 下一步 / 阻塞，并可跳转到它所基于的那次交付。
//
// compact（#120 预览视图，默认关）：换成 32px 的事件行（阶段 C2：[恢复] 胶囊 r12 …… 基于 xxx · 时间 ˅），展开的明细放在同一个盒子里、
// 那一行的下面。明细的内容两种版式共用同一份（DefineDetail / ReuseDetail，不多出任何 DOM 节点）。经典列表不传它，经典的 DOM 一个字没动。
const props = defineProps<{
  capsule: CheckpointView
  expanded: boolean
  timeText: string
  /** 胶囊基于的那次交付在当前时间线上的轮次号；找不到（例如已被删除）时为 null。 */
  anchorTurn: number | null
  compact?: boolean
}>()

const emit = defineEmits<{
  (e: 'toggle'): void
  (e: 'jump'): void
  (e: 'copy', text: string, label: string): void
}>()

const basisLabel = computed(() => capsuleBasisLabel(props.capsule))
const sections = computed(() => capsuleSections(props.capsule))
const logCommand = computed(() => gitLogSinceCommand(props.capsule.basis?.git_commit))
const TONE = CARD_TONE_CLASS[CARD_TYPES.capsule.tone]

const [DefineDetail, ReuseDetail] = createReusableTemplate()
</script>

<template>
  <div data-testid="capsule-marker" :data-revision="capsule.revision">
    <!-- 展开的明细内容：经典的圆角框与事件行的盒子里各用一次（Define 只留一个注释节点，不是元素，经典的元素结构不变） -->
    <DefineDetail>
      <p v-if="capsule.current_stage" class="text-[10px] font-mono text-muted-foreground">
        阶段 {{ capsule.current_stage.id }} · {{ capsule.current_stage.status }}
      </p>
      <p v-if="sections.length === 0" class="text-muted-foreground font-sans">这份胶囊没有可展示的内容。</p>
      <div v-for="section in sections" :key="section.key">
        <div class="text-[10px] font-mono text-muted-foreground mb-0.5">{{ section.title }}</div>
        <ul class="list-disc pl-4 space-y-0.5">
          <li v-for="(item, idx) in section.items" :key="idx" class="text-foreground/90 whitespace-pre-wrap break-words">{{ item }}</li>
        </ul>
      </div>
      <div class="flex items-center gap-2 flex-wrap pt-1">
        <!-- 引用这份胶囊：在输入框插入「【恢复 r8】」（#106） -->
        <ReplyRefButton :token="capsuleRefToken(capsule.revision)" label="引用此胶囊" />
        <button
          v-if="anchorTurn"
          type="button"
          class="text-[10px] font-mono px-1.5 py-0.5 rounded border border-border/60 text-muted-foreground hover:text-foreground hover:bg-muted cursor-pointer transition-colors"
          data-testid="capsule-jump"
          @click="emit('jump')"
        >跳转到其基于的交付（第 {{ anchorTurn }} 轮）</button>
        <button
          v-if="logCommand"
          type="button"
          class="text-[10px] font-mono px-1.5 py-0.5 rounded border border-border/60 text-muted-foreground hover:text-foreground hover:bg-muted cursor-pointer transition-colors"
          @click="emit('copy', logCommand, 'git log 命令')"
        >复制 git log（至今）</button>
      </div>
    </DefineDetail>

    <!-- 事件行（预览视图，compact） -->
    <div v-if="compact" :class="[EVENT_BOX, TONE]">
      <EventRowHead
        type="capsule"
        :title="`胶囊 r${capsule.revision}`"
        :preview="basisLabel"
        :time="cardTimeText(capsule.created_at)"
        :time-title="timeText"
        interactive
        :expanded="expanded"
        :open-title="expanded ? '收起恢复胶囊' : '展开恢复胶囊：目标 / 决策 / 下一步 / 阻塞'"
        main-testid="capsule-toggle"
        @click="emit('toggle')"
      />
      <div v-if="expanded" class="space-y-2 border-t border-border/50 px-3 py-2" data-testid="capsule-details">
        <ReuseDetail />
      </div>
    </div>

    <template v-else>
      <button
        type="button"
        class="w-full flex items-center gap-2 select-none group cursor-pointer"
        :aria-expanded="expanded"
        @click="emit('toggle')"
      >
        <div class="h-px flex-1 bg-amber-500/30"></div>
        <div class="flex items-center gap-1.5 px-2 py-0.5 rounded-full border border-amber-500/30 bg-amber-500/5 group-hover:bg-amber-500/10 transition-colors text-[10px] sm:text-[11px] font-mono text-amber-600 dark:text-amber-400">
          <Archive class="w-3 h-3 shrink-0" />
          <span class="font-medium">恢复胶囊 r{{ capsule.revision }}</span>
          <span v-if="basisLabel">· {{ basisLabel }}</span>
          <span class="text-muted-foreground">· {{ timeText }}</span>
          <component :is="expanded ? ChevronDown : ChevronRight" class="w-3 h-3 shrink-0" />
        </div>
        <div class="h-px flex-1 bg-amber-500/30"></div>
      </button>

      <div
        v-if="expanded"
        class="mt-2 mx-auto max-w-3xl rounded-md border border-amber-500/30 bg-amber-500/5 p-3 text-xs space-y-2"
        data-testid="capsule-details"
      >
        <ReuseDetail />
      </div>
    </template>
  </div>
</template>
