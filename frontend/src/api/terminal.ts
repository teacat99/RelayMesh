// 终端功能的 REST 封装（对应 internal/terminal/rest.go）。
//
// 权限分两层：
//   - 登录即可：状态、主机列表、备注、会话重命名、审计、上锁 / 解锁；
//   - 需要「二次解锁」：启用 / 停用 / 删除主机、发现模式、新建 / 连接 / 终止会话。
//     解锁令牌只保存在内存里（刷新页面即失效），通过 X-Terminal-Unlock 请求头随终端请求（以及令牌管理请求）发送。

import { api } from './client'
import { ReadGate } from '../lib/terminal/readGate'
import { carriesUnlock } from '../lib/terminal/unlockRetry'

export const UNLOCK_HEADER = 'X-Terminal-Unlock'

let unlockToken = ''

/** 设置（或清空）解锁令牌；之后所有 /terminal/ 下的请求都会自动带上它。 */
export function setUnlockToken(token: string): void {
  unlockToken = token
}

/** 当前的解锁令牌（空串表示未解锁）；事件流用 fetch 自己发请求，需要手动带上。 */
export function getUnlockToken(): string {
  return unlockToken
}

api.interceptors.request.use(config => {
  if (unlockToken && carriesUnlock(config.url, config.method)) {
    config.headers.set(UNLOCK_HEADER, unlockToken)
  }
  return config
})

// ---------- 类型 ----------

export type GateReason = 'ok' | 'no_password' | 'weak_jwt_secret' | 'disabled_by_env' | 'internal_error'

export interface DiscoveryView {
  open: boolean
  always: boolean
  until?: string
  remaining_seconds: number
}

/** 服务端自带的 Agent 安装源（见后端 /i/<票据>/<文件>）。 */
export interface InstallSourceView {
  /** 服务端是否附带了 Agent 安装文件；为 false 时安装命令退回 GitHub 来源。 */
  available: boolean
  /** 当前发现窗口的安装票据，只在窗口开启期间才有。 */
  ticket?: string
  /** 服务端附带的平台，形如 linux-amd64、windows-amd64。 */
  platforms?: string[]
  /** 内置 HTTPS 叶子证书的公钥指纹（curl --pinnedpubkey 格式：sha256//<base64>）。 */
  spki_pin?: string
  /** 这个服务端对应的 Release 标签（vX.Y.Z / vX.Y.Z-rc.N）；开发版与未知版本没有。没有安装源时，GitHub 来源的命令据此固定到同一个版本。 */
  version?: string
}

export interface TerminalStatus {
  enabled: boolean
  reason: GateReason
  unlocked?: boolean
  unlock_remaining_seconds?: number
  unlock_idle_seconds?: number
  discovery?: DiscoveryView
  tls_pin?: string
  https_port?: number
  install?: InstallSourceView
  /** SSH 后端的状态：缺少强主密钥时整体关闭（reason 说明原因）。 */
  ssh?: SshStatus
  /** 服务端生效的记录与保留策略（来自环境变量，只读）。 */
  retention?: RetentionView
  /** 终端偏好（宽度保持、终端限高）：保存在服务端，所有浏览器共享。 */
  prefs?: TerminalPrefsView
}

/** 各项终端偏好的取值范围；与后端 internal/store/terminal_prefs.go 一致，0 一律表示关闭 / 不限制。 */
export interface TerminalPrefsLimits {
  fixed_cols_min: number
  fixed_cols_max: number
  record_max_lines_min: number
  record_max_lines_max: number
  /** 终端启动器预设：数量与各字段的最大长度（按字符数）。旧版 Hub 没有这几项。 */
  launcher_max?: number
  launcher_name_max?: number
  launcher_dir_max?: number
  /** 「非交互续聊」预设（#126）：各文本字段的上限（按字符数）与默认提示词（%workflow 原样保留）；旧版 Hub 没有这几项——界面据此判断 Hub 认不认识这种预设。 */
  launcher_run_prompt_max?: number
  launcher_run_model_max?: number
  launcher_run_bin_max?: number
  launcher_run_prompt_default?: string
  /** 「新开对话」运行（#176 第三期）没写自定义提示词时用的默认提示词（%workflow 原样保留）；旧版 Hub 没有——界面据此判断 Hub 认不认识「启用 Agent」。 */
  launcher_run_new_prompt_default?: string
  /** 预设「最长运行时间」的上限，也是没填时的默认值（分钟）：平台单次运行上限 7 天；旧版 Hub 没有。 */
  launcher_run_max_minutes?: number
}

export type HeadlessPosture = 'allowlist' | 'force'

/** 非交互续聊预设的对话来源（#191）：指定会话 / 新会话 / 最近会话。 */
export type LauncherSource = 'resume' | 'new' | 'continue'

/** 「新会话」运行之后的去向（#559）：记下编号 / 不切换 / 成功后切到最近会话。 */
export type LauncherAfterNew = 'remember' | 'stay' | 'continue'

/**
 * 「非交互续聊」预设的运行配置（#126）：点「运行」后由 Hub 经主机的执行通道运行一次
 * `agent -p --resume <对话> … '<提示词>'`——不开终端、没有 TUI，结果显示在预设清单里。
 * 工作流编号与对话编号至少填一个：对话编号固定要续的对话；只填工作流编号时，取这个工作流里最近一次汇报的对话。
 */
export interface LauncherRun {
  /** 可执行文件名；留空 = agent。有的主机装的是旧名字 cursor-agent。 */
  bin?: string
  /** 模型（--model）；留空 = 沿用对话原来的模型。 */
  model?: string
  /** allowlist（默认，不带 --force）| force（放开全部命令）。 */
  posture?: HeadlessPosture
  /** 智能体使用的 RelayMesh 工作流编号；运行记录与聊天里的系统行归到这个工作流。 */
  workflow_id?: string
  /** 固定要续的对话编号；留空 = 取工作流里最近一次汇报的对话。 */
  conversation_id?: string
  /** 提示词正文；留空用服务端的默认正文。支持 %workflow。 */
  prompt?: string
  /**
   * 一次运行最长跑多久（分钟，#141）；不填 = 平台单次运行上限 7 天。到点主机强制结束这次运行。
   * 填的值收进 [1 分钟, 7 天]；默认提示词会提醒智能体这个时限（自定义提示词不加）。
   */
  max_run_minutes?: number
  /**
   * 异常退出时自动续跑（#151，默认关闭）：这个预设发起的运行以 Cursor CLI 标为「可重试」的错误（如服务端限流 resource_exhausted、
   * 连接失败）或崩溃类信号退出时，Hub 退避之后自动再续聊一次——24 小时内最多 3 次，连续 2 次没跑满判定时长又失败就熔断并通知你。
   * 续跑出来的运行连续跑满判定时长（auto_resume_ok_minutes）就算续跑成功：累计的次数与退避从头算（#172）。
   * 你取消的、到运行时长上限的、需要你处理或认不出原因的失败不会重试。
   */
  auto_resume?: boolean
  /**
   * 「续跑成功」的判定时长（分钟，#172）：续跑出来的运行连续跑满这么久，就清零 24 小时内累计的续跑次数与退避；
   * 同时是「没有进展」的界线（没跑满就又失败，算一次没进展，连续 2 次熔断）。不填 = 10 分钟；填的值收进 [1 分钟, 1 天]。
   * 只在开着自动续跑时有意义。旧版 Hub 不认识它。
   */
  auto_resume_ok_minutes?: number
  /**
   * 对话来源（#191，三选一）：resume = 指定会话（命令带 --resume；填了 conversation_id 就固定接它，留空 = 取工作流里最近汇报的对话）；
   * new = 新会话（命令不带 --resume，智能体按提示词带着工作流编号接入，Hub 从运行输出里学到新对话的编号）；
   * continue = 最近会话（命令用 --continue，接着该目录里 Cursor CLI 认的最近对话——接到哪个由 CLI 决定，可能不是这个工作流的）。
   * 不带 = 存量预设，按旧字段推导（有 new_conversation 是 new，其余是 resume）。旧版 Hub 不认识它。
   */
  source?: LauncherSource
  /**
   * 「新会话」运行之后的去向（#559；只在 source 是 new 时有意义）：remember = 记下对话编号，之后的运行接着它（默认，只新开一次）；
   * stay = 不切换，每次运行都新开；continue = 运行成功后 Hub 自动把预设切成「最近会话」。旧版 Hub 不认识它。
   */
  after_new?: LauncherAfterNew
  /**
   * 旧版的「第一次运行时新开对话」勾选（#176 第三期）：存量预设仍靠它表示「新会话 · 记下编号」。设了 source 之后它只是镜像
   * （服务端按 source / after_new 同步），界面不再直接编辑它。需要填工作流编号、不能固定对话编号。
   */
  new_conversation?: boolean
  /** 预设的来源标记：enable_agent = 由会话页「启用 Agent」创建（清单里据此标出来）；手工建的没有。保存时必须原样带回去。 */
  origin?: 'enable_agent' | string
}

