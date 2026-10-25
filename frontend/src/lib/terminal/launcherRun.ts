import type { HostView, LauncherRunPlan, LauncherRunView, TerminalLauncher } from '../../api/terminal'
import { relativeTime, runLimitText } from './format'
import { effectiveAfterNew, effectiveSource, hostReadiness } from './launcher'

/** 平台单次运行的上限（7 天，秒）：预设没设「最长运行时间」时就是它（与服务端 termproto.MaxRunTimeoutSec 一致）。 */
export const DEFAULT_RUN_LIMIT_SEC = 7 * 24 * 3600

// 「非交互续聊」预设（#126）的运行呈现：不碰界面的那一部分。
// 点「运行」由 Hub 经主机的执行通道运行一次 `agent -p --resume …`，没有终端；结果显示在预设清单里：
// 状态、用时、退出码、最后一条回复，以及「空转退出」（运行成功但智能体没向 RelayMesh 发过任何消息）的提醒。

export type RunTone = 'good' | 'warn' | 'bad' | 'neutral'

export interface RunStatus {
  label: string
  tone: RunTone
  /** 正在跑：清单里显示转圈。待批准是在等人，不转圈。 */
  busy: boolean
}

/** 运行状态的文字与圆点颜色；预设的运行现状与「其他非交互运行」区共用。 */
export const RUN_TONE_TEXT: Record<RunTone, string> = {
  good: 'text-emerald-600 dark:text-emerald-400',
  warn: 'text-amber-600 dark:text-amber-400',
  bad: 'text-destructive',
  neutral: 'text-foreground/80',
}
export const RUN_TONE_DOT: Record<RunTone, string> = {
  good: 'bg-emerald-500',
  warn: 'bg-amber-500',
  bad: 'bg-destructive',
  neutral: 'bg-muted-foreground/60',
}

/** 一次运行现在是什么状态。 */
export function runStatus(v: LauncherRunView): RunStatus {
  switch (v.state) {
    case 'running':
      return { label: '运行中', tone: 'neutral', busy: true }
    case 'awaiting_approval':
      return { label: '待批准', tone: 'warn', busy: false }
    case 'succeeded':
      return v.silent
        ? { label: '已结束 · 空转退出', tone: 'warn', busy: false }
        : { label: '已结束 · 成功', tone: 'good', busy: false }
    case 'failed':
      return { label: typeof v.exit_code === 'number' ? `失败（退出码 ${v.exit_code}）` : '失败', tone: 'bad', busy: false }
    case 'timeout':
      // 到点被主机强制结束（预设的「最长运行时间」，没设就是 7 天）：这不是命令出了错，而是用满了时限（#141）。
      return { label: '达到运行时长上限', tone: 'warn', busy: false }
    case 'cancelled':
      return { label: '已取消', tone: 'neutral', busy: false }
    case 'rejected':
    case 'denied':
      return { label: '被拒绝', tone: 'bad', busy: false }
    case 'lost':
      return { label: '状态丢失（主机重启或掉线）', tone: 'warn', busy: false }
    default:
      return { label: v.state || '未知', tone: 'neutral', busy: v.active }
  }
}

/** 用时的口语写法：不到 1 秒 / 12 秒 / 3 分 5 秒 / 1 小时 2 分。 */
export function durationText(ms: number | undefined): string {
  if (typeof ms !== 'number' || !Number.isFinite(ms) || ms < 0) return ''
  const s = Math.round(ms / 1000)
  if (s < 1) return '不到 1 秒'
  if (s < 60) return `${s} 秒`
  const m = Math.floor(s / 60)
  if (m < 60) return s % 60 ? `${m} 分 ${s % 60} 秒` : `${m} 分`
  const h = Math.floor(m / 60)
  return m % 60 ? `${h} 小时 ${m % 60} 分` : `${h} 小时`
}

