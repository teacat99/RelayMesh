package store

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"gorm.io/gorm"
)

// 工作流改绑（项目迁移）。
//
// 背景：会话按「项目目录 + 主机名」隔离，并且 D-161 禁止同一个 workflow_id 被另一个项目目录写入。
// 项目换了目录、或搬到另一台机器之后，旧工作流会因为这两个维度对不上而「查不到 / 写不进」。
// 这里提供受控的改绑：只改某一个工作流名下 feedback_sessions 与 queued_feedbacks 的
// project_directory / host_name，不动正文、不动凭据绑定、不动更新时间，更不碰其他任何工作流。
//
// 刻意不暴露成 MCP 工具：改绑会绕开 D-161 的跨项目隔离，只允许持有数据库的运维人员
// 通过 relaymesh rebind-workflow 命令（默认只预演）执行。

var windowsDrivePath = regexp.MustCompile(`^[A-Za-z]:[\\/]`)

const (
	rebindMaxDirLen  = 512 // 与 project_directory 列宽一致
	rebindMaxHostLen = 128 // 与 host_name 列宽一致
)

// WorkflowBinding 是一个工作流当前绑定的「项目目录 + 主机」画像。
type WorkflowBinding struct {
	WorkflowID string   `json:"workflow_id"`
	Dirs       []string `json:"dirs"`     // 名下出现过的不同 project_directory，正常只有一个
	Hosts      []string `json:"hosts"`    // 名下出现过的不同 host_name（空值不计）
	Sessions   int64    `json:"sessions"` // feedback_sessions 行数
	Pending    int64    `json:"pending"`  // 其中仍在等待用户回复的
	Queued     int64    `json:"queued"`   // queued_feedbacks 行数
}

// RebindRequest 描述一次改绑：目标目录必填，目标主机为空表示不改主机。
type RebindRequest struct {
	WorkflowID string
	ToDir      string
	ToHost     string
}

// RebindResult 是一次改绑（或预演）的结果。
type RebindResult struct {
	Before         WorkflowBinding `json:"before"`
	After          WorkflowBinding `json:"after"`            // 预演时等于 Before
	TargetDir      string          `json:"target_dir"`       // 规整后的目标目录
	TargetHost     string          `json:"target_host"`      // 规整后的目标主机（空 = 不改）
	SessionsToMove int64           `json:"sessions_to_move"` // 需要改写（预演）或已改写（执行）的会话行数
	QueuedToMove   int64           `json:"queued_to_move"`
	Applied        bool            `json:"applied"`
}

// Noop 表示该工作流已经绑定在目标上，无需任何改写。
func (r *RebindResult) Noop() bool { return r.SessionsToMove == 0 && r.QueuedToMove == 0 }

// CredentialHost 是「凭据名 → 主机名」的只读视图，供运维挑选改绑目标主机；不含令牌。
type CredentialHost struct {
	Name     string `json:"name"`
	HostName string `json:"host_name"`
	Active   bool   `json:"active"`
}

// WorkflowBindingOf 返回一个工作流当前的绑定画像；工作流不存在时返回 NotFound。
func (s *Store) WorkflowBindingOf(ctx context.Context, workflowID string) (*WorkflowBinding, error) {
	return workflowBindingOf(s.db.WithContext(ctx), workflowID)
}

// ListWorkflowBindings 按工作流汇总当前绑定，最近活跃的在前；供运维先看清再改绑。
func (s *Store) ListWorkflowBindings(ctx context.Context) ([]WorkflowBinding, error) {
	db := s.db.WithContext(ctx)
	var ids []string
	if err := db.Model(&model.FeedbackSession{}).
		Where("workflow_id != ''").
		Group("workflow_id").
		Order("MAX(updated_at) DESC").
		Pluck("workflow_id", &ids).Error; err != nil {
		return nil, err
	}
	out := make([]WorkflowBinding, 0, len(ids))
	for _, id := range ids {
		b, err := workflowBindingOf(db, id)
		if err != nil {
			return nil, err
		}
		out = append(out, *b)
	}
	return out, nil
}