/**
 * 终端启动器（会话页顶栏的「终端」入口）的一条预设：「非交互续聊」——在哪台主机、哪个目录、接着哪个对话运行一次。
 * 保存在服务端、所有浏览器共享。旧版交互式「开终端」预设（kind 缺省、带命令与会话守护配置）已经停用（#245）：
 * 服务端还会把它们原样读出来，界面只认得出「这不是续聊预设」并提供删除；它们的会话守护配置不再有类型。
 */
export interface TerminalLauncher {
  id: string
  name: string
  /** 目标主机的机器码。 */
  host: string
  /** 目录；「非交互续聊」必须写完整的 / 开头（或 Windows 盘符）路径。 */
  dir?: string
  /** 旧版交互式预设的命令：只读展示（让人删除前看一眼它原来在运行什么），界面不再提供启动或编辑；#245 第三期随预设一起清理。 */
  command?: string
  /** 预设类型：headless = 非交互续聊（点「运行」由 Hub 运行一次）；缺省 = 旧版交互式预设（已停用，只能删除）。 */
  kind?: 'headless'
  /** 非交互续聊的运行配置；只有 kind 是 headless 时才有。 */
  run?: LauncherRun
}

/** 点「运行」之后服务端的回复：运行已经提交，浏览器不需要再做什么。 */
export interface LauncherRunPlan {
  name: string
  host: string
  run_id: string
  /** 刚提交时的状态：running；「需批准」的主机会停在 awaiting_approval，要到 ExecMesh 里批准。 */
  run_state: string
  posture: HeadlessPosture
  model?: string
  conversation_id: string
  /** 这次运行是新开对话的（命令里没有 --resume，#176）；旧版 Hub 没有。 */
  new_conversation?: boolean
  /** 新会话运行之后的去向（#191）：remember 记下编号 / stay 不切换 / continue 成功后切到最近会话；只在 new_conversation 时有；旧版 Hub 没有。 */
  after_new?: LauncherAfterNew
  /** 这次运行是「最近会话」（命令用 --continue，#191）：conversation_id 为空，接到哪个对话由 Cursor CLI 决定；旧版 Hub 没有。 */
  continue?: boolean
  /** 这次运行的最长运行时间（秒）：预设里设的值，没设就是平台上限 7 天。旧版 Hub 没有。 */
  max_run_seconds?: number
  /** 需要提醒你的前提（例如白名单姿态要求 Cursor 的 Mcp(...) 白名单已放行 RelayMesh）。 */
  warnings?: string[]
}

/** 一个「非交互续聊」预设最近一次运行的现状（运行记录本身在 ExecMesh 里）。 */
export interface LauncherRunView {
  run_id: string
  /** 这条运行记录所在的 ExecMesh 线程键：链到 /exec/r/<键> 看完整输出。 */
  thread_key?: string
  /** running | awaiting_approval | succeeded | failed | timeout | cancelled | rejected | denied | lost */
  state: string
  /** 还没结束（含待批准）。 */
  active: boolean
  exit_code?: number
  created_at: string
  started_at?: string
  ended_at?: string
  duration_ms?: number
  /** 这次运行的最长运行时间（秒）：到点主机强制结束它（state = timeout，界面写「达到运行时长上限」）；旧记录没有。 */
  timeout_sec?: number
  /** 从输出末尾提取的最后一条回复（已脱敏、限长、压成一行）。 */
  reply?: string
  /** 运行成功了，但期间智能体没有向 RelayMesh 发出任何消息（空转退出）。 */
  silent?: boolean
  /** 运行没有正常结束（被取消 / 被拒绝 / 启动失败 / 丢失）时的原因，一句中文；有退出码的失败与正常结束没有它；旧记录可能只有笼统的「被系统取消」。 */
  why?: string
  /** 异常退出（失败 / 崩溃）时的失败说明：Cursor CLI 自己报的错误类别、错误码与一句中文（#151）；取消、到时长上限、成功的运行没有。旧版 Hub 没有。 */
  failure?: RunFailureView
  /** 自动续跑的现状（#151）：预设没开自动续跑、这次运行也与自动续跑无关时没有；旧版 Hub 没有。 */
  auto?: AutoResumeView
  /** 这次运行是项目管理 Agent 经 preset_run 发起的（#179）：Agent 的名字；手动运行 / 自动续跑没有；旧版 Hub 没有。 */
  by?: string
  /** 这次运行接对话的方式（#191，从命令判断）：resume 指定会话 / new 新会话 / continue 最近会话；旧版 Hub 没有。 */
  mode?: LauncherSource
  /**
   * 这次运行实际接上的对话编号（#191）：指定会话就是命令里的编号；新会话与最近会话的命令里没有编号，从输出开头的 init 事件取
   * （运行还没读到、或认不出时没有）。最近会话接到哪个对话是 Cursor CLI 的决定，这里能看到实际结果。旧版 Hub 没有。
   */
  conversation?: string
}

/**
 * 一次异常退出的失败说明（#151）。class：retriable（Cursor CLI 标为可重试）/ action_required（要你处理）/ non_retriable（不可重试）/
 * crashed（进程被信号终止）/ unknown（认不出）；服务端以后可能增加新的类别，界面遇到不认识的按「认不出」处理。
 */
export interface RunFailureView {
  class: 'retriable' | 'action_required' | 'non_retriable' | 'crashed' | 'unknown' | string
  /** Cursor CLI 报出的 Connect 错误码（resource_exhausted、unavailable……）。 */
  code?: string
  /** 一句中文：发生了什么、属于哪一类。 */
  text: string
  /** 错误行原文（已脱敏）。 */
  raw?: string
  /** 这类失败值得自动重试。 */
  retry?: boolean
}

/** 自动续跑的安排的状态：待开跑 / 已开跑 / 你取消了 / 作废 / 放弃（预算用完、连续没进展、没法提交）。 */
export type AutoResumeState = 'pending' | 'started' | 'stopped' | 'skipped' | 'gaveup'

/** 一次预设运行相关的自动续跑现状：这次运行是不是自动续跑出来的、它出事之后系统怎么安排的（#151）。 */
export interface AutoResumeView {
  /** 预设里开着自动续跑。 */
  enabled: boolean
  /** 24 小时内最多自动续跑几次。 */
  max: number
  /** 「续跑成功」的判定时长（分钟，#172）：续跑出来的运行连续跑满这么久，累计的次数就清零。旧版 Hub 没有。 */
  ok_minutes?: number
  /** 这次运行本身是自动续跑出来的、而且已经连续跑满判定时长：算续跑成功，次数已清零（#172）。旧版 Hub 没有。 */
  settled?: boolean
  /** 大于 0 表示这次运行本身是自动续跑出来的：24 小时内的第几次。 */
  attempt?: number
  /** 这次运行出事之后系统的安排；没有安排就没有。 */
  state?: AutoResumeState | string
  /** 安排里这次续跑排第几次。 */
  next_attempt?: number
  /** 待开跑（pending）的开跑时间。 */
  due?: string
  /** 作废 / 放弃 / 取消的原因。 */
  why?: string
  /** 续跑出来的新运行（started）。 */
  next_run_id?: string
}

/** 「其他非交互运行」的来源：旧的会话页运行接口 / 会话守护的非交互唤醒（已下线，只剩旧记录）/ 所属预设已被删除的预设运行。 */
export type OtherRunSource = 'session_run' | 'wake' | 'deleted_preset'

