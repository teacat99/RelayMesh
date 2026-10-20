// 确认项（Q12）的纯逻辑：作答校验、推荐采用、分组、时间线落点、文档决策卡汇总、答复文案。
// 与 internal/confirm 里的规则保持一致（推荐选项的取法、L2 不进批量），界面只负责呈现。

import type { ConfirmBoard, ConfirmItem, ConfirmKind, ConfirmOption, ConfirmRisk, ConfirmStatus } from '../api/confirms'

export const CONFIRM_NOTE_MAX = 2000
/** 自定义答复（#183）的字数上限，与服务端 model.ConfirmCustomMax 一致。 */
export const CONFIRM_CUSTOM_MAX = 2000

export const KIND_LABEL: Record<ConfirmKind, string> = {
  ask: '智能体提问',
  plan: '计划确认',
  decision: '文档决策',
}

export const RISK_LABEL: Record<ConfirmRisk, string> = {
  L0: 'L0 · 只读 / 随时可回滚',
  L1: 'L1 · 可回滚的改动',
  L2: 'L2 · 不可逆 / 高风险',
}

export const RISK_SHORT: Record<ConfirmRisk, string> = { L0: 'L0', L1: 'L1', L2: 'L2' }

export const STATUS_LABEL: Record<ConfirmStatus, string> = {
  pending: '待答复',
  answered: '已答复',
  dismissed: '已忽略',
  withdrawn: '已撤回',
  expired: '已过期',
}

export type ConfirmTone = 'muted' | 'warn' | 'danger'

export function riskTone(risk: ConfirmRisk | string | undefined): ConfirmTone {
  if (risk === 'L2') return 'danger'
  if (risk === 'L1') return 'warn'
  return 'muted'
}

export function confirmRef(seq: number): string {
  return seq > 0 ? `Q${seq}` : ''
}

/** 带推荐标记的选项 id：单选取第一个，多选取全部；没有推荐时返回空数组（与服务端 RecommendedOptionIDs 一致）。 */
export function recommendedIds(opts: ReadonlyArray<ConfirmOption>, multi: boolean): string[] {
  const out: string[] = []
  for (const o of opts) {
    if (!o.recommended) continue
    out.push(o.id)
    if (!multi) break
  }
  return out
}

type Choosable = Pick<ConfirmItem, 'options' | 'multi' | 'risk'>

/** 一键采用推荐：只有 L0 / L1 才给（L2 必须逐条有意识地选），且得真的有推荐选项。 */
export function canAdoptRecommended(item: Choosable & Partial<Pick<ConfirmItem, 'status'>>): boolean {
  if (item.status && item.status !== 'pending') return false
  if (item.risk === 'L2') return false
  return recommendedIds(item.options, item.multi).length > 0
}

/** 点选项后的新选中集合：单选替换，多选切换；保持选项在原列表里的顺序。 */
export function toggleSelection(opts: ReadonlyArray<ConfirmOption>, multi: boolean, current: ReadonlyArray<string>, id: string): string[] {
  if (!multi) return [id]
  const next = new Set(current)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  return opts.map(o => o.id).filter(x => next.has(x))
}

export interface SelectionCheck {
  ok: boolean
  /** 不能提交时给用户看的一句话。 */
  reason: string
}

/** 自定义答复的输入状态（#183）：on = 选中了「其他（自定义）」；text = 文本框里写的内容（没选中时可能还留着，提交时不算）。 */
export interface CustomInput {
  on: boolean
  text: string
}

/** 点「其他（自定义）」后预设选项怎么变：单选时自定义与预设选项二选一（打开自定义就清空选项），多选时两者并存。 */
export function selectionAfterCustomToggle(multi: boolean, current: ReadonlyArray<string>, customWillBeOn: boolean): string[] {
  if (!multi && customWillBeOn) return []
  return [...current]
}

