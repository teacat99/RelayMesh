// Package ntfy 把 RelayMesh 里「需要你处理」的事件推送到 ntfy（https://ntfy.sh 或自建实例）。
//
// 浏览器里的声音与系统通知只在网页开着、事件流连着的时候才会响；智能体在等你回复，你却不在电脑前，
// 提醒就送不到。ntfy 是一个简单的推送服务：Hub 往一个「主题」发一条消息，手机或桌面上订阅了这个主题的
// ntfy 应用立刻就会弹出通知——不依赖任何网页是否打开。
//
// 本包是叶子包（只依赖标准库与 model），职责分三层：
//   - config.go   配置的形状、校验、对外视图（凭据只写不读）；
//   - sender.go   一次推送的 HTTP 细节：JSON 发布、认证头、重试、错误脱敏、目标地址防护；
//   - notifier.go 事件队列：去重、延迟、限流、状态统计，让「推送失败 / 变慢」永远拖不住智能体的请求。
package ntfy

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

const (
	// DefaultServer 是公共的 ntfy 服务；自建实例填自己的地址。
	DefaultServer = "https://ntfy.sh"
	// DefaultPriority 对应 ntfy 的「high」：手机上会弹出并伴随较长的震动。1 最低、3 默认、5 紧急。
	DefaultPriority = 4
	// MaxDelaySeconds 是「延迟推送」的上限（15 分钟）。
	MaxDelaySeconds = 900
	// DefaultRunMinSeconds 是「模型运行结束」推送的默认门槛：运行得比这更短的命令不推送。
	DefaultRunMinSeconds = 60
	// MaxRunMinSeconds 是这个门槛的上限（24 小时）。
	MaxRunMinSeconds = 86400

	maxServerLen   = 200
	maxClickLen    = 500
	maxSecretLen   = 256
	maxUsernameLen = 128
)

// ErrInvalidConfig 表示配置取值不合法；错误信息面向用户，可以直接展示。
var ErrInvalidConfig = errors.New("ntfy 配置不合法")

