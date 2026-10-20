package store

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 网页手动新建会话（#174 第一期）。
//
// 起因：以前一个工作流只能由智能体第一次调用 RelayMesh 时「隐式」生出来，用户想在网页里先把会话建好、
// 再去启用对应的智能体，就得先开一轮沟通让智能体把会话建出来。现在改成网页先建：落一行占位会话，
// 再把一段「启动提示词」交给用户，用户把它发给要使用的智能体，智能体第一次调用时带上这个 workflow_id 就接进来了。
//
// 占位会话的形态（与 CreateNoticeSession 一致，复用它在界面里的全部呈现）：
//   - Kind = notice，状态一建就是 completed 且已被智能体消费：界面上只有一条 AI 气泡（说明 + 启动提示词），没有回复框；
//   - 会话编号以 sess-web- 开头，前端据此识别「还没有智能体接入」的工作流（侧栏 / 页头显示「待接入」）；
//   - 默认不带项目目录与凭据：智能体第一次调用时带的目录与凭据才是真值。D-161（项目所有权）只在两边都非空时才比较，
//     而「按目录 / 按凭据继承最近工作流」的查询都以非空的目录 / 凭据为前提，所以占位会话既不会挡住智能体、也不会被别的会话误继承
//     （#184 的可选项「项目目录」是唯一的例外，见下）；
//   - 用户给的名字同时写进 wf_custom_title:<工作流>，之后智能体汇报里带的 title 不会把它冲掉（与「重命名」同一机制）。
//
// 用户在智能体接入之前写下的第一条指令走现有的「追加指令」：最近一轮是已被消费的 completed，所以会排进收件箱，
// 启动提示词里让智能体接入后先读收件箱。
//
// 「高级选项」（#184）：名字仍是唯一必填项，另有四个可选项——自己指定的工作流编号、首条指令、起始阶段、项目目录与主机。
// 其中**项目目录会落到占位会话上，工作流从此绑定该目录**（D-161）：智能体第一次汇报带的 project_directory 与它不一致就会被
// 拒绝（与既有的跨项目保护同一口径），所以绑定了目录时启动提示词会要求智能体原样回填这个目录；不填目录与以前完全一样。
// 首条指令与起始阶段由 API 层用现有的投递 / 阶段机制完成（见 internal/api/web_workflow.go），这里只提供入参规整。

// WebPlaceholderSessionPrefix 是网页新建会话时占位会话行的编号前缀。
const WebPlaceholderSessionPrefix = "sess-web-"

// WebWorkflowTitleMaxRunes 是网页新建会话时名字的最大字符数（按字符，不是字节）。
const WebWorkflowTitleMaxRunes = 80

// 「高级选项」各项的长度上限（按字符，不是字节）。
const (
	// WebWorkflowFirstMessageMaxRunes 是首条指令的最大字符数。
	WebWorkflowFirstMessageMaxRunes = 20000
	// WebWorkflowProjectDirMaxRunes 是项目目录的最大字符数。
	WebWorkflowProjectDirMaxRunes = 1024
	// WebWorkflowHostNameMaxRunes 是主机名的最大字符数。
	WebWorkflowHostNameMaxRunes = 120
)

