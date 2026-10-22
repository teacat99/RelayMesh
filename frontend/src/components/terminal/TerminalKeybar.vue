<script setup lang="ts">
import { ArrowDown, ArrowLeft, ArrowRight, ArrowUp, ClipboardPaste } from 'lucide-vue-next'
import { ESC, PAGE_DOWN, PAGE_UP, TAB, type CursorKey } from '../../lib/terminal/keys'

// 手机 / 平板上的软键盘辅助条：软键盘上没有的 Esc、Tab、Ctrl、方向键等。
// 按钮用 pointerdown.prevent 避免抢走终端的焦点（否则每按一下软键盘就收起一次）。
defineProps<{ ctrl: boolean; alt: boolean }>()

const emit = defineEmits<{
  (e: 'text', seq: string): void
  (e: 'cursor', key: CursorKey): void
  (e: 'toggle', modifier: 'ctrl' | 'alt'): void
  (e: 'paste'): void
}>()

const symbols = ['|', '~', '/', '-', '.', ':']
const btn =
  'shrink-0 h-9 min-w-9 px-2.5 rounded-sm border border-border/70 bg-background text-xs font-mono text-foreground active:bg-accent transition-colors flex items-center justify-center select-none cursor-pointer'
const btnOn = 'bg-primary text-primary-foreground border-primary active:bg-primary/90'
</script>

<template>
  <div
    class="shrink-0 border-t border-border/70 bg-card px-2 py-1.5 flex items-center gap-1.5 overflow-x-auto"
    role="toolbar"
    aria-label="终端辅助按键"
  >
    <button type="button" :class="btn" @pointerdown.prevent @click="emit('text', ESC)">Esc</button>
    <button type="button" :class="btn" @pointerdown.prevent @click="emit('text', TAB)">Tab</button>
    <button type="button" :class="[btn, ctrl && btnOn]" :aria-pressed="ctrl" @pointerdown.prevent @click="emit('toggle', 'ctrl')">Ctrl</button>
    <button type="button" :class="[btn, alt && btnOn]" :aria-pressed="alt" @pointerdown.prevent @click="emit('toggle', 'alt')">Alt</button>
    <span class="w-px h-5 bg-border/70 shrink-0" aria-hidden="true" />
    <button type="button" :class="btn" aria-label="左" @pointerdown.prevent @click="emit('cursor', 'left')"><ArrowLeft class="w-4 h-4" /></button>
    <button type="button" :class="btn" aria-label="下" @pointerdown.prevent @click="emit('cursor', 'down')"><ArrowDown class="w-4 h-4" /></button>
    <button type="button" :class="btn" aria-label="上" @pointerdown.prevent @click="emit('cursor', 'up')"><ArrowUp class="w-4 h-4" /></button>
    <button type="button" :class="btn" aria-label="右" @pointerdown.prevent @click="emit('cursor', 'right')"><ArrowRight class="w-4 h-4" /></button>
    <button type="button" :class="btn" @pointerdown.prevent @click="emit('cursor', 'home')">Home</button>
    <button type="button" :class="btn" @pointerdown.prevent @click="emit('cursor', 'end')">End</button>
    <button type="button" :class="btn" @pointerdown.prevent @click="emit('text', PAGE_UP)">PgUp</button>
    <button type="button" :class="btn" @pointerdown.prevent @click="emit('text', PAGE_DOWN)">PgDn</button>
    <span class="w-px h-5 bg-border/70 shrink-0" aria-hidden="true" />
    <button v-for="s in symbols" :key="s" type="button" :class="btn" @pointerdown.prevent @click="emit('text', s)">{{ s }}</button>
    <span class="w-px h-5 bg-border/70 shrink-0" aria-hidden="true" />
    <button type="button" :class="[btn, 'gap-1']" @pointerdown.prevent @click="emit('paste')">
      <ClipboardPaste class="w-4 h-4" />
      <span>粘贴</span>
    </button>
  </div>
</template>
