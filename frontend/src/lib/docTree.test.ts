import { describe, it } from 'node:test'
import assert from 'node:assert/strict'
import { buildDocTree } from './docTree'
import type { ProjectDoc } from '@/api/projectDocs'

function doc(path: string): ProjectDoc {
  return {
    id: 1,
    workflow_id: 'w',
    seq: 1,
    path,
    title: path,
    kind: 'note',
    status: 'draft',
    tags: [],
    rev: 1,
    hash: '',
    updated_at: '',
  }
}

describe('buildDocTree', () => {
  it('嵌套路径与排序', () => {
    const tree = buildDocTree([doc('b/a.md'), doc('a/x.md'), doc('a/y.md')])
    assert.equal(tree.length, 2)
    assert.equal(tree[0].type, 'folder')
    if (tree[0].type === 'folder') {
      assert.equal(tree[0].name, 'a')
      assert.equal(tree[0].children.filter(c => c.type === 'leaf').length, 2)
    }
  })
})
