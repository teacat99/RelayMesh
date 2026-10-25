package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"gorm.io/gorm"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 终端启动器（会话页顶栏的「终端」入口）的预设：在哪台主机、哪个目录、自动输入什么命令。
// 预设保存在 Hub（system_settings），所有浏览器共享；单独一个键，不与终端偏好（TerminalPrefs）混在一起，
// 这样两边各自读—改—写，互不影响。

// TerminalLaunchersKey 是启动器预设在 system_settings 表里的键，值是 JSON（见 terminalLauncherDoc）。
const TerminalLaunchersKey = "terminal_launchers"

// 启动器预设的取值范围（按字符数，不是字节数；中文名字和中文目录同样适用）。
const (
	MaxTerminalLaunchers    = 20
	MaxLauncherNameRunes    = 40
	MaxLauncherDirRunes     = 512
	MaxLauncherCommandRunes = 1000
)

// 会话守护（LauncherGuard）的取值范围。静默分钟数的下限防止把「正常思考」当停摆，上限 12 小时。
const (
	DefaultGuardSilenceMinutes = 30
	MinGuardSilenceMinutes     = 5
	MaxGuardSilenceMinutes     = 720
	MaxGuardWorkflowRunes      = 128
	// MaxGuardPromptRunes 与会话 / 全局模板同一个上限（sessionprompt.TemplateMaxRunes，测试里核对）：
	// v2 内置默认比旧版长，预设里「以默认为底稿」填进去必须还能存（#232 D3，原先是 300）。
	MaxGuardPromptRunes = 1500
)

var (
	// ErrInvalidLauncher 表示预设的某个字段不合法；错误信息可以直接展示给用户。
	ErrInvalidLauncher = errors.New("终端启动预设不合法")
	// ErrLauncherLimit 表示预设数量已到上限，不能再新增（修改已有的不受影响）。
	ErrLauncherLimit = errors.New("终端启动预设数量已到上限")
	// ErrLauncherNotFound 表示要删除的预设不存在（可能已在别的浏览器里删掉了）。
	ErrLauncherNotFound = errors.New("终端启动预设不存在")
)

var launcherIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)

// 目录必须写完整：POSIX 绝对路径、`~` 开头的家目录路径，或 Windows 的盘符 / UNC 路径。
// 相对路径在主机上没有「当前目录」可言——Agent 找不到目录时会静默回落到默认目录，而启动器要在这个目录里运行命令，
// 写成相对路径只会得到一个没有人注意到的错误位置。
var launcherDirRe = regexp.MustCompile(`^(/|~|[A-Za-z]:[\\/]|\\\\)`)

// TerminalLauncher 是一条启动预设。
type TerminalLauncher struct {
	// ID 由界面生成，保存后不变；增删改都按它定位，所以两个浏览器同时改不同的预设不会互相覆盖。
	ID string `json:"id"`
	// Name 同时是新建会话的名字：启动器靠「主机 + 名字」认出这个预设已经在运行的会话（见前端 lib/terminal/launcher.ts），
	// 所以预设之间不能重名。
	Name string `json:"name"`
	// Host 是目标主机的机器码。
	Host string `json:"host"`
	// Dir 是会话启动时所在的目录；留空表示主机的默认目录。
	Dir string `json:"dir,omitempty"`
	// Command 是会话就绪后自动输入并回车的命令；留空表示只打开一个终端。它是一整行，不含换行与控制字符。
	Command string `json:"command,omitempty"`
	// Guard 是可选的「会话守护」：监测这个预设启动的智能体会话有没有意外退出或停摆，并给出一键唤醒。
	// nil 表示没配置过（旧存档都是这种）。旧版本读到带 guard 的存档会忽略它——回滚后守护配置会丢，预设本身不受影响。
	Guard *LauncherGuard `json:"guard,omitempty"`
	// Kind 是预设类型：空串（默认，旧存档都是这种）= 开终端；LauncherKindHeadless = 非交互续聊（见 HeadlessRun）。
	// 旧版本读到非交互续聊预设，会把它当成一个没有命令的终端预设——回滚后这种预设的运行配置会丢，其余预设不受影响。
	Kind string `json:"kind,omitempty"`
	// Run 是非交互续聊的运行配置，只在 Kind == LauncherKindHeadless 时有。
	Run *HeadlessRun `json:"run,omitempty"`
}

type terminalLauncherDoc struct {
	Launchers []TerminalLauncher `json:"launchers"`
}

