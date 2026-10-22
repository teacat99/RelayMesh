package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	Mode                     string
	DataDir                  string
	Host                     string
	Port                     int
	HostName                 string
	DBPath                   string
	ProjectID                string
	MCPToken                 string
	ConfigureToken           string
	ExecutionToken           string
	WebUsername              string
	WebPassword              string
	JWTSecret                string
	TrustedProxies           string // RELAYMESH_TRUSTED_PROXIES，语义与校验见 api.ResolveTrustedProxies
	CSPMode                  string // RELAYMESH_CSP：off / report / enforce（默认 enforce），语义见 api.ResolveCSPMode
	AllowQueryToken          bool   // RELAYMESH_ALLOW_QUERY_TOKEN：SSE 端点是否仍接受 URL 上的 ?token=（兼容旧前端，默认开启；生产可关闭）
	TerminalMode             string // RELAYMESH_TERMINAL：auto（默认，满足安全前置条件才启用）/ off（强制关闭，只能关不能开），语义见 terminal.ResolveMode
	TerminalExec             string // RELAYMESH_TERMINAL_EXEC：off（默认）/ on（允许模型经 MCP 在已授权的主机上执行命令；仍需终端总闸开启），语义见 terminal.ResolveExec
	TerminalFiles            string // RELAYMESH_TERMINAL_FILES：off（默认）/ on（允许模型经 MCP 查看、拉取、写入已授权主机上令牌允许目录内的文件；还需要 RELAYMESH_TERMINAL_EXEC=on），语义见 terminal.ResolveFiles
	TerminalDiscovery        string // RELAYMESH_TERMINAL_DISCOVERY：window（默认，管理员手动开窗）/ always（常开，不建议公网），语义见 terminal.ResolveDiscovery
	TerminalRecord           string // RELAYMESH_TERMINAL_RECORD：auto（默认）/ off（服务端全局关闭终端记录，下发给每台主机的 Agent），语义见 terminal.ResolveRecord
	TerminalRecordDays       int    // RELAYMESH_TERMINAL_RECORD_DAYS：记录保留天数上限（0 表示不额外限制，主机本机默认 30 天；只能比主机本机配置更严格）
	TerminalRecordSessionMB  int    // RELAYMESH_TERMINAL_RECORD_SESSION_MB：单会话记录大小上限 MiB（0 表示不额外限制）
	TerminalRecordHostMB     int    // RELAYMESH_TERMINAL_RECORD_HOST_MB：单台主机记录总量上限 MiB（0 表示不额外限制）
	SSHSecretKey             string // RELAYMESH_SSH_SECRET_KEY：SSH 主机凭据的加密主密钥（独立于 JWT 密钥；未设置或过弱时 SSH 后端整块关闭）
	AllowedOrigins           string // RELAYMESH_ALLOWED_ORIGINS：终端浏览器 WebSocket 额外允许的 Origin（逗号分隔，含协议与端口）
	AgentDistDir             string // RELAYMESH_AGENT_DIST_DIR：Agent 各平台二进制的分发目录（Web 生成的安装命令从服务端下载）；留空自动探测可执行文件旁 / 容器内 /app 下的 agent-dist
	WaitAfterMinutes         int
	MaxNoFeedbackChecks      int
	WaitInstruction          string
	WaitExhaustedInstruction string
	FeedbackTimeoutSeconds   int
	ASRAPIURL                string
	ASRAPIKey                string
	ASRModel                 string
	NtfyToken                string // RELAYMESH_NTFY_TOKEN：ntfy 访问令牌；设置后盖过界面里保存的令牌（令牌可以完全不进数据库）
	InboxOff                 bool   // RELAYMESH_INBOX=off：追加指令收件箱的总闸（只能关，不能靠它打开）：不追加回执提示行、不让 hook 提醒、指引里不提收件箱；已排队的追加照旧在智能体下次发起交互时被带走
	OAuthOff                 bool   // RELAYMESH_OAUTH=off：关闭全部 OAuth 端点与元数据，/mcp 行为与未实现 OAuth 时一致
	PublicURL                string // RELAYMESH_PUBLIC_URL：授权服务器与资源元数据的公开根地址（缺省按代理头与 Host 推导）
	HTTPSPort                int
	TLSEnabled               bool
	TLSCertPath              string
	TLSKeyPath               string
	DevMode                  bool
	AutoOpenBrowser          bool
	Version                  string

	// 以下三项只供实验与端到端测试（不写进部署模板）：把会话守护 / 自动续跑的检查间隔、自动续跑的退避与「续跑成功」的判定时长缩短到秒级，免得真等几分钟。生产不要设。
	TerminalGuardTickSec         int    // RELAYMESH_TERMINAL_GUARD_TICK_SEC：守护与自动续跑的检查间隔（秒，默认 60）
	TerminalAutoResumeBackoffSec string // RELAYMESH_TERMINAL_AUTORESUME_BACKOFF_SEC：自动续跑的退避（秒，逗号分隔，默认 300,1200,3600）
	TerminalAutoResumeOKSec      int    // RELAYMESH_TERMINAL_AUTORESUME_OK_SEC：自动续跑「成功」的判定时长（秒；默认按预设里的分钟数，10 分钟）
}

