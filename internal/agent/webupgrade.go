package agent

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// Web 一键升级的 Agent 侧（方案见 .cursor/planning/2026-10-08-agent-web-upgrade-plan.md）。
//
// 这里只做「判断能不能升、下载、校验、交接」；真正替换程序、重启服务、等新版本上线、必要时回滚，
// 由 Handoff 启动的升级进程完成（cli 包的 `upgrade --from … --auto-rollback`），因为替换会停止 Agent 自己。
//
// 几条不变量：
//   - 本地开关（Allowed）是主机所有者的决定，Hub 改不了；每次请求都现读，下载完成交接前再读一次；
//   - 下载地址只由 Agent 自己配置的服务端地址与自己的平台推出，帧里没有 URL / 文件名；
//   - 不比当前新的版本一律不升（Hub 回退过、或重复点击）；
//   - 任何一步失败都不改动已安装的程序，并把原因报给 Hub。

// SelfUpgrade 是 Agent 响应 Hub 的 upgrade 帧所需的本机能力与策略。nil 表示这个 Agent 不支持（不声明能力）。
type SelfUpgrade struct {
	// Allowed 报告主机所有者是否允许从 Web 升级（每次现读，不缓存）。
	Allowed func() bool
	// StageDir 是下载新版本的目录；不存在会被创建（0700）。
	StageDir string
	// Handoff 把已校验的新程序交给一个与 Agent 进程树无关的升级进程，由它替换程序并重启服务。
	// 返回 nil 表示升级进程已经启动，Agent 随后会被重启；返回错误表示什么都没有发生。
	Handoff func(ctx context.Context, newBinary, version string) error
	// Note 返回上一次升级留下的结果，握手时带给 Hub；可为 nil。
	Note func() *termproto.UpgradeNote
	// Connected 在每次与 Hub 握手成功时调用，参数是本进程的版本号；可为 nil。
	// 升级进程靠它留下的「上线标记」判断新版本是否真的连上了 Hub。
	Connected func(version string)
	// AutoMode 报告主机所有者在本机为「自动升级」选的模式（termproto.AutoUpgradeOff / Notify / Silent），每次现读；
	// nil 表示这个 Agent 不提供自动升级，握手里不声明。
	AutoMode func() string
	// AutoChecked 在每次自动升级检查得出结论时调用，把结果交给本机保存（供 `auto-upgrade status` 展示）；可为 nil。
	AutoChecked func(AutoCheckNote)
	// AutoSettings 现读主机所有者对自动升级的完整设置（模式、时间段、钉住的公钥）；第二个返回值为 false 表示没有同意。
	// nil 表示这个 Agent 读不到设置：静默模式不会安装任何东西（只做提示）。
	AutoSettings func() (AutoSettings, bool)
	// AutoAttempts 返回本机记录的静默升级尝试；AutoAttempted 记一次「对这个版本开始静默升级」（要在下载之前写好）。
	// 任何一个为 nil 都意味着无法记录尝试：静默模式不会安装任何东西。
	AutoAttempts  func() AutoAttempts
	AutoAttempted func(version string) error

	// 以下供测试注入，零值取默认。
	HTTPClient      func() (*http.Client, error)                           // 默认：按 Agent 的信任配置构造
	Platform        func() (goos, goarch string)                           // 默认：runtime.GOOS / GOARCH
	Verify          func(ctx context.Context, file string) (string, error) // 默认：运行 `<file> version` 取版本号
	MaxBytes        int64                                                  // 默认 256 MiB
	DownloadTimeout time.Duration                                          // 默认 5 分钟
	Now             func() time.Time                                       // 默认 time.Now
}

const (
	defaultUpgradeMaxBytes = 256 << 20
	defaultUpgradeTimeout  = 5 * time.Minute
	// upgradeHandoffHold 是交接之后「已经有一次升级在进行」的保持时间：升级进程需要这么久来替换、重启与等新版本上线。
	upgradeHandoffHold = 10 * time.Minute
)

// upgradeGateInterval 是检查本地开关有没有变化的间隔；变化后 Agent 重新握手，Hub 随即看到新的能力。
// 是变量只为了让测试不必真等几秒。
var upgradeGateInterval = 5 * time.Second

