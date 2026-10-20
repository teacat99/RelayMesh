export interface DecisionOption {
  id: string
  label: string
  detail?: string
  recommended?: boolean
}

export interface DecisionCard {
  id: string
  title: string
  body?: string
  options: DecisionOption[]
  default?: string
  multi?: boolean
  note?: boolean
  risk?: string
}

export type DocSegment =
  | { kind: 'markdown'; text: string }
  | { kind: 'decision'; card: DecisionCard; errors: string[]; raw: string }

const fence = '```decision'

/** 将正文切成 Markdown 段与 decision 段（与 Go ParseDecisions 对齐）。 */
export function splitDocDecisions(body: string): DocSegment[] {
  const lines = body.split('\n')
  const out: DocSegment[] = []
  let md: string[] = []
  const flush = () => {
    if (md.length) {
      out.push({ kind: 'markdown', text: md.join('\n') })
      md = []
    }
  }
  for (let i = 0; i < lines.length; i++) {
    if (lines[i].trim() !== fence) {
      md.push(lines[i])
      continue
    }
    flush()
    i++
    const jsonLines: string[] = []
    while (i < lines.length && lines[i].trim() !== '```') {
      jsonLines.push(lines[i])
      i++
    }
    const raw = jsonLines.join('\n').trim()
    const errors: string[] = []
    let card: DecisionCard = { id: '', title: '', options: [] }
    if (!raw) {
      errors.push('decision 围栏内容为空')
    } else {
      try {
        card = JSON.parse(raw) as DecisionCard
        errors.push(...validateDecision(card))
      } catch {
        errors.push('JSON 解析失败')
      }
    }
    out.push({ kind: 'decision', card, errors, raw })
  }
  flush()
  return out
}

function validateDecision(c: DecisionCard): string[] {
  const errs: string[] = []
  if (!c.id?.trim()) errs.push('id 不能为空')
  if (!c.title?.trim()) errs.push('title 不能为空')
  if (!c.options || c.options.length < 2 || c.options.length > 8) errs.push('options 需要 2～8 个')
  const ids = new Set<string>()
  for (const o of c.options || []) {
    if (!o.id) errs.push('option 缺少 id')
    if (ids.has(o.id)) errs.push(`option id 重复：${o.id}`)
    ids.add(o.id)
    if (!o.label) errs.push(`option ${o.id} 缺少 label`)
  }
  if (c.default && !ids.has(c.default)) errs.push('default 必须是某个 option 的 id')
  if (c.risk && !['L0', 'L1', 'L2'].includes(c.risk)) errs.push('risk 必须是 L0/L1/L2')
  return errs
}
