package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 「删除主机时同时卸载设备端」的 Agent 一侧：
//   - 只有以常驻服务运行、并且提供了 SelfUninstall 的 Agent 才在 hello 里声明 Uninstall 能力；
//   - 收到 uninstall 帧才会调用 SelfUninstall（待启用期间、没有能力时一律忽略）；
//   - 卸载进程启动成功后暂停重连一段时间，避免刚被删除的主机又在发现列表里冒出来一次。

func persistentIdentity(t *testing.T) *Identity {
	t.Helper()
	id, err := LoadOrCreateIdentity(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func uninstallFrame(t *testing.T, purge bool) []byte {
	t.Helper()
	b, err := termproto.Marshal(termproto.Uninstall{T: termproto.TUninstall, Purge: purge})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestHelloAdvertisesUninstallOnlyForInstalledService(t *testing.T) {
	cases := []struct {
		name      string
		self      func(bool) error
		temporary bool
		want      bool
	}{
		{"没有提供卸载能力（前台运行 / 不支持的系统）", nil, false, false},
		{"常驻身份且提供了卸载能力", func(bool) error { return nil }, false, true},
		{"临时身份即使提供了也不声明（没有安装过任何东西）", func(bool) error { return nil }, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var (
				mu  sync.Mutex
				got termproto.Hello
			)
			target := stubHub(t, func(ctx context.Context, ws *websocket.Conn, _ *http.Request) {
				ch, _ := challenge(t, termproto.ProtocolVersion)
				sendJSON(t, ctx, ws, ch)
				_, data, err := ws.Read(ctx)
				if err != nil {
					return
				}
				var h termproto.Hello
				if json.Unmarshal(data, &h) != nil {
					return
				}
				mu.Lock()
				got = h
				mu.Unlock()
				pub, _ := termproto.DecodeKey(h.Pub, 32)
				sendJSON(t, ctx, ws, termproto.Welcome{T: termproto.TWelcome, Status: termproto.WelcomePending, MachineCode: termproto.MachineCode(pub)})
				_ = ws.Close(websocket.StatusCode(termproto.CloseUnknownMachine), "bye")
			})
			a := newAgentFor(t, target, func(o *Options) {
				o.SelfUninstall = tc.self
				if !tc.temporary {
					o.Identity = persistentIdentity(t)
				}
			})
			connectWith(t, a)
			mu.Lock()
			defer mu.Unlock()
			if got.Meta.Uninstall != tc.want {
				t.Fatalf("hello.meta.uninstall = %v，期望 %v", got.Meta.Uninstall, tc.want)
			}
		})
	}
}

func TestRelayHandlesUninstallFrame(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls, started int
	var gotPurge bool
	r := newRelay(ctx, nil, nil, true, t.Logf)
	r.selfUninstall = func(purge bool) error { calls++; gotPurge = purge; return nil }
	r.onUninstalling = func() { started++ }

	if code, stop := r.handleText(uninstallFrame(t, true)); stop || code != 0 {
		t.Fatalf("卸载帧不应结束连接: code=%d stop=%v", code, stop)
	}
	if calls != 1 || !gotPurge || started != 1 {
		t.Fatalf("应调用一次卸载并带上 purge，并通知已启动: calls=%d purge=%v started=%d", calls, gotPurge, started)
	}

	r.handleText(uninstallFrame(t, false))
	if calls != 2 || gotPurge {
		t.Fatalf("purge=false 应原样传递: calls=%d purge=%v", calls, gotPurge)
	}

	// 卸载进程没能启动：什么都没发生，不能进入「暂停重连」。
	r.selfUninstall = func(bool) error { return errors.New("systemd-run 不可用") }
	r.handleText(uninstallFrame(t, true))
	if started != 2 {
		t.Fatalf("失败时不应通知已启动（否则会白白暂停重连）: started=%d", started)
	}

	// 没有声明能力：忽略，不崩溃，也不会通知「已启动」。
	r.selfUninstall = nil
	r.handleText(uninstallFrame(t, true))
	if calls != 2 || started != 2 {
		t.Fatalf("没有能力时不应调用也不应通知: calls=%d started=%d", calls, started)
	}
}

func TestRelayIgnoresUninstallWhilePending(t *testing.T) {
	// 待启用期间只接受 approved / rejected：一个还没被管理员启用的连接不该能触发本机的卸载。
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	called := false
	r := newRelay(ctx, nil, nil, false, t.Logf)
	r.selfUninstall = func(bool) error { called = true; return nil }
	r.handleText(uninstallFrame(t, true))
	if called {
		t.Fatal("待启用的连接不应触发卸载")
	}
}

func TestUninstallHoldoff(t *testing.T) {
	target := ServerTarget{URL: "ws://127.0.0.1:1" + AgentWSPath, Host: "127.0.0.1:1"}
	a := newAgentFor(t, target, nil)
	if a.uninstallHoldoff() > 0 {
		t.Fatal("没有接受过卸载请求时不应暂停重连")
	}
	a.uninstallUntil.Store(time.Now().Add(uninstallGrace).UnixNano())
	if h := a.uninstallHoldoff(); h <= time.Minute || h > uninstallGrace {
		t.Fatalf("接受卸载请求后应暂停约 %v: %v", uninstallGrace, h)
	}
	a.uninstallUntil.Store(time.Now().Add(-time.Second).UnixNano())
	if a.uninstallHoldoff() > 0 {
		t.Fatal("过期后应恢复正常重连（卸载没有成功时 Agent 不能就此失联）")
	}
}

func TestAgentAcceptsUninstallFromHubAndHoldsOffReconnect(t *testing.T) {
	var calls atomic.Int32
	var purge atomic.Bool
	processed := make(chan struct{})

	target := stubHub(t, func(ctx context.Context, ws *websocket.Conn, _ *http.Request) {
		ch, _ := challenge(t, termproto.ProtocolVersion)
		sendJSON(t, ctx, ws, ch)
		_, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		var h termproto.Hello
		if json.Unmarshal(data, &h) != nil {
			return
		}
		pub, _ := termproto.DecodeKey(h.Pub, 32)
		sendJSON(t, ctx, ws, termproto.Welcome{
			T: termproto.TWelcome, Status: termproto.WelcomeEnabled, MachineCode: termproto.MachineCode(pub),
			Policy: termproto.Policy{Terminal: true},
		})
		// 与 Hub 删除主机时的顺序一致：先 uninstall，再 policy(terminal=false)，最后以 CloseDisabled 关闭。
		sendJSON(t, ctx, ws, termproto.Uninstall{T: termproto.TUninstall, Purge: true})
		select {
		case <-processed:
		case <-ctx.Done():
			return
		}
		sendJSON(t, ctx, ws, termproto.PolicyMsg{T: termproto.TPolicy, Policy: termproto.Policy{Terminal: false, Reason: "host_deleted"}})
		_ = ws.Close(websocket.StatusCode(termproto.CloseDisabled), "host_deleted")
	})

	a := newAgentFor(t, target, func(o *Options) {
		o.Identity = persistentIdentity(t)
		o.SelfUninstall = func(p bool) error {
			calls.Add(1)
			purge.Store(p)
			close(processed)
			return nil
		}
	})
	res := connectWith(t, a)

	if calls.Load() != 1 || !purge.Load() {
		t.Fatalf("Agent 应收到并执行一次卸载（purge）: calls=%d purge=%v", calls.Load(), purge.Load())
	}
	if int(res.code) != termproto.CloseDisabled {
		t.Fatalf("随后仍应按关闭码处理: %+v", res)
	}
	if h := a.uninstallHoldoff(); h < time.Minute {
		t.Fatalf("接受卸载后应暂停重连，否则这台已删除的主机会在发现列表里再冒出来: %v", h)
	}
}
