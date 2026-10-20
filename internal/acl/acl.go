// Package acl 是项目知识库的授权判定（#114，设计稿 06 §7）：一个不碰数据库的纯函数。
//
// 调用方（MCP 分发层、REST）负责把「谁在调用、碰的是什么、库里有哪些有效授权」整理成 Request，
// 这里只回答「够不够」以及「依据哪条规则」。这样规则表可以用表驱动测试穷举，不依赖任何存储。
//
// 明确不是安全边界：身份是调用方自报的，这套规则防的是误操作、越权写和「拿着别人的 ID 就改」；
// 真正的边界仍然是 Token。
package acl

import (
	"strconv"
	"strings"
)

// Role 是权限等级，逐级包含：read ⊂ comment ⊂ edit ⊂ manage。
type Role int

const (
	RoleNone Role = iota
	RoleRead
	RoleComment
	RoleEdit
	RoleManage
)

// ParseRole 把字符串角色转成 Role；不认识的返回 RoleNone。
func ParseRole(s string) Role {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "read":
		return RoleRead
	case "comment":
		return RoleComment
	case "edit":
		return RoleEdit
	case "manage":
		return RoleManage
	}
	return RoleNone
}

func (r Role) String() string {
	switch r {
	case RoleRead:
		return "read"
	case RoleComment:
		return "comment"
	case RoleEdit:
		return "edit"
	case RoleManage:
		return "manage"
	}
	return "none"
}

// 档位：off 不判定；audit 只记录「若启用会被拒绝」的调用；enforce 真正拦截。
const (
	ModeOff     = "off"
	ModeAudit   = "audit"
	ModeEnforce = "enforce"
)

// 资源种类。
const (
	// KindDoc 是项目知识库里的文档（含 sessions/<工作流>/ 下的）。
	KindDoc = "doc"
	// KindModule 是模块本身（元数据、负责人、授权）。
	KindModule = "module"
	// KindProject 是项目级（目录、模块树）。
	KindProject = "project"
	// KindSession 是某个工作流的会话数据：任务、历史、检查点等（不含收件箱）。
	KindSession = "session"
	// KindInbox 是某个工作流的收件箱：读取会把消息标成已送达，等于写，所以只有它的所有者能碰。
	KindInbox = "inbox"
	// KindTasks 是项目任务板的管理权（#175 B2c）：派任务、改所有人的任务。它只认显式授权——
	// 没有「同项目只读」「负责人」「知道编号」这些隐含规则，没被授权的 Agent 一律不是管理 Agent。
	KindTasks = "tasks"
)

// 授权范围种类（与 model.Scope* 同值）。
const (
	ScopeProject  = "project"
	ScopeModule   = "module"
	ScopeDoc      = "doc"
	ScopeWorkflow = "workflow"
	// ScopeTasks 只覆盖 KindTasks：给了任务管理权，不等于能编辑文档。
	ScopeTasks = "tasks"
)

// 规则编号，对应设计稿 06 §7.3。
const (
	RuleOff      = "off"
	RuleHuman    = "R1"
	RuleProject  = "R2"
	RuleOwner    = "R3"
	RuleKnownID  = "R4"
	RuleGrant    = "grant"
	RuleInbox    = "inbox-owner"
	RuleNoAccess = "R7"
)

// Caller 是调用方。
type Caller struct {
	Human bool
	// IdentityID 是 Agent 身份编号；0 表示认不出。
	IdentityID uint
	// ProjectID 是调用方身份所在目录归属的项目；0 表示不属于任何项目。
	ProjectID uint
}

// Resource 是被访问的东西，字段由调用方从库里查出来。
type Resource struct {
	Kind       string
	ProjectID  uint // 资源所在项目；0 = 不属于任何项目
	ModulePath string
	DocPath    string
	DocID      uint
	WorkflowID string
	// OwnerIdentityID 是文档自己的负责人。
	OwnerIdentityID uint
	// ModuleOwnerID 是文档所在模块（或模块本身）的负责人。
	ModuleOwnerID uint
	// WorkflowIdentityID 是资源所属工作流绑定的身份（自己的 sessions/<工作流>/、任务、收件箱都算它的）。
	WorkflowIdentityID uint
}

// Grant 是一条已经过滤过的有效授权（没撤销、没过期、主体就是调用方或调用方所在的群）。
type Grant struct {
	ProjectID uint
	ScopeKind string
	ScopeRef  string
	Role      Role
}

// Request 是一次判定的全部输入。
type Request struct {
	Mode   string
	Caller Caller
	Res    Resource
	// Need 是这次操作需要的最低角色：读 = read，写 = edit，授权 / 改负责人 = manage。
	Need   Role
	Grants []Grant
}

// Decision 是判定结果。
type Decision struct {
	Allow bool
	// Rule 是起决定作用的规则：放行时是给出最高权限的那条，拒绝时是 R7。
	Rule string
	// Have 是调用方在这个资源上得到的最高角色。
	Have Role
	// Reason 是给人和 Agent 看的一句话（拒绝时说明缺什么、怎么补）。
	Reason string
}

// MaxGrantRole 是授权能给出的最高角色：manage 不能转授。
const MaxGrantRole = RoleEdit