/**
 * 一次不属于任何预设的 Hub 提交的非交互运行（#147）。预设清单只认「预设自己的最近一次运行」，这类运行在那里没有位置，
 * 启动器弹窗把它们单列出来（进行中的与 24 小时内结束的，最多 10 条，新的在前），可以到 ExecMesh 里查看、进行中的可以取消。
 */
export interface LauncherOtherRun extends LauncherRunView {
  /** 来源；服务端以后可能增加新的来源，界面遇到不认识的按「其他」处理。 */
  source: OtherRunSource | string
  /** 这次运行续聊的对话所在的工作流（可能没有）。 */
  workflow_id?: string
  /** 运行所在主机的机器码；界面按它换成主机名。 */
  host: string
  /** 运行的工作目录（主机上的路径）；界面据此判断它属不属于「本项目」。 */
  dir?: string
  /** 提交时写下的一句话说明，例如「会话页运行：接着对话 chat-s1 的非交互续聊」。 */
  description?: string
}

// ---------- 会话页底部「运行」（#140） ----------

/** 此刻点「运行」会被拒绝的一个原因；code 是稳定的机器可读标识。 */
export interface SessionRunIssue {
  code: string
  message: string
}

/**
 * 一条与会话「对应」的非交互续聊预设：主机、目录与这个会话一致，并且跟的就是这个会话（预设里的工作流是它的工作流，
 * 或预设里固定的对话编号就是它的对话）。确认窗展示的内容，以及此刻能不能运行，都由服务端按提交运行时同一份检查算好。
 */
export interface SessionLauncherView {
  id: string
  name: string
  host: string
  host_title?: string
  dir: string
  /** 这次运行实际要接着的对话；还不知道是空（新会话与最近会话也没有编号，见 mode）。 */
  conversation_id?: string
  /** 这次运行实际接对话的方式（#191）：resume 指定会话 / new 新会话 / continue 最近会话；旧版 Hub 没有。 */
  mode?: LauncherSource
  model?: string
  posture: HeadlessPosture
  /** 这次运行的最长运行时间（秒）。 */
  max_run_seconds: number
  /** 拼出的整行命令（只读预览）。 */
  command?: string
  /** 此刻不能运行的原因（按提交时先撞到哪个的顺序）。 */
  issues?: SessionRunIssue[]
  warnings?: string[]
  can_run: boolean
  /** 现在不能运行只是因为「对话还在运行 / 这个预设上一次运行没结束」：界面不显示「运行」，而不是显示一个灰掉的按钮。 */
  busy: boolean
  last_run: LauncherRunView | null
}

/** 没有对应预设时的一份预填：启动器「本项目」标签顶部据此给「为这个会话创建续聊预设」。 */
export interface SessionLauncherSuggest {
  name: string
  host?: string
  host_title?: string
  dir: string
  workflow_id: string
  posture: HeadlessPosture
  /** 主机没法唯一确定（会话没上报主机名，或有多台同名主机）：要先让人选一台，不能一键创建。 */
  needs_host?: boolean
}

/** GET /terminal/launchers/for-session：这个会话在哪台主机、哪个目录，有哪些对应的续聊预设，没有时怎么建议。 */
export interface SessionLaunchersView {
  workflow_id: string
  conversation_id?: string
  /** 会话上报的主机名；host 是对上主机清单后唯一确定的机器码（没法唯一确定时为空）。 */
  session_host?: string
  host?: string
  /** 名字与会话上报的主机一致的主机（机器码）：启动器「本项目」标签按它筛预设；空 = 不按主机筛。 */
  hosts: string[]
  /** 会话上报的项目目录（主机上的绝对路径）；没有或不是绝对路径时为空。 */
  dir?: string
  presets: SessionLauncherView[]
  suggest?: SessionLauncherSuggest
  /** 没有对应预设、又建议不出来时的原因。 */
  reason?: string
}

// ---------- 会话页「启用 Agent」（#176 第三期） ----------

/** 弹窗里填的选项（与服务端 sessionEnableRequest 一致）；没填的字段服务端用预设的默认——接口不会替调用方放宽权限。 */
export interface SessionEnableRequest {
  workflow_id: string
  /** 机器码；留空 = 服务端在唯一可用的主机时自动选，否则要用户选。 */
  host?: string
  /** 主机上的绝对目录（--workspace）。 */
  dir: string
  bin?: string
  model?: string
  /** 留空 = allowlist（接口默认）；弹窗预先填的是 force（Q43 b）。 */
  posture?: HeadlessPosture
  /** 可选：接着这个已有的 Cursor 对话（把它接到这个会话上）；留空 = 新开对话。 */
  conversation_id?: string
  /** 自定义提示词；留空用默认的「新开对话」提示词。 */
  prompt?: string
  /** 一次运行最长跑多久（分钟）；留空 = 平台上限 7 天。 */
  max_run_minutes?: number
  auto_resume?: boolean
}

/** 弹窗里「在哪台主机上运行」的一个候选。 */
export interface SessionEnableHost {
  machine_code: string
  hostname: string
  remark?: string
  os?: string
  online: boolean
  /** 主机的名字与会话上报的一致（待接入的会话没有上报，所以多半都是 false）。 */
  matched: boolean
}

/** 这个工作流已经启用过时的预设（再次启用会复用它，不会越点越多）。 */
export interface SessionEnablePreset {
  id: string
  name: string
}

/** 弹窗里的只读预览：不产生记录、不需要解锁；能不能点「启用并运行」与不能的原因都由服务端按真正提交时同一份检查算好。 */
export interface SessionEnablePreview {
  workflow_id: string
  title?: string
  /** 还是「待接入」（会话行全是网页新建的占位）；不是就不能启用。 */
  pending: boolean
  /**
   * 网页新建这个会话时绑定的项目目录（#184；没绑定时没有这个字段）。绑定后智能体第一次汇报带的 project_directory
   * 必须与它一致（D-161），所以没填运行目录时默认用它，填了不同的目录服务端预览会报 dir_mismatch。
   */
  bound_dir?: string
  host: string
  hosts: SessionEnableHost[]
  dir: string
  bin: string
  model: string
  posture: HeadlessPosture
  /** 这次会新开对话（命令里没有 --resume）。 */
  new_conversation: boolean
  conversation_id: string
  /** 实际交给智能体的提示词（%workflow 已替换）；default_prompt 是没写自定义提示词时的默认正文（%workflow 原样保留）。 */
  prompt: string
  default_prompt: string
  /** 将在主机上运行的完整命令（只读核对）；拼不出来时为空。 */
  command: string
  max_run_minutes: number
  auto_resume: boolean
  warnings: string[]
  issues: SessionRunIssue[]
  can_run: boolean
  preset?: SessionEnablePreset
  last_run?: LauncherRunView
}

/** POST /terminal/session-enable 的回复：运行已提交，预设已建好（或复用）。 */
export interface SessionEnableResult {
  run: LauncherRunPlan
  preset: SessionEnablePreset
  prefs: TerminalPrefsView
}

/** 「终端限高」对在线主机的覆盖情况：旧的会话守护进程不认识这项设置（升级只换 Agent、不会重启守护进程）。 */
export interface RecordLinesCoverage {
  online: number
  unsupported: number
}

export interface TerminalPrefsView {
  /** 宽度保持：大于 0 时每个终端固定为这个列数（窗口放不下就缩字号），0 表示跟随窗口。 */
  fixed_cols: number
  /** 终端限高：大于 0 时每个会话在主机上的记录至少保留最近这么多行输出，更早的滚动丢弃；0 表示不按行数限制。 */
  record_max_lines: number
  limits: TerminalPrefsLimits
  /** 仅在设了行数上限时返回。 */
  record_lines?: RecordLinesCoverage
  /** 终端启动器的预设（按保存顺序）。旧版 Hub 没有这一项。 */
  launchers?: TerminalLauncher[]
  /**
   * 「Agent 自动升级」总闸（默认关）。关着时，主机所有者在本机开启了也不会有任何检查结果。
   * 缺失 = 这台 Hub 还不认识自动升级（旧版本）。
   */
  agent_auto_upgrade?: boolean
}

