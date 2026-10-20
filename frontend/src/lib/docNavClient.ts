// docNav 绑上真实接口的那一层：所有「打开一篇文档」的入口都从这里拿 openDoc（#131）。
import type { Router } from 'vue-router'
import { projectsApi } from '../api/projects'
import { createOwnerResolver, openDocWith, type DocTarget } from './docNav'

// 归属项目的名字：跟着解析顺手记下，只给提示用，不参与路由判断。
const ownerNames = new Map<string, string>()

/** 工作流 → 所属项目（0 = 没有归属）；缓存与失败退回老页的规则见 createOwnerResolver。 */
export const ownerProjects = createOwnerResolver(async workflowId => {
  const p = await projectsApi.ofWorkflow(workflowId)
  if (!p || p.id <= 0 || p.archived_at) {
    ownerNames.delete(workflowId)
    return 0
  }
  ownerNames.set(workflowId, p.name)
  return p.id
})

export const ownerProjectId = (workflowId: string) => ownerProjects.resolve(workflowId)
export const ownerProjectIdWithin = (workflowId: string, ms?: number) => ownerProjects.within(workflowId, ms)
/** 最近一次解析出的所属项目名（没解析过 / 没有归属 = 空串）。 */
export const ownerProjectName = (workflowId: string) => ownerNames.get(workflowId) ?? ''

/** 项目页改了目录归属 / 做了迁移之后调用，下一次点文档重新查所属项目。 */
export const invalidateOwnerProjects = () => ownerProjects.invalidate()

/** 打开一篇文档：属于项目就进项目内的文档页，否则老文档页。 */
export function openDoc(router: Router, t: DocTarget, opts: { replace?: boolean } = {}): Promise<void> {
  return openDocWith(router, ownerProjects, t, opts)
}
