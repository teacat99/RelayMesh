package api

import (
	"context"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/ntfy"
	"github.com/teacat99/RelayMesh/v2/internal/store"
	"github.com/teacat99/RelayMesh/v2/internal/terminal"
)

// ntfy 的「模型运行结束」推送接线。
//
// 终端包只报告事实（terminal.RunEndEvent），ntfy 包按配置决定推不推、怎么写；
// 两个包互不依赖，把事实翻译成推送快照、把机器码解析成主机名的活放在这一层。

// ntfyHostName 返回「机器码 → 主机显示名」的解析函数（管理员写的备注优先，没有备注才用主机名）。
// 查不到、查询出错都返回空串：推送里宁可少一行主机，也不拿机器码凑数。
func ntfyHostName(st *store.Store) func(ctx context.Context, machineCode string) string {
	return func(ctx context.Context, machineCode string) string {
		h, err := st.GetTerminalHost(ctx, machineCode)
		if err != nil || h == nil {
			return ""
		}
		return terminal.HostDisplayName(*h)
	}
}

// runInfoFromEvent 把终端的运行结束事件翻译成 ntfy 的推送快照。
// 这里不带输出（运行记录里本来就没有完整输出），命令是脱敏后的副本。
func runInfoFromEvent(ev terminal.RunEndEvent) ntfy.RunInfo {
	r := ev.Run
	return ntfy.RunInfo{
		RunID:            r.RunID,
		State:            r.State,
		Machine:          r.Target,
		Description:      r.Description,
		Command:          r.Command,
		ExitCode:         r.ExitCode,
		Signal:           r.Signal,
		Why:              ev.Why,
		Duration:         time.Duration(r.DurationMS) * time.Millisecond,
		WorkflowID:       r.WorkflowID,
		ProjectDirectory: r.ProjectDirectory,
		CancelBy:         ev.CancelBy,
	}
}
