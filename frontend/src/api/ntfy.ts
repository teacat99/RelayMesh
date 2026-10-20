// ntfy 推送设置的 REST 封装（对应 internal/api/ntfy.go）。
// 访问令牌 / 密码「只写不读」：服务端永远不返回它们，只告诉界面是否已设置（token_set / password_set）。

import { api } from './client'

/** 服务端返回的配置视图（不含任何凭据）。 */
export interface NtfyConfig {
  enabled: boolean
  server: string
  topic: string
  username: string
  click_url: string
  /** ntfy 优先级 1~5。 */
  priority: number
  include_summary: boolean
  /** 延迟推送的秒数；0 = 立即。 */
  delay_seconds: number
  /** 模型发起的命令（ExecMesh）运行结束时是否也推送；默认关闭。 */
  run_notify: boolean
  /** 运行结束推送的门槛：运行得比这更短的命令不推送（1~86400 秒）。 */
  run_min_seconds: number
  token_set: boolean
  password_set: boolean
  /** 令牌由环境变量 RELAYMESH_NTFY_TOKEN 提供：界面里改不了，也清不掉。 */
  token_from_env: boolean
}

/** 自进程启动以来的推送情况（只在内存里）。 */
export interface NtfyStatus {
  last_attempt_at?: string
  last_ok: boolean
  /** 最近一次是不是「测试推送」。 */
  last_test: boolean
  last_http_status?: number
  last_error?: string
  sent: number
  failed: number
  /** 被限流合并或因队列已满而没发出的条数。 */
  dropped: number
}

export interface NtfyView {
  config: NtfyConfig
  status: NtfyStatus
}

/** 保存时提交的整张表单：非凭据字段整体替换；凭据留空 = 保持原值，要清掉就带 clear_ 标记。 */
export interface NtfyUpdate {
  enabled: boolean
  server: string
  topic: string
  username: string
  click_url: string
  priority: number
  include_summary: boolean
  delay_seconds: number
  run_notify: boolean
  run_min_seconds: number
  token?: string
  clear_token?: boolean
  password?: string
  clear_password?: boolean
}

export interface NtfyTestResult {
  ok: boolean
  attempts: number
  took_ms: number
  http_status: number
  error?: string
  status: NtfyStatus
}

export const ntfyApi = {
  async get(): Promise<NtfyView> {
    const res = await api.get<NtfyView>('/notify/ntfy')
    return res.data
  },

  async update(body: NtfyUpdate): Promise<NtfyView> {
    const res = await api.put<NtfyView>('/notify/ntfy', body)
    return res.data
  },

  /** 立刻发一条测试推送；draft 是尚未保存的表单（叠加在已保存的配置上试发），省略则测已保存的配置。 */
  async test(draft?: NtfyUpdate): Promise<NtfyTestResult> {
    // 服务端只试一次，单次最长约 12 秒；超时要比默认的 15 秒宽松一点，免得界面先放弃
    const res = await api.post<NtfyTestResult>('/notify/ntfy/test', draft ? { draft } : {}, { timeout: 30000 })
    return res.data
  },
}

/** 取出接口错误里给用户看的那句话。 */
export function ntfyErrorMessage(err: unknown, fallback = '操作失败，请稍后重试'): string {
  const e = err as { response?: { data?: { error?: string } }; message?: string }
  return e?.response?.data?.error || e?.message || fallback
}
