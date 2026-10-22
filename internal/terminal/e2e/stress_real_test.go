//go:build e2e && !windows

package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 压测（AC-P21，方案第 17 节）：8 个并发运行各自持续高输出 60 秒，同时保持两路往返探针
// （interactive_feedback 写路径、list_sessions 读路径）和一个 Web 查看者订阅事件通道。
// 它较重（约 2 分钟），需要显式设置 RM_E2E_STRESS=1。

// rssKiB 读取进程的常驻内存（KiB）。
func rssKiB(pid int) int64 {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return -1
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			f := strings.Fields(line)
			if len(f) >= 2 {
				n, _ := strconv.ParseInt(f[1], 10, 64)
				return n
			}
		}
	}
	return -1
}

// dirBytes 统计目录下所有文件的总字节数。
func dirBytes(dir string) int64 {
	var n int64
	_ = filepath.Walk(dir, func(_ string, fi os.FileInfo, err error) error {
		if err == nil && fi.Mode().IsRegular() {
			n += fi.Size()
		}
		return nil
	})
	return n
}

// dbFileBytes 是 Hub 的 SQLite 主文件与 WAL 的总字节数。
func dbFileBytes(h *hub) int64 {
	var n int64
	files, _ := filepath.Glob(filepath.Join(h.dir, "relaymesh.db*"))
	for _, f := range files {
		if fi, err := os.Stat(f); err == nil {
			n += fi.Size()
		}
	}
	return n
}

type latSample struct {
	at     time.Time
	d      time.Duration
	failed bool
}

// prober 周期性地做一次调用并记下耗时，之后按时间窗口取分位数。
type prober struct {
	name    string
	mu      sync.Mutex
	samples []latSample
}

// loop 周期性地调用 call；call 返回要记录的耗时（由调用方决定统计哪一段）和错误。
func (p *prober) loop(ctx context.Context, every time.Duration, call func() (time.Duration, error)) {
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		start := time.Now()
		d, err := call()
		s := latSample{at: start, d: d, failed: err != nil}
		p.mu.Lock()
		p.samples = append(p.samples, s)
		p.mu.Unlock()
	}
}

func (p *prober) window(from, to time.Time) (ds []time.Duration, failures int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, s := range p.samples {
		if s.at.Before(from) || !s.at.Before(to) {
			continue
		}
		if s.failed {
			failures++
			continue
		}
		ds = append(ds, s.d)
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	return ds, failures
}

func pct(ds []time.Duration, q float64) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	i := int(float64(len(ds)-1) * q)
	return ds[i]
}

// mcpToolOnce 做一次 MCP 工具调用，只返回错误，可在非测试协程里安全使用。
func mcpToolOnce(hc *http.Client, base, token string, id int, name string, args map[string]any) error {
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args},
	})
	req, err := http.NewRequest("POST", base+"/mcp", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var rpc struct {
		Error  *struct{ Message string } `json:"error"`
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &rpc); err != nil {
		return err
	}
	if rpc.Error != nil || rpc.Result.IsError {
		return fmt.Errorf("工具返回错误: %s", lastN(string(raw), 160))
	}
	return nil
}

