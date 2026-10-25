package terminal

import (
	"context"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// 终端的运行服务（历史名「会话守护」，D-214）。
//
// 它原先有两件事：判断交互式「开终端」预设拉起的智能体会话有没有意外结束 / 停摆并通知（守护），以及非交互运行的提交协调。
// 守护连同交互式预设在 #245 第二期下线，这里只剩第二件事：
//   - 同一个工作流 / 预设 / 项目管理 Agent 同一时刻只允许一个提交（sessionRunMu、launcherRunning、agentRunMu，见
//     session_run.go、launcher_run.go、agent_run.go）；
//   - 运行结束时的处理（headless_run.go 的 onRunEnded）与自动续跑（auto_resume.go）；
//   - 周期性地跑自动续跑检查（run）。

const (
	// guardFirstDelay：Hub 启动后等这么久才开始第一轮检查，让主机的 Agent 先重连、报上会话清单。
	guardFirstDelay = 20 * time.Second
	// guardTickEvery：两轮检查的间隔。
	guardTickEvery = time.Minute
)

type guardService struct {
	h        *Hub
	autoHook atomic.Pointer[func(AutoResumeEvent)] // 自动续跑放弃时的通知（#151），见 auto_resume.go
	// tickEvery 是两轮检查的间隔：默认 guardTickEvery；只有实验与端到端测试会经 Options.GuardTickEvery 缩短它。
	tickEvery time.Duration

	// 会话页「运行」（#128）正在提交的工作流：同一个工作流同一时刻只允许一个提交（见 session_run.go）。
	sessionRunMu   sync.Mutex
	sessionRunning map[string]bool
	// 预设运行正在提交的预设（#176）：同一个预设同一时刻只允许一个提交（共用 sessionRunMu）。
	// 「上一次运行还没结束」的检查看的是已落库的运行记录，两个几乎同时到达的提交都能在记录落库前通过它；
	// 对续聊来说后面的会被主机上的对话冲突挡住，但新开对话没有这道保险，会开出两个智能体——所以提交要串行。
	launcherRunning map[string]bool

	// 项目管理 Agent 经 MCP 运行预设（#179，见 agent_run.go）：频率账本的检查与提交放在同一把锁里，
	// 两个几乎同时到达的调用不会同时通过「每小时 / 每天 / 同时在跑」的上限。
	agentRunMu sync.Mutex
	// agentLimits 覆盖频率默认值（零值 = 用 defaultAgentRunLimits）；只有测试会改它。
	agentLimits AgentRunLimits
}

func newGuardService(h *Hub) *guardService {
	g := &guardService{h: h, sessionRunning: map[string]bool{}, tickEvery: guardTickEvery}
	if h.opts.GuardTickEvery > 0 {
		g.tickEvery = h.opts.GuardTickEvery
	}
	return g
}

// guardNameKey 是「按名字认会话」的键：与启动器一致（启动时新建会话用的就是预设的名字），不分大小写、空白折叠。
func guardNameKey(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// ---------- 周期检查 ----------

// run 是后台循环：直到 ctx 取消。
func (g *guardService) run(ctx context.Context) {
	first := guardFirstDelay
	if g.tickEvery < first {
		first = g.tickEvery // 间隔被缩短（实验 / 测试）时，第一轮也别再多等
	}
	timer := time.NewTimer(first)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		g.safeTick(ctx)
		timer.Reset(g.tickEvery)
	}
}

// safeTick 跑一轮自动续跑检查；出任何意外都只记日志、下一轮继续。
func (g *guardService) safeTick(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[terminal] 自动续跑的检查出错（下一轮继续）：%v", r)
		}
	}()
	g.autoResumeTick(ctx, g.h.now())
}