/** 状态行后半段：多久以前、用时。进行中只说从什么时候开始。 */
export function runMeta(v: LauncherRunView, nowMs: number = Date.now()): string {
  const parts: string[] = []
  // 这次运行是项目管理 Agent 经 preset_run 发起的（#179）：写明是谁，免得被当成你手动点的。
  if (v.by) parts.push(`项目管理 Agent「${v.by}」发起`)
  // 这次运行是系统自动续跑出来的（#151）：标出是 24 小时内的第几次；已经连续跑满判定时长的，标明续跑成功（#172）。
  if (v.auto && typeof v.auto.attempt === 'number' && v.auto.attempt > 0) {
    const label = `自动续跑 ${v.auto.attempt}/${v.auto.max || v.auto.attempt}`
    parts.push(v.auto.settled ? `${label}（已成功）` : label)
  }
  // 「最近会话」（--continue，#191）接哪个对话由 Cursor CLI 决定、不是 Hub 选的：写明实际接上的对话（读到输出之前只写来源）。
  if (v.mode === 'continue') parts.push(v.conversation ? `接上对话 ${v.conversation.slice(0, 8)}…` : '最近会话')
  if (v.active) {
    parts.push(`${relativeTime(v.started_at ?? v.created_at, nowMs)}开始`)
  } else {
    const at = v.ended_at ?? v.started_at ?? v.created_at
    parts.push(relativeTime(at, nowMs))
    const d = durationText(v.duration_ms)
    if (d) parts.push(`用时 ${d}`)
  }
  return parts.filter(Boolean).join(' · ')
}

/** 状态下面的一句补充说明（需要你做点什么、或者这次运行有什么不对劲时才有）。 */
export function runHint(v: LauncherRunView): string {
  if (v.state === 'awaiting_approval') return '这台主机的执行模式是「需批准」：到 ExecMesh 的运行记录里批准后才会开始。'
  if (v.state === 'succeeded' && v.silent) {
    return '这次运行期间智能体没有向 RelayMesh 发出任何消息：命令跑通了，但没有人知道它做了什么。可以到 ExecMesh 的运行记录里看输出，或换个提示词再运行。'
  }
  if (v.state === 'lost') return '运行记录停在了半路（主机重启或掉线）；不确定它有没有做完，可以先到 ExecMesh 看一眼。'
  if (v.state === 'cancelled') {
    // 说清是谁、为什么取消的；对话本身没有结束，再点「运行」可以接着聊。
    return v.why ? `被取消：${v.why}。对话本身没有结束，可以再点「运行」接着聊。` : ''
  }
  // 没有退出码的失败 / 被拒绝：原因就是它没跑起来的解释。
  if (v.why && (v.state === 'rejected' || v.state === 'denied' || (v.state === 'failed' && typeof v.exit_code !== 'number'))) return v.why
  if (v.state === 'timeout') {
    const limit = runLimitText(v.timeout_sec)
    return `${limit ? `这次运行用满了设定的最长运行时间（${limit}）` : '这次运行用满了设定的最长运行时间'}，被主机强制结束；对话本身没有结束，可以接着聊。想让它每次多跑一会儿，在预设里把「最长运行时间」调大。`
  }
  return ''
}

// ---------- 异常退出与自动续跑（#151） ----------

/**
 * 距离某个时刻还有多久，倒计时的口语写法：「45 秒后」「5 分钟后」「1 小时 5 分钟后」（分钟向上取整，不会在还有几分钟时说「马上」）；
 * 已经到了或不到半分钟写「马上」；时间不合法是空串。
 */
export function untilText(iso: string | undefined, nowMs: number = Date.now()): string {
  const t = iso ? Date.parse(iso) : NaN
  if (!Number.isFinite(t)) return ''
  const sec = Math.round((t - nowMs) / 1000)
  if (sec < 30) return '马上'
  if (sec < 90) return `${sec} 秒后`
  const min = Math.ceil(sec / 60)
  if (min < 60) return `${min} 分钟后`
  const h = Math.floor(min / 60)
  return min % 60 ? `${h} 小时 ${min % 60} 分钟后` : `${h} 小时后`
}

