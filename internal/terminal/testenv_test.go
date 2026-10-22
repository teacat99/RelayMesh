package terminal

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/store"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

const (
	testPassword = "correct horse battery staple"
	testUser     = "admin"
)

// fakeAuth 以最小实现满足 Authenticator：Bearer 令牌形如 "human-<主体>" 表示人类登录，其余（含 MCP 令牌）一律无效。
type fakeAuth struct {
	*fakeCreds
}

func (f fakeAuth) VerifyHumanJWT(token string) (string, bool) {
	if strings.HasPrefix(token, "human-") && len(token) > len("human-") {
		return strings.TrimPrefix(token, "human-"), true
	}
	return "", false
}

// IsMCPToken：前缀为 "mcp-valid-" 的令牌代表一把有效的 MCP 令牌；其余（含 "mcp-token" 之类的随手字符串）是未知令牌。
func (f fakeAuth) IsMCPToken(token string) bool {
	return strings.HasPrefix(token, "mcp-valid-")
}

// fakeLimiter 是可观察的失败计数限流器。
type fakeLimiter struct {
	mu     sync.Mutex
	fails  map[string]int
	locked map[string]time.Time
}

func newFakeLimiter() *fakeLimiter {
	return &fakeLimiter{fails: map[string]int{}, locked: map[string]time.Time{}}
}

func (l *fakeLimiter) CheckLocked(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if until, ok := l.locked[key]; ok && time.Now().Before(until) {
		return true, time.Until(until)
	}
	return false, 0
}

func (l *fakeLimiter) RecordFailure(key string, max int, lockout time.Duration) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fails[key]++
	if l.fails[key] >= max {
		l.locked[key] = time.Now().Add(lockout)
		return true, lockout
	}
	return false, 0
}

func (l *fakeLimiter) RecordSuccess(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, key)
	delete(l.locked, key)
}

// testClock 是可手动推进的时钟，用于验证停滞判定等依赖时间的逻辑。
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func newTestClock() *testClock { return &testClock{t: time.Now()} }

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type testEnv struct {
	t       *testing.T
	engine  *gin.Engine
	hub     *Hub
	srv     *httptest.Server
	store   *store.Store
	creds   *fakeCreds
	limiter *fakeLimiter
	clock   *testClock

	readTimeout  time.Duration
	writeTimeout time.Duration
	useTLS       bool
	compress     bool       // 拨号时提出 permessage-deflate（伪 Agent 与浏览器两侧都提；默认不提，覆盖旧客户端）
	browserWire  *wireCount // 非空时统计 dialBrowser 建立的连接在 socket 上实际传输的字节数（仅明文连接）

	serial      bool          // withSerial / withLogCapture：这个环境所在的测试不并行（见 newTestEnv）
	captureLogs bool          // withLogCapture：把全局日志输出收进 logs
	logs        *bytes.Buffer // captureLogs 时非空：环境存活期间全局日志的全部输出
}

// sub 返回绑定到子测试 t 的环境副本：辅助函数里的 Fatal 必须作用在正在运行的（子）测试上。
func (e *testEnv) sub(t *testing.T) *testEnv {
	c := *e
	c.t = t
	return &c
}

type envOption func(*Options, *testEnv)

// withHTTPTimeouts 在测试服务器上设置 http.Server 的读 / 写超时，用于验证 WebSocket 不受其影响。
func withHTTPTimeouts(read, write time.Duration) envOption {
	return func(_ *Options, e *testEnv) { e.readTimeout, e.writeTimeout = read, write }
}

// withTLS 让测试服务器以自签名 TLS 启动（用于验证 Agent 的证书指纹固定）。
func withTLS() envOption { return func(_ *Options, e *testEnv) { e.useTLS = true } }

// withCompression 让伪 Agent 与测试里的浏览器拨号时都协商 permessage-deflate（见 compress_test.go）。
func withCompression() envOption { return func(_ *Options, e *testEnv) { e.compress = true } }

func withMode(m Mode) envOption { return func(o *Options, _ *testEnv) { o.Mode = m } }
func withJWTSecret(s string) envOption {
	return func(o *Options, _ *testEnv) { o.JWTSecret = s }
}
func withOrigins(list ...string) envOption {
	return func(o *Options, _ *testEnv) { o.AllowedOrigins = list }
}
func withDiscoveryAlways() envOption {
	return func(o *Options, _ *testEnv) { o.DiscoveryAlways = true }
}
func withHandshakeTimeout(d time.Duration) envOption {
	return func(o *Options, _ *testEnv) { o.HandshakeTimeout = d }
}
func withOpenTimeout(d time.Duration) envOption {
	return func(o *Options, _ *testEnv) { o.OpenTimeout = d }
}
func withAttachTimeout(d time.Duration) envOption {
	return func(o *Options, _ *testEnv) { o.AttachTimeout = d }
}
func withFakeClock() envOption {
	return func(o *Options, e *testEnv) { o.Now = e.clock.Now }
}
func withUnlockTTL(idle, abs time.Duration) envOption {
	return func(o *Options, _ *testEnv) { o.UnlockIdle, o.UnlockAbsolute = idle, abs }
}

