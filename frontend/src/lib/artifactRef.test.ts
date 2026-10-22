import { test } from 'node:test'
import assert from 'node:assert/strict'
import { extractArtifactRefs, formatArtifactRef } from './artifactRef'

test('取出引用并去掉记号', () => {
  assert.deepEqual(extractArtifactRefs('看板做好了\n\n[[artifact:12]]'), { text: '看板做好了', ids: [12] })
  assert.deepEqual(extractArtifactRefs('[[artifact:3]] 和 [[artifact:4]] 还有 [[artifact:3]]'), { text: '和  还有', ids: [3, 4] })
})

test('没有引用时原样返回；非法记号不算', () => {
  assert.deepEqual(extractArtifactRefs('普通文字 [[artifact:x]] [[artifact:0]]'), { text: '普通文字 [[artifact:x]]', ids: [] })
  assert.deepEqual(extractArtifactRefs(undefined), { text: '', ids: [] })
  assert.deepEqual(extractArtifactRefs('  保留首尾空白  '), { text: '  保留首尾空白  ', ids: [] })
})

test('格式化', () => {
  assert.equal(formatArtifactRef(7), '[[artifact:7]]')
})