/**
 * 失败说明下面的一句补充：Cursor CLI 自己报的错误类别意味着什么、你可以怎么办。
 * 认不出原因的（unknown）不写——状态里已经有退出码，再写一句「认不出」只是噪音。
 * 预设里开着自动续跑时由 autoResumeLine 说明接下来会怎样，这里不重复。
 */
export function runFailureLine(v: LauncherRunView): { text: string; hint: string } | null {
  const f = v.failure
  if (!f || f.class === 'unknown' || !f.text) return null
  const auto = !!v.auto?.enabled
  let hint = ''
  switch (f.class) {
    case 'retriable':
      hint = auto ? '' : '这类错误通常是暂时的：可以再点「运行」接着聊；在预设里打开「异常退出时自动续跑」可以让它自己重试。'
      break
    case 'action_required':
      hint = '需要你处理（例如重新登录、额度或确认）之后再点「运行」；重试没有用。'
      break
    case 'non_retriable':
      hint = '这类错误重试没有用：先看一眼运行记录，改好设置再点「运行」。'
      break
    case 'crashed':
      hint = f.retry ? (auto ? '' : '进程像是崩溃了：可以再点「运行」接着聊。') : ''
      break
  }
  return { text: f.text, hint }
}

export interface AutoLine {
  text: string
  tone: RunTone
  /** 还在排期、可以取消。 */
  cancellable: boolean
}

/**
 * 预设里开着自动续跑时，这次运行出事之后系统怎么安排的：排期中（可取消）、已经续跑、你取消了、作废、放弃。
 * 没有安排、也没有要说的（没出事、预设没开、安排与这次运行无关）时是 null。
 */
export function autoResumeLine(v: LauncherRunView, nowMs: number = Date.now()): AutoLine | null {
  const a = v.auto
  if (!a) return null
  const n = a.next_attempt && a.next_attempt > 0 ? `（第 ${a.next_attempt}/${a.max || 3} 次）` : ''
  switch (a.state) {
    case 'pending': {
      const when = untilText(a.due, nowMs)
      return { text: when === '马上' ? `即将自动续跑${n}` : `将在 ${when}自动续跑${n}`, tone: 'warn', cancellable: true }
    }
    case 'started':
      return { text: `已自动续跑${n}，最新一次运行在上面`, tone: 'neutral', cancellable: false }
    case 'stopped':
      return { text: '你取消了这次自动续跑', tone: 'neutral', cancellable: false }
    case 'skipped':
      return { text: `没有自动续跑：${a.why || '条件变了'}`, tone: 'neutral', cancellable: false }
    case 'gaveup':
      return { text: `不再自动续跑：${a.why || '没法继续'}。请你看看原因再手动运行`, tone: 'bad', cancellable: false }
  }
  // 没有登记：开着自动续跑、这次又是「重试没有用」的失败——说明为什么没有续跑，免得人对着它干等。
  if (a.enabled && v.state === 'failed' && v.failure && !v.failure.retry && v.failure.class !== 'unknown') {
    return { text: '没有自动续跑：这类失败重试没有用', tone: 'neutral', cancellable: false }
  }
  // 这次运行本身是自动续跑出来的、已经连续跑满判定时长：算续跑成功，24 小时内累计的次数已清零（#172）。
  if (a.settled) {
    const ok = runLimitText((a.ok_minutes ?? 0) * 60)
    return { text: `续跑成功：这次续跑${ok ? `连续跑满了 ${ok}` : '已经稳定运行'}，24 小时内累计的续跑次数已清零`, tone: 'good', cancellable: false }
  }
  return null
}

export type RunReadiness = { ok: true } | { ok: false; text: string }

/** 预设现在能不能点「运行」；不能时给出一句可以直接展示的原因。 */
export function runReadiness(host: HostView | undefined, last: LauncherRunView | undefined): RunReadiness {
  const r = hostReadiness(host)
  if (!r.ok) return { ok: false, text: r.text }
  if (last?.active) return { ok: false, text: '上一次运行还没结束' }
  return { ok: true }
}

