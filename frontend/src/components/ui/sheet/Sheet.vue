<script setup lang="ts">
import { computed } from 'vue'
import { DialogRoot, type DialogRootEmits, type DialogRootProps, useForwardPropsEmits } from 'radix-vue'
import { useDialogBackGesture } from '@/composables/useDialogBackGesture'

const props = defineProps<DialogRootProps>()
const emits = defineEmits<DialogRootEmits>()
const forwarded = useForwardPropsEmits(props, emits)

// Sheets are visually drawers but share radix's DialogRoot primitive,
// so the same back-gesture treatment applies: opening a sheet pushes
// a history sentinel, and the system back gesture closes the sheet
// instead of leaving the page.
const openRef = computed({
  get: () => props.open === true,
  set: (v: boolean) => emits('update:open', v)
})
useDialogBackGesture(openRef)
</script>

<template>
  <DialogRoot v-bind="forwarded">
    <slot />
  </DialogRoot>
</template>
