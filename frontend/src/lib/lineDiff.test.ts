import { describe, it } from 'node:test'
import assert from 'node:assert/strict'
import { lineDiffLcs } from './lineDiff'

describe('lineDiffLcs', () => {
  it('detects add and del', () => {
    const d = lineDiffLcs('a\nb', 'a\nc')
    assert.ok(d.some(x => x.type === 'del' && x.text === 'b'))
    assert.ok(d.some(x => x.type === 'add' && x.text === 'c'))
  })
})
