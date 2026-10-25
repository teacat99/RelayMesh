package oauth_test

// #240：授权页「待决请求」占用的限流名额，必须随请求的生命周期归还。
//
// 背景：/oauth/authorize 把请求放进「待决」时会在限流器里占一个名额（同一 IP+客户端最多 20 个、全局最多 5000 个），
// 以前只有批准 / 拒绝时才归还；用户打开授权页又关掉、或客户端发起后不再回来的请求，过期后名额永远不还，
// 累积到上限就会一直得到 slow_down，直到 Hub 重启。并行的两个问题：保存「请求→IP」的 map 没加锁（并发请求会让进程
// 因 concurrent map writes 直接崩溃），同一个待决请求被并发批准时没有原子地「取走」，可能被兑现多次。
// 这里的用例都走真实的 HTTP 路由，覆盖：过期归还、批准 / 拒绝归还、并发无竞争、同一请求只被决定一次；
// 「管理员删除客户端时归还」黑盒观察不到（删除后授权会先在找不到客户端处失败），由包内的 memoryStore 单测直接断言。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/api"
	"github.com/teacat99/RelayMesh/v2/internal/oauth"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 与 ratelimit.go 里 oauthPendingPerIPClient 对齐（那个常量是包内私有的，这里写死并注明出处）。
const pendingPerIPClientForTest = 20

// authorizeRaw 发一次 /oauth/authorize，返回状态码与 Location；remoteAddr 为空时用 httptest 默认地址。
func authorizeRaw(engine http.Handler, remoteAddr, clientID, redirectURI, resource, challenge string) (int, string) {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, buildAuthorizeURL(clientID, redirectURI, resource, challenge, "st"), nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	if remoteAddr != "" {
		req.RemoteAddr = remoteAddr
	}
	engine.ServeHTTP(w, req)
	return w.Code, w.Header().Get("Location")
}

// pendingIDFromLocation 从授权页跳转地址里取出待决请求编号。
func pendingIDFromLocation(loc string) string {
	i := strings.Index(loc, "req=")
	if i < 0 {
		return ""
	}
	id, _ := url.QueryUnescape(loc[i+4:])
	if j := strings.IndexAny(id, "&"); j >= 0 {
		id = id[:j]
	}
	return id
}

// decideRaw 对待决请求提交一次「拒绝」（拒绝不需要终端解锁，只需要登录）；返回状态码。
func decideRaw(engine http.Handler, jwt, reqID string) int {
	dec, _ := json.Marshal(map[string]any{"approve": false, "nonce": reqID})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/oauth/requests/"+reqID+"/decision", bytes.NewReader(dec))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+jwt)
	engine.ServeHTTP(w, req)
	return w.Code
}

type quotaEnv struct {
	engine      http.Handler
	jwt         string
	clientID    string
	redirectURI string
	resource    string
	challenge   string
}

func newQuotaEnv(t *testing.T) *quotaEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)
	st, err := store.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	enableOAuthServer(t, st)
	cfg := oauthTestCfg()
	srv := api.NewServer(cfg, st, nil)
	engine := srv.Engine()
	jwt := loginJWT(t, engine, "admin", cfg.WebPassword)
	clientID, redirectURI, resource, _, challenge := registerTestClient(t, engine)
	return &quotaEnv{engine: engine, jwt: jwt, clientID: clientID, redirectURI: redirectURI, resource: resource, challenge: challenge}
}

func (e *quotaEnv) authorize(remoteAddr string) (int, string) {
	return authorizeRaw(e.engine, remoteAddr, e.clientID, e.redirectURI, e.resource, e.challenge)
}

// 被放弃的待决请求过期之后，名额必须归还：同一 IP+客户端放弃 20 个请求后仍能发起新的授权。
func TestPendingQuotaReleasedWhenAbandonedRequestsExpire(t *testing.T) {
	env := newQuotaEnv(t)

	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	oauth.SetServiceClock(func() time.Time { return now })
	t.Cleanup(func() { oauth.SetServiceClock(nil) })

	for i := 0; i < pendingPerIPClientForTest; i++ {
		if code, _ := env.authorize(""); code != http.StatusFound {
			t.Fatalf("第 %d 个请求还没到上限就被拒了：%d", i+1, code)
		}
	}
	if code, _ := env.authorize(""); code != http.StatusTooManyRequests {
		t.Fatalf("占满名额之后的下一个请求应该被限流（429 slow_down），实际 %d", code)
	}

	// 过 11 分钟：超过待决请求 10 分钟的有效期，被放弃的这 20 个请求都已经作废。
	now = now.Add(11 * time.Minute)
	code, _ := env.authorize("")
	if code != http.StatusFound {
		t.Fatalf("待决请求已过期，名额应当归还、新的授权应当能发起；实际 %d（名额泄漏，要等 Hub 重启才恢复）", code)
	}
	// 归还的是全部名额，而不是只还一个：再放弃 19 个也应当仍然能发起。
	for i := 0; i < pendingPerIPClientForTest-1; i++ {
		if code, _ := env.authorize(""); code != http.StatusFound {
			t.Fatalf("过期归还后第 %d 个新请求被拒了：%d", i+2, code)
		}
	}
}

