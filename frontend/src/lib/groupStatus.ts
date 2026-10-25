// 群聊页页头的「群级状态」徽标（#241 第三步）：把群里各位 Agent 成员的状态汇成一句话，
// 思路与会话页页头的 SessionStatusBadge 一致——等你的排最前，其次是疑似卡住的、在干活的，再其次是出了问题的，最后是空闲 / 都不在线。
// 「疑似卡住」与侧栏、会话页同口径（lib/agentState.ts 的 stateView：服务端判出的硬卡住，会话上的 stalled 标志，值得第一眼看到）。
//
// 这里只有两个纯函数，不碰任何 store：
//   - overlayLiveState：成员的状态是服务端取群页时带的那一份，只在群里有消息 / 成员变动的事件时才会重取；
//     而成员「干完活变空闲」「冒出一条等你回答」不一定伴随群里的消息，光读那一份徽标会停在旧状态。
//     所以徽标与成员面板都用 Agent 状态仓库里的实时会话盖在上面：有实时数据用实时的，没有就用页面带的那一份。
//   - deriveGroupStatus：把成员汇成徽标（文字、色调、悬停说明）。
// 摆放与取数在 components/group/GroupStatusBadge.vue 与 GroupChatPane.vue。
import type { GroupMember } from '../api/groups'
import type { AgentSession } from './agentState'

/** 实时会话里用得到的四项（Agent 状态仓库里的 AgentSession 是它的超集）；stalled 是服务端判出的硬卡住，没带就当没卡住。 */
export type LiveMemberState = Pick<AgentSession, 'state' | 'sub' | 'waiting_you'> & { stalled?: boolean }

/**
 * 叠加过实时状态的成员：比服务端随群页带的 GroupMember 多一个 stalled（硬卡住）。
 * 群页带的那份里没有这个字段（服务端 GroupMember 只有 state / sub / waiting_you），它只来自实时会话，所以不加进 API 类型。
 */
export type LiveGroupMember = GroupMember & { stalled?: boolean }

/**
 * 用实时会话盖掉成员身上服务端带的状态。
 * 只动在职的 Agent 成员；找不到实时会话（仓库还没取到、或这个工作流不在概览里）或实时会话没有状态时原样返回这位成员。
 * 不改传入的数组与成员对象。
 */
export function overlayLiveState(
  members: readonly GroupMember[],
  liveOf: (workflowId: string) => LiveMemberState | null | undefined,
): LiveGroupMember[] {
  return members.map(m => {
    if (m.kind !== 'workflow' || m.left) return m
    const live = liveOf(m.workflow_id || m.ref)
    if (!live || !live.state) return m
    return { ...m, state: live.state, sub: live.sub, waiting_you: live.waiting_you, stalled: !!live.stalled }
  })
}

/** 徽标的状态，按「越靠前越要紧」排：等你回答 → 等授权 → 疑似卡住 → 工作中 → 出错 → 空闲 → 都不在线。 */
export type GroupStatusKey = 'waiting_you' | 'waiting_approval' | 'stalled' | 'working' | 'error' | 'idle' | 'away'

/** 色调：与成员面板里状态点同一套（工作中绿、等你 / 等授权琥珀、疑似卡住与出错红、其余灰）。 */
export type GroupStatusTone = 'attention' | 'active' | 'danger' | 'muted'

export interface GroupStatus {
  key: GroupStatusKey
  tone: GroupStatusTone
  /** 处于这个状态的成员有几位（空闲 / 都不在线时文字里不写数字，数据属性里仍带着）。 */
  count: number
  /** 徽标上的字：「2 位在等你」。 */
  label: string
  /** 窄屏上的短写法：「等你 2」。 */
  shortLabel: string
  /** 等你 / 工作中 / 疑似卡住的圆点跳动（疑似卡住与 stateView 一样跳，值得第一眼看到）。 */
  pulse: boolean
  /** 悬停与读屏的说明：各状态各有哪几位，一行一个状态。 */
  title: string
}

