package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

func TestNormalizeWebWorkflowTitle(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"普通名字原样", "支付重构", "支付重构", false},
		{"首尾空白去掉", "  支付重构  ", "支付重构", false},
		{"换行制表折成空格", "支付\n重构\t二期", "支付 重构 二期", false},
		{"连续空白折成一个", "a   b \u00a0 c", "a b c", false},
		{"零宽字符丢掉", "支\u200b付", "支付", false},
		{"只有空白拒绝", " \n\t ", "", true},
		{"只有零宽字符拒绝", "\u200b\u200d", "", true},
		{"空串拒绝", "", "", true},
		{"八十个字符刚好", strings.Repeat("字", 80), strings.Repeat("字", 80), false},
		{"八十一个字符拒绝", strings.Repeat("字", 81), "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := NormalizeWebWorkflowTitle(c.in)
			if c.wantErr {
				var appErr *AppError
				if err == nil || !errors.As(err, &appErr) || appErr.Kind != ErrInvalidInput {
					t.Fatalf("应返回入参错误: got=%q err=%v", got, err)
				}
				return
			}
			if err != nil || got != c.want {
				t.Fatalf("规整结果不对: got=%q want=%q err=%v", got, c.want, err)
			}
		})
	}
}

func TestWebWorkflowStartPrompt_CarriesWorkflowID(t *testing.T) {
	p := WebWorkflowStartPrompt("支付重构", "wf-web-x", "")
	for _, want := range []string{"「支付重构」", "workflow_id：wf-web-x", `workflow_id："wf-web-x"`, "interactive_feedback", "project_directory 填你当前工作区的绝对路径", "inbox"} {
		if !strings.Contains(p, want) {
			t.Errorf("启动提示词缺少 %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "```") {
		t.Errorf("启动提示词里不能有代码围栏（会撑破占位会话里包着它的代码块）:\n%s", p)
	}
}

// 绑定了项目目录时，提示词要求智能体原样回填这个目录（否则 D-161 会把它挡在门外）；Windows 路径的反斜杠不能被转义。
func TestWebWorkflowStartPrompt_BoundDirectory(t *testing.T) {
	for _, dir := range []string{"/srv/app", `C:\Users\me\proj`, `\\fileserver\share\proj`} {
		p := WebWorkflowStartPrompt("支付重构", "wf-web-x", dir)
		if !strings.Contains(p, `project_directory 必须原样填 "`+dir+`"`) {
			t.Errorf("绑定目录 %q 后提示词应要求原样回填:\n%s", dir, p)
		}
		if strings.Contains(p, "填你当前工作区的绝对路径") {
			t.Errorf("绑定目录后不该再让智能体填自己的工作区:\n%s", p)
		}
		if strings.Contains(p, "```") {
			t.Errorf("启动提示词里不能有代码围栏:\n%s", p)
		}
	}
}

func TestNormalizeWebProjectDirectory(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"空串 = 不绑定", "", "", false},
		{"只有空白 = 不绑定", "  \t ", "", false},
		{"POSIX 绝对路径原样", "/srv/app", "/srv/app", false},
		{"POSIX 末尾斜杠去掉", "/srv/app/", "/srv/app", false},
		{"POSIX 多余的斜杠与点收拢", "/srv//app/./sub/", "/srv/app/sub", false},
		{"POSIX .. 收拢", "/srv/app/../other", "/srv/other", false},
		{"POSIX 根目录", "/", "/", false},
		{"首尾空白去掉", "  /srv/app  ", "/srv/app", false},
		{"Windows 反斜杠原样（只去末尾分隔符）", `C:\Users\me\proj\`, `C:\Users\me\proj`, false},
		{"Windows 正斜杠原样", "D:/work/proj/", "D:/work/proj", false},
		{"Windows 盘符根保留", `C:\`, `C:\`, false},
		{"Windows 盘符根多个分隔符", `C:\\\`, `C:\`, false},
		{"Windows 大小写不动", `c:\Users\Me`, `c:\Users\Me`, false},
		{"UNC 路径", `\\fileserver\share\proj\`, `\\fileserver\share\proj`, false},
		{"UNC 只有服务器拒绝", `\\fileserver`, "", true},
		{"UNC 服务器带一个分隔符拒绝", `\\fileserver\`, "", true},
		{"相对路径拒绝", "src/app", "", true},
		{"点开头拒绝", "./app", "", true},
		{"波浪号拒绝", "~/proj", "", true},
		{"只有盘符拒绝", "C:", "", true},
		{"盘符后不是分隔符拒绝", "C:proj", "", true},
		{"含换行拒绝", "/srv/app\n/etc", "", true},
		{"含制表拒绝", "/srv/a\tpp", "", true},
		{"含 NUL 拒绝", "/srv/a\x00pp", "", true},
		{"含零宽字符拒绝", "/srv/a\u200bpp", "", true},
		{"含三个反引号拒绝（会撑破提示词代码块）", "/srv/```/x", "", true},
		{"含单个反引号可以", "/srv/a`b", "/srv/a`b", false},
		{"刚好 1024 个字符", "/" + strings.Repeat("a", 1023), "/" + strings.Repeat("a", 1023), false},
		{"超过 1024 个字符拒绝", "/" + strings.Repeat("a", 1024), "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := NormalizeWebProjectDirectory(c.in)
			if c.wantErr {
				var appErr *AppError
				if err == nil || !errors.As(err, &appErr) || appErr.Kind != ErrInvalidInput {
					t.Fatalf("应返回入参错误: got=%q err=%v", got, err)
				}
				return
			}
			if err != nil || got != c.want {
				t.Fatalf("规整结果不对: got=%q want=%q err=%v", got, c.want, err)
			}
		})
	}
}

