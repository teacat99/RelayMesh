<script setup lang="ts">
import { computed } from 'vue'
import { cn } from '@/lib/utils'
import { alertVariants, type AlertVariants } from './variants'

interface Props {
  variant?: AlertVariants['variant']
  class?: string
}

const props = withDefaults(defineProps<Props>(), { variant: 'default' })

const classes = computed(() => cn(alertVariants({ variant: props.variant }), props.class))
</script>

<template>
  <div role="alert" :class="classes"><slot /></div>
</template>
