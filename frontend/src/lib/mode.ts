// 工作模式：RelayMesh（会话与交付）与 ExecMesh（终端与模型执行）共用同一个应用壳，用 URL 前缀区分。
// 纯函数，便于单元测试；状态保存在 stores/mode.ts。

export type AppMode = 'relay' | 'exec'

export const EXEC_HOME = '/exec'
export const RELAY_HOME = '/'

/** 路径属于哪个模式：`/exec` 与 `/exec/...` 是 ExecMesh，其余都是 RelayMesh。 */
export function modeOfPath(path: string): AppMode {
  const p = path.split(/[?#]/, 1)[0]
  return p === EXEC_HOME || p.startsWith(EXEC_HOME + '/') ? 'exec' : 'relay'
}

/**
 * 这个路径是否值得被记成「该模式上次停留的位置」。
 * RelayMesh 只记首页：设置、归档是从首页打开的附属页面，切回 RelayMesh 应该回到会话列表。
 */
export function isRememberable(path: string): boolean {
  const p = path.split(/[?#]/, 1)[0]
  return modeOfPath(p) === 'exec' ? true : p === RELAY_HOME
}

/** 终端会话页的路径；record=true 表示打开它的「记录回放」标签（运行中的会话也可以回放到目前为止的内容）。 */
export function execSessionPath(sid: string, record = false): string {
  return `${EXEC_HOME}/t/${encodeURIComponent(sid)}${record ? '?view=record' : ''}`
}

export function execThreadPath(key: string): string {
  return `${EXEC_HOME}/r/${encodeURIComponent(key)}`
}

export const EXEC_HOSTS_PATH = `${EXEC_HOME}/hosts`
/** 归档页：被归档的会话与模型线程都在这里（侧栏的「已归档」筛选只在有归档时才出现）。 */
export const EXEC_ARCHIVE_PATH = `${EXEC_HOME}/archive`

// ---------- 项目切换器 ----------
// 标题处的切换器把 RelayMesh 与 ExecMesh 当成两个并列的「项目」：各有自己的图标、名称与一句话说明。

export interface ProjectInfo {
  id: AppMode
  name: string
  /** 名称下方的小字（沿用侧栏原有的副标题）。 */
  subtitle: string
  /** 菜单里的一句话职责说明。 */
  tagline: string
}

export const PROJECTS: Record<AppMode, ProjectInfo> = {
  relay: { id: 'relay', name: 'RelayMesh', subtitle: 'Agent Relay Hub', tagline: '会话与交付：智能体与你的沟通中枢' },
  exec: { id: 'exec', name: 'ExecMesh', subtitle: 'Remote Terminal Hub', tagline: '终端与模型执行：远程主机、命令与审批' },
}

export const PROJECT_ORDER: AppMode[] = ['relay', 'exec']

export function otherProject(mode: AppMode): AppMode {
  return mode === 'relay' ? 'exec' : 'relay'
}

/** 各项目身上需要提醒的事项数（由调用方从各个仓库汇总）。 */
export interface ProjectCounts {
  /** RelayMesh：等待你反馈的会话数。 */
  relayWaiting: number
  /** ExecMesh：待你处理的事项数（待启用主机 + 待批准的模型命令）。 */
  execAttention: number
  /** ExecMesh：正在运行的模型命令数。 */
  modelRunning: number
}

export interface ProjectAlert {
  /** 琥珀色徽标上的数字；0 表示没有待处理事项。 */
  attention: number
  /** 是否有模型命令正在运行（绿色脉冲点，只有 ExecMesh 有）。 */
  running: boolean
  /** 给 title / aria-label 用的完整说明；没有任何提醒时为空串。 */
  text: string
}

/**
 * 切换器上某个项目的提醒。只有「另一个项目」才需要提醒——你正待在的项目里，这些信息界面上本来就看得见。
 * 因此 current === id 时一律返回空提醒。
 */
export function projectAlert(id: AppMode, current: AppMode, counts: ProjectCounts): ProjectAlert {
  if (id === current) return { attention: 0, running: false, text: '' }
  if (id === 'exec') {
    const parts: string[] = []
    if (counts.execAttention > 0) parts.push(`${counts.execAttention} 项待处理（待启用主机或待批准的命令）`)
    if (counts.modelRunning > 0) parts.push(`${counts.modelRunning} 个模型正在运行`)
    return { attention: counts.execAttention, running: counts.modelRunning > 0, text: parts.join('，') }
  }
  return {
    attention: counts.relayWaiting,
    running: false,
    text: counts.relayWaiting > 0 ? `${counts.relayWaiting} 个会话等待反馈` : '',
  }
}

/** 徽标上显示的数字文本：超过 99 显示为 99+。 */
export function badgeNumber(n: number): string {
  return n > 99 ? '99+' : String(n)
}

/** 切换器按钮的无障碍名称：当前项目 + 另一个项目的提醒（如果有）。 */
export function switcherLabel(current: AppMode, counts: ProjectCounts): string {
  const alert = projectAlert(otherProject(current), current, counts)
  const base = `切换项目，当前是 ${PROJECTS[current].name}`
  return alert.text ? `${base}（${PROJECTS[otherProject(current)].name}：${alert.text}）` : base
}