// 没有任何新的待决请求进来时，过期请求也要能被回收：限流的判断发生在「放进待决」之前，
// 所以名额回收不能只挂在放进待决的那一步上，否则一旦占满就再也走不到回收。
func TestPendingQuotaReleasedEvenWhenLimitedRequestsKeepComing(t *testing.T) {
	env := newQuotaEnv(t)

	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	oauth.SetServiceClock(func() time.Time { return now })
	t.Cleanup(func() { oauth.SetServiceClock(nil) })

	for i := 0; i < pendingPerIPClientForTest; i++ {
		env.authorize("")
	}
	// 占满之后客户端不停重试（每次都是 429），期间时间一点点往前走。
	for i := 0; i < 5; i++ {
		now = now.Add(time.Minute)
		if code, _ := env.authorize(""); code != http.StatusTooManyRequests {
			t.Fatalf("重试 %d：有效期内应当一直被限流，实际 %d", i+1, code)
		}
	}
	now = now.Add(6 * time.Minute) // 距离最早的请求已经 11 分钟
	if code, _ := env.authorize(""); code != http.StatusFound {
		t.Fatalf("请求过期后，哪怕之前一直在被限流，也应当恢复；实际 %d", code)
	}
}

// 批准 / 拒绝仍然会立刻归还名额（原有行为不能退化）。
func TestPendingQuotaReleasedOnDecision(t *testing.T) {
	env := newQuotaEnv(t)
	// 轮数要超过单 IP+客户端的名额上限（20），又要留在单 IP 每分钟 60 次的端点频率上限之内（另有 1 次注册）。
	for round := 0; round < pendingPerIPClientForTest+15; round++ {
		code, loc := env.authorize("")
		if code != http.StatusFound {
			t.Fatalf("第 %d 轮发起授权失败：%d（每轮都拒绝，名额应当一直够用）", round+1, code)
		}
		if st := decideRaw(env.engine, env.jwt, pendingIDFromLocation(loc)); st != http.StatusOK {
			t.Fatalf("第 %d 轮拒绝失败：%d", round+1, st)
		}
	}
}

// 并发发起 / 决定授权不能有数据竞争（以前保存「请求→IP」的 map 没加锁，并发写会让进程直接崩溃）。
// 配合 go test -race 才能稳定复现；不带 -race 时，旧实现也有相当概率触发 fatal error: concurrent map writes。
func TestAuthorizeAndDecideConcurrentlyAreRaceFree(t *testing.T) {
	env := newQuotaEnv(t)

	const workers, rounds = 24, 8
	var wg sync.WaitGroup
	var failures atomic.Int32
	report := make(chan string, workers*rounds)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			addr := fmt.Sprintf("192.0.2.%d:40000", w+1) // 每个 worker 一个独立 IP，避开单 IP 的名额与频率上限
			for r := 0; r < rounds; r++ {
				code, loc := env.authorize(addr)
				if code != http.StatusFound {
					failures.Add(1)
					report <- fmt.Sprintf("worker %d 第 %d 轮发起授权：%d", w, r+1, code)
					return
				}
				if r%2 == 0 { // 一半拒绝掉，另一半放着不管（模拟被放弃的请求），两种退出方式同时存在
					if st := decideRaw(env.engine, env.jwt, pendingIDFromLocation(loc)); st != http.StatusOK {
						failures.Add(1)
						report <- fmt.Sprintf("worker %d 第 %d 轮拒绝：%d", w, r+1, st)
						return
					}
				}
			}
		}(w)
	}
	wg.Wait()
	close(report)
	if failures.Load() > 0 {
		var msgs []string
		for m := range report {
			msgs = append(msgs, m)
		}
		t.Fatalf("并发流程出现失败：\n%s", strings.Join(msgs, "\n"))
	}
}

// 同一个待决请求被并发决定时，只能有一个成功：请求要被原子地「取走」，名额也只归还一次。
func TestPendingRequestDecidedOnlyOnceUnderConcurrency(t *testing.T) {
	env := newQuotaEnv(t)

	for round := 0; round < 5; round++ {
		code, loc := env.authorize("")
		if code != http.StatusFound {
			t.Fatalf("第 %d 轮发起授权失败：%d", round+1, code)
		}
		id := pendingIDFromLocation(loc)

		const racers = 12
		var wg sync.WaitGroup
		var ok atomic.Int32
		start := make(chan struct{})
		for i := 0; i < racers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if decideRaw(env.engine, env.jwt, id) == http.StatusOK {
					ok.Add(1)
				}
			}()
		}
		close(start)
		wg.Wait()
		if n := ok.Load(); n != 1 {
			t.Fatalf("第 %d 轮：同一个待决请求被 %d 个并发决定同时成功，只应当有 1 个", round+1, n)
		}
	}
	// 名额只还一次：之后仍然能放弃满 20 个，第 21 个才被限流（若重复归还，计数会被拉成负数、上限形同虚设）。
	for i := 0; i < pendingPerIPClientForTest; i++ {
		if code, _ := env.authorize(""); code != http.StatusFound {
			t.Fatalf("并发决定之后第 %d 个新请求被拒了：%d", i+1, code)
		}
	}
	if code, _ := env.authorize(""); code != http.StatusTooManyRequests {
		t.Fatalf("放弃满 %d 个之后应当被限流，实际 %d（名额被重复归还，上限失效）", pendingPerIPClientForTest, code)
	}
}