// state 是握手时声明的能力。
func (s *SelfUpgrade) state() string {
	if s == nil {
		return ""
	}
	if s.Allowed != nil && s.Allowed() {
		return termproto.WebUpgradeOn
	}
	return termproto.WebUpgradeOff
}

// autoState 是握手时声明的「自动升级」模式；不提供自动升级时为空。读到不认识的值按关闭处理。
func (s *SelfUpgrade) autoState() string {
	if s == nil || s.AutoMode == nil {
		return ""
	}
	if mode := s.AutoMode(); termproto.ValidAutoUpgradeMode(mode) {
		return mode
	}
	return termproto.AutoUpgradeOff
}

// autoSettings 现读自动升级的完整设置；没有提供读取方式或没有同意时第二个返回值为 false。
func (s *SelfUpgrade) autoSettings() (AutoSettings, bool) {
	if s == nil || s.AutoSettings == nil {
		return AutoSettings{}, false
	}
	return s.AutoSettings()
}

// upgradeGates 是握手里声明的本地开关（Web 一键升级、自动升级，以及静默升级的时间段）。
// 声明与稍后监视开关变化用的是同一个值，所以握手期间开关变了也不会漏掉。
type upgradeGates struct {
	web    string // termproto.WebUpgradeOn / Off / ""
	auto   string // termproto.AutoUpgradeNotify / Silent / Off / ""
	window string // 静默升级的时间段原文（HH:MM-HH:MM），只在静默模式下有；仅供 Hub 展示
}

func (s *SelfUpgrade) gates() upgradeGates {
	g := upgradeGates{web: s.state(), auto: s.autoState()}
	if g.auto == termproto.AutoUpgradeSilent {
		if set, ok := s.autoSettings(); ok && set.Problem == "" {
			g.window = set.WindowText
		}
	}
	return g
}

func (s *SelfUpgrade) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// platform 是这台主机的平台标识（GOOS-GOARCH，如 linux-amd64），与发布签名里绑定的平台同一写法。
func (s *SelfUpgrade) platform() string {
	goos, goarch := runtime.GOOS, runtime.GOARCH
	if s.Platform != nil {
		goos, goarch = s.Platform()
	}
	return goos + "-" + goarch
}

var upgradeAssetRe = regexp.MustCompile(`^relaymesh-agent-(linux|darwin|windows)-(amd64|arm64)(\.exe)?$`)

// assetName 是这个平台的 Agent 二进制在服务端分发目录里的文件名；没有对应构建的平台返回 false。
func (s *SelfUpgrade) assetName() (string, bool) {
	goos, goarch := runtime.GOOS, runtime.GOARCH
	if s.Platform != nil {
		goos, goarch = s.Platform()
	}
	name := "relaymesh-agent-" + goos + "-" + goarch
	if goos == "windows" {
		name += ".exe"
	}
	return name, upgradeAssetRe.MatchString(name)
}

// upgrader 执行升级请求。每个 Agent 一个：「正在进行」的状态要跨越 WebSocket 重连保留。
type upgrader struct {
	cfg     *SelfUpgrade
	version string
	server  ServerTarget
	trust   TrustOptions
	logf    func(string, ...any)

	mu          sync.Mutex
	running     bool
	handoffTime time.Time
}

func newUpgrader(cfg *SelfUpgrade, version string, server ServerTarget, trust TrustOptions, logf func(string, ...any)) *upgrader {
	return &upgrader{cfg: cfg, version: version, server: server, trust: trust, logf: logf}
}

// upgradeFail 是升级过程中的失败：Code 是给 Hub 的机器可读代码，Detail 是给人看的说明。
type upgradeFail struct {
	stage  string
	code   string
	detail string
}

func (e *upgradeFail) Error() string { return e.code + ": " + e.detail }

func refuse(code, detail string) *upgradeFail {
	return &upgradeFail{stage: termproto.UpgradeStageRefused, code: code, detail: detail}
}

func fail(code, detail string) *upgradeFail {
	return &upgradeFail{stage: termproto.UpgradeStageFailed, code: code, detail: detail}
}

