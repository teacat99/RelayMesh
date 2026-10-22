// 项目知识库的 REST 封装（#114，对应 internal/api/projects.go）。只有登录的「人」能调用。

import { api } from './client'
import { workErrorMessage } from './workItems'

export type ACLMode = 'off' | 'audit' | 'enforce'

export interface ProjectStats {
  dirs: number
  modules: number
  docs: number
  workflows: number
}

export interface Project {
  id: number
  slug: string
  name: string
  description?: string
  acl_mode: ACLMode
  created_at: string
  updated_at: string
  archived_at?: string | null
  stats?: ProjectStats
}

export interface ProjectDir {
  id: number
  project_id: number
  host_name: string
  project_directory: string
  branch_hint?: string
  added_by: string
  added_at: string
}

export interface ProjectModule {
  id: number
  project_id: number
  path: string
  title: string
  description?: string
  owner_identity_id?: number
  created_at: string
}

export interface ProjectDetail {
  project: Project
  dirs: ProjectDir[]
  modules: ProjectModule[]
  workflows: string[]
}

export interface ProjectSuggestion {
  host_name: string
  project_directory: string
  suggested_name: string
  suggested_slug?: string
  workflows: string[]
  docs: number
}

export interface MigrationMove {
  doc_id: number
  workflow_id: string
  from_path: string
  to_path: string
  reason: 'sheet' | 'conflict' | string
}

/** 迁移预览里的每一篇待并入文档（路径不变的也在内；reason 为空 = 路径不变）。 */
export interface MigrationItem {
  doc_id: number
  workflow_id: string
  from_path: string
  to_path: string
  title: string
  reason?: 'sheet' | 'conflict' | string
}

export interface MigrationReport {
  project_id: number
  workflows: string[]
  docs: number
  assigned: number
  moves: MigrationMove[]
  /** 全部待并入文档（含路径不变的）；老后端没有这个字段。 */
  items?: MigrationItem[]
  already: number
  unplaceable?: string[]
  hash: string
  applied: boolean
}

/** 项目里一篇文档的元数据（不含正文）。 */
export interface KbDoc {
  id: number
  workflow_id: string
  path: string
  title: string
  kind: string
  status: string
  render?: string
  updated_at: string
  tags?: string[]
  module_path?: string
}

export interface KbSearchHit {
  path: string
  title: string
  score: number
  snippet: string
  anchor?: string
  doc_id: number
  workflow_id: string
  module_path?: string
  kind: string
  status: string
  render?: string
  updated_at: string
}

export interface ModuleTask {
  workflow_id: string
  seq: number
  title: string
  state: string
}

export interface ModulePageData {
  module: ProjectModule
  children: ProjectModule[]
  owner_label?: string
  index?: KbDoc | null
  design?: KbDoc | null
  decisions: KbDoc[]
  artifacts: KbDoc[]
  docs: KbDoc[]
  recent: KbDoc[]
  tasks: ModuleTask[]
}

export interface BlueprintItem {
  path: string
  title: string
  hint: string
  present: boolean
  suggested_kind?: string
  template?: string
}

export type GrantRole = 'read' | 'comment' | 'edit'
// tasks = 项目管理 Agent（#175 B2c）：scope_ref 就是项目编号，角色只能是 edit，只能由用户在「任务管理」里授予。
export type GrantScopeKind = 'project' | 'module' | 'doc' | 'workflow' | 'tasks'

export interface AclGrant {
  id: number
  project_id: number
  principal_kind: 'identity' | 'group'
  principal_ref: string
  scope_kind: GrantScopeKind
  scope_ref: string
  role: GrantRole | 'manage'
  granted_by_kind: string
  granted_by_ref: string
  granted_by_label?: string
  expires_at?: string | null
  created_at: string
  revoked_at?: string | null
  note?: string
}

export interface AuditEvent {
  id: number
  at: string
  project_id?: number
  actor_kind: string
  actor_ref: string
  actor_label?: string
  action: string
  scope_kind?: string
  scope_ref?: string
  target?: string
  result: 'allow' | 'deny' | 'assumed' | string
  detail?: string
}

export interface AuditQuery {
  project?: number
  actor?: string
  action?: string
  result?: string
  scope?: string
  since?: string
  until?: string
  before?: number
  limit?: number
}

function clean<T extends Record<string, unknown>>(q: T): Record<string, string | number> {
  const out: Record<string, string | number> = {}
  for (const [k, v] of Object.entries(q)) {
    if (v === undefined || v === null || v === '' || v === 0) continue
    out[k] = v as string | number
  }
  return out
}

