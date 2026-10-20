// 新反馈请求的「只提醒一次」：同一条待回复会话可能经过好几条路径进入前端——事件流里的完整载荷、按 id 补拉、
// 兜底的「当前会话」校准——每条路径都要有资格触发提醒，但同一条请求只能响一次。
// 纯逻辑，便于单元测试；提醒本身（声音 / 桌面通知 / ExecMesh 里的消息）由通知仓库完成。

export interface AnnounceCandidate {
  session_id: string
  status: string
}

export interface RequestAnnouncer {
  /**
   * 这条会话此刻是否应当提醒：必须是待回复的，并且此前没有提醒过。
   * 返回 true 时同时登记，之后再问同一条一律返回 false。
   */
  claim(session: AnnounceCandidate): boolean
}

/** limit：最多记住多少条；超出时先忘掉最早的（最坏的结果只是极旧的请求被再提醒一次）。 */
export function createRequestAnnouncer(limit = 500): RequestAnnouncer {
  const seen = new Set<string>()
  return {
    claim(session) {
      if (session.status !== 'pending' || !session.session_id || seen.has(session.session_id)) return false
      seen.add(session.session_id)
      if (seen.size > limit) {
        const oldest = seen.values().next().value
        if (oldest !== undefined) seen.delete(oldest)
      }
      return true
    },
  }
}