// Authorize 按设计稿 R1–R7 判定一次访问。
func Authorize(r Request) Decision {
	mode := r.Mode
	if mode != ModeAudit && mode != ModeEnforce {
		return Decision{Allow: true, Rule: RuleOff, Have: RoleManage}
	}
	if r.Need == RoleNone {
		r.Need = RoleRead
	}
	if r.Caller.Human {
		return Decision{Allow: true, Rule: RuleHuman, Have: RoleManage}
	}
	id := r.Caller.IdentityID

	// 收件箱：读也会改变状态，所以不走授权、不走 R2 / R4，只有所有者本人。
	if r.Res.Kind == KindInbox {
		if id != 0 && id == r.Res.WorkflowIdentityID {
			return Decision{Allow: true, Rule: RuleInbox, Have: RoleManage}
		}
		return deny(r, RoleNone, "收件箱只有它的所有者能读写（读取会把消息标成已送达）")
	}

	best, rule := RoleNone, RuleNoAccess
	raise := func(role Role, by string) {
		if role > best {
			best, rule = role, by
		}
	}

	// R3：自己的负责范围。任务管理权不适用——它只认显式授权，不能靠「负责人」之类的隐含规则得到。
	if r.Res.Kind != KindTasks && id != 0 && (id == r.Res.OwnerIdentityID || id == r.Res.ModuleOwnerID || id == r.Res.WorkflowIdentityID) {
		raise(RoleManage, RuleOwner)
	}
	// 显式授权。
	for _, g := range r.Grants {
		if g.Role == RoleNone || !grantCovers(g, r.Res) {
			continue
		}
		role := g.Role
		if role > MaxGrantRole {
			role = MaxGrantRole
		}
		raise(role, RuleGrant)
	}
	// R2：同一项目的身份，对该项目知识库只读。
	if r.Res.ProjectID != 0 && r.Caller.ProjectID == r.Res.ProjectID && id != 0 {
		switch r.Res.Kind {
		case KindDoc, KindModule, KindProject:
			raise(RoleRead, RuleProject)
		}
	}
	// R4：知道某个工作流的编号，就能只读它的会话数据（任务、历史、会话文档）；不含收件箱，也不含项目知识库其余部分。
	if r.Res.Kind == KindSession && r.Res.WorkflowID != "" {
		raise(RoleRead, RuleKnownID)
	}

	if best >= r.Need {
		return Decision{Allow: true, Rule: rule, Have: best}
	}
	return deny(r, best, "")
}

func deny(r Request, have Role, reason string) Decision {
	if reason == "" {
		reason = "需要 " + r.Need.String() + " 权限，你在这里只有 " + have.String()
		switch {
		case r.Caller.IdentityID == 0:
			reason += "（没有可识别的身份：请在调用里带上 project_directory 与 host_name）"
		case r.Need > RoleRead:
			reason += "；写入只来自自己的负责范围、你（用户）的授权或负责人 Agent 的授权"
		default:
			reason += "；跨项目只能经「知道工作流编号」读取会话数据，或被显式授权"
		}
	}
	return Decision{Allow: false, Rule: RuleNoAccess, Have: have, Reason: reason}
}

func grantCovers(g Grant, res Resource) bool {
	if g.ProjectID != 0 && res.ProjectID != 0 && g.ProjectID != res.ProjectID {
		return false
	}
	ref := strings.TrimSpace(g.ScopeRef)
	switch g.ScopeKind {
	case ScopeProject:
		return res.ProjectID != 0 && ref == strconv.FormatUint(uint64(res.ProjectID), 10)
	case ScopeModule:
		if ref == "" || res.ModulePath == "" {
			return false
		}
		return res.ModulePath == ref || strings.HasPrefix(res.ModulePath, ref+"/")
	case ScopeDoc:
		if ref == "" {
			return false
		}
		if res.DocPath != "" && res.DocPath == ref {
			return true
		}
		return res.DocID != 0 && (ref == strconv.FormatUint(uint64(res.DocID), 10) || ref == "d"+strconv.FormatUint(uint64(res.DocID), 10))
	case ScopeWorkflow:
		return ref != "" && ref == res.WorkflowID
	case ScopeTasks:
		return res.Kind == KindTasks && res.ProjectID != 0 && ref == strconv.FormatUint(uint64(res.ProjectID), 10)
	}
	return false
}

// CanManageTasks 回答「这个身份是不是这个项目的任务管理 Agent」（#175 B2c）：
// 持有这个项目的 tasks 范围授权，或项目范围、角色在 edit 及以上的授权（项目级编辑本来就包含改所有任务）。
// 一律按 enforce 判定（项目在 off / audit 档时它只用来决定「若启用强制会怎样」，不能因为档位放宽管理权）；
// 没有可识别的身份、没有授权都返回不允许。grants 是调用方已经过滤好的有效授权。
func CanManageTasks(identityID, projectID uint, grants []Grant) Decision {
	if projectID == 0 {
		return Decision{Allow: false, Rule: RuleNoAccess, Reason: "任务不在任何项目的板上，没有管理 Agent 的说法"}
	}
	return Authorize(Request{
		Mode:   ModeEnforce,
		Caller: Caller{IdentityID: identityID, ProjectID: projectID},
		Res:    Resource{Kind: KindTasks, ProjectID: projectID},
		Need:   RoleEdit,
		Grants: grants,
	})
}