func TestNormalizeWebHostName(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"空串 = 不指定", "", "", false},
		{"只有空白 = 不指定", " \n ", "", false},
		{"普通主机名原样", "dev-box.local", "dev-box.local", false},
		{"首尾空白与内部换行折叠", "  dev\nbox  ", "dev box", false},
		{"零宽字符丢掉", "dev\u200b-box", "dev-box", false},
		{"刚好 120 个字符", strings.Repeat("h", 120), strings.Repeat("h", 120), false},
		{"超过 120 个字符拒绝", strings.Repeat("h", 121), "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := NormalizeWebHostName(c.in)
			if c.wantErr {
				var appErr *AppError
				if err == nil || !errors.As(err, &appErr) || appErr.Kind != ErrInvalidInput {
					t.Fatalf("应返回入参错误: got=%q err=%v", got, err)
				}
				return
			}
			if err != nil || got != c.want {
				t.Fatalf("规整结果不对: got=%q want=%q err=%v", got, c.want, err)
			}
		})
	}
}

func TestNormalizeWebFirstMessage(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"空串 = 没有首条指令", "", "", false},
		{"只有空白 = 没有首条指令", " \n\t ", "", false},
		{"首尾空白去掉、正文换行保留", "  先读 README\n再改 main.go\n  ", "先读 README\n再改 main.go", false},
		{"含 NUL 拒绝", "a\x00b", "", true},
		{"刚好两万个字符", strings.Repeat("字", 20000), strings.Repeat("字", 20000), false},
		{"超过两万个字符拒绝", strings.Repeat("字", 20001), "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := NormalizeWebFirstMessage(c.in)
			if c.wantErr {
				var appErr *AppError
				if err == nil || !errors.As(err, &appErr) || appErr.Kind != ErrInvalidInput {
					t.Fatalf("应返回入参错误: got=%q err=%v", got, err)
				}
				return
			}
			if err != nil || got != c.want {
				t.Fatalf("规整结果不对: got=%q want=%q err=%v", got, c.want, err)
			}
		})
	}
}