func hasControlRune(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// Normalize 规整各字段：名字是单行文本，各种空白（含换行、制表）折叠成一个普通空格；其余字段只去掉首尾空白。
// 命令里的空白（含中间的空格）一律保持原样。
func (l TerminalLauncher) Normalize() TerminalLauncher {
	l.ID = strings.TrimSpace(l.ID)
	l.Name = strings.Join(strings.Fields(l.Name), " ")
	l.Host = strings.TrimSpace(l.Host)
	l.Dir = strings.TrimSpace(l.Dir)
	l.Command = strings.TrimSpace(l.Command)
	l.Kind = strings.ToLower(strings.TrimSpace(l.Kind))
	if l.Kind == LauncherKindTerminal {
		l.Kind = "" // 默认类型存成空串：新旧存档、新旧版本的读写保持一致
	}
	if l.Guard != nil {
		// 换成新指针而不是原地改：值接收者拿到的是调用方同一份守护配置，不能顺手改了它。
		if g := l.Guard.normalize(); g.isZero() {
			l.Guard = nil
		} else {
			l.Guard = &g
		}
	}
	if l.Run != nil {
		r := l.Run.normalize() // 同上：不改调用方的数据
		l.Run = &r
	}
	return l
}

// Validate 检查一条（已规整的）预设；错误信息面向用户，并带上出问题的字段。
func (l TerminalLauncher) Validate() error {
	if !launcherIDRe.MatchString(l.ID) {
		return fmt.Errorf("%w：预设编号格式不正确", ErrInvalidLauncher)
	}
	switch n := utf8.RuneCountInString(l.Name); {
	case n == 0:
		return fmt.Errorf("%w：请给预设起个名字", ErrInvalidLauncher)
	case n > MaxLauncherNameRunes:
		return fmt.Errorf("%w：名字最长 %d 个字，现在是 %d 个字", ErrInvalidLauncher, MaxLauncherNameRunes, n)
	case hasControlRune(l.Name):
		return fmt.Errorf("%w：名字里不能有控制字符", ErrInvalidLauncher)
	case termproto.SanitizeText(l.Name, 0) != l.Name:
		// 名字同时是会话名，而终端子系统会丢掉零宽字符、方向控制符这类不可显示的字符；
		// 保存时就拒绝，才能保证「预设的名字 = 会话的名字」，启动器据此认出它已经在运行的会话。
		return fmt.Errorf("%w：名字里有不能显示的特殊字符（零宽字符、方向控制符等）", ErrInvalidLauncher)
	}
	if !termproto.ValidMachineCode(l.Host) {
		return fmt.Errorf("%w：请选择一台主机", ErrInvalidLauncher)
	}
	switch l.Kind {
	case "", LauncherKindHeadless:
	default:
		return fmt.Errorf("%w：不认识的预设类型", ErrInvalidLauncher)
	}
	if l.Kind == "" && l.Run != nil {
		return fmt.Errorf("%w：只有「非交互续聊」预设才有运行配置", ErrInvalidLauncher)
	}
	if l.Dir != "" {
		switch n := utf8.RuneCountInString(l.Dir); {
		case n > MaxLauncherDirRunes:
			return fmt.Errorf("%w：目录最长 %d 个字，现在是 %d 个字", ErrInvalidLauncher, MaxLauncherDirRunes, n)
		case hasControlRune(l.Dir):
			return fmt.Errorf("%w：目录里不能有换行或控制字符", ErrInvalidLauncher)
		case !launcherDirRe.MatchString(l.Dir):
			return fmt.Errorf("%w：目录请写完整路径（以 / 或 ~ 开头；Windows 写盘符，例如 C:\\work）", ErrInvalidLauncher)
		}
	}
	if l.Command != "" {
		switch n := utf8.RuneCountInString(l.Command); {
		case n > MaxLauncherCommandRunes:
			return fmt.Errorf("%w：命令最长 %d 个字，现在是 %d 个字", ErrInvalidLauncher, MaxLauncherCommandRunes, n)
		case hasControlRune(l.Command):
			return fmt.Errorf("%w：命令只能是一行，不能有换行、制表符或其他控制字符", ErrInvalidLauncher)
		}
	}
	if l.Guard != nil {
		if err := l.Guard.validate(); err != nil {
			return err
		}
		if l.Guard.ExitEndsSession && strings.TrimSpace(l.Command) != "" {
			if err := validateLauncherExecCommand(l.Command); err != nil {
				return err
			}
		}
	}
	if l.IsHeadless() {
		return l.validateHeadless()
	}
	return nil
}

func sameLauncherName(a, b string) bool { return strings.EqualFold(a, b) }

// decodeTerminalLaunchers 容忍损坏的存档：解析失败按空处理；不合法、重复（编号或名字）的条目逐条丢掉，超出数量上限的截断。
// 宁可少几条预设，也不要把一条坏数据交给界面去执行。
func decodeTerminalLaunchers(raw string) []TerminalLauncher {
	var doc terminalLauncherDoc
	if json.Unmarshal([]byte(raw), &doc) != nil {
		return []TerminalLauncher{}
	}
	out := make([]TerminalLauncher, 0, len(doc.Launchers))
	ids := map[string]bool{}
	for _, l := range doc.Launchers {
		l = l.Normalize()
		// 守护配置是可选附加物：它坏了只丢掉它，不连累预设本身（预设是日常开终端用的，不能因为附加配置被改坏就不见了）。
		if l.Guard != nil && l.Guard.validate() != nil {
			l.Guard = nil
		}
		if l.Validate() != nil || ids[l.ID] {
			continue
		}
		dup := false
		for _, o := range out {
			if sameLauncherName(o.Name, l.Name) {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		ids[l.ID] = true
		out = append(out, l)
		if len(out) >= MaxTerminalLaunchers {
			break
		}
	}
	return out
}

// ListTerminalLaunchers 读取全部预设（按保存顺序）；从未保存过时返回空列表（不是 nil）。
func (s *Store) ListTerminalLaunchers(ctx context.Context) ([]TerminalLauncher, error) {
	var rec model.SystemSetting
	err := s.db.WithContext(ctx).Where("key = ?", TerminalLaunchersKey).First(&rec).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return []TerminalLauncher{}, nil
		}
		return nil, err
	}
	return decodeTerminalLaunchers(rec.Value), nil
}

// errLauncherUnchanged 是 mutate 的「什么都不用改」信号：updateTerminalLaunchers 不写库、也不报错。
var errLauncherUnchanged = errors.New("预设没有变化")

// updateTerminalLaunchers 在一个事务里「读—改—写」整份预设；mutate 返回错误时不写入
// （返回 errLauncherUnchanged 表示没有要改的，同样不写入但不算错误）。
func (s *Store) updateTerminalLaunchers(ctx context.Context, mutate func([]TerminalLauncher) ([]TerminalLauncher, error)) ([]TerminalLauncher, error) {
	var out []TerminalLauncher
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		var rec model.SystemSetting
		cur := []TerminalLauncher{}
		if err := tx.Where("key = ?", TerminalLaunchersKey).First(&rec).Error; err == nil {
			cur = decodeTerminalLaunchers(rec.Value)
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		next, err := mutate(cur)
		if errors.Is(err, errLauncherUnchanged) {
			out = cur // 条件不满足：不写库
			return nil
		}
		if err != nil {
			return err
		}
		raw, err := json.Marshal(terminalLauncherDoc{Launchers: next})
		if err != nil {
			return err
		}
		if err := tx.Save(&model.SystemSetting{Key: TerminalLaunchersKey, Value: string(raw), UpdatedAt: time.Now()}).Error; err != nil {
			return err
		}
		out = next
		return nil
	})
	return out, err
}

