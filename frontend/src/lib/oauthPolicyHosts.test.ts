import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import { normalizeClientHostAllowlist, parseAllowlistText } from './oauthPolicyHosts'

describe('oauthPolicyHosts', () => {
  it('去重并小写化', () => {
    const r = normalizeClientHostAllowlist(parseAllowlistText('Example.COM\nexample.com\ntools.example.com'))
    assert.equal(r.error, undefined)
    assert.deepEqual(r.hosts, ['example.com', 'tools.example.com'])
  })

  it('拒绝带协议的条目', () => {
    const r = normalizeClientHostAllowlist(['https://evil.com'])
    assert.ok(r.error?.includes('协议'))
  })
})
