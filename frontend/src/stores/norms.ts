import { defineStore } from 'pinia'
import { ref } from 'vue'
import { normsApi, type UserNorm } from '../api/client'

export const useNormsStore = defineStore('norms', () => {
  const norms = ref<UserNorm[]>([])
  const isLoading = ref(false)
  const error = ref<string | null>(null)

  async function fetchNorms() {
    isLoading.value = true
    error.value = null
    try {
      const res = await normsApi.list()
      norms.value = res.norms || []
    } catch (e: any) {
      error.value = e?.response?.data?.error || e.message || 'Failed to fetch norms'
    } finally {
      isLoading.value = false
    }
  }

  async function createNorm(norm: { name: string; summary: string; content: string; is_active?: boolean }) {
    const res = await normsApi.create(norm)
    await fetchNorms()
    return res.norm
  }

  async function updateNorm(name: string, updates: Partial<Pick<UserNorm, 'summary' | 'content' | 'is_active'>>) {
    const res = await normsApi.update(name, updates)
    await fetchNorms()
    return res.norm
  }

  async function deleteNorm(name: string) {
    await normsApi.remove(name)
    await fetchNorms()
  }

  async function toggleActive(name: string, isActive: boolean) {
    await normsApi.update(name, { is_active: isActive })
    const idx = norms.value.findIndex(n => n.name === name)
    if (idx >= 0) {
      norms.value[idx].is_active = isActive
    }
  }

  return {
    norms,
    isLoading,
    error,
    fetchNorms,
    createNorm,
    updateNorm,
    deleteNorm,
    toggleActive
  }
})
