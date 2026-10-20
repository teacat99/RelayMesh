// 「查看旧令牌」的纯逻辑：错误文案映射、锁定时间的人话表述、令牌在屏幕上停留的时长。
// 独立成纯 TS 模块，是为了能脱离 Vue 组件做单元测试。

/** 令牌明文在界面上停留的最长时间；到点自动隐藏，避免人走开后令牌一直挂在屏幕上。 */
export const REVEAL_AUTO_HIDE_MS = 60_000

export interface RevealErrorInfo {
  /** 展示给人的提示 */
  message: string
  /** 来源已被临时锁定：再输入没有意义，界面应暂停提交 */
  locked: boolean
  /** 令牌已不存在（可能被删除）：界面应收起输入框并刷新列表 */
  gone: boolean
}

interface ErrorLike {
  response?: {
    status?: number
    data?: { error?: string; code?: string; remaining_seconds?: number }
  }
  message?: string
}

/** 把剩余秒数说成「约 N 分钟 / 不到 1 分钟」；不是正数就返回空串，由调用方决定怎么兜底。 */
export function formatLockRemaining(seconds: unknown): string {
  if (typeof seconds !== 'number' || !Number.isFinite(seconds) || seconds <= 0) return ''
  if (seconds < 60) return '不到 1 分钟'
  return `约 ${Math.ceil(seconds / 60)} 分钟`
}

/**
 * 把「查看令牌」请求的失败映射成人话。
 * 约定：密码错误是 403（不是 401——401 会被全局拦截器当成登录过期而踢回登录页）；被锁定是 429；令牌不存在是 404。
 */
export function revealErrorInfo(err: unknown): RevealErrorInfo {
  const e = (err ?? {}) as ErrorLike
  const status = e.response?.status
  const data = e.response?.data

  if (status === undefined) {
    return { message: '无法连接到服务端，请检查网络后重试', locked: false, gone: false }
  }
  if (status === 429) {
    const wait = formatLockRemaining(data?.remaining_seconds)
    return {
      message: wait ? `连续输错次数过多，已被临时锁定，请${wait}后再试` : data?.error || '连续输错次数过多，已被临时锁定，请稍后再试',
      locked: true,
      gone: false,
    }
  }
  if (status === 403 && data?.code === 'reveal_bad_password') {
    return { message: data.error || '密码错误，请重新输入', locked: false, gone: false }
  }
  if (status === 404) {
    return { message: '这个令牌已不存在（可能已被删除）', locked: false, gone: true }
  }
  if (status === 401) {
    return { message: '登录已过期，请重新登录后再试', locked: false, gone: false }
  }
  if (status === 503) {
    return { message: '服务端暂时无法验证密码，请稍后重试', locked: false, gone: false }
  }
  return { message: data?.error || e.message || '查看失败', locked: false, gone: false }
}
