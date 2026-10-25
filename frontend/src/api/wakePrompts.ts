// 唤醒词模板的 REST 封装（对应 internal/api/wake_prompts.go，#232 D3，D-376）。
// 会话自己的模板（续聊 / 新开对话）读写走 workflowPromptsApi（/workflows/:id/prompts 的 wake_resume / wake_new）；
// 全局覆盖随全局设置保存（settings.wakeTemplates）。这里只有「内置默认与变量表」和「预览」两个只读接口。

import { api } from './client'
import { workErrorMessage } from './workItems'

/** 唤醒词的三个出口。 */
export type WakeKind = 'resume' | 'new' | 'web_start'

/** 预览结果里的模板来源：preset 预设正文、session 会话模板、global 全局覆盖、builtin 内置默认、draft 草稿。 */
export type WakeLayer = 'preset' | 'session' | 'global' | 'builtin' | 'draft'

/** 一类唤醒词的说明与内置默认。 */
export interface WakeKindInfo {
  kind: WakeKind
  label: string
  /** 一句话说明这类唤醒词在什么时候发给谁。 */
  desc: string
  /** 内置默认（带 %变量，未渲染）。 */
  template: string
  /** 预设没配工作流时的写法；与 template 相同时服务端省略。 */
  template_no_workflow?: string
  /** 会话能不能给这一类写自己的模板。 */
  session: boolean
  /** 模板必须带 %workflow。 */
  needs_workflow: boolean
}

/** 一个变量（不含 %）。live = 「现状」变量：发出那一刻由 Hub 现算。 */
export interface WakeVarInfo {
  name: string
  desc: string
  live?: boolean
}

export interface WakeDefaults {
  kinds: WakeKindInfo[]
  vars: WakeVarInfo[]
  /** 每类模板的字数上限。 */
  template_max: number
}

export interface WakePreviewRequest {
  kind: WakeKind
  /** 带上：用这个会话此刻的真实现状渲染；不带：用示例值。 */
  workflow_id?: string
  /** 带上：渲染这份草稿（同样要过保存时的校验，不合规 400）；不带：渲染分层选出来的那一份。 */
  template?: string
  /** 与 template 一起用：template 是启动器预设（或会话页运行配置）里自己写的正文，不是某一层的模板草稿——排在所有分层最前面（layer=preset），只按预设的规则校验。 */
  preset?: boolean
}

export interface WakePreview {
  /** 最终发出去的文字。 */
  text: string
  layer: WakeLayer | string
  /** 实际渲染的模板（未展开变量）。 */
  template: string
  length: number
  /** 用的是示例值（没带 workflow_id）。 */
  sample: boolean
}

export const wakePromptsApi = {
  async defaults(): Promise<WakeDefaults> {
    const { data } = await api.get<WakeDefaults>('/wake-prompts/defaults')
    return data
  },

  async preview(req: WakePreviewRequest): Promise<WakePreview> {
    const { data } = await api.post<WakePreview>('/wake-prompts/preview', req)
    return data
  },
}

/** 把接口错误翻译成可直接展示的话。 */
export function wakeErrorMessage(err: unknown, fallback = '操作失败，请稍后重试'): string {
  return workErrorMessage(err, fallback)
}
