<script setup lang="ts">
import { Check } from 'lucide-vue-next'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuTrigger } from '../ui/dropdown-menu'
import { IDLE_OPTIONS, type IdleChoice } from '../../lib/terminal/lifecycle'

// 生命周期选择菜单：空闲不回收，或空闲多久后回收；会话上还可以选「沿用主机默认」。
withDefaults(
  defineProps<{
    modelValue: IdleChoice
    title: string
    /** 提供时多出一项「沿用主机默认」（会话用；主机默认本身没有这一项）。 */
    inheritLabel?: string
    disabled?: boolean
  }>(),
  { inheritLabel: '', disabled: false },
)
const emit = defineEmits<{ (e: 'update:modelValue', v: IdleChoice): void }>()
</script>

<template>
  <DropdownMenu>
    <DropdownMenuTrigger as-child :disabled="disabled">
      <slot name="trigger" />
    </DropdownMenuTrigger>
    <DropdownMenuContent align="end" class="text-xs min-w-[15rem]">
      <DropdownMenuLabel class="text-[11px] font-normal text-muted-foreground leading-snug">{{ title }}</DropdownMenuLabel>
      <DropdownMenuItem v-if="inheritLabel" class="text-xs" @select="emit('update:modelValue', 'inherit')">
        <Check v-if="modelValue === 'inherit'" class="!size-3.5" /><span v-else class="!size-3.5" aria-hidden="true" />{{ inheritLabel }}
      </DropdownMenuItem>
      <DropdownMenuItem v-for="o in IDLE_OPTIONS" :key="o.value" class="text-xs" @select="emit('update:modelValue', o.value)">
        <Check v-if="modelValue === o.value" class="!size-3.5" /><span v-else class="!size-3.5" aria-hidden="true" />{{ o.label }}
      </DropdownMenuItem>
    </DropdownMenuContent>
  </DropdownMenu>
</template>