export interface RetentionView {
  /** 服务端全局关闭终端记录。 */
  record_off: boolean
  /** 大于 0 时是服务端给主机记录加的上限；0 表示不额外限制（按主机本机配置，默认 30 天 / 64 MiB 每会话）。 */
  record_days: number
  record_session_mb: number
  record_host_mb: number
  /** 已结束会话在清单里的保留天数 / 模型运行记录的保留天数。 */
  index_days: number
  run_days: number
}

export interface SessionInfo {
  sid: string
  name?: string
  rows: number
  cols: number
  base: number
  end: number
  state: 'running' | 'exited' | string
  exit_code?: number
  created?: number
  attached: number
  shell?: string
  pid?: number
  alt?: boolean
  /** 录制状态：on | paused | error | off | veto | incognito；缺失表示旧版 Agent（不具备记录能力）。 */
  rec?: RecState | string
  /** 主机上保留的记录字节数（最近一次报告，仅供展示）。 */
  rec_bytes?: number
  rec_first?: number
  rec_last?: number
  rec_gap?: boolean
  /** 生效的空闲回收秒数；0 / 缺失表示空闲不回收（只对运行中的会话有意义）。 */
  idle?: number
  /** 这个会话单独设置过生命周期；否则沿用主机默认。 */
  idle_own?: boolean
  /** 最后一次交互（有输入，或最后一个浏览器断开）的 Unix 秒；有人连接时即为现在。 */
  active?: number
  /** 经 SSH 网关打开的会话：目标 SSH 主机的机器码（会话本身运行在网关主机上，shell 为 ssh）。 */
  target?: string
}

export type RecState = 'on' | 'paused' | 'error' | 'off' | 'veto' | 'incognito'

export type HostStatus = 'enabled' | 'disabled' | 'blocked'

export interface HostView {
  machine_code: string
  remark: string
  status: HostStatus
  temporary: boolean
  online: boolean
  hostname: string
  os: string
  arch: string
  agent_version: string
  /**
   * 主机上 Agent 的「Agent 侧代码指纹」（小写十六进制，缺失 = 旧版本 / 非发布构建）。版本号跟着整个仓库的构建走，服务端每热更一次就变；
   * 指纹只在 Agent 依赖的代码真的变了才变——是否「可升级」、守护进程是否「较旧」都以它为准（两边都有指纹时）。
   */
  agent_rev?: string
  privilege: string
  last_ip: string
  last_seen_at?: string
  enabled_at?: string
  sessions: SessionInfo[]
  /** 管理员在这台主机上关闭了终端记录。 */
  rec_off?: boolean
  /** 服务端全局关闭了终端记录（此时单机开关不起作用）。 */
  rec_server_off?: boolean
  /** 这台主机上会话的默认生命周期：连续没有交互这么多秒才回收；0 / 缺失表示空闲不回收。 */
  idle_sec?: number
  /** 会话守护进程状态；缺失表示旧版 Agent（会话随 Agent 进程结束）。 */
  holder?: HolderView
  duplicate_name?: boolean
  /** G2：管理员允许模型使用这台主机（默认关闭）。 */
  allow_model?: boolean
  /** 主机的执行模式：'' = 自动，approve = 需批准，readonly = 只读。 */
  exec_mode?: ExecMode | string
  /** 这台主机的「暂停模型」开关。 */
  model_paused?: boolean
  /**
   * 新模型授权的默认态：allow = 新增主机时写入现有模型令牌、新建令牌时预选；deny（缺省）= 不预先授予。
   * 只是预填规则，令牌能否使用主机始终只看令牌自己的主机名单。
   */
  default_for_new_models?: HostDefault
  /** 此刻进行中的模型运行数（含待批准）。 */
  model_running?: number
  /** 主机类型：agent（装了 Agent）或 ssh（没有 Agent，经 SSH 接入：Hub 直连与经网关都有人工终端、模型获得授权后都能执行命令；都没有文件传输）；缺失按 agent 处理。 */
  kind?: HostKind | string
  ssh?: SshView
  /** 在线的 Agent 声明了「删除主机时可同时卸载设备端」的能力（装成常驻服务运行、版本足够新、系统受支持）。 */
  can_uninstall?: boolean
  /** 服务端自带的 Agent 版本；只在服务端附带了 Agent 安装文件（并写明版本）时才有。 */
  latest_agent_version?: string
  /** 服务端自带这批 Agent 的代码指纹（分发目录的 REV 文件）；缺失 = 服务端没有写（旧镜像），此时按版本号判断。 */
  latest_agent_rev?: string
  /** 这台主机该升级：两边都有指纹时是「指纹不同」，否则是「版本号不同」。 */
  upgrade_available?: boolean
  /**
   * older：能确定比服务端自带的更旧；changed：两边的 Agent 代码确实不同（指纹），但版本号无法比较新旧（例如同一天的两次构建）；
   * different：版本号不同但无法比较新旧（本地构建 / 短哈希版本号），且至少有一边没有指纹。
   */
  upgrade_kind?: UpgradeKind
  /** 两边都有指纹且一致：版本号不同只是构建号不同，Agent 代码完全一致，不需要升级（此时不会有 upgrade_available）。 */
  up_to_date_by_rev?: boolean
  /** 这台主机的升级窗口正开着：窗口期间，带着 ticket 的安装脚本地址可以取到 Agent 安装文件。 */
  upgrade_window?: UpgradeWindowView
  /**
   * 在线 Agent 声明的「从 Web 一键升级」能力：缺失 = 不支持（旧版本 / 前台运行 / 系统暂不受支持）；
   * off = 支持，但主机所有者没有在本机允许；on = 主机所有者已允许。这个开关只能在主机上改，服务端无法替主人选择。
   */
  web_upgrade?: WebUpgradeCap
  /** 最近一次「一键升级」请求的进展与结果（完成的保留约 10 分钟，失败的保留 24 小时）。 */
  upgrade_job?: UpgradeJobView
  /** 主机本地记录的上一次升级结果（新版本没连上而被还原后，旧版本上线时会带回）。 */
  last_upgrade?: UpgradeNoteView
  /**
   * 在线 Agent 声明的「自动升级」同意：缺失 = 不支持（旧版本 / 前台运行 / 系统暂不受支持）；off = 支持但主机所有者没有开启；
   * notify = 只提醒（检查有没有新版本，不下载、不升级）；silent = 静默升级。这个开关只能在主机上改，服务端无法替主人选择。
   */
  auto_upgrade?: AutoUpgradeMode
  /** 这个 Agent 最近一次向 Hub 问「有没有新版本」的结果；还没问过则缺失。 */
  auto_check?: AutoCheckView
  /** 静默模式下 Agent 声明的「允许开始升级的时间段」（主机本地时间，HH:MM-HH:MM，可跨午夜）；没设则缺失。只供展示：是否在时间段内由 Agent 在本机判断。 */
  auto_window?: string
  /** 静默模式下最近一次升级请求得到的裁决（批准 / 没有签名 / 主机忙 …）；还没请求过则缺失。 */
  auto_grant?: AutoGrantView
}

export type AutoUpgradeMode = 'off' | 'notify' | 'silent'

/** Hub 对一次检查的回答：disabled 总闸关着；not_allowed 本机没开启；up_to_date 已是最新；available 有新版本；unavailable 暂时没有适用的程序。 */
export type AutoCheckStatus = 'disabled' | 'not_allowed' | 'up_to_date' | 'available' | 'unavailable' | 'too_soon'

export interface AutoCheckView {
  /** Hub 受理这次检查的时间（ISO）。 */
  at: string
  status: AutoCheckStatus
  /** Hub 上最新的 Agent 版本（回答 available / up_to_date 时有）。 */
  version?: string
}

/**
 * Hub 对一次静默升级请求的裁决：granted 批准（Agent 马上去下载）；unsigned 这个版本没有发布签名；busy 有会损失的会话或别的升级在进行；
 * stale 请求的版本已不是最新（Agent 会重新检查）；其余同检查的回答。凭据（票据、校验值、签名）只在发给 Agent 的帧里，不在这个视图里。
 */