/** 一句话说清预设会怎么续聊（清单里展示）：接着哪个对话、用什么模型、什么权限姿态。 */
export function runSummary(l: Pick<TerminalLauncher, 'run'>): string {
  const r = l.run
  if (!r) return ''
  const parts: string[] = []
  const source = effectiveSource(r)
  if (r.conversation_id) parts.push(`固定对话 ${r.conversation_id.slice(0, 8)}`)
  else if (source === 'continue') parts.push('最近会话（接着目录里 Cursor CLI 认的最近对话）')
  else if (source === 'new' && r.workflow_id) {
    switch (effectiveAfterNew(r)) {
      case 'stay': parts.push('新会话（每次运行都新开对话）'); break
      case 'continue': parts.push('新会话（第一次新开，成功后自动切成最近会话）'); break
      default: parts.push(`第一次新开对话，之后接着工作流 ${r.workflow_id} 里的对话`)
    }
  } else if (r.workflow_id) parts.push(`工作流 ${r.workflow_id} 里最近的对话`)
  if (r.model) parts.push(r.model)
  parts.push(r.posture === 'force' ? '放开全部命令（--force）' : '只放行白名单内的操作')
  // 设了最长运行时间才写（没设就是 7 天，不是你的选择，不占位置）。
  const limit = runLimitText((r.max_run_minutes ?? 0) * 60)
  if (limit) parts.push(`最长运行 ${limit}`)
  // 开着自动续跑的才写（默认关闭，不占位置）：清单里一眼看出哪些预设会在无人看管时自己重试（#151）。
  // 判定时长是自定义的才写（默认 10 分钟不占位置，#172）。
  if (r.auto_resume) {
    const ok = runLimitText((r.auto_resume_ok_minutes ?? 0) * 60)
    parts.push(ok ? `异常退出自动续跑（跑满 ${ok}算成功）` : '异常退出自动续跑')
  }
  return parts.join(' · ')
}

/** 点「运行」成功之后弹出的提示。 */
export function runNotice(plan: LauncherRunPlan): { title: string; description: string } {
  const lines: string[] = []
  if (plan.run_state === 'awaiting_approval') {
    lines.push('这台主机的执行模式是「需批准」：到 ExecMesh 的运行记录里批准后才会开始。')
  } else if (plan.new_conversation) {
    // 之后会怎样取决于「新会话」运行之后的去向（#559）；旧版 Hub 没有 after_new，按默认的「记下编号」说。
    const after = plan.after_new === 'stay'
      ? '这个预设每次运行都会新开对话，不会接着这一个'
      : plan.after_new === 'continue'
        ? '运行成功之后，预设会自动切换成「最近会话」'
        : '之后再点「运行」都接着它，不会再新开'
    lines.push(`在主机上新开了一个 Cursor CLI 对话（不打开终端）：它会带着工作流编号接入，${after}；结果会显示在这条预设下面，聊天里也会有记录。`)
  } else if (plan.continue) {
    lines.push('在主机上接着这个目录里最近的 Cursor CLI 对话运行一次（--continue，不打开终端）：接到哪个对话由 CLI 决定，实际接上的对话会写在运行记录里；结果会显示在这条预设下面，聊天里也会有记录。')
  } else {
    lines.push('在主机上接着原对话运行一次（不打开终端）；结果会显示在这条预设下面，聊天里也会有记录。')
  }
  // 预设设了时限才提醒（默认的 7 天不用说）。
  if (typeof plan.max_run_seconds === 'number' && plan.max_run_seconds > 0 && plan.max_run_seconds < DEFAULT_RUN_LIMIT_SEC) {
    lines.push(`这次运行最长 ${runLimitText(plan.max_run_seconds)}，到点会被强制结束。`)
  }
  for (const w of plan.warnings ?? []) lines.push(w)
  return {
    title: plan.run_state === 'awaiting_approval' ? `「${plan.name}」等待批准` : `已开始运行「${plan.name}」`,
    description: lines.join('\n'),
  }
}

/** 进行中的运行每隔多久读一次现状 / 没有进行中的运行时多久读一次（别处点的运行、或运行已结束要跟上）。 */
export const RUN_POLL_ACTIVE_MS = 2_500
export const RUN_POLL_IDLE_MS = 15_000

