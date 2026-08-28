<script setup lang="ts">
import { computed } from 'vue'
import { DialogRoot, type DialogRootEmits, type DialogRootProps, useForwardPropsEmits } from 'radix-vue'
import { useDialogBackGesture } from '@/composables/useDialogBackGesture'

const props = defineProps<DialogRootProps>()
const emits = defineEmits<DialogRootEmits>()
const forwarded = useForwardPropsEmits(props, emits)

// Mirror the controlled `open` prop into a writable ref so the back-
// gesture composable can both observe transitions AND request
// programmatic close (it writes false into the same channel that
// v-model:open reads). When the dialog is uncontrolled (no `open`
// prop) the prop stays undefined and the composable becomes a no-op.
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