export type AutoGrantStatus = 'granted' | 'disabled' | 'not_allowed' | 'up_to_date' | 'stale' | 'unavailable' | 'unsigned' | 'busy' | 'too_soon'

export interface AutoGrantView {
  /** Hub 受理这次请求的时间（ISO）。 */
  at: string
  status: AutoGrantStatus
  /** 请求要升级到的版本。 */
  version?: string
}

export type UpgradeKind = 'older' | 'different' | 'changed'

export type WebUpgradeCap = 'on' | 'off'

/** 一次「一键升级」请求的状态：requested 已发出 → downloading 下载校验 → handoff 交给升级进程（Agent 即将重启）→ done / failed。 */
export type UpgradeJobState = 'requested' | 'downloading' | 'handoff' | 'done' | 'failed'

export interface UpgradeJobView {
  id: string
  state: UpgradeJobState
  /** 请求的来源：缺省 = 管理员在 Web 上点的一键升级；auto = 主机自己发起的静默自动升级。 */
  origin?: 'auto'
  /** 超过 5 分钟没有任何进展（仅对未结束的请求）。 */
  stalled?: boolean
  from: string
  to: string
  /** 失败时的稳定代码（not_allowed / checksum / signature / download / verify / handoff / apply / rolled_back …）与说明。 */
  code?: string
  detail?: string
  /** 新版本没有连上服务端，已经自动还原成旧版本。 */
  rolled_back?: boolean
  started_at: string
  updated_at: string
}

/** 主机本地记录的一次升级结果。 */
export interface UpgradeNoteView {
  outcome: 'ok' | 'rolled_back' | 'failed' | string
  from?: string
  to?: string
  /** 结果产生的 Unix 秒（主机时钟）。 */
  at?: number
  detail?: string
}

/** 升级会影响到的东西（服务端在需要确认时回报；界面也用主机数据自己算一份，在点击前就提醒）。 */
export interface WebUpgradeSessions {
  running: number
  attached: number
  alt: number
  model_running: number
  /** 会话由独立的守护进程持有：升级只重启 Agent，不会中断它们。 */
  persistent: boolean
}

/** 一台主机的升级窗口（默认 10 分钟，可延时、可提前结束；检测到升级完成后服务端自动关闭）。 */
export interface UpgradeWindowView {
  open: boolean
  /** 只对这台主机有效的升级票据（32 位十六进制），属于凭据：不要写进日志或转发给他人。 */
  ticket: string
  until: string
  remaining_seconds: number
  /** 开窗时这台主机的 Agent 版本；之后版本变了就说明升级完成。 */
  base_version?: string
}

export type HostKind = 'agent' | 'ssh'

/** 主机对新建模型授权的默认态。 */
export type HostDefault = 'allow' | 'deny'

/** 把一台主机写入模型令牌名单的结果：granted 是被改动的令牌名，full 是名单已满、无法写入的令牌数。 */
export interface HostGrantResult {
  granted: string[]
  full: number
}

/**
 * 删除主机并请求「同时卸载设备端」的结果：
 * requested 已向在线 Agent 下发（卸载在设备上异步完成）；offline 主机不在线，没法下发；
 * unsupported 在线但 Agent 不支持（旧版本 / 不是常驻服务 / 暂不支持的系统）；failed 下发时出错。
 */
export type UninstallOutcome = 'requested' | 'offline' | 'unsupported' | 'failed'

export interface SshStatus {
  enabled: boolean
  /** no_secret_key | weak_secret_key */
  reason?: string
  message?: string
  max_hosts: number
}

export interface SshView {
  address: string
  user: string
  /** password | key */
  auth: string
  key_type: string
  fingerprint: string
  /** mismatch：出现过主机密钥不一致，需要管理员核对指纹后重新确认。 */
  key_state?: string
  has_secret: boolean
  error?: string
  checked_at?: string
  ok_at?: string
  backend_enabled: boolean
  /** 经网关接入：网关主机的机器码；缺失表示 Hub 直连。两条路径的能力一致（人工终端、模型授权后执行命令、无文件传输），区别只在谁去连、会话与凭据放在哪。 */
  gateway?: string
  /** 网关此刻可用（在线、已开启 SSH 网关、会话守护进程就绪）。 */
  gateway_ready?: boolean
  /** 登录凭据只保存在网关自己的加密存储里（Hub 没有副本，也不需要 SSH 主密钥）；Hub 直连的主机缺失。 */
  cred_on_gateway?: boolean
  /** 已确认网关上没有这台主机的凭据（例如网关换了数据目录），需要管理员重新填写。 */
  cred_missing?: boolean
  /** 登记时探测不出 POSIX 环境（需要 sh 与 base64）：只能当人工终端用，模型用不了。 */
  term_only?: boolean
  /** 承载这台主机的网关在本机用 --deny-model-exec 一票否决了模型执行，服务端无法更改。 */
  gateway_denied?: boolean
}

export interface SshProbe {
  address: string
  key_type: string
  fingerprint: string
}

export interface SshTestResult {
  ok: boolean
  code?: string
  message?: string
  hostname?: string
  os?: string
  arch?: string
  user?: string
  home?: string
  latency_ms?: number
}

export type SshAuthType = 'password' | 'key'

export interface SshAddBody {
  remark?: string
  address: string
  user: string
  auth_type: SshAuthType
  password?: string
  private_key?: string
  passphrase?: string
  /** 管理员核对过的主机密钥指纹（SHA256:…）；服务端会重新探测并比对。 */
  fingerprint: string
  /** 新增主机时是否按默认态授予现有模型令牌（缺省 deny）。 */
  default_for_new_models?: HostDefault
  /** 经哪台网关主机接入（机器码）；缺省表示 Hub 直连。 */
  gateway?: string
}

export interface SshConnBody {
  address?: string
  user?: string
  auth_type?: SshAuthType
  password?: string
  private_key?: string
  passphrase?: string
  /** 改接路径：机器码 = 改经该网关；空字符串 = 改为 Hub 直连；缺省 = 不改。 */
  gateway?: string
}

export type ExecMode = '' | 'auto' | 'approve' | 'readonly'

/** 守护进程重启后报告的、已经不存在的会话。 */
export interface LostSession {
  sid: string
  name?: string
  cwd?: string
  created?: number
  /** host_reboot | holder_stopped | holder_exit */
  reason: string
  at?: number
}

/** 会话守护进程上报的执行环境（与服务端 termproto.ExecInfo 一致）；字段都是主机自报的，只当提示用。 */
export interface HolderExecInfo {
  /** 默认 shell 的名称：bash / sh / zsh / pwsh / powershell / cmd …（Windows 上依次选 pwsh、powershell、cmd）。 */
  shell?: string
  /** 账户家目录。 */
  home?: string
  /** 路径分隔符：/ 或 \。 */
  sep?: string
}

export interface HolderView {
  /** ready | unavailable */
  state: string
  persistent: boolean
  version?: string
  /** 守护进程的「Agent 侧代码指纹」（缺失 = 旧版本守护进程）；与主机的 agent_rev 比较，判断守护进程跑的是不是旧代码。 */
  rev?: string
  started?: number
  /** state=unavailable 时的原因：connecting | superseded | version | unreachable */
  reason?: string
  lost?: LostSession[]
  /** 本机用 --deny-model-exec 一票否决了模型执行（人工终端不受影响）。 */
  deny_exec?: boolean
  /** 守护进程上报的执行环境（默认 shell、家目录、路径分隔符）；缺失表示旧版本。启动器据此决定在这台主机上怎么拼「先进目录」的那一句。 */
  exec?: HolderExecInfo
  /** 守护进程具备的额外能力（例如 exec、ssh）；缺失表示旧版本。ssh = 这台主机是 SSH 网关。 */
  caps?: string[]
}

export interface PendingView {
  machine_code: string
  hostname: string
  os: string
  arch: string
  agent_version: string
  privilege: string
  kind: 'persistent' | 'temporary' | string
  ip: string
  first_seen: string
  last_seen: string
  online: boolean
  duplicate_name?: boolean
}

export interface HostsResponse {
  hosts: HostView[]
  pending: PendingView[]
  discovery: DiscoveryView
  /** 已归档的侧栏条目 key（人工会话「机器码:会话 ID」、模型线程「r:线程键」），按归档时间倒序。 */
  archived?: string[]
}