func TestNormalizeWebPhase(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", "", false},
		{"  ", "", false},
		{"discuss", model.PhaseDiscuss, false},
		{"plan", model.PhasePlan, false},
		{" dev ", model.PhaseDev, false},
		{"assess", model.PhaseDiscuss, false}, // 旧五段式编号换算
		{"verify", model.PhaseDev, false},
		{"done", model.PhaseDev, false},
		{"Plan", "", true}, // 大小写敏感，与阶段接口一致
		{"review", "", true},
		{"custom-phase", "", true},
	}
	for _, c := range cases {
		got, err := NormalizeWebPhase(c.in)
		if c.wantErr {
			var appErr *AppError
			if err == nil || !errors.As(err, &appErr) || appErr.Kind != ErrInvalidInput {
				t.Errorf("阶段 %q 应返回入参错误: got=%q err=%v", c.in, got, err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("阶段 %q 规整结果不对: got=%q want=%q err=%v", c.in, got, c.want, err)
		}
	}
}

func TestCreateWebWorkflow_PlaceholderShape(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()

	res, err := st.CreateWebWorkflow(ctx, CreateWebWorkflowInput{Title: "  支付\n重构  ", EnvHostName: "env-host"})
	if err != nil {
		t.Fatalf("新建会话: %v", err)
	}
	s := res.Session
	if !strings.HasPrefix(s.WorkflowID, "wf-web-") {
		t.Errorf("自动生成的工作流编号应以 wf-web- 开头: %q", s.WorkflowID)
	}
	if !IsWebPlaceholderSessionID(s.ID) {
		t.Errorf("占位会话编号应以 %s 开头: %q", WebPlaceholderSessionPrefix, s.ID)
	}
	if s.Kind != model.FeedbackKindNotice || s.Status != "completed" || !s.ConsumedByAI || s.ConsumedAt == nil {
		t.Errorf("占位会话应是已消费的 notice: kind=%q status=%q consumed=%v", s.Kind, s.Status, s.ConsumedByAI)
	}
	if s.Title != "支付 重构" {
		t.Errorf("标题应是规整后的名字: %q", s.Title)
	}
	if s.ProjectDirectory != "" || s.CredentialID != nil {
		t.Errorf("占位会话不带项目目录与凭据（智能体第一次调用才是真值）: dir=%q cred=%v", s.ProjectDirectory, s.CredentialID)
	}
	if s.HostName != "env-host" {
		t.Errorf("没有全局主机名时应落服务环境主机名: %q", s.HostName)
	}
	if !strings.Contains(s.Summary, s.WorkflowID) || !strings.Contains(s.Summary, res.StartPrompt) {
		t.Errorf("占位会话的说明里应带工作流编号与启动提示词:\n%s", s.Summary)
	}
	if !strings.Contains(res.StartPrompt, s.WorkflowID) {
		t.Errorf("启动提示词里应带工作流编号: %s", res.StartPrompt)
	}

	// 用户起的名字固定下来：重命名同一机制的设置项
	var setting model.SystemSetting
	if err := st.db.Where("key = ?", "wf_custom_title:"+s.WorkflowID).First(&setting).Error; err != nil || setting.Value != "支付 重构" {
		t.Fatalf("应写入自定义标题设置: %+v err=%v", setting, err)
	}
	// 落库的就是这一行
	var rows []model.FeedbackSession
	if err := st.db.Where("workflow_id = ?", s.WorkflowID).Find(&rows).Error; err != nil || len(rows) != 1 || rows[0].ID != s.ID {
		t.Fatalf("库里应正好有这一行占位会话: %v err=%v", rows, err)
	}
}

func TestCreateWebWorkflow_ExplicitWorkflowID(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()

	res, err := st.CreateWebWorkflow(ctx, CreateWebWorkflowInput{Title: "分支 A", WorkflowID: "wf-branch-a"})
	if err != nil || res.Session.WorkflowID != "wf-branch-a" {
		t.Fatalf("给定合规编号应原样采用: %+v err=%v", res, err)
	}

	// 编号撞了：冲突，且不留半截数据
	_, err = st.CreateWebWorkflow(ctx, CreateWebWorkflowInput{Title: "分支 A 二号", WorkflowID: "wf-branch-a"})
	var appErr *AppError
	if err == nil || !errors.As(err, &appErr) || appErr.Kind != ErrConflict {
		t.Fatalf("编号已存在应返回冲突错误: %v", err)
	}
	var n int64
	if err := st.db.Model(&model.FeedbackSession{}).Where("workflow_id = ?", "wf-branch-a").Count(&n).Error; err != nil || n != 1 {
		t.Fatalf("冲突不应多落一行: n=%d err=%v", n, err)
	}
	var setting model.SystemSetting
	if err := st.db.Where("key = ?", "wf_custom_title:wf-branch-a").First(&setting).Error; err != nil || setting.Value != "分支 A" {
		t.Fatalf("冲突不应改动原来的自定义标题: %+v err=%v", setting, err)
	}

	for _, bad := range []string{"ab", "-lead", "has space", "含中文编号", strings.Repeat("a", 65), "a/b"} {
		_, err := st.CreateWebWorkflow(ctx, CreateWebWorkflowInput{Title: "x", WorkflowID: bad})
		if err == nil || !errors.As(err, &appErr) || appErr.Kind != ErrInvalidInput {
			t.Errorf("不合规的编号 %q 应返回入参错误: %v", bad, err)
		}
	}
	if _, err := st.CreateWebWorkflow(ctx, CreateWebWorkflowInput{Title: strings.Repeat("长", 81)}); err == nil {
		t.Error("名字太长应拒绝")
	}
}

// 高级选项（#184）：绑定项目目录与主机。占位会话带上规整后的目录与用户指定的主机；
// 智能体用同一个目录接入成功（末尾斜杠不算不同），换目录被 D-161 拒绝（与既有的跨项目保护同一口径）。
func TestCreateWebWorkflow_BoundDirectoryAndHost(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()

	res, err := st.CreateWebWorkflow(ctx, CreateWebWorkflowInput{
		Title: "绑定目录", ProjectDirectory: " /tmp/bound-proj/ ", HostName: " dev\nbox ", EnvHostName: "env-host",
	})
	if err != nil {
		t.Fatalf("新建会话: %v", err)
	}
	s, wf := res.Session, res.Session.WorkflowID
	if s.ProjectDirectory != "/tmp/bound-proj" {
		t.Errorf("占位会话应带规整后的项目目录: %q", s.ProjectDirectory)
	}
	if s.HostName != "dev box" {
		t.Errorf("用户指定的主机应优先于环境主机名: %q", s.HostName)
	}
	if !strings.Contains(res.StartPrompt, `project_directory 必须原样填 "/tmp/bound-proj"`) {
		t.Errorf("绑定目录后启动提示词应要求原样回填:\n%s", res.StartPrompt)
	}
	if !strings.Contains(s.Summary, "/tmp/bound-proj") || !strings.Contains(s.Summary, res.StartPrompt) {
		t.Errorf("占位会话的说明里应写出绑定的目录并带着启动提示词:\n%s", s.Summary)
	}

	// 换目录汇报：D-161 冲突（阻塞汇报与异步通知两条路径都是）
	var appErr *AppError
	if _, err := st.CreateFeedbackSession(ctx, CreateSessionInput{WorkflowID: wf, ProjectDirectory: "/tmp/other-proj", Summary: "换了目录", TimeoutSeconds: 60}); err == nil || !errors.As(err, &appErr) || appErr.Kind != ErrConflict {
		t.Fatalf("换目录的阻塞汇报应被项目所有权校验拒绝: %v", err)
	}
	if _, err := st.CreateNoticeSession(ctx, CreateSessionInput{WorkflowID: wf, ProjectDirectory: "/tmp/other-proj", Summary: "换了目录的通知"}); err == nil || !errors.As(err, &appErr) || appErr.Kind != ErrConflict {
		t.Fatalf("换目录的异步通知应被项目所有权校验拒绝: %v", err)
	}

	// 同一个目录（带末尾斜杠也算同一个）接入成功，标题沿用用户起的名字
	ok, err := st.CreateFeedbackSession(ctx, CreateSessionInput{WorkflowID: wf, ProjectDirectory: "/tmp/bound-proj/", Summary: "同目录接入", TimeoutSeconds: 60})
	if err != nil {
		t.Fatalf("同目录接入被挡住了: %v", err)
	}
	if ok.WorkflowID != wf || ok.Title != "绑定目录" {
		t.Errorf("接入应落进同一个工作流并沿用用户的名字: wf=%q title=%q", ok.WorkflowID, ok.Title)
	}
}

// WorkflowBoundDirectory 与 D-161 同一口径：取 id 最小的那一行，规整后返回；没绑定 / 没有这个工作流 / 空编号都是空串。
func TestWorkflowBoundDirectory(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()

	bound, err := st.CreateWebWorkflow(ctx, CreateWebWorkflowInput{Title: "绑定", ProjectDirectory: "/tmp/bound-proj/"})
	if err != nil {
		t.Fatal(err)
	}
	free, err := st.CreateWebWorkflow(ctx, CreateWebWorkflowInput{Title: "不绑定"})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ name, wf, want string }{
		{"绑定了目录", bound.Session.WorkflowID, "/tmp/bound-proj"},
		{"没绑定目录", free.Session.WorkflowID, ""},
		{"不存在的工作流", "wf-nope-1234", ""},
		{"空编号", "  ", ""},
	}
	for _, c := range cases {
		if got, err := st.WorkflowBoundDirectory(ctx, c.wf); err != nil || got != c.want {
			t.Errorf("%s: got %q err=%v, want %q", c.name, got, err, c.want)
		}
	}
}

