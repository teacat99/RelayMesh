<script setup lang="ts">
import { computed } from 'vue'
import { Primitive, type PrimitiveProps } from 'radix-vue'
import { cn } from '@/lib/utils'
import { buttonVariants, type ButtonVariants } from './variants'

interface Props extends PrimitiveProps {
  variant?: ButtonVariants['variant']
  size?: ButtonVariants['size']
  class?: string
  disabled?: boolean
  type?: 'button' | 'submit' | 'reset'
}

const props = withDefaults(defineProps<Props>(), {
  as: 'button',
  variant: 'default',
  size: 'default',
  type: 'button'
})

const classes = computed(() =>
  cn(buttonVariants({ variant: props.variant, size: props.size }), props.class)
)
</script>

<template>
  <Primitive
    :as="as"
    :as-child="asChild"
    :class="classes"
    :type="type"
    :disabled="disabled"
  >
    <slot />
  </Primitive>
</template>