export function checkSelection(
  item: Pick<ConfirmItem, 'options' | 'multi' | 'allow_note'>,
  selected: ReadonlyArray<string>,
  note: string,
  custom?: CustomInput,
): SelectionCheck {
  // 自定义答复（#183）：预设选项都不合适时的出路，所有确认项都有，不受 allow_note 约束；选中了就必须写内容
  const customOn = !!custom?.on
  const customText = customOn ? custom!.text.trim() : ''
  if (customOn && !customText) return { ok: false, reason: '请写下自定义内容，或取消「其他（自定义）」' }
  if ([...customText].length > CONFIRM_CUSTOM_MAX) return { ok: false, reason: `自定义内容不能超过 ${CONFIRM_CUSTOM_MAX} 字` }
  if (selected.length === 0 && !customOn) return { ok: false, reason: '请先选一个选项；都不合适就选「其他（自定义）」自己写' }
  if (!item.multi) {
    if (customOn && selected.length > 0) return { ok: false, reason: '这一项是单选：预设选项和自定义内容只能二选一' }
    if (!customOn && selected.length !== 1) return { ok: false, reason: '这一项是单选，只能选一个' }
  }
  const known = new Set(item.options.map(o => o.id))
  for (const id of selected) {
    if (!known.has(id)) return { ok: false, reason: '选中的选项已经不存在，请刷新后重选' }
  }
  const n = note.trim()
  if (n && !item.allow_note) return { ok: false, reason: '这一项不接受备注' }
  if ([...n].length > CONFIRM_NOTE_MAX) return { ok: false, reason: `备注不能超过 ${CONFIRM_NOTE_MAX} 字` }
  return { ok: true, reason: '' }
}

export function optionLabel(opts: ReadonlyArray<ConfirmOption>, id: string): string {
  return opts.find(o => o.id === id)?.label || id
}

/** 已答复里用户写的自定义答复（#183）；没写（含老答复）为空串。 */
export function customAnswerOf(item: Pick<ConfirmItem, 'answer'>): string {
  return item.answer?.custom?.trim() ?? ''
}

/** 把一段可能很长、带换行的文字压成一行摘要：取第一个非空行、合并空白，超过 max 个字符时截断加「…」；还有被省略的后续行也加「…」。 */
export function condenseText(text: string, max: number): string {
  const lines = text.split('\n').map(l => l.replace(/\s+/g, ' ').trim()).filter(Boolean)
  if (lines.length === 0) return ''
  const chars = Array.from(lines[0])
  const cut = chars.length > max
  return (cut ? chars.slice(0, max).join('') : lines[0]) + (cut || lines.length > 1 ? '…' : '')
}

/**
 * 已答复的一句话：「先做 B」「A、C；备注：…」；写了自定义答复的最后追加「自定义：…」（只写自定义时前面没有选项）。
 * customMax：给了就把自定义答复压成一行摘要（折叠的列表行、提示条用，免得一大段文字撑开一行）；不给就是全文。
 */
export function answerText(item: Pick<ConfirmItem, 'options' | 'answer'>, customMax?: number): string {
  const a = item.answer
  if (!a) return ''
  const picked = a.options ?? []
  const custom = customAnswerOf(item)
  if (picked.length === 0 && !custom) return ''
  const segs: string[] = []
  if (picked.length > 0) segs.push(picked.map(id => optionLabel(item.options, id)).join('、'))
  const note = a.note?.trim()
  if (note) segs.push(`备注：${note}`)
  if (custom) segs.push(`自定义：${customMax ? condenseText(custom, customMax) : custom}`)
  return segs.join('；')
}

export interface RecallOption {
  id: string
  label: string
  detail?: string
  recommended: boolean
  /** 用户选了它（只有已答复的卡才有）。 */
  chosen: boolean
  /** 答复里有这个 id，但选项里已经找不到了（文档决策卡的选项后来改过）。 */
  missing: boolean
}

/**
 * 回看一张处理过的卡（#132）：把全部选项列出来，用户选中的标出来；已忽略 / 已撤回 / 已过期的没有选中项。
 * 答复里有、选项里却找不到的 id 追加在最后，照实标成「已不在选项里」，不假装没发生过。
 */
export function recallOptions(item: Pick<ConfirmItem, 'options' | 'status' | 'answer'>): RecallOption[] {
  const picked = item.status === 'answered' ? (item.answer?.options ?? []) : []
  const chosen = new Set(picked)
  const out: RecallOption[] = item.options.map(o => ({
    id: o.id,
    label: o.label || o.id,
    detail: o.detail || undefined,
    recommended: !!o.recommended,
    chosen: chosen.has(o.id),
    missing: false,
  }))
  const known = new Set(item.options.map(o => o.id))
  for (const id of picked) {
    if (!known.has(id)) out.push({ id, label: id, recommended: false, chosen: true, missing: true })
  }
  return out
}

