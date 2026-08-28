import { defineStore } from 'pinia'
import { ref } from 'vue'
import { api, authApi, setAuthToken } from '../api/client'

export interface AuthStatusResponse {
  auth_required: boolean
  mcp_auth_required: boolean
  project_id: string
  host_name?: string
  current_username?: string
  is_customized?: boolean
}

export interface LoginResponse {
  status: string
  auth_required: boolean
  token: string
  expires_in?: number
  message?: string
}

export const useAuthStore = defineStore('auth', () => {
  const token = ref<string>(localStorage.getItem('relaymesh_token') || '')
  const authRequired = ref<boolean>(false)
  const mcpAuthRequired = ref<boolean>(false)
  const projectId = ref<string>('default')
  const hostName = ref<string>('')
  const currentUsername = ref<string>('admin')
  const isCustomized = ref<boolean>(false)
  const isAuthenticated = ref<boolean>(false)
  const showLoginModal = ref<boolean>(false)
  const loginLoading = ref<boolean>(false)
  const loginError = ref<string>('')
  const isLocked = ref<boolean>(false)
  const lockedRemainingSeconds = ref<number>(0)
  let countdownInterval: number | null = null

  function startLockoutCountdown(seconds: number) {
    isLocked.value = true
    lockedRemainingSeconds.value = Math.max(seconds, 1)
    if (countdownInterval) clearInterval(countdownInterval)
    countdownInterval = window.setInterval(() => {
      lockedRemainingSeconds.value--
      if (lockedRemainingSeconds.value <= 0) {
        if (countdownInterval) clearInterval(countdownInterval)
        countdownInterval = null
        isLocked.value = false
        loginError.value = ''
      }
    }, 1000)
  }

  // 检查后端鉴权状态
  async function checkAuthStatus(): Promise<boolean> {
    try {
      const res = await api.get<AuthStatusResponse>('/auth/status')
      authRequired.value = res.data.auth_required
      mcpAuthRequired.value = res.data.mcp_auth_required
      projectId.value = res.data.project_id || 'default'
      hostName.value = res.data.host_name || ''
      currentUsername.value = res.data.current_username || 'admin'
      isCustomized.value = !!res.data.is_customized

      if (!authRequired.value) {
        // 免密模式
        isAuthenticated.value = true
        showLoginModal.value = false
        return true
      }

      // 密码保护模式
      if (token.value) {
        setAuthToken(token.value)
        isAuthenticated.value = true
        showLoginModal.value = false
        return true
      } else {
        isAuthenticated.value = false
        showLoginModal.value = true
        return false
      }
    } catch (err) {
      console.error('Failed to check auth status', err)
      return false
    }
  }

  // 账号密码登录
  async function login(username: string, password: string): Promise<boolean> {
    loginLoading.value = true
    loginError.value = ''
    try {
      const res = await api.post<LoginResponse>('/auth/login', {
        username: username.trim(),
        password: password.trim()
      })
      if (res.data.token) {
        token.value = res.data.token
        setAuthToken(res.data.token)
        isAuthenticated.value = true
        showLoginModal.value = false
        isLocked.value = false
        if (countdownInterval) clearInterval(countdownInterval)
        return true
      } else if (!res.data.auth_required) {
        isAuthenticated.value = true
        showLoginModal.value = false
        isLocked.value = false
        if (countdownInterval) clearInterval(countdownInterval)
        return true
      }
      return false
    } catch (err: any) {
      console.error('Login failed', err)
      if (err.response?.status === 429) {
        const remaining = err.response?.data?.remaining_seconds || 900
        startLockoutCountdown(remaining)
        loginError.value = err.response?.data?.error || '由于连续多次尝试失败，该 IP 已被安全锁定'
      } else {
        loginError.value = err.response?.data?.error || '登录失败，请检查账号和密码'
      }
      return false
    } finally {
      loginLoading.value = false
    }
  }

  // 退出登录
  function logout() {
    token.value = ''
    setAuthToken('')
    isAuthenticated.value = false
    if (authRequired.value) {
      showLoginModal.value = true
    }
  }

  // 处理 401 未授权拦截
  function handleUnauthorized() {
    if (authRequired.value) {
      token.value = ''
      setAuthToken('')
      isAuthenticated.value = false
      showLoginModal.value = true
    }
  }

  // 修改管理账号与访问密码
  async function changeCredentials(newUsername: string, newPassword: string, oldPassword?: string): Promise<{ success: boolean; message: string }> {
    try {
      const res = await authApi.changeCredentials({
        old_password: oldPassword,
        new_username: newUsername.trim(),
        new_password: newPassword.trim()
      })
      if (res.status === 'ok') {
        currentUsername.value = res.username
        isCustomized.value = true
        if (res.token) {
          token.value = res.token
          setAuthToken(res.token)
        }
        localStorage.setItem('relaymesh_username', res.username)
        return { success: true, message: res.message || '更新成功' }
      }
      return { success: false, message: '更新失败' }
    } catch (err: any) {
      return { success: false, message: err.response?.data?.error || '修改账号密码失败，请检查旧密码' }
    }
  }

  // 重置账号密码为环境变量初始值
  async function resetCredentials(): Promise<{ success: boolean; message: string }> {
    try {
      const res = await authApi.resetCredentials()
      if (res.status === 'ok') {
        currentUsername.value = res.username
        isCustomized.value = false
        localStorage.setItem('relaymesh_username', res.username)
        return { success: true, message: res.message || '已重置为初始设定值' }
      }
      return { success: false, message: '重置失败' }
    } catch (err: any) {
      return { success: false, message: err.response?.data?.error || '重置失败' }
    }
  }

  return {
    token,
    authRequired,
    mcpAuthRequired,
    projectId,
    hostName,
    currentUsername,
    isCustomized,
    isAuthenticated,
    showLoginModal,
    loginLoading,
    loginError,
    isLocked,
    lockedRemainingSeconds,
    checkAuthStatus,
    login,
    changeCredentials,
    resetCredentials,
    logout,
    handleUnauthorized
  }
})
