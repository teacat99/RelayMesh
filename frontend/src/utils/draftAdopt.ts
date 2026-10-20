/**
 * 草稿跨设备同步的「采纳」决策（反馈「草稿相关的云同步方案也要检查一下」）。
 *
 * 纯逻辑，不依赖 Vue / 网络，可以直接在 Node 下单测。
 *
 * 旧实现的缺口：只有「本机完全没有这个工作流的草稿记录」时才会去服务端取草稿。
 * 在另一台设备上写了一半的草稿，这台设备只要曾经打开过这个工作流（本地有记录），就永远看不到它；
 * 在另一台设备上提交 / 清空了草稿，这台设备的旧草稿也不会消失，下一次敲字还会把它重新传回服务器。
 *
 * 现在的规则（最后写入者胜，但绝不覆盖本机尚未同步的改动）：
 * - 本机没有记录 → 采纳服务端草稿（沿用原有行为）；
 * - 本机只有一份「从未同步过的空白草稿」→ 同样采纳。它不是用户的改动：只是打开页面、刷新、切走时自动落下的空壳。
 *   把它当成「未同步的本机改动」保留下来，下一次上传就会用空白覆盖掉另一台设备写在服务端的草稿（实测发生过：
 *   新设备在载入完成之前刷新一次，就会留下这种空壳）；
 * - 本机自上次同步以来没有改动（内容哈希等于「已同步」标记），且服务端比上次同步时更新、内容也确实不同 → 采纳；
 * - 本机有未同步的改动 → 保留本机（之后的上传会以本机为准，这是「最后写入者胜」的约定）；
 * - 服务端没有草稿 / 内容相同 / 不比上次同步更新 / 决策期间用户又在输入 → 保留本机。
 */

import type { DraftImageLike, MultiDraftLike } from './draftSync'

export type AdoptAction = 'adopt' | 'keep'

export type AdoptReason =
  | 'no_server_draft'
  | 'user_touched'
  | 'no_local_record'
  | 'blank_unsynced'
  | 'same_content'
  | 'local_dirty'
  | 'not_newer'
  | 'newer'

export interface AdoptInput {
  /** 服务端草稿的更新时间（毫秒）；服务端没有草稿为 null。 */
  serverUpdatedAt: number | null
  /** 本机上次与服务端同步（上传成功 / 采纳）时服务端记录的更新时间（毫秒）；没有记录为 null。 */
  syncedAt: number | null
  /** 本机是否「干净」：有已同步标记，且标记等于当前内容的哈希（自上次同步以来本机没有改动）。 */
  localClean: boolean
  /** 本机根本没有这个工作流的草稿记录（全新设备 / 清过浏览器数据）。 */
  noLocalRecord: boolean
  /** 服务端草稿与本机当前内容一致。 */
  sameContent: boolean
  /** 本机内容是空白草稿，且从未与服务端同步过（没有「已同步」标记）：没有任何需要保护的本机内容（见 isBlankUnsynced）。 */
  blankUnsynced: boolean
  /** 决策期间用户又改动了输入。 */
  touched: boolean
}

export function decideAdopt(input: AdoptInput): { action: AdoptAction; reason: AdoptReason } {
  if (input.serverUpdatedAt === null) return { action: 'keep', reason: 'no_server_draft' }
  if (input.touched) return { action: 'keep', reason: 'user_touched' }
  if (input.noLocalRecord) return { action: 'adopt', reason: 'no_local_record' }
  if (input.sameContent) return { action: 'keep', reason: 'same_content' }
  if (input.blankUnsynced) return { action: 'adopt', reason: 'blank_unsynced' }
  if (!input.localClean) return { action: 'keep', reason: 'local_dirty' }
  if (input.syncedAt !== null && input.serverUpdatedAt <= input.syncedAt) return { action: 'keep', reason: 'not_newer' }
  return { action: 'adopt', reason: 'newer' }
}

const IMAGE_TAIL_SAMPLE = 48

/** 图片的「身份」：有内容键就用内容键（与正文是否已载入、是否内联无关），旧草稿没有键就用轻量指纹。 */
function imageIdentity(img: DraftImageLike): string {
  if (img.key) return `k:${img.key}`
  const data = img.data || ''
  return ['f', img.name || '', img.format || '', img.hash || '', img.url || '', String(data.length), data.slice(-IMAGE_TAIL_SAMPLE)].join('\u0001')
}

/**
 * 用户能感知的草稿内容签名：当前序号、每个卡槽的文本 / 预设 / 图片身份。
 * 不含 updated_at，也与图片正文是否已载入无关——用来比较「本机」与「服务端」两份草稿是不是同一份内容。
 */
export function contentSignature(state: MultiDraftLike): string {
  return JSON.stringify([
    state.activeIndex,
    state.drafts.map(slot => [slot.id, slot.text, slot.presets || [], (slot.images || []).map(imageIdentity)]),
  ])
}

/** 草稿是否「空白」：只有一个卡槽，没有文字、预设、图片。 */
export function isBlankDraft(state: MultiDraftLike): boolean {
  if (state.drafts.length !== 1) return false
  const slot = state.drafts[0]
  return !slot.text && (slot.presets || []).length === 0 && (slot.images || []).length === 0
}

/**
 * 本机是不是「从未同步过的空白草稿」：空白，并且没有「已同步」标记（这台设备从来没有把这个工作流的草稿传上去 / 采纳下来过）。
 *
 * 这种草稿不代表用户的任何意图——它是打开页面、刷新或切走时自动落下的空壳。
 * 因此：它不该阻止采纳别处写的草稿（decideAdopt 的 blank_unsynced），也不该被上传去覆盖服务端的内容
 * （载入时按「服务端已一致」登记，见 FeedbackInputDock.loadDrafts）。
 * 已经同步过的空白（用户确实清空 / 提交过）有标记，不属于这一类，照常上传。
 */
export function isBlankUnsynced(state: MultiDraftLike, syncedMark: string | null): boolean {
  return syncedMark === null && isBlankDraft(state)
}

/** 服务端返回的 ISO 时间 → 毫秒；解析失败为 null。 */
export function parseServerTime(value: string | undefined | null): number | null {
  if (!value) return null
  const ms = Date.parse(value)
  return Number.isFinite(ms) ? ms : null
}
