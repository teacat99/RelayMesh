package terminal

import (
	"log"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 模型运行结束的通知挂钩：让 ntfy 这类「浏览器之外」的提醒知道一次运行终结了。
//
// 终端包只报告事实，不替接收方做「要不要提醒」的判断——那取决于接收方自己的配置（开关、时长门槛……）。
// 事件在 ExecService.finalize 里发出：它是所有终态的唯一出口，且自带「只终结一次」的保护，
// 所以每次运行恰好通知一次，主机重发的结束事件、重复的对账都不会让接收方收到第二条。

// RunEndEvent 是一次模型运行终结时对外报告的事实。
type RunEndEvent struct {
	// Run 是终态记录的快照：命令是脱敏副本，预览是有界的；完整输出从不在这里。
	Run model.TerminalRun
	// CancelBy 是请求取消这次运行的来源（user / pause / revoked / system / model）；
	// 空表示没有任何人要求取消——取消状态加上空来源，就是「被主机侧中断」（典型是守护进程停了）。
	CancelBy string
	// Why 是终态原因的中文说明（如「主机重启，进程已丢失」）；没有原因或原因码不认识时为空。
	Why string
}

// SetRunEndHook 接入运行终结通知；启动时调用一次，传 nil 表示摘掉。
//
// fn 在终结运行的协程里被同步调用（此时状态已落库、事件已发布，不持有任何锁），所以必须立即返回，
// 不能在里面做网络请求；需要慢操作的接收方自己入队。fn 里的 panic 会被截住，不影响运行的收尾。
func (h *Hub) SetRunEndHook(fn func(RunEndEvent)) {
	if h == nil || h.exec == nil {
		return
	}
	if fn == nil {
		h.exec.endHook.Store(nil)
		return
	}
	h.exec.endHook.Store(&fn)
}

// runEnded 把一次终结报告给挂钩（没有接线就什么都不做）。
func (s *ExecService) runEnded(rec model.TerminalRun, cancelBy string) {
	s.watch.ended(rec) // 输出监视：把终态交给监视协程补读尾巴、给对话的状态一个定论（非阻塞）
	if s.h != nil && s.h.guardian != nil {
		s.h.guardian.onRunEnded(rec) // 系统唤醒的运行：在聊天里记一条结束的系统行（内部处理，自带 panic 保护以外不会阻塞）
	}
	fn := s.endHook.Load()
	if fn == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[terminal] 运行结束通知挂钩出错（运行本身不受影响）：%v", r)
		}
	}()
	(*fn)(RunEndEvent{Run: rec, CancelBy: cancelBy, Why: failMessage(rec.DenyReason)})
}

// HostDisplayName 是一台主机对外的名字：管理员写的备注优先，没有备注才用主机名。
// 给包外需要「写一句人话」的地方用（比如推送里的主机名），规则与界面和 MCP 里一致。
func HostDisplayName(r model.TerminalHost) string { return hostDisplayName(r) }
