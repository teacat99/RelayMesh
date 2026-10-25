package terminal

import (
	"context"
	"strings"

	"github.com/teacat99/RelayMesh/v2/internal/sessionprompt"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 唤醒词渲染的终端侧封装（#232 D3，D-376）。
//
// Hub 发给智能体的「唤醒词」有三个出口：预设运行（续聊 / 新开对话）、会话页运行、网页新建会话的启动提示词。
// 模板的分层选用、变量的现算与清洗都在 store.RenderWake（渲染器在 internal/sessionprompt）；这里只决定每个出口怎么调它、
// 以及各出口升级前就有的特殊行为（时限提醒、非交互后缀、不安全编号的兜底）怎么保留。

// launcherWake 是渲染一次预设运行 / 会话页运行的提示词要的输入。
type launcherWake struct {
	// Kind 只能是 KindResume（续聊）或 KindNew（新开对话）。
	Kind sessionprompt.Kind
	// Custom 是预设（或会话页运行配置）里自己写的正文；非空时排在所有分层的最前面。
	Custom     string
	WorkflowID string
	// Project 是这次运行的工作目录（预设里配的），%project 取它；空则取工作流最近一条会话的目录。
	Project string
	// MaxRunSec > 0 时：内置默认后面补一句「本次运行最长 N」（升级前就是这样）；用户自己写的模板里可以用 %limit 放在想放的位置。
	// NoLimitNote 为真时不补（会话页运行升级前就没有时限提醒）。
	MaxRunSec   int
	NoLimitNote bool
}

// renderLauncherWake 渲染一次运行的提示词。
//
// 升级前的行为保持：预设自己写了正文就按它渲染；没写正文才用默认，并且只有内置默认才补时限提醒（用户自己写的话一个字不加）。
// 新增的是：没写正文时，先看会话模板、再看全局覆盖，最后才是内置默认。
func renderLauncherWake(ctx context.Context, st *store.Store, w launcherWake) store.WakeResult {
	note := ""
	if w.MaxRunSec > 0 && !w.NoLimitNote {
		note = launcherRunLimitNote(w.MaxRunSec)
	}
	res := st.RenderWake(ctx, store.WakeInput{
		Kind: w.Kind, WorkflowID: strings.TrimSpace(w.WorkflowID), PresetBody: w.Custom,
		Base: sessionprompt.Vars{Project: strings.TrimSpace(w.Project), Limit: note},
	})
	if res.Layer == store.WakeLayerBuiltin && note != "" {
		res.Text += " " + note
	}
	return res
}
