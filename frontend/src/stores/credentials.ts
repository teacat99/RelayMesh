import { defineStore } from 'pinia'
import { ref } from 'vue'
import { credentialsApi, type MCPCredential, type MCPPermissions, type EnvCredential } from '../api/client'

export const useCredentialsStore = defineStore('credentials', () => {
  const credentials = ref<MCPCredential[]>([])
  const envCredentials = ref<EnvCredential[]>([])
  const isLoading = ref(false)
  const error = ref<string | null>(null)

  async function fetchCredentials() {
    isLoading.value = true
    error.value = null
    try {
      const res = await credentialsApi.list()
      credentials.value = res.credentials || []
      envCredentials.value = res.env_credentials || []
    } catch (e: any) {
      error.value = e?.response?.data?.error || e.message || 'Failed to fetch credentials'
    } finally {
      isLoading.value = false
    }
  }

  async function createCredential(data: { name: string; host_name?: string; is_active?: boolean; permissions?: Partial<MCPPermissions>; note?: string }) {
    const res = await credentialsApi.create(data)
    await fetchCredentials()
    return { credential: res.credential, token: res.token }
  }

  async function updateCredential(id: number, updates: Partial<Pick<MCPCredential, 'name' | 'host_name' | 'is_active' | 'permissions' | 'note'>>) {
    const res = await credentialsApi.update(id, updates)
    await fetchCredentials()
    return res.credential
  }

  async function deleteCredential(id: number) {
    await credentialsApi.remove(id)
    await fetchCredentials()
  }

  async function regenerateToken(id: number) {
    const res = await credentialsApi.regenerateToken(id)
    await fetchCredentials()
    return res.token
  }

  async function toggleActive(id: number, isActive: boolean) {
    await credentialsApi.update(id, { is_active: isActive })
    const idx = credentials.value.findIndex(c => c.id === id)
    if (idx >= 0) {
      credentials.value[idx].is_active = isActive
    }
  }

  return {
    credentials,
    envCredentials,
    isLoading,
    error,
    fetchCredentials,
    createCredential,
    updateCredential,
    deleteCredential,
    regenerateToken,
    toggleActive
  }
})