// ListCredentialHosts 列出各凭据登记的主机名（只含名称与主机，不含令牌）。
func (s *Store) ListCredentialHosts(ctx context.Context) ([]CredentialHost, error) {
	var rows []model.MCPCredential
	if err := s.db.WithContext(ctx).
		Select("name", "host_name", "is_active").
		Order("name asc").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]CredentialHost, 0, len(rows))
	for _, r := range rows {
		out = append(out, CredentialHost{Name: r.Name, HostName: r.HostName, Active: r.IsActive})
	}
	return out, nil
}

// SnapshotTo 把整个数据库另存为一份一致性快照（VACUUM INTO），不阻塞读写；目标文件必须事先不存在。
func (s *Store) SnapshotTo(ctx context.Context, dest string) error {
	dest = strings.TrimSpace(dest)
	if dest == "" || dest == ":memory:" {
		return NewInvalidInputError("快照目标路径不能为空")
	}
	return s.db.WithContext(ctx).Exec("VACUUM INTO ?", dest).Error
}

// RebindWorkflow 把一个工作流改绑到新的项目目录（可同时改主机）。
//
// apply=false 只做预演：返回当前画像与将要改写的行数，不写入任何数据。
// apply=true 在单个事务里改写，并在提交前校验结果（目录唯一、主机符合预期），任何一项不符就整体回滚。
// 改写使用 UpdateColumns：保持 updated_at 不变，避免这个工作流「看起来刚刚活跃过」而打乱最近活跃排序。
func (s *Store) RebindWorkflow(ctx context.Context, req RebindRequest, apply bool) (*RebindResult, error) {
	dir, host, err := normalizeRebindTarget(req.ToDir, req.ToHost)
	if err != nil {
		return nil, err
	}

	res := &RebindResult{TargetDir: dir, TargetHost: host}
	run := func(db *gorm.DB) error {
		before, err := workflowBindingOf(db, req.WorkflowID)
		if err != nil {
			return err
		}
		res.Before = *before
		res.After = *before

		sessWhere, sessArgs := rebindMismatch(before.WorkflowID, dir, host)
		if err := db.Model(&model.FeedbackSession{}).Where(sessWhere, sessArgs...).Count(&res.SessionsToMove).Error; err != nil {
			return err
		}
		if err := db.Model(&model.QueuedFeedback{}).Where(sessWhere, sessArgs...).Count(&res.QueuedToMove).Error; err != nil {
			return err
		}
		if !apply || res.Noop() {
			return nil
		}

		set := map[string]any{"project_directory": dir}
		if host != "" {
			set["host_name"] = host
		}
		if err := db.Model(&model.FeedbackSession{}).Where("workflow_id = ?", before.WorkflowID).UpdateColumns(set).Error; err != nil {
			return err
		}
		if err := db.Model(&model.QueuedFeedback{}).Where("workflow_id = ?", before.WorkflowID).UpdateColumns(set).Error; err != nil {
			return err
		}

		after, err := workflowBindingOf(db, before.WorkflowID)
		if err != nil {
			return err
		}
		if len(after.Dirs) != 1 || after.Dirs[0] != dir {
			return fmt.Errorf("改绑后校验失败：目录应唯一为 %q，实际为 %q", dir, after.Dirs)
		}
		if host != "" && (len(after.Hosts) != 1 || after.Hosts[0] != host) {
			return fmt.Errorf("改绑后校验失败：主机应唯一为 %q，实际为 %q", host, after.Hosts)
		}
		if after.Sessions != before.Sessions || after.Queued != before.Queued || after.Pending != before.Pending {
			return fmt.Errorf("改绑后校验失败：行数发生变化（会话 %d→%d，排队 %d→%d，等待 %d→%d）",
				before.Sessions, after.Sessions, before.Queued, after.Queued, before.Pending, after.Pending)
		}
		res.After = *after
		res.Applied = true
		return nil
	}

	if apply {
		err = s.WithTx(ctx, run)
	} else {
		err = run(s.db.WithContext(ctx))
	}
	if err != nil {
		return nil, err
	}
	return res, nil
}

