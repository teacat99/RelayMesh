<script setup lang="ts">
import { SelectValue, type SelectValueProps } from 'radix-vue'

const props = defineProps<SelectValueProps & { class?: string, placeholder?: string }>()
</script>
<template>
  <SelectValue :placeholder="placeholder" :class="props.class">
    <slot />
  </SelectValue>
</template>
