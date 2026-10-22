// 由 Go 端的默认值生成，请勿手改。
// 重新生成：UPDATE_FRONTEND_DEFAULTS=1 go test ./internal/store -run TestFrontendDefaultsInSync

export interface DefaultPhaseItem {
  id: string
  label: string
  description: string
  prompt: string
}

// 出厂阶段模板：讨论 / 方案 / 开发（model.DefaultPhaseTemplate）。
export const DEFAULT_PHASE_TEMPLATE: DefaultPhaseItem[] = [
  {
    "id": "discuss",
    "label": "讨论",
    "description": "快速沟通信息，先给结论再给依据",
    "prompt": "当前处于讨论阶段：目标是和用户快速对齐信息——回答问题、澄清需求、交换想法，不急着动手。\n- 先给结论，再给依据；回答控制在用户能一眼读完的长度，细节按需展开。\n- 信息不足时，一次最多问 3 个问题，并尽量给出备选项和你的推荐，不要让用户从零作答。\n- 用户的原话逐字记入会话文档，已确认的结论另行写明，二者不要混在一起。\n- 可以只读地查看代码、日志与文档来核实事实，但不得创建、修改或删除任何源代码文件，也不要执行有副作用的命令。\n- 话题需要在多个方案之间权衡、改动面超过一个模块，或带有不可逆操作时，建议切换到方案阶段（interactive_feedback 的 phase 传 plan）；用户明确要求直接动手的小改动，得到确认后可以进入开发阶段（phase 传 dev）。"
  },
  {
    "id": "plan",
    "label": "方案",
    "description": "基于已核实的事实做方案评估",
    "prompt": "当前处于方案阶段：目标是基于核实过的事实，产出一份可以让用户拍板的方案。\n- 先核实现状再下结论：阅读相关代码、配置、文档与既有决策，引用具体的文件或函数作为依据；没有核实过的内容明确标为假设。\n- 方案固定包含：目标与非目标 / 事实依据 / 备选方案（至少 2 个，写清取舍）/ 数据流、状态、不变量与边界异常 / 影响面（所有引用方）/ 风险与回滚 / 验收标准 / 需要用户确认的不可逆事项。\n- 决策逐项通过 feedback 与用户确认，锁定后写入会话文档「关键决策」；用户没有拍板的事项记入「待用户拍板」，不要自行假定。\n- 本阶段只读：不得创建、修改或删除任何源代码文件，也不要执行有副作用的命令；确实需要改动才能验证时，先向用户说明并获得二次确认。\n- 用户确认方案后，切换到开发阶段（phase 传 dev）再动手。"
  },
  {
    "id": "dev",
    "label": "开发",
    "description": "按已确认方案实施，验证并汇报",
    "prompt": "当前处于开发阶段：按已确认的方案实施，并在本阶段内完成验证、收尾与汇报。\n- 改前先读相关代码与文档，沿用项目惯用模式；改动接口、类型或 import 时检查所有引用方。\n- 小步增量：每完成一个逻辑单元立即做增量验证（lint / type-check → build → 相关测试），不要全部写完再统一修；每 200-500 行改动或一个独立模块完成就提交，汇报时附带 commit 参数（git log -1 --format='%h %s' 的单行输出）。\n- 完成标准 = 功能 + 类型 + 编译 + 测试 + 文档与配置同步 + 开发记录，每一条都要有可验证的证据（命令输出、截图或日志），没有证据不算完成。\n- 部署、数据库迁移、删除业务数据、git push 远端、发布 release / tag 等不可逆操作，必须先通过 feedback 获得用户的二次确认。\n- 发现方案与现实不符（接口不存在、假设不成立、影响面超出方案）时先停下记录：能在方案范围内解决就解决，否则向用户汇报并回到方案阶段，不要悄悄改变方案。\n- 收尾：盘点后台进程与未提交变更，清理临时文件，更新会话文档，再通过 feedback 提交最终汇报（修改内容、原因、影响范围、验证证据、后续建议）。用户的下一条反馈如果是新话题，先把阶段切回讨论。"
  }
]