/** 有没有进行中的运行：预设的最近一次运行，或不属于任何预设的运行（#147）。 */
export function runsActive(runs: Record<string, LauncherRunView> | null | undefined, others?: readonly LauncherRunView[] | null): boolean {
  return (!!runs && Object.values(runs).some(v => v.active)) || (!!others && others.some(v => v.active))
}

export function runPollDelay(runs: Record<string, LauncherRunView> | null | undefined, others?: readonly LauncherRunView[] | null): number {
  return runsActive(runs, others) ? RUN_POLL_ACTIVE_MS : RUN_POLL_IDLE_MS
}

/**
 * 有没有预设的运行刚刚结束（#191）：上一次读到它还在进行，这一次它已经结束、或预设的最近一次运行换成了另一条。
 * 「新会话」运行成功之后服务端会把预设改成「最近会话」，界面要在这时把预设清单读新，免得表单拿着旧预设保存、把切换盖回去。
 * 第一次读（没有上一次的现状）不算。
 */
export function runsJustEnded(before: Record<string, LauncherRunView> | null | undefined, after: Record<string, LauncherRunView> | null | undefined): boolean {
  if (!before) return false
  return Object.entries(before).some(([id, was]) => {
    if (!was.active) return false
    const now = after?.[id]
    return !now || now.run_id !== was.run_id || !now.active
  })
}

// ---------- 不属于任何预设的运行（#147） ----------
// 旧入口（会话页「运行」接口）、会话守护的非交互唤醒（已下线，只剩旧记录）、所属预设已删除的预设运行：预设清单里没有位置，
// 弹窗里单列一个「其他非交互运行」区，可以到 ExecMesh 里看输出，进行中的可以取消。

/** 来源的口语名与一句解释（悬停提示用）。服务端以后增加新来源时，界面按通用说法显示。 */
export function otherRunSource(source: string): { label: string; hint: string } {
  switch (source) {
    case 'session_run':
      return { label: '会话页「运行」', hint: '会话页的「运行」入口（旧版页面或脚本）直接提交的非交互续聊，没有挂在任何预设上。' }
    case 'wake':
      return { label: '非交互唤醒', hint: '旧版会话守护的「非交互唤醒」提交的运行（该功能已下线），它不是预设的「运行」。' }
    case 'deleted_preset':
      return { label: '预设已删除', hint: '这次运行是删掉预设之前提交的，预设没了，所以只能在这里看到它。' }
    default:
      return { label: '非交互运行', hint: '' }
  }
}

/** 区块标题旁的概况：「2 个运行中 · 1 个已结束」。 */
export function othersSummary(others: readonly LauncherRunView[]): string {
  const active = others.filter(o => o.active).length
  const done = others.length - active
  return [active ? `${active} 个运行中` : '', done ? `${done} 个已结束` : ''].filter(Boolean).join(' · ')
}

/** 能不能取消：还没结束的（含待批准）都可以；结束了的没有什么可取消。 */
export function otherRunCancellable(o: LauncherRunView): boolean {
  return o.active
}

/**
 * 状态下面的一句补充说明。与预设里的 runHint 大体一致，但这些运行没有「运行」按钮，也没有可调的「最长运行时间」，
 * 所以被取消 / 用满时限时不再引导去点「运行」或改预设。
 */
export function otherRunHint(o: LauncherRunView): string {
  if (o.state === 'cancelled') return o.why ? `被取消：${o.why}。对话本身没有结束。` : ''
  // 异常退出（#151）：把 Cursor CLI 自己报的原因说出来。这些运行不属于预设，没有自动续跑可说，也没有「运行」按钮可指，所以只有原因本身。
  if (o.state === 'failed' && o.failure && o.failure.class !== 'unknown' && o.failure.text) return o.failure.text
  if (o.state === 'timeout') {
    const limit = runLimitText(o.timeout_sec)
    return `${limit ? `这次运行用满了设定的最长运行时间（${limit}）` : '这次运行用满了设定的最长运行时间'}，被主机强制结束；对话本身没有结束。`
  }
  return runHint(o)
}
