<script setup lang="ts">
import { Toaster, type ToasterProps } from 'vue-sonner'
import { useThemeStore } from '@/stores/theme'
import { useAppMode } from '@/composables/useAppMode'
import { computed } from 'vue'

const props = withDefaults(
  defineProps<ToasterProps>(),
  {
    position: 'bottom-left',
    expand: true,
    visibleToasts: 4,
    closeButton: true,
    offset: 8
  }
)

const theme = useThemeStore()
const resolvedTheme = computed<ToasterProps['theme']>(() =>
  theme.isDark ? 'dark' : 'light'
)

// 消息提示的位置随项目而变：
//   RelayMesh：贴着输入区上方、与悬浮操作栏左对齐（位置由 globals.css 按输入区高度动态算出）；
//   ExecMesh：没有输入区，终端占满右侧，消息贴在终端区域的底边、靠右——不压住左侧栏底部的状态与按钮，
//   也不盖住通常靠左的命令行；动画方向随之改成从底边升起、沉回底边（见 globals.css 的 .toaster-exec）。
const mode = useAppMode()
const isExec = computed(() => mode.value === 'exec')
const position = computed<ToasterProps['position']>(() => (isExec.value ? 'bottom-right' : props.position))
</script>

<template>
  <Toaster
    v-bind="props"
    :position="position"
    :class="isExec ? 'toaster-exec' : ''"
    :theme="resolvedTheme"
    :toast-options="{
      classes: {
        toast: '!bg-card/95 !backdrop-blur-xs !border !border-border/80 !shadow-2xs !rounded-sm !text-foreground !font-sans !text-[11px] sm:!text-xs !min-h-0 !gap-0 !w-auto !min-w-[120px] !max-w-fit !flex-row !items-center !p-0',
        title: '!text-[11px] sm:!text-xs !font-medium !text-foreground !font-mono',
        description: '!text-[10px] !text-muted-foreground !leading-snug',
        actionButton: '!static !shrink-0 !bg-transparent hover:!bg-muted !border-0 !border-l !border-border/60 !text-muted-foreground hover:!text-foreground !rounded-none !w-7 !h-full !flex !items-center !justify-center !cursor-pointer !p-0 !text-xs !font-medium',
        cancelButton: '!static !shrink-0 !bg-transparent hover:!bg-muted !border-0 !border-l !border-border/60 !text-muted-foreground hover:!text-foreground !rounded-none !w-7 !h-full !flex !items-center !justify-center !cursor-pointer !p-0 !text-xs !font-medium',
        closeButton: '!static !shrink-0 !ml-auto !order-last !bg-transparent hover:!bg-muted !border-0 !border-l !border-border/60 !text-muted-foreground/60 hover:!text-foreground !rounded-none !rounded-r-sm !w-7 !h-full !flex !items-center !justify-center !cursor-pointer !transform-none !top-auto !right-auto !left-auto !opacity-100 !visible',
        success: '!border-emerald-500/30 [&_[data-icon]]:!text-emerald-500 [&_[data-button]]:!text-emerald-500 hover:[&_[data-button]]:!text-emerald-400',
        error: '!border-destructive/30 [&_[data-icon]]:!text-destructive [&_[data-button]]:!text-destructive hover:[&_[data-button]]:!text-destructive/80',
        warning: '!border-amber-500/30 [&_[data-icon]]:!text-amber-500 [&_[data-button]]:!text-amber-500 hover:[&_[data-button]]:!text-amber-400',
        info: '!border-primary/30 [&_[data-icon]]:!text-primary [&_[data-button]]:!text-primary hover:[&_[data-button]]:!text-primary/80',
      }
    }"
  />
</template>