// begin 登记「开始一次升级」；已经有一次在进行（或刚交接不久）时返回 false。
func (u *upgrader) begin() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.running || (!u.handoffTime.IsZero() && u.cfg.now().Sub(u.handoffTime) < upgradeHandoffHold) {
		return false
	}
	u.running = true
	return true
}

func (u *upgrader) end(handedOff bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.running = false
	if handedOff {
		u.handoffTime = u.cfg.now()
	}
}

// upgradeGuard 是一次升级在「放行条件」上与其他来源不同的部分。同一条下载、校验、交接的链路服务两种来源：
// 管理员在 Web 上点的「一键升级」（信任 Hub 给出的 SHA-256），和 Agent 自己发起的静默自动升级（无人值守，
// 必须再验过独立的发布签名）。差异都收在这里，链路本身只有一份。
type upgradeGuard struct {
	// label 在日志里说明这是哪一种升级。
	label string
	// allowed 现读本机是否仍然允许这一种升级（precheck 与交接前各看一次，所有者随时可以收回）。
	allowed func() bool
	// notAllowedMsg / revokedMsg 是 allowed 为 false 时给 Hub 的说明（开始前 / 升级过程中被收回）。
	notAllowedMsg, revokedMsg string
	// requireOlder 为 true 时，只有能确定本机版本更旧才升级。静默升级是无人值守的，不拿无法比较新旧的构建去升级；
	// Web 一键升级是管理员亲手点的，允许「版本不同但无法比较」。
	requireOlder bool
	// authenticate 非 nil 时，在下载并核对 SHA-256 之后、运行新程序之前调用：返回错误就丢弃下载物，不运行、不交接。
	authenticate func(file string) error
	// started 非 nil 时，在占到「进行中」名额之后、开始下载之前调用：返回错误就放弃这次升级（静默升级在这里先记一次尝试）。
	started func() error
}

// webGuard 是 Web 一键升级的放行条件：看 web-upgrade 开关，信任 Hub 给出的 SHA-256。
func (u *upgrader) webGuard() upgradeGuard {
	return upgradeGuard{
		label:         "Web 升级请求",
		allowed:       func() bool { return u.cfg.Allowed != nil && u.cfg.Allowed() },
		notAllowedMsg: "这台主机没有允许从 Web 升级：请由主机所有者在本机执行 relaymesh-agent web-upgrade on",
		revokedMsg:    "升级过程中，主机所有者关闭了「允许从 Web 升级」，已放弃",
	}
}

// run 处理一次 Web 一键升级请求，进展通过 report 报给 Hub。它会阻塞到交接完成或失败，调用方应在单独的 goroutine 里运行。
func (u *upgrader) run(ctx context.Context, m termproto.Upgrade, report func(termproto.UpgradeStatus)) {
	u.runGuarded(ctx, m, report, u.webGuard())
}

// upgradeResult 是一次升级请求走完链路之后的结果。
type upgradeResult int

const (
	upgradeNotDone     upgradeResult = iota // 没有交接：被拒绝或失败了（原因已经报告给 Hub）
	upgradeHandedOff                        // 已交接给升级进程；升级成功时 Agent 会被重启，所以正常情况下不会有人读到这个结果
	upgradeApplyFailed                      // 已交接，但升级进程没有完成替换或重启（Agent 一直活着）
)

