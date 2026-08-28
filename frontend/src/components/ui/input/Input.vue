<script setup lang="ts">
import { computed, useAttrs } from 'vue'
import { cn } from '@/lib/utils'

defineOptions({ inheritAttrs: false })

interface Props {
  modelValue?: string | number
  class?: string
  type?: string
}

const props = withDefaults(defineProps<Props>(), {
  type: 'text'
})
const emit = defineEmits<{
  (e: 'update:modelValue', v: string | number): void
}>()

const attrs = useAttrs()

const classes = computed(() =>
  cn(
    'flex h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm shadow-sm transition-colors file:border-0 file:bg-transparent file:text-sm file:font-medium placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-0 disabled:cursor-not-allowed disabled:opacity-50',
    props.class
  )
)

function onInput(e: Event) {
  const target = e.target as HTMLInputElement
  if (props.type === 'number') {
    emit('update:modelValue', target.value === '' ? '' : Number(target.value))
  } else {
    emit('update:modelValue', target.value)
  }
}
</script>

<template>
  <input
    :type="type"
    :class="classes"
    :value="modelValue"
    v-bind="attrs"
    @input="onInput"
  />
</template>
