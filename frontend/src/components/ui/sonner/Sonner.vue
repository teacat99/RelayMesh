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
        toast: '!bg-card/95 !backdrop-blur-md !border !border-border/90 !shadow-md !rounded-xs !px-2.5 !py-1.5 !text-foreground !font-sans !text-[11px] !min-h-0 !gap-2 !w-auto !min-w-[108px] !max-w-fit !whitespace-nowrap !flex-row !items-center !pr-6',
        title: '!text-[11px] !font-bold !text-foreground !font-mono !whitespace-nowrap',
        description: '!text-[10px] !text-muted-foreground !leading-tight !whitespace-nowrap',
        actionButton: '!bg-primary !text-primary-foreground !text-[10px] !font-mono !rounded-2xs !px-2 !py-0.5',
        cancelButton: '!bg-muted !text-muted-foreground !text-[10px] !font-mono !rounded-2xs !px-2 !py-0.5',
        closeButton: '!bg-background/80 hover:!bg-muted !border !border-border/80 !text-muted-foreground hover:!text-foreground !rounded-2xs !w-3.5 !h-3.5 !flex !items-center !justify-center !cursor-pointer !top-1.5 !right-1.5',
        success: '!border-emerald-500/40 !bg-card/95 [&_[data-icon]]:!text-emerald-500',
        error: '!border-destructive/40 !bg-card/95 [&_[data-icon]]:!text-destructive',
        warning: '!border-amber-500/40 !bg-card/95 [&_[data-icon]]:!text-amber-500',
        info: '!border-primary/40 !bg-card/95 [&_[data-icon]]:!text-primary',
      }
    }"
  />
</template>