// runGuarded 按 g 的放行条件处理一次升级请求，返回走完链路的结果。
func (u *upgrader) runGuarded(ctx context.Context, m termproto.Upgrade, report func(termproto.UpgradeStatus), g upgradeGuard) upgradeResult {
	send := func(stage, code, detail string) {
		report(termproto.UpgradeStatus{T: termproto.TUpgradeStatus, ID: m.ID, Stage: stage, Code: code, Detail: detail})
	}
	reject := func(err error) {
		var f *upgradeFail
		if !errors.As(err, &f) {
			f = fail(termproto.UpgradeCodeHandoff, err.Error())
		}
		u.logf("%s没有完成（%s）: %s", g.label, f.code, f.detail)
		send(f.stage, f.code, f.detail)
	}

	if err := u.precheck(m, g); err != nil {
		reject(err)
		return upgradeNotDone
	}
	// 只有真正占到「进行中」名额的请求才有资格在结束时释放它：被拒绝的重复请求不能把别人的名额清掉。
	if !u.begin() {
		reject(refuse(termproto.UpgradeCodeBusy, "已经有一次升级在进行，请稍后再看"))
		return upgradeNotDone
	}
	finished := false
	finish := func(handedOff bool) {
		if !finished {
			finished = true
			u.end(handedOff)
		}
	}
	defer finish(false)
	if g.started != nil {
		if err := g.started(); err != nil {
			reject(refuse(termproto.UpgradeCodeRecord, "无法在本机记录这次升级尝试，已放弃（数据目录不可写？）: "+err.Error()))
			return upgradeNotDone
		}
	}
	if err := u.perform(ctx, m, send, g); err != nil {
		reject(err)
		return upgradeNotDone
	}
	handoffAt := u.cfg.now()
	finish(true)
	if u.watchOutcome(ctx, handoffAt, send) {
		return upgradeApplyFailed
	}
	return upgradeHandedOff
}

// upgradeOutcomePoll 是交接之后查看升级进程结果的间隔。变量只为让测试不必真等。
var upgradeOutcomePoll = 2 * time.Second

// watchOutcome 在交接之后盯着升级进程留下的结果。升级成功时 Agent 会被重启，这个协程随进程一起消失；
// 它只为一种情况存在：升级进程没能完成替换或重启（磁盘满、权限不对……），Agent 因此一直活着——
// 这时立刻把原因告诉 Hub 并放开「已有升级在进行」的限制，而不是让管理员干等到超时。
//
// 返回 true 表示升级进程没有完成（报告了失败，或等满了保持时间 Agent 仍然活着）；其余情况（成功、连接结束、
// 没有办法观察）返回 false。
func (u *upgrader) watchOutcome(ctx context.Context, handoffAt time.Time, send func(stage, code, detail string)) (applyFailed bool) {
	if u.cfg.Note == nil {
		return false
	}
	since := handoffAt.Unix()
	deadline := time.Now().Add(upgradeHandoffHold)
	t := time.NewTicker(upgradeOutcomePoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
		}
		if time.Now().After(deadline) {
			return true
		}
		n := u.cfg.Note()
		if n == nil || n.At < since {
			continue // 还没有结果，或是上一次升级留下的旧结果
		}
		if n.Outcome == termproto.UpgradeOutcomeOK {
			return false
		}
		u.release()
		detail := n.Detail
		if detail == "" {
			detail = "升级进程没有完成替换或重启，请查看主机上的日志"
		}
		u.logf("升级进程没有完成（%s）: %s", n.Outcome, detail)
		send(termproto.UpgradeStageFailed, termproto.UpgradeCodeApply, detail)
		return true
	}
}

// release 放开「刚交接过」的限制：升级进程已经明确失败，管理员可以马上重试。
func (u *upgrader) release() {
	u.mu.Lock()
	u.handoffTime = time.Time{}
	u.mu.Unlock()
}

// precheck 是不需要占用「进行中」名额的前置判断：字段合规、本地开关、版本关系、本机能力。
func (u *upgrader) precheck(m termproto.Upgrade, g upgradeGuard) error {
	if err := m.Validate(); err != nil {
		return refuse(termproto.UpgradeCodeBadRequest, err.Error())
	}
	if g.allowed == nil || !g.allowed() {
		return refuse(termproto.UpgradeCodeNotAllowed, g.notAllowedMsg)
	}
	rel := termproto.RelateAgentVersion(u.version, m.Version)
	if rel == termproto.VersionCurrent || (g.requireOlder && rel != termproto.VersionOlder) {
		return refuse(termproto.UpgradeCodeUpToDate, fmt.Sprintf("当前版本 %s 不比目标版本 %s 旧，不需要升级", u.version, m.Version))
	}
	if u.cfg.Handoff == nil || u.cfg.StageDir == "" {
		return refuse(termproto.UpgradeCodeNotSupported, "这个 Agent 不是以可升级的常驻服务运行")
	}
	return nil
}

