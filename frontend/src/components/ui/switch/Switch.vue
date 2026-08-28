<script setup lang="ts">
import { computed } from 'vue'
import { SwitchRoot, SwitchThumb, type SwitchRootEmits, type SwitchRootProps, useForwardPropsEmits } from 'radix-vue'
import { cn } from '@/lib/utils'

interface Props extends SwitchRootProps {
  class?: string
}

const props = defineProps<Props>()
const emits = defineEmits<SwitchRootEmits>()

const delegated = computed(() => {
  const { class: _c, ...rest } = props
  return rest
})
const forwarded = useForwardPropsEmits(delegated, emits)

const rootClass = computed(() =>
  cn(
    'peer inline-flex h-5 w-9 shrink-0 cursor-pointer items-center rounded-full border-2 border-transparent transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background disabled:cursor-not-allowed disabled:opacity-50 data-[state=checked]:bg-primary data-[state=unchecked]:bg-input',
    props.class
  )
)
</script>

<template>
  <SwitchRoot v-bind="forwarded" :class="rootClass">
    <SwitchThumb
      class="pointer-events-none block h-4 w-4 rounded-full bg-background shadow-lg ring-0 transition-transform data-[state=checked]:translate-x-4 data-[state=unchecked]:translate-x-0"
    />
  </SwitchRoot>
</template>
