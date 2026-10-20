package mcp

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/teacat99/RelayMesh/v2/internal/acl"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 任务级权限的接线（#175 B2c，设计稿 design/shared-task-board-b2.md 第 7 节）。
//
// 规则本身在 store 层（internal/store/work_assign.go：角色 × 操作矩阵、三档策略），这里只做两件事：
//
//  1. 进入 store 之前确定「依据」：这次调用的 Agent 是不是这个项目的管理 Agent（持有 tasks 范围授权，或项目范围的 edit 以上授权），
//     以及策略档位（取任务所在项目的访问控制模式）。查库全在这一层完成——单连接 SQLite 下，store 的事务回调里不能再回头查授权；
//  2. store 返回之后把结果记进审计：audit 档的 res.Violations（照常执行了、但若启用强制会被拒绝）记成 would_deny，
//     enforce 档的真实拒绝记成 would_deny=false。
//
// 与「工作流范围闸」（acl_gate.go）互补：那个闸管「按工作流编号访问别人的数据」，这里管「共享板上谁能改哪条任务」。
// 项目在 off 档、独立板、没有归入项目的工作流：策略为 off，与以前一样什么都不校验。

// workPolicy 是一次 MCP 任务调用里任务级权限的依据。
type workPolicy struct {
	s      *Server
	actor  store.WorkActor
	proj   *model.Project // 非 nil = 任务板属于一个开了访问控制（audit / enforce）的项目
	caller *aclCaller
}

// newWorkPolicy 为一次任务调用确定操作者、是否管理 Agent 与策略档位。
func (s *Server) newWorkPolicy(ctx context.Context, workflowID, sessionID string) *workPolicy {
	wf := strings.TrimSpace(workflowID)
	p := &workPolicy{s: s, actor: store.WorkActor{Kind: model.WorkActorAgent, SessionID: strings.TrimSpace(sessionID), WorkflowID: wf}}
	caller := aclCallerFrom(ctx)
	if wf == "" || caller == nil || caller.Project == nil {
		return p // 没归入项目，或项目在 off 档（闸只在 audit / enforce 档才记下项目）
	}
	// 以任务板为准：工作流的板是粘性的，目录归属后来变了也可能还留在原来的项目上。
	key, err := s.store.BoardKeyOf(ctx, wf)
	if err != nil {
		return p
	}
	pid, _, ok := store.ParseBoardKey(key)
	if !ok || pid == 0 {
		return p // 独立板：没有「项目管理 Agent」的说法
	}
	proj := caller.Project
	if proj.ID != pid {
		if proj, err = s.store.GetProject(ctx, pid); err != nil || proj == nil {
			return p
		}
	}
	switch proj.ACLMode {
	case acl.ModeAudit:
		p.actor.Policy = store.WorkPolicyAudit
	case acl.ModeEnforce:
		p.actor.Policy = store.WorkPolicyEnforce
	default:
		return p
	}
	grants, _ := s.store.ActiveGrantsForCaller(ctx, proj.ID, caller.IdentityID, wf)
	p.actor.Manager = acl.CanManageTasks(caller.IdentityID, proj.ID, grants).Allow
	p.proj, p.caller = proj, caller
	return p
}

// forbiddenText 取任务级权限拒绝里写给智能体看的那句话；不是权限拒绝返回空串。
func forbiddenText(err error) string {
	var ae *store.AppError
	if errors.As(err, &ae) && ae.Kind == store.ErrForbidden {
		return ae.Message
	}
	return ""
}

// note 把一次写入的权限结果记进审计：seq 是被写的任务（新建失败时为 0）。
func (p *workPolicy) note(ctx context.Context, tool, action string, seq int, res *store.WorkItemResult, err error) {
	if p == nil || p.proj == nil {
		return
	}
	if seq == 0 && res != nil && res.Item != nil {
		seq = res.Item.Seq
	}
	target := tool + ":" + action
	if seq > 0 {
		target += "#" + strconv.Itoa(seq)
	}
	record := func(reason string, wouldDeny bool) {
		p.s.aclAudit(ctx, p.proj, p.caller, p.actor.WorkflowID, model.AuditACLDenied, model.AuditDeny, model.ScopeTasks,
			strconv.FormatUint(uint64(p.proj.ID), 10), target, store.AuditCrossReadDedupe,
			map[string]any{"mode": p.proj.ACLMode, "rule": "tasks", "reason": reason, "would_deny": wouldDeny, "manager": p.actor.Manager})
	}
	if res != nil {
		for _, v := range res.Violations {
			record(v, true)
		}
	}
	if msg := forbiddenText(err); msg != "" {
		record(msg, false)
	}
}

// hint 给回执用：普通 Agent 被 audit 档记了违规时，在回执里提醒一句（不阻止），免得它以为这条路一直走得通。
func (p *workPolicy) hint(res *store.WorkItemResult) string {
	if p == nil || p.proj == nil || res == nil || len(res.Violations) == 0 {
		return ""
	}
	return fmt.Sprintf("提示：项目「%s」的访问控制是 %s 档，这次写入照常执行了，但按规则它本该被拒绝（%s）；启用强制后会直接失败，请只改自己的任务，或请用户把你指定为项目管理 Agent。", p.proj.Name, p.proj.ACLMode, strings.Join(res.Violations, "；"))
}