// withSerial 让这个环境所在的测试保持串行：测试会改进程级的共享状态时传它。串行的测试在整包的串行阶段
// 就跑完，不会与任何并行测试重叠（见 newTestEnv）。改包级变量请用 overrideGlobal，它自己会钉成串行。
func withSerial() envOption { return func(_ *Options, e *testEnv) { e.serial = true } }

// withLogCapture 把全局日志输出收进 env.logs（环境结束时还原），用于断言「日志里没有泄露机密」。
// 全局日志是进程级共享的，所以它同时意味着串行。
func withLogCapture() envOption {
	return func(_ *Options, e *testEnv) { e.serial, e.captureLogs = true, true }
}

// 并行与串行的登记（#200）。三张表都用 sync.Map：并行阶段会有很多测试同时读写。
var (
	serialNames  sync.Map // 测试全名 -> struct{}：声明了串行的测试（子测试继承，见 isSerial）
	parallelTops sync.Map // 顶层测试名 -> struct{}：它自己或它的某个子测试已经转入并行
	parallelTs   sync.Map // *testing.T -> struct{}：这个测试已调用过 t.Parallel()（只能调一次，再调会 panic）
)

func topTestName(t *testing.T) string {
	top, _, _ := strings.Cut(t.Name(), "/")
	return top
}

// isSerial 报告 t 或它的任一祖先是否声明了串行。
func isSerial(t *testing.T) bool {
	name := t.Name()
	for {
		if _, ok := serialNames.Load(name); ok {
			return true
		}
		i := strings.LastIndex(name, "/")
		if i < 0 {
			return false
		}
		name = name[:i]
	}
}

// markSerial 声明这个测试要改进程级的共享状态（包级变量、全局日志输出），必须保持串行：串行的测试在整包的
// 串行阶段就跑完，不会与任何并行测试重叠。它的子测试也串行。
// 必须在这个测试（以及它所在的顶层测试）第一次转入并行之前调用，否则直接失败——「先并行、后改全局」是
// 必然出问题的顺序，宁可大声失败也不能悄悄串扰别的测试。
func markSerial(t *testing.T) {
	t.Helper()
	if _, par := parallelTs.Load(t); par {
		t.Fatal("markSerial 必须在这个测试第一次 newTestEnv 之前调用：测试已经转入并行，再改全局状态会串扰别的测试")
	}
	if _, par := parallelTops.Load(topTestName(t)); par {
		t.Fatalf("顶层测试 %s 里已有并行的部分，不能再声明串行：把需要串行的环境放进只含串行用例的顶层测试", topTestName(t))
	}
	name := t.Name()
	if _, seen := serialNames.LoadOrStore(name, struct{}{}); !seen {
		t.Cleanup(func() { serialNames.Delete(name) })
	}
}

// overrideGlobal 临时改一个包级变量（测试结束后还原），并把这个测试钉成串行：包级变量是进程共享的，
// 并行的测试会看到被改过的值。要在这个测试第一次 newTestEnv 之前调用。
func overrideGlobal[V any](t *testing.T, p *V, v V) {
	t.Helper()
	markSerial(t)
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

// serialRequested 预演一遍选项，只为知道这个环境是否要求串行。预演没有副作用：选项只是给 Options / testEnv
// 的字段赋值。
func serialRequested(opts []envOption) bool {
	probe := &testEnv{clock: &testClock{}}
	var o Options
	for _, apply := range opts {
		apply(&o, probe)
	}
	return probe.serial
}

// parallelUnlessSerial 让 t 并行（除非它或它的祖先声明了串行）。同一个测试里多次建环境时只调一次 t.Parallel()。
func parallelUnlessSerial(t *testing.T) {
	t.Helper()
	if isSerial(t) {
		return
	}
	if _, again := parallelTs.LoadOrStore(t, struct{}{}); again {
		return
	}
	t.Cleanup(func() { parallelTs.Delete(t) })
	parallelTops.Store(topTestName(t), struct{}{})
	t.Parallel()
}

// newTestEnv 搭一套进程内的 Hub + SQLite + 测试服务器。
//
// 并行（#200）：默认让调用它的测试（或子测试）并行。整包在 -race 下的耗时主要是「每个用例新建一套环境」
// 的固定开销——store.New 的建表建索引约 0.7 秒 CPU——而用例之间没有共享状态（各有各的内存库、测试服务器与
// 随机端口），所以把建环境放进并行阶段，整包耗时约等于总 CPU 时间除以核数。会改进程级共享状态的测试要保持
// 串行：改包级变量用 overrideGlobal，断言全局日志用 withLogCapture()，其余传 withSerial()。Go 的测试框架只在
// 所有串行顶层测试跑完之后才放行停在 t.Parallel() 的测试，串行的测试因此不会与任何并行测试重叠。
//
// 写并行友好的测试要记住：并行的子测试要等父测试函数返回后才执行——父函数里 defer 的资源在子测试跑之前就已
// 释放（用 t.Cleanup），父函数里排在 t.Run 之后的语句会先于子测试执行（别在那里断言子测试的结果），子测试
// 闭包也不要写闭包外的变量。newTestEnv 返回之前，测试可能已停在 t.Parallel() 里好几分钟：别在它之前取
// time.Now() 当计时起点或期限基准。要回到逐个顺序执行（排查疑似串扰）：go test -parallel 1。
func newTestEnv(t *testing.T, opts ...envOption) *testEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)

	// 先决定串行还是并行，再建环境：并行的测试会停在 t.Parallel() 里直到串行阶段结束，停下的这段时间不能算进
	// 环境——假时钟按创建那一刻的真实时间定锚，先建后停会让它比真实时间落后几分钟（下载写超时之类基于它
	// 算出的期限因此早已过期）。同理，并行要放在 store.New 之前：它才是最贵的一步。
	if serialRequested(opts) {
		markSerial(t)
	}
	parallelUnlessSerial(t)

	e := &testEnv{
		t:       t,
		creds:   &fakeCreds{user: testUser, pass: testPassword},
		limiter: newFakeLimiter(),
		clock:   newTestClock(),
	}
	o := Options{
		Auth:          fakeAuth{e.creds},
		Limiter:       e.limiter,
		JWTSecret:     strongSecret,
		WatchInterval: 20 * time.Millisecond,
	}
	for _, apply := range opts {
		apply(&o, e)
	}
	if e.captureLogs {
		e.logs = &bytes.Buffer{}
		prev := log.Writer()
		log.SetOutput(e.logs)
		t.Cleanup(func() { log.SetOutput(prev) })
	}

	st, err := store.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	e.store = st
	if o.Store == nil {
		o.Store = st
	}
	hub, err := NewHub(o)
	if err != nil {
		t.Fatal(err)
	}
	e.hub = hub

	engine := gin.New()
	hub.Mount(engine)
	e.engine = engine
	e.srv = httptest.NewUnstartedServer(engine)
	if e.readTimeout > 0 {
		e.srv.Config.ReadTimeout = e.readTimeout
	}
	if e.writeTimeout > 0 {
		e.srv.Config.WriteTimeout = e.writeTimeout
	}
	if e.useTLS {
		e.srv.StartTLS()
	} else {
		e.srv.Start()
	}
	hub.Start()

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		hub.Shutdown(ctx)
		e.srv.CloseClientConnections()
		e.srv.Close()
		_ = st.Close()
	})
	return e
}