export interface UnlockResponse {
  token: string
  expires_at: string
  idle_seconds: number
  absolute_seconds: number
}

export interface AuditLog {
  id: number
  created_at: string
  event: string
  result: 'ok' | 'fail' | 'deny' | '' | string
  actor: string
  ip: string
  machine_code: string
  session_id: string
  detail: string
}

/** 已结束会话的索引（来自 Hub 数据库，不含终端内容）。 */
export interface HistoryEntry {
  sid: string
  machine_code: string
  name?: string
  shell?: string
  /** 会话创建时间（主机时钟的 Unix 秒）。 */
  started_at?: number
  ended_at?: string
  /** exit | killed | gone | host_reboot | holder_stopped | holder_exit | idle_ttl | policy … */
  ended_reason?: string
  exit_code?: number
  rec?: RecState | string
  rec_bytes?: number
  rec_first?: number
  rec_last?: number
  rec_gap?: boolean
  /** 主机报告这个会话的记录已不存在（过期或被清理）。 */
  rec_purged?: boolean
}

export interface RecordInfoResponse {
  online: boolean
  index: HistoryEntry | null
  /** 在线时才有：主机端记录描述（见 lib/terminal/record 的 parseRecInfo）。 */
  info?: unknown
}

/** 一页记录内容：原始事件字节 + 续读游标。 */
export interface RecordPage {
  data: Uint8Array
  seg: number
  off: number
  nextSeg: number
  nextOff: number
  eof: boolean
  skipped: boolean
}

export interface TicketResponse {
  ticket: string
  expires_in: number
  ws_path: string
  protocols: string[]
}

// ---------- 模型执行 ----------

export type RunState = 'awaiting_approval' | 'running' | 'succeeded' | 'failed' | 'timeout' | 'cancelled' | 'rejected' | 'denied' | 'lost'

/** 一次模型运行（对应 Hub 的 terminal_runs 行；命令是脱敏副本，预览有长度上限）。 */
export interface RunView {
  run_id: string
  thread_key: string
  actor_kind: 'model' | 'human' | 'system' | string
  credential_id: number
  credential_name: string
  client_name?: string
  remote_ip?: string
  origin_host?: string
  workflow_id?: string
  thread?: string
  project_directory?: string
  description?: string
  target: string
  cwd?: string
  shell?: string
  command: string
  command_sha256?: string
  tools?: string
  state: RunState | string
  deny_reason?: string
  exit_code?: number
  signal?: string
  policy_mode?: string
  approved_by?: string
  approved_at?: string
  started_at?: string
  ended_at?: string
  duration_ms: number
  timeout_sec: number
  stdout_bytes: number
  stderr_bytes: number
  truncated: boolean
  middle_dropped_bytes: number
  preview_head?: string
  preview_tail?: string
  created_at: string
  updated_at: string
}

/** 模型线程的汇总（侧栏条目）。 */
export interface ThreadView {
  key: string
  target: string
  credential_id: number
  credential_name: string
  client_name?: string
  thread?: string
  workflow_id?: string
  total: number
  active: number
  awaiting: number
  first_at: string
  last_at: string
  last_run_id: string
  last_state: RunState | string
  last_title: string
}

export interface ExecStatus {
  enabled: boolean
  /** 文件能力（RELAYMESH_TERMINAL_FILES）是否在服务端开启。 */
  files_enabled?: boolean
  available: boolean
  reason?: string
  paused: boolean
  active: number
  awaiting: number
  subscribers: number
  retention_days: number
}

/** 一页完整输出（来自主机，原样；仅做 UTF-8 规整）。 */
export interface RunOutputPage {
  run_id: string
  stream: 'stdout' | 'stderr'
  offset: number
  next_offset: number
  total_bytes: number
  text: string
  eof: boolean
  running: boolean
  gap?: boolean
}

export type PauseScope = 'all' | 'host'

// ---------- 错误 ----------

/** 终端 REST 失败的统一形态：code 是 Hub 返回的稳定错误码，status 为 0 表示没有拿到响应。 */
export class TerminalApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly code: string,
    readonly data: Record<string, unknown> = {},
  ) {
    super(message)
    this.name = 'TerminalApiError'
  }
}

export function toApiError(e: unknown): TerminalApiError {
  if (e instanceof TerminalApiError) return e
  const ax = e as { response?: { status?: number; data?: Record<string, unknown> }; message?: string; code?: string } | null
  const res = ax?.response
  if (res) {
    const data = (res.data && typeof res.data === 'object' ? res.data : {}) as Record<string, unknown>
    const code = typeof data.code === 'string' ? data.code : ''
    const msg = typeof data.error === 'string' && data.error ? data.error : `请求失败（HTTP ${res.status ?? 0}）`
    return new TerminalApiError(msg, res.status ?? 0, code, data)
  }
  const timeout = ax?.code === 'ECONNABORTED'
  return new TerminalApiError(timeout ? '请求超时，请稍后重试' : '无法连接到服务端，请检查网络', 0, timeout ? 'timeout' : 'network')
}

/** 用户在解锁对话框里取消时抛出；带 cancelled 标记，连接层据此判断「不是故障，需要人工处理」。 */
export class UnlockCancelled extends Error {
  readonly cancelled = true
  readonly code = 'unlock_required'
  constructor() {
    super('已取消解锁')
    this.name = 'UnlockCancelled'
  }
}

// ---------- 请求 ----------

const enc = encodeURIComponent

/** 启用主机的请求体：两个字段都缺省时不发请求体（服务端据此保持原备注与默认态）。 */
function enableBody(remark?: string, defaultForNewModels?: HostDefault): { remark?: string; default_for_new_models?: HostDefault } | undefined {
  const body: { remark?: string; default_for_new_models?: HostDefault } = {}
  if (remark !== undefined) body.remark = remark
  if (defaultForNewModels !== undefined) body.default_for_new_models = defaultForNewModels
  return Object.keys(body).length ? body : undefined
}

async function call<T>(p: Promise<{ data: T }>): Promise<T> {
  try {
    return (await p).data
  } catch (e) {
    throw toApiError(e)
  }
}

/**
 * 记录读取要穿过 Hub 读主机的磁盘，主机端同时只服务 2 个读取。按主机排队，并在「主机正忙」时退避重试，
 * 这样几个回放标签同时加载（刷新、主机重新上线）时只是稍慢，不会互相挤出错误。
 */
const SSH_TIMEOUT_MS = 45_000

const recordGate = new ReadGate({ isBusy: e => (e as { code?: string } | null)?.code === 'busy' })

