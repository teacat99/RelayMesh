<script setup lang="ts">
import { ref, computed, watch, onMounted, onUnmounted, nextTick } from 'vue'
import { phasesApi, type PhaseItem } from '../../api/client'
import { useSessionStore } from '../../stores/session'
import PhaseEditDialog from './PhaseEditDialog.vue'

const props = defineProps<{
  workflowId: string
}>()

const sessionStore = useSessionStore()
const currentPhaseId = ref('')
const phases = ref<PhaseItem[]>([])
const defaultPrompts = ref<Record<string, string>>({})
const loading = ref(false)
const containerRef = ref<HTMLElement | null>(null)
const indicatorStyle = ref({ left: '0px', width: '0px' })

let resizeObserver: ResizeObserver | null = null

const editDialogOpen = ref(false)
const editingPhase = ref<PhaseItem | null>(null)
const editingIndex = ref(0)

async function fetchPhase() {
  if (!props.workflowId) return
  loading.value = true
  try {
    const data = await phasesApi.get(props.workflowId)
    currentPhaseId.value = data.current_phase_id || ''
    phases.value = data.phases || []
    defaultPrompts.value = data.default_prompts || {}
    await nextTick()
    updateIndicator()
  } catch { /* noop */ }
  loading.value = false
}

async function selectPhase(phaseId: string) {
  if (phaseId === currentPhaseId.value || !props.workflowId) return
  const prev = currentPhaseId.value
  currentPhaseId.value = phaseId
  await nextTick()
  updateIndicator()
  try {
    await phasesApi.set(props.workflowId, { phase_id: phaseId, source: 'human' })
  } catch {
    currentPhaseId.value = prev
    await nextTick()
    updateIndicator()
  }
}

function openEditDialog(phase: PhaseItem, idx: number) {
  editingPhase.value = phase
  editingIndex.value = idx
  editDialogOpen.value = true
}

async function handlePhaseSave(updated: PhaseItem) {
  const items = [...phases.value]
  const idx = items.findIndex(p => p.id === updated.id)
  if (idx >= 0) {
    items[idx] = updated
    phases.value = items
    try {
      await phasesApi.set(props.workflowId, { phases: items })
    } catch { /* noop */ }
  }
}

async function handlePhaseDelete(id: string) {
  const items = phases.value.filter(p => p.id !== id)
  phases.value = items
  try {
    await phasesApi.set(props.workflowId, { phases: items })
  } catch { /* noop */ }
  await nextTick()
  updateIndicator()
}

function updateIndicator() {
  if (!containerRef.value) return
  const buttons = containerRef.value.querySelectorAll('[data-phase-btn]')
  const idx = phases.value.findIndex(p => p.id === currentPhaseId.value)
  if (idx < 0 || idx >= buttons.length) return
  const btn = buttons[idx] as HTMLElement
  indicatorStyle.value = {
    left: `${btn.offsetLeft}px`,
    width: `${btn.offsetWidth}px`,
  }
}

const currentIdx = computed(() => {
  const idx = phases.value.findIndex(p => p.id === currentPhaseId.value)
  return idx >= 0 ? idx : 0
})

watch(() => props.workflowId, fetchPhase)

onMounted(() => {
  fetchPhase()
  resizeObserver = new ResizeObserver(() => updateIndicator())
  if (containerRef.value) resizeObserver.observe(containerRef.value)
})

onUnmounted(() => {
  resizeObserver?.disconnect()
  resizeObserver = null
})

watch(containerRef, (el) => {
  resizeObserver?.disconnect()
  if (el) resizeObserver?.observe(el)
})

watch(() => sessionStore.lastPhaseEvent, async (evt) => {
  if (evt && evt.workflow_id === props.workflowId && evt.phase) {
    currentPhaseId.value = evt.phase
    await nextTick()
    updateIndicator()
  }
})
</script>

<template>
  <div
    v-if="phases.length > 0"
    ref="containerRef"
    class="relative flex items-center rounded-sm border border-border/80 shadow-2xs min-w-0 whitespace-nowrap text-[11px] sm:text-xs overflow-x-auto no-scrollbar scrollbar-none bg-card/95 backdrop-blur-xs select-none"
    title="工作流阶段（点击切换，右键编辑）"
  >
    <!-- Sliding indicator -->
    <div
      class="absolute top-0 bottom-0 bg-primary rounded-sm transition-all duration-300 ease-out z-0"
      :style="{ left: indicatorStyle.left, width: indicatorStyle.width }"
    ></div>
    <button
      v-for="(phase, idx) in phases"
      :key="phase.id"
      data-phase-btn
      type="button"
      class="relative z-10 px-2 sm:px-2.5 py-1 transition-colors duration-200 cursor-pointer flex items-center gap-1"
      :class="phase.id === currentPhaseId
        ? 'text-primary-foreground font-medium'
        : idx < currentIdx
          ? 'text-muted-foreground hover:text-foreground'
          : 'text-muted-foreground/60 hover:text-muted-foreground'"
      :title="phase.description || phase.label"
      @click="selectPhase(phase.id)"
      @contextmenu.prevent="openEditDialog(phase, idx)"
    >
      <span>{{ phase.label }}</span>
    </button>
  </div>

  <PhaseEditDialog
    v-model:open="editDialogOpen"
    :phase="editingPhase"
    :phase-index="editingIndex"
    :total-phases="phases.length"
    :default-prompt="editingPhase ? (defaultPrompts[editingPhase.id] || '') : ''"
    @save="handlePhaseSave"
    @delete="handlePhaseDelete"
  />
</template>
