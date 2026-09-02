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
        toast: '!bg-card/95 !backdrop-blur-xs !border !border-border/80 !shadow-2xs !rounded-sm !text-foreground !font-sans !text-[11px] sm:!text-xs !min-h-0 !gap-1.5 !w-auto !min-w-[108px] !max-w-fit !whitespace-nowrap !flex-row !items-center !p-0 !pl-2 sm:!pl-2.5',
        title: '!text-[11px] sm:!text-xs !font-medium !text-foreground !font-mono !whitespace-nowrap',
        description: '!text-[10px] !text-muted-foreground !leading-tight !whitespace-nowrap',
        actionButton: '!bg-primary !text-primary-foreground !text-[10px] !font-mono !rounded-2xs !px-2 !py-0.5',
        cancelButton: '!bg-muted !text-muted-foreground !text-[10px] !font-mono !rounded-2xs !px-2 !py-0.5',
        closeButton: '!static !shrink-0 !ml-auto !order-last !bg-transparent hover:!bg-muted !border-0 !border-l !border-border/60 !text-muted-foreground/60 hover:!text-foreground !rounded-none !rounded-r-sm !w-6 !h-full !flex !items-center !justify-center !cursor-pointer !transform-none !top-auto !right-auto !left-auto',
        success: '!border-emerald-500/30 [&_[data-icon]]:!text-emerald-500',
        error: '!border-destructive/30 [&_[data-icon]]:!text-destructive',
        warning: '!border-amber-500/30 [&_[data-icon]]:!text-amber-500',
        info: '!border-primary/30 [&_[data-icon]]:!text-primary',
      }
    }"
  />
</template>