func Load() *Config {
	return LoadWithMode("serve")
}

// LoadWithMode loads configuration tailored for the given runtime mode ("serve" or "stdio").
func LoadWithMode(mode string) *Config {
	if mode == "" {
		mode = "serve"
	}
	dataDir := ResolveDataDir(mode)

	defaultDBPath := "data/relaymesh.db"
	defaultTLSCert := "data/certs/cert.pem"
	defaultTLSKey := "data/certs/key.pem"
	defaultAutoOpenBrowser := true
	defaultHost := "127.0.0.1"

	if mode == "stdio" {
		defaultDBPath = filepath.Join(dataDir, "relaymesh.db")
		defaultTLSCert = filepath.Join(dataDir, "certs", "cert.pem")
		defaultTLSKey = filepath.Join(dataDir, "certs", "key.pem")
		defaultAutoOpenBrowser = false
		_ = EnsureDataDir(dataDir)
	}

	host := getEnv("RELAYMESH_HOST", getEnv("HOST", defaultHost))
	if mode == "stdio" {
		// stdio 模式强制只监听 127.0.0.1 本地环回，严禁暴露至局域网
		host = "127.0.0.1"
	}

	cfg := &Config{
		Mode:                     mode,
		DataDir:                  dataDir,
		Host:                     host,
		Port:                     getEnvInt("RELAYMESH_PORT", getEnvInt("PORT", 18775)),
		HostName:                 getEnv("RELAYMESH_HOST_NAME", getEnv("HOST_NAME", "")),
		DBPath:                   getEnv("RELAYMESH_DB_PATH", defaultDBPath),
		ProjectID:                getEnv("RELAYMESH_PROJECT_ID", getEnv("TWH_LITE_PROJECT_ID", "default")),
		MCPToken:                 getEnv("RELAYMESH_MCP_TOKEN", getEnv("TWH_LITE_MCP_TOKEN", "")),
		ConfigureToken:           getEnv("RELAYMESH_CONFIGURE_TOKEN", getEnv("TWH_LITE_CONFIGURE_TOKEN", "")),
		ExecutionToken:           getEnv("RELAYMESH_EXECUTION_TOKEN", getEnv("TWH_LITE_EXECUTION_TOKEN", "")),
		WebUsername:              getEnv("RELAYMESH_WEB_USERNAME", "admin"),
		WebPassword:              getEnv("RELAYMESH_WEB_PASSWORD", ""),
		JWTSecret:                getEnv("RELAYMESH_JWT_SECRET", DefaultJWTSecret),
		TrustedProxies:           getEnv("RELAYMESH_TRUSTED_PROXIES", ""),
		CSPMode:                  getEnv("RELAYMESH_CSP", ""),
		AllowQueryToken:          getEnvBool("RELAYMESH_ALLOW_QUERY_TOKEN", true),
		TerminalMode:             getEnv("RELAYMESH_TERMINAL", ""),
		TerminalExec:             getEnv("RELAYMESH_TERMINAL_EXEC", ""),
		TerminalFiles:            getEnv("RELAYMESH_TERMINAL_FILES", ""),
		TerminalDiscovery:        getEnv("RELAYMESH_TERMINAL_DISCOVERY", ""),
		TerminalRecord:           getEnv("RELAYMESH_TERMINAL_RECORD", ""),
		TerminalRecordDays:       getEnvInt("RELAYMESH_TERMINAL_RECORD_DAYS", 0),
		TerminalRecordSessionMB:  getEnvInt("RELAYMESH_TERMINAL_RECORD_SESSION_MB", 0),
		TerminalRecordHostMB:     getEnvInt("RELAYMESH_TERMINAL_RECORD_HOST_MB", 0),
		SSHSecretKey:             getEnv("RELAYMESH_SSH_SECRET_KEY", ""),
		AllowedOrigins:           getEnv("RELAYMESH_ALLOWED_ORIGINS", ""),
		AgentDistDir:             getEnv("RELAYMESH_AGENT_DIST_DIR", ""),
		WaitAfterMinutes:         getEnvInt("RELAYMESH_WAIT_AFTER_MINUTES", getEnvInt("TWH_LITE_WAIT_AFTER_MINUTES", 5)),
		MaxNoFeedbackChecks:      getEnvInt("RELAYMESH_MAX_NO_FEEDBACK_CHECKS", getEnvInt("TWH_LITE_MAX_NO_FEEDBACK_CHECKS", 24)),
		WaitInstruction:          getEnv("RELAYMESH_WAIT_INSTRUCTION", getEnv("TWH_LITE_WAIT_INSTRUCTION", "请等待 {minutes} 分钟后再次调用 MCP 获取反馈。")),
		WaitExhaustedInstruction: getEnv("RELAYMESH_WAIT_EXHAUSTED_INSTRUCTION", getEnv("TWH_LITE_WAIT_EXHAUSTED_INSTRUCTION", "本次等待窗口已结束；暂无新反馈。如需继续推进，可提交新的 progress 或 question。")),
		FeedbackTimeoutSeconds:   getEnvInt("RELAYMESH_FEEDBACK_TIMEOUT_SECONDS", 120),
		ASRAPIURL:                getEnv("RELAYMESH_ASR_API_URL", getEnv("MIMO_API_URL", "https://api.xiaomimimo.com/v1/chat/completions")),
		ASRAPIKey:                getEnv("RELAYMESH_ASR_API_KEY", getEnv("MIMO_API_KEY", "")),
		ASRModel:                 getEnv("RELAYMESH_ASR_MODEL", getEnv("MIMO_MODEL", "mimo-v2.5-asr")),
		NtfyToken:                getEnv("RELAYMESH_NTFY_TOKEN", ""),
		InboxOff:                 isOffValue(getEnv("RELAYMESH_INBOX", "")),
		OAuthOff:                 isOffValue(getEnv("RELAYMESH_OAUTH", "")),
		PublicURL:                getEnv("RELAYMESH_PUBLIC_URL", ""),
		HTTPSPort:                getEnvInt("RELAYMESH_HTTPS_PORT", getEnvInt("HTTPS_PORT", 18776)),
		TLSEnabled:               getEnvBool("RELAYMESH_TLS_ENABLED", true),
		TLSCertPath:              getEnv("RELAYMESH_TLS_CERT", defaultTLSCert),
		TLSKeyPath:               getEnv("RELAYMESH_TLS_KEY", defaultTLSKey),
		DevMode:                  getEnvBool("RELAYMESH_DEV", false),
		AutoOpenBrowser:          getEnvBool("RELAYMESH_OPEN_BROWSER", defaultAutoOpenBrowser),

		TerminalGuardTickSec:         getEnvInt("RELAYMESH_TERMINAL_GUARD_TICK_SEC", 0),
		TerminalAutoResumeBackoffSec: getEnv("RELAYMESH_TERMINAL_AUTORESUME_BACKOFF_SEC", ""),
		TerminalAutoResumeOKSec:      getEnvInt("RELAYMESH_TERMINAL_AUTORESUME_OK_SEC", 0),
	}
	return cfg
}

func getEnv(key, fallback string) string {
	if val := os.Getenv(key); strings.TrimSpace(val) != "" {
		return strings.TrimSpace(val)
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if val := os.Getenv(key); strings.TrimSpace(val) != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(val)); err == nil {
			return n
		}
	}
	return fallback
}

func getEnvBool(key string, fallback bool) bool {
	if val := os.Getenv(key); strings.TrimSpace(val) != "" {
		v := strings.ToLower(strings.TrimSpace(val))
		return v == "1" || v == "true" || v == "yes" || v == "on"
	}
	return fallback
}

// isOffValue 判断一个开关型环境变量是不是明确写了「关」（off / 0 / false / no / disabled，不区分大小写）。
// 留空或其他取值都不算关，保持默认行为。
func isOffValue(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "off", "0", "false", "no", "disabled":
		return true
	}
	return false
}
