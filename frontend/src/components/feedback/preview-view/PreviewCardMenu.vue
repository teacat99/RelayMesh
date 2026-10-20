<script setup lang="ts">
// 消息卡右上角的「⋯」更多菜单（#120 · D-273，只给预览视图用）：
// 把复制、删除这类次要动作收进一个按钮，卡片头部才有地方留给标题。每一项是 { key, label, onSelect, destructive?, keepOpen? }，由具体的卡片提供。
// destructive 把文字画成破坏色；keepOpen 让选中后菜单不关（「删除」两段式确认的第一击：菜单留着，这一项换成「再点一次确认」）。
//
// 惰性挂载（#158）：一个 150 轮的长会话里有一千多张卡，每张卡都挂一整套 radix 下拉菜单（根 / 触发器 / 内容各自带着响应式状态与监听器）
// 要多占几十 MB 堆与三千多个监听器，而绝大多数卡的菜单一辈子也不会被点开。所以没被碰过的卡只画一个朴素按钮，
// 真正的下拉菜单在用户「靠近」它时才挂载（armed）；打开与否由本组件的 open 状态受控（v-model:open），所以哪条路径挂载的都能同样地打开：
//   · 鼠标 / 触控笔悬停（pointerenter）：提前挂好，等用户点击时触发器已经就位，之后与以前完全一样（radix 的触发器在 click 时切换）；
//   · 点击（click，左键、没按 ctrl，与 radix 触发器的判断一致）：触屏轻点、键盘 Enter / 空格、脚本触发的 click 走这一条——挂载并打开；
//   · ArrowDown：与 radix 触发器一致，挂载并打开；
//   · 兜底：鼠标没有先悬停就按下（合成点击、光标没动、页面在光标下滚动之类）时，pointerenter 紧挨着 pointerdown 来，
//     挂载会把按钮换成新节点，随后的 click 会因为「按下的节点已被摘掉」而不再派发——所以鼠标在 pointerdown 就打开（触屏不这样：滚动手势也会从按钮上起手）。
// 不在 focus 上预挂载：挂载会换掉按钮的 DOM 节点，键盘焦点会因此丢失；按键触发时才换，换完把焦点还给新按钮。
import { nextTick, ref } from 'vue'
import { MoreHorizontal } from 'lucide-vue-next'
import type { CardMenuItem } from '../../../lib/messageCard'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from '../../ui/dropdown-menu'

defineProps<{ items: CardMenuItem[] }>()

const BTN_CLASS =
  '-mr-1 inline-flex size-6 shrink-0 cursor-pointer items-center justify-center rounded-sm text-muted-foreground outline-none transition-colors hover:bg-muted hover:text-foreground focus-visible:ring-1 focus-visible:ring-ring'

/** 下拉菜单是否已挂载；只会由 false 变 true，一张卡挂过一次就一直挂着。 */
const armed = ref(false)
/** 菜单是否打开（受控）：挂载之前的「打开」请求也记在这里，挂载完成时直接是打开的。 */
const open = ref(false)
const plainBtn = ref<HTMLButtonElement | null>(null)
const realBtn = ref<HTMLButtonElement | null>(null)

function arm(thenOpen: boolean) {
  if (!armed.value) {
    // 朴素按钮正拿着键盘焦点时，换成真触发器后把焦点还给它（换节点会丢焦点）
    const hadFocus = !!plainBtn.value && document.activeElement === plainBtn.value
    armed.value = true
    if (hadFocus) void nextTick(() => realBtn.value?.focus())
  }
  if (thenOpen) open.value = true
}

function onPointerEnter(ev: PointerEvent) {
  // 触屏没有「悬停」，pointerenter 紧挨着 pointerdown 来，预挂载没有意义；触屏一律走 click
  if (ev.pointerType === 'touch') return
  arm(false)
}

function onPointerDown(ev: PointerEvent) {
  // 只处理上面说的兜底：鼠标 / 触控笔、左键、没按 ctrl（macOS 的 ctrl+点击是右键）
  if (ev.pointerType === 'touch' || ev.button !== 0 || ev.ctrlKey) return
  arm(true)
}

function onClick(ev: MouseEvent) {
  // 与 radix 触发器一致：左键且没按 ctrl（键盘 Enter / 空格与脚本触发的 click，button 是 0、ctrlKey 是 false）
  if (ev.button === 0 && !ev.ctrlKey) arm(true)
}

function onSelect(item: CardMenuItem, ev: Event) {
  if (item.keepOpen) ev.preventDefault()
  item.onSelect()
}
</script>

<template>
  <template v-if="items.length > 0">
    <DropdownMenu v-if="armed" v-model:open="open">
      <DropdownMenuTrigger as-child>
        <button ref="realBtn" type="button" :class="BTN_CLASS" title="更多" aria-label="更多" data-testid="card-menu">
          <MoreHorizontal class="size-3.5" />
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" class="min-w-[8rem] text-xs" data-testid="card-menu-content">
        <DropdownMenuItem
          v-for="item in items"
          :key="item.key"
          class="text-xs"
          :class="item.destructive ? 'text-destructive focus:bg-destructive/10 focus:text-destructive' : ''"
          :data-testid="`card-menu-${item.key}`"
          @select="(ev: Event) => onSelect(item, ev)"
        >
          {{ item.label }}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
    <button
      v-else
      ref="plainBtn"
      type="button"
      :class="BTN_CLASS"
      title="更多"
      aria-label="更多"
      aria-haspopup="menu"
      aria-expanded="false"
      data-testid="card-menu"
      @pointerenter="onPointerEnter"
      @pointerdown="onPointerDown"
      @click="onClick"
      @keydown.down.prevent="arm(true)"
    >
      <MoreHorizontal class="size-3.5" />
    </button>
  </template>
</template>