// UpsertTerminalLauncher 新增一条预设，或按编号覆盖已有的那一条；返回保存后的全部预设，以及这次是不是新增。
// 名字不能与别的预设重名；新增时数量不能超过上限（覆盖已有的不受限）。
func (s *Store) UpsertTerminalLauncher(ctx context.Context, l TerminalLauncher) ([]TerminalLauncher, bool, error) {
	l = l.Normalize()
	if err := l.Validate(); err != nil {
		return nil, false, err
	}
	created := false
	list, err := s.updateTerminalLaunchers(ctx, func(cur []TerminalLauncher) ([]TerminalLauncher, error) {
		idx := -1
		for i, o := range cur {
			if o.ID == l.ID {
				idx = i
				continue
			}
			if sameLauncherName(o.Name, l.Name) {
				return nil, fmt.Errorf("%w：已经有一个叫「%s」的预设，换个名字", ErrInvalidLauncher, o.Name)
			}
		}
		next := append([]TerminalLauncher(nil), cur...)
		if idx >= 0 {
			next[idx] = l
			return next, nil
		}
		if len(cur) >= MaxTerminalLaunchers {
			return nil, fmt.Errorf("%w：最多 %d 个，先删掉不用的再新建", ErrLauncherLimit, MaxTerminalLaunchers)
		}
		created = true
		return append(next, l), nil
	})
	if err != nil {
		return nil, false, err
	}
	return list, created, nil
}

// DeleteTerminalLauncher 按编号删除一条预设；返回删除后的全部预设，以及被删掉的那一条（用于审计）。
func (s *Store) DeleteTerminalLauncher(ctx context.Context, id string) ([]TerminalLauncher, TerminalLauncher, error) {
	var gone TerminalLauncher
	list, err := s.updateTerminalLaunchers(ctx, func(cur []TerminalLauncher) ([]TerminalLauncher, error) {
		next := make([]TerminalLauncher, 0, len(cur))
		found := false
		for _, o := range cur {
			if o.ID == id {
				found, gone = true, o
				continue
			}
			next = append(next, o)
		}
		if !found {
			return nil, ErrLauncherNotFound
		}
		return next, nil
	})
	if err != nil {
		return nil, TerminalLauncher{}, err
	}
	return list, gone, nil
}