export const projectsApi = {
  async list(archived = false): Promise<Project[]> {
    const { data } = await api.get<{ items: Project[] }>('/projects', { params: archived ? { archived: 1 } : undefined })
    return data?.items ?? []
  },
  async get(id: number): Promise<ProjectDetail> {
    const { data } = await api.get<ProjectDetail>(`/projects/${id}`)
    return data
  },
  async create(body: {
    name: string
    slug?: string
    description?: string
    acl_mode?: ACLMode
    dirs?: { host_name: string; project_directory: string; branch_hint?: string }[]
  }): Promise<{ project: Project; dir_errors?: string[] }> {
    const { data } = await api.post<{ project: Project; dir_errors?: string[] }>('/projects', body)
    return data
  },
  async update(id: number, body: { name?: string; description?: string; acl_mode?: ACLMode }): Promise<Project> {
    const { data } = await api.put<{ project: Project }>(`/projects/${id}`, body)
    return data.project
  },
  async archive(id: number): Promise<void> {
    await api.delete(`/projects/${id}`)
  },
  async suggestions(): Promise<ProjectSuggestion[]> {
    const { data } = await api.get<{ items: ProjectSuggestion[] }>('/projects/suggestions')
    return data?.items ?? []
  },
  async addDir(id: number, body: { host_name: string; project_directory: string; branch_hint?: string }): Promise<ProjectDir> {
    const { data } = await api.post<{ dir: ProjectDir }>(`/projects/${id}/dirs`, body)
    return data.dir
  },
  async removeDir(id: number, dirId: number): Promise<void> {
    await api.delete(`/projects/${id}/dirs/${dirId}`)
  },
  async upsertModule(id: number, body: { path: string; title: string; description?: string; owner_identity_id?: number }): Promise<ProjectModule> {
    const { data } = await api.put<{ module: ProjectModule }>(`/projects/${id}/modules`, body)
    return data.module
  },
  async deleteModule(id: number, path: string): Promise<void> {
    await api.delete(`/projects/${id}/modules`, { params: { path } })
  },
  async migrationDryRun(id: number): Promise<MigrationReport> {
    const { data } = await api.get<{ report: MigrationReport }>(`/projects/${id}/migration`)
    return data.report
  },
  async migrationApply(id: number, hash: string): Promise<MigrationReport> {
    const { data } = await api.post<{ report: MigrationReport }>(`/projects/${id}/migration`, { hash })
    return data.report
  },
  async exportZip(id: number): Promise<Blob> {
    const { data } = await api.get<Blob>(`/projects/${id}/docs/export.zip`, { responseType: 'blob' })
    return data
  },
  async docs(id: number, params?: { module?: string; kind?: string; status?: string; tag?: string; render?: string }): Promise<KbDoc[]> {
    const { data } = await api.get<{ items: KbDoc[] }>(`/projects/${id}/docs`, { params: params ? clean(params) : undefined })
    return data?.items ?? []
  },
  async search(id: number, params: { q?: string; module?: string; kind?: string; status?: string; tag?: string; render?: string; since?: string; limit?: number }): Promise<KbSearchHit[]> {
    const { data } = await api.get<{ items: KbSearchHit[] }>(`/projects/${id}/docs/search`, { params: clean(params) })
    return data?.items ?? []
  },
  async modulePage(id: number, path: string): Promise<ModulePageData> {
    const { data } = await api.get<{ page: ModulePageData }>(`/projects/${id}/module-page`, { params: { path } })
    return data.page
  },
  async blueprint(id: number, templates = false): Promise<{ items: BlueprintItem[]; gaps: number }> {
    const { data } = await api.get<{ items: BlueprintItem[]; gaps: number }>(`/projects/${id}/blueprint`, { params: templates ? { templates: 1 } : undefined })
    return data
  },
  async ofWorkflow(workflowId: string): Promise<Project | null> {
    const { data } = await api.get<{ project: Project | null }>(`/workflows/${encodeURIComponent(workflowId)}/project`)
    return data?.project ?? null
  },
  async grants(id: number, all = false): Promise<AclGrant[]> {
    const { data } = await api.get<{ items: AclGrant[] }>(`/projects/${id}/grants`, { params: all ? { all: 1 } : undefined })
    return data?.items ?? []
  },
  async createGrant(id: number, body: {
    principal_kind: 'identity' | 'group'
    principal_ref: string
    scope_kind: GrantScopeKind
    scope_ref?: string
    role: GrantRole
    expires_hours?: number
    note?: string
  }): Promise<AclGrant> {
    const { data } = await api.post<{ grant: AclGrant }>(`/projects/${id}/grants`, body)
    return data.grant
  },
  async revokeGrant(id: number, grantId: number): Promise<void> {
    await api.delete(`/projects/${id}/grants/${grantId}`)
  },
  async audit(q: AuditQuery): Promise<{ items: AuditEvent[]; next_before: number }> {
    const { data } = await api.get<{ items: AuditEvent[]; next_before: number }>('/audit', { params: clean(q as Record<string, unknown>) })
    return { items: data?.items ?? [], next_before: data?.next_before ?? 0 }
  },
  /** 导出 CSV：带登录凭据取回内容再触发下载（地址栏直接打开没有凭据）。 */
  async auditCsv(q: AuditQuery): Promise<Blob> {
    const { data } = await api.get<Blob>('/audit/export.csv', { params: clean(q as Record<string, unknown>), responseType: 'blob' })
    return data
  },
}

export function projectErrorMessage(err: unknown, fallback = '操作失败，请稍后重试'): string {
  return workErrorMessage(err, fallback)
}
