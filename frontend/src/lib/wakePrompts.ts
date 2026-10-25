// 唤醒词模板（#232 D3，D-376）的纯数据与纯函数，便于单元测试。
//
// Hub 给智能体发「唤醒词」有三个出口：续聊（非交互接着原对话）、新开对话（无人值守开新对话）、
// 网页启动提示词（网页新建会话时给你转发的那段）。（原来的第四个出口「键入唤醒」随交互式预设下线，#245。）模板分层，谁在前谁生效：
//   预设里自己写的正文 > 会话模板（会话页「常驻」弹窗的「唤醒词」页签）> 全局覆盖（设置 › 会话提示词）> 内置默认。
// 这里放：来源名称、变量清单的筛选、与服务端一致的校验与字数统计、在光标处插入变量的算法。

import type { WakeKind, WakeVarInfo } from '../api/wakePrompts'

/** 服务端取不到上限时的兜底（与 sessionprompt.TemplateMaxRunes 一致；正常以接口返回的 template_max 为准）。 */
export const WAKE_TEMPLATE_MAX_DEFAULT = 1500

/** 全局覆盖里每一类模板对应的字段名（存在设置 JSON 的 wakeTemplates 里）。 */
export type WakeTemplateKey = 'resume' | 'new' | 'webStart'

/** 全局设置里对唤醒词模板的覆盖。空串或缺省 = 没覆盖，用内置默认。 */
export interface WakeTemplateOverrides {
  resume?: string
  new?: string
  webStart?: string
}

export const WAKE_KINDS: readonly WakeKind[] = ['resume', 'new', 'web_start']

const KEY_OF_KIND: Readonly<Record<WakeKind, WakeTemplateKey>> = {
  resume: 'resume',
  new: 'new',
  web_start: 'webStart',
}

export function wakeKeyOf(kind: WakeKind): WakeTemplateKey {
  return KEY_OF_KIND[kind]
}

const KIND_LABEL: Readonly<Record<WakeKind, string>> = {
  resume: '续聊',
  new: '新开对话',
  web_start: '网页启动提示词',
}

export function wakeKindLabel(kind: WakeKind): string {
  return KIND_LABEL[kind] ?? kind
}

const LAYER_LABEL: Readonly<Record<string, string>> = {
  preset: '预设里写的正文',
  session: '会话模板',
  global: '全局覆盖',
  builtin: '内置默认',
  draft: '你正在写的草稿',
}

/** 模板来源（分层）的中文名。 */
export function wakeLayerLabel(layer: string): string {
  return LAYER_LABEL[layer] ?? layer
}

/** 来源的一句话说明（悬浮提示用）。 */
export function wakeLayerHint(layer: string): string {
  switch (layer) {
    case 'preset':
      return '启动器预设里自己写了提示词：它优先于一切模板'
    case 'session':
      return '这个会话自己写的模板（会话页「常驻」弹窗的「唤醒词」页签）'
    case 'global':
      return '设置 › 会话提示词 里写的全局模板，所有没有自己模板的会话都用它'
    case 'builtin':
      return 'Hub 自带的默认文字，你什么都没写时用它'
    case 'draft':
      return '还没保存的文字，只用来预览'
    default:
      return ''
  }
}

/** 渲染时「现状」类变量的说明徽标：发出那一刻由 Hub 现算。 */
export function wakeVarLiveNote(v: WakeVarInfo): string {
  return v.live ? '发出那一刻由 Hub 现算' : ''
}

const COMMON_VARS = ['workflow', 'title', 'project', 'host', 'mode', 'auth', 'phase', 'resident', 'inbox', 'tasks', 'next', 'anchor']

/** 每一类模板里能用的变量名（顺序即界面显示顺序；不在清单里的 %xxx 原样保留，不会报错）。 */
export function wakeVarNames(kind: WakeKind): string[] {
  switch (kind) {
    case 'resume':
    case 'new':
      return [...COMMON_VARS, 'limit']
    case 'web_start':
      return ['workflow', 'title', 'project']
  }
  return []
}

