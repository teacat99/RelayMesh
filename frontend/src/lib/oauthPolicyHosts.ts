/** 与 internal/oauth/policy_hosts.go 口径一致：OAuth 策略里的自定义客户端域名。 */
export const OAUTH_CLIENT_HOST_ALLOWLIST_MAX = 32

export function normalizeClientHostAllowlist(lines: string[]): { hosts: string[]; error?: string } {
  const seen = new Set<string>()
  const out: string[] = []
  for (const raw of lines) {
    const h = raw.trim().toLowerCase()
    if (!h) continue
    const err = validateOAuthClientHost(h)
    if (err) return { hosts: [], error: err }
    if (seen.has(h)) continue
    seen.add(h)
    out.push(h)
    if (out.length > OAUTH_CLIENT_HOST_ALLOWLIST_MAX) {
      return { hosts: [], error: `自定义客户端域名最多 ${OAUTH_CLIENT_HOST_ALLOWLIST_MAX} 条` }
    }
  }
  return { hosts: out }
}

export function parseAllowlistText(text: string): string[] {
  return text.split(/[\n,，;；]+/)
}

export function allowlistToText(hosts: string[] | undefined): string {
  return (hosts ?? []).join('\n')
}

function validateOAuthClientHost(host: string): string | undefined {
  if (/[:/\\?#*[\]]/.test(host) || host.includes('..')) {
    return `域名「${host}」格式无效：只能填写主机名，不要带协议、端口或路径`
  }
  if (host.startsWith('.') || host.endsWith('.')) {
    return `域名「${host}」不能以点开头或结尾`
  }
  if (!/^[a-z0-9.-]+$/.test(host)) {
    return `域名「${host}」含有非法字符`
  }
  return undefined
}