// 不合规的目录 / 主机在落库之前就被拒绝：不留下半个会话，也不留自定义标题。
func TestCreateWebWorkflow_BadAdvancedInputLeavesNothing(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()

	cases := []CreateWebWorkflowInput{
		{Title: "不留痕迹", WorkflowID: "wf-bad-dir", ProjectDirectory: "relative/dir"},
		{Title: "不留痕迹", WorkflowID: "wf-bad-dir2", ProjectDirectory: "/ok\n/no"},
		{Title: "不留痕迹", WorkflowID: "wf-bad-host", HostName: strings.Repeat("h", 121)},
	}
	for _, in := range cases {
		var appErr *AppError
		if _, err := st.CreateWebWorkflow(ctx, in); err == nil || !errors.As(err, &appErr) || appErr.Kind != ErrInvalidInput {
			t.Errorf("%+v 应返回入参错误: %v", in, err)
		}
		var n int64
		if err := st.db.Model(&model.FeedbackSession{}).Where("workflow_id = ?", in.WorkflowID).Count(&n).Error; err != nil || n != 0 {
			t.Errorf("被拒绝的请求不该落会话行: wf=%s n=%d err=%v", in.WorkflowID, n, err)
		}
		var s int64
		if err := st.db.Model(&model.SystemSetting{}).Where("key = ?", "wf_custom_title:"+in.WorkflowID).Count(&s).Error; err != nil || s != 0 {
			t.Errorf("被拒绝的请求不该写自定义标题: wf=%s n=%d err=%v", in.WorkflowID, s, err)
		}
	}
}