// 自定义工作流编号：字母或数字开头，后面是字母 / 数字 / . _ -，3～64 个字符。
// 网页的「高级选项」里可以填它（不填自动生成）；前端 lib/newSession.ts 的 WORKFLOW_ID_PATTERN 必须与它保持一致。
var webWorkflowIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{2,63}$`)

// IsWebPlaceholderSessionID 判断一个会话编号是不是网页新建会话时落下的占位行。
func IsWebPlaceholderSessionID(sessionID string) bool {
	return strings.HasPrefix(sessionID, WebPlaceholderSessionPrefix)
}

// WorkflowBoundDirectory 返回这个工作流此刻被绑定的项目目录，口径与汇报路径上的 D-161 所有权校验完全一致：
// 取这个工作流里 id 最小的那一行，目录去首尾空白后用 filepath.Clean 规整；没有会话行、那一行没有目录、
// 或规整后是 "." 时返回空串（= 没有绑定，智能体第一次汇报带什么目录都行）。
// 「启用 Agent」据此提前告诉用户：在主机上选的运行目录与绑定的目录不一致时，智能体的第一次汇报会被拒绝。
func (s *Store) WorkflowBoundDirectory(ctx context.Context, workflowID string) (string, error) {
	workflowID = strings.TrimSpace(workflowID)
	if workflowID == "" {
		return "", nil
	}
	var first model.FeedbackSession
	err := s.db.WithContext(ctx).Where("workflow_id = ?", workflowID).Order("id asc").First(&first).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	dir := strings.TrimSpace(first.ProjectDirectory)
	if dir == "" {
		return "", nil
	}
	if dir = filepath.Clean(dir); dir == "." {
		return "", nil
	}
	return dir, nil
}

// NormalizeWebWorkflowTitle 规整网页新建会话时填的名字：空白与控制字符（含换行、制表）一律折成一个空格，
// 不可见的格式字符（零宽空格之类）直接丢掉，去掉首尾空白；规整后为空，或超过 WebWorkflowTitleMaxRunes 个字符的拒绝。
func NormalizeWebWorkflowTitle(raw string) (string, error) {
	title := foldSingleLine(raw)
	if title == "" {
		return "", NewInvalidInputError("title is required")
	}
	if n := utf8.RuneCountInString(title); n > WebWorkflowTitleMaxRunes {
		return "", NewInvalidInputError(fmt.Sprintf("title is too long (%d characters, max %d)", n, WebWorkflowTitleMaxRunes))
	}
	return title, nil
}

// foldSingleLine 把一段文字折成单行：空白与控制字符（含换行、制表）折成一个空格，不可见的格式字符（零宽空格之类）丢掉，去掉首尾空白。
func foldSingleLine(raw string) string {
	var b strings.Builder
	pendingSpace := false
	for _, r := range raw {
		switch {
		case unicode.Is(unicode.Cf, r):
			continue
		case unicode.IsSpace(r) || unicode.IsControl(r):
			pendingSpace = true
		default:
			if pendingSpace && b.Len() > 0 {
				b.WriteByte(' ')
			}
			pendingSpace = false
			b.WriteRune(r)
		}
	}
	return b.String()
}

// NormalizeWebHostName 规整网页新建会话时填的主机名（会话的归属标签）：折成单行；空串表示不指定（沿用全局设置 / 环境变量）；
// 超过 WebWorkflowHostNameMaxRunes 个字符的拒绝。
func NormalizeWebHostName(raw string) (string, error) {
	host := foldSingleLine(raw)
	if n := utf8.RuneCountInString(host); n > WebWorkflowHostNameMaxRunes {
		return "", NewInvalidInputError(fmt.Sprintf("host_name is too long (%d characters, max %d)", n, WebWorkflowHostNameMaxRunes))
	}
	return host, nil
}

// NormalizeWebProjectDirectory 规整网页新建会话时填的项目目录：空串表示不绑定（返回 ""）；
// 否则必须是绝对路径——POSIX 的 /…，Windows 的 X:\… 或 X:/…，以及 \\服务器\共享\…——不含控制字符，不超过
// WebWorkflowProjectDirMaxRunes 个字符。
//
// 规整只做「不会改变含义」的那部分：POSIX 路径做 path.Clean（多余的 /、. 与 ..、末尾 /）；Windows 路径只去掉末尾分隔符
// （盘符根 C:\ 保留），不改大小写、不换分隔符。原因：D-161 比较的是字符串，智能体按启动提示词把这个值原样回填，两边才会一致。
func NormalizeWebProjectDirectory(raw string) (string, error) {
	dir := strings.TrimSpace(raw)
	if dir == "" {
		return "", nil
	}
	if n := utf8.RuneCountInString(dir); n > WebWorkflowProjectDirMaxRunes {
		return "", NewInvalidInputError(fmt.Sprintf("project_directory is too long (%d characters, max %d)", n, WebWorkflowProjectDirMaxRunes))
	}
	for _, r := range dir {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return "", NewInvalidInputError("project_directory must not contain control or invisible characters")
		}
	}
	if strings.Contains(dir, "```") {
		// 目录会原样进启动提示词，而提示词被放在占位会话的 ``` 代码块里：三个反引号会撑破它。
		return "", NewInvalidInputError("project_directory must not contain \"```\"")
	}
	bad := NewInvalidInputError("project_directory must be an absolute path (starting with '/', a drive letter like 'C:\\', or '\\\\server\\share')")
	switch {
	case strings.HasPrefix(dir, `\\`):
		// UNC：至少要有「服务器」与「共享名」两段。
		trimmed := strings.TrimRight(dir, `\/`)
		parts := strings.FieldsFunc(strings.TrimLeft(trimmed, `\/`), func(r rune) bool { return r == '\\' || r == '/' })
		if len(parts) < 2 {
			return "", bad
		}
		return trimmed, nil
	case len(dir) >= 3 && isASCIILetter(dir[0]) && dir[1] == ':' && (dir[2] == '\\' || dir[2] == '/'):
		return dir[:3] + strings.TrimRight(dir[3:], `\/`), nil
	case strings.HasPrefix(dir, "/"):
		return path.Clean(dir), nil
	default:
		return "", bad
	}
}