export const terminalApi = {
  status: () => call(api.get<TerminalStatus>('/terminal/status')),

  /** 宽度保持：0 关闭，否则固定为该列数。只影响浏览器怎么上报终端尺寸，登录即可。 */
  setDisplayPrefs: (fixedCols: number) =>
    call(api.put<{ prefs: TerminalPrefsView }>('/terminal/prefs/display', { fixed_cols: fixedCols })),
  /** 终端限高：0 不按行数限制，否则每个会话至少保留最近这么多行，更早的滚动丢弃。会让主机丢弃旧内容，需要解锁（并写审计）。 */
  setRetentionPrefs: (recordMaxLines: number) =>
    call(api.put<{ prefs: TerminalPrefsView }>('/terminal/prefs/retention', { record_max_lines: recordMaxLines })),
  /** Agent 自动升级总闸（默认关）。它只是让 Hub 肯回答「有没有新版本」，真正要不要升级由主机所有者在本机决定；改动需要解锁并写审计。 */
  setAutoUpgradePrefs: (enabled: boolean) =>
    call(api.put<{ prefs: TerminalPrefsView }>('/terminal/prefs/auto-upgrade', { enabled })),

  /**
   * 新建或覆盖一条「非交互续聊」预设（编号由界面生成）。预设决定人点「运行」之后会运行什么，所以和运行一样需要解锁。
   * 命令内容不进审计。保存是整条覆盖：kind / run 必须带回去（服务端不再接受交互式预设：缺省 kind 一律 400 launcher_interactive_retired）。
   */
  saveLauncher: (l: TerminalLauncher) =>
    call(api.put<{ prefs: TerminalPrefsView }>(`/terminal/launchers/${enc(l.id)}`, {
      name: l.name, host: l.host, dir: l.dir ?? '', kind: l.kind ?? '', run: l.run ?? null,
    })),
  /** 删除一条预设；它已经不在了（别处删过）服务端回 404 launcher_not_found。 */
  deleteLauncher: (id: string) => call(api.delete<{ prefs: TerminalPrefsView }>(`/terminal/launchers/${enc(id)}`)),

  /**
   * 运行一次「非交互续聊」预设（#126）：服务端经主机的执行通道提交一次 `agent -p --resume …`，不开终端。
   * 要求刚刚解锁（它会让主机运行命令）；预设不存在 404 launcher_not_found，不是续聊预设 409 not_headless，
   * 其余前置条件不满足（主机离线、对话还在运行、上一次还没跑完……）回 409 + 可读原因，不会产生运行记录。
   */
  runLauncher: (id: string) => call(api.post<{ run: LauncherRunPlan }>(`/terminal/launchers/${enc(id)}/run`)),
  /**
   * 各「非交互续聊」预设最近一次运行的现状（登录即可读；从没运行过的预设没有条目），以及不属于任何预设的非交互运行（others，#147）。
   * 旧版 Hub 没有这个接口（404）；有接口但还没有 others 的版本不带这个字段。
   */
  launcherRuns: () => call(api.get<{ runs: Record<string, LauncherRunView>; others?: LauncherOtherRun[] }>('/terminal/launchers/runs')),
  /**
   * 取消一次排期中的自动续跑（#151）；runId 是出事的那次运行。取消只会让系统少做一件事：登录即可，不需要解锁。
   * 已经开跑、已经作废或你取消过的回 409 not_pending。
   */
  cancelAutoResume: (runId: string) => call(api.post<{ ok: boolean }>(`/terminal/launchers/runs/${enc(runId)}/auto/cancel`)),

  /**
   * 这个会话对应的非交互续聊预设（#140）：会话页底部「运行」按钮与启动器「本项目」标签用它。
   * 只读：登录即可，不产生运行记录；真正运行走 runLauncher（要刚刚解锁）。旧版 Hub 没有这个接口（404）。
   */
  sessionLaunchers: (workflowId: string) =>
    call(api.get<{ session: SessionLaunchersView }>('/terminal/launchers/for-session', { params: { workflow_id: workflowId } })),

  /**
   * 「启用 Agent」的只读预览（#176 第三期）：按弹窗里填的选项算出会在哪台主机、哪个目录新开对话、拼出的命令是什么、
   * 现在能不能点「启用并运行」与不能的原因。登录即可，不产生记录。旧版 Hub 没有这个接口（404）。
   */
  sessionEnablePreview: (body: SessionEnableRequest) =>
    call(api.post<{ preview: SessionEnablePreview }>('/terminal/session-enable/preview', body)),
  /**
   * 启用 Agent：为这个「待接入」的会话建（或复用）一条非交互续聊预设并运行它——在主机上无人值守地新开一个 Cursor CLI 对话，
   * 智能体带着工作流编号接入。要求刚刚解锁（它会让主机运行命令）；不是待接入 409 not_pending，已有智能体在运行 409 conversation_live，
   * 上一次还没结束 409 run_active，预设满了 409 launcher_limit，其余前置条件不满足回 409/400 + 可读原因，不会留下半成品预设。
   */
  sessionEnable: (body: SessionEnableRequest) => call(api.post<SessionEnableResult>('/terminal/session-enable', body)),

  unlock: (password: string) => call(api.post<UnlockResponse>('/terminal/unlock', { password })),
  lock: () => call(api.post<{ ok: boolean }>('/terminal/lock')),
  keepalive: () => call(api.post<{ remaining_seconds: number }>('/terminal/unlock/keepalive')),

  hosts: () => call(api.get<HostsResponse>('/terminal/hosts')),
  setRemark: (code: string, remark: string) => call(api.post<{ ok: boolean }>(`/terminal/hosts/${enc(code)}/remark`, { remark })),
  /** defaultForNewModels 只在新增主机（待启用条目首次启用）时生效；响应的 grant 说明授予了哪些模型令牌。 */
  enable: (code: string, remark?: string, defaultForNewModels?: HostDefault) =>
    call(api.post<{ host: HostView; grant?: HostGrantResult }>(`/terminal/hosts/${enc(code)}/enable`, enableBody(remark, defaultForNewModels))),
  ignore: (code: string) => call(api.post<{ ok: boolean }>(`/terminal/hosts/${enc(code)}/ignore`)),
  unignore: (code: string) => call(api.post<{ ok: boolean }>(`/terminal/hosts/${enc(code)}/unignore`)),
  disable: (code: string) => call(api.post<{ ok: boolean }>(`/terminal/hosts/${enc(code)}/disable`)),
  /** uninstall=true：同时请求在线的 Agent 卸载自己；响应里的 uninstall 说明设备上到底发生了什么。 */
  remove: (code: string, opts?: { uninstall?: boolean }) =>
    call(api.delete<{ ok: boolean; uninstall?: UninstallOutcome }>(`/terminal/hosts/${enc(code)}${opts?.uninstall ? '?uninstall=1' : ''}`)),

  /**
   * 打开发现窗口。extend=true 表示「续期」：在剩余时间上累加（窗口没开着时等同于打开），
   * 不带它则是重新计时，且会让之前签发的安装命令失效。
   */
  openDiscovery: (minutes?: number, extend?: boolean) => {
    const body: { minutes?: number; extend?: boolean } = {}
    if (minutes) body.minutes = minutes
    if (extend) body.extend = true
    return call(api.post<{ discovery: DiscoveryView }>('/terminal/discovery', Object.keys(body).length ? body : undefined))
  },
  closeDiscovery: () => call(api.delete<{ discovery: DiscoveryView }>('/terminal/discovery')),

  /** 开启一台主机的升级窗口（默认 10 分钟）；extend=true 是「延时」：在剩余时间上累加，已签发的升级命令继续有效。 */
  openUpgradeWindow: (code: string, minutes?: number, extend?: boolean) => {
    const body: { minutes?: number; extend?: boolean } = {}
    if (minutes) body.minutes = minutes
    if (extend) body.extend = true
    return call(api.post<{ upgrade_window: UpgradeWindowView }>(`/terminal/hosts/${enc(code)}/upgrade-window`, Object.keys(body).length ? body : undefined))
  },
  /** 提前结束升级窗口：票据立刻失效。 */
  closeUpgradeWindow: (code: string) => call(api.delete<{ upgrade_window: null }>(`/terminal/hosts/${enc(code)}/upgrade-window`)),
  /**
   * 请这台主机的 Agent 自己升级到服务端自带的版本（需要 Agent 在线、主机所有者已在本机允许）。
   * 有正在运行的会话或模型运行时服务端先回 409 sessions_active（data.sessions 是摘要），带上 confirmSessions 才会继续。
   */
  webUpgrade: (code: string, confirmSessions = false) =>
    call(api.post<{ upgrade_job: UpgradeJobView }>(`/terminal/hosts/${enc(code)}/web-upgrade`, confirmSessions ? { confirm_sessions: true } : undefined)),

  /** 归档 / 取消归档侧栏条目（服务端保存，多台设备共享；会话本身不受影响）。 */
  archive: (keys: string[], archived: boolean) => call(api.post<{ ok: boolean; changed: number }>('/terminal/archive', { keys, archived })),

  /**
   * 忽略主机页「有会话已不存在」的提示：Hub 记住，守护进程之后再报同样的丢失也不会回来；
   * 不删除任何会话记录，不要求主机在线。all=true 忽略这台主机当前显示的全部提示。
   */
  dismissLost: (code: string, body: { sids?: string[]; all?: boolean }) =>
    call(api.post<{ ok: boolean; changed: number }>(`/terminal/hosts/${enc(code)}/lost/dismiss`, body)),

  sessions: (code: string) => call(api.get<{ online: boolean; sessions: SessionInfo[] }>(`/terminal/hosts/${enc(code)}/sessions`)),
  openSession: (
    code: string,
    body: { name?: string; rows?: number; cols?: number; incognito?: boolean; idle_seconds?: number; cwd?: string; ssh_target?: string },
    // 经网关打开 SSH 会话要等网关拨号 + 认证，比本机 shell 慢得多。
  ) => call(api.post<{ session: SessionInfo }>(`/terminal/hosts/${enc(code)}/sessions`, body, body.ssh_target ? { timeout: SSH_TIMEOUT_MS } : undefined)),
  /** 修改主机默认的会话生命周期（0 = 空闲不回收）；只影响没有单独设置的会话。 */
  setHostLifecycle: (code: string, idleSeconds: number) =>
    call(api.post<{ host: HostView }>(`/terminal/hosts/${enc(code)}/lifecycle`, { idle_seconds: idleSeconds })),
  /** 修改一个会话自己的生命周期：null = 改回沿用主机默认，0 = 空闲不回收。 */
  setSessionLifecycle: (code: string, sid: string, idleSeconds: number | null) =>
    call(api.post<{ ok: boolean }>(`/terminal/hosts/${enc(code)}/sessions/${enc(sid)}/lifecycle`, { idle_seconds: idleSeconds })),
  renameSession: (code: string, sid: string, name: string) =>
    call(api.post<{ ok: boolean }>(`/terminal/hosts/${enc(code)}/sessions/${enc(sid)}/name`, { name })),
  killSession: (code: string, sid: string) => call(api.post<{ ok: boolean }>(`/terminal/hosts/${enc(code)}/sessions/${enc(sid)}/kill`)),
  ticket: (code: string, sid: string, body: { since: number; rows: number; cols: number; tail_lines?: number }) =>
    call(api.post<TicketResponse>(`/terminal/hosts/${enc(code)}/sessions/${enc(sid)}/ticket`, body)),

  audit: (params: { limit?: number; before?: number } = {}) => call(api.get<{ logs: AuditLog[] }>('/terminal/audit', { params })),

  // ---------- 终端记录（内容在主机上，Hub 只转发） ----------
  history: (code: string) => call(api.get<{ online: boolean; sessions: HistoryEntry[] }>(`/terminal/hosts/${enc(code)}/history`)),
  recordInfo: (code: string, sid: string) =>
    recordGate.run(code, () => call(api.get<RecordInfoResponse>(`/terminal/hosts/${enc(code)}/sessions/${enc(sid)}/record`))),
  recordPage: (code: string, sid: string, opts: { seg?: number; off?: number; max?: number; download?: boolean; signal?: AbortSignal } = {}) =>
    recordGate.run(code, () => fetchRecordPage(code, sid, opts), opts.signal),
  setRecording: (code: string, off: boolean) => call(api.post<{ host: HostView }>(`/terminal/hosts/${enc(code)}/recording`, { off })),

  // ---------- 模型执行 ----------
  execStatus: () => call(api.get<ExecStatus>('/terminal/exec/status')),
  execThreads: (params: { target?: string; credential?: number; limit?: number } = {}) =>
    call(api.get<{ threads: ThreadView[] }>('/terminal/exec/threads', { params })),
  execRuns: (params: { thread?: string; target?: string; credential?: number; state?: string; before?: number; limit?: number } = {}) =>
    call(api.get<{ runs: RunView[]; cursor: number }>('/terminal/exec/runs', { params })),
  execRun: (id: string) => call(api.get<{ run: RunView }>(`/terminal/exec/runs/${enc(id)}`)),
  execCancel: (id: string) => call(api.post<{ run: RunView }>(`/terminal/exec/runs/${enc(id)}/cancel`)),
  execReject: (id: string) => call(api.post<{ run: RunView }>(`/terminal/exec/runs/${enc(id)}/reject`)),
  execApprove: (id: string) => call(api.post<{ run: RunView }>(`/terminal/exec/runs/${enc(id)}/approve`)),
  execOutput: (id: string, params: { stream: 'stdout' | 'stderr'; offset?: number; limit?: number }) =>
    call(api.get<RunOutputPage>(`/terminal/exec/runs/${enc(id)}/output`, { params })),
  execPause: (scope: PauseScope, machineCode?: string) => call(api.post<ExecStatus>('/terminal/exec/pause', { scope, machine_code: machineCode })),
  execResume: (scope: PauseScope, machineCode?: string) => call(api.post<ExecStatus>('/terminal/exec/resume', { scope, machine_code: machineCode })),
  setHostModel: (code: string, body: { allow_model?: boolean; exec_mode?: ExecMode | string; default_for_new_models?: HostDefault }) =>
    call(api.post<{ host: HostView }>(`/terminal/hosts/${enc(code)}/model`, body)),
  /** 预览「把这台主机授予现有模型令牌」会改动哪些令牌（只读；待启用的主机也可以预览）。 */
  grantModelsPreview: (code: string) => call(api.get<{ grant: HostGrantResult }>(`/terminal/hosts/${enc(code)}/grant-models`)),
  /** 把这台主机写入现有模型令牌的主机名单（只增不减；通配令牌与能力 URL 令牌不动）。 */
  grantModels: (code: string) => call(api.post<{ grant: HostGrantResult }>(`/terminal/hosts/${enc(code)}/grant-models`)),

  // ---------- SSH 主机 ----------
  // 连接远端要等握手，超时给宽一些（服务端自己的拨号超时更短）。
  sshProbe: (address: string, gateway?: string) =>
    call(api.post<{ probe: SshProbe }>('/terminal/ssh/probe', { address, gateway: gateway || undefined }, { timeout: SSH_TIMEOUT_MS })),
  sshAdd: (body: SshAddBody) => call(api.post<{ host: HostView; grant?: HostGrantResult }>('/terminal/ssh/hosts', body, { timeout: SSH_TIMEOUT_MS })),
  sshTest: (code: string) => call(api.post<{ result: SshTestResult }>(`/terminal/hosts/${enc(code)}/ssh/test`, undefined, { timeout: SSH_TIMEOUT_MS })),
  sshProbeHost: (code: string) => call(api.post<{ probe: SshProbe }>(`/terminal/hosts/${enc(code)}/ssh/probe`, undefined, { timeout: SSH_TIMEOUT_MS })),
  sshUpdateConnection: (code: string, body: SshConnBody) =>
    call(api.post<{ host: HostView }>(`/terminal/hosts/${enc(code)}/ssh/connection`, body, { timeout: SSH_TIMEOUT_MS })),
  sshRekey: (code: string, fingerprint: string) =>
    call(api.post<{ host: HostView }>(`/terminal/hosts/${enc(code)}/ssh/rekey`, { fingerprint }, { timeout: SSH_TIMEOUT_MS })),
}