// ---------- REST 辅助 ----------

type apiResult struct {
	Status int
	Body   map[string]any
	Raw    string
}

func (r apiResult) str(key string) string {
	if v, ok := r.Body[key].(string); ok {
		return v
	}
	return ""
}

// call 发送一个 REST 请求。headers 以「名, 值」成对给出。
func (e *testEnv) call(method, path string, body any, headers ...string) apiResult {
	e.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			e.t.Fatal(err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, e.srv.URL+path, rdr)
	if err != nil {
		e.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	client := http.DefaultClient
	if e.useTLS {
		client = e.srv.Client() // 信任测试服务器的自签名证书
	}
	resp, err := client.Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	res := apiResult{Status: resp.StatusCode, Raw: string(raw)}
	_ = json.Unmarshal(raw, &res.Body)
	return res
}

func humanAuth(name string) string { return "Bearer human-" + name }

// unlock 用正确密码换取解锁令牌。
func (e *testEnv) unlock(subject string) string {
	e.t.Helper()
	res := e.call("POST", "/api/v1/terminal/unlock", map[string]string{"password": testPassword}, "Authorization", humanAuth(subject))
	if res.Status != 200 || res.str("token") == "" {
		e.t.Fatalf("解锁失败: %d %s", res.Status, res.Raw)
	}
	return res.str("token")
}

// admin 返回携带登录与解锁凭据的请求头。
func (e *testEnv) admin() []string {
	return []string{"Authorization", humanAuth(testUser), unlockHeader, e.unlock(testUser)}
}

func (e *testEnv) openDiscovery(adminHdr []string) {
	e.t.Helper()
	res := e.call("POST", "/api/v1/terminal/discovery", map[string]int{"minutes": 5}, adminHdr...)
	if res.Status != 200 {
		e.t.Fatalf("打开发现窗口失败: %d %s", res.Status, res.Raw)
	}
}

// ---------- 假 Agent ----------

type fakeSession struct {
	info termproto.SessionInfo
	buf  []byte
}

type fakeRecord struct {
	data []byte
	info json.RawMessage
}

type fakeChan struct {
	sid   string
	acked uint64
}

type fakeAgent struct {
	t      *testing.T
	env    *testEnv
	priv   ed25519.PrivateKey
	pub    ed25519.PublicKey
	code   string
	ws     *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc

	welcome termproto.Welcome
	// enableBody 是管理员启用这台主机时 REST 返回的响应体（enrollAgent / enrollAgentWith 填充）。
	enableBody map[string]any
	// wire 统计这条 Agent→Hub 连接在 socket 上实际传输的字节数；ext 是握手响应里协商出的扩展（compress_test.go 用）。
	wire *wireCount
	ext  string

	mu          sync.Mutex
	sessions    map[string]*fakeSession
	order       []string
	chans       map[uint32]*fakeChan
	policies    []termproto.Policy
	uninstalls  []termproto.Uninstall        // 收到的卸载请求
	upgrades    []termproto.Upgrade          // 收到的 Web 升级请求
	offers      []termproto.AutoUpgradeOffer // 收到的自动升级回答
	offerRaw    []string                     // 它们的原始 JSON（验证回答里没有多余字段）
	grants      []termproto.AutoUpgradeGrant // 收到的静默升级批准 / 拒绝
	grantRaw    []string                     // 它们的原始 JSON（验证没有多余字段、没有泄露别的东西）
	frameOrder  []string                     // uninstall / policy 帧到达的先后（验证 Hub 先下发卸载、再下发关闭策略）
	approvedPol termproto.Policy             // 批准时（approved 帧）随带的策略
	rejected    bool
	inputs      [][]byte
	acks        []uint64
	resizes     []termproto.Resize
	attaches    []termproto.Attach
	detaches    []uint32

	// 记录相关的假实现：records 以会话 ID 为键保存「假记录内容」（Hub 对内容不透明，任意字节即可）。
	records   map[string]*fakeRecord
	recReqs   []termproto.RecordRead
	recStats  []termproto.RecordStat
	opens     []termproto.Open
	lifecycle []termproto.SetLifecycle
	recFail   string // 非空时对 record_read / record_stat 回复 record_fail
	recSilent bool   // 为 true 时不回复记录请求（模拟卡死的 Agent）

	refuseOpen   string // 非空时对 open 回复 open_failed
	refuseCode   string // 与 refuseOpen 同时使用：open_failed 里带的稳定失败码（SSH 网关用）
	silentOpen   bool   // 为 true 时不回复 open（模拟卡死的 Agent）
	refuseAttach string // 非空时对 attach 回复 attach_failed

	// extra 在 handle 没有匹配到已知类型时被调用，让专项测试（如模型执行）挂接自己的主机行为。
	extra func(t string, data []byte)

	approved chan struct{}
	done     chan struct{}
	closeErr error
}

func newFakeIdentity(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func (e *testEnv) agentURL() string {
	return "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/api/v1/terminal/agent/ws"
}

func (e *testEnv) browserURL() string {
	return "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/api/v1/terminal/ws"
}

// dialAgent 建立连接并完成握手（挑战应答）。握手过程中若服务端直接拒绝，返回 (nil, err, resp)。
func (e *testEnv) dialAgent(priv ed25519.PrivateKey, meta termproto.Meta, header http.Header) (*fakeAgent, *http.Response, error) {
	e.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	dopts := &websocket.DialOptions{
		Subprotocols: []string{termproto.SubprotocolAgent},
		HTTPHeader:   header,
	}
	var wire *wireCount
	if !e.useTLS { // TLS 场景由其它用例单独处理证书；这里只在明文连接上统计字节
		wire = &wireCount{}
		dopts.HTTPClient = countingHTTPClient(wire)
	}
	if e.compress {
		dopts.CompressionMode = websocket.CompressionContextTakeover
	}
	ws, resp, err := websocket.Dial(ctx, e.agentURL(), dopts)
	if err != nil {
		cancel()
		return nil, resp, err
	}
	ws.SetReadLimit(1 << 20)
	pub := priv.Public().(ed25519.PublicKey)
	a := &fakeAgent{
		t: e.t, env: e, priv: priv, pub: pub, code: termproto.MachineCode(pub),
		wire: wire, ext: resp.Header.Get("Sec-WebSocket-Extensions"),
		ws: ws, ctx: ctx, cancel: cancel,
		sessions: map[string]*fakeSession{},
		chans:    map[uint32]*fakeChan{},
		approved: make(chan struct{}, 1),
		done:     make(chan struct{}),
	}
	return a, resp, nil
}

// handshake 读取挑战并发送签名后的 hello，随后读取 welcome。返回读取 welcome 时的错误（例如被服务端以关闭码拒绝）。
func (a *fakeAgent) handshake(meta termproto.Meta) error {
	a.t.Helper()
	ctx, cancel := context.WithTimeout(a.ctx, 5*time.Second)
	defer cancel()

	_, data, err := a.ws.Read(ctx)
	if err != nil {
		return err
	}
	var ch termproto.Challenge
	if err := json.Unmarshal(data, &ch); err != nil || ch.T != termproto.TChallenge {
		a.t.Fatalf("期望 challenge，得到 %s", data)
	}
	nonce, err := termproto.DecodeKey(ch.Nonce, 32)
	if err != nil {
		a.t.Fatal(err)
	}
	hello := termproto.Hello{
		T: termproto.THello, V: termproto.ProtocolVersion,
		Pub:  termproto.EncodeKey(a.pub),
		Sig:  termproto.EncodeKey(termproto.SignChallenge(a.priv, nonce)),
		Meta: meta,
	}
	return a.sendHelloAndWelcome(ctx, hello)
}

func (a *fakeAgent) sendHelloAndWelcome(ctx context.Context, hello termproto.Hello) error {
	b, _ := json.Marshal(hello)
	if err := a.ws.Write(ctx, websocket.MessageText, b); err != nil {
		return err
	}
	_, data, err := a.ws.Read(ctx)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &a.welcome); err != nil || a.welcome.T != termproto.TWelcome {
		a.t.Fatalf("期望 welcome，得到 %s", data)
	}
	return nil
}

// start 在后台处理 Hub 下发的帧（模拟一个简单的回显 shell），并在启用后上报会话清单。
func (a *fakeAgent) start() {
	go func() {
		defer close(a.done)
		if a.welcome.Status == termproto.WelcomeEnabled {
			a.sendSessions()
		}
		for {
			typ, data, err := a.ws.Read(a.ctx)
			if err != nil {
				a.mu.Lock()
				a.closeErr = err
				a.mu.Unlock()
				return
			}
			a.handle(typ, data)
		}
	}()
}

func (a *fakeAgent) send(v any) {
	b, _ := json.Marshal(v)
	ctx, cancel := context.WithTimeout(a.ctx, 5*time.Second)
	defer cancel()
	_ = a.ws.Write(ctx, websocket.MessageText, b)
}

func (a *fakeAgent) sendBinary(b []byte) {
	ctx, cancel := context.WithTimeout(a.ctx, 5*time.Second)
	defer cancel()
	_ = a.ws.Write(ctx, websocket.MessageBinary, b)
}

func (a *fakeAgent) sendSessions() {
	a.mu.Lock()
	list := make([]termproto.SessionInfo, 0, len(a.order))
	for _, sid := range a.order {
		list = append(list, a.sessions[sid].info)
	}
	a.mu.Unlock()
	a.send(termproto.Sessions{T: termproto.TSessions, Sessions: list})
}

func (a *fakeAgent) handle(typ websocket.MessageType, data []byte) {
	if typ == websocket.MessageBinary {
		ft, _ := termproto.FrameType(data)
		switch ft {
		case termproto.FrameInput:
			ch, payload, err := termproto.DecodeInput(data)
			if err != nil {
				return
			}
			cp := append([]byte(nil), payload...)
			a.mu.Lock()
			a.inputs = append(a.inputs, cp)
			c := a.chans[ch]
			a.mu.Unlock()
			if c != nil {
				a.emit(c.sid, cp) // 回显
			}
		case termproto.FrameAck:
			ch, off, err := termproto.DecodeAck(data)
			if err != nil {
				return
			}
			a.mu.Lock()
			a.acks = append(a.acks, off)
			if c := a.chans[ch]; c != nil && off > c.acked {
				c.acked = off
			}
			a.mu.Unlock()
		}
		return
	}

	t, _ := termproto.ParseType(data)
	switch t {
	case termproto.TApproved:
		var m termproto.Approved
		_ = json.Unmarshal(data, &m)
		a.mu.Lock()
		a.approvedPol = m.Policy
		a.mu.Unlock()
		select {
		case a.approved <- struct{}{}:
		default:
		}
		a.sendSessions()
	case termproto.TRejected:
		a.mu.Lock()
		a.rejected = true
		a.mu.Unlock()
	case termproto.TPolicy:
		var m termproto.PolicyMsg
		_ = json.Unmarshal(data, &m)
		a.mu.Lock()
		a.policies = append(a.policies, m.Policy)
		a.frameOrder = append(a.frameOrder, "policy")
		a.mu.Unlock()
	case termproto.TUninstall:
		var m termproto.Uninstall
		_ = json.Unmarshal(data, &m)
		a.mu.Lock()
		a.uninstalls = append(a.uninstalls, m)
		a.frameOrder = append(a.frameOrder, "uninstall")
		a.mu.Unlock()
	case termproto.TUpgrade:
		var m termproto.Upgrade
		_ = json.Unmarshal(data, &m)
		a.mu.Lock()
		a.upgrades = append(a.upgrades, m)
		a.mu.Unlock()
	case termproto.TAutoUpgradeOffer:
		var m termproto.AutoUpgradeOffer
		_ = json.Unmarshal(data, &m)
		a.mu.Lock()
		a.offers = append(a.offers, m)
		a.offerRaw = append(a.offerRaw, string(data))
		a.mu.Unlock()
	case termproto.TAutoUpgradeGrant:
		var m termproto.AutoUpgradeGrant
		_ = json.Unmarshal(data, &m)
		a.mu.Lock()
		a.grants = append(a.grants, m)
		a.grantRaw = append(a.grantRaw, string(data))
		a.mu.Unlock()
	case termproto.TOpen:
		var m termproto.Open
		_ = json.Unmarshal(data, &m)
		a.onOpen(m)
	case termproto.TRecordRead:
		var m termproto.RecordRead
		_ = json.Unmarshal(data, &m)
		a.onRecordRead(m)
	case termproto.TRecordStat:
		var m termproto.RecordStat
		_ = json.Unmarshal(data, &m)
		a.onRecordStat(m)
	case termproto.TAttach:
		var m termproto.Attach
		_ = json.Unmarshal(data, &m)
		a.onAttach(m)
	case termproto.TDetach:
		var m termproto.Detach
		_ = json.Unmarshal(data, &m)
		a.mu.Lock()
		delete(a.chans, m.Ch)
		a.detaches = append(a.detaches, m.Ch)
		a.mu.Unlock()
	case termproto.TResize:
		var m termproto.Resize
		_ = json.Unmarshal(data, &m)
		a.mu.Lock()
		a.resizes = append(a.resizes, m)
		a.mu.Unlock()
	case termproto.TKill:
		var m termproto.Kill
		_ = json.Unmarshal(data, &m)
		a.kill(m.SID)
	case termproto.TLifecycle:
		var m termproto.SetLifecycle
		_ = json.Unmarshal(data, &m)
		a.mu.Lock()
		a.lifecycle = append(a.lifecycle, m)
		if s := a.sessions[m.SID]; s != nil {
			s.info.IdleOwn = m.IdleSec != nil
			if m.IdleSec != nil {
				s.info.Idle = *m.IdleSec
			} else {
				s.info.Idle = 0
			}
		}
		a.mu.Unlock()
		a.sendSessions()
	case termproto.TRename:
		var m termproto.Rename
		_ = json.Unmarshal(data, &m)
		a.mu.Lock()
		if s := a.sessions[m.SID]; s != nil {
			s.info.Name = m.Name
		}
		a.mu.Unlock()
		a.sendSessions()
	default:
		if a.extra != nil {
			a.extra(t, data)
		}
	}
}

func (a *fakeAgent) onOpen(m termproto.Open) {
	a.mu.Lock()
	a.opens = append(a.opens, m)
	refuse, refuseCode, silent := a.refuseOpen, a.refuseCode, a.silentOpen
	a.mu.Unlock()
	if silent {
		return
	}
	if refuse != "" {
		a.send(termproto.OpenFailed{T: termproto.TOpenFailed, Req: m.Req, Reason: refuse, Code: refuseCode})
		return
	}
	sid := randHex(16)
	rows, cols := termproto.ClampSize(m.Rows, m.Cols)
	info := termproto.SessionInfo{SID: sid, Name: m.Name, Rows: rows, Cols: cols, State: termproto.SessionRunning, Shell: "/bin/sh", PID: 4242, Created: time.Now().Unix()}
	if m.SSH != nil {
		// 模拟网关上的会话守护进程：SSH 会话没有本地进程，Shell 记为 ssh，并指回目标主机。
		info.Shell, info.PID, info.Target = "ssh", 0, m.SSH.ID
	}
	if m.Incognito {
		info.Rec = termproto.RecIncognito
	} else {
		info.Rec = termproto.RecOn
	}
	a.mu.Lock()
	a.sessions[sid] = &fakeSession{info: info}
	a.order = append(a.order, sid)
	a.mu.Unlock()
	a.send(termproto.Opened{T: termproto.TOpened, Req: m.Req, Session: info})
	a.sendSessions()
}

func (a *fakeAgent) onAttach(m termproto.Attach) {
	a.mu.Lock()
	a.attaches = append(a.attaches, m)
	s := a.sessions[m.SID]
	if a.refuseAttach != "" {
		reason := a.refuseAttach
		a.mu.Unlock()
		a.send(termproto.AttachFailed{T: termproto.TAttachFailed, Ch: m.Ch, SID: m.SID, Reason: reason})
		return
	}
	if s == nil {
		a.mu.Unlock()
		a.send(termproto.AttachFailed{T: termproto.TAttachFailed, Ch: m.Ch, SID: m.SID, Reason: "no such session"})
		return
	}
	from := m.Since
	end := uint64(len(s.buf))
	if from > end {
		from = end
	}
	a.chans[m.Ch] = &fakeChan{sid: m.SID, acked: from}
	replay := append([]byte(nil), s.buf[from:]...)
	info := s.info
	a.mu.Unlock()

	a.send(termproto.Attached{
		T: termproto.TAttached, Ch: m.Ch, SID: m.SID, Base: 0, End: end, From: from,
		Rows: info.Rows, Cols: info.Cols, State: info.State,
	})
	for off := 0; off < len(replay); {
		n := len(replay) - off
		if n > termproto.MaxDataPayload {
			n = termproto.MaxDataPayload
		}
		a.sendBinary(termproto.EncodeOutput(m.Ch, from+uint64(off), replay[off:off+n]))
		off += n
	}
}

// emit 让会话产生输出：追加到缓冲，并按偏移发给该会话上所有已附着的通道。
func (a *fakeAgent) emit(sid string, data []byte) {
	a.mu.Lock()
	s := a.sessions[sid]
	if s == nil {
		a.mu.Unlock()
		return
	}
	off := uint64(len(s.buf))
	s.buf = append(s.buf, data...)
	s.info.End = uint64(len(s.buf))
	var targets []uint32
	for ch, c := range a.chans {
		if c.sid == sid {
			targets = append(targets, ch)
		}
	}
	a.mu.Unlock()

	for _, ch := range targets {
		for p := 0; p < len(data); {
			n := len(data) - p
			if n > termproto.MaxDataPayload {
				n = termproto.MaxDataPayload
			}
			a.sendBinary(termproto.EncodeOutput(ch, off+uint64(p), data[p:p+n]))
			p += n
		}
	}
}

func (a *fakeAgent) kill(sid string) {
	a.mu.Lock()
	s := a.sessions[sid]
	if s == nil {
		a.mu.Unlock()
		return
	}
	code := 137
	var targets []uint32
	for ch, c := range a.chans {
		if c.sid == sid {
			targets = append(targets, ch)
		}
	}
	delete(a.sessions, sid)
	for i, v := range a.order {
		if v == sid {
			a.order = append(a.order[:i], a.order[i+1:]...)
			break
		}
	}
	a.mu.Unlock()

	a.send(termproto.Exit{T: termproto.TExit, SID: sid, Code: &code})
	for _, ch := range targets {
		a.send(termproto.Detached{T: termproto.TDetached, Ch: ch, Reason: "killed"})
		a.mu.Lock()
		delete(a.chans, ch)
		a.mu.Unlock()
	}
	a.sendSessions()
}

func (a *fakeAgent) close() {
	a.cancel()
	_ = a.ws.CloseNow()
}

// waitClosed 等待读循环结束并返回关闭状态码（0 表示非关闭帧导致的结束）。
func (a *fakeAgent) waitClosed(timeout time.Duration) websocket.StatusCode {
	a.t.Helper()
	select {
	case <-a.done:
	case <-time.After(timeout):
		a.t.Fatal("等待 Agent 连接被关闭超时")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return websocket.CloseStatus(a.closeErr)
}

// connectEnabled 让一个（已在库中启用的）身份上线并启动后台处理。
func (e *testEnv) connectEnabled(priv ed25519.PrivateKey, meta termproto.Meta) *fakeAgent {
	e.t.Helper()
	a, _, err := e.dialAgent(priv, meta, nil)
	if err != nil {
		e.t.Fatalf("拨号失败: %v", err)
	}
	if err := a.handshake(meta); err != nil {
		e.t.Fatalf("握手失败: %v", err)
	}
	if a.welcome.Status != termproto.WelcomeEnabled {
		e.t.Fatalf("期望 enabled，得到 %s", a.welcome.Status)
	}
	a.start()
	e.t.Cleanup(a.close)
	return a
}

// enrollAgent 走完整流程：打开发现窗口 → Agent 进入待启用 → 管理员启用 → Agent 在线。
func (e *testEnv) enrollAgent(meta termproto.Meta, remark string) *fakeAgent {
	e.t.Helper()
	return e.enrollAgentWith(meta, map[string]any{"remark": remark})
}

// enrollAgentWith 与 enrollAgent 相同，但启用请求体可自定义（例如带 default_for_new_models）。
func (e *testEnv) enrollAgentWith(meta termproto.Meta, body map[string]any) *fakeAgent {
	e.t.Helper()
	_, priv := newFakeIdentity(e.t)
	hdr := e.admin()
	e.openDiscovery(hdr)

	a, _, err := e.dialAgent(priv, meta, nil)
	if err != nil {
		e.t.Fatalf("拨号失败: %v", err)
	}
	if err := a.handshake(meta); err != nil {
		e.t.Fatalf("握手失败: %v", err)
	}
	if a.welcome.Status != termproto.WelcomePending {
		e.t.Fatalf("期望 pending，得到 %s", a.welcome.Status)
	}
	a.start()
	e.t.Cleanup(a.close)

	eventually(e.t, 2*time.Second, "待启用列表出现该主机", func() bool {
		res := e.call("GET", "/api/v1/terminal/hosts", nil, "Authorization", humanAuth(testUser))
		pend, _ := res.Body["pending"].([]any)
		for _, p := range pend {
			if m, ok := p.(map[string]any); ok && m["machine_code"] == a.code {
				return true
			}
		}
		return false
	})
	res := e.call("POST", "/api/v1/terminal/hosts/"+a.code+"/enable", body, hdr...)
	if res.Status != 200 {
		e.t.Fatalf("启用失败: %d %s", res.Status, res.Raw)
	}
	a.enableBody = res.Body
	select {
	case <-a.approved:
	case <-time.After(3 * time.Second):
		e.t.Fatal("Agent 未收到 approved")
	}
	eventually(e.t, 2*time.Second, "Hub 登记为在线", func() bool { return e.hub.onlineAgent(a.code) != nil })
	return a
}

// openSession 通过 REST 新建会话并返回 sid。
func (e *testEnv) openSession(a *fakeAgent, hdr []string, name string) string {
	e.t.Helper()
	res := e.call("POST", "/api/v1/terminal/hosts/"+a.code+"/sessions", map[string]any{"name": name, "rows": 30, "cols": 100}, hdr...)
	if res.Status != 200 {
		e.t.Fatalf("新建会话失败: %d %s", res.Status, res.Raw)
	}
	sess, _ := res.Body["session"].(map[string]any)
	sid, _ := sess["sid"].(string)
	if !termproto.ValidSessionID(sid) {
		e.t.Fatalf("返回的 sid 非法: %v", sess)
	}
	return sid
}

// ticket 签发一次性终端票据。
func (e *testEnv) ticket(a *fakeAgent, sid string, hdr []string, since uint64) string {
	e.t.Helper()
	res := e.call("POST", "/api/v1/terminal/hosts/"+a.code+"/sessions/"+sid+"/ticket", map[string]any{"since": since, "rows": 40, "cols": 120}, hdr...)
	if res.Status != 200 || res.str("ticket") == "" {
		e.t.Fatalf("签发票据失败: %d %s", res.Status, res.Raw)
	}
	return res.str("ticket")
}

// dialBrowser 用票据拨号终端 WebSocket。
func (e *testEnv) dialBrowser(ticket string, header http.Header) (*websocket.Conn, *http.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dopts := &websocket.DialOptions{
		Subprotocols: []string{termproto.SubprotocolBrowser, termproto.TicketSubprotocolPrefix + ticket},
		HTTPHeader:   header,
	}
	if e.useTLS {
		dopts.HTTPClient = e.srv.Client() // 信任测试服务器的自签名证书
	} else if e.browserWire != nil {
		dopts.HTTPClient = countingHTTPClient(e.browserWire)
	}
	if e.compress {
		dopts.CompressionMode = websocket.CompressionContextTakeover
	}
	ws, resp, err := websocket.Dial(ctx, e.browserURL(), dopts)
	if ws != nil {
		ws.SetReadLimit(1 << 20) // Go 客户端默认只收 32 KiB 的消息；浏览器没有这个限制
	}
	return ws, resp, err
}

// readReady 读取浏览器侧的第一条消息并要求它是 ready。
func readReady(t *testing.T, ws *websocket.Conn) termproto.BrowserReady {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	typ, data, err := ws.Read(ctx)
	if err != nil {
		t.Fatalf("读取 ready 失败: %v", err)
	}
	if typ != websocket.MessageText {
		t.Fatalf("ready 应为文本帧")
	}
	var r termproto.BrowserReady
	if err := json.Unmarshal(data, &r); err != nil || r.T != termproto.TReady {
		t.Fatalf("期望 ready，得到 %s", data)
	}
	return r
}

// readOutput 持续读取直到收集到至少 n 字节输出，返回累计内容与最后偏移。
func readOutput(t *testing.T, ws *websocket.Conn, want int) (data []byte, firstOffset uint64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	first := true
	for len(data) < want {
		typ, msg, err := ws.Read(ctx)
		if err != nil {
			t.Fatalf("读取输出失败（已收 %d/%d 字节）: %v", len(data), want, err)
		}
		if typ != websocket.MessageBinary {
			continue // 控制帧（exit 等）
		}
		off, payload, err := termproto.DecodeBrowserOutput(msg)
		if err != nil {
			t.Fatal(err)
		}
		if first {
			firstOffset = off
			first = false
		}
		data = append(data, payload...)
	}
	return data, firstOffset
}

func writeAck(t *testing.T, ws *websocket.Conn, offset uint64) {
	t.Helper()
	b, _ := json.Marshal(termproto.BrowserAck{T: termproto.TAck, Offset: offset})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := ws.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatalf("发送 ack 失败: %v", err)
	}
}

// eventually 在期限内轮询条件。
func eventually(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待超时：%s", what)
}

func testMeta(host string) termproto.Meta {
	return termproto.Meta{Host: host, OS: "linux", Arch: "amd64", Ver: "1.7.0-test", Priv: termproto.PrivRoot, Kind: termproto.KindPersistent}
}

// ---------- 假 Agent：终端记录 ----------

// setRecord 为会话准备假的记录内容与索引描述。
func (a *fakeAgent) setRecord(sid string, content []byte, info string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.records == nil {
		a.records = map[string]*fakeRecord{}
	}
	a.records[sid] = &fakeRecord{data: append([]byte(nil), content...), info: json.RawMessage(info)}
}

func (a *fakeAgent) onRecordRead(m termproto.RecordRead) {
	a.mu.Lock()
	a.recReqs = append(a.recReqs, m)
	silent, fail := a.recSilent, a.recFail
	rec := a.records[m.SID]
	a.mu.Unlock()
	if silent {
		return
	}
	if fail != "" {
		a.send(termproto.RecordFail{T: termproto.TRecordFail, Req: m.Req, Reason: fail})
		return
	}
	if rec == nil {
		a.send(termproto.RecordFail{T: termproto.TRecordFail, Req: m.Req, Reason: termproto.RecReasonNotFound})
		return
	}
	max := m.Max
	if max <= 0 {
		max = termproto.DefaultRecordRead
	}
	start := int(m.Off)
	if start > len(rec.data) {
		start = len(rec.data)
	}
	end := start + max
	if end > len(rec.data) {
		end = len(rec.data)
	}
	seg := m.Seg
	if seg == 0 {
		seg = 1
	}
	if start == end {
		a.sendBinary(termproto.EncodeRecord(termproto.RecordChunk{
			Req: m.Req, Flags: termproto.RecFlagEOF | termproto.RecFlagLast, Seg: seg, Off: uint32(start), NextSeg: seg, NextOff: uint32(end),
		}))
		return
	}
	for p := start; p < end; {
		n := end - p
		if n > termproto.MaxRecordFrameData {
			n = termproto.MaxRecordFrameData
		}
		var flags byte
		if p+n == end {
			flags |= termproto.RecFlagLast
			if end == len(rec.data) {
				flags |= termproto.RecFlagEOF
			}
		}
		a.sendBinary(termproto.EncodeRecord(termproto.RecordChunk{
			Req: m.Req, Flags: flags, Seg: seg, Off: uint32(p), NextSeg: seg, NextOff: uint32(p + n), Data: rec.data[p : p+n],
		}))
		p += n
	}
}

func (a *fakeAgent) onRecordStat(m termproto.RecordStat) {
	a.mu.Lock()
	a.recStats = append(a.recStats, m)
	silent, fail := a.recSilent, a.recFail
	rec := a.records[m.SID]
	a.mu.Unlock()
	if silent {
		return
	}
	if fail != "" {
		a.send(termproto.RecordFail{T: termproto.TRecordFail, Req: m.Req, Reason: fail})
		return
	}
	if rec == nil {
		a.send(termproto.RecordFail{T: termproto.TRecordFail, Req: m.Req, Reason: termproto.RecReasonNotFound})
		return
	}
	a.send(termproto.RecordInfo{T: termproto.TRecordInfo, Req: m.Req, SID: m.SID, Info: rec.info})
}

// callRaw 发送请求并返回原始响应（记录内容接口返回的是二进制而不是 JSON）。
func (e *testEnv) callRaw(method, path string, headers ...string) (int, http.Header, []byte) {
	e.t.Helper()
	req, err := http.NewRequest(method, e.srv.URL+path, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, raw
}
