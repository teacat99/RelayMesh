package api

import (
	"log"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/config"
	"github.com/teacat99/RelayMesh/v2/internal/installer"
	"github.com/teacat99/RelayMesh/v2/internal/store"
	"github.com/teacat99/RelayMesh/v2/internal/terminal"
)

// newTerminalHub 按配置创建终端 Hub，并把它与登录体系接好：
//   - 终端总闸的「密码」条件读取 AuthHandler.EffectiveCredentials（唯一真源）；
//   - 登录凭据被修改 / 重置时，撤销全部解锁令牌、票据并断开浏览器终端连接。
//
// 解锁尝试使用独立的 RateLimiter，避免与登录防爆破的封禁名单互相污染。
// 初始化失败时返回 (nil, nil)：终端功能整体不可用，其余功能不受影响。
func newTerminalHub(cfg *config.Config, st *store.Store, auth *AuthHandler) (*terminal.Hub, *RateLimiter) {
	mode, warning := terminal.ResolveMode(cfg.TerminalMode)
	if warning != "" {
		log.Printf("[security] %s", warning)
	}
	always, warning := terminal.ResolveDiscovery(cfg.TerminalDiscovery)
	if warning != "" {
		log.Printf("[security] %s", warning)
	}

	recordOff, warning := terminal.ResolveRecord(cfg.TerminalRecord)
	if warning != "" {
		log.Printf("[security] %s", warning)
	}

	execOn, warning := terminal.ResolveExec(cfg.TerminalExec)
	if warning != "" {
		log.Printf("[security] %s", warning)
	}

	filesOn, warning := terminal.ResolveFiles(cfg.TerminalFiles)
	if warning != "" {
		log.Printf("[security] %s", warning)
	}
	if filesOn && !execOn {
		log.Printf("[security] RELAYMESH_TERMINAL_FILES=on 需要同时开启 RELAYMESH_TERMINAL_EXEC=on（文件能力复用模型执行的全部闸门），文件能力保持关闭")
		filesOn = false
	}

	distDir := installer.ResolveDir(cfg.AgentDistDir)

	limiter := NewRateLimiter()
	hub, err := terminal.NewHub(terminal.Options{
		Auth:            auth,
		Store:           st,
		Limiter:         limiter,
		JWTSecret:       cfg.JWTSecret,
		Mode:            mode,
		DiscoveryAlways: always,
		AllowedOrigins:  splitCSV(cfg.AllowedOrigins),
		AgentDistDir:    distDir,
		Version:         cfg.Version,
		ExecEnabled:     execOn,
		FilesEnabled:    filesOn,
		SSHSecretKey:    cfg.SSHSecretKey,

		RecordOff:           recordOff,
		RecordRetentionDays: cfg.TerminalRecordDays,
		RecordSessionMB:     cfg.TerminalRecordSessionMB,
		RecordHostMB:        cfg.TerminalRecordHostMB,

		// 仅供实验与端到端测试缩短等待（生产不设）：自动续跑的检查间隔、自动续跑的退避与「续跑成功」的判定时长。
		GuardTickEvery:    time.Duration(max(cfg.TerminalGuardTickSec, 0)) * time.Second,
		AutoResumeBackoff: terminal.ParseAutoResumeBackoff(cfg.TerminalAutoResumeBackoffSec),
		AutoResumeOK:      time.Duration(max(cfg.TerminalAutoResumeOKSec, 0)) * time.Second,
	})
	if err != nil {
		limiter.Close()
		log.Printf("[terminal] 初始化失败，终端功能不可用: %v", err)
		return nil, nil
	}
	auth.SetCredentialsChangedHook(hub.CredentialsChanged)
	auth.SetCredentialRevealHook(hub.CredentialRevealed)

	discovery := "window"
	if always {
		discovery = "always"
	}
	logAgentDist(distDir)
	log.Printf("[terminal] 终端功能: mode=%s discovery=%s exec=%t files=%t（是否实际可用由总闸实时判定：需已设置 Web 密码且 JWT 密钥合格；exec 为模型执行的服务端总开关、files 为文件能力的服务端开关，默认都关闭）", mode, discovery, execOn, filesOn)
	return hub, limiter
}

// splitCSV 把逗号分隔的配置值拆成去空白、去空项的列表。
func splitCSV(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// logAgentDist 在启动日志里说明 Agent 安装源的状态：Web 生成的安装命令是否能直接从本服务端下载。
func logAgentDist(dir string) {
	platforms := installer.NewDist(dir).Platforms()
	switch {
	case len(platforms) > 0:
		log.Printf("[terminal] Agent 安装源: %s（%s）；发现窗口开启期间，Web 生成的安装命令直接从本服务端下载", dir, strings.Join(platforms, " "))
	case dir != "":
		log.Printf("[terminal] Agent 安装源: 目录 %s 里没有可分发的 relaymesh-agent-<系统>-<架构> 二进制，Web 将回退为 GitHub Release 来源的安装命令", dir)
	default:
		log.Printf("[terminal] Agent 安装源: 未找到 agent-dist 目录（可用 RELAYMESH_AGENT_DIST_DIR 指定），Web 将回退为 GitHub Release 来源的安装命令；Docker 镜像已内置")
	}
}
