import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  anchorDivider,
  capsuleBasisLabel,
  capsuleSections,
  computeDividers,
  gitLogRangeCommand,
  gitLogSinceCommand,
  gitShowCommand,
  isSafeHash,
  sameCommit,
  shortHash,
  type AnchorSession,
  type CheckpointView
} from './anchor'

const round = (id: string, at: string, commit?: string, subject?: string): AnchorSession => ({
  session_id: id,
  created_at: at,
  git_commit: commit,
  git_subject: subject
})

const capsule = (revision: number, at: string, extra: Partial<CheckpointView> = {}): CheckpointView => ({
  revision,
  status: 'submitted',
  created_at: at,
  objective: `目标 ${revision}`,
  basis: {},
  ...extra
})

test('isSafeHash 只接受 7~40 位小写十六进制', () => {
  assert.equal(isSafeHash('57bb6d1'), true)
  assert.equal(isSafeHash('0123456789abcdef0123456789abcdef01234567'), true)
  assert.equal(isSafeHash('57bb6d'), false)
  assert.equal(isSafeHash('0123456789abcdef0123456789abcdef012345678'), false)
  assert.equal(isSafeHash('57BB6D1'), false)
  assert.equal(isSafeHash('57bb6d1; rm -rf /'), false)
  assert.equal(isSafeHash('$(whoami)'), false)
  assert.equal(isSafeHash(''), false)
  assert.equal(isSafeHash(undefined), false)
  assert.equal(isSafeHash(null), false)
})

test('shortHash 取前 7 位，空值返回空串', () => {
  assert.equal(shortHash('0123456789abcdef'), '0123456')
  assert.equal(shortHash('57bb6d1'), '57bb6d1')
  assert.equal(shortHash(undefined), '')
  assert.equal(shortHash('0123456789abcdef', 10), '0123456789')
})

test('sameCommit 长短不一时按前缀判断，空值不判断', () => {
  assert.equal(sameCommit('57bb6d1', '57bb6d1'), true)
  assert.equal(sameCommit('57bb6d1', '57bb6d1aaaaaaa'), true)
  assert.equal(sameCommit('57BB6D1', '57bb6d1'), true)
  assert.equal(sameCommit('57bb6d1', '7321232'), false)
  assert.equal(sameCommit('', '57bb6d1'), false)
  assert.equal(sameCommit(undefined, undefined), false)
})

test('复制命令只拼入合法哈希，非法哈希得到空串', () => {
  assert.equal(gitLogSinceCommand('57bb6d1'), 'git log --oneline 57bb6d1..HEAD')
  assert.equal(gitShowCommand('57bb6d1'), 'git show 57bb6d1')
  assert.equal(gitLogRangeCommand('57bb6d1', '7321232'), 'git log --oneline 57bb6d1..7321232')
  assert.equal(gitLogSinceCommand('57bb6d1; curl evil'), '')
  assert.equal(gitShowCommand('`id`'), '')
  assert.equal(gitLogRangeCommand('57bb6d1', 'bad hash'), '')
  assert.equal(gitLogRangeCommand(undefined, '7321232'), '')
})

test('anchorDivider：相邻两轮都有哈希且不同才出现', () => {
  const a = round('s1', '2026-10-01T10:00:00Z', '57bb6d1')
  const b = round('s2', '2026-10-01T11:00:00Z', '7321232')
  const same = round('s3', '2026-10-01T12:00:00Z', '7321232')
  const bare = round('s4', '2026-10-01T13:00:00Z')
  assert.deepEqual(anchorDivider(a, b), { from: '57bb6d1', to: '7321232' })
  assert.equal(anchorDivider(b, same), null, '哈希相同不显示')
  assert.equal(anchorDivider(b, bare), null, '后一轮没有哈希不判断')
  assert.equal(anchorDivider(bare, a), null, '前一轮没有哈希不判断')
  assert.equal(anchorDivider(undefined, a), null, '第一轮前面没有分隔条')
  assert.equal(anchorDivider(round('x', '2026-10-01T10:00:00Z', '57bb6d1'), round('y', '2026-10-01T10:01:00Z', '57bb6d1aaaa')), null, '长短哈希同一提交不显示')
})

test('computeDividers 不跨越空缺判断（漏传的轮次两侧都不比较）', () => {
  const rounds = [
    round('s1', '2026-10-01T10:00:00Z', '57bb6d1'),
    round('s2', '2026-10-01T11:00:00Z'),
    round('s3', '2026-10-01T12:00:00Z', '7321232'),
    round('s4', '2026-10-01T13:00:00Z', 'abcdef0')
  ]
  assert.deepEqual(computeDividers(rounds), { s4: { from: '7321232', to: 'abcdef0' } })
  assert.deepEqual(computeDividers([]), {})
  assert.deepEqual(computeDividers([round('only', '2026-10-01T10:00:00Z', '57bb6d1')]), {})
})

test('没有任何锚点的会话：不产生任何分隔条（与以前的界面一致）', () => {
  const rounds = [round('a', '2026-10-01T10:00:00Z'), round('b', '2026-10-01T11:00:00Z'), round('c', '2026-10-01T12:00:00Z')]
  assert.deepEqual(computeDividers(rounds), {})
})

test('capsuleBasisLabel / capsuleSections：只展示有内容的分区', () => {
  const full = capsule(5, '2026-10-01T10:00:00Z', {
    basis: { git_commit: '57bb6d1aaaa', anchor_session_id: 's1' },
    decisions: [{ decision_id: 'D-1', summary: '采用方案 A' }, { summary: '没有编号的决策' }],
    next_actions: ['做 M2'],
    blockers: ['等待用户拍板'],
    in_progress: []
  })
  assert.equal(capsuleBasisLabel(full), '基于 57bb6d1')
  assert.equal(capsuleBasisLabel(capsule(1, '2026-10-01T10:00:00Z')), '')
  assert.deepEqual(capsuleSections(full).map(s => s.key), ['objective', 'decisions', 'next_actions', 'blockers'])
  assert.deepEqual(capsuleSections(full)[1].items, ['D-1：采用方案 A', '没有编号的决策'])
  assert.deepEqual(capsuleSections(capsule(1, '2026-10-01T10:00:00Z', { objective: '' })), [])
})