const pad2 = (n: number) => String(n).padStart(2, '0')

/** 「10-15 17:32」（浏览器本地时间）；时间无法解析时为空串。 */
export function clockText(iso: string | undefined): string {
  if (!iso) return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  return `${pad2(d.getMonth() + 1)}-${pad2(d.getDate())} ${pad2(d.getHours())}:${pad2(d.getMinutes())}`
}

const HANDLED_VERB: Partial<Record<ConfirmStatus, string>> = {
  answered: '答复于',
  dismissed: '忽略于',
  withdrawn: '撤回于',
  expired: '过期于',
}

/** 回看卡片上的时间说明：「提出于 10-15 17:20 · 答复于 10-15 17:32」；还没处理的只有前半句。 */
export function recallTimes(item: Pick<ConfirmItem, 'status' | 'created_at' | 'answered_at' | 'updated_at'>): string {
  const parts: string[] = []
  const created = clockText(item.created_at)
  if (created) parts.push(`提出于 ${created}`)
  const verb = HANDLED_VERB[item.status]
  const at = clockText(item.status === 'answered' ? item.answered_at || item.updated_at : item.updated_at)
  if (verb && at) parts.push(`${verb} ${at}`)
  return parts.join(' · ')
}

export interface ConfirmGroup {
  risk: ConfirmRisk
  label: string
  items: ConfirmItem[]
}

const RISK_ORDER: ConfirmRisk[] = ['L2', 'L1', 'L0']

/** 待处理项按风险分组（高风险在前），组内旧的在前；没有项的组不出现。 */
export function groupByRisk(items: ReadonlyArray<ConfirmItem>): ConfirmGroup[] {
  const groups: ConfirmGroup[] = []
  for (const risk of RISK_ORDER) {
    const list = items
      .filter(i => (i.risk || 'L1') === risk)
      .sort((a, b) => a.seq - b.seq)
    if (list.length) groups.push({ risk, label: RISK_LABEL[risk], items: list })
  }
  return groups
}

/** 可以批量「采用推荐」的项：待处理、非 L2、有推荐选项。 */
export function adoptableItems(items: ReadonlyArray<ConfirmItem>): ConfirmItem[] {
  return items.filter(i => canAdoptRecommended(i)).sort((a, b) => a.seq - b.seq)
}

export interface AdoptPreviewRow {
  ref: string
  title: string
  kind: ConfirmKind
  choice: string
}

/** 批量采用推荐前给用户看的清单：每条写明「将提交什么」。 */
export function adoptPreview(items: ReadonlyArray<ConfirmItem>): AdoptPreviewRow[] {
  return adoptableItems(items).map(i => ({
    ref: i.ref || confirmRef(i.seq),
    title: i.title,
    kind: i.kind,
    choice: recommendedIds(i.options, i.multi).map(id => optionLabel(i.options, id)).join('、'),
  }))
}

/** 工具栏「待答复」徽标的文案：「待答复 3」「待答复 3 · 告警 1」「告警 1」；都为 0 时返回空串（徽标不出现）。 */
export function pendingChipText(items: number, guards: number): string {
  const parts: string[] = []
  if (items > 0) parts.push(`待答复 ${items}`)
  if (guards > 0) parts.push(`告警 ${guards}`)
  return parts.join(' · ')
}

/** 徽标的悬浮提示：说清楚数的是什么、点开能做什么。 */
export function pendingChipTitle(items: number, guards: number): string {
  const parts: string[] = []
  if (items > 0) parts.push(`${items} 项提问等你答复`)
  if (guards > 0) parts.push(`${guards} 条守护告警`)
  return parts.length ? `${parts.join('、')}；点开可定位到对话里的卡片` : ''
}

