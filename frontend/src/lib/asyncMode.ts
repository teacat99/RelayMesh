// 异步对话模式的纯逻辑（#42）：状态摘要、开关能不能点、档位文案。
// 与组件分开放，方便单元测试；不依赖 Vue。
import type { AsyncModeView, AuthProfile, FeedbackSession } from '../api/types'
import type { WorkflowUpdate } from '../api/workItems'

export type AsyncStateKind = 'active' | 'degraded' | 'off' | 'never' | 'unknown'

export interface AsyncModeSummary {
  state: AsyncStateKind
  /** 一眼能看懂的结论。 */
  label: string
  /** 补充说明：这个状态意味着什么、下一步怎么办。 */
  detail: string
  /** 开关能不能点：关着时需要这个工作流收到过 hook；开着时永远可以关。 */
  canToggle: boolean
  /** 开关点不了的原因（给提示行 / 悬停说明）；能点时为空。 */
  blockedReason: string
}

/**
 * 异步对话生效时的一句话说明：把「不可逆操作要您批准才执行」放在里面，别让人误以为放飞了。
 * 不可逆操作（L2）不再让智能体停下来等：它发一张 L2 确认卡，批准前不执行这一步，其余工作照常推进（#217）。
 */
const ACTIVE_DETAIL =
  '智能体发完进度就继续工作，您的追加经 hook 在工具调用之间送达。不可逆操作（部署、推送、删数据、改凭据）会发一张 L2 确认卡等您批准，批准前不会执行，也不会因此停下其他工作。'

export function summarizeAsyncMode(view: AsyncModeView | null | undefined): AsyncModeSummary {
  if (!view) {
    return { state: 'unknown', label: '加载中', detail: '', canToggle: false, blockedReason: '' }
  }
  const canToggle = view.enabled || view.can_enable
  const blockedReason = canToggle ? '' : '还没收到这个工作流的 hook 事件，先安装 hook（上面的「怎么装？」）'
  if (view.effective) {
    return { state: 'active', label: '已生效', detail: ACTIVE_DETAIL, canToggle, blockedReason }
  }
  if (view.enabled) {
    return {
      state: 'degraded',
      label: '已开启，暂未生效',
      detail: view.reason_text || '编辑器 hook 暂未联通，当前按默认的逐条确认模式工作；hook 回来后自动生效。',
      canToggle,
      blockedReason,
    }
  }
  if (view.ever_enabled) {
    return { state: 'off', label: '已关闭', detail: '已回到默认的逐条确认模式。', canToggle, blockedReason }
  }
  return {
    state: 'never',
    label: '未开启',
    detail: view.can_enable
      ? '开启后，智能体发完进度就继续工作，不再停下来等您回复。'
      : '先安装编辑器 hook：异步对话靠它把您的追加在智能体工作时送达。',
    canToggle,
    blockedReason,
  }
}

/** 异步生效时阶段滑块让位给授权档位：阶段提示词在异步模式里不再注入。 */
export function showPhaseSlider(view: AsyncModeView | null | undefined): boolean {
  return !view?.effective
}

export function levelLabel(level: string): string {
  switch (level) {
    case 'L0':
      return '只读，写入先问'
    case 'L1':
      return '可回滚的改动直接做'
    default:
      return level
  }
}

/** 工具栏上授权档位小标签的文字。 */
export function profileChipText(view: AsyncModeView | null | undefined): string {
  const p = view?.profile
  if (!p) return '授权档位'
  return `授权：${p.name}`
}

/** 悬停说明：档位名、等级与规则全文。 */
export function profileChipTitle(view: AsyncModeView | null | undefined): string {
  const p = view?.profile
  if (!p) return ''
  return `授权档位「${p.name}」（${p.level} ${levelLabel(p.level)}）\n${p.rules}`
}

/** 档位下拉里一项的文字：默认档位带标记。 */
export function profileOptionLabel(p: Pick<AuthProfile, 'name' | 'level' | 'is_default'>): string {
  return `${p.name} · ${p.level}${p.is_default ? '（默认）' : ''}`
}

/** 异步模式下发布的不阻塞气泡：没有回复框，不算轮次，也不是「待确认」。 */
export function isNotice(session: Pick<FeedbackSession, 'kind'> | null | undefined): boolean {
  return session?.kind === 'notice'
}

/** 把 notice 之外的真实轮次挑出来（顶栏 / 计数等只关心真实的汇报）。 */
export function onlyRounds<T extends Pick<FeedbackSession, 'kind'>>(list: readonly T[]): T[] {
  return list.filter(s => !isNotice(s))
}

