package terminal

import (
	"context"
	"sync"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/config"
)

// Reason 是总闸的判定原因码（稳定的机器可读值，前端据此展示引导文案）。
type Reason string

const (
	ReasonOK            Reason = "ok"
	ReasonNoPassword    Reason = "no_password"     // 未设置 Web 登录密码：任何能到达服务的人都等同管理员，终端必须关闭
	ReasonWeakJWTSecret Reason = "weak_jwt_secret" // JWT 签名密钥不合格：令牌可被伪造，终端必须关闭
	ReasonDisabledByEnv Reason = "disabled_by_env" // 运维通过 RELAYMESH_TERMINAL=off 强制关闭
	ReasonInternal      Reason = "internal_error"  // 无法确认前置条件（例如读取凭据失败）：按关闭处理
)

// Decision 是一次总闸判定。
type Decision struct {
	Open   bool
	Reason Reason
}

// CredentialSource 读取当前生效的登录凭据（环境变量与数据库已合并后的结果）。
type CredentialSource interface {
	EffectiveCredentials(ctx context.Context) (username string, password string, err error)
}

// Gate 是终端功能的总闸。
//
// 「可用 = 已设 Web 密码 ∧ JWT 签名密钥强度合格 ∧ 未被 RELAYMESH_TERMINAL=off」。
// Evaluate 对每次请求实时判定（Strict）；Watch 以固定间隔轮询，在状态翻转时立刻回调，
// 用于主动拆除已建立的 WebSocket 连接（没有新请求到来时 Strict 无法触发拆除）。
type Gate struct {
	mode      Mode
	jwtStrong bool
	creds     CredentialSource

	mu   sync.Mutex
	last Decision
	seen bool
}

// NewGate 创建总闸。jwtSecret 只用于启动时的强度判定，不会被保存。
func NewGate(mode Mode, jwtSecret string, creds CredentialSource) *Gate {
	return &Gate{
		mode:      mode,
		jwtStrong: config.IsStrongJWTSecret(jwtSecret),
		creds:     creds,
	}
}

// Evaluate 判定当前是否允许使用终端功能。任何无法确认的情况都按关闭处理（fail-closed）。
func (g *Gate) Evaluate(ctx context.Context) Decision {
	if g.mode == ModeOff {
		return Decision{Reason: ReasonDisabledByEnv}
	}
	if !g.jwtStrong {
		return Decision{Reason: ReasonWeakJWTSecret}
	}
	if g.creds == nil {
		return Decision{Reason: ReasonInternal}
	}
	_, password, err := g.creds.EffectiveCredentials(ctx)
	if err != nil {
		return Decision{Reason: ReasonInternal}
	}
	if password == "" {
		return Decision{Reason: ReasonNoPassword}
	}
	return Decision{Open: true, Reason: ReasonOK}
}

// Watch 每隔 interval 判定一次，状态（Open 或 Reason）发生变化时调用 onChange(旧, 新)；首次判定只记录基线，不回调。
// ctx 结束时返回。onChange 在本 goroutine 内同步执行，耗时操作应自行异步。
func (g *Gate) Watch(ctx context.Context, interval time.Duration, onChange func(prev, cur Decision)) {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	g.observe(ctx, nil)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// ctx 与 ticker 同时就绪时 select 会随机挑一个：ctx 已经结束就不再判定（关闭流程里的判定不算数）。
			if ctx.Err() != nil {
				return
			}
			g.observe(ctx, onChange)
		}
	}
}

// Observe 立即判定一次并按需回调，供凭据被修改后主动触发（不必等下一个轮询周期）。
func (g *Gate) Observe(ctx context.Context, onChange func(prev, cur Decision)) {
	g.observe(ctx, onChange)
}

func (g *Gate) observe(ctx context.Context, onChange func(prev, cur Decision)) {
	cur := g.Evaluate(ctx)
	// 判定期间 ctx 已经结束（Hub 正在关闭，或调用方放弃了）：读凭据失败是被取消打断的，不是真的「无法确认前置条件」。
	// 这样的判定既不记录、也不回调——否则关闭的那一瞬间会被当成「总闸关闭」，收尾逻辑会把主机上全部进行中的模型运行取消
	// （2026-10-16 一次热更就这样打断了一个跑了近 4 小时的非交互运行）。
	if ctx.Err() != nil {
		return
	}
	g.mu.Lock()
	prev, seen := g.last, g.seen
	g.last, g.seen = cur, true
	g.mu.Unlock()
	if seen && prev != cur && onChange != nil {
		onChange(prev, cur)
	}
}
