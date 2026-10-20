package agent

import (
	"context"
	"fmt"

	"github.com/teacat99/RelayMesh/v2/internal/agent/holder"
)

// Limits 与 ShellFunc 的实现在 holder 包里（会话由守护进程持有）；这里保留别名，
// 让命令行与测试沿用 agent.Limits / agent.DefaultLimits 这些名字。
type (
	Limits    = holder.Limits
	ShellFunc = holder.ShellFunc
)

// DefaultLimits 返回默认限额。
func DefaultLimits() Limits { return holder.DefaultLimits() }

// Manager 是会话管理器（内嵌模式下由 Agent 自己持有）。
type Manager = holder.Manager

// HolderMode 决定终端会话由谁持有。
type HolderMode string

const (
	// HolderEmbedded 让会话运行在 Agent 进程内：行为与最初的实现一致，会话随进程结束，不具持久性。
	// 它也是 Options 的零值——库与测试不会意外去连接机器上真实的守护进程。
	HolderEmbedded HolderMode = "embedded"
	// HolderExternal 只使用外置守护进程；连不上就报告「不可用」并重试，绝不静默退化为内嵌
	// （那会产生不持久的会话，还可能与守护进程里的会话重复）。
	HolderExternal HolderMode = "external"
	// HolderAuto 启动时能连上外置守护进程就用它，否则使用内嵌模式。
	HolderAuto HolderMode = "auto"
)

// ParseHolderMode 解析 --holder 选项的取值。
func ParseHolderMode(s string) (HolderMode, error) {
	switch HolderMode(s) {
	case "", HolderAuto:
		return HolderAuto, nil
	case HolderExternal, HolderEmbedded:
		return HolderMode(s), nil
	}
	return "", fmt.Errorf("--holder 只能是 auto、external 或 embedded，收到 %q", s)
}

// socketSource 连接外置守护进程的本地套接字。
type socketSource struct {
	path    string
	version string
}

func (s *socketSource) Dial(ctx context.Context) (*holder.Client, error) {
	conn, err := holder.Dial(ctx, s.path)
	if err != nil {
		return nil, err
	}
	return holder.Handshake(ctx, conn, holder.RoleController, s.version)
}

// embeddedSource 连接进程内的 Host（经内存管道，走与外置模式完全相同的协议）。
type embeddedSource struct {
	host    *holder.Host
	version string
}

func (e *embeddedSource) Dial(ctx context.Context) (*holder.Client, error) {
	client, server := holder.NewPipe()
	go func() { _ = e.host.Serve(context.Background(), server) }()
	return holder.Handshake(ctx, client, holder.RoleController, e.version)
}