/** 「多久以前」的人话：刚刚 / N 分钟前 / N 小时前 / N 天前；时间无法解析时为空串。 */
export function ageText(iso: string | undefined, nowMs: number = Date.now()): string {
  if (!iso) return ''
  const t = Date.parse(iso)
  if (!Number.isFinite(t)) return ''
  const sec = Math.max(0, Math.round((nowMs - t) / 1000))
  if (sec < 60) return '刚刚'
  if (sec < 3600) return `${Math.floor(sec / 60)} 分钟前`
  if (sec < 86400) return `${Math.floor(sec / 3600)} 小时前`
  return `${Math.floor(sec / 86400)} 天前`
}

// ---------- 文档里的决策卡 ----------

export type DecisionStatus = 'pending' | 'answered' | 'expired' | 'withdrawn' | 'dismissed' | 'invalid'

/** 文档详情接口里 decisions[] 的一项（服务端把围栏与确认项对齐后的视图）。 */
export interface DocDecisionView {
  card: {
    id: string
    title: string
    body?: string
    options: ConfirmOption[]
    default?: string
    multi?: boolean
    note?: boolean
    risk?: string
  }
  start_line?: number
  end_line?: number
  errors?: string[]
  status: DecisionStatus | string
  status_note?: string
  confirm_ref?: string
  answer?: { options: string[]; note?: string; custom?: string; by?: string }
  answered_at?: string
}

/**
 * 把正文里切出来的决策段与服务端对齐后的决策视图配起来：按 card.id 配，同一个 id 出现多次时按出现顺序一一对应
 * （服务端对重复 id 只认第一张，后面的会是「无效」视图）。配不上的段返回 undefined（界面显示成只读）。
 */
export function matchDecisionViews(
  segments: ReadonlyArray<{ kind: string; card?: { id: string } }>,
  views: ReadonlyArray<DocDecisionView> | null | undefined,
): Array<DocDecisionView | undefined> {
  const byId = new Map<string, DocDecisionView[]>()
  for (const v of views ?? []) {
    const id = v.card?.id ?? ''
    const list = byId.get(id)
    if (list) list.push(v)
    else byId.set(id, [v])
  }
  const used = new Map<string, number>()
  return segments.map(seg => {
    if (seg.kind !== 'decision') return undefined
    const id = seg.card?.id ?? ''
    const n = used.get(id) ?? 0
    used.set(id, n + 1)
    return byId.get(id)?.[n]
  })
}

/** 这张决策卡现在能不能作答：围栏合法、已有对应的确认项、且还在待处理。 */
export function decisionAnswerable(v: DocDecisionView | undefined, historic = false): boolean {
  if (!v || historic) return false
  if (v.errors && v.errors.length > 0) return false
  return v.status === 'pending' && !!v.confirm_ref
}

export interface DecisionSummary {
  total: number
  pending: number
  answered: number
  /** 可以「一键采用推荐」的待决策（非 L2、有推荐选项）。 */
  adoptable: DocDecisionView[]
}

export function summarizeDecisions(views: ReadonlyArray<DocDecisionView> | null | undefined, historic = false): DecisionSummary {
  const out: DecisionSummary = { total: 0, pending: 0, answered: 0, adoptable: [] }
  for (const v of views ?? []) {
    out.total++
    if (v.status === 'answered') out.answered++
    if (!decisionAnswerable(v, historic)) continue
    out.pending++
    const risk = (v.card.risk || 'L1') as ConfirmRisk
    if (canAdoptRecommended({ options: v.card.options, multi: !!v.card.multi, risk })) out.adoptable.push(v)
  }
  return out
}

/** 决策卡上的「采用推荐」预览行（给文档页顶部的确认对话框用）。 */
export function decisionAdoptPreview(views: ReadonlyArray<DocDecisionView>): AdoptPreviewRow[] {
  return views.map(v => ({
    ref: v.confirm_ref || '',
    title: v.card.title,
    kind: 'decision' as const,
    choice: recommendedIds(v.card.options, !!v.card.multi).map(id => optionLabel(v.card.options, id)).join('、'),
  }))
}

/** 决策卡已答复时的一句话。 */
export function decisionAnswerText(v: DocDecisionView): string {
  return answerText({ options: v.card.options, answer: v.answer ? { options: v.answer.options, note: v.answer.note, custom: v.answer.custom, by: v.answer.by || 'human' } : undefined })
}
