/**
 * 草稿图片的提交格式（反馈「草稿图片也纳入云存储」）。
 *
 * 草稿里的图片「粘贴即上传」到服务器的草稿图片存储之后，提交时不必再把几 MB 的正文重传一遍：
 * 带 draft_key 的图片只发引用，服务端从图片存储里取出同一份字节存进会话。
 *
 * 万一服务端那份已经不在（被清理、被别处删掉），服务端会整体拒绝（400 draft_image_missing）且不改变任何东西，
 * 客户端用图片自带的内存正文改发「内联」格式再试一次：不会因为云端状态异常而丢图，也不会让这次提交失败。
 *
 * 纯逻辑，不依赖 Vue / axios，可以直接在 Node 下单测。
 */

import type { SessionImage } from '../api/types'

export interface WireImages {
  images: SessionImage[]
  /** 草稿所属的工作流（请求顶层的 draft_workflow_id）；没有引用型图片时为空。 */
  draftWorkflowId?: string
}

/** 去掉只在界面内存里有意义的字段与传输层字段，余下的才是会话里该保存的图片。 */
function stripLocalFields(img: SessionImage): SessionImage {
  const { synced: _synced, lost: _lost, draft_key: _draftKey, draft_workflow_id: _draftWorkflowId, ...rest } = img
  return rest
}

export function hasDraftRefs(images: readonly SessionImage[] | undefined): boolean {
  return !!images?.some(img => !!img.draft_key)
}

/** 引用模式：带 draft_key 的图片只留元数据与引用，其余图片原样（去掉内存字段）。 */
export function toRefImages(images: readonly SessionImage[] | undefined): WireImages {
  const out: SessionImage[] = []
  let draftWorkflowId: string | undefined
  for (const img of images ?? []) {
    const clean = stripLocalFields(img)
    if (img.draft_key) {
      const { data: _data, url: _url, ...meta } = clean
      out.push({ ...meta, draft_key: img.draft_key })
      if (!draftWorkflowId && img.draft_workflow_id) draftWorkflowId = img.draft_workflow_id
    } else {
      out.push(clean)
    }
  }
  return draftWorkflowId ? { images: out, draftWorkflowId } : { images: out }
}

/** 内联模式：所有图片都带自己的正文，不引用服务器上的草稿图片。 */
export function toInlineImages(images: readonly SessionImage[] | undefined): WireImages {
  return { images: (images ?? []).map(stripLocalFields) }
}

/** 服务端是否因为引用的草稿图片不存在而整体拒绝了这次提交（HTTP 400 + code=draft_image_missing）。 */
export function isDraftImageMissing(error: unknown): boolean {
  const e = error as { response?: { status?: number; data?: { code?: string } } } | null | undefined
  return e?.response?.status === 400 && e.response.data?.code === 'draft_image_missing'
}

/**
 * 发送带图片的请求：先走引用格式；服务端说引用的图片不在了，就用内存里的正文改发内联格式再试一次。
 * 只有「每张引用型图片都还带着正文」时才会重试，否则把原错误抛给调用方。
 */
export async function sendWithDraftImages<T>(
  images: readonly SessionImage[] | undefined,
  send: (wire: WireImages) => Promise<T>,
): Promise<T> {
  if (!hasDraftRefs(images)) return send(toInlineImages(images))
  try {
    return await send(toRefImages(images))
  } catch (error) {
    const canInline = (images ?? []).every(img => !img.draft_key || !!img.data)
    if (isDraftImageMissing(error) && canInline) return send(toInlineImages(images))
    throw error
  }
}