// perform 下载、校验并交接。返回 nil 表示升级进程已经启动。
func (u *upgrader) perform(ctx context.Context, m termproto.Upgrade, send func(stage, code, detail string), g upgradeGuard) error {
	u.logf("收到%s：%s → %s，正在下载并校验…", g.label, u.version, m.Version)
	send(termproto.UpgradeStageDownloading, "", "")

	file, err := u.download(ctx, m)
	if err != nil {
		return err
	}
	// 先验签、再执行：下一步会运行新程序，没有通过独立验证的程序绝不能被运行。
	if g.authenticate != nil {
		if aerr := g.authenticate(file); aerr != nil {
			_ = os.Remove(file)
			return fail(termproto.UpgradeCodeSignature, "发布签名验证失败，已丢弃下载的程序: "+aerr.Error())
		}
	}
	if got, verr := u.verify(ctx, file); verr != nil {
		_ = os.Remove(file)
		return fail(termproto.UpgradeCodeVerify, "新程序无法运行: "+verr.Error())
	} else if got != m.Version {
		_ = os.Remove(file)
		return fail(termproto.UpgradeCodeVerify, fmt.Sprintf("新程序报出的版本是 %q，不是期望的 %q，已放弃", got, m.Version))
	}
	// 下载与校验期间所有者可能已经收回了授权：交接前再看一次。
	if !g.allowed() {
		_ = os.Remove(file)
		return refuse(termproto.UpgradeCodeNotAllowed, g.revokedMsg)
	}

	// 先告知 Hub 再交接：交接成功后 Agent 很快就会被重启，来不及再报。交接失败时紧接着会再发一条 failed。
	send(termproto.UpgradeStageHandoff, "", "")
	if err := u.cfg.Handoff(ctx, file, m.Version); err != nil {
		_ = os.Remove(file)
		return fail(termproto.UpgradeCodeHandoff, "无法启动升级进程: "+err.Error())
	}
	u.logf("新版本 %s 已校验，升级进程已启动：Agent 即将重启", m.Version)
	return nil
}

func (u *upgrader) client() (*http.Client, error) {
	if u.cfg.HTTPClient != nil {
		return u.cfg.HTTPClient()
	}
	return u.trust.HTTPClient(u.server)
}

