<script setup lang="ts">
import { computed } from 'vue'
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
// 渲染成 32px 的事件行（#120 预览视图，阶段 C2：[恢复] 胶囊 r12 …… 基于 xxx · 时间 ˅），展开的明细放在同一个盒子里、那一行的下面。
const props = defineProps<{
  capsule: CheckpointView
  expanded: boolean
  timeText: string
  /** 胶囊基于的那次交付在当前时间线上的轮次号；找不到（例如已被删除）时为 null。 */
  anchorTurn: number | null
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
</script>

<template>
  <div data-testid="capsule-marker" :data-revision="capsule.revision">
    <!-- 事件行 -->
    <div :class="[EVENT_BOX, TONE]">
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
      </div>
    </div>
  </div>
</template>