func isASCIILetter(b byte) bool { return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') }

// NormalizeWebFirstMessage 规整网页新建会话时写的首条指令：去首尾空白（正文里的换行原样保留）；空串表示没有首条指令；
// 含 NUL 或超过 WebWorkflowFirstMessageMaxRunes 个字符的拒绝。
func NormalizeWebFirstMessage(raw string) (string, error) {
	msg := strings.TrimSpace(raw)
	if strings.ContainsRune(msg, 0) {
		return "", NewInvalidInputError("first_message must not contain NUL characters")
	}
	if n := utf8.RuneCountInString(msg); n > WebWorkflowFirstMessageMaxRunes {
		return "", NewInvalidInputError(fmt.Sprintf("first_message is too long (%d characters, max %d)", n, WebWorkflowFirstMessageMaxRunes))
	}
	return msg, nil
}

// NormalizeWebPhase 规整网页新建会话时选的起始阶段：空串表示不指定（沿用默认阶段）；
// 只接受 discuss / plan / dev，旧五段式的编号（assess / verify / done）换算成对应的新编号后接受；其它拒绝。
func NormalizeWebPhase(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	switch phase := model.CanonicalPhaseID(raw); phase {
	case model.PhaseDiscuss, model.PhasePlan, model.PhaseDev:
		return phase, nil
	}
	return "", NewInvalidInputError("phase must be one of discuss, plan, dev")
}

// WebWorkflowStartPrompt 是交给用户、由用户转发给智能体的「启动提示词」。
// 关键信息只有三条：带上这个 workflow_id、目录填自己当前的工作区（用户在「高级选项」里绑定了项目目录时，改成原样填那个目录，
// 否则 D-161 会把它挡在门外）、接入后先取用户留下的第一条指令（回执里带回来的，或收件箱里的）。projectDir 为空 = 没绑定。
func WebWorkflowStartPrompt(title, workflowID, projectDir string) string {
	dirRule := "project_directory 填你当前工作区的绝对路径"
	if projectDir != "" {
		// 不用 %q：Windows 路径里的反斜杠会被转义成两个，「原样填」就不原样了。
		dirRule = fmt.Sprintf("project_directory 必须原样填 \"%s\"（网页里已经为这个会话绑定了这个项目目录；你当前的工作区不是它时，先在对话里告诉我，不要改填别的路径）", projectDir)
	}
	return fmt.Sprintf(`请接入 RelayMesh 的工作流「%s」（workflow_id：%s）。

1. 先调用 RelayMesh 的 interactive_feedback 汇报一次（异步模式下用 wait:false）：参数里必须带 workflow_id："%s"，%s；不要另起别的 workflow_id，也不要省略它。
2. 我可能已经留了第一条指令：第 1 步的回执里带回来了就直接执行；没带回来就调用 workflow_context 的 inbox（workflow_id 同上）读取；仍然没有，就在对话里等我的下一条。
3. 之后所有的汇报、提问、确认都沿用这个 workflow_id，并按 RelayMesh 回执里的指引继续。`, title, workflowID, workflowID, dirRule)
}

// webWorkflowSummary 是占位会话里那条 AI 气泡（Markdown）：说明现状，并把启动提示词原样放进代码块，
// 这样用户关掉弹窗之后，随时能在这个会话的第一条消息里再找到它。绑定了项目目录时多一行说明（智能体必须在这个目录接入）。
func webWorkflowSummary(title, workflowID, projectDir, prompt string) string {
	dirLine := ""
	if projectDir != "" {
		dirLine = fmt.Sprintf("**项目目录**：`%s`（已绑定：智能体第一次汇报必须带这个目录，换目录会被拒绝）\n\n", projectDir)
	}
	return fmt.Sprintf("「%s」已在网页创建，正在等待智能体接入。\n\n"+
		"**工作流编号**：`%s`\n\n"+
		"%s"+
		"把下面这段**启动提示词**发给要使用的智能体（整段复制即可）；它第一次调用 RelayMesh 时带上这个工作流编号，就会接进这个会话：\n\n"+
		"```text\n%s\n```\n\n"+
		"智能体接入之前，您可以先在下方输入框写好第一条指令，它接入后会在收件箱里读到。",
		title, workflowID, dirLine, prompt)
}

// CreateWebWorkflowInput 是网页新建会话的入参。
type CreateWebWorkflowInput struct {
	// Title 必填，经 NormalizeWebWorkflowTitle 规整。
	Title string
	// WorkflowID 可选；为空自动生成 wf-web-<日期>-<6 位随机>。给定时必须符合 webWorkflowIDPattern 且库里还没有这个工作流。
	WorkflowID string
	// EnvHostName 是服务自己配置的主机名，仅作兜底（全局设置里的主机名优先）。
	EnvHostName string
	// ProjectDirectory 可选：把占位会话（以及这个工作流）绑定到某个项目目录，经 NormalizeWebProjectDirectory 规整；
	// 为空 = 不绑定（D-161 不比较，智能体带什么目录都行）。
	ProjectDirectory string
	// HostName 可选：用户指定的主机名（会话的归属标签），经 NormalizeWebHostName 规整；
	// 为空沿用「全局设置 > 服务环境变量 > hosts」。智能体接入后，凭据绑定的主机仍以凭据为准。
	HostName string
}

// CreateWebWorkflowResult 是网页新建会话的结果。
type CreateWebWorkflowResult struct {
	Session     *model.FeedbackSession
	StartPrompt string
}

// CreateWebWorkflow 新建一个工作流：落一行占位会话 + 写入自定义标题，返回占位会话与启动提示词。
// 工作流编号已存在（给定编号撞了）返回冲突错误；名字或编号不合规返回入参错误。
func (s *Store) CreateWebWorkflow(ctx context.Context, input CreateWebWorkflowInput) (*CreateWebWorkflowResult, error) {
	title, err := NormalizeWebWorkflowTitle(input.Title)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	workflowID := strings.TrimSpace(input.WorkflowID)
	if workflowID == "" {
		workflowID = fmt.Sprintf("wf-web-%s-%s", now.Format("20060102"), uuid.New().String()[:6])
	} else if !webWorkflowIDPattern.MatchString(workflowID) {
		return nil, NewInvalidInputError("workflow_id may only contain letters, digits, '.', '_' and '-' (3-64 characters, starting with a letter or digit)")
	}

	// 项目目录与主机的校验也在落库之前：不合规的请求不会留下半个会话。
	projectDir, err := NormalizeWebProjectDirectory(input.ProjectDirectory)
	if err != nil {
		return nil, err
	}
	userHost, err := NormalizeWebHostName(input.HostName)
	if err != nil {
		return nil, err
	}

	sessionID := WebPlaceholderSessionPrefix + uuid.New().String()[:8]
	prompt := WebWorkflowStartPrompt(title, workflowID, projectDir)

	var session model.FeedbackSession
	err = s.WithTx(ctx, func(tx *gorm.DB) error {
		var existing int64
		if err := tx.Model(&model.FeedbackSession{}).Where("workflow_id = ?", workflowID).Count(&existing).Error; err != nil {
			return err
		}
		if existing > 0 {
			return NewConflictError(fmt.Sprintf("workflow_id %q already exists", workflowID), 0)
		}

		// host_name：用户在「高级选项」里指定的最优先；否则与汇报一致（没有凭据可看）：全局设置 > 服务环境变量 > 默认。
		hostName := userHost
		if hostName == "" {
			if globalSettings, _ := s.GetGlobalAppSettingsWithDB(ctx, tx); globalSettings != nil {
				hostName = globalSettings.HostName
			}
		}
		if hostName == "" {
			hostName = input.EnvHostName
		}
		if hostName == "" {
			hostName = "hosts"
		}

		session = model.FeedbackSession{
			ID:               sessionID,
			WorkflowID:       workflowID,
			Kind:             model.FeedbackKindNotice,
			HostName:         hostName,
			ProjectDirectory: projectDir,
			Title:            title,
			Summary:          webWorkflowSummary(title, workflowID, projectDir, prompt),
			Status:           "completed",
			UserMessages:     model.StringArray{},
			Images:           model.SessionImages{},
			ConsumedByAI:     true,
			ConsumedAt:       &now,
			CreatedAt:        now,
			UpdatedAt:        now,
		}
		if err := tx.Create(&session).Error; err != nil {
			return err
		}
		// 用户起的名字固定下来：智能体以后汇报里带的 title 不会覆盖它（与重命名同一机制）。
		return tx.Save(&model.SystemSetting{Key: "wf_custom_title:" + workflowID, Value: title, UpdatedAt: now}).Error
	})
	if err != nil {
		return nil, err
	}
	return &CreateWebWorkflowResult{Session: &session, StartPrompt: prompt}, nil
}
