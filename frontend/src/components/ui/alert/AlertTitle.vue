<script setup lang="ts">
import { computed } from 'vue'
import { cn } from '@/lib/utils'
interface Props { class?: string }
const props = defineProps<Props>()
const classes = computed(() => cn('mb-1 font-medium leading-none tracking-tight', props.class))
</script>
<template><h5 :class="classes"><slot /></h5></template>
