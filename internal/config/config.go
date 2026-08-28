package config

import (
	"os"
	"strconv"
	"strings"
)

type Config struct {
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
	AllowNonLoopback         bool
	WaitAfterMinutes         int
	MaxNoFeedbackChecks      int
	WaitInstruction          string
	WaitExhaustedInstruction string
	FeedbackTimeoutSeconds   int
	WaitCountdownMinutes     int
	ASRAPIURL                string
	ASRAPIKey                string
	ASRModel                 string
	HTTPSPort                int
	TLSEnabled               bool
	TLSCertPath              string
	TLSKeyPath               string
	DevMode                  bool
	AutoOpenBrowser          bool
}

func Load() *Config {
	cfg := &Config{
		Host:                     getEnv("RELAYMESH_HOST", getEnv("HOST", "127.0.0.1")),
		Port:                     getEnvInt("RELAYMESH_PORT", getEnvInt("PORT", 18775)),
		HostName:                 getEnv("RELAYMESH_HOST_NAME", getEnv("HOST_NAME", "")),
		DBPath:                   getEnv("RELAYMESH_DB_PATH", "data/relaymesh.db"),
		ProjectID:                getEnv("RELAYMESH_PROJECT_ID", getEnv("TWH_LITE_PROJECT_ID", "default")),
		MCPToken:                 getEnv("RELAYMESH_MCP_TOKEN", getEnv("TWH_LITE_MCP_TOKEN", "")),
		ConfigureToken:           getEnv("RELAYMESH_CONFIGURE_TOKEN", getEnv("TWH_LITE_CONFIGURE_TOKEN", "")),
		ExecutionToken:           getEnv("RELAYMESH_EXECUTION_TOKEN", getEnv("TWH_LITE_EXECUTION_TOKEN", "")),
		WebUsername:              getEnv("RELAYMESH_WEB_USERNAME", "admin"),
		WebPassword:              getEnv("RELAYMESH_WEB_PASSWORD", ""),
		JWTSecret:                getEnv("RELAYMESH_JWT_SECRET", "relaymesh-secret-key-change-in-production"),
		AllowNonLoopback:         getEnvBool("RELAYMESH_ALLOW_NON_LOOPBACK", getEnvBool("TWH_LITE_ALLOW_NON_LOOPBACK", true)),
		WaitAfterMinutes:         getEnvInt("RELAYMESH_WAIT_AFTER_MINUTES", getEnvInt("TWH_LITE_WAIT_AFTER_MINUTES", 5)),
		MaxNoFeedbackChecks:      getEnvInt("RELAYMESH_MAX_NO_FEEDBACK_CHECKS", getEnvInt("TWH_LITE_MAX_NO_FEEDBACK_CHECKS", 24)),
		WaitInstruction:          getEnv("RELAYMESH_WAIT_INSTRUCTION", getEnv("TWH_LITE_WAIT_INSTRUCTION", "请等待 {minutes} 分钟后再次调用 MCP 获取反馈。")),
		WaitExhaustedInstruction: getEnv("RELAYMESH_WAIT_EXHAUSTED_INSTRUCTION", getEnv("TWH_LITE_WAIT_EXHAUSTED_INSTRUCTION", "本次等待窗口已结束；暂无新反馈。如需继续推进，可提交新的 progress 或 question。")),
		FeedbackTimeoutSeconds:   getEnvInt("RELAYMESH_FEEDBACK_TIMEOUT_SECONDS", 120),
		WaitCountdownMinutes:     getEnvInt("RELAYMESH_WAIT_COUNTDOWN_MINUTES", 2),
		ASRAPIURL:                getEnv("RELAYMESH_ASR_API_URL", getEnv("MIMO_API_URL", "https://api.xiaomimimo.com/v1/chat/completions")),
		ASRAPIKey:                getEnv("RELAYMESH_ASR_API_KEY", getEnv("MIMO_API_KEY", "")),
		ASRModel:                 getEnv("RELAYMESH_ASR_MODEL", getEnv("MIMO_MODEL", "mimo-v2.5-asr")),
		HTTPSPort:                getEnvInt("RELAYMESH_HTTPS_PORT", getEnvInt("HTTPS_PORT", 18776)),
		TLSEnabled:               getEnvBool("RELAYMESH_TLS_ENABLED", true),
		TLSCertPath:              getEnv("RELAYMESH_TLS_CERT", "data/certs/cert.pem"),
		TLSKeyPath:               getEnv("RELAYMESH_TLS_KEY", "data/certs/key.pem"),
		DevMode:                  getEnvBool("RELAYMESH_DEV", false),
		AutoOpenBrowser:          getEnvBool("RELAYMESH_OPEN_BROWSER", true),
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