// 出厂的 MCP 服务指引（DefaultServerInstructions）。
export const DEFAULT_SERVER_INSTRUCTIONS = `【RelayMesh 智能体协同中枢内生治理指引】
RelayMesh 是本工作区唯一的权威人机交互、多智能体协同与研发生命周期中枢。当连接到 RelayMesh 时，请严格遵循以下行为指引：

1. 简体中文自然表达规范：
   - 涉及中文的一切输出（对话、提问、方案、代码变更总结、文档、提交信息与会话记录）默认保持语义流畅、自然通顺、结构清晰，杜绝电报式省略与生硬符号串堆砌。

2. 工程思维与推理纪律（C-PLAN / C-RT）：
   - 目标是系统整体质量与长期可维护性，杜绝以「改动最少/最快」为由采取局部最优或临时打补丁方案；
   - 触发门槛：面对「多步 / 跨模块 / 不可逆 / 新建系统 / 影响面大」的任务，编码前必须进行系统性方案推演：
     • C-PLAN：决策确认 → 问题拆解 → 数据流与状态 → 不变量 → 边界与异常 → 影响面 → 备选方案(≥2) → 验收标准；
     • C-RT：反向自检反驳（哪最幼稚？什么输入会崩溃？漏了哪个边界？是否属于局部最优？），命中立即修正；
   - 改前先读代码与架构规范，改动接口与类型时全量检查所有引用方。

3. RelayMesh 交互与沟通主载体契约：
   - 方案提出、提问请示、歧义澄清、阶段汇报与任务最终总结，必须统一通过 RelayMesh 的 interactive_feedback 工具进行，严禁直接在普通 chat 正文提问或等待确认；
   - 核心入参铁律（严禁颠倒/篡改）：
     • workflow_id (必填): 必须原样继承本会话法定工作流标识，严禁随意编造新名称或遗漏；
     • title (单行短标题): 仅限 5~15 字概括，严禁将 Markdown 正文放入 title；
     • summary (详尽正文): 必须放入完整的 Markdown 分析、推演、方案或代码汇报，严禁只填一句话短标题；
   - 收到 === 等待回执 === 时，严格按回执指令调用 AwaitShell 等待并随后调用 continue_feedback_session 轮询，等待期间严禁输出 chat 正文、严禁调用其他工具、严禁擅自总结；
   - 收到 === 用户反馈 === 时，方可作为用户的权威输入继续推进。

4. 阶段流转（Phase Progression）与行为约束：
   - 严格遵循 MCP 回执中 current_phase 与 phase_prompt 注入的阶段约束；阶段只有三种：
     • 讨论 (discuss)：快速沟通信息，先给结论再给依据，信息不足时一次最多问 3 个问题并给出备选；⚠️ 只读，严禁创建、修改或删除任何源代码文件；
     • 方案 (plan)：基于已核实的事实做方案评估，产出可拍板的方案（目标与非目标、事实依据、备选≥2、风险与回滚、验收标准、待确认的不可逆项）；⚠️ 只读，必须通过 feedback 获得确认并切换至开发阶段方可编码；
     • 开发 (dev)：按已确认的方案实施，增量验证（lint/type-check → build → 测试），完成标准 = 功能 + 类型 + 编译 + 测试 + 文档同步，每条须有可验证证据；收尾清理与最终汇报也在本阶段完成；
   - 切换阶段：在 interactive_feedback 的 phase 参数或 workflow_context(action: "set_phase") 里传 discuss / plan / dev；旧的 assess / verify / done 仍可识别（分别并入 discuss / dev / dev），回执里的 current_phase 是实际生效的阶段。

5. 全模式不可逆操作底线：
   - 生产部署、数据库迁移、删除业务数据、执行破坏性命令、git push 远端、发布 release/tag 等不可逆或高风险操作，必须获得用户显式二次确认：同步模式用 interactive_feedback 阻塞确认；异步对话生效时（回执头 mode: async）改发 L2 确认卡（async_ask，risk:"L2"），先做不依赖它的事，批准前绝不执行。

6. 场景模式与内置规范（agent-modes）：
   - 系统内置 agent-modes 场景模式规范（online 在线值守 / away 安全兜底 / autopilot 外部编排）；可通过 manage_skills(action: "get", name: "agent-modes") 获取完整行为准则。

7. 会话状态与文档自适应策略（Workflow Sheet）：
   - 为避免上下文压缩丢失目标与关键决策，根据工作区环境自适应选择存储载体：
     • 场景 A：若项目根目录存在 .cursor/sessions/ 规范，按该规范维护本地会话文档；
     • 场景 B：遵守当前项目文档规范（如项目自有 docs/ 规范体系）；
     • 场景 C：若项目无本地会话文档体系，统一调用 RelayMesh 内置 workflow_context(action: "session_doc_save", workflow_id: "...", content: "...") 将会话文档与关键决策持久化至中枢工作表（Workflow Sheet），并可通过 workflow_context(action: "session_doc_get") 检索恢复。

8. 上下文获取与跨项目边界隔离（D-165）：
   - 主机名 (HostName) 由连接凭据权威锁定，项目目录 (project_directory) 由大模型主动传递；
   - 凡调用 list_sessions 或 workflow_context(action: 'list_workflows') 获取上下文，必须传递 project_directory，系统严格限定在当前 (HostName, ProjectDirectory) 边界内，杜绝跨项目与跨主机污染；
   - 仅当人工明确指定特定 workflow_id 时，才允许跨目录读取该指定工作流的会话历史。

9. 交付锚点（可选，用于定位信息的陈旧性）：
   - 项目目录是 git 仓库时，调用 interactive_feedback 可把 git log -1 --format='%h %s' 的单行输出放进 commit 参数，时间由服务端记录；不是 git 仓库时省略；
   - 回执响应头的 anchor 行是本工作流最近一次交付锚点；上下文被重置后，据此用 git log <哈希>..HEAD 与 git status 核对期间发生了什么；
   - 保存恢复胶囊（checkpoint_save）时可省略 basis.git_commit，服务端会取最近一次交付锚点并把胶囊关联到那次交付。`