/** 授权档位表单的校验：返回第一条问题；没问题返回空串。口径与后端 validateAuthProfileFields 一致。 */
export function validateProfileDraft(
  draft: { name: string; level: string; rules: string },
  limits: { name_max: number; rules_max: number },
): string {
  const name = draft.name.trim()
  const rules = draft.rules.trim()
  if (!name) return '档位名称不能为空'
  if (/[\r\n]/.test(name)) return '档位名称不能换行'
  if ([...name].length > limits.name_max) return `档位名称不能超过 ${limits.name_max} 个字`
  if (draft.level !== 'L0' && draft.level !== 'L1') return '档位等级只能是 L0 或 L1'
  if (!rules) return '规则摘要不能为空：它是写给智能体看的，说明哪些事可以直接做、哪些先问'
  if ([...rules].length > limits.rules_max) return `规则摘要不能超过 ${limits.rules_max} 个字`
  return ''
}

// ---------- 时间线上的任务进展卡：每次更新一张 ----------

/** 一次最多留多少张：异步模式下智能体可能一口气改很多任务，只保留最近的，免得把对话淹没（连续的卡片还会叠成一摞）。 */
export const TASK_CARD_MAX = 40
/** 卡片上进度 / 结论文字的上限（按字符数）。 */
export const TASK_CARD_TEXT_MAX = 140

export interface TaskUpdateCard {
  /** 稳定键：一次更新一张卡，键就是更新记录的编号，任务后来再动不会改写这张卡。 */
  key: string
  seq: number
  title: string
  /** 这次更新之后任务所处的状态（给徽标着色）：有状态变化取变化后的，否则取任务现在的状态。 */
  state: string
  /** 动词：新建 / 进行中 / 验证中 / 已完成 / 已搁置 / 待办 / 进展（只更新了进度或备注）。 */
  verb: string
  /** 这次更新写下的文字（进度 / 备注 / 结论）；没有内容时为空串。 */
  text: string
  /** 卡片落在时间线上的时间 = 这次更新的提交时间。 */
  at: string
}

const STATE_VERB: Record<string, string> = {
  todo: '待办',
  doing: '进行中',
  review: '验证中',
  done: '已完成',
  shelved: '已搁置',
}

function ms(v: string | undefined): number {
  if (!v) return NaN
  const t = Date.parse(v)
  return Number.isFinite(t) ? t : NaN
}

function clip(text: string, max: number): string {
  const runes = [...text.trim().replace(/\s+/g, ' ')]
  return runes.length > max ? runes.slice(0, max).join('') + '…' : runes.join('')
}

/**
 * 把「开启异步模式之后智能体写下的任务更新」整理成时间线卡片：每次更新一张，按提交时间升序，
 * 同一任务的几次更新各自留在各自的时间点上（不再只留最新一张，那等于改写历史）；最多保留最近的 TASK_CARD_MAX 张。
 * - 只看提交时间晚于开启时间的更新（开启之前的历史不往时间线里塞）；
 * - 创建任务那一条（只有变化后的状态、没有变化前的）动词是「新建」；改了状态的按变化后的状态给动词；
 *   只更新了进度 / 备注 / 阶段的动词是「进展」。
 * enabledAt 缺失（从没开启过）→ 没有卡片。
 */
export function buildTaskUpdateCards(
  updates: ReadonlyArray<Pick<WorkflowUpdate, 'id' | 'seq' | 'title' | 'state' | 'from_state' | 'to_state' | 'text' | 'created_at'>>,
  enabledAt: string | undefined,
  max: number = TASK_CARD_MAX,
): TaskUpdateCard[] {
  const since = ms(enabledAt)
  if (!Number.isFinite(since) || max <= 0) return []
  const cards: Array<TaskUpdateCard & { t: number; id: number }> = []
  for (const u of updates) {
    const t = ms(u.created_at)
    if (!Number.isFinite(t) || t <= since) continue
    const changed = Boolean(u.to_state)
    const created = changed && !u.from_state
    const state = changed ? (u.to_state as string) : u.state
    cards.push({
      key: `task-u${u.id}`,
      seq: u.seq,
      title: u.title,
      state,
      verb: created ? '新建' : changed ? STATE_VERB[state] || state : '进展',
      text: clip(u.text || '', TASK_CARD_TEXT_MAX),
      at: u.created_at,
      t,
      id: u.id,
    })
  }
  cards.sort((a, b) => a.t - b.t || a.id - b.id)
  return cards.slice(-max).map(({ t: _t, id: _id, ...card }) => card)
}
