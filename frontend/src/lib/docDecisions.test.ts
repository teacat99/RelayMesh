import { describe, it } from 'node:test'
import assert from 'node:assert/strict'
import { splitDocDecisions } from './docDecisions'

describe('splitDocDecisions', () => {
  it('splits decision fence', () => {
    const body = '# T\n\n```decision\n{"id":"d1","title":"Q","options":[{"id":"a","label":"A"},{"id":"b","label":"B"}],"default":"a"}\n```\n'
    const segs = splitDocDecisions(body)
    assert.equal(segs.filter(s => s.kind === 'decision').length, 1)
  })
})
