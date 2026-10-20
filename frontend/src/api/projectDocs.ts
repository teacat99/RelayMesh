import { api } from './client'
import type { DocDecisionView } from '../lib/confirms'

export interface ProjectDoc {
  id: number
  workflow_id: string
  seq: number
  path: string
  title: string
  kind: string
  status: string
  tags: string[]
  // 列表接口默认不带正文（服务端 body_omitted=true，这里是空串，不代表文档为空）；要正文用 get() 取单篇，或给 list 传 { with_body: '1' }（#177）。
  body?: string
  rev: number
  hash: string
  updated_at: string
  project_id?: number
  module_path?: string
  /** md | mermaid | html | data；html 是沙箱里运行的 Artifact */
  render?: string
}

export interface DocWorkflowStat {
  workflow_id: string
  total: number
  updated_at: string
}

/** 链接关系的一端：出链指向的文档（exists=false 表示目标还没建），或反链来自的文档。 */
export interface DocLinkRef {
  target?: string
  id?: number
  workflow_id?: string
  path: string
  title?: string
  exists: boolean
}

export interface DocLinks {
  outlinks: DocLinkRef[]
  backlinks: DocLinkRef[]
}

export interface DocDetail {
  doc: ProjectDoc
  body: string
  headings: { level: number; text: string; anchor: string }[]
  /** 文档里的决策卡与确认项对齐后的视图（状态、答复、对应的确认项编号）。 */
  decisions: DocDecisionView[]
  /** true 表示这是历史修订的正文：决策卡只读，不能作答。 */
  historic?: boolean
}

function docContentURL(workflowId: string) {
  return `/workflows/${encodeURIComponent(workflowId)}/docs/content`
}

export const projectDocsApi = {
  async list(workflowId: string, params?: Record<string, string>) {
    const { data } = await api.get<{ items: ProjectDoc[]; workflows: DocWorkflowStat[]; body_omitted?: boolean }>(
      `/workflows/${encodeURIComponent(workflowId)}/docs`,
      { params },
    )
    return data
  },
  async get(workflowId: string, docRef: string, rev?: number) {
    const { data } = await api.get<DocDetail>(docContentURL(workflowId), {
      params: { path: docRef, ...(rev ? { rev } : {}) },
    })
    return data
  },
  async create(workflowId: string, body: Record<string, unknown>) {
    const { data } = await api.post(`/workflows/${encodeURIComponent(workflowId)}/docs`, body)
    return data
  },
  async update(workflowId: string, docRef: string, body: Record<string, unknown>) {
    const { data } = await api.put(docContentURL(workflowId), body, { params: { path: docRef } })
    return data
  },
  async search(workflowId: string, q: string) {
    const { data } = await api.get<{ hits: { path: string; title: string; snippet: string; anchor?: string }[] }>(
      `/workflows/${encodeURIComponent(workflowId)}/docs/search`,
      { params: { q } },
    )
    return data
  },
  async revisions(workflowId: string, docRef: string) {
    const { data } = await api.get<{ revisions: { rev: number; summary: string; actor: string; created_at: string }[] }>(
      `${docContentURL(workflowId)}/revisions`,
      { params: { path: docRef } },
    )
    return data
  },
  async restore(workflowId: string, docRef: string, rev: number) {
    const { data } = await api.post(`${docContentURL(workflowId)}/revisions/${rev}/restore`, null, {
      params: { path: docRef },
    })
    return data
  },
  async comment(workflowId: string, docRef: string, anchor: string, text: string) {
    const { data } = await api.post(
      `${docContentURL(workflowId)}/comments`,
      { anchor, text },
      { params: { path: docRef } },
    )
    return data
  },
  async blueprint(workflowId: string, withTemplates = false) {
    const { data } = await api.get<{ items: { path: string; title: string; hint: string; present: boolean; template?: string }[]; gaps: number }>(
      `/workflows/${encodeURIComponent(workflowId)}/docs/blueprint`,
      { params: { with_templates: withTemplates ? '1' : '0' } },
    )
    return data
  },
  async links(workflowId: string, docRef: string) {
    const { data } = await api.get<DocLinks>(`${docContentURL(workflowId)}/links`, { params: { path: docRef } })
    return data
  },
  exportZipUrl(workflowId: string) {
    return `/api/v1/workflows/${encodeURIComponent(workflowId)}/docs/export.zip`
  },
}
