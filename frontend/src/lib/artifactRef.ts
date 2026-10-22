// 聊天消息里的 Artifact / 文档引用：记号 [[artifact:<文档编号>]]（Agent 用 chat send 的 artifact_ref 附带，也可直接写在正文里）。

const REF_RE = /\[\[artifact:(\d{1,9})\]\]/g

export interface ExtractedRefs {
  /** 去掉记号后的正文 */
  text: string
  /** 按出现顺序、去重后的文档编号 */
  ids: number[]
}

export function extractArtifactRefs(text: string | undefined | null): ExtractedRefs {
  const src = text ?? ''
  const ids: number[] = []
  const stripped = src.replace(REF_RE, (_m, id: string) => {
    const n = Number(id)
    if (n > 0 && !ids.includes(n)) ids.push(n)
    return ''
  })
  // 去掉记号后留下的多余空行
  return { text: ids.length || src !== stripped ? stripped.replace(/\n{3,}/g, '\n\n').trim() : src, ids }
}

export function formatArtifactRef(id: number): string {
  return `[[artifact:${id}]]`
}
