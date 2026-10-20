import { describe, it } from 'node:test'
import assert from 'node:assert/strict'
import { parseDocHeadings } from './docHeadings'
import { buildDocTree } from './docTree'

describe('docHeadings', () => {
  it('skips headings inside fences', () => {
    const hs = parseDocHeadings('# A\n```\n# B\n```\n## C')
    assert.equal(hs.length, 2)
    assert.equal(hs[0].text, 'A')
  })
  it('builds tree', () => {
    const t = buildDocTree([
      { id: 1, workflow_id: 'w', seq: 1, path: 'a/b.md', title: 'b', kind: 'note', status: 'draft', tags: [], rev: 1, hash: '', updated_at: '' },
      { id: 2, workflow_id: 'w', seq: 2, path: 'a/c.md', title: 'c', kind: 'note', status: 'draft', tags: [], rev: 1, hash: '', updated_at: '' },
    ])
    assert.equal(t[0].type, 'folder')
    if (t[0].type === 'folder') assert.equal(t[0].children.length, 2)
  })
})
