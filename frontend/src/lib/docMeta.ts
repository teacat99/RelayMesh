/** 文档类型 / 状态展示元数据（与 Work 页 tone 一致）。 */
export const DOC_KINDS = [
  'design',
  'decision',
  'plan',
  'eval',
  'spec',
  'guide',
  'reference',
  'runbook',
  'note',
  'changelog',
  'other',
] as const

export const DOC_STATUSES = ['draft', 'review', 'approved', 'superseded', 'archived'] as const

export const DOC_KIND_LABEL: Record<string, string> = {
  design: '设计',
  decision: '决策',
  plan: '规划',
  eval: '评估',
  spec: '规格',
  guide: '指南',
  reference: '参考',
  runbook: '手册',
  note: '笔记',
  changelog: '变更',
  other: '其他',
}

export const DOC_STATUS_LABEL: Record<string, string> = {
  draft: '草稿',
  review: '审阅',
  approved: '已定',
  superseded: '已替代',
  archived: '归档',
}

/** 状态点颜色（Tailwind 背景类） */
export function docStatusDotClass(status: string): string {
  switch (status) {
    case 'approved':
      return 'bg-emerald-500'
    case 'review':
      return 'bg-sky-500'
    case 'superseded':
      return 'bg-amber-500'
    case 'archived':
      return 'bg-muted-foreground/50'
    default:
      return 'bg-muted-foreground/70'
  }
}

/** 类型图标缩写（侧栏叶子前缀） */
export function docKindAbbrev(kind: string): string {
  return (DOC_KIND_LABEL[kind] || kind).slice(0, 1)
}
