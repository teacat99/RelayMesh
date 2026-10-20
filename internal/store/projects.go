package store

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 项目知识库的存储（#114，设计稿 06 §3 / §8）。
//
// 规则：
//   - 项目只由用户创建与确认；这里的写方法不判断调用者身份，由 REST 层保证只有「人」能调到（Agent 没有入口）；
//   - 一个（主机，目录）只属于一个项目；工作流属于哪个项目，取决于它最近一次汇报会话所在的（主机，目录）；
//   - 迁移先 dry-run 出对拍报告（含哈希），确认时带上哈希，哈希对不上就拒绝：确认的就是看过的那一份。

const (
	projectNameMax    = 128
	projectDescMax    = 1000
	moduleDepthMax    = 8
	moduleSegmentMax  = 64
	moduleTitleMax    = 128
	projectsMax       = 200
	projectDirsMax    = 50
	projectModulesMax = 500
)

var projectSlugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// SlugifyProjectName 把项目名转成建议的 slug：小写字母数字与连字符；转不出来（比如全是汉字）就返回空串。
func SlugifyProjectName(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		default:
			if b.Len() > 0 && !dash {
				b.WriteByte('-')
				dash = true
			}
		}
		if b.Len() >= 63 {
			break
		}
	}
	return strings.Trim(b.String(), "-")
}

