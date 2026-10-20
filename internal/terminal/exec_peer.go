package terminal

import (
	"context"

	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// execPeer 是一台主机上「真正执行命令」的那一层：
//   - Agent 主机：经 Agent 连接，由主机上的会话守护进程持有进程；
//   - SSH 主机：经 SSH 在远端启动后台任务并轮询（见 ssh_peer.go）。
//
// ExecService 的状态机、闸门流水线、审计、结果整形对两者完全一致；差别只在这个接口之后。
// 事件方向相反但形状相同：执行层把进展翻译成与 Agent 相同的 termproto 帧，交给 ExecService 的 onRun* 处理。
type execPeer interface {
	machine() string
	// usable 报告此刻能否下发运行 / 读取输出。
	usable() bool
	// gone 在通道结束时关闭（Agent 断线）；返回 nil 表示通道不会因连接而结束（SSH 按需建立连接）。
	gone() <-chan struct{}
	// execSupport 报告主机的执行能力：supported 为 false 表示不支持；denied 是本机一票否决；info 是执行环境（可能为 nil）。
	execSupport() (supported, denied bool, info *termproto.ExecInfo)
	sendRun(r termproto.Run) error
	sendRunCancel(id, reason string) error
	sendRunAck(id string) error
	readRun(ctx context.Context, id, stream string, off uint64, max int) (*RunPage, error)
}

var _ execPeer = (*agentConn)(nil)

func (a *agentConn) machine() string       { return a.code }
func (a *agentConn) usable() bool          { return a.isEnabled() }
func (a *agentConn) gone() <-chan struct{} { return a.ctx.Done() }

// peerAlive 判断通道此刻是否仍然存活：gone 已关闭即不存活。
func peerAlive(p execPeer) bool {
	if p == nil {
		return false
	}
	g := p.gone()
	if g == nil {
		return true
	}
	select {
	case <-g:
		return false
	default:
		return true
	}
}

// peerFor 返回某台主机当前的执行层：在线的 Agent 优先；否则是 SSH 主机的执行层；都没有返回 nil。
// 返回值是真正的 nil 接口（不会出现「非 nil 接口包着 nil 指针」）。
func (s *ExecService) peerFor(machine string) execPeer {
	if a := s.h.onlineAgent(machine); a != nil && a.isEnabled() {
		return a
	}
	if p := s.h.ssh.peerOf(machine); p != nil {
		return p
	}
	return nil
}
