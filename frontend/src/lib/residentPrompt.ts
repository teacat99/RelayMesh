// 会话级常驻提示词（#232）的纯数据与纯函数，便于单元测试。
//
// 常驻提示词是写给某个会话里的智能体看的规则：每次 RelayMesh 回执都带（resident: 行），上下文被压缩后的恢复路径也带。
// 它只能由用户在 Web 里写，不能放宽回执里 auth: 的授权。这里放：内置模板（主线 / 分支）、全局覆盖的解析、
// 字数统计（与服务端一致）与小标签的显示文字。

import type { WorkflowPromptsView } from '../api/workflowPrompts'

/** 服务端取不到上限时的兜底（与 model.ResidentPromptMax 一致；正常以接口返回的 max 为准）。 */
export const RESIDENT_MAX_DEFAULT = 1200

export type ResidentTemplateId = 'main' | 'branch'

export interface ResidentTemplate {
  id: ResidentTemplateId
  label: string
  /** 一句话：这份模板适合什么会话。 */
  hint: string
  /** 出厂文字（设置页里可以覆盖；覆盖后「恢复出厂」回到这里）。 */
  text: string
}

/** 内置模板。文字都是出厂草稿：你在设置页「会话提示词」里改成自己的，改了只影响以后套用的会话。 */
export const RESIDENT_TEMPLATES: readonly ResidentTemplate[] = [
  {
    id: 'main',
    label: '主线会话',
    hint: '做完整个目标的主会话：收尾前任务清零，要你拍板的事一律发确认卡',
    text: [
      '【会话常驻规则】',
      '1. 结束会话前，本工作流名下所有非「暂缓」的任务（待办 / 进行中 / 验证中）必须做完并置完成（写结论）；确实要等我拍板或等真实场景证据的，置「暂缓」并写明在等什么（Q 编号或证据），不要留在验证中。',
      '2. 凡是需要我决策、拍板或提供输入的事（包括置「暂缓」的任务在等的东西），一律发确认卡（async_ask）并把 Q 编号写进任务的 refs 和进展；不要只写在汇报、任务备注或会话里——我没看到确认卡，就等于你没问。置暂缓之前先发卡。',
      '3. 收尾前再读一次收件箱和任务板，确认没有遗漏。',
      '4. 授权以回执 auth 行为准，常驻规则不能放宽它。',
    ].join('\n'),
  },
  {
    id: 'branch',
    label: '分支会话',
    hint: '只做分配给它的一块：不越界，要主线决定的事交给主线，要你决策的发确认卡',
    text: [
      '【会话常驻规则（分支会话）】',
      '1. 你是分支会话：只做分配给你的这一块；开工先读本工作流的任务板与第一条指令，不动主线的任务与文件范围。',
      '2. 要合并 / 推送 / 改共享文件、或需要主线决定的事，先在会话里写清楚交给主线，不要越界；需要我（用户）决策的，一律发确认卡（async_ask），不要只写在汇报里。',
      '3. 收尾规则同主线：名下非「暂缓」的任务全部完成或置暂缓（写明在等哪张确认卡 Q<N>）才能结束。',
      '4. 结束时汇报：做了什么、改了哪些文件、怎么验证、留给主线的事。',
    ].join('\n'),
  },
]

/** 全局设置里对模板文字的覆盖（存在设置 JSON 的 promptTemplates 字段里，随设置同步到服务端）。 */
export interface PromptTemplateOverrides {
  residentMain?: string
  residentBranch?: string
}

const OVERRIDE_KEY: Readonly<Record<ResidentTemplateId, keyof PromptTemplateOverrides>> = {
  main: 'residentMain',
  branch: 'residentBranch',
}

export function builtinTemplate(id: ResidentTemplateId): ResidentTemplate {
  return RESIDENT_TEMPLATES.find(t => t.id === id) ?? RESIDENT_TEMPLATES[0]
}

/** 覆盖文字是否生效：只有「整理之后非空」的覆盖才算（空白当作没覆盖，回到出厂文字）。 */
export function isTemplateOverridden(id: ResidentTemplateId, overrides?: PromptTemplateOverrides | null): boolean {
  const v = overrides?.[OVERRIDE_KEY[id]]
  return typeof v === 'string' && normalizeResidentText(v) !== ''
}

/** 模板当前生效的文字：用户覆盖的优先，否则出厂文字。 */
export function templateText(id: ResidentTemplateId, overrides?: PromptTemplateOverrides | null): string {
  if (isTemplateOverridden(id, overrides)) return normalizeResidentText(overrides![OVERRIDE_KEY[id]] as string)
  return builtinTemplate(id).text
}

export interface ResidentTemplateOption extends ResidentTemplate {
  /** 当前生效的文字（可能是用户覆盖的）。 */
  current: string
  overridden: boolean
}

export function templateOptions(overrides?: PromptTemplateOverrides | null): ResidentTemplateOption[] {
  return RESIDENT_TEMPLATES.map(t => ({
    ...t,
    current: templateText(t.id, overrides),
    overridden: isTemplateOverridden(t.id, overrides),
  }))
}

