import { defineStore } from 'pinia'
import { ref } from 'vue'
import { credentialsApi, type MCPCredential, type CredentialInput, type EnvCredential } from '../api/client'

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

  async function createCredential(data: CredentialInput & { name: string }) {
    const res = await credentialsApi.create(data)
    await fetchCredentials()
    return { credential: res.credential, token: res.token, capabilityPath: res.capability_path ?? '' }
  }

  async function updateCredential(id: number, updates: CredentialInput) {
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
    return { token: res.token, capabilityPath: res.capability_path ?? '' }
  }

  /** 验证密码后读出令牌明文；只读，不会改动列表，也不缓存明文。 */
  async function revealToken(id: number, password: string) {
    const res = await credentialsApi.revealToken(id, password)
    return { token: res.token, capabilityPath: res.capability_path ?? '' }
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
    revealToken,
    toggleActive
  }
})
