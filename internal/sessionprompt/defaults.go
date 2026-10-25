package sessionprompt

import "strings"

// 内置默认模板（v2，#232 D3）。预设、会话、全局设置都没写自己的模板时用它们；用户随时可以在 Web 里改（会话级 / 全局级）。
//
// 写法上的几条原则：
//   - 没人在终端里看输出的运行，结果必须发到 RelayMesh，「等我回复」在这里等于结束本次运行——要写明，免得它一句话不说就悄悄退出；
//   - 带上「现状」（未读追加、未完成任务、检查点的下一步）与常驻规则，醒来就知道在哪、该守什么，不必先调好几个工具摸底；
//   - 权限沿用本会话此前的授权范围（以回执 auth 行为准），不自行放宽——唤醒是同一场对话的延续，不是新的授权；
//   - 凡是需要用户决策 / 拍板 / 提供输入的事，一律发确认卡（async_ask）并写 Q 编号，不要只写在汇报里（Q84）；
//   - 目录是 git 仓库才核对 git 状态，不再对所有目录一律要求。

const (
	// resumeTemplate 是非交互续聊的默认模板。
	resumeTemplate = `这是一次非交互续聊：没有人在终端里看你的输出，所以结果必须发到 RelayMesh。
工作流 %workflow（%title），项目目录 %project，模式 %mode，授权档位 %auth，当前阶段 %phase。
现状（发出这条提示词时由 Hub 现算）：未读追加指令 %inbox 条；未完成任务 %tasks；上次检查点的下一步：%next；最近交付：%anchor。
常驻规则：%resident
请先恢复现场：用 RelayMesh 的 workflow_context inbox 读取工作流 %workflow 里我离线期间的追加指令，用 task_board 查看任务清单，再按清单继续；目录是 git 仓库的话先核对 git status 和 git log，不要重复已完成的步骤。
恢复后先用 interactive_feedback 按回执里的异步规则发一条进度通知说明现状（不要阻塞等回复），再继续：任务进行中就继续执行，已交付或在等我回复就通过 RelayMesh 沟通。凡是需要我决策、拍板或提供输入的事（包括置「暂缓」的任务在等的东西），一律发确认卡（async_ask）并写 Q 编号，不要只写在汇报里，也不要停下来等。
无论有没有事可做，结束前都要向 RelayMesh 发一条消息说明结果。权限沿用本会话此前的授权范围（以回执 auth 行为准），不自行放宽，超出的先向我确认。仍无法继续就说明原因后结束本次运行，不要重复同样的尝试。`

	// newTemplate 是非交互新开对话的默认模板：全新的对话不认识这个工作流，先带着工作流编号接进来，
	// 与网页新建会话时给用户的启动提示词是同一套「接入」步骤。
	newTemplate = `这是一次无人值守的新对话：没有人在终端里看你的输出，所以结果必须发到 RelayMesh。
请接入 RelayMesh 的工作流（workflow_id：%workflow，标题：%title），项目目录 %project，模式 %mode，授权档位 %auth。
现状（发出这条提示词时由 Hub 现算）：未读追加指令 %inbox 条；未完成任务 %tasks；上次检查点的下一步：%next。
常驻规则：%resident
1. 先调用 RelayMesh 的 interactive_feedback 汇报一次（异步模式下用 wait:false），参数里必须带 workflow_id "%workflow"，project_directory 填你当前工作区的绝对路径；不要另起别的 workflow_id，也不要省略它。
2. 我可能已经留了第一条指令：第 1 步的回执里带回来了就直接执行；没带回来就调用 workflow_context 的 inbox（workflow_id 同上）读取；仍然没有，就汇报「已接入，等待指示」后结束本次运行。
3. 之后所有的汇报、提问、确认都沿用这个 workflow_id，并按 RelayMesh 回执里的指引继续；凡是需要我决策、拍板或提供输入的事一律发确认卡（async_ask）并写 Q 编号；「等我回复」在这里等于结束本次运行，所以无论有没有事可做，结束前都要向 RelayMesh 发一条消息说明结果。
权限以回执 auth 行为准，不自行放宽，超出的先向我确认。`
)

// Builtin 返回某类唤醒词的内置默认模板（带 %变量，未渲染）。
// hasWorkflow 为 false（预设没配工作流、或编号不能写进提示词）时，续聊的默认模板去掉点名工作流的整行，
// 免得出现「工作流 （）」之类的残句；新开对话本来就必须带工作流编号，没有工作流时与升级前一样原样渲染。
// 网页启动提示词（KindWebStart）的默认正文由 store.WebWorkflowStartPrompt 生成（它要按「有没有绑定目录」分支），这里返回空串。
func Builtin(kind Kind, hasWorkflow bool) string {
	var t string
	switch kind {
	case KindResume:
		t = resumeTemplate
	case KindNew:
		t = newTemplate
	default:
		return ""
	}
	if hasWorkflow || kind == KindNew {
		return t
	}
	return withoutWorkflow(t)
}

// withoutWorkflow 去掉默认模板里依赖工作流编号的部分。只对内置模板做，用户自己写的模板不做任何改写。
func withoutWorkflow(t string) string {
	var keep []string
	for _, line := range strings.Split(t, "\n") {
		switch {
		case strings.HasPrefix(line, "工作流 %workflow（"), strings.HasPrefix(line, "现状（"), strings.HasPrefix(line, "常驻规则："):
			continue // 身份、现状与常驻规则都依赖工作流
		}
		line = strings.ReplaceAll(line, "读取工作流 %workflow 里我离线期间的追加指令", "读取我离线期间的追加指令")
		keep = append(keep, line)
	}
	return strings.Join(keep, "\n")
}