// download 下载目标平台的 Agent 二进制到暂存目录，边下边算 SHA-256，与 Hub 在帧里给出的值一致才算数。
func (u *upgrader) download(ctx context.Context, m termproto.Upgrade) (string, error) {
	name, ok := u.cfg.assetName()
	if !ok {
		return "", fail(termproto.UpgradeCodeNotSupported, "这个平台没有可下载的 Agent 构建")
	}
	base, err := u.server.HTTPBase()
	if err != nil {
		return "", fail(termproto.UpgradeCodeDownload, err.Error())
	}
	client, err := u.client()
	if err != nil {
		return "", fail(termproto.UpgradeCodeDownload, err.Error())
	}
	timeout := u.cfg.DownloadTimeout
	if timeout <= 0 {
		timeout = defaultUpgradeTimeout
	}
	maxBytes := u.cfg.MaxBytes
	if maxBytes <= 0 {
		maxBytes = defaultUpgradeMaxBytes
	}
	client.Timeout = timeout
	// 下载地址只能是自己配置的服务端：不跟随重定向，免得被带到别处（信任配置是按服务端地址设定的）。
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	dctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(dctx, http.MethodGet, base+"/i/"+m.Ticket+"/"+name, nil)
	if err != nil {
		return "", fail(termproto.UpgradeCodeDownload, err.Error())
	}
	req.Header.Set("User-Agent", "relaymesh-agent/"+u.version)
	resp, err := client.Do(req)
	if err != nil {
		return "", fail(termproto.UpgradeCodeDownload, "连接服务端失败: "+rootCause(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fail(termproto.UpgradeCodeDownload, fmt.Sprintf("服务端返回 HTTP %d（升级窗口可能已经关闭）", resp.StatusCode))
	}
	if resp.ContentLength > maxBytes {
		return "", fail(termproto.UpgradeCodeDownload, fmt.Sprintf("文件大小 %d 超过上限 %d", resp.ContentLength, maxBytes))
	}

	if err := os.MkdirAll(u.cfg.StageDir, 0o700); err != nil {
		return "", fail(termproto.UpgradeCodeDownload, "无法创建暂存目录: "+err.Error())
	}
	sweepStaleDownloads(u.cfg.StageDir, u.cfg.now())
	tmp, err := os.CreateTemp(u.cfg.StageDir, ".download-*")
	if err != nil {
		return "", fail(termproto.UpgradeCodeDownload, "无法创建暂存文件: "+err.Error())
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		_ = tmp.Close()
		cleanup()
		return "", fail(termproto.UpgradeCodeDownload, "下载中断: "+rootCause(err))
	}
	if n > maxBytes {
		_ = tmp.Close()
		cleanup()
		return "", fail(termproto.UpgradeCodeDownload, fmt.Sprintf("文件超过上限 %d 字节", maxBytes))
	}
	if n == 0 {
		_ = tmp.Close()
		cleanup()
		return "", fail(termproto.UpgradeCodeDownload, "下载到的文件是空的")
	}
	if got := hex.EncodeToString(h.Sum(nil)); subtle.ConstantTimeCompare([]byte(got), []byte(m.SHA256)) != 1 {
		_ = tmp.Close()
		cleanup()
		return "", fail(termproto.UpgradeCodeChecksum, "下载内容的 SHA-256 与服务端给出的不一致，已丢弃")
	}
	if err := tmp.Chmod(0o700); err != nil {
		_ = tmp.Close()
		cleanup()
		return "", fail(termproto.UpgradeCodeDownload, err.Error())
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return "", fail(termproto.UpgradeCodeDownload, err.Error())
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return "", fail(termproto.UpgradeCodeDownload, err.Error())
	}
	final := filepath.Join(u.cfg.StageDir, stagedBinaryName)
	if err := os.Rename(tmpName, final); err != nil {
		cleanup()
		return "", fail(termproto.UpgradeCodeDownload, err.Error())
	}
	return final, nil
}

// staleDownloadAge 是遗留的下载临时文件被清掉的年龄：下载最多 5 分钟，超过一小时的一定是上次崩溃留下的。
const staleDownloadAge = time.Hour

// sweepStaleDownloads 清掉暂存目录里崩溃遗留的下载临时文件，免得它们（每个可能几十 MB）无限累积。
func sweepStaleDownloads(dir string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), ".download-") {
			continue
		}
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) < staleDownloadAge {
			continue
		}
		_ = os.Remove(filepath.Join(dir, e.Name()))
	}
}

// verify 运行 `<file> version`，返回新程序自报的版本号：确认它在这台机器上跑得起来，并且就是期望的版本。
func (u *upgrader) verify(ctx context.Context, file string) (string, error) {
	if u.cfg.Verify != nil {
		return u.cfg.Verify(ctx, file)
	}
	return runVersion(ctx, file)
}

func runVersion(ctx context.Context, file string) (string, error) {
	vctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(vctx, file, "version")
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin"} // 新程序只需要打印版本号，不继承 Agent 的环境
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return parseVersionOutput(string(out))
}

// parseVersionOutput 取出 `RelayMesh Agent <版本>` 里的版本号。
func parseVersionOutput(out string) (string, error) {
	f := strings.Fields(out)
	if len(f) != 3 || f[0] != "RelayMesh" || f[1] != "Agent" {
		return "", fmt.Errorf("version 的输出不认识: %q", strings.TrimSpace(out))
	}
	return f[2], nil
}

// watchUpgradeGate 在连接期间盯着本地开关（Web 一键升级与自动升级）：它们变了就让 Agent 重新握手，
// Hub 随即看到新的能力（所有者不必重启服务）。
//
// 检查间隔由调用方在启动本协程之前取定（而不是在协程里读 upgradeGateInterval）：它是测试会改写的包级变量，
// 在协程里读会与测试结束时的还原构成数据竞争。
func (a *Agent) watchUpgradeGate(ctx context.Context, interval time.Duration, announced upgradeGates, changed func()) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if a.opts.SelfUpgrade.gates() != announced {
				changed()
				return
			}
		}
	}
}
