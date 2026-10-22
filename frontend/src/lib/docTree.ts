import type { ProjectDoc } from '@/api/projectDocs'

export type DocTreeNode =
  | { type: 'folder'; name: string; key: string; children: DocTreeNode[] }
  | { type: 'leaf'; name: string; key: string; doc: ProjectDoc }

const STORAGE_PREFIX = 'relaymesh.docs.tree.expanded.'

export function treeStorageKey(workflowId: string): string {
  return STORAGE_PREFIX + workflowId
}

export function loadExpandedFolders(workflowId: string): Set<string> {
  if (!workflowId || typeof sessionStorage === 'undefined') return new Set()
  try {
    const raw = sessionStorage.getItem(treeStorageKey(workflowId))
    if (!raw) return new Set()
    const arr = JSON.parse(raw) as string[]
    return new Set(Array.isArray(arr) ? arr : [])
  } catch {
    return new Set()
  }
}

export function saveExpandedFolders(workflowId: string, keys: Set<string>): void {
  if (!workflowId || typeof sessionStorage === 'undefined') return
  try {
    sessionStorage.setItem(treeStorageKey(workflowId), JSON.stringify([...keys]))
  } catch {
    /* quota */
  }
}

/** 按路径分段构建文件夹树；叶子按路径排序。 */
export function buildDocTree(items: ProjectDoc[]): DocTreeNode[] {
  const root: DocTreeNode[] = []
  const sorted = [...items].sort((a, b) => a.path.localeCompare(b.path))
  for (const doc of sorted) {
    const parts = doc.path.split('/')
    const fileName = parts.pop() || doc.path
    let level = root
    let prefix = ''
    for (const seg of parts) {
      prefix = prefix ? `${prefix}/${seg}` : seg
      let folder = level.find(n => n.type === 'folder' && n.name === seg) as
        | Extract<DocTreeNode, { type: 'folder' }>
        | undefined
      if (!folder) {
        folder = { type: 'folder', name: seg, key: prefix, children: [] }
        level.push(folder)
      }
      level = folder.children
    }
    level.push({ type: 'leaf', name: fileName, key: doc.path, doc })
  }
  return root
}

export function defaultExpandedKeys(tree: DocTreeNode[]): string[] {
  const keys: string[] = []
  for (const n of tree) {
    if (n.type === 'folder') {
      keys.push(n.key)
    }
  }
  return keys
}