// 占位会话不能影响智能体的正常接入，也不能被别的会话误继承（C-RT：占位行没有目录与凭据，查询又都以非空为前提）。
func TestCreateWebWorkflow_DoesNotInterfereWithInheritance(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()

	placeholder, err := st.CreateWebWorkflow(ctx, CreateWebWorkflowInput{Title: "新会话"})
	if err != nil {
		t.Fatalf("新建会话: %v", err)
	}
	wf := placeholder.Session.WorkflowID

	// 1. 别的会话（没给工作流编号）按目录继承时，不会被占位会话截走
	other, err := st.CreateFeedbackSession(ctx, CreateSessionInput{ProjectDirectory: "/tmp/web-wf-other", Summary: "别的项目的第一轮"})
	if err != nil {
		t.Fatalf("建别的会话: %v", err)
	}
	if other.WorkflowID == wf {
		t.Fatalf("没带目录继承信息的新会话不应落进占位工作流: %s", other.WorkflowID)
	}
	// 完全不带目录与凭据的也一样（空字符串不能匹配占位行的空目录）
	bare, err := st.CreateFeedbackSession(ctx, CreateSessionInput{Summary: "什么都没带的第一轮"})
	if err != nil {
		t.Fatalf("建裸会话: %v", err)
	}
	if bare.WorkflowID == wf {
		t.Fatalf("什么都没带的新会话不应落进占位工作流: %s", bare.WorkflowID)
	}

	// 2. 智能体带着工作流编号与自己的目录接入：不冲突（D-161 只在两边都非空时才比较），标题沿用用户起的名字
	notice, err := st.CreateNoticeSession(ctx, CreateSessionInput{WorkflowID: wf, ProjectDirectory: "/tmp/web-wf-agent", Title: "智能体自己起的标题", Summary: "已接入"})
	if err != nil {
		t.Fatalf("智能体首次汇报（异步通知）被占位会话挡住了: %v", err)
	}
	if notice.Title != "新会话" {
		t.Errorf("智能体汇报里带的 title 不应覆盖用户起的名字: %q", notice.Title)
	}
	if notice.ProjectDirectory != "/tmp/web-wf-agent" {
		t.Errorf("接入后的会话行应带智能体自己的目录: %q", notice.ProjectDirectory)
	}
	blocking, err := st.CreateFeedbackSession(ctx, CreateSessionInput{WorkflowID: wf, ProjectDirectory: "/tmp/web-wf-agent", Summary: "阻塞汇报", TimeoutSeconds: 60})
	if err != nil {
		t.Fatalf("智能体首次阻塞汇报被占位会话挡住了: %v", err)
	}
	if blocking.WorkflowID != wf || blocking.Title != "新会话" {
		t.Errorf("阻塞汇报应落进同一个工作流并沿用用户的名字: wf=%q title=%q", blocking.WorkflowID, blocking.Title)
	}

	// 3. 接入之后，同一目录里不带编号的后续调用照旧按目录继承到这个工作流
	next, err := st.CreateFeedbackSession(ctx, CreateSessionInput{ProjectDirectory: "/tmp/web-wf-agent", Summary: "没带编号的下一轮"})
	if err != nil {
		t.Fatalf("接入后的下一轮: %v", err)
	}
	if next.WorkflowID != wf {
		t.Errorf("接入后同目录不带编号的调用应继承到这个工作流: got=%q want=%q", next.WorkflowID, wf)
	}
}