// NormalizeModulePath 规整模块路径：用 / 分隔、最多 8 层、每层 ≤ 64 个字符，不允许 . / .. 段与路径里的危险字符。
func NormalizeModulePath(p string) (string, error) {
	p = strings.Trim(strings.TrimSpace(strings.ReplaceAll(p, `\`, "/")), "/")
	if p == "" {
		return "", errors.New("模块路径不能为空")
	}
	segs := strings.Split(p, "/")
	if len(segs) > moduleDepthMax {
		return "", fmt.Errorf("模块路径最多 %d 层", moduleDepthMax)
	}
	for i, s := range segs {
		s = strings.TrimSpace(s)
		if s == "" || s == "." || s == ".." {
			return "", errors.New("模块路径里不能有空段或 . / ..")
		}
		if len([]rune(s)) > moduleSegmentMax {
			return "", fmt.Errorf("模块路径每层最多 %d 个字符", moduleSegmentMax)
		}
		if strings.ContainsAny(s, "\x00:*?\"<>|") {
			return "", errors.New("模块路径里有不允许的字符")
		}
		segs[i] = s
	}
	return strings.Join(segs, "/"), nil
}

// ProjectInput 新建项目。
type ProjectInput struct {
	Slug        string
	Name        string
	Description string
	ACLMode     string // 空 = audit
}

// ProjectPatch 修改项目。
type ProjectPatch struct {
	Name        *string
	Description *string
	ACLMode     *string
}

func (s *Store) CreateProject(ctx context.Context, in ProjectInput) (*model.Project, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" || len([]rune(name)) > projectNameMax {
		return nil, NewInvalidInputError(fmt.Sprintf("项目名称不能为空，且最多 %d 个字符", projectNameMax))
	}
	desc := strings.TrimSpace(in.Description)
	if len([]rune(desc)) > projectDescMax {
		return nil, NewInvalidInputError(fmt.Sprintf("项目说明最多 %d 个字符", projectDescMax))
	}
	slug := strings.ToLower(strings.TrimSpace(in.Slug))
	if slug == "" {
		slug = SlugifyProjectName(name)
	}
	if !projectSlugRe.MatchString(slug) {
		return nil, NewInvalidInputError("项目标识只能用小写字母、数字和连字符，以字母或数字开头，最多 63 个字符")
	}
	mode := model.ACLAudit
	if strings.TrimSpace(in.ACLMode) != "" {
		if mode = model.NormalizeACLMode(in.ACLMode); mode == "" {
			return nil, NewInvalidInputError("ACL 档位只能是 off、audit 或 enforce")
		}
	}
	var out model.Project
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		var n int64
		if err := tx.Model(&model.Project{}).Where("archived_at IS NULL").Count(&n).Error; err != nil {
			return err
		}
		if n >= projectsMax {
			return NewInvalidInputError(fmt.Sprintf("最多 %d 个项目", projectsMax))
		}
		var dup int64
		if err := tx.Model(&model.Project{}).Where("slug = ?", slug).Count(&dup).Error; err != nil {
			return err
		}
		if dup > 0 {
			return NewConflictError("项目标识已被占用："+slug, 0)
		}
		now := time.Now()
		out = model.Project{Slug: slug, Name: name, Description: desc, ACLMode: mode, CreatedBy: "human", CreatedAt: now, UpdatedAt: now}
		return tx.Create(&out).Error
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *Store) GetProject(ctx context.Context, id uint) (*model.Project, error) {
	var p model.Project
	if err := s.db.WithContext(ctx).First(&p, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, NewNotFoundError("项目不存在")
		}
		return nil, err
	}
	return &p, nil
}

// ListProjects 按名称排序；默认不含已归档的。
func (s *Store) ListProjects(ctx context.Context, includeArchived bool) ([]model.Project, error) {
	q := s.db.WithContext(ctx).Order("name ASC, id ASC")
	if !includeArchived {
		q = q.Where("archived_at IS NULL")
	}
	var out []model.Project
	return out, q.Find(&out).Error
}

func (s *Store) UpdateProject(ctx context.Context, id uint, patch ProjectPatch) (*model.Project, error) {
	p, err := s.GetProject(ctx, id)
	if err != nil {
		return nil, err
	}
	updates := map[string]any{}
	if patch.Name != nil {
		n := strings.TrimSpace(*patch.Name)
		if n == "" || len([]rune(n)) > projectNameMax {
			return nil, NewInvalidInputError(fmt.Sprintf("项目名称不能为空，且最多 %d 个字符", projectNameMax))
		}
		updates["name"], p.Name = n, n
	}
	if patch.Description != nil {
		d := strings.TrimSpace(*patch.Description)
		if len([]rune(d)) > projectDescMax {
			return nil, NewInvalidInputError(fmt.Sprintf("项目说明最多 %d 个字符", projectDescMax))
		}
		updates["description"], p.Description = d, d
	}
	if patch.ACLMode != nil {
		m := model.NormalizeACLMode(*patch.ACLMode)
		if m == "" {
			return nil, NewInvalidInputError("ACL 档位只能是 off、audit 或 enforce")
		}
		updates["acl_mode"], p.ACLMode = m, m
	}
	if len(updates) == 0 {
		return p, nil
	}
	updates["updated_at"] = time.Now()
	if err := s.db.WithContext(ctx).Model(&model.Project{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		return nil, err
	}
	return s.GetProject(ctx, id)
}

// ArchiveProject 归档项目：目录归属保留，文档照常可读；只是从默认列表里消失。
func (s *Store) ArchiveProject(ctx context.Context, id uint) error {
	now := time.Now()
	res := s.db.WithContext(ctx).Model(&model.Project{}).Where("id = ? AND archived_at IS NULL", id).Updates(map[string]any{"archived_at": now, "updated_at": now})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return NewNotFoundError("项目不存在或已归档")
	}
	return nil
}

// ───────────── 目录归属 ─────────────

func (s *Store) ListProjectDirs(ctx context.Context, projectID uint) ([]model.ProjectDir, error) {
	var out []model.ProjectDir
	return out, s.db.WithContext(ctx).Where("project_id = ?", projectID).Order("host_name ASC, project_directory ASC").Find(&out).Error
}

// AddProjectDir 把（主机，目录）归入项目。同一个目录已经在别的项目里 → 冲突并说明在哪个项目。
func (s *Store) AddProjectDir(ctx context.Context, projectID uint, host, dir, branchHint string) (*model.ProjectDir, error) {
	host = strings.TrimSpace(host)
	dir = normalizeProjectDir(dir)
	if host == "" || dir == "" {
		return nil, NewInvalidInputError("主机名与项目目录都不能为空")
	}
	if len(host) > 128 {
		return nil, NewInvalidInputError("主机名太长")
	}
	hint := strings.TrimSpace(branchHint)
	if len([]rune(hint)) > 128 {
		return nil, NewInvalidInputError("分支提示最多 128 个字符")
	}
	if _, err := s.GetProject(ctx, projectID); err != nil {
		return nil, err
	}
	var out model.ProjectDir
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		var n int64
		if err := tx.Model(&model.ProjectDir{}).Where("project_id = ?", projectID).Count(&n).Error; err != nil {
			return err
		}
		if n >= projectDirsMax {
			return NewInvalidInputError(fmt.Sprintf("每个项目最多 %d 个目录", projectDirsMax))
		}
		var exist model.ProjectDir
		err := tx.Where("LOWER(host_name) = LOWER(?) AND project_directory = ?", host, dir).Take(&exist).Error
		if err == nil {
			if exist.ProjectID == projectID {
				out = exist
				return nil
			}
			var other model.Project
			name := fmt.Sprintf("#%d", exist.ProjectID)
			if tx.First(&other, exist.ProjectID).Error == nil {
				name = "「" + other.Name + "」"
			}
			return NewConflictError("这个目录已经属于项目"+name, 0)
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		out = model.ProjectDir{ProjectID: projectID, HostName: host, ProjectDirectory: dir, BranchHint: hint, AddedBy: "human", AddedAt: time.Now()}
		return tx.Create(&out).Error
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *Store) RemoveProjectDir(ctx context.Context, projectID, dirID uint) error {
	res := s.db.WithContext(ctx).Where("id = ? AND project_id = ?", dirID, projectID).Delete(&model.ProjectDir{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return NewNotFoundError("目录归属不存在")
	}
	return nil
}

// ProjectOfDir 返回（主机，目录）所属的项目；没有归属返回 nil, nil。host 为空时只按目录匹配（取最早登记的一条）。
func (s *Store) ProjectOfDir(ctx context.Context, host, dir string) (*model.Project, error) {
	return projectOfDirOn(ctx, s.db, host, dir)
}

// projectOfDirOn 是 ProjectOfDir 的核心，可以在事务句柄上调用（单连接下事务里不能再用 s.db）。
func projectOfDirOn(ctx context.Context, db *gorm.DB, host, dir string) (*model.Project, error) {
	dir = normalizeProjectDir(dir)
	if dir == "" {
		return nil, nil
	}
	q := db.WithContext(ctx).Where("project_directory = ?", dir)
	if h := strings.TrimSpace(host); h != "" {
		q = q.Where("LOWER(host_name) = LOWER(?)", h)
	}
	var pd model.ProjectDir
	if err := q.Order("id ASC").Take(&pd).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	var p model.Project
	if err := db.WithContext(ctx).Where("archived_at IS NULL").First(&p, pd.ProjectID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &p, nil
}

// workflowDir 取工作流最近一次汇报会话所在的（主机，目录）；没有记录返回空串。
func (s *Store) workflowDir(ctx context.Context, workflowID string) (host, dir string) {
	return workflowDirOn(ctx, s.db, workflowID)
}

// workflowDirOn 是 workflowDir 的核心，可以在事务句柄上调用。
func workflowDirOn(ctx context.Context, db *gorm.DB, workflowID string) (host, dir string) {
	var sess model.FeedbackSession
	err := db.WithContext(ctx).Select("host_name", "project_directory").
		Where("workflow_id = ? AND project_directory <> ''", workflowID).
		Order("updated_at DESC").Limit(1).Take(&sess).Error
	if err != nil {
		return "", ""
	}
	return strings.TrimSpace(sess.HostName), normalizeProjectDir(sess.ProjectDirectory)
}

// ProjectOfWorkflow 返回工作流所属的项目；没有归属（或没有任何汇报会话）返回 nil, nil。
func (s *Store) ProjectOfWorkflow(ctx context.Context, workflowID string) (*model.Project, error) {
	wf := strings.TrimSpace(workflowID)
	if wf == "" {
		return nil, nil
	}
	host, dir := s.workflowDir(ctx, wf)
	if dir == "" {
		return nil, nil
	}
	return s.ProjectOfDir(ctx, host, dir)
}

// ProjectWorkflowIDs 列出项目里所有工作流（最近一次汇报会话的目录落在项目目录里的），按编号排序。
func (s *Store) ProjectWorkflowIDs(ctx context.Context, projectID uint) ([]string, error) {
	dirs, err := s.ListProjectDirs(ctx, projectID)
	if err != nil || len(dirs) == 0 {
		return nil, err
	}
	var rows []struct {
		WorkflowID       string
		HostName         string
		ProjectDirectory string
	}
	// 每个工作流只看最近一条有目录的会话。
	err = s.db.WithContext(ctx).Raw(`
		SELECT fs.workflow_id AS workflow_id, fs.host_name AS host_name, fs.project_directory AS project_directory
		FROM feedback_sessions fs
		JOIN (SELECT workflow_id, MAX(updated_at) AS mu FROM feedback_sessions WHERE project_directory <> '' AND workflow_id <> '' GROUP BY workflow_id) m
		  ON m.workflow_id = fs.workflow_id AND m.mu = fs.updated_at
		WHERE fs.project_directory <> ''`).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, r := range rows {
		d := normalizeProjectDir(r.ProjectDirectory)
		for _, pd := range dirs {
			if pd.ProjectDirectory != d {
				continue
			}
			if h := strings.TrimSpace(r.HostName); h == "" || strings.EqualFold(h, pd.HostName) {
				set[r.WorkflowID] = true
				break
			}
		}
	}
	out := make([]string, 0, len(set))
	for w := range set {
		out = append(out, w)
	}
	sort.Strings(out)
	return out, nil
}

// ───────────── 项目建议（迁移的第一步）─────────────

// ProjectSuggestion 是一个「待确认的项目建议」：一个还没有归属项目的（主机，目录），以及落在那里的工作流与文档数。
type ProjectSuggestion struct {
	HostName         string   `json:"host_name"`
	ProjectDirectory string   `json:"project_directory"`
	SuggestedName    string   `json:"suggested_name"`
	SuggestedSlug    string   `json:"suggested_slug,omitempty"`
	Workflows        []string `json:"workflows"`
	Docs             int      `json:"docs"`
}

// ProjectSuggestions 列出还没有归属项目的目录：每个（主机，目录）一条，名字取目录名。
// 用户可以逐条确认成项目，也可以把几条合并到同一个项目里（比如同一仓库的几个工作树）。
func (s *Store) ProjectSuggestions(ctx context.Context) ([]ProjectSuggestion, error) {
	var rows []struct {
		WorkflowID       string
		HostName         string
		ProjectDirectory string
	}
	err := s.db.WithContext(ctx).Raw(`
		SELECT fs.workflow_id AS workflow_id, fs.host_name AS host_name, fs.project_directory AS project_directory
		FROM feedback_sessions fs
		JOIN (SELECT workflow_id, MAX(updated_at) AS mu FROM feedback_sessions WHERE project_directory <> '' AND workflow_id <> '' GROUP BY workflow_id) m
		  ON m.workflow_id = fs.workflow_id AND m.mu = fs.updated_at
		WHERE fs.project_directory <> ''`).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	var owned []model.ProjectDir
	if err := s.db.WithContext(ctx).Find(&owned).Error; err != nil {
		return nil, err
	}
	taken := map[string]bool{}
	for _, d := range owned {
		taken[strings.ToLower(d.HostName)+"\x00"+d.ProjectDirectory] = true
	}
	docCount := map[string]int{}
	var stats []struct {
		WorkflowID string
		N          int
	}
	if err := s.db.WithContext(ctx).Model(&model.ProjectDoc{}).Select("workflow_id, COUNT(*) AS n").Where("deleted_at IS NULL").Group("workflow_id").Scan(&stats).Error; err != nil {
		return nil, err
	}
	for _, st := range stats {
		docCount[st.WorkflowID] = st.N
	}
	byKey := map[string]*ProjectSuggestion{}
	for _, r := range rows {
		dir := normalizeProjectDir(r.ProjectDirectory)
		host := strings.TrimSpace(r.HostName)
		if dir == "" || host == "" {
			continue // 没有主机名就没法登记目录归属
		}
		key := strings.ToLower(host) + "\x00" + dir
		if taken[key] {
			continue
		}
		sg := byKey[key]
		if sg == nil {
			name := dirBase(dir)
			sg = &ProjectSuggestion{HostName: host, ProjectDirectory: dir, SuggestedName: name, SuggestedSlug: SlugifyProjectName(name)}
			byKey[key] = sg
		}
		sg.Workflows = append(sg.Workflows, r.WorkflowID)
		sg.Docs += docCount[r.WorkflowID]
	}
	out := make([]ProjectSuggestion, 0, len(byKey))
	for _, sg := range byKey {
		sort.Strings(sg.Workflows)
		out = append(out, *sg)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Docs != out[j].Docs {
			return out[i].Docs > out[j].Docs
		}
		return out[i].HostName+out[i].ProjectDirectory < out[j].HostName+out[j].ProjectDirectory
	})
	return out, nil
}

func dirBase(dir string) string {
	d := strings.ReplaceAll(dir, `\`, "/")
	d = strings.TrimRight(d, "/")
	if i := strings.LastIndex(d, "/"); i >= 0 && i < len(d)-1 {
		return d[i+1:]
	}
	if d == "" {
		return dir
	}
	return d
}

// ───────────── 模块树 ─────────────

func (s *Store) ListProjectModules(ctx context.Context, projectID uint) ([]model.ProjectModule, error) {
	var out []model.ProjectModule
	return out, s.db.WithContext(ctx).Where("project_id = ?", projectID).Order("path ASC").Find(&out).Error
}

// ModuleInput 新建 / 修改模块。
type ModuleInput struct {
	Path            string
	Title           string
	Description     string
	OwnerIdentityID uint
}

// UpsertProjectModule 按路径创建或修改模块。父级模块不存在会一并补建（标题取路径末段），保证树是连通的。
func (s *Store) UpsertProjectModule(ctx context.Context, projectID uint, in ModuleInput) (*model.ProjectModule, error) {
	path, err := NormalizeModulePath(in.Path)
	if err != nil {
		return nil, NewInvalidInputError(err.Error())
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = path[strings.LastIndex(path, "/")+1:]
	}
	if len([]rune(title)) > moduleTitleMax {
		return nil, NewInvalidInputError(fmt.Sprintf("模块名称最多 %d 个字符", moduleTitleMax))
	}
	desc := strings.TrimSpace(in.Description)
	if len([]rune(desc)) > projectDescMax {
		return nil, NewInvalidInputError(fmt.Sprintf("模块说明最多 %d 个字符", projectDescMax))
	}
	if _, err := s.GetProject(ctx, projectID); err != nil {
		return nil, err
	}
	var out model.ProjectModule
	err = s.WithTx(ctx, func(tx *gorm.DB) error {
		var n int64
		if err := tx.Model(&model.ProjectModule{}).Where("project_id = ?", projectID).Count(&n).Error; err != nil {
			return err
		}
		now := time.Now()
		// 补建缺失的父级。
		segs := strings.Split(path, "/")
		for i := 1; i < len(segs); i++ {
			parent := strings.Join(segs[:i], "/")
			var c int64
			if err := tx.Model(&model.ProjectModule{}).Where("project_id = ? AND path = ?", projectID, parent).Count(&c).Error; err != nil {
				return err
			}
			if c == 0 {
				if n >= projectModulesMax {
					return NewInvalidInputError(fmt.Sprintf("每个项目最多 %d 个模块", projectModulesMax))
				}
				if err := tx.Create(&model.ProjectModule{ProjectID: projectID, Path: parent, Title: segs[i-1], CreatedAt: now}).Error; err != nil {
					return err
				}
				n++
			}
		}
		err := tx.Where("project_id = ? AND path = ?", projectID, path).Take(&out).Error
		switch {
		case err == nil:
			out.Title, out.Description = title, desc
			if in.OwnerIdentityID != 0 || out.OwnerIdentityID != 0 {
				out.OwnerIdentityID = in.OwnerIdentityID
			}
			return tx.Save(&out).Error
		case errors.Is(err, gorm.ErrRecordNotFound):
			if n >= projectModulesMax {
				return NewInvalidInputError(fmt.Sprintf("每个项目最多 %d 个模块", projectModulesMax))
			}
			out = model.ProjectModule{ProjectID: projectID, Path: path, Title: title, Description: desc, OwnerIdentityID: in.OwnerIdentityID, CreatedAt: now}
			return tx.Create(&out).Error
		default:
			return err
		}
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteProjectModule 删除模块（及其子模块）；挂在这些模块上的文档不删除，只是解除模块绑定。
func (s *Store) DeleteProjectModule(ctx context.Context, projectID uint, path string) error {
	p, err := NormalizeModulePath(path)
	if err != nil {
		return NewInvalidInputError(err.Error())
	}
	return s.WithTx(ctx, func(tx *gorm.DB) error {
		res := tx.Where("project_id = ? AND (path = ? OR path LIKE ?)", projectID, p, p+"/%").Delete(&model.ProjectModule{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return NewNotFoundError("模块不存在")
		}
		return tx.Model(&model.ProjectDoc{}).Where("project_id = ? AND (module_path = ? OR module_path LIKE ?)", projectID, p, p+"/%").
			UpdateColumn("module_path", "").Error
	})
}

// ProjectStats 是项目的几个计数，给列表与详情用。
type ProjectStats struct {
	Dirs      int `json:"dirs"`
	Modules   int `json:"modules"`
	Docs      int `json:"docs"`
	Workflows int `json:"workflows"`
}

// ProjectStatsOf 统计项目的目录、模块、文档与工作流数量。
func (s *Store) ProjectStatsOf(ctx context.Context, projectID uint) (ProjectStats, error) {
	var st ProjectStats
	var n int64
	db := s.db.WithContext(ctx)
	if err := db.Model(&model.ProjectDir{}).Where("project_id = ?", projectID).Count(&n).Error; err != nil {
		return st, err
	}
	st.Dirs = int(n)
	if err := db.Model(&model.ProjectModule{}).Where("project_id = ?", projectID).Count(&n).Error; err != nil {
		return st, err
	}
	st.Modules = int(n)
	if err := db.Model(&model.ProjectDoc{}).Where("project_id = ? AND deleted_at IS NULL AND status <> ?", projectID, model.DocStatusArchived).Count(&n).Error; err != nil {
		return st, err
	}
	st.Docs = int(n)
	wfs, err := s.ProjectWorkflowIDs(ctx, projectID)
	if err != nil {
		return st, err
	}
	st.Workflows = len(wfs)
	return st, nil
}

// GetProjectModule 取项目里的一个模块；不存在返回 nil, nil。
func (s *Store) GetProjectModule(ctx context.Context, projectID uint, path string) (*model.ProjectModule, error) {
	var m model.ProjectModule
	err := s.db.WithContext(ctx).Where("project_id = ? AND path = ?", projectID, path).Take(&m).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// ModuleOwnerFor 返回模块的负责人：模块自己没有就沿父模块往上找最近的一个。没有返回 0。
func (s *Store) ModuleOwnerFor(ctx context.Context, projectID uint, path string) uint {
	for p := strings.Trim(path, "/"); p != ""; {
		if m, err := s.GetProjectModule(ctx, projectID, p); err == nil && m != nil && m.OwnerIdentityID != 0 {
			return m.OwnerIdentityID
		}
		i := strings.LastIndex(p, "/")
		if i < 0 {
			break
		}
		p = p[:i]
	}
	return 0
}

// ResolveDocModule 返回一篇文档（路径 + 可选的显式模块）实际会绑定到的模块路径；没有则空串。
func (s *Store) ResolveDocModule(ctx context.Context, projectID uint, docPath, explicit string) string {
	if e := strings.Trim(strings.TrimSpace(explicit), "/"); e != "" {
		return e
	}
	return s.deriveModulePath(ctx, projectID, docPath)
}

// LatestWorkflowOfIdentity 是身份最近活跃的工作流（给授权提醒找收件箱用）；没有返回空串。
func (s *Store) LatestWorkflowOfIdentity(ctx context.Context, identityID uint) string {
	if identityID == 0 {
		return ""
	}
	var sess model.AgentSession
	if err := s.db.WithContext(ctx).Select("workflow_id").Where("identity_id = ? AND workflow_id <> ''", identityID).
		Order("last_event_at DESC").Limit(1).Take(&sess).Error; err == nil {
		return sess.WorkflowID
	}
	var row model.AgentIdentity
	if err := s.db.WithContext(ctx).Select("host_name", "project_directory").Where("id = ?", identityID).Take(&row).Error; err != nil {
		return ""
	}
	var fs model.FeedbackSession
	if err := s.db.WithContext(ctx).Select("workflow_id").
		Where("project_directory = ? AND host_name = ? AND workflow_id <> ''", row.ProjectDirectory, row.HostName).
		Order("updated_at DESC").Limit(1).Take(&fs).Error; err == nil {
		return fs.WorkflowID
	}
	return ""
}
