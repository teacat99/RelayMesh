// HTML Artifact 的 REST 封装（#114 4b，对应 internal/api/artifacts.go）：渲染票据、数据键值、评论。

import { api } from './client'

export interface ArtifactDataItem {
  doc_id: number
  key: string
  /** 合法 JSON 文本 */
  value: string
  version: number
  updated_by: string
  updated_at: string
}

export interface ArtifactComment {
  id: number
  doc_id: number
  anchor?: string
  body: string
  author_kind: string
  author_label: string
  resolved: boolean
  resolved_at?: string | null
  created_at: string
}

/** 聊天引用卡片 / 右侧预览用的摘要；非 HTML 文档带正文。 */
export interface ArtifactSummary {
  id: number
  title: string
  path: string
  kind: string
  render: string
  rev: number
  workflow_id: string
  project_id: number
  module_path?: string
  updated_at: string
  open_comments: number
  body?: string
}

const base = (id: number) => `/artifacts/${id}`

export const artifactsApi = {
  async summary(docId: number): Promise<ArtifactSummary> {
    const { data } = await api.get<ArtifactSummary>(base(docId))
    return data
  },
  /** 渲染页的一次性地址（iframe 带不了请求头，票据绑定文档、单次有效）。 */
  async renderURL(docId: number): Promise<string> {
    const { data } = await api.post<{ url: string }>(`${base(docId)}/ticket`)
    return data.url
  },
  async dataList(docId: number): Promise<ArtifactDataItem[]> {
    const { data } = await api.get<{ items: ArtifactDataItem[] }>(`${base(docId)}/data`)
    return data.items ?? []
  },
  async dataGet(docId: number, key: string): Promise<ArtifactDataItem> {
    const { data } = await api.get<{ item: ArtifactDataItem }>(`${base(docId)}/data/${encodeURIComponent(key)}`)
    return data.item
  },
  async dataSet(docId: number, key: string, value: unknown, expectedVersion?: number): Promise<ArtifactDataItem> {
    const { data } = await api.put<{ item: ArtifactDataItem }>(`${base(docId)}/data/${encodeURIComponent(key)}`, {
      value: value === undefined ? null : value,
      ...(expectedVersion === undefined ? {} : { expected_version: expectedVersion }),
    })
    return data.item
  },
  async dataDelete(docId: number, key: string): Promise<boolean> {
    const { data } = await api.delete<{ deleted: boolean }>(`${base(docId)}/data/${encodeURIComponent(key)}`)
    return data.deleted
  },
  async comments(docId: number, includeResolved = false): Promise<ArtifactComment[]> {
    const { data } = await api.get<{ items: ArtifactComment[] }>(`${base(docId)}/comments`, { params: includeResolved ? { resolved: 1 } : {} })
    return data.items ?? []
  },
  async postComment(docId: number, anchor: string, text: string): Promise<ArtifactComment> {
    const { data } = await api.post<{ item: ArtifactComment }>(`${base(docId)}/comments`, { anchor, text })
    return data.item
  },
  async resolveComment(docId: number, commentId: number, resolved = true): Promise<ArtifactComment> {
    const { data } = await api.post<{ item: ArtifactComment }>(`${base(docId)}/comments/${commentId}/resolve`, { resolved })
    return data.item
  },
}