// 绑定了项目目录（#184）的占位会话参与「按目录继承最近工作流」：这是有意保留的口径（用户亲手把这个会话绑到了这个目录），
// 不是漏洞——只有「没带工作流编号」的调用才会被继承进来，带了编号的调用不受影响；换一个目录也不受影响。
// 文档（docs/terminal-operations.md §8.11）与新建会话弹窗里的说明都按这个行为写，这里把它钉住。
func TestCreateWebWorkflow_BoundDirectoryJoinsDirectoryInheritance(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()

	bound, err := st.CreateWebWorkflow(ctx, CreateWebWorkflowInput{Title: "绑定目录", ProjectDirectory: "/tmp/web-wf-inherit/"})
	if err != nil {
		t.Fatalf("新建会话: %v", err)
	}
	wf := bound.Session.WorkflowID
	if got := bound.Session.ProjectDirectory; got != "/tmp/web-wf-inherit" {
		t.Fatalf("占位会话应带规整后的目录: %q", got)
	}

	// 同一个目录里没带工作流编号的调用：继承到这个绑定了目录的工作流
	inherited, err := st.CreateFeedbackSession(ctx, CreateSessionInput{ProjectDirectory: "/tmp/web-wf-inherit", Summary: "没带编号的第一轮"})
	if err != nil {
		t.Fatalf("没带编号的调用: %v", err)
	}
	if inherited.WorkflowID != wf {
		t.Errorf("绑定了目录的工作流应成为该目录里没带编号的调用的继承目标: got=%q want=%q", inherited.WorkflowID, wf)
	}

	// 带了别的工作流编号的调用：不受影响，落进它自己的工作流
	own, err := st.CreateFeedbackSession(ctx, CreateSessionInput{WorkflowID: "own-workflow-184", ProjectDirectory: "/tmp/web-wf-inherit", Summary: "带编号的调用"})
	if err != nil {
		t.Fatalf("带编号的调用: %v", err)
	}
	if own.WorkflowID != "own-workflow-184" {
		t.Errorf("带了工作流编号的调用不该被继承规则改写: %q", own.WorkflowID)
	}

	// 别的目录里没带编号的调用：同样不受影响
	elsewhere, err := st.CreateFeedbackSession(ctx, CreateSessionInput{ProjectDirectory: "/tmp/web-wf-inherit-other", Summary: "别的目录"})
	if err != nil {
		t.Fatalf("别的目录的调用: %v", err)
	}
	if elsewhere.WorkflowID == wf {
		t.Errorf("别的目录里没带编号的调用不该落进绑定了目录的工作流: %q", elsewhere.WorkflowID)
	}
}
