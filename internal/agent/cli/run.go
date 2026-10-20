package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agent"
)

// cmdRun 前台运行 Agent，直到收到中断信号。
func cmdRun(ctx context.Context, args []string, env Env) int {
	var c common
	fs := newFlagSet("run", env)
	c.bind(fs, env)
	if err := parse(fs, args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			usage(env)
			return 0
		}
		env.errorf("参数错误: %v\n", err)
		return 2
	}
	if err := c.settle(fs); err != nil {
		env.errorf("参数错误: %v\n", err)
		return 2
	}

	if c.relaunched {
		// 提权后的子进程独占一个新窗口，退出前留住错误信息。
		defer pauseBeforeClose(env)
	}

	interactive := isInteractive(env.Stdin)
	cfg, err := c.resolve(env, interactive)
	if err != nil {
		env.errorf("配置错误: %v\n", err)
		return 2
	}

	// 后台运行（没有控制台可看）时把横幅与日志都写进文件；此后 Stdout / Stderr 都指向它。
	if c.logFile != "" {
		lf, err := openRotatingFile(c.logFile, logFileMaxBytes)
		if err != nil {
			env.errorf("无法打开日志文件 %s: %v\n", c.logFile, err)
			return 1
		}
		defer lf.Close()
		env.Stdout, env.Stderr = lf, lf
		interactive = false
	}

	// Windows 上未提权的交互式临时运行：请求提升，由提权后的子进程完成连接。
	if code, handled := maybeElevate(env, c, interactive); handled {
		return code
	}

	var id *agent.Identity
	if c.temp {
		id, err = agent.NewTemporaryIdentity()
	} else {
		id, err = agent.LoadOrCreateIdentity(cfg.dataDir)
	}
	if err != nil {
		env.errorf("无法加载身份: %v\n", err)
		return 1
	}

	logger := log.New(env.Stderr, "", log.LstdFlags)
	printBanner(env, cfg, id)

	opts := agent.Options{
		Server:   cfg.target,
		Trust:    cfg.trust,
		Identity: id,
		Version:  env.Version,
		Rev:      env.Rev,
		Limits:   cfg.limits,
		Logf:     logger.Printf,

		Holder:       cfg.holderMode,
		HolderSocket: cfg.holderSocket,
		Record:       cfg.record,
		Exec:         cfg.exec,
		Files:        cfg.files,
		SSH:          cfg.ssh,
	}
	if !c.temp {
		// 管理员在 Web 上删除主机并勾选「同时卸载设备端」时的本机卸载能力；不是装成服务运行的 Agent 为 nil（不声明能力）
		opts.SelfUninstall = selfUninstaller(env, c, cfg)
		// 管理员在 Web 上点「一键升级」时的本机升级能力（同样只有装成服务运行的 Agent 才有；是否真的允许由主机所有者的
		// 本地开关 `web-upgrade on` 决定，默认关闭）
		opts.SelfUpgrade = selfUpgrader(env, c, cfg)
	}
	a, err := agent.New(opts)
	if err != nil {
		env.errorf("配置错误: %v\n", err)
		return 2
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := a.Run(ctx); err != nil {
		env.errorf("运行失败: %v\n", err)
		return 1
	}
	if a.SessionsOutliveAgent() {
		logger.Printf("Agent 已退出；终端会话由会话守护进程持有，仍在运行")
	} else {
		logger.Printf("Agent 已退出，本机的全部终端会话已终止")
	}
	return 0
}

func printBanner(env Env, cfg resolved, id *agent.Identity) {
	env.printf("RelayMesh Agent %s\n", env.Version)
	env.printf("  服务端 : %s\n", cfg.target.URL)
	env.printf("  机器码 : %s\n", id.MachineCode())
	switch {
	case id.Kind == "temporary":
		env.printf("  身份   : 临时（不落盘；退出后机器码失效，下次运行需要重新启用）\n")
	case id.Created:
		env.printf("  身份   : 新生成，保存在 %s\n", id.Path)
	default:
		env.printf("  身份   : %s\n", id.Path)
	}
	env.printf("  信任   : %s\n", trustDescription(cfg))
	env.printf("  会话   : %s\n", holderDescription(cfg))
	if cfg.ssh != nil {
		// 网关默认开启：让运维一眼看到它在，以及它允许连到哪里、怎么收紧或关闭。
		allow := "不限制目标（可用 --ssh-allow 收紧，或用 --no-ssh-gateway 关闭）"
		if len(cfg.ssh.Allow) > 0 {
			allow = "只允许 " + strings.Join(cfg.ssh.Allow, "、")
		}
		env.printf("  SSH网关: 已开启，%s\n", allow)
	}
	if cfg.files.AllowPrivateURLs {
		env.printf("  文件   : push_url 可访问回环 / 私网地址（--file-url-allow-private）\n")
	}
	env.printf("\n")
}

func holderDescription(cfg resolved) string {
	switch cfg.holderMode {
	case agent.HolderExternal:
		return "只使用会话守护进程（Agent 重启不影响会话）；守护进程未运行时终端不可用，直到它启动"
	case agent.HolderEmbedded:
		return "内嵌（会话随 Agent 进程结束，界面会标注「重启会中断」）"
	}
	return "自动：检测到会话守护进程就交给它，否则内嵌（会话随 Agent 进程结束）"
}

func trustDescription(cfg resolved) string {
	switch {
	case len(cfg.trust.Pins) > 0:
		return fmt.Sprintf("固定证书指纹（%d 个）", len(cfg.trust.Pins))
	case !cfg.target.Secure:
		return "明文 ws://（--insecure，请确保网络可信）"
	case cfg.trust.Prompt != nil:
		return "系统证书；遇到未知证书时询问并记住（TOFU）"
	default:
		return "系统证书"
	}
}

// pollInterval 是 install 后等待服务就绪时的轮询间隔（测试可缩短）。
var pollInterval = 500 * time.Millisecond