// rebindMismatch 生成「属于该工作流、但目录或主机与目标不一致」的行条件。
func rebindMismatch(workflowID, dir, host string) (string, []any) {
	if host == "" {
		return "workflow_id = ? AND project_directory != ?", []any{workflowID, dir}
	}
	return "workflow_id = ? AND (project_directory != ? OR host_name != ?)", []any{workflowID, dir, host}
}

func workflowBindingOf(db *gorm.DB, workflowID string) (*WorkflowBinding, error) {
	id := strings.TrimSpace(workflowID)
	if id == "" {
		return nil, NewInvalidInputError("workflow_id 不能为空")
	}
	b := &WorkflowBinding{WorkflowID: id}
	if err := db.Model(&model.FeedbackSession{}).Where("workflow_id = ?", id).Count(&b.Sessions).Error; err != nil {
		return nil, err
	}
	if err := db.Model(&model.QueuedFeedback{}).Where("workflow_id = ?", id).Count(&b.Queued).Error; err != nil {
		return nil, err
	}
	if b.Sessions == 0 && b.Queued == 0 {
		return nil, NewNotFoundError(fmt.Sprintf("工作流 %q 不存在", id))
	}
	if err := db.Model(&model.FeedbackSession{}).Where("workflow_id = ? AND status = ?", id, "pending").Count(&b.Pending).Error; err != nil {
		return nil, err
	}

	dirs := map[string]struct{}{}
	hosts := map[string]struct{}{}
	for _, table := range []any{&model.FeedbackSession{}, &model.QueuedFeedback{}} {
		var ds, hs []string
		if err := db.Model(table).Where("workflow_id = ?", id).Distinct().Pluck("project_directory", &ds).Error; err != nil {
			return nil, err
		}
		if err := db.Model(table).Where("workflow_id = ?", id).Distinct().Pluck("host_name", &hs).Error; err != nil {
			return nil, err
		}
		for _, d := range ds {
			dirs[d] = struct{}{}
		}
		for _, h := range hs {
			if h != "" {
				hosts[h] = struct{}{}
			}
		}
	}
	b.Dirs = sortedKeys(dirs)
	b.Hosts = sortedKeys(hosts)
	return b, nil
}

func sortedKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// normalizeRebindTarget 校验并规整目标目录 / 主机：只接受绝对路径（POSIX 或 Windows 盘符），
// 拒绝根目录、控制字符与超长值。目录末尾的分隔符会被去掉，POSIX 路径同时做 path.Clean。
func normalizeRebindTarget(dir, host string) (string, string, error) {
	dir = strings.TrimSpace(dir)
	host = strings.TrimSpace(host)
	if dir == "" {
		return "", "", NewInvalidInputError("目标项目目录不能为空")
	}
	switch {
	case strings.HasPrefix(dir, "/"):
		dir = path.Clean(dir)
		if dir == "/" {
			return "", "", NewInvalidInputError("根目录不能作为项目目录")
		}
	case windowsDrivePath.MatchString(dir):
		// Hub 不一定跑在 Windows 上，无法按 Windows 语义规整，只去掉末尾分隔符。
		if trimmed := strings.TrimRight(dir, `\/`); len(trimmed) > 2 {
			dir = trimmed
		} else {
			return "", "", NewInvalidInputError("盘符根目录不能作为项目目录")
		}
	default:
		return "", "", NewInvalidInputError(fmt.Sprintf("目标项目目录必须是绝对路径，收到 %q", dir))
	}
	if len(dir) > rebindMaxDirLen {
		return "", "", NewInvalidInputError(fmt.Sprintf("目标项目目录过长（最多 %d 字节）", rebindMaxDirLen))
	}
	if strings.IndexFunc(dir, unicode.IsControl) >= 0 {
		return "", "", NewInvalidInputError("目标项目目录含控制字符")
	}
	if len(host) > rebindMaxHostLen {
		return "", "", NewInvalidInputError(fmt.Sprintf("目标主机名过长（最多 %d 字节）", rebindMaxHostLen))
	}
	if strings.IndexFunc(host, unicode.IsControl) >= 0 {
		return "", "", NewInvalidInputError("目标主机名含控制字符")
	}
	return dir, host, nil
}