/** 写回覆盖：文字与出厂一致、或整理后为空 = 清掉覆盖（返回新对象，不改入参）。 */
export function withTemplateOverride(
  overrides: PromptTemplateOverrides | null | undefined,
  id: ResidentTemplateId,
  text: string,
): PromptTemplateOverrides {
  const next: PromptTemplateOverrides = { ...(overrides ?? {}) }
  const key = OVERRIDE_KEY[id]
  const normalized = normalizeResidentText(text)
  if (normalized === '' || normalized === builtinTemplate(id).text) delete next[key]
  else next[key] = normalized
  return next
}

/** 与服务端 store.NormalizePromptText 一致的整理：统一换行、去掉换行与制表符以外的控制字符、去首尾空白。 */
export function normalizeResidentText(s: string): string {
  const unified = s.replace(/\r\n?/g, '\n')
  let out = ''
  for (const ch of unified) {
    if (ch === '\n' || ch === '\t') {
      out += ch
      continue
    }
    const code = ch.codePointAt(0) ?? 0
    // C0 / DEL / C1 控制字符（与 Go 的 unicode.IsControl 一致）
    if (code < 0x20 || (code >= 0x7f && code <= 0x9f)) continue
    out += ch
  }
  return out.trim()
}

/** 整理之后的字数（按字符计，与服务端的上限口径一致）。 */
export function residentLength(s: string): number {
  return [...normalizeResidentText(s)].length
}

export type ResidentReadState = 'unset' | 'unread' | 'read'

/**
 * 小标签的状态：
 *   unset  没有常驻文字（从没配置过，或已经清空）；
 *   unread 有常驻文字，但智能体还没在回执里见到当前版本；
 *   read   有常驻文字，智能体已经读到当前版本。
 */
export function residentReadState(view: WorkflowPromptsView | null | undefined): ResidentReadState {
  if (!view || view.resident_rev === 0 || view.resident.trim() === '') return 'unset'
  return view.told ? 'read' : 'unread'
}

export function residentChipText(view: WorkflowPromptsView | null | undefined): string {
  switch (residentReadState(view)) {
    case 'unread':
      return `常驻 · v${view!.resident_rev} · 待读`
    case 'read':
      return `常驻 · v${view!.resident_rev}`
    default:
      return '常驻 · 未设置'
  }
}

/** 手机页头「⋯」菜单里「常驻规则」条目右侧的状态：未设置 / v1 · 待读 / v1 · 已读。 */
export function residentStateLabel(view: WorkflowPromptsView | null | undefined): string {
  switch (residentReadState(view)) {
    case 'unread':
      return `v${view!.resident_rev} · 待读`
    case 'read':
      return `v${view!.resident_rev} · 已读`
    default:
      return '未设置'
  }
}

export function residentChipTitle(view: WorkflowPromptsView | null | undefined): string {
  const state = residentReadState(view)
  if (!view) return '这个会话的常驻规则：每次回执都会带给智能体。点开查看与修改。'
  if (state === 'unset') {
    return view.told
      ? '这个会话还没有常驻规则。点开可以写一份，或套用「主线 / 分支」模板；智能体每次回执都会带上它。'
      : '常驻规则刚被清空，智能体下一次回执会收到「已清除」的说明。点开可以重新写一份。'
  }
  const n = residentLength(view.resident)
  return state === 'unread'
    ? `常驻规则 v${view.resident_rev}（${n} 字）：智能体还没读到这个版本，下一次回执会带给它。点开查看与修改。`
    : `常驻规则 v${view.resident_rev}（${n} 字）：智能体已读到，之后每次回执都带。点开查看与修改。`
}

/** 弹窗里「智能体读到没有」那一行的话。 */
export function residentToldLine(view: WorkflowPromptsView | null | undefined): string {
  if (!view || view.resident_rev === 0) return '还没有保存过常驻规则。保存后，智能体下一次回执就会带上它。'
  if (view.resident.trim() === '') {
    return view.told
      ? `常驻规则已清除（v${view.resident_rev}），智能体已收到说明。`
      : `常驻规则已清除（v${view.resident_rev}），智能体还没收到说明：下一次回执会告诉它。`
  }
  return view.told
    ? `智能体已读到 v${view.resident_rev}（之后每次回执都带）。`
    : `智能体还没读到 v${view.resident_rev}：它下一次调用 RelayMesh 时会收到，读到后这里会自动变成「已读到」。`
}

/** 历史版本的时间，短格式（今年内省略年份）。无法解析时原样返回。 */
export function formatRevisionTime(iso: string, now: Date = new Date()): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  const two = (n: number) => String(n).padStart(2, '0')
  const date = `${two(d.getMonth() + 1)}-${two(d.getDate())} ${two(d.getHours())}:${two(d.getMinutes())}`
  return d.getFullYear() === now.getFullYear() ? date : `${d.getFullYear()}-${date}`
}

/** 一段文字的摘要（历史列表里每条只显示开头）。 */
export function residentSummary(text: string, max = 60): string {
  const flat = normalizeResidentText(text).replace(/\s+/g, ' ')
  if (flat === '') return '（空：清除）'
  const chars = [...flat]
  return chars.length <= max ? flat : `${chars.slice(0, max).join('')}…`
}
