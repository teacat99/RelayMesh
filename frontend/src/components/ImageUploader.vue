<script setup lang="ts">
import { X, Image as ImageIcon } from 'lucide-vue-next'
import type { SessionImage } from '../api/types'

const props = defineProps<{
  modelValue: SessionImage[]
}>()

const emit = defineEmits<{
  (e: 'update:modelValue', value: SessionImage[]): void
}>()

function removeImage(index: number) {
  const updated = [...props.modelValue]
  updated.splice(index, 1)
  emit('update:modelValue', updated)
}
</script>

<template>
  <div v-if="modelValue.length > 0" class="flex flex-wrap gap-2 pt-1 pb-1">
    <div
      v-for="(img, idx) in modelValue"
      :key="idx"
      class="relative group rounded-sm overflow-hidden border border-border w-16 h-16 bg-muted flex items-center justify-center shadow-2xs"
    >
      <img
        :src="`data:image/${img.format || 'png'};base64,${img.data}`"
        :alt="img.name || 'preview'"
        class="w-full h-full object-cover"
      />
      <button
        type="button"
        class="absolute top-1 right-1 bg-black/70 hover:bg-black/90 text-white rounded-full p-0.5 opacity-0 group-hover:opacity-100 transition-opacity"
        @click.stop="removeImage(idx)"
        title="移除此图片"
      >
        <X class="w-3 h-3" />
      </button>
      <div class="absolute bottom-0 inset-x-0 bg-black/60 text-white text-[9px] font-mono px-1 truncate text-center">
        {{ img.name || `img-${idx + 1}` }}
      </div>
    </div>
  </div>
</template>