const ORDER: readonly GroupStatusKey[] = ['waiting_you', 'waiting_approval', 'stalled', 'working', 'error', 'idle', 'away']

const TONE: Record<GroupStatusKey, GroupStatusTone> = {
  waiting_you: 'attention',
  waiting_approval: 'attention',
  stalled: 'danger',
  working: 'active',
  error: 'danger',
  idle: 'muted',
  away: 'muted',
}

/** 说明里各状态的叫法；与成员面板里每位成员的状态行同一套说法（lib/group.ts 的 memberStatusText）。 */
const BUCKET_TEXT: Record<GroupStatusKey, string> = {
  waiting_you: '在等你回答',
  waiting_approval: '等授权',
  stalled: '疑似卡住',
  working: '工作中',
  error: '出错',
  idle: '空闲',
  away: '不在线',
}

/**
 * 一位成员落在哪个状态里。已中断的算空闲（会话还在、可以接着聊）；已结束 / 离线 / 还没接入 / 不认识的都算不在线。
 * 硬卡住（stalled，只来自实时会话）与 stateView 同口径：先于 state 判，服务端只对 working 的会话判它、且不会与「等你」同时出现。
 */
export function memberBucket(m: Pick<GroupMember, 'state' | 'waiting_you'> & { stalled?: boolean }): GroupStatusKey {
  if (m.waiting_you || m.state === 'waiting_you') return 'waiting_you'
  if (m.stalled) return 'stalled'
  switch (m.state) {
    case 'waiting_approval':
      return 'waiting_approval'
    case 'working':
      return 'working'
    case 'error':
      return 'error'
    case 'idle':
    case 'aborted':
      return 'idle'
    default:
      return 'away'
  }
}

function labelOf(key: GroupStatusKey, n: number): string {
  switch (key) {
    case 'waiting_you':
      return `${n} 位在等你`
    case 'waiting_approval':
      return `${n} 位等授权`
    case 'stalled':
      return `${n} 位疑似卡住`
    case 'working':
      return `${n} 位工作中`
    case 'error':
      return `${n} 位出错`
    case 'idle':
      return '空闲'
    default:
      return '都不在线'
  }
}

function shortLabelOf(key: GroupStatusKey, n: number): string {
  switch (key) {
    case 'waiting_you':
      return `等你 ${n}`
    case 'waiting_approval':
      return `授权 ${n}`
    case 'stalled':
      return `卡住 ${n}`
    case 'working':
      return `工作 ${n}`
    case 'error':
      return `出错 ${n}`
    case 'idle':
      return '空闲'
    default:
      return '离线'
  }
}

/**
 * 把群里在职的 Agent 成员汇成徽标；群里没有 Agent 成员时返回 null（不画徽标）。
 * 取最靠前的那个状态：有人在等你就说「N 位在等你」，否则有人疑似卡住就说「N 位疑似卡住」，再否则有人在干活就说「N 位工作中」，依此类推；
 * 其余状态的人只出现在悬停说明里。
 */
export function deriveGroupStatus(members: readonly LiveGroupMember[]): GroupStatus | null {
  const names: Record<GroupStatusKey, string[]> = { waiting_you: [], waiting_approval: [], stalled: [], working: [], error: [], idle: [], away: [] }
  let total = 0
  for (const m of members) {
    if (m.kind !== 'workflow' || m.left) continue
    total++
    names[memberBucket(m)].push(m.name?.trim() || m.ref)
  }
  if (total === 0) return null
  const key = ORDER.find(k => names[k].length > 0) ?? 'away'
  const count = names[key].length
  const tone = TONE[key]
  return {
    key,
    tone,
    count,
    label: labelOf(key, count),
    shortLabel: shortLabelOf(key, count),
    pulse: tone === 'attention' || tone === 'active' || key === 'stalled',
    title: ORDER.filter(k => names[k].length > 0)
      .map(k => `${BUCKET_TEXT[k]}：${names[k].join('、')}`)
      .join('\n'),
  }
}
