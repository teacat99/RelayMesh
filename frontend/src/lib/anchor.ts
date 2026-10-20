// 交付锚点与恢复胶囊在时间线上的纯逻辑（不依赖 Vue，便于单测）。
//
// 约束：只用相对导入与 import type（单测用 esbuild 直接打包，不解析 `@/` 别名）。

export interface AnchorSession {
  session_id: string
  git_commit?: string
  git_subject?: string
  created_at: string
}

export interface CheckpointBasisView {
  task_revision?: number
  report_sequence?: number
  feedback_sequence?: number
  git_commit?: string
  anchor_session_id?: string
}

export interface CheckpointDecisionView {
  decision_id?: string
  status?: string
  summary: string
}

export interface CheckpointView {
  revision: number
  status: string
  created_at: string
  objective: string
  current_stage?: { id: string; status: string }
  decisions?: CheckpointDecisionView[]
  next_actions?: string[]
  in_progress?: string[]
  blockers?: string[]
  basis: CheckpointBasisView
}

const HASH_PATTERN = /^[0-9a-f]{7,40}$/

/** 哈希是否合法（小写十六进制 7~40 位）。复制到剪贴板的命令只允许拼入合法哈希，杜绝把脏文本带进用户的终端。 */
export function isSafeHash(hash: string | undefined | null): hash is string {
  return typeof hash === 'string' && HASH_PATTERN.test(hash)
}

/** 短哈希：默认取前 7 位。 */
export function shortHash(hash: string | undefined | null, length = 7): string {
  if (!hash) return ''
  return hash.slice(0, length)
}

/** 两个哈希是否指向同一提交（长短不一时互为前缀即视为相同）。任一为空则不判断，返回 false。 */
export function sameCommit(a: string | undefined | null, b: string | undefined | null): boolean {
  if (!a || !b) return false
  const x = a.toLowerCase()
  const y = b.toLowerCase()
  return x === y || x.startsWith(y) || y.startsWith(x)
}

/** `git log --oneline <hash>..HEAD`；哈希不合法时返回空串。 */
export function gitLogSinceCommand(hash: string | undefined | null): string {
  return isSafeHash(hash) ? `git log --oneline ${hash}..HEAD` : ''
}

/** `git log --oneline <from>..<to>`；任一哈希不合法时返回空串。 */
export function gitLogRangeCommand(from: string | undefined | null, to: string | undefined | null): string {
  return isSafeHash(from) && isSafeHash(to) ? `git log --oneline ${from}..${to}` : ''
}

/** `git show <hash>`；哈希不合法时返回空串。 */
export function gitShowCommand(hash: string | undefined | null): string {
  return isSafeHash(hash) ? `git show ${hash}` : ''
}

export interface AnchorDivider {
  from: string
  to: string
}

/**
 * 相邻两轮之间是否要显示「期间有新提交」分隔条。
 * 只有相邻两轮**都有**哈希且不同才显示；任何一轮没有哈希就不判断（不猜）。
 */
export function anchorDivider(prev: AnchorSession | undefined, next: AnchorSession | undefined): AnchorDivider | null {
  if (!prev?.git_commit || !next?.git_commit) return null
  if (sameCommit(prev.git_commit, next.git_commit)) return null
  return { from: prev.git_commit, to: next.git_commit }
}

/** 对整个轮次序列计算每一轮「前面」的分隔条，键为后一轮的 session_id。 */
export function computeDividers(rounds: AnchorSession[]): Record<string, AnchorDivider> {
  const out: Record<string, AnchorDivider> = {}
  for (let i = 1; i < rounds.length; i++) {
    const divider = anchorDivider(rounds[i - 1], rounds[i])
    if (divider) out[rounds[i].session_id] = divider
  }
  return out
}

/** 胶囊基准的展示文案：「基于 57bb6d1」；没有基准则为空串。 */
export function capsuleBasisLabel(capsule: CheckpointView): string {
  const hash = shortHash(capsule.basis?.git_commit)
  return hash ? `基于 ${hash}` : ''
}

/** 胶囊详情里需要展示的分区（过滤掉空分区），保证界面不出现空标题。 */
export function capsuleSections(capsule: CheckpointView): { key: string; title: string; items: string[] }[] {
  const sections: { key: string; title: string; items: string[] }[] = []
  if (capsule.objective) sections.push({ key: 'objective', title: '目标', items: [capsule.objective] })
  const decisions = (capsule.decisions ?? [])
    .map(d => (d.decision_id ? `${d.decision_id}：${d.summary}` : d.summary))
    .filter(Boolean)
  if (decisions.length) sections.push({ key: 'decisions', title: '决策', items: decisions })
  if (capsule.in_progress?.length) sections.push({ key: 'in_progress', title: '进行中', items: capsule.in_progress })
  if (capsule.next_actions?.length) sections.push({ key: 'next_actions', title: '下一步', items: capsule.next_actions })
  if (capsule.blockers?.length) sections.push({ key: 'blockers', title: '阻塞', items: capsule.blockers })
  return sections
}