/** 读取一页记录。响应是二进制事件流，游标在响应头里；失败时 Hub 返回的 JSON 错误体同样要解出来。 */
async function fetchRecordPage(
  code: string,
  sid: string,
  opts: { seg?: number; off?: number; max?: number; download?: boolean; signal?: AbortSignal },
): Promise<RecordPage> {
  try {
    const res = await api.get<ArrayBuffer>(`/terminal/hosts/${enc(code)}/sessions/${enc(sid)}/record/data`, {
      params: { seg: opts.seg ?? 0, off: opts.off ?? 0, max: opts.max, purpose: opts.download ? 'download' : undefined },
      responseType: 'arraybuffer',
      signal: opts.signal,
      timeout: 30_000,
    })
    const h = res.headers
    const n = (name: string) => Number(h[name] ?? 0) || 0
    return {
      data: new Uint8Array(res.data),
      seg: n('x-rec-seg'),
      off: n('x-rec-off'),
      nextSeg: n('x-rec-next-seg'),
      nextOff: n('x-rec-next-off'),
      eof: String(h['x-rec-eof']) === 'true',
      skipped: String(h['x-rec-skipped']) === 'true',
    }
  } catch (e) {
    const ax = e as { response?: { data?: unknown } }
    // 错误体也是按二进制收下的：先还原成 JSON 再交给统一的错误转换。
    if (ax?.response?.data instanceof ArrayBuffer) {
      try {
        ax.response.data = JSON.parse(new TextDecoder().decode(ax.response.data))
      } catch {
        ax.response.data = {}
      }
    }
    throw toApiError(e)
  }
}
