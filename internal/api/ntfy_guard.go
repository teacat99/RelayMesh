package api

import (
	"fmt"
	"strings"

	"github.com/teacat99/RelayMesh/v2/internal/ntfy"
	"github.com/teacat99/RelayMesh/v2/internal/terminal"
)

// ntfy 的「终端会话守护」推送接线（D-214）。
//
// 终端包只报告事实（terminal.GuardAlert：哪个预设、意外结束还是停摆、为什么），ntfy 包决定怎么写、推不推；
// 两个包互不依赖，把事实翻译成推送快照的活放在这一层，与「模型运行结束」的接线（ntfy_run.go）对称。

// guardInfoFromAlert 把守护事件翻译成 ntfy 的推送快照。Key 由预设、事件标识与提醒序号组成：
// 同一次事件的首次通知与各次提醒是不同的推送，而重复提交同一条会被 ntfy 去重。
func guardInfoFromAlert(a terminal.GuardAlert) ntfy.GuardInfo {
	return ntfy.GuardInfo{
		Key:        fmt.Sprintf("%s:%s:%d", a.PresetID, a.Episode, a.Reminder),
		Name:       a.Name,
		Machine:    a.Machine,
		Crashed:    a.State == terminal.GuardCrashed,
		Reason:     a.Reason,
		Since:      a.Since,
		Reminder:   a.Reminder,
		WorkflowID: a.WorkflowID,
	}
}

// guardInfoFromAutoResume 把「不再自动续跑」事件（#151）翻译成 ntfy 的推送快照：复用守护的推送通道，Kind 标明是哪种。
// Key 由预设与最后出事的运行组成：同一次放弃只推一次。原因里把「为什么不再续跑」和「这次为什么失败」写成一句。
func guardInfoFromAutoResume(ev terminal.AutoResumeEvent) ntfy.GuardInfo {
	reason := strings.TrimSpace(ev.Why)
	if r := strings.TrimSpace(ev.Reason); r != "" {
		reason += "；这次的失败原因：" + r
	}
	return ntfy.GuardInfo{
		Key:        fmt.Sprintf("arz:%s:%s", ev.PresetID, ev.FromRun),
		Kind:       ntfy.GuardKindAutoResumeGaveUp,
		Name:       ev.Name,
		Machine:    ev.Machine,
		Reason:     reason,
		WorkflowID: ev.WorkflowID,
	}
}