// feedbackRoundTrip 走完一次真实的反馈往返：模型调用 interactive_feedback 后挂起等待，人在 Web 提交，
// 模型的调用随之返回。耗时只统计「人点提交」到「模型调用返回」这一段，这是用户感知到的延迟。
func feedbackRoundTrip(a *api, hc *http.Client, token string, n int) (time.Duration, error) {
	wf := "stress-probe-" + strconv.Itoa(n)
	done := make(chan error, 1)
	go func() {
		done <- mcpToolOnce(hc, a.base, token, n, "interactive_feedback", map[string]any{
			"workflow_id": wf, "project_directory": "/stress", "title": "压测探针",
			"summary": "压测探针 " + strconv.Itoa(n),
		})
	}()
	var sid string
	for deadline := time.Now().Add(10 * time.Second); sid == "" && time.Now().Before(deadline); {
		var out struct {
			Sessions []struct {
				ID     string `json:"session_id"`
				Status string `json:"status"`
			} `json:"sessions"`
		}
		if c := a.raw("GET", "/api/v1/workflows/"+wf+"/sessions", nil, &out, false); c == 200 {
			for _, s := range out.Sessions {
				if s.Status == "pending" {
					sid = s.ID
				}
			}
		}
		if sid == "" {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if sid == "" {
		return 0, fmt.Errorf("会话没有出现")
	}
	start := time.Now()
	if c := a.raw("POST", "/api/v1/sessions/"+sid+"/submit", map[string]any{"response_text": "探针回复"}, nil, false); c != 200 {
		return 0, fmt.Errorf("提交失败: HTTP %d", c)
	}
	if err := <-done; err != nil {
		return 0, err
	}
	return time.Since(start), nil
}

// consumeExecEvents 像一个打开着模型线程页的 Web 查看者那样订阅事件通道，只数事件与字节。
func consumeExecEvents(ctx context.Context, a *api, events, bytesRead *atomic.Int64) {
	req, err := http.NewRequestWithContext(ctx, "GET", a.base+"/api/v1/terminal/exec/events", nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+a.jwt)
	req.Header.Set("X-Terminal-Unlock", a.unlock)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		line := sc.Text()
		bytesRead.Add(int64(len(line) + 1))
		if strings.HasPrefix(line, "data:") {
			events.Add(1)
		}
	}
}

// holdStalledEvents 订阅事件通道后一个字节也不读，直到 ctx 结束：模拟网络很慢或页面被挂起的查看者。
func holdStalledEvents(ctx context.Context, a *api) {
	req, err := http.NewRequestWithContext(ctx, "GET", a.base+"/api/v1/terminal/exec/events", nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+a.jwt)
	req.Header.Set("X-Terminal-Unlock", a.unlock)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	<-ctx.Done()
}

func TestRealStressEightHighOutputRuns(t *testing.T) {
	if os.Getenv("RM_E2E_STRESS") == "" {
		t.Skip("压测较重（约 2 分钟），设置 RM_E2E_STRESS=1 才运行")
	}
	l := newExecLab(t, execOn())
	hubPID, holderPID, agentPID := l.h.proc.pid(), l.hp.pid(), l.ag.pid()

	// 探针令牌：只有治理类权限。
	var cred struct {
		Token string `json:"token"`
	}
	if c := l.h.api.raw("POST", "/api/v1/credentials", map[string]any{
		"name": "probe", "permissions": map[string]any{"feedback": true, "sessions": true},
	}, &cred, true); c != 200 || cred.Token == "" {
		t.Fatalf("创建探针令牌失败: HTTP %d", c)
	}

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	hc := &http.Client{Timeout: 30 * time.Second}
	var seq atomic.Int64
	feedback := &prober{name: "interactive_feedback 往返"}
	reads := &prober{name: "list_sessions"}
	go feedback.loop(ctx, 150*time.Millisecond, func() (time.Duration, error) {
		return feedbackRoundTrip(l.h.api, hc, cred.Token, int(seq.Add(1)))
	})
	go reads.loop(ctx, 100*time.Millisecond, func() (time.Duration, error) {
		start := time.Now()
		err := mcpToolOnce(hc, l.h.base(), cred.Token, int(seq.Add(1)), "list_sessions", map[string]any{"project_directory": "/stress"})
		return time.Since(start), err
	})

	// 预热 + 基线。
	time.Sleep(5 * time.Second)
	baseFrom := time.Now()
	time.Sleep(20 * time.Second)
	baseTo := time.Now()
	rssBase := rssKiB(hubPID)
	dbBase := dbFileBytes(l.h)
	dataBase := dirBytes(l.d.data)

	// 加载：Web 查看者（一个正常、一个停住不读）+ 8 个并发高输出运行。
	// 对照组（RM_E2E_STRESS_CONTROL=1）：不经过 RelayMesh，直接在本机起 8 个同样的 yes，
	// 用来区分「探针变慢是 CPU 被抢」还是「Hub 的开销」。
	control := os.Getenv("RM_E2E_STRESS_CONTROL") != ""
	var events, evBytes atomic.Int64
	marker := "STRESSMARK" + randHex(6)
	yesLine := marker + "-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
	var waitAll func() map[string]string
	loadFrom := time.Now()
	if control {
		var cmds []*exec.Cmd
		for i := 0; i < 8; i++ {
			cmd := exec.Command("timeout", "60", "yes", yesLine)
			if err := cmd.Start(); err != nil {
				t.Fatalf("起对照进程失败: %v", err)
			}
			cmds = append(cmds, cmd)
		}
		waitAll = func() map[string]string {
			out := map[string]string{}
			for i, c := range cmds {
				_ = c.Wait()
				out["control-"+strconv.Itoa(i)] = "done"
			}
			return out
		}
	} else {
		go consumeExecEvents(ctx, l.h.api, &events, &evBytes)
		go holdStalledEvents(ctx, l.h.api)
		time.Sleep(500 * time.Millisecond)
		script := "timeout 60 yes '" + yesLine + "'"
		runs := make([]string, 0, 8)
		for i := 0; i < 8; i++ {
			res := l.mcp.exec(l.code, script, map[string]any{"wait_seconds": 0, "timeout_seconds": 120, "description": fmt.Sprintf("压测 %d", i)})
			if res.str("run_id") == "" || res.str("state") != "running" {
				t.Fatalf("第 %d 个运行应处于 running: %v", i, res)
			}
			runs = append(runs, res.str("run_id"))
		}
		waitAll = func() map[string]string {
			// 等所有运行结束（timeout 60 会在 60 秒时让 yes 以 124 退出）。
			deadline := time.Now().Add(110 * time.Second)
			states := map[string]string{}
			for time.Now().Before(deadline) && len(states) < len(runs) {
				for _, id := range runs {
					if _, done := states[id]; done {
						continue
					}
					res, isErr := l.mcp.run("status", id, map[string]any{"wait_seconds": 5})
					if isErr {
						t.Errorf("查询运行 %s 失败: %v", id, res)
						return states
					}
					if st := res.str("state"); st != "running" {
						states[id] = st
					}
				}
			}
			return states
		}
	}

	var peak, holderPeak, agentPeak int64
	sampleDone := make(chan struct{})
	go func() {
		defer close(sampleDone)
		tick := time.NewTicker(500 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
			if v := rssKiB(hubPID); v > atomic.LoadInt64(&peak) {
				atomic.StoreInt64(&peak, v)
			}
			if v := rssKiB(holderPID); v > atomic.LoadInt64(&holderPeak) {
				atomic.StoreInt64(&holderPeak, v)
			}
			if v := rssKiB(agentPID); v > atomic.LoadInt64(&agentPeak) {
				atomic.StoreInt64(&agentPeak, v)
			}
		}
	}()

	states := waitAll()
	loadTo := time.Now()
	if len(states) != 8 {
		t.Fatalf("运行没有在期限内全部结束: %v", states)
	}
	ran := loadTo.Sub(loadFrom)
	if ran < 55*time.Second {
		t.Fatalf("高输出运行应持续约 60 秒，实际只有 %s", ran)
	}
	t.Logf("8 个运行都已结束，用时 %s，终态 %v", ran.Round(time.Second), states)

	// 恢复期。
	time.Sleep(10 * time.Second)
	recFrom := loadTo
	recTo := time.Now()
	rssAfter := rssKiB(hubPID)
	dbAfter := dbFileBytes(l.h)
	dataAfter := dirBytes(l.d.data)
	stop()
	<-sampleDone

	// 探针数据。
	report := func(p *prober) (baseP95, loadP95 time.Duration) {
		bd, bf := p.window(baseFrom, baseTo)
		ld, lf := p.window(loadFrom, loadTo)
		rd, rf := p.window(recFrom, recTo)
		t.Logf("探针 %-20s 基线 n=%d p50=%s p95=%s max=%s 失败=%d | 加载 n=%d p50=%s p95=%s max=%s 失败=%d | 恢复 n=%d p95=%s 失败=%d",
			p.name, len(bd), pct(bd, 0.5), pct(bd, 0.95), pct(bd, 1), bf,
			len(ld), pct(ld, 0.5), pct(ld, 0.95), pct(ld, 1), lf,
			len(rd), pct(rd, 0.95), rf)
		if bf+lf+rf > 0 {
			t.Errorf("探针 %s 出现失败调用: 基线 %d 加载 %d 恢复 %d", p.name, bf, lf, rf)
		}
		if len(bd) < 50 || len(ld) < 150 {
			t.Errorf("探针 %s 样本太少: 基线 %d 加载 %d", p.name, len(bd), len(ld))
		}
		return pct(bd, 0.95), pct(ld, 0.95)
	}
	for _, p := range []*prober{feedback, reads} {
		b95, l95 := report(p)
		// 阈值：相对基线劣化 < 20%；基线只有几毫秒时，再给 5 毫秒的测量噪声余量。
		limit := b95 + b95/5 + 5*time.Millisecond
		if l95 > limit {
			if control {
				t.Logf("（对照组）探针 %s 的 p95 从 %s 变为 %s", p.name, b95, l95)
			} else {
				t.Errorf("探针 %s 的 p95 在加载下从 %s 变为 %s，超过阈值 %s", p.name, b95, l95, limit)
			}
		}
	}

	// 内存与存储。
	t.Logf("Hub 常驻内存: 基线 %d MiB，加载峰值 %d MiB（增量 %d MiB），恢复后 %d MiB", rssBase/1024, peak/1024, (peak-rssBase)/1024, rssAfter/1024)
	t.Logf("守护进程常驻内存峰值 %d MiB，Agent 峰值 %d MiB", holderPeak/1024, agentPeak/1024)
	t.Logf("Hub 数据库（含 WAL）: 基线 %d KiB，加载后 %d KiB（增量 %d KiB）", dbBase/1024, dbAfter/1024, (dbAfter-dbBase)/1024)
	t.Logf("主机数据目录: 基线 %d KiB，加载后 %d KiB（增量 %d MiB，每个运行最多保留头 8 MiB + 尾 256 KiB）", dataBase/1024, dataAfter/1024, (dataAfter-dataBase)/(1<<20))
	t.Logf("Web 查看者收到 %d 个事件、%d KiB", events.Load(), evBytes.Load()/1024)
	if d := peak - rssBase; d > 50*1024 {
		t.Errorf("Hub 常驻内存增量 %d MiB，超过 50 MiB 阈值", d/1024)
	}
	if control {
		t.Logf("对照组：8 个 yes 直接在本机跑，没有经过 RelayMesh；上面的探针数字就是纯 CPU 抢占造成的")
		return
	}
	if d := dbAfter - dbBase; d > 8<<20 {
		t.Errorf("Hub 数据库增长了 %d KiB：输出似乎进了数据库", d/1024)
	}
	// 每个运行在库里只有有界预览（头 4 KiB + 尾 2 KiB），输出块不会进库：标记总量远小于输出量。
	var markBytes int64
	files, _ := filepath.Glob(filepath.Join(l.h.dir, "relaymesh.db*"))
	for _, f := range files {
		if b, err := os.ReadFile(f); err == nil {
			markBytes += int64(bytes.Count(b, []byte(marker))) * 80
		}
	}
	t.Logf("Hub 数据库里能数到的输出标记约 %d KiB（只应是各运行的有界预览）", markBytes/1024)
	if markBytes > 512<<10 {
		t.Errorf("Hub 数据库里的输出内容约 %d KiB，超出了有界预览应有的量", markBytes/1024)
	}
	if lg, err := os.ReadFile(l.h.proc.log); err == nil && bytes.Contains(lg, []byte(marker)) {
		t.Errorf("高输出的内容不应出现在 Hub 日志里")
	}
	// 每个运行最多留头 8 MiB + 尾 256 KiB（两路各一份，这里只有 stdout 有内容），8 个运行的上限约 70 MiB，留余量。
	if d := dataAfter - dataBase; d > 128<<20 {
		t.Errorf("主机数据目录增长了 %d MiB，超过了头尾保留的上限", d/(1<<20))
	}
	for name, pid := range map[string]int{"Hub": hubPID, "守护进程": holderPID, "Agent": agentPID} {
		if !pidAlive(pid) {
			t.Errorf("%s 进程在压测中退出了", name)
		}
	}
}
