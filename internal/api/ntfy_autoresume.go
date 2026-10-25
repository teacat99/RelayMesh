package api

import (
	"fmt"
	"strings"

	"github.com/teacat99/RelayMesh/v2/internal/ntfy"
	"github.com/teacat99/RelayMesh/v2/internal/terminal"
)

// ntfy 的「不再自动续跑」推送接线（#151；这条通道原先与「终端会话守护」告警共用，守护在 #245 下线后只剩这一种）。
//
// 终端包只报告事实（terminal.AutoResumeEvent：哪个预设、为什么不再续跑），ntfy 包决定怎么写、推不推；
// 两个包互不依赖，把事实翻译成推送快照的活放在这一层，与「模型运行结束」的接线（ntfy_run.go）对称。

// autoResumeInfoFromEvent 把「不再自动续跑」事件翻译成 ntfy 的推送快照。
// Key 由预设与最后出事的运行组成：同一次放弃只推一次。原因里把「为什么不再续跑」和「这次为什么失败」写成一句。
func autoResumeInfoFromEvent(ev terminal.AutoResumeEvent) ntfy.AutoResumeInfo {
	reason := strings.TrimSpace(ev.Why)
	if r := strings.TrimSpace(ev.Reason); r != "" {
		reason += "；这次的失败原因：" + r
	}
	return ntfy.AutoResumeInfo{
		Key:        fmt.Sprintf("arz:%s:%s", ev.PresetID, ev.FromRun),
		Name:       ev.Name,
		Machine:    ev.Machine,
		Reason:     reason,
		WorkflowID: ev.WorkflowID,
	}
}