/** 从服务端的完整变量表里筛出这一类能用的，保持服务端给的顺序。 */
export function wakeVarsFor(kind: WakeKind, all: readonly WakeVarInfo[]): WakeVarInfo[] {
  const allow = new Set(wakeVarNames(kind))
  return all.filter(v => allow.has(v.name))
}

/** 与服务端 NormalizePromptText 一致的整理：统一换行、去掉除换行 / 制表符外的控制字符、去首尾空白。 */
export function normalizeWakeText(text: string): string {
  // eslint-disable-next-line no-control-regex
  return text.replace(/\r\n?/g, '\n').replace(/[\u0000-\u0008\u000b-\u001f\u007f-\u009f]/g, '').trim()
}

/** 字数（按字符数，与服务端一致）。 */
export function wakeLength(text: string): number {
  return Array.from(normalizeWakeText(text)).length
}

/**
 * 保存前的本地检查，返回要展示的问题（空串 = 没问题）。规则与服务端 store.NormalizeWakeTemplate 一致：
 * 字数上限；新开对话与网页启动提示词必须带 %workflow（智能体靠这个编号接入会话，缺了它就接不进来）。
 * 本地检查只是为了即时提示，最终以服务端为准。
 */
export function wakeTemplateProblem(kind: WakeKind, text: string, max = WAKE_TEMPLATE_MAX_DEFAULT): string {
  const n = wakeLength(text)
  if (n > max) return `最多 ${max} 个字，现在是 ${n} 个字，请删减后再保存`
  if (n > 0 && (kind === 'new' || kind === 'web_start') && !text.includes('%workflow')) {
    return `${wakeKindLabel(kind)}模板必须包含 %workflow：智能体靠这个编号接入会话，缺了它就接不进来`
  }
  return ''
}

/** 在文本框的光标处（或选区）插入 %变量，返回新文本与插入后的光标位置。选区非空时替换选区。 */
export function insertVarAt(text: string, selStart: number, selEnd: number, name: string): { text: string; caret: number } {
  const a = Math.max(0, Math.min(selStart, text.length))
  const b = Math.max(a, Math.min(selEnd, text.length))
  const ins = '%' + name
  // 变量名后面紧跟字母 / 数字 / 下划线会被识别成更长的变量（如 %inbox 后接 x）：需要时补一个空格隔开
  const after = text.slice(b, b + 1)
  const pad = /[A-Za-z0-9_]/.test(after) ? ' ' : ''
  const out = text.slice(0, a) + ins + pad + text.slice(b)
  return { text: out, caret: a + ins.length }
}

/** 这一类的草稿与已保存的文字有没有区别（按服务端的整理方式比较）。 */
export function wakeDirty(draft: string, saved: string): boolean {
  return normalizeWakeText(draft) !== normalizeWakeText(saved)
}

/** 读出全局覆盖里某一类的文字（没有 = 空串）。 */
export function wakeOverrideOf(o: WakeTemplateOverrides | undefined, kind: WakeKind): string {
  return (o?.[wakeKeyOf(kind)] ?? '').trim() === '' ? '' : (o?.[wakeKeyOf(kind)] ?? '')
}

/** 写入（或清除）全局覆盖里某一类，返回新对象；全部为空时返回 undefined（设置里不留空壳）。 */
export function withWakeOverride(o: WakeTemplateOverrides | undefined, kind: WakeKind, text: string): WakeTemplateOverrides | undefined {
  const next: WakeTemplateOverrides = { ...(o ?? {}) }
  const v = normalizeWakeText(text)
  if (v === '') delete next[wakeKeyOf(kind)]
  else next[wakeKeyOf(kind)] = v
  return Object.keys(next).length === 0 ? undefined : next
}

/** 渲染结果里「会合并成一行」的说明：续聊 / 新开对话的提示词是命令行里的一个参数，不能含换行。 */
export function wakeFlattenNote(kind: WakeKind): string {
  switch (kind) {
    case 'resume':
    case 'new':
      return '分行写的模板，发出时会合并成一行（命令行里的一个参数不能含换行）。'
    default:
      return ''
  }
}
