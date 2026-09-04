<script setup lang="ts">
import { Toaster, type ToasterProps } from 'vue-sonner'
import { useThemeStore } from '@/stores/theme'
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
</script>

<template>
  <Toaster
    v-bind="props"
    :theme="resolvedTheme"
    :toast-options="{
      classes: {
        toast: '!bg-card/95 !backdrop-blur-xs !border !border-border/80 !shadow-2xs !rounded-sm !text-foreground !font-sans !text-[11px] sm:!text-xs !min-h-0 !gap-0 !w-auto !min-w-[120px] !max-w-fit !flex-row !items-center !p-0',
        title: '!text-[11px] sm:!text-xs !font-medium !text-foreground !font-mono',
        description: '!text-[10px] !text-muted-foreground !leading-snug',
        actionButton: '!static !shrink-0 !bg-transparent hover:!bg-muted !border-0 !border-l !border-border/60 !text-muted-foreground hover:!text-foreground !rounded-none !w-7 !h-full !flex !items-center !justify-center !cursor-pointer !p-0 !text-xs !font-medium',
        cancelButton: '!static !shrink-0 !bg-transparent hover:!bg-muted !border-0 !border-l !border-border/60 !text-muted-foreground hover:!text-foreground !rounded-none !w-7 !h-full !flex !items-center !justify-center !cursor-pointer !p-0 !text-xs !font-medium',
        closeButton: '!static !shrink-0 !ml-auto !order-last !bg-transparent hover:!bg-muted !border-0 !border-l !border-border/60 !text-muted-foreground/60 hover:!text-foreground !rounded-none !rounded-r-sm !w-7 !h-full !flex !items-center !justify-center !cursor-pointer !transform-none !top-auto !right-auto !left-auto !opacity-100 !visible',
        success: '!border-emerald-500/30 [&_[data-icon]]:!text-emerald-500',
        error: '!border-destructive/30 [&_[data-icon]]:!text-destructive',
        warning: '!border-amber-500/30 [&_[data-icon]]:!text-amber-500',
        info: '!border-primary/30 [&_[data-icon]]:!text-primary',
      }
    }"
  />
</template>