// ntfy 的主题名只允许字母、数字、下划线和连字符，最长 64 个字符。
var topicPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Config 是持久化的完整配置（含凭据）；只存在于服务端，绝不整体返回给浏览器（见 Public）。
type Config struct {
	// Enabled 是总开关；关闭时事件被静默丢弃（配置仍保留）。
	Enabled bool `json:"enabled"`
	// Server 是 ntfy 服务地址，不带末尾斜杠；允许带路径前缀（反向代理场景）。
	Server string `json:"server"`
	// Topic 是主题名。公共的 ntfy.sh 上，知道主题名的人都能订阅，所以它相当于密码——请用足够长的随机名字。
	Topic string `json:"topic"`
	// Token 是 ntfy 访问令牌（Bearer）；与 Username / Password 二选一，两者都有时令牌优先。
	Token    string `json:"token,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	// ClickURL 是点击通知后打开的地址，一般填 RelayMesh 控制台的外部访问地址；留空则点击没有动作。
	ClickURL string `json:"click_url,omitempty"`
	// Priority 是「默认优先级」1~5：等你回复、守护告警、命令出状况、测试推送、以及表里没单独设的类型都用它。
	Priority int `json:"priority"`
	// PriorityByKind 是「各类消息的优先级」覆盖表（见 priority.go）：消息类型 → 1~5，或 0 表示这一类不推送；
	// 没有出现的类型走内置默认（逐项复刻升级前的行为，异常类默认不推送）。nil 与空表等价。
	PriorityByKind map[string]int `json:"priority_by_kind,omitempty"`
	// IncludeSummary 为真时，通知里附上智能体摘要的开头一小段；默认只发标题，避免内容经过第三方服务。
	IncludeSummary bool `json:"include_summary"`
	// DelaySeconds 大于 0 时延迟推送：这段时间里你在网页上处理掉了请求就不再推送（0 = 立即）。
	DelaySeconds int `json:"delay_seconds"`
	// RunNotify 为真时，模型发起的命令（ExecMesh）运行结束也推送一条：成功、失败、超时、结果丢失，
	// 以及被主机侧中断（例如守护进程重启）；你自己或模型主动取消的不推。默认关闭，需要明确打开。
	RunNotify bool `json:"run_notify"`
	// RunMinSeconds 是运行结束推送的门槛：运行得比这更短的命令不推送，免得每条小命令都响铃。1~86400；0 视为默认值。
	RunMinSeconds int `json:"run_min_seconds"`
}

// Normalize 去掉首尾空白并补默认值，不改变含义；校验之前先调用它。
func (c Config) Normalize() Config {
	c.Server = strings.TrimRight(strings.TrimSpace(c.Server), "/")
	if c.Server == "" {
		c.Server = DefaultServer
	}
	c.Topic = strings.TrimSpace(c.Topic)
	c.Token = strings.TrimSpace(c.Token)
	c.Username = strings.TrimSpace(c.Username)
	// 密码里的空格可能是有意的，只去掉粘贴时容易带进来的换行。
	c.Password = strings.Trim(c.Password, "\r\n")
	c.ClickURL = strings.TrimSpace(c.ClickURL)
	if c.Priority == 0 {
		c.Priority = DefaultPriority
	}
	// 升级前保存的配置没有这个字段（读出来是 0）；0 一律当作默认门槛，而不是「所有命令都推送」。
	if c.RunMinSeconds == 0 {
		c.RunMinSeconds = DefaultRunMinSeconds
	}
	// 覆盖表里不认识的类型（手写进库的错键、以后版本删掉的类型）直接丢掉；值是否合法留给 Validate 报给用户。
	c.PriorityByKind = normalizePriorityByKind(c.PriorityByKind)
	return c
}

// Validate 检查取值；cfg 应已 Normalize。requireTopic 为真时主题必填（启用或要发送测试时）。
func (c Config) Validate(requireTopic bool) error {
	if err := validateHTTPURL("服务地址", c.Server, maxServerLen, false); err != nil {
		return err
	}
	if c.Topic == "" {
		if requireTopic {
			return invalid("主题不能为空")
		}
	} else if !topicPattern.MatchString(c.Topic) {
		return invalid("主题只能由字母、数字、下划线和连字符组成，且不超过 64 个字符")
	}
	if c.Token != "" {
		if len(c.Token) > maxSecretLen || strings.IndexFunc(c.Token, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
			return invalid("访问令牌格式不对：不能包含空白字符，且不超过 256 个字符")
		}
	}
	if len(c.Username) > maxUsernameLen || strings.ContainsAny(c.Username, ":") || hasControl(c.Username) {
		return invalid("用户名不能包含冒号或控制字符，且不超过 128 个字符")
	}
	if len(c.Password) > maxSecretLen || hasControl(c.Password) {
		return invalid("密码不能包含控制字符，且不超过 256 个字符")
	}
	if (c.Username == "") != (c.Password == "") {
		return invalid("用户名和密码需要同时填写")
	}
	if c.ClickURL != "" {
		if err := validateHTTPURL("点击打开的地址", c.ClickURL, maxClickLen, true); err != nil {
			return err
		}
	}
	if c.Priority < 1 || c.Priority > 5 {
		return invalid("优先级应在 1~5 之间")
	}
	// 按表的顺序检查，报错的先后因此是确定的（不依赖 map 的遍历顺序）。
	for _, k := range kindTable {
		if v, set := c.PriorityByKind[k.ID]; set && (v < 0 || v > 5) {
			return invalid(fmt.Sprintf("「%s」的优先级应在 1~5 之间，或者设成 0（不推送）", k.Label))
		}
	}
	if c.DelaySeconds < 0 || c.DelaySeconds > MaxDelaySeconds {
		return invalid(fmt.Sprintf("延迟推送应在 0~%d 秒之间", MaxDelaySeconds))
	}
	// 没打开运行结束推送时也要校验：这个值会随整张表单存进库里，存进去的不能是坏值。
	if c.RunMinSeconds < 1 || c.RunMinSeconds > MaxRunMinSeconds {
		return invalid(fmt.Sprintf("运行结束推送的最短运行时长应在 1~%d 秒之间", MaxRunMinSeconds))
	}
	return nil
}

// authHeader 返回 Authorization 头的值；没有配置认证时为空。令牌优先于用户名密码。
func (c Config) authHeader() string {
	if c.Token != "" {
		return "Bearer " + c.Token
	}
	if c.Username != "" && c.Password != "" {
		return "Basic " + basicAuth(c.Username, c.Password)
	}
	return ""
}

// secrets 返回需要从任何对外文本里抹掉的凭据。
func (c Config) secrets() []string {
	out := make([]string, 0, 3)
	for _, s := range []string{c.Token, c.Password, c.authHeader()} {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// WithEnvToken 在环境变量提供了令牌时，让它盖过数据库里保存的令牌（令牌可以完全不进数据库）。
func (c Config) WithEnvToken(envToken string) Config {
	if t := strings.TrimSpace(envToken); t != "" {
		c.Token = t
	}
	return c
}

// PublicConfig 是返回给浏览器的视图：凭据一律只给「是否已设置」，不给值。
type PublicConfig struct {
	Enabled        bool   `json:"enabled"`
	Server         string `json:"server"`
	Topic          string `json:"topic"`
	Username       string `json:"username"`
	ClickURL       string `json:"click_url"`
	Priority       int    `json:"priority"`
	IncludeSummary bool   `json:"include_summary"`
	DelaySeconds   int    `json:"delay_seconds"`
	RunNotify      bool   `json:"run_notify"`
	RunMinSeconds  int    `json:"run_min_seconds"`
	// PriorityByKind 永远是对象（没有覆盖时是 {}）：界面据此判断哪些行用了默认。
	PriorityByKind map[string]int `json:"priority_by_kind"`
	TokenSet       bool           `json:"token_set"`
	PasswordSet    bool           `json:"password_set"`
	// TokenFromEnv 为真时令牌来自环境变量 RELAYMESH_NTFY_TOKEN，界面里改不了也清不掉。
	TokenFromEnv bool `json:"token_from_env"`
}

// Public 生成对外视图；envToken 是环境变量里的令牌（有值时 token_set 与 token_from_env 都为真）。
func (c Config) Public(envToken string) PublicConfig {
	c = c.Normalize()
	fromEnv := strings.TrimSpace(envToken) != ""
	return PublicConfig{
		Enabled:        c.Enabled,
		Server:         c.Server,
		Topic:          c.Topic,
		Username:       c.Username,
		ClickURL:       c.ClickURL,
		Priority:       c.Priority,
		IncludeSummary: c.IncludeSummary,
		DelaySeconds:   c.DelaySeconds,
		RunNotify:      c.RunNotify,
		RunMinSeconds:  c.RunMinSeconds,
		PriorityByKind: copyPriorityByKind(c.PriorityByKind),
		TokenSet:       c.Token != "" || fromEnv,
		PasswordSet:    c.Password != "",
		TokenFromEnv:   fromEnv,
	}
}

// Update 是界面保存时提交的整张表单：非凭据字段整体替换；凭据「只写不读」——
// 留空表示保持原值，要换就填新值，要清掉就带上对应的 clear_ 标记。
type Update struct {
	Enabled        bool   `json:"enabled"`
	Server         string `json:"server"`
	Topic          string `json:"topic"`
	Username       string `json:"username"`
	ClickURL       string `json:"click_url"`
	Priority       int    `json:"priority"`
	IncludeSummary bool   `json:"include_summary"`
	DelaySeconds   int    `json:"delay_seconds"`
	RunNotify      bool   `json:"run_notify"`
	RunMinSeconds  int    `json:"run_min_seconds"`
	// PriorityByKind 整张替换「各类消息的优先级」覆盖表；没带这个字段（nil）表示保持原样——
	// 页面是旧版本缓存、或者别的客户端只改了其他项时，不会把已设好的表悄悄清掉。要清空就发 {}。
	PriorityByKind map[string]int `json:"priority_by_kind"`

	Token         string `json:"token"`
	ClearToken    bool   `json:"clear_token"`
	Password      string `json:"password"`
	ClearPassword bool   `json:"clear_password"`
}

// ApplyTo 把表单叠加到当前已保存的配置上，返回新配置（已 Normalize，尚未校验）。
func (u Update) ApplyTo(cur Config) Config {
	next := Config{
		Enabled:        u.Enabled,
		Server:         u.Server,
		Topic:          u.Topic,
		Username:       u.Username,
		ClickURL:       u.ClickURL,
		Priority:       u.Priority,
		IncludeSummary: u.IncludeSummary,
		DelaySeconds:   u.DelaySeconds,
		RunNotify:      u.RunNotify,
		RunMinSeconds:  u.RunMinSeconds,
		PriorityByKind: cur.PriorityByKind,
		Token:          cur.Token,
		Password:       cur.Password,
	}
	if u.PriorityByKind != nil {
		next.PriorityByKind = u.PriorityByKind
	}
	switch {
	case u.ClearToken:
		next.Token = ""
	case strings.TrimSpace(u.Token) != "":
		next.Token = u.Token
	}
	switch {
	case u.ClearPassword:
		next.Password = ""
	case strings.Trim(u.Password, "\r\n") != "":
		next.Password = u.Password
	}
	// 用户名被清空时，遗留的密码没有意义，一并清掉，免得「用户名和密码需要同时填写」这种看不见原因的报错。
	if strings.TrimSpace(next.Username) == "" {
		next.Password = ""
	}
	return next.Normalize()
}

func invalid(msg string) error { return fmt.Errorf("%w：%s", ErrInvalidConfig, msg) }

func hasControl(s string) bool {
	return strings.IndexFunc(s, unicode.IsControl) >= 0
}

// validateHTTPURL 要求是 http / https 网址：不带用户信息（凭据只走认证项）、不含控制字符；
// allowQuery 为假时还不许带查询串和锚点（服务地址是拼接基准，带了会出歧义）。
func validateHTTPURL(label, raw string, maxLen int, allowQuery bool) error {
	if raw == "" {
		return invalid(label + "不能为空")
	}
	if len(raw) > maxLen {
		return invalid(fmt.Sprintf("%s太长（最多 %d 个字符）", label, maxLen))
	}
	if hasControl(raw) || strings.ContainsAny(raw, " \t") {
		return invalid(label + "里不能有空白或控制字符")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return invalid(label + "不是有效的网址")
	}
	// 先看协议再看主机：漏写协议（ntfy.sh、localhost:8080）是最常见的错，值得给出最直接的提示。
	if u.Scheme != "http" && u.Scheme != "https" {
		return invalid(label + "必须以 http:// 或 https:// 开头")
	}
	if u.Host == "" || u.Hostname() == "" {
		return invalid(label + "不是有效的网址")
	}
	if u.User != nil {
		return invalid(label + "里不要直接带用户名密码，请使用下面的认证项")
	}
	if !allowQuery && (u.RawQuery != "" || u.Fragment != "") {
		return invalid(label + "不要带参数（?…）或锚点（#…）")
	}
	return nil
}
